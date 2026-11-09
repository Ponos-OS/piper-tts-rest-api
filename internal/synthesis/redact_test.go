package synthesis_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

func TestRedactUploadURL(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{
			"AWS SigV4",
			"https://s3.amazonaws.com/b/o?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20260819T120000Z&X-Amz-Expires=3600&X-Amz-Credential=AKIA%2F20260819%2Fs3&X-Amz-Signature=abc123",
			"https://s3.amazonaws.com/b/o?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20260819T120000Z&X-Amz-Expires=3600&X-Amz-Credential=[REDACTED]&X-Amz-Signature=[REDACTED]",
		},
		{
			"MinIO",
			"https://minio.example.com/b/o?X-Amz-Credential=MINIOKEY%2F20260819&X-Amz-Signature=abc123&X-Amz-Expires=86400",
			"https://minio.example.com/b/o?X-Amz-Credential=[REDACTED]&X-Amz-Signature=[REDACTED]&X-Amz-Expires=86400",
		},
		{
			"GCS",
			"https://storage.googleapis.com/b/o?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Signature=abc123&X-Goog-Expires=3600",
			"https://storage.googleapis.com/b/o?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Signature=[REDACTED]&X-Goog-Expires=3600",
		},
		{
			"Azure SAS",
			"https://acct.blob.core.windows.net/c/o?sv=2024&se=2026-08-19T12%3A00%3A00Z&sr=b&sig=abc123",
			"https://acct.blob.core.windows.net/c/o?sv=2024&se=2026-08-19T12%3A00%3A00Z&sr=b&sig=[REDACTED]",
		},
		{
			"generic provider",
			"https://generic.example/bucket/file?token=abc123&sig=abc123&other=keep",
			"https://generic.example/bucket/file?token=[REDACTED]&sig=[REDACTED]&other=keep",
		},
		{
			"nothing sensitive",
			"https://s3.amazonaws.com/b/o?versionId=123&response-content-disposition=inline",
			"https://s3.amazonaws.com/b/o?versionId=123&response-content-disposition=inline",
		},
		{"no query", "https://s3.amazonaws.com/b/o", "https://s3.amazonaws.com/b/o"},
		{"user info", "https://user:abc123@storage.example/b/o?x=1", "https://storage.example/b/o?x=1"},
		{"port and escaped path", "http://localhost:9000/my%20bucket/o?x=1", "http://localhost:9000/my%20bucket/o?x=1"},
		{"fragment", "https://storage.example/b/o#abc123", "https://storage.example/b/o"},
		{"not a URL", "not-a-valid-url", "not-a-valid-url"},
		{"empty", "", ""},
		{"unparsable", "http://[::1", "http://[::1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := synthesis.RedactUploadURL(tt.in)

			assert.Equal(t, tt.want, got)
			if tt.name != "not a URL" && tt.name != "unparsable" {
				assert.NotContains(t, got, "abc123")
			}
		})
	}
}
