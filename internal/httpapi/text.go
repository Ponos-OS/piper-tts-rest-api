// Package httpapi holds the HTTP server wiring: routes, middleware and the
// server lifecycle.
package httpapi

import "net/http"

func writeText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	//nolint:errcheck,gosec // a failed write means the client went away; there is nobody to tell.
	w.Write([]byte(text))
}
