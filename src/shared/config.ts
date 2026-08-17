import type { LogLevel, LogMode } from "./utils/index.js";

/**
 * Application configuration values sourced from environment variables.
 */
export const config = {
  /** Service configuration. */
  service: {
    /** Port the HTTP server listens on. */
    port: Number.parseInt(process.env.PORT ?? "3000", 10),

    /**
     * Service name for logging.
     * @default 'piper-tts-rest-api'
     */
    name: process.env.SERVICE_NAME ?? "piper-tts-rest-api",

    /**
     * @type {'production' | 'development' | 'test'}
     * @description Environment the service is running in.
     * @default 'production'
     */
    env: process.env.NODE_ENV ?? "production",
  },

  /**
   * Maximum number of concurrent text-to-speech requests.
   * @default 10
   */
  maxConcurrency: Number.parseInt(process.env.MAX_CONCURRENCY ?? "10", 10),

  /** Path to the Piper model file. */
  piperModelPath:
    process.env.PIPER_MODEL ?? "/app/models/en_US-lessac-medium.onnx",

  /**
   * Maximum allowed request body size is 0.5MB, which is sufficient for at least ~75000 words — more than enough for typical use cases.
   * @default 500_000
   */
  maxBodySizeBytes: Number(process.env.MAX_BODY_SIZE_BYTES ?? 500_000),

  /** Asynchronous synthesis configuration. */
  synthesize: createSynthesizeConfig(
    process.env,
    process.env.NODE_ENV ?? "production",
  ),

  /** Logging configuration. */
  log: {
    /**
     * Logging mode: PLAIN_TEXT for human-readable logs, JSON for structured logs.
     * @default 'PLAIN_TEXT'
     */
    mode: ((process.env.LOGGING_MODE as LogMode | undefined) ??
      "PLAIN_TEXT") as LogMode,

    /**
     * Logging level: error, warn, info, debug, verbose.
     * @default 'info'
     */
    level: ((process.env.LOGGING_LEVEL as LogLevel | undefined) ??
      "info") as LogLevel,
  },

  otel: {
    enabled: process.env.OTEL_ENABLED === "true",
    /** @type {string | undefined} */
    exporterEndpoint: process.env.OTEL_EXPORTER_OTLP_ENDPOINT,
    batchConfig: {
      /**
       * @description Max spans held in the in-memory queue waiting to be exported. Spans beyond this cap are dropped (with a diag warning) — acts as a back-pressure safety valve if the collector is slow/unreachable.
       */
      maxPendingSpans: Number.parseInt(
        process.env.OTEL_BATCH_MAX_PENDING_SPANS ?? "2048",
        10,
      ),
      /**
       * @description Max spans sent in a single export request. The processor flushes immediately once the queue reaches this size, regardless of the scheduled delay. Smaller = more frequent, smaller payloads.
       */
      spansPerExport: Number.parseInt(
        process.env.OTEL_BATCH_SPANS_PER_EXPORT ?? "512",
        10,
      ),
      /**
       * @description Interval at which the processor wakes up and flushes whatever is currently queued, even if `maxExportBatchSize` hasn't been reached. Trade-off: lower = fresher data in the backend, higher = fewer HTTP round-trips.
       */
      flushIntervalMs: Number.parseInt(
        process.env.OTEL_BATCH_FLUSH_INTERVAL_MS ?? "5000",
        10,
      ),
      /**
       * @description Hard deadline for a single export call to the OTLP endpoint. If the collector doesn't respond within this window the export is aborted and the spans in that batch are dropped (not retried).
       */
      exportTimeoutMs: Number.parseInt(
        process.env.OTEL_BATCH_EXPORT_TIMEOUT_MS ?? "30000",
        10,
      ),
    },
    /**
     * @description Same trade-offs as `batchConfig` but for the log record pipeline driven by `BatchLogRecordProcessor`. Logs are typically chattier than spans so it's worth tuning these independently.
     */
    logsBatchConfig: {
      /** Max log records held in memory before back-pressure drops the oldest. */
      maxPendingLogs: Number.parseInt(
        process.env.OTEL_LOGS_BATCH_MAX_PENDING ?? "2048",
        10,
      ),
      /** Max log records flushed in a single export request. */
      logsPerExport: Number.parseInt(
        process.env.OTEL_LOGS_BATCH_PER_EXPORT ?? "512",
        10,
      ),
      /** How often the processor flushes whatever is queued. */
      flushIntervalMs: Number.parseInt(
        process.env.OTEL_LOGS_BATCH_FLUSH_INTERVAL_MS ?? "5000",
        10,
      ),
      /** Per-export hard deadline before the batch is aborted. */
      exportTimeoutMs: Number.parseInt(
        process.env.OTEL_LOGS_BATCH_EXPORT_TIMEOUT_MS ?? "30000",
        10,
      ),
    },
  },
} as const;

export interface SynthesizeConfig {
  /** Usually higher than the speak limitation. */
  maxBodySizeBytes: number;
  /** Caps how many times this service retries before giving up on TTS generation */
  maxAttempts: number;
  /** Retries will backoff as to not retry just immediately */
  retryDelayMs: number;
  /** Status report callback calls timeout after */
  statusCallbackTimeoutMs: number;
  /** Generate presigned upload URL times out after */
  genUploadUrlTimeoutMs: number;
  /** Idle presigned upload URL calls timeout after */
  uploadIdleTimeoutMs: number;
  /** Upload calls timeout after */
  uploadTimeoutMs: number;
  wordsPerMinute: number;
  rabbitmq: {
    url: string;
    exchange: string;
    workQueue: string;
    retryQueue: string;
    deadLetterExchange: string;
    deadLetterQueue: string;
  };
  callback: {
    requireHttps: boolean;
    allowedHosts: readonly string[];
  };
}

export function createSynthesizeConfig(
  env: NodeJS.ProcessEnv,
  serviceEnv: string,
): SynthesizeConfig {
  const rabbitmqUrl = env.RABBITMQ_URL ?? "amqp://localhost:5672";
  validateRabbitmqUrl(rabbitmqUrl);

  return {
    maxBodySizeBytes: readPositiveInteger(
      env.SYNTHESIZE_MAX_BODY_SIZE_BYTES,
      2_000_000,
      "SYNTHESIZE_MAX_BODY_SIZE_BYTES",
    ),
    maxAttempts: readPositiveInteger(
      env.SYNTHESIZE_MAX_ATTEMPTS,
      3,
      "SYNTHESIZE_MAX_ATTEMPTS",
    ),
    retryDelayMs: readPositiveInteger(
      env.SYNTHESIZE_RETRY_DELAY_MS,
      30_000,
      "SYNTHESIZE_RETRY_DELAY_MS",
    ),
    statusCallbackTimeoutMs: readPositiveInteger(
      env.SYNTHESIZE_STATUS_CALLBACK_TIMEOUT_MS,
      10_000,
      "SYNTHESIZE_STATUS_CALLBACK_TIMEOUT_MS",
    ),
    genUploadUrlTimeoutMs: readPositiveInteger(
      env.SYNTHESIZE_GEN_UPLOAD_URL_TIMEOUT_MS,
      10_000,
      "SYNTHESIZE_GEN_UPLOAD_URL_TIMEOUT_MS",
    ),
    uploadIdleTimeoutMs: readPositiveInteger(
      env.SYNTHESIZE_UPLOAD_IDLE_TIMEOUT_MS,
      10_000,
      "SYNTHESIZE_UPLOAD_IDLE_TIMEOUT_MS",
    ),
    uploadTimeoutMs: readPositiveInteger(
      env.SYNTHESIZE_UPLOAD_TIMEOUT_MS,
      600_000,
      "SYNTHESIZE_UPLOAD_TIMEOUT_MS",
    ),
    wordsPerMinute: readPositiveInteger(
      env.SYNTHESIZE_WORDS_PER_MINUTE,
      150,
      "SYNTHESIZE_WORDS_PER_MINUTE",
    ),
    rabbitmq: {
      url: rabbitmqUrl,
      exchange: readName(
        env.SYNTHESIZE_EXCHANGE,
        "synthesize",
        "SYNTHESIZE_EXCHANGE",
      ),
      workQueue: readName(
        env.SYNTHESIZE_WORK_QUEUE,
        "synthesize.work",
        "SYNTHESIZE_WORK_QUEUE",
      ),
      retryQueue: readName(
        env.SYNTHESIZE_RETRY_QUEUE,
        "synthesize.retry",
        "SYNTHESIZE_RETRY_QUEUE",
      ),
      deadLetterExchange: readName(
        env.SYNTHESIZE_DEAD_LETTER_EXCHANGE,
        "synthesize.dlx",
        "SYNTHESIZE_DEAD_LETTER_EXCHANGE",
      ),
      deadLetterQueue: readName(
        env.SYNTHESIZE_DEAD_LETTER_QUEUE,
        "synthesize.dlq",
        "SYNTHESIZE_DEAD_LETTER_QUEUE",
      ),
    },
    callback: {
      requireHttps: readBoolean(
        env.SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS,
        serviceEnv !== "development" && serviceEnv !== "test",
        "SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS",
      ),
      allowedHosts: readAllowedHosts(env.SYNTHESIZE_CALLBACK_ALLOWED_HOSTS),
    },
  };
}

function readPositiveInteger(
  rawValue: string | undefined,
  defaultValue: number,
  name: string,
): number {
  if (rawValue === undefined) {
    return defaultValue;
  }
  if (!/^\d+$/.test(rawValue)) {
    throw new Error(`${name} must be a positive integer`);
  }

  const value = Number.parseInt(rawValue, 10);

  if (value < 1) {
    throw new Error(`${name} must be a positive integer`);
  }

  return value;
}

function readBoolean(
  rawValue: string | undefined,
  defaultValue: boolean,
  name: string,
): boolean {
  if (rawValue === undefined) {
    return defaultValue;
  }
  if (rawValue === "true") {
    return true;
  }
  if (rawValue === "false") {
    return false;
  }

  throw new Error(`${name} must be either true or false`);
}

function readAllowedHosts(rawValue: string | undefined): readonly string[] {
  if (rawValue === undefined || rawValue.trim() === "") {
    return [];
  }

  const hosts = rawValue.split(",").map((host) => host.trim().toLowerCase());

  if (hosts.some((host) => host === "" || host.includes("/"))) {
    throw new Error(
      "SYNTHESIZE_CALLBACK_ALLOWED_HOSTS must be comma-separated hostnames",
    );
  }

  return hosts;
}

function readName(
  rawValue: string | undefined,
  defaultValue: string,
  name: string,
): string {
  const value = rawValue ?? defaultValue;

  if (value.trim() === "") {
    throw new Error(`${name} must not be empty`);
  }

  return value;
}

function validateRabbitmqUrl(value: string): void {
  let url: URL;

  try {
    url = new URL(value);
  } catch {
    throw new Error("RABBITMQ_URL must be a valid amqp or amqps URL");
  }

  if (url.protocol !== "amqp:" && url.protocol !== "amqps:") {
    throw new Error("RABBITMQ_URL must use amqp or amqps");
  }
}
