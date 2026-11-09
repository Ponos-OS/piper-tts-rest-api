// Package redact hides secrets in values that are about to be logged.
package redact

import (
	"net/url"
	"strings"
)

// Value replaces a secret.
const Value = "[REDACTED]"

// sensitiveNames are the header and query parameter names whose values are secret.
var sensitiveNames = map[string]struct{}{
	"apikey":               {},
	"api-key":              {},
	"assertion":            {},
	"access-token":         {},
	"authorization":        {},
	"awsaccesskeyid":       {},
	"cookie":               {},
	"client-secret":        {},
	"passwd":               {},
	"password":             {},
	"proxy-authorization":  {},
	"x-api-key":            {},
	"x-auth-token":         {},
	"x-access-token":       {},
	"x-csrf-token":         {},
	"x-xsrf-token":         {},
	"x-amz-signature":      {},
	"x-amz-credential":     {},
	"x-amz-security-token": {},
	"x-goog-signature":     {},
	"x-goog-credential":    {},
	"token":                {},
	"id-token":             {},
	"refresh-token":        {},
	"jwt":                  {},
	"secret":               {},
	"session":              {},
	"session-id":           {},
	"sessionid":            {},
	"signature":            {},
	"set-cookie":           {},
	"sig":                  {},
}

// IsSensitive reports whether a header or query parameter name is on the list.
// The name is case-insensitive, and "_" and "-" are the same.
func IsSensitive(name string) bool {
	_, found := sensitiveNames[strings.ReplaceAll(strings.ToLower(name), "_", "-")]

	return found
}

// Query returns a raw query string with the values of secret parameters replaced.
// Everything else keeps its order and encoding.
func Query(rawQuery string) string {
	pairs := strings.Split(rawQuery, "&")
	for i, pair := range pairs {
		name, _, hasValue := strings.Cut(pair, "=")
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}
		if hasValue && IsSensitive(decoded) {
			pairs[i] = name + "=" + Value
		}
	}

	return strings.Join(pairs, "&")
}
