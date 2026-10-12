package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/jsonx"
)

// errInvalidJSON keeps the HTTP error classification stable; the strict
// decoding rules live in jsonx, shared with the SQS boundary.
var errInvalidJSON = errors.New("invalid request JSON")

const maxRequestBytes = 64 << 10

// decodeRequest applies the HTTP-specific guards (content type, size) and the
// shared strict JSON boundary: objects and strings only, so no monetary value
// can pass through float64, and ambiguous duplicates are rejected.
func decodeRequest(w http.ResponseWriter, r *http.Request, destination any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errInvalidJSON
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		return errInvalidJSON
	}
	if err := jsonx.Decode(data, destination); err != nil {
		return errInvalidJSON
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "response encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(encoded, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, message, correlation string) {
	writeJSON(w, status, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		CorrelationID string `json:"correlationId"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message}, CorrelationID: correlation})
}
