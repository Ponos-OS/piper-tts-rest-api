import type { ConsumeMessage } from "amqplib";

import { context, propagation, SpanKind, trace } from "@opentelemetry/api";
import { createReadStream } from "node:fs";
import { hostname } from "node:os";
import { basename } from "node:path";
import { Readable, Transform } from "node:stream";

import type { AmqpBroker } from "./amqp.js";

import {
  fetchWithTimeout,
  logger,
  redactPresignedUrl,
  type SynthesizeConfig,
  type SynthesizeErrorCode,
  type SynthesizeJobEvent,
  SynthesizeJobHeader,
  type SynthesizeJobMessage,
} from "../shared/index.js";
import { readPiperModelSampleRate } from "./piper.js";
import {
  createThrottledProgressReporter,
  synthesizeTextToMp3File,
} from "./synthesis.js";

const CONTENT_TYPE = "audio/mpeg";
const tracer = trace.getTracer("synthesize-worker");

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
  logger.info("Starting synthesize worker...", {
    context: "startSynthesizeWorker",
  });

  const sampleRate = readPiperModelSampleRate(piperModelPath);
  const activeJobs = new Map<AbortController, Promise<void>>();
  let closed = false;

  await broker.consume((message) => {
    if (!message || closed) {
      logger.warn(
        `Because message is ${String(message)} or closed is ${closed} we are not processing the job!`,
        { context: "startSynthesizeWorker" },
      );

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
      logger.info(`Closing synthesizer worker...`, {
        context: "startSynthesizeWorker",
      });
      closed = true;
      for (const controller of activeJobs.keys()) {
        controller.abort(new Error("Synthesize worker is shutting down"));
      }
      await Promise.allSettled(activeJobs.values());
      logger.info(`Closed synthesizer worker.`, {
        context: "startSynthesizeWorker",
      });
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
  logger.info(`Handling a synthesize message...`, {
    context: "SynthesizeWorker",
  });

  let job: SynthesizeJobMessage;

  try {
    job = parseJob(message.content);
  } catch {
    broker.reject(message, false);
    return;
  }

  const attempt = readAttempt(message.properties.headers?.["x-attempt"]);
  const headers = message.properties.headers as SynthesizeJobHeader;
  const parentContext = propagation.extract(context.active(), headers);
  const queuedAt = Number(headers.timestamp);
  const authorization = headers.authorization;
  const startedAt = performance.now();

  await tracer.startActiveSpan(
    "synthesize.consume",
    { kind: SpanKind.CONSUMER },
    parentContext,
    async (span) => {
      try {
        const queueWaitMs = Number.isFinite(queuedAt)
          ? Date.now() - queuedAt
          : undefined;

        logJob(job, attempt, modelPath, "queued", {
          queueWaitMs,
        });

        tracer.startActiveSpan("synthesize.queue_wait", (queueWaitSpan) => {
          if (queueWaitMs !== undefined) {
            queueWaitSpan.setAttribute("synthesize.queue_wait_ms", queueWaitMs);
          }
          queueWaitSpan.end();
        });
        await sendStatus(
          job,
          attempt,
          { status: "queued" },
          config,
          signal,
          authorization,
        );
        await processJob(
          job,
          attempt,
          config,
          modelPath,
          sampleRate,
          signal,
          authorization,
        );

        logJob(job, attempt, modelPath, "completed", {
          totalDurationMs: performance.now() - startedAt,
        });

        broker.acknowledge(message);
      } catch (error) {
        if (isRetryable(error) && attempt < config.maxAttempts) {
          try {
            await publishRetry(broker, message, config, attempt, error);
            logJob(job, attempt, modelPath, "retrying", {
              errorCode: toStatusError(error).code,
              errorMessage: toStatusError(error).message,
              maxAttempts: config.maxAttempts,
              retryCount: attempt,
            });
            broker.acknowledge(message);
          } catch {
            broker.reject(message, true);
          }
          return;
        }

        await tracer.startActiveSpan(
          "synthesize.terminal_failure",
          async (failureSpan) => {
            const failedAt = new Date().toISOString();
            const statusError = toStatusError(error);
            logJob(job, attempt, modelPath, "failed", {
              errorCode: statusError.code,
              errorMessage: statusError.message,
              failedAt,
              maxAttempts: config.maxAttempts,
              retryCount: attempt,
              stage: statusError.code,
              totalDurationMs: performance.now() - startedAt,
            });
            await sendStatus(
              job,
              attempt,
              {
                status: "failed",
                failedAt,
                error: statusError,
              },
              config,
              signal,
              authorization,
            );
            failureSpan.end();
          },
        );
        broker.acknowledge(message);
      } finally {
        span.end();
      }
    },
  );
}

async function processJob(
  job: SynthesizeJobMessage,
  attempt: number,
  config: SynthesizeConfig,
  modelPath: string,
  sampleRate: number,
  signal: AbortSignal,
  authorization?: string,
): Promise<void> {
  logger.info(`Processing ${job.jobId} (attempt #${attempt})`, {
    context: "processJob",
  });

  const synthesis = await tracer.startActiveSpan(
    "synthesize.generation",
    async (span) => {
      const value = await synthesizeTextToMp3File(
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
              authorization,
            );
          },
        },
      ).catch((error: unknown) => {
        throw new WorkerError("generation-failed", "Audio generation failed", {
          cause: error,
        });
      });

      span.end();

      return value;
    },
  );

  try {
    const uploadUrl = await tracer.startActiveSpan(
      "synthesize.upload_url",
      async (span) => {
        try {
          return await getUploadUrl(
            job,
            attempt,
            config,
            signal,
            authorization,
          );
        } finally {
          span.end();
        }
      },
    );

    await sendStatus(
      job,
      attempt,
      { status: "uploading", percent: 0 },
      config,
      signal,
      authorization,
    );

    const uploadStartedAt = performance.now();

    await tracer.startActiveSpan("synthesize.upload", async (span) => {
      try {
        await uploadFile(
          uploadUrl,
          synthesis.filePath,
          synthesis.fileSizeBytes,
          job,
          attempt,
          config,
          signal,
          authorization,
        );
      } catch (error) {
        logUploadFailure(
          jobEvent(job, attempt, modelPath, "failed", {
            errorCode: toStatusError(error).code,
            errorMessage: toStatusError(error).message,
            stage: "upload",
          }),
          uploadUrl,
        );
        throw error;
      } finally {
        span.end();
      }
    });
    logJob(job, attempt, modelPath, "uploading", {
      fileSizeBytes: synthesis.fileSizeBytes,
      fileSizeMb: synthesis.fileSizeBytes / 1_000_000,
      uploadDurationMs: performance.now() - uploadStartedAt,
    });
    await sendStatus(
      job,
      attempt,
      { status: "completed", fileSizeBytes: synthesis.fileSizeBytes },
      config,
      signal,
      authorization,
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
  authorization?: string,
): Promise<string> {
  logger.info(
    `Requesting a new presigned URL for a text ${job.text.length} character long (attempt: ${attempt})`,
    { context: "getUploadUrl" },
  );

  const response = await fetchWithTimeout(
    job.genUploadUrl,
    callbackRequest(
      job,
      attempt,
      {
        jobId: job.jobId,
        attempt,
        contentType: CONTENT_TYPE,
      },
      authorization,
    ),
    config.genUploadUrlTimeoutMs,
    signal,
  );

  if (!response.ok) {
    throw httpError("upload-url-failed", response);
  }

  const payload = (await response.json()) as { uploadUrl?: unknown };

  if (
    typeof payload.uploadUrl !== "string" ||
    payload.uploadUrl.trim() === ""
  ) {
    logger.error(
      `Client returned ${JSON.stringify(payload)} but we expect the payload to have a uploadUrl`,
      { context: "getUploadUrl" },
    );
    throw new WorkerError(
      "upload-url-failed",
      "Upload URL callback returned an invalid response",
    );
  }

  logger.info(
    `Generated presigned URL is ${redactPresignedUrl(payload.uploadUrl)}`,
    { context: "getUploadUrl" },
  );

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
  authorization?: string,
): Promise<void> {
  logger.info(
    `Initializing upload process: (file size: ${fileSizeBytes}, file path: ${filePath}, upload URL: ${redactPresignedUrl(uploadUrl)})`,
    { context: "SynthesizeWorker" },
  );

  const controller = new AbortController();
  const abort = (): void => controller.abort(signal.reason);

  signal.addEventListener("abort", abort, { once: true });

  const deadline = setTimeout(
    () => controller.abort(new Error("Upload timed out")),
    config.uploadTimeoutMs,
  );

  let idleTimer: NodeJS.Timeout | undefined;
  function resetIdleTimer(): void {
    logger.verbose("Resetting idle timer...", { context: "uploadFile" });
    if (idleTimer) {
      clearTimeout(idleTimer);
    }
    idleTimer = setTimeout(
      () => controller.abort(new Error("Upload became idle")),
      config.uploadIdleTimeoutMs,
    );
  }

  let sent = 0;
  const report = createThrottledProgressReporter((percent) => {
    void sendStatus(
      job,
      attempt,
      { status: "uploading", percent },
      config,
      signal,
      authorization,
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
    logger.verbose(
      "Cleaning up upload file process regardless of success/failure",
      { context: "uploadFile" },
    );
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
  authorization?: string,
): Promise<void> {
  await tracer.startActiveSpan("synthesize.statusCallback", async (span) => {
    logger.info(
      `Calling client to report we are in "${status.status}" state for ${job.jobId}.`,
      { context: "sendStatus" },
    );

    try {
      await fetchWithTimeout(
        job.statusCallbackUrl,
        callbackRequest(
          job,
          attempt,
          { jobId: job.jobId, attempt, ...status },
          authorization,
        ),
        config.statusCallbackTimeoutMs,
        signal,
      );
    } catch {
      logger.warn(
        `Failed to report back the "${status.status}" (status callbacks are best-effort and never change job processing).`,
        {
          context: "sendStatus",
          jobId: job.jobId,
          attempt,
          statusCallbackUrl: job.statusCallbackUrl,
          statusCallbackTimeoutMs: config.statusCallbackTimeoutMs,
        },
      );
    } finally {
      span.end();
    }
  });
}

function callbackRequest(
  job: SynthesizeJobMessage,
  attempt: number,
  body: unknown,
  authorization?: string,
): RequestInit {
  return {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": `${job.jobId}:${attempt}`,
      ...(authorization && { Authorization: authorization }),
    },
    body: JSON.stringify(body),
    redirect: "manual",
  };
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
  await tracer.startActiveSpan("synthesize.retry_publish", async (span) => {
    try {
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
    } finally {
      span.end();
    }
  });
}

function logSynthesizeJobEvent(event: SynthesizeJobEvent): void {
  logger.info("Synthesize job event", {
    context: "SynthesizeWorker",
    ...event,
  });
}

function logUploadFailure(event: SynthesizeJobEvent, uploadUrl: string): void {
  logSynthesizeJobEvent({
    ...event,
    uploadUrl: redactPresignedUrl(uploadUrl),
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
    logger.info(`Invalid synthesize job shape: ${JSON.stringify(value)}`, {
      context: "retryDelayMs",
    });

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
    (job.authorization === undefined || typeof job.authorization === "string")
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

function logJob(
  job: SynthesizeJobMessage,
  attempt: number,
  modelPath: string,
  state: SynthesizeJobEvent["state"],
  details: Omit<
    SynthesizeJobEvent,
    | "attempt"
    | "instanceId"
    | "jobId"
    | "modelName"
    | "state"
    | "textCharacterCount"
    | "traceId"
  > = {},
): void {
  logSynthesizeJobEvent(jobEvent(job, attempt, modelPath, state, details));
}

function jobEvent(
  job: SynthesizeJobMessage,
  attempt: number,
  modelPath: string,
  state: SynthesizeJobEvent["state"],
  details: Omit<
    SynthesizeJobEvent,
    | "attempt"
    | "instanceId"
    | "jobId"
    | "modelName"
    | "state"
    | "textCharacterCount"
    | "traceId"
  > = {},
): SynthesizeJobEvent {
  return {
    attempt,
    instanceId: hostname() ?? "unknown",
    jobId: job.jobId,
    modelName: basename(modelPath),
    state,
    textCharacterCount: job.text.length,
    traceId: trace.getActiveSpan()?.spanContext().traceId,
    ...details,
  };
}
