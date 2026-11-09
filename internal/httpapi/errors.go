package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
)

type errorBody struct {
	Error  string       `json:"error"`
	Fields []fieldError `json:"fields,omitempty"`
}

// writeBodyError answers the errors of decodeJSON.
func writeBodyError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var invalid *validationError

	switch {
	case errors.Is(err, errBodyTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{Error: "Request body too large"})
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "Invalid request body", Fields: invalid.Fields})
	case errors.Is(err, errInvalidJSON):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "Invalid JSON body"})
	default:
		log.ErrorContext(r.Context(), "Reading the request body failed", slog.Any(keyError, err))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "Internal Server Error"})
	}
}
