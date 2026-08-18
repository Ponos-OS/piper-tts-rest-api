import type { ConsumeMessage } from "amqplib";

import { createReadStream } from "node:fs";
import { Readable, Transform } from "node:stream";

import type {
  SynthesizeConfig,
  SynthesizeErrorCode,
  SynthesizeJobMessage,
} from "../shared/index.js";
import type { AmqpBroker } from "./amqp.js";

import { readPiperModelSampleRate } from "./piper.js";
import {
  createThrottledProgressReporter,
  synthesizeTextToMp3File,
} from "./synthesis.js";

const CONTENT_TYPE = "audio/mpeg";

type WorkerStatus =
  | { status: "queued" }
  | { status: "generating" | "uploading"; percent: number }
  | { status: "completed"; fileSizeBytes: number }
  | {
      status: "failed";
      failedAt: string;
      error: { code: SynthesizeErrorCode; message: string };
    };

export interface SynthesizeWorker {
  close(): Promise<void>;
}

export async function startSynthesizeWorker(
  broker: Pick<AmqpBroker, "acknowledge" | "consume" | "publish" | "reject">,
  synthesizeConfig: SynthesizeConfig,
  piperModelPath: string,
): Promise<SynthesizeWorker> {
  const sampleRate = readPiperModelSampleRate(piperModelPath);
  const activeJobs = new Map<AbortController, Promise<void>>();
  let closed = false;

  await broker.consume((message) => {
    if (!message || closed) {
      return;
    }

    const controller = new AbortController();
    const job = handleMessage(
      broker,
      message,
      synthesizeConfig,
      piperModelPath,
      sampleRate,
      controller.signal,
    ).finally(() => activeJobs.delete(controller));
    activeJobs.set(controller, job);
  });

  return {
    async close(): Promise<void> {
      closed = true;
      for (const controller of activeJobs.keys()) {
        controller.abort(new Error("Synthesize worker is shutting down"));
      }
      await Promise.allSettled(activeJobs.values());
    },
  };
}

async function handleMessage(
  broker: Pick<AmqpBroker, "acknowledge" | "publish" | "reject">,
  message: ConsumeMessage,
  config: SynthesizeConfig,
  modelPath: string,
  sampleRate: number,
  signal: AbortSignal,
): Promise<void> {
  let job: SynthesizeJobMessage;
  try {
    job = parseJob(message.content);
  } catch {
    broker.reject(message, false);
    return;
  }

  const attempt = readAttempt(message.properties.headers?.["x-attempt"]);
  try {
    await sendStatus(job, attempt, { status: "queued" }, config, signal);
    await processJob(job, attempt, config, modelPath, sampleRate, signal);
    broker.acknowledge(message);
  } catch (error) {
    if (isRetryable(error) && attempt < config.maxAttempts) {
      try {
        await publishRetry(broker, message, config, attempt, error);
        broker.acknowledge(message);
      } catch {
        broker.reject(message, true);
      }
      return;
    }

    await sendStatus(
      job,
      attempt,
      {
        status: "failed",
        failedAt: new Date().toISOString(),
        error: toStatusError(error),
      },
      config,
      signal,
    );
    broker.acknowledge(message);
  }
}

async function processJob(
  job: SynthesizeJobMessage,
  attempt: number,
  config: SynthesizeConfig,
  modelPath: string,
  sampleRate: number,
  signal: AbortSignal,
): Promise<void> {
  const synthesis = await synthesizeTextToMp3File(
    job.text,
    modelPath,
    sampleRate,
    config.wordsPerMinute,
    {
      signal,
      onProgress: (percent) => {
        void sendStatus(
          job,
          attempt,
          { status: "generating", percent },
          config,
          signal,
        );
      },
    },
  ).catch((error: unknown) => {
    throw new WorkerError("generation-failed", "Audio generation failed", {
      cause: error,
    });
  });

  try {
    const uploadUrl = await getUploadUrl(job, attempt, config, signal);
    await sendStatus(
      job,
      attempt,
      { status: "uploading", percent: 0 },
      config,
      signal,
    );
    await uploadFile(
      uploadUrl,
      synthesis.filePath,
      synthesis.fileSizeBytes,
      job,
      attempt,
      config,
      signal,
    );
    await sendStatus(
      job,
      attempt,
      { status: "completed", fileSizeBytes: synthesis.fileSizeBytes },
      config,
      signal,
    );
  } finally {
    await synthesis.cleanup();
  }
}

async function getUploadUrl(
  job: SynthesizeJobMessage,
  attempt: number,
  config: SynthesizeConfig,
  signal: AbortSignal,
): Promise<string> {
  const response = await fetchWithTimeout(
    job.genUploadUrl,
    callbackRequest(job, attempt, {
      jobId: job.jobId,
      attempt,
      contentType: CONTENT_TYPE,
    }),
    config.genUploadUrlTimeoutMs,
    signal,
  );
  if (!response.ok) {
    throw httpError("upload-url-failed", response);
  }

  const payload = (await response.json()) as { uploadUrl?: unknown };
  if (typeof payload.uploadUrl !== "string" || payload.uploadUrl === "") {
    throw new WorkerError(
      "upload-url-failed",
      "Upload URL callback returned an invalid response",
    );
  }
  return payload.uploadUrl;
}

async function uploadFile(
  uploadUrl: string,
  filePath: string,
  fileSizeBytes: number,
  job: SynthesizeJobMessage,
  attempt: number,
  config: SynthesizeConfig,
  signal: AbortSignal,
): Promise<void> {
  const controller = new AbortController();
  const abort = (): void => controller.abort(signal.reason);
  signal.addEventListener("abort", abort, { once: true });
  const deadline = setTimeout(
    () => controller.abort(new Error("Upload timed out")),
    config.uploadTimeoutMs,
  );
  let idleTimer: NodeJS.Timeout | undefined;
  const resetIdleTimer = (): void => {
    if (idleTimer) {
      clearTimeout(idleTimer);
    }
    idleTimer = setTimeout(
      () => controller.abort(new Error("Upload became idle")),
      config.uploadIdleTimeoutMs,
    );
  };

  let sent = 0;
  const report = createThrottledProgressReporter((percent) => {
    void sendStatus(
      job,
      attempt,
      { status: "uploading", percent },
      config,
      signal,
    );
  }, 500);
  const source = createReadStream(filePath);
  const counter = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      resetIdleTimer();
      sent += chunk.length;
      report(Math.min(99, Math.floor((sent / fileSizeBytes) * 100)));
      callback(null, chunk);
    },
  });

  resetIdleTimer();
  try {
    const body = Readable.toWeb(
      source.pipe(counter),
    ) as ReadableStream<Uint8Array>;
    const request: RequestInit & { duplex: "half" } = {
      method: "PUT",
      headers: {
        "Content-Length": String(fileSizeBytes),
        "Content-Type": CONTENT_TYPE,
      },
      body,
      duplex: "half",
      redirect: "manual",
      signal: controller.signal,
    };
    const response = await fetch(uploadUrl, request);
    if (!response.ok) {
      throw httpError("upload-failed", response);
    }
  } catch (error) {
    if (error instanceof WorkerError) {
      throw error;
    }
    throw new WorkerError("upload-failed", "MP3 upload failed", {
      cause: error,
    });
  } finally {
    clearTimeout(deadline);
    if (idleTimer) {
      clearTimeout(idleTimer);
    }
    signal.removeEventListener("abort", abort);
    source.destroy();
    counter.destroy();
  }
}

async function sendStatus(
  job: SynthesizeJobMessage,
  attempt: number,
  status: WorkerStatus,
  config: SynthesizeConfig,
  signal: AbortSignal,
): Promise<void> {
  try {
    await fetchWithTimeout(
      job.statusCallbackUrl,
      callbackRequest(job, attempt, { jobId: job.jobId, attempt, ...status }),
      config.statusCallbackTimeoutMs,
      signal,
    );
  } catch {
    // Status callbacks are best-effort and never change job processing.
  }
}

function callbackRequest(
  job: SynthesizeJobMessage,
  attempt: number,
  body: unknown,
): RequestInit {
  return {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": `${job.jobId}:${attempt}`,
      ...(job.callbackToken === undefined
        ? {}
        : { Authorization: job.callbackToken }),
    },
    body: JSON.stringify(body),
    redirect: "manual",
  };
}

async function fetchWithTimeout(
  url: string,
  options: RequestInit,
  timeoutMs: number,
  signal: AbortSignal,
): Promise<Response> {
  const controller = new AbortController();
  const abort = (): void => controller.abort(signal.reason);
  signal.addEventListener("abort", abort, { once: true });
  const timeout = setTimeout(
    () => controller.abort(new Error("Request timed out")),
    timeoutMs,
  );
  try {
    return await fetch(url, { ...options, signal: controller.signal });
  } finally {
    clearTimeout(timeout);
    signal.removeEventListener("abort", abort);
  }
}

async function publishRetry(
  broker: Pick<AmqpBroker, "publish">,
  message: ConsumeMessage,
  config: SynthesizeConfig,
  attempt: number,
  error: unknown,
): Promise<void> {
  const headers = {
    ...message.properties.headers,
    "x-attempt": attempt + 1,
  };
  await broker.publish({
    exchange: "",
    routingKey: config.rabbitmq.retryQueue,
    content: message.content,
    options: {
      contentType: "application/json",
      expiration: String(retryDelayMs(config.retryDelayMs, error)),
      headers,
      messageId: message.properties.messageId,
      persistent: true,
      timestamp: Math.floor(Date.now() / 1_000),
    },
  });
}

function retryDelayMs(defaultDelayMs: number, error: unknown): number {
  if (error instanceof HttpError && error.statusCode === 429) {
    return error.retryAfterMs ?? defaultDelayMs;
  }
  return defaultDelayMs;
}

function parseJob(content: Buffer): SynthesizeJobMessage {
  let value: unknown;
  try {
    value = JSON.parse(content.toString("utf8")) as unknown;
  } catch {
    throw new WorkerError("invalid-job-message", "Message is not valid JSON");
  }
  if (!isJob(value)) {
    throw new WorkerError(
      "invalid-job-message",
      "Message has an invalid job shape",
    );
  }
  return value;
}

function isJob(value: unknown): value is SynthesizeJobMessage {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const job = value as Record<string, unknown>;
  return (
    ["jobId", "text", "genUploadUrl", "statusCallbackUrl"].every(
      (key) => typeof job[key] === "string" && job[key] !== "",
    ) &&
    (job.callbackToken === undefined || typeof job.callbackToken === "string")
  );
}

function readAttempt(value: unknown): number {
  return typeof value === "number" && Number.isInteger(value) && value > 0
    ? value
    : 1;
}

function httpError(
  code: "upload-url-failed" | "upload-failed",
  response: Response,
): HttpError {
  return new HttpError(
    code,
    `HTTP ${response.status}`,
    response.status,
    response.headers.get("retry-after"),
  );
}

function isRetryable(error: unknown): boolean {
  return (
    !(error instanceof HttpError) ||
    error.statusCode === 408 ||
    error.statusCode === 429 ||
    error.statusCode >= 500
  );
}

function toStatusError(error: unknown): {
  code: SynthesizeErrorCode;
  message: string;
} {
  if (error instanceof WorkerError) {
    return { code: error.code, message: error.message };
  }
  return { code: "generation-failed", message: "Synthesis job failed" };
}

class WorkerError extends Error {
  readonly code: SynthesizeErrorCode;

  constructor(
    code: SynthesizeErrorCode,
    message: string,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "WorkerError";
    this.code = code;
  }
}

class HttpError extends WorkerError {
  readonly retryAfterMs: number | undefined;
  readonly statusCode: number;

  constructor(
    code: "upload-url-failed" | "upload-failed",
    message: string,
    statusCode: number,
    retryAfter: string | null,
  ) {
    super(code, message);
    this.statusCode = statusCode;
    this.retryAfterMs = parseRetryAfter(retryAfter);
  }
}

function parseRetryAfter(value: string | null): number | undefined {
  if (value === null) {
    return undefined;
  }
  if (/^\d+$/.test(value)) {
    return Number.parseInt(value, 10) * 1_000;
  }
  const date = Date.parse(value);
  return Number.isNaN(date) ? undefined : Math.max(0, date - Date.now());
}
