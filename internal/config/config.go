// Package config loads the service configuration from environment variables.
package config

import (
	"errors"
	"time"
)

// Environment names that relax the HTTPS callback requirement.
const (
	envDevelopment = "development"
	envTest        = "test"
)

// Values accepted by OTEL_LOG_SHIPPING.
const (
	logShippingOTLP   = "otlp"
	logShippingStdout = "stdout"
)

// Log modes and levels accepted by LOGGING_MODE and LOGGING_LEVEL.
const (
	LogModePlainText = "PLAIN_TEXT"
	LogModeJSON      = "JSON"

	LogLevelError   = "error"
	LogLevelWarn    = "warn"
	LogLevelInfo    = "info"
	LogLevelVerbose = "verbose"
	LogLevelDebug   = "debug"
)

// Defaults match the documented values in docs/config.md.
const (
	defaultPort                = 3000
	defaultMaxConcurrency      = 10
	defaultMaxBodySizeBytes    = 500_000
	defaultSynthBodySizeBytes  = 2_000_000
	defaultSynthMaxAttempts    = 3
	defaultWordsPerMinute      = 150
	defaultRetryDelay          = 30 * time.Second
	defaultCallbackTimeout     = 10 * time.Second
	defaultUploadIdleTimeout   = 10 * time.Second
	defaultUploadTimeout       = 10 * time.Minute
	defaultBatchQueueSize      = 2048
	defaultBatchSize           = 512
	defaultBatchFlushInterval  = 5 * time.Second
	defaultBatchExportTimeout  = 30 * time.Second
	defaultServiceName         = "piper-tts-rest-api"
	defaultPiperModelPath      = "/app/models/en_US-lessac-medium.onnx"
	defaultRabbitMQURL         = "amqp://localhost:5672"
	defaultEnvironment         = "production"
	defaultLogMode             = LogModePlainText
	defaultLogLevel            = LogLevelInfo
	defaultExchange            = "synthesize"
	defaultWorkQueue           = "synthesize.work"
	defaultRetryQueue          = "synthesize.retry"
	defaultDeadLetterExchange  = "synthesize.dlx"
	defaultDeadLetterQueue     = "synthesize.dlq"
	defaultOTLPLogShippingMode = logShippingOTLP
)

// Config is the complete, validated service configuration.
type Config struct {
	Service Service
	// MaxConcurrency caps simultaneous /speak requests.
	MaxConcurrency int
	// PiperModelPath is the .onnx voice model; <path>.json must sit next to it.
	PiperModelPath string
	// MaxBodySizeBytes caps the /speak request body.
	MaxBodySizeBytes int
	Synthesize       Synthesize
	Log              Log
	Otel             Otel
}

// Service holds process-level settings.
type Service struct {
	Port int
	Name string
	// Env is production, development or test; the latter two relax the HTTPS callback rule.
	Env string
}

// Log holds logging settings. Valid values are checked by Load.
type Log struct {
	// Mode is PLAIN_TEXT or JSON.
	Mode string
	// Level is one of error, warn, info, debug, verbose.
	Level string
}

// Synthesize holds settings for the asynchronous POST /synthesize flow.
type Synthesize struct {
	// MaxBodySizeBytes caps the /synthesize request body.
	MaxBodySizeBytes int
	// MaxAttempts is the total processing attempts, including the first.
	MaxAttempts int
	// RetryDelay is how long a failed job waits in the retry queue.
	RetryDelay time.Duration
	// StatusCallbackTimeout bounds one status callback call.
	StatusCallbackTimeout time.Duration
	// GenUploadURLTimeout bounds the presigned-URL generation callback.
	GenUploadURLTimeout time.Duration
	// UploadIdleTimeout aborts an upload that stops making progress.
	UploadIdleTimeout time.Duration
	// UploadTimeout is the absolute upload deadline.
	UploadTimeout time.Duration
	// WordsPerMinute is used to estimate progress.
	WordsPerMinute int
	RabbitMQ       RabbitMQ
	Callback       Callback
}

// RabbitMQ holds the connection URL and topology names.
type RabbitMQ struct {
	URL                string
	Exchange           string
	WorkQueue          string
	RetryQueue         string
	DeadLetterExchange string
	DeadLetterQueue    string
}

// Callback holds the rules applied to caller-supplied callback URLs.
type Callback struct {
	RequireHTTPS bool
	// AllowedHosts is lower-cased; empty means any host is allowed.
	AllowedHosts []string
}

// Otel holds OpenTelemetry settings. The OTLP endpoint, sampler and
// sampler argument are also read by the OTel SDK itself.
type Otel struct {
	// Enabled turns tracing (and log shipping) on.
	Enabled bool
	// ExporterEndpoint is the OTLP HTTP base URL; empty means unset.
	ExporterEndpoint string
	// LogShipping is otlp or stdout; only meaningful when Enabled.
	LogShipping string
	Traces      Batch
	Logs        Batch
}

// Batch tunes a batching OTLP exporter pipeline.
type Batch struct {
	// MaxQueueSize is the in-memory queue size; overflow is dropped.
	MaxQueueSize int
	// MaxExportBatchSize is the most items sent in one export request.
	MaxExportBatchSize int
	FlushInterval      time.Duration
	ExportTimeout      time.Duration
}

// Load reads the configuration through lookup (os.LookupEnv).
// It returns every invalid variable at once.
func Load(lookup func(key string) (string, bool)) (Config, error) {
	l := &loader{lookup: lookup}

	service := Service{
		Port: l.positiveInt("PORT", defaultPort),
		Name: l.str("SERVICE_NAME", defaultServiceName),
		Env:  l.str("APP_ENV", defaultEnvironment),
	}

	cfg := Config{
		Service:          service,
		MaxConcurrency:   l.positiveInt("MAX_CONCURRENCY", defaultMaxConcurrency),
		PiperModelPath:   l.str("PIPER_MODEL", defaultPiperModelPath),
		MaxBodySizeBytes: l.positiveInt("MAX_BODY_SIZE_BYTES", defaultMaxBodySizeBytes),
		Synthesize:       loadSynthesize(l, service.Env),
		Log: Log{
			Mode:  l.oneOf("LOGGING_MODE", defaultLogMode, LogModePlainText, LogModeJSON),
			Level: l.oneOf("LOGGING_LEVEL", defaultLogLevel, LogLevelError, LogLevelWarn, LogLevelInfo, LogLevelDebug, LogLevelVerbose),
		},
		Otel: loadOtel(l),
	}

	if err := errors.Join(l.errs...); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func loadSynthesize(l *loader, env string) Synthesize {
	return Synthesize{
		MaxBodySizeBytes:      l.positiveInt("SYNTHESIZE_MAX_BODY_SIZE_BYTES", defaultSynthBodySizeBytes),
		MaxAttempts:           l.positiveInt("SYNTHESIZE_MAX_ATTEMPTS", defaultSynthMaxAttempts),
		RetryDelay:            l.millis("SYNTHESIZE_RETRY_DELAY_MS", defaultRetryDelay),
		StatusCallbackTimeout: l.millis("SYNTHESIZE_STATUS_CALLBACK_TIMEOUT_MS", defaultCallbackTimeout),
		GenUploadURLTimeout:   l.millis("SYNTHESIZE_GEN_UPLOAD_URL_TIMEOUT_MS", defaultCallbackTimeout),
		UploadIdleTimeout:     l.millis("SYNTHESIZE_UPLOAD_IDLE_TIMEOUT_MS", defaultUploadIdleTimeout),
		UploadTimeout:         l.millis("SYNTHESIZE_UPLOAD_TIMEOUT_MS", defaultUploadTimeout),
		WordsPerMinute:        l.positiveInt("SYNTHESIZE_WORDS_PER_MINUTE", defaultWordsPerMinute),
		RabbitMQ: RabbitMQ{
			URL:                l.amqpURL("RABBITMQ_URL", defaultRabbitMQURL),
			Exchange:           l.name("SYNTHESIZE_EXCHANGE", defaultExchange),
			WorkQueue:          l.name("SYNTHESIZE_WORK_QUEUE", defaultWorkQueue),
			RetryQueue:         l.name("SYNTHESIZE_RETRY_QUEUE", defaultRetryQueue),
			DeadLetterExchange: l.name("SYNTHESIZE_DEAD_LETTER_EXCHANGE", defaultDeadLetterExchange),
			DeadLetterQueue:    l.name("SYNTHESIZE_DEAD_LETTER_QUEUE", defaultDeadLetterQueue),
		},
		Callback: Callback{
			RequireHTTPS: l.boolean("SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS", env != envDevelopment && env != envTest),
			AllowedHosts: l.hosts("SYNTHESIZE_CALLBACK_ALLOWED_HOSTS"),
		},
	}
}

func loadOtel(l *loader) Otel {
	// Like the TypeScript service, anything but "true" means disabled.
	enabled, _ := l.lookup("OTEL_ENABLED")
	otel := Otel{
		Enabled:          enabled == "true",
		ExporterEndpoint: l.str("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		LogShipping:      defaultOTLPLogShippingMode,
		Traces: Batch{
			MaxQueueSize:       l.positiveInt("OTEL_BATCH_MAX_PENDING_SPANS", defaultBatchQueueSize),
			MaxExportBatchSize: l.positiveInt("OTEL_BATCH_SPANS_PER_EXPORT", defaultBatchSize),
			FlushInterval:      l.millis("OTEL_BATCH_FLUSH_INTERVAL_MS", defaultBatchFlushInterval),
			ExportTimeout:      l.millis("OTEL_BATCH_EXPORT_TIMEOUT_MS", defaultBatchExportTimeout),
		},
		Logs: Batch{
			MaxQueueSize:       l.positiveInt("OTEL_LOGS_BATCH_MAX_PENDING", defaultBatchQueueSize),
			MaxExportBatchSize: l.positiveInt("OTEL_LOGS_BATCH_PER_EXPORT", defaultBatchSize),
			FlushInterval:      l.millis("OTEL_LOGS_BATCH_FLUSH_INTERVAL_MS", defaultBatchFlushInterval),
			ExportTimeout:      l.millis("OTEL_LOGS_BATCH_EXPORT_TIMEOUT_MS", defaultBatchExportTimeout),
		},
	}
	if otel.Enabled {
		otel.LogShipping = l.oneOf("OTEL_LOG_SHIPPING", defaultOTLPLogShippingMode, logShippingOTLP, logShippingStdout)
	}

	return otel
}
