package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
)

// maxJSONBody bounds request bodies the API reads into memory. Every JSON
// request here is a handful of short fields, so 64 KiB is generous; without a
// limit anyone who can reach the port could make the server buffer gigabytes.
const maxJSONBody = 64 << 10

// decodeJSON reads a size-limited JSON body into v. On failure it has already
// written the response (413 for an oversized body, 400 otherwise) and reports
// false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
