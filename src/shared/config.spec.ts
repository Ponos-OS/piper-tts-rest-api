import { createOtelConfig, createSynthesizeConfig } from "./config.js";

describe(createOtelConfig.name, () => {
  it("should send Winston logs through the OpenTelemetry Logs SDK by default", () => {
    expect(createOtelConfig({}).winston.disableLogSending).toBe(false);
  });

  it("should read the Winston log sending setting", () => {
    expect(
      createOtelConfig({ OTEL_WINSTON_DISABLE_LOG_SENDING: "true" }).winston
        .disableLogSending,
    ).toBe(true);
  });

  it("should reject an invalid Winston log sending setting", () => {
    expect(() =>
      createOtelConfig({ OTEL_WINSTON_DISABLE_LOG_SENDING: "yes" }),
    ).toThrow("OTEL_WINSTON_DISABLE_LOG_SENDING must be either true or false");
  });
});

describe(createSynthesizeConfig.name, () => {
  it("should use documented defaults", () => {
    const config = createSynthesizeConfig({}, "test");

    expect(config).toMatchObject({
      maxBodySizeBytes: 2_000_000,
      maxAttempts: 3,
      retryDelayMs: 30_000,
      statusCallbackTimeoutMs: 10_000,
      genUploadUrlTimeoutMs: 10_000,
      uploadIdleTimeoutMs: 10_000,
      uploadTimeoutMs: 600_000,
      wordsPerMinute: 150,
      rabbitmq: {
        url: "amqp://localhost:5672",
        exchange: "synthesize",
        workQueue: "synthesize.work",
        retryQueue: "synthesize.retry",
        deadLetterExchange: "synthesize.dlx",
        deadLetterQueue: "synthesize.dlq",
      },
      callback: { requireHttps: false, allowedHosts: [] },
    });
  });

  it("should require HTTPS callbacks outside development and test", () => {
    const config = createSynthesizeConfig({}, "production");

    expect(config.callback.requireHttps).toBe(true);
  });

  it("should read callback allowed hosts", () => {
    const config = createSynthesizeConfig(
      {
        SYNTHESIZE_CALLBACK_ALLOWED_HOSTS:
          "api.example.com, callbacks.example.com",
      },
      "test",
    );

    expect(config.callback.allowedHosts).toEqual([
      "api.example.com",
      "callbacks.example.com",
    ]);
  });

  it.each([
    ["SYNTHESIZE_MAX_ATTEMPTS", "0"],
    ["SYNTHESIZE_UPLOAD_TIMEOUT_MS", "ten"],
  ])("should reject an invalid %s value", (name, value) => {
    expect(() => createSynthesizeConfig({ [name]: value }, "test")).toThrow(
      `${name} must be a positive integer`,
    );
  });

  it("should reject an invalid RabbitMQ URL", () => {
    expect(() =>
      createSynthesizeConfig(
        { RABBITMQ_URL: "https://rabbitmq.example.com" },
        "test",
      ),
    ).toThrow("RABBITMQ_URL must use amqp or amqps");
  });

  it("should reject an invalid callback HTTPS setting", () => {
    expect(() =>
      createSynthesizeConfig(
        { SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS: "yes" },
        "test",
      ),
    ).toThrow(
      "SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS must be either true or false",
    );
  });
});
