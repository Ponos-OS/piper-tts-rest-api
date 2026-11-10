package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
)

// Codes of the "code" key, the same as the TypeScript version used.
const (
	codeInvalidRequest     = "invalid-request"
	codeInvalidCallbackURL = "invalid-callback-url"
	codeBodyTooLarge       = "request-body-too-large"
)

// errorBody is the JSON body of an error response.
// Code is a stable machine-readable value; Fields says which JSON fields broke which rule.
type errorBody struct {
	Error  string       `json:"error"`
	Code   string       `json:"code,omitempty"`
	Fields []fieldError `json:"fields,omitempty"`
}

// writeBodyError answers the errors of reading and decoding a request body.
func writeBodyError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var invalid *validationError

	switch {
	case errors.Is(err, errBodyTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{Error: "Request body too large", Code: codeBodyTooLarge})
	case errors.Is(err, errNotObject):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "Body must be a JSON object", Code: codeInvalidRequest})
	case errors.Is(err, errInvalidJSON):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "Invalid JSON body", Code: codeInvalidRequest})
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "Invalid request body", Code: codeInvalidRequest, Fields: invalid.Fields})
	default:
		log.ErrorContext(r.Context(), "Reading the request body failed", slog.Any(keyError, err))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "Internal Server Error"})
	}
}
