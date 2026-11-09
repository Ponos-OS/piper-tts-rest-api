// Package synthesis holds the domain types of the asynchronous POST /synthesize flow.
package synthesis

import (
	"net/url"
	"slices"
	"strings"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
)

// Rules reported in a Violation for a callback URL.
const (
	RuleAbsolute    = "absolute"
	RuleScheme      = "scheme"
	RuleHTTPS       = "https"
	RuleCredentials = "credentials"
	RuleFragment    = "fragment"
	RuleHost        = "host"
)

// Request is the body of POST /synthesize.
// The `validate` tags are checked when the body is decoded; Normalize checks the URL rules that depend on configuration.
type Request struct {
	Text              string `json:"text"              validate:"required,notblank"`
	GenUploadURL      string `json:"genUploadUrl"      validate:"required,notblank"`
	StatusCallbackURL string `json:"statusCallbackUrl" validate:"required,notblank"`
}

// Violation says which JSON field broke which rule.
type Violation struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
}

// ValidationError lists every callback URL rule a request broke.
type ValidationError struct {
	Violations []Violation
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		parts[i] = v.Field + ": " + v.Rule
	}

	return "invalid callback URLs: " + strings.Join(parts, ", ")
}

// Normalize trims the text and the URLs and checks both callback URLs against cb.
// The returned request holds the normalized URLs (lower-case scheme and host).
func (r Request) Normalize(cb config.Callback) (Request, error) {
	var violations []Violation

	genUploadURL, rule := checkCallbackURL(r.GenUploadURL, cb)
	if rule != "" {
		violations = append(violations, Violation{Field: "genUploadUrl", Rule: rule})
	}
	statusCallbackURL, rule := checkCallbackURL(r.StatusCallbackURL, cb)
	if rule != "" {
		violations = append(violations, Violation{Field: "statusCallbackUrl", Rule: rule})
	}
	if len(violations) > 0 {
		return Request{}, &ValidationError{Violations: violations}
	}

	return Request{
		Text:              strings.TrimSpace(r.Text),
		GenUploadURL:      genUploadURL,
		StatusCallbackURL: statusCallbackURL,
	}, nil
}

// checkCallbackURL returns the normalized URL, or the first rule it breaks.
func checkCallbackURL(raw string, cb config.Callback) (string, string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", RuleAbsolute
	}
	if rule := brokenRule(u, cb); rule != "" {
		return "", rule
	}

	u.Host = strings.ToLower(u.Host)

	return u.String(), ""
}

func brokenRule(u *url.URL, cb config.Callback) string {
	switch {
	case u.Scheme == "":
		return RuleAbsolute
	case u.Scheme != "http" && u.Scheme != "https":
		return RuleScheme
	case u.Host == "":
		return RuleAbsolute
	case cb.RequireHTTPS && u.Scheme != "https":
		return RuleHTTPS
	case u.User != nil:
		return RuleCredentials
	case u.Fragment != "":
		return RuleFragment
	case hostNotAllowed(u, cb):
		return RuleHost
	}

	return ""
}

func hostNotAllowed(u *url.URL, cb config.Callback) bool {
	return len(cb.AllowedHosts) > 0 && !slices.Contains(cb.AllowedHosts, strings.ToLower(u.Hostname()))
}
