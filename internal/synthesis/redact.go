package synthesis

import (
	"net/url"

	"github.com/kasir-barati/piper-tts-rest-api/internal/redact"
)

// RedactUploadURL returns a presigned URL for logging while redacting secrets
// A URL that cannot be parsed is returned as is: the operator needs to see what the caller sent.
func RedactUploadURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}

	redacted := u.Scheme + "://" + u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		redacted += "?" + redact.Query(u.RawQuery)
	}

	return redacted
}
