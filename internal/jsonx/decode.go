// Package jsonx holds the strict JSON boundary shared by the HTTP and SQS
// adapters: business payloads accept objects and strings only, so monetary
// values never pass through float64, and ambiguous documents are rejected.
package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid JSON document")

// Decode unmarshals data into dst under the strict boundary contract:
// UTF-8, objects and strings only (no numbers, booleans or null), depth
// limit, no duplicate keys regardless of case, no unknown fields and no
// content after the top-level object.
func Decode(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return ErrInvalid
	}
	scanner := json.NewDecoder(bytes.NewReader(data))
	scanner.UseNumber()
	if err := scanObject(scanner, 0); err != nil {
		return ErrInvalid
	}
	if _, err := scanner.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return ErrInvalid
	}
	return nil
}

func scanObject(d *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrInvalid
	}
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalid
	}
	keys := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return ErrInvalid
		}
		name, ok := key.(string)
		if !ok {
			return ErrInvalid
		}
		normalized := strings.ToLower(name)
		if keys[normalized] {
			return ErrInvalid
		}
		keys[normalized] = true
		// Decode the raw value to recurse while retaining its exact bytes.
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return ErrInvalid
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			return ErrInvalid
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
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return ErrInvalid
	}
	return nil
}
