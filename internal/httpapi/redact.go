package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

const redacted = "[REDACTED]"

// Marks a header or query parameter as secret.
// E.g. authorization, x-api-key, access_token, X-Amz-Signature, session_id
var sensitiveNameParts = []string{
	"auth", "token", "secret", "key", "password", "passwd",
	"signature", "credential", "session", "cookie",
}

// Name is case-insensitive.
func isSensitive(name string) bool {
	lower := strings.ToLower(name)
	for _, part := range sensitiveNameParts {
		if strings.Contains(lower, part) {
			return true
		}
	}

	return false
}

// Returns a copy of headers with the secrets redacted.
func redactHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		if !isSensitive(name) {
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
			return scheme + " " + redacted
		}
	}

	return redacted
}

// Returns the path and query of the url while secrets are redacted.
func redactedRequestURI(u *url.URL) string {
	base := (&url.URL{Path: u.Path, RawPath: u.RawPath}).RequestURI()
	if u.RawQuery == "" {
		return base
	}

	pairs := strings.Split(u.RawQuery, "&")
	for i, pair := range pairs {
		name, _, hasValue := strings.Cut(pair, "=")
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}
		if hasValue && isSensitive(decoded) {
			pairs[i] = name + "=" + redacted
		}
	}

	return base + "?" + strings.Join(pairs, "&")
}
