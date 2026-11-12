package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// Values are marshaled before committing headers. Encoding failures produce a
// generic 500; the returned error retains the cause for server-side handling.
// Write failures are returned without attempting another response.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	if v == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		return nil
	}

	body, err := json.Marshal(v)
	if err != nil {
		err = fmt.Errorf("encode json: %w", err)
		status = http.StatusInternalServerError
		body = []byte(`{"code":"internal_error","message":"internal server error"}`)
		w.Header().Del("Content-Length")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, writeErr := w.Write(append(body, '\n')); writeErr != nil {
		return errors.Join(err, fmt.Errorf("write json: %w", writeErr))
	}
	return err
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

	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return v, ErrBodyTooLarge
		}
		return v, ErrTrailingData
	}

	return v, nil
}
