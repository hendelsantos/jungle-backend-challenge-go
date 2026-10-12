package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

var errInvalidJSON = errors.New("invalid request JSON")

const maxRequestBytes = 64 << 10

// All input fields in these contracts are objects or strings. Validate tokens
// without float conversion; reject ambiguous duplicates, null and extra values.
func decodeRequest(w http.ResponseWriter, r *http.Request, destination any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errInvalidJSON
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil || !utf8.Valid(data) {
		return errInvalidJSON
	}
	scanner := json.NewDecoder(bytes.NewReader(data))
	scanner.UseNumber()
	if err := scanObject(scanner, 0); err != nil {
		return errInvalidJSON
	}
	if _, err := scanner.Token(); !errors.Is(err, io.EOF) {
		return errInvalidJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errInvalidJSON
	}
	return nil
}

func scanObject(d *json.Decoder, depth int) error {
	if depth > 8 {
		return errInvalidJSON
	}
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errInvalidJSON
	}
	keys := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return errInvalidJSON
		}
		name, ok := key.(string)
		if !ok {
			return errInvalidJSON
		}
		normalized := strings.ToLower(name)
		if keys[normalized] {
			return errInvalidJSON
		}
		keys[normalized] = true
		// Decode the raw value to recurse while retaining its exact bytes.
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return errInvalidJSON
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			return errInvalidJSON
		}
		switch raw[0] {
		case '{':
			child := json.NewDecoder(bytes.NewReader(raw))
			child.UseNumber()
			if err := scanObject(child, depth+1); err != nil {
				return err
			}
		case '"':
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return errInvalidJSON
			}
		default:
			return errInvalidJSON
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
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
