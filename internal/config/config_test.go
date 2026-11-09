package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]

		return v, ok
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := config.Load(lookupFrom(nil))

	require.NoError(t, err)
	assert.Equal(t, config.Config{
		Service:          config.Service{Port: 3000, Name: "piper-tts-rest-api", Version: "unknown", Env: "production"},
		MaxConcurrency:   10,
		PiperModelPath:   "/app/models/en_US-lessac-medium.onnx",
		MaxBodySizeBytes: 500_000,
		Synthesize: config.Synthesize{
			MaxBodySizeBytes:      2_000_000,
			MaxAttempts:           3,
			RetryDelay:            30 * time.Second,
			StatusCallbackTimeout: 10 * time.Second,
			GenUploadURLTimeout:   10 * time.Second,
			UploadIdleTimeout:     10 * time.Second,
			UploadTimeout:         10 * time.Minute,
			WordsPerMinute:        150,
			RabbitMQ: config.RabbitMQ{
				URL:                "amqp://localhost:5672",
				Exchange:           "synthesize",
				WorkQueue:          "synthesize.work",
				DeadLetterExchange: "synthesize.dlx",
				DeadLetterQueue:    "synthesize.dlq",
			},
			Callback: config.Callback{RequireHTTPS: true},
		},
		Log: config.Log{Mode: "PLAIN_TEXT", Level: "info"},
		Otel: config.Otel{
			LogShipping: "otlp",
			Traces:      config.Batch{MaxQueueSize: 2048, MaxExportBatchSize: 512, FlushInterval: 5 * time.Second, ExportTimeout: 30 * time.Second},
			Logs:        config.Batch{MaxQueueSize: 2048, MaxExportBatchSize: 512, FlushInterval: 5 * time.Second, ExportTimeout: 30 * time.Second},
		},
	}, cfg)
}

func TestLoad_ReadsEveryVariable(t *testing.T) {
	env := map[string]string{
		"PORT":                                  "8080",
		"SERVICE_NAME":                          "tts",
		"SERVICE_VERSION":                       "4.1.0",
		"APP_ENV":                               "staging",
		"MAX_CONCURRENCY":                       "4",
		"PIPER_MODEL":                           "/m/voice.onnx",
		"MAX_BODY_SIZE_BYTES":                   "1000",
		"LOGGING_MODE":                          "JSON",
		"LOGGING_LEVEL":                         "verbose",
		"SYNTHESIZE_MAX_BODY_SIZE_BYTES":        "2000",
		"SYNTHESIZE_MAX_ATTEMPTS":               "5",
		"SYNTHESIZE_RETRY_DELAY_MS":             "1000",
		"SYNTHESIZE_STATUS_CALLBACK_TIMEOUT_MS": "2000",
		"SYNTHESIZE_GEN_UPLOAD_URL_TIMEOUT_MS":  "3000",
		"SYNTHESIZE_UPLOAD_IDLE_TIMEOUT_MS":     "4000",
		"SYNTHESIZE_UPLOAD_TIMEOUT_MS":          "5000",
		"SYNTHESIZE_WORDS_PER_MINUTE":           "200",
		"RABBITMQ_URL":                          "amqps://mq.example.com:5671",
		"SYNTHESIZE_EXCHANGE":                   "x",
		"SYNTHESIZE_WORK_QUEUE":                 "w",
		"SYNTHESIZE_DEAD_LETTER_EXCHANGE":       "dx",
		"SYNTHESIZE_DEAD_LETTER_QUEUE":          "dq",
		"SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS":    "false",
		"SYNTHESIZE_CALLBACK_ALLOWED_HOSTS":     "a.example.com",
		"OTEL_ENABLED":                          "true",
		"OTEL_EXPORTER_OTLP_ENDPOINT":           "http://collector:4318",
		"OTEL_LOG_SHIPPING":                     "stdout",
		"OTEL_BATCH_MAX_PENDING_SPANS":          "1",
		"OTEL_BATCH_SPANS_PER_EXPORT":           "2",
		"OTEL_BATCH_FLUSH_INTERVAL_MS":          "3",
		"OTEL_BATCH_EXPORT_TIMEOUT_MS":          "4",
		"OTEL_LOGS_BATCH_MAX_PENDING":           "5",
		"OTEL_LOGS_BATCH_PER_EXPORT":            "6",
		"OTEL_LOGS_BATCH_FLUSH_INTERVAL_MS":     "7",
		"OTEL_LOGS_BATCH_EXPORT_TIMEOUT_MS":     "8",
	}

	cfg, err := config.Load(lookupFrom(env))

	require.NoError(t, err)
	assert.Equal(t, config.Config{
		Service:          config.Service{Port: 8080, Name: "tts", Version: "4.1.0", Env: "staging"},
		MaxConcurrency:   4,
		PiperModelPath:   "/m/voice.onnx",
		MaxBodySizeBytes: 1000,
		Synthesize: config.Synthesize{
			MaxBodySizeBytes:      2000,
			MaxAttempts:           5,
			RetryDelay:            time.Second,
			StatusCallbackTimeout: 2 * time.Second,
			GenUploadURLTimeout:   3 * time.Second,
			UploadIdleTimeout:     4 * time.Second,
			UploadTimeout:         5 * time.Second,
			WordsPerMinute:        200,
			RabbitMQ: config.RabbitMQ{
				URL:                "amqps://mq.example.com:5671",
				Exchange:           "x",
				WorkQueue:          "w",
				DeadLetterExchange: "dx",
				DeadLetterQueue:    "dq",
			},
			Callback: config.Callback{AllowedHosts: []string{"a.example.com"}},
		},
		Log: config.Log{Mode: "JSON", Level: "verbose"},
		Otel: config.Otel{
			Enabled:          true,
			ExporterEndpoint: "http://collector:4318",
			LogShipping:      "stdout",
			Traces:           config.Batch{MaxQueueSize: 1, MaxExportBatchSize: 2, FlushInterval: 3 * time.Millisecond, ExportTimeout: 4 * time.Millisecond},
			Logs:             config.Batch{MaxQueueSize: 5, MaxExportBatchSize: 6, FlushInterval: 7 * time.Millisecond, ExportTimeout: 8 * time.Millisecond},
		},
	}, cfg)
}

func TestLoad_CallbackHTTPS_DefaultFollowsAppEnv(t *testing.T) {
	tests := []struct {
		appEnv       string
		requireHTTPS bool
	}{
		{appEnv: "production", requireHTTPS: true},
		{appEnv: "staging", requireHTTPS: true},
		{appEnv: "development", requireHTTPS: false},
		{appEnv: "test", requireHTTPS: false},
	}

	for _, tt := range tests {
		t.Run(tt.appEnv, func(t *testing.T) {
			cfg, err := config.Load(lookupFrom(map[string]string{"APP_ENV": tt.appEnv}))

			require.NoError(t, err)
			assert.Equal(t, tt.requireHTTPS, cfg.Synthesize.Callback.RequireHTTPS)
		})
	}
}

func TestLoad_ExplicitRequireHTTPSWinsOverEnvironment(t *testing.T) {
	env := map[string]string{"APP_ENV": "test", "SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS": "true"}

	cfg, err := config.Load(lookupFrom(env))

	require.NoError(t, err)
	assert.True(t, cfg.Synthesize.Callback.RequireHTTPS)
}

func TestLoad_AllowedHosts(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		hosts []string
	}{
		{name: "empty", raw: "", hosts: nil},
		{name: "only spaces", raw: "   ", hosts: nil},
		{name: "one host", raw: "api.example.com", hosts: []string{"api.example.com"}},
		{name: "trimmed and lower-cased", raw: " API.Example.com , b.example.com", hosts: []string{"api.example.com", "b.example.com"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load(lookupFrom(map[string]string{"SYNTHESIZE_CALLBACK_ALLOWED_HOSTS": tt.raw}))

			require.NoError(t, err)
			assert.Equal(t, tt.hosts, cfg.Synthesize.Callback.AllowedHosts)
		})
	}
}

func TestLoad_RejectsNonPositiveIntegers(t *testing.T) {
	names := []string{
		"PORT", "MAX_CONCURRENCY", "MAX_BODY_SIZE_BYTES",
		"SYNTHESIZE_MAX_BODY_SIZE_BYTES", "SYNTHESIZE_MAX_ATTEMPTS", "SYNTHESIZE_RETRY_DELAY_MS",
		"SYNTHESIZE_STATUS_CALLBACK_TIMEOUT_MS", "SYNTHESIZE_GEN_UPLOAD_URL_TIMEOUT_MS",
		"SYNTHESIZE_UPLOAD_IDLE_TIMEOUT_MS", "SYNTHESIZE_UPLOAD_TIMEOUT_MS", "SYNTHESIZE_WORDS_PER_MINUTE",
		"OTEL_BATCH_MAX_PENDING_SPANS", "OTEL_BATCH_SPANS_PER_EXPORT",
		"OTEL_BATCH_FLUSH_INTERVAL_MS", "OTEL_BATCH_EXPORT_TIMEOUT_MS",
		"OTEL_LOGS_BATCH_MAX_PENDING", "OTEL_LOGS_BATCH_PER_EXPORT",
		"OTEL_LOGS_BATCH_FLUSH_INTERVAL_MS", "OTEL_LOGS_BATCH_EXPORT_TIMEOUT_MS",
	}
	values := []string{"0", "-1", "ten", "1.5", "+5", "", "99999999999"}

	for _, name := range names {
		for _, value := range values {
			t.Run(name+"="+value, func(t *testing.T) {
				_, err := config.Load(lookupFrom(map[string]string{name: value}))

				require.EqualError(t, err, name+" must be a positive integer")
			})
		}
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name, value, message string
	}{
		{"SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS", "yes", "SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS must be either true or false"},
		{"RABBITMQ_URL", "https://rabbitmq.example.com", "RABBITMQ_URL must use amqp or amqps"},
		{"RABBITMQ_URL", "localhost:5672", "RABBITMQ_URL must use amqp or amqps"},
		{"RABBITMQ_URL", "amqp://bad host", "RABBITMQ_URL must be a valid amqp or amqps URL"},
		{"SYNTHESIZE_EXCHANGE", " ", "SYNTHESIZE_EXCHANGE must not be empty"},
		{"SYNTHESIZE_WORK_QUEUE", "", "SYNTHESIZE_WORK_QUEUE must not be empty"},
		{"SYNTHESIZE_DEAD_LETTER_EXCHANGE", "", "SYNTHESIZE_DEAD_LETTER_EXCHANGE must not be empty"},
		{"SYNTHESIZE_DEAD_LETTER_QUEUE", "", "SYNTHESIZE_DEAD_LETTER_QUEUE must not be empty"},
		{"SYNTHESIZE_CALLBACK_ALLOWED_HOSTS", "a.example.com,,b.example.com", "SYNTHESIZE_CALLBACK_ALLOWED_HOSTS must be comma-separated hostnames"},
		{"SYNTHESIZE_CALLBACK_ALLOWED_HOSTS", "https://a.example.com", "SYNTHESIZE_CALLBACK_ALLOWED_HOSTS must be comma-separated hostnames"},
		{"LOGGING_MODE", "XML", "LOGGING_MODE is not an allowed value: PLAIN_TEXT, JSON"},
		{"LOGGING_LEVEL", "trace", "LOGGING_LEVEL is not an allowed value: error, warn, info, debug, verbose"},
	}

	for _, tt := range tests {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			_, err := config.Load(lookupFrom(map[string]string{tt.name: tt.value}))

			require.EqualError(t, err, tt.message)
		})
	}
}

func TestLoad_ValidatesLogShippingOnlyWhenOtelIsEnabled(t *testing.T) {
	t.Run("enabled rejects an unknown value", func(t *testing.T) {
		env := map[string]string{"OTEL_ENABLED": "true", "OTEL_LOG_SHIPPING": "kafka"}

		_, err := config.Load(lookupFrom(env))

		require.EqualError(t, err, "OTEL_LOG_SHIPPING is not an allowed value: otlp, stdout")
	})

	t.Run("disabled ignores it", func(t *testing.T) {
		env := map[string]string{"OTEL_LOG_SHIPPING": "kafka"}

		cfg, err := config.Load(lookupFrom(env))

		require.NoError(t, err)
		assert.False(t, cfg.Otel.Enabled)
	})
}

func TestLoad_OtelEnabledOnlyForExactlyTrue(t *testing.T) {
	cfg, err := config.Load(lookupFrom(map[string]string{"OTEL_ENABLED": "TRUE"}))

	require.NoError(t, err)
	assert.False(t, cfg.Otel.Enabled)
}

func TestLoad_ReportsEveryInvalidVariable(t *testing.T) {
	env := map[string]string{"PORT": "0", "SYNTHESIZE_MAX_ATTEMPTS": "ten", "RABBITMQ_URL": "http://x"}

	_, err := config.Load(lookupFrom(env))

	require.Error(t, err)
	require.ErrorContains(t, err, "PORT must be a positive integer")
	require.ErrorContains(t, err, "SYNTHESIZE_MAX_ATTEMPTS must be a positive integer")
	require.ErrorContains(t, err, "RABBITMQ_URL must use amqp or amqps")
}
