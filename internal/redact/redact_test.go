package redact_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kasir-barati/piper-tts-rest-api/internal/redact"
)

func TestIsSensitive_MatchesListedNamesOnly(t *testing.T) {
	sensitive := []string{
		"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token",
		"access_token", "Access-Token", "ACCESS_TOKEN", "api_key", "apikey", "jwt", "client_secret", "password",
		"session_id", "sessionid", "AWSAccessKeyId", "Signature", "X-Amz-Signature", "X-Amz-Credential",
		"X-Amz-Security-Token", "X-Goog-Signature", "X-Goog-Credential", "sig", "SIG",
	}
	plain := []string{
		"User-Agent", "Content-Type", "public_key", "public-key", "key", "monkey", "keyword", "author", "design",
		"significant", "X-Amz-Expires", "X-Amz-Date", "X-Amz-Algorithm", "se", "sr", "sv",
		"response-content-disposition", "versionId", "lang",
	}

	for _, name := range sensitive {
		assert.True(t, redact.IsSensitive(name), name)
	}
	for _, name := range plain {
		assert.False(t, redact.IsSensitive(name), name)
	}
}

func TestQuery(t *testing.T) {
	tests := []struct{ in, want string }{
		{"lang=en", "lang=en"},
		{"token=abc123&lang=en", "token=[REDACTED]&lang=en"},
		{"lang=en&Access_Token=abc123", "lang=en&Access_Token=[REDACTED]"},
		{"api%5Fkey=abc123", "api%5Fkey=[REDACTED]"},
		{"sv=2024&sig=abc123&se=2026", "sv=2024&sig=[REDACTED]&se=2026"},
		{"token&lang=en", "token&lang=en"},
		{"bad%zz=1&token=abc123", "bad%zz=1&token=[REDACTED]"},
		{"public_key=abc123&lang=en", "public_key=abc123&lang=en"},
		{"X-Amz-Expires=3600&X-Amz-Signature=abc123", "X-Amz-Expires=3600&X-Amz-Signature=[REDACTED]"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, redact.Query(tt.in))
		})
	}
}
