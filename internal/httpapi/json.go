package httpapi

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		writeText(w, http.StatusInternalServerError, "Internal Server Error")

		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	//nolint:errcheck,gosec // a failed write means the client went away; there is nobody to tell.
	w.Write(body)
}
