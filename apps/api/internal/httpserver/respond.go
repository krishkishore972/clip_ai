package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
)

// MaxBodyBytes caps how much of a JSON request body DecodeJSON will read.
// Endpoints that accept media uploads must use their own, larger limit.
const MaxBodyBytes int64 = 1 << 20 // 1 MiB

// DecodeJSON errors that handlers map to HTTP status codes.
var (
	ErrUnsupportedMediaType = errors.New("content type must be application/json")
	ErrBodyTooLarge         = errors.New("request body too large")
	ErrTrailingData         = errors.New("request body must contain a single JSON value")
)

// WriteJSON writes v as JSON with the given status.
//
// If encoding fails after the status line is sent the response cannot be
// repaired; callers should only pass values that are known to marshal.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if v == nil {
		w.WriteHeader(status)
		return
	}

	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON reads exactly one JSON value into T.
//
// It rejects non-JSON content types, unknown fields, bodies over
// MaxBodyBytes, and trailing data after the first value.
func DecodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var v T

	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return v, ErrUnsupportedMediaType
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return v, ErrBodyTooLarge
		}
		return v, fmt.Errorf("decode json: %w", err)
	}

	if dec.More() {
		return v, ErrTrailingData
	}

	return v, nil
}
