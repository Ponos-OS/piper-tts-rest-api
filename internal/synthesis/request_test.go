package synthesis_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

func request(upload, status string) synthesis.Request {
	return synthesis.Request{Text: "Hello", GenUploadURL: upload, StatusCallbackURL: status}
}

func TestNormalize_ReturnsTrimmedRequest(t *testing.T) {
	cb := config.Callback{RequireHTTPS: true, AllowedHosts: []string{"callbacks.example.com"}}
	in := synthesis.Request{
		Text:              "  Hello world  ",
		GenUploadURL:      " https://Callbacks.Example.com/uploads ",
		StatusCallbackURL: "https://callbacks.example.com:8443/status?a=1",
	}

	got, err := in.Normalize(cb)

	require.NoError(t, err)
	assert.Equal(t, synthesis.Request{
		Text:              "Hello world",
		GenUploadURL:      "https://callbacks.example.com/uploads",
		StatusCallbackURL: "https://callbacks.example.com:8443/status?a=1",
	}, got)
}

func TestNormalize_RejectsCallbackURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
		cb   config.Callback
		rule string
	}{
		{"relative", "/status", config.Callback{}, synthesis.RuleAbsolute},
		{"no host", "https:///status", config.Callback{}, synthesis.RuleAbsolute},
		{"not a URL", "::", config.Callback{}, synthesis.RuleAbsolute},
		{"other scheme", "ftp://callbacks.example.com/status", config.Callback{}, synthesis.RuleScheme},
		{"http when https is required", "http://callbacks.example.com/status", config.Callback{RequireHTTPS: true}, synthesis.RuleHTTPS},
		{"credentials", "https://user:pass@callbacks.example.com/status", config.Callback{}, synthesis.RuleCredentials},
		{"user without password", "https://user@callbacks.example.com/status", config.Callback{}, synthesis.RuleCredentials},
		{"fragment", "https://callbacks.example.com/status#top", config.Callback{}, synthesis.RuleFragment},
		{"host not allowed", "https://other.example.com/status", config.Callback{AllowedHosts: []string{"callbacks.example.com"}}, synthesis.RuleHost},
		{"host only matches by hostname", "https://callbacks.example.com.evil.test/status", config.Callback{AllowedHosts: []string{"callbacks.example.com"}}, synthesis.RuleHost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := request("https://callbacks.example.com/upload", tt.url).Normalize(tt.cb)

			var invalid *synthesis.ValidationError
			require.ErrorAs(t, err, &invalid)
			assert.Equal(t, []synthesis.Violation{{Field: "statusCallbackUrl", Rule: tt.rule}}, invalid.Violations)
		})
	}
}

func TestNormalize_AllowsHTTPWhenHTTPSIsNotRequired(t *testing.T) {
	got, err := request("http://localhost:9000/upload", "http://localhost:9000/status").Normalize(config.Callback{})

	require.NoError(t, err)
	assert.Equal(t, "http://localhost:9000/upload", got.GenUploadURL)
}

func TestNormalize_ReportsBothURLs(t *testing.T) {
	_, err := request("/upload", "ftp://callbacks.example.com/status").Normalize(config.Callback{})

	var invalid *synthesis.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []synthesis.Violation{
		{Field: "genUploadUrl", Rule: synthesis.RuleAbsolute},
		{Field: "statusCallbackUrl", Rule: synthesis.RuleScheme},
	}, invalid.Violations)
	assert.EqualError(t, err, "invalid callback URLs: genUploadUrl: absolute, statusCallbackUrl: scheme")
}
