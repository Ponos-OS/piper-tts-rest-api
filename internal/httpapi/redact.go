package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/kasir-barati/piper-tts-rest-api/internal/redact"
)

// Returns a copy of headers with the secrets redacted.
func redactHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		if !redact.IsSensitive(name) {
			out[name] = values

			continue
		}
		lower := strings.ToLower(name)
		redactedValues := make([]string, len(values))
		for i, v := range values {
			redactedValues[i] = redactHeaderValue(lower, v)
		}
		out[name] = redactedValues
	}

	return out
}

func redactHeaderValue(lowerName, value string) string {
	if lowerName == "authorization" || lowerName == "proxy-authorization" {
		if scheme, _, found := strings.Cut(value, " "); found {
			return scheme + " " + redact.Value
		}
	}

	return redact.Value
}

// Returns the path and query of the url while secrets are redacted.
func redactedRequestURI(u *url.URL) string {
	base := (&url.URL{Path: u.Path, RawPath: u.RawPath}).RequestURI()
	if u.RawQuery == "" {
		return base
	}

	return base + "?" + redact.Query(u.RawQuery)
}
