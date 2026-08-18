import type { ConsumeMessage } from "amqplib";

import { jest } from "@jest/globals";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const readPiperModelSampleRateMock = jest.fn();
const synthesizeTextToMp3FileMock = jest.fn();

jest.unstable_mockModule("./piper.js", () => ({
  readPiperModelSampleRate: readPiperModelSampleRateMock,
}));
jest.unstable_mockModule("./synthesis.js", () => ({
  createThrottledProgressReporter: (callback: (percent: number) => void) =>
    callback,
  synthesizeTextToMp3File: synthesizeTextToMp3FileMock,
}));

const { startSynthesizeWorker } = await import("./synthesize-worker.js");

const config = {
  maxBodySizeBytes: 2_000_000,
  maxAttempts: 3,
  retryDelayMs: 30_000,
  statusCallbackTimeoutMs: 10_000,
  genUploadUrlTimeoutMs: 10_000,
  uploadIdleTimeoutMs: 10_000,
  uploadTimeoutMs: 600_000,
  wordsPerMinute: 150,
  rabbitmq: {
    url: "amqp://rabbitmq:5672",
    exchange: "synthesize",
    workQueue: "synthesize.work",
    retryQueue: "synthesize.retry",
    deadLetterExchange: "synthesize.dlx",
    deadLetterQueue: "synthesize.dlq",
  },
  callback: { requireHttps: true, allowedHosts: [] },
} as const;

describe(startSynthesizeWorker.name, () => {
  const fetchMock = jest.fn<typeof fetch>();
  let originalFetch: typeof fetch;
  let filePath: string;
  let workDir: string;

  beforeEach(async () => {
    jest.clearAllMocks();
    originalFetch = globalThis.fetch;
    globalThis.fetch = fetchMock;
    readPiperModelSampleRateMock.mockReturnValue(22_050);
    workDir = await mkdtemp(join(tmpdir(), "synthesize-worker-"));
    filePath = join(workDir, "audio.mp3");
    await writeFile(filePath, Buffer.from([0xff, 0xfb, 0x90, 0x00]));
    synthesizeTextToMp3FileMock.mockResolvedValue({
      filePath,
      fileSizeBytes: 4,
      generationDurationMs: 12,
      cleanup: jest.fn().mockResolvedValue(undefined as never),
    } as never);
  });

  afterEach(async () => {
    globalThis.fetch = originalFetch;
    await rm(workDir, { force: true, recursive: true });
  });

  it("should generate, upload, report completion, and acknowledge a successful job", async () => {
    // Arrange
    const { broker, deliver } = createBroker();
    fetchMock
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ uploadUrl: "https://storage.example.com/job.mp3" }),
          {
            status: 200,
          },
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));

    // Act
    await startSynthesizeWorker(
      broker,
      config,
      "/app/models/en_US-lessac-medium.onnx",
    );
    deliver(message());
    await waitFor(
      () => jest.mocked(broker.acknowledge).mock.calls.length === 1,
    );

    // Assert
    expect(synthesizeTextToMp3FileMock).toHaveBeenCalledWith(
      "hello world",
      "/app/models/en_US-lessac-medium.onnx",
      22_050,
      150,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "https://client.example.com/upload-url",
      expect.objectContaining({ method: "POST" }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      "https://storage.example.com/job.mp3",
      expect.objectContaining({
        headers: expect.objectContaining({
          "Content-Length": "4",
          "Content-Type": "audio/mpeg",
        }),
        method: "PUT",
      }),
    );
    expect(await requestBody(fetchMock.mock.calls[4][1]?.body)).toEqual({
      attempt: 1,
      fileSizeBytes: 4,
      jobId: "job-1",
      status: "completed",
    });
    expect(broker.publish).not.toHaveBeenCalled();
    expect(broker.reject).not.toHaveBeenCalled();
  });

  it("should publish a retry with incremented attempt count and delay before acknowledging the failed message", async () => {
    // Arrange
    const { broker, deliver } = createBroker();
    fetchMock
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        new Response(null, {
          headers: { "retry-after": "60" },
          status: 429,
        }),
      );

    // Act
    await startSynthesizeWorker(
      broker,
      config,
      "/app/models/en_US-lessac-medium.onnx",
    );
    deliver(message({ "x-attempt": 1 }));
    await waitFor(
      () => jest.mocked(broker.acknowledge).mock.calls.length === 1,
    );

    // Assert
    expect(broker.publish).toHaveBeenCalledWith(
      expect.objectContaining({
        content: expect.any(Buffer),
        exchange: "",
        options: expect.objectContaining({
          expiration: "60000",
          headers: expect.objectContaining({ "x-attempt": 2 }),
          persistent: true,
        }),
        routingKey: "synthesize.retry",
      }),
    );
    expect(broker.reject).not.toHaveBeenCalled();
  });

  it("should report a terminal failure for a non-retryable upload URL response", async () => {
    // Arrange
    const { broker, deliver } = createBroker();
    fetchMock
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 400 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));

    // Act
    await startSynthesizeWorker(
      broker,
      config,
      "/app/models/en_US-lessac-medium.onnx",
    );
    deliver(message());
    await waitFor(
      () => jest.mocked(broker.acknowledge).mock.calls.length === 1,
    );

    // Assert
    expect(await requestBody(fetchMock.mock.calls[2][1]?.body)).toEqual(
      expect.objectContaining({
        attempt: 1,
        error: { code: "upload-url-failed", message: "HTTP 400" },
        jobId: "job-1",
        status: "failed",
      }),
    );
    expect(broker.publish).not.toHaveBeenCalled();
  });

  it("should REJECT malformed messages without requeueing so broker topology dead-letters them", async () => {
    // Arrange
    const { broker, deliver } = createBroker();

    // Act
    await startSynthesizeWorker(
      broker,
      config,
      "/app/models/en_US-lessac-medium.onnx",
    );
    deliver({ ...message(), content: Buffer.from("not json") });
    await waitFor(() => jest.mocked(broker.reject).mock.calls.length === 1);

    // Assert
    expect(broker.reject).toHaveBeenCalledWith(expect.any(Object), false);
    expect(synthesizeTextToMp3FileMock).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

function createBroker(): {
  broker: Parameters<typeof startSynthesizeWorker>[0];
  deliver(message: ConsumeMessage): void;
} {
  let consumer: ((message: ConsumeMessage | null) => void) | undefined;

  return {
    broker: {
      acknowledge: jest.fn(),
      consume: jest.fn().mockImplementation(async (handler: any) => {
        consumer = handler;
        return "consumer-1";
      }),
      publish: jest.fn().mockResolvedValue(undefined as never),
      reject: jest.fn(),
    },
    deliver: (deliveredMessage: any) => consumer?.(deliveredMessage),
  } as any;
}

function message(headers: Record<string, unknown> = {}): ConsumeMessage {
  return {
    content: Buffer.from(
      JSON.stringify({
        genUploadUrl: "https://client.example.com/upload-url",
        jobId: "job-1",
        statusCallbackUrl: "https://client.example.com/status",
        text: "hello world",
      }),
    ),
    properties: { headers, messageId: "job-1" },
  } as ConsumeMessage;
}

async function requestBody(
  body: BodyInit | null | undefined,
): Promise<unknown> {
  return JSON.parse(await new Response(body).text()) as unknown;
}

async function waitFor(condition: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (condition()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
  throw new Error("Timed out waiting for worker processing");
}
