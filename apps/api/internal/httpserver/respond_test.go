package httpserver

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type payload struct {
	Name string `json:"name"`
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusCreated, payload{Name: "clip"}); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	var got payload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if got.Name != "clip" {
		t.Errorf("body = %+v, want name clip", got)
	}
}

func TestWriteJSONNilWritesNoBody(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusNoContent, nil); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		body        string
		want        payload
		wantErr     error
		wantAnyErr  bool
	}{
		{
			name:        "valid",
			contentType: "application/json",
			body:        `{"name":"clip"}`,
			want:        payload{Name: "clip"},
		},
		{
			name:        "charset parameter",
			contentType: "application/json; charset=utf-8",
			body:        `{"name":"clip"}`,
			want:        payload{Name: "clip"},
		},
		{
			name:        "wrong content type",
			contentType: "text/plain",
			body:        `{}`,
			wantErr:     ErrUnsupportedMediaType,
		},
		{
			name:    "missing content type",
			body:    `{}`,
			wantErr: ErrUnsupportedMediaType,
		},
		{
			name:        "unknown field",
			contentType: "application/json",
			body:        `{"nope":1}`,
			wantAnyErr:  true,
		},
		{
			name:        "trailing data",
			contentType: "application/json",
			body:        `{"name":"a"}{"name":"b"}`,
			wantErr:     ErrTrailingData,
		},
		{
			name:        "malformed json",
			contentType: "application/json",
			body:        `{"name":`,
			wantAnyErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()

			got, err := DecodeJSON[payload](rec, req)

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantAnyErr:
				if err == nil {
					t.Fatal("error = nil, want an error")
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tt.want {
					t.Errorf("decoded = %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	body := `{"name":"` + strings.Repeat("a", int(MaxBodyBytes)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	_, err := DecodeJSON[payload](httptest.NewRecorder(), req)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("error = %v, want %v", err, ErrBodyTooLarge)
	}
}

func TestDecodeJSONSuffix(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		suffix string
		want   error
	}{
		{"whitespace", " \r\n\t", nil},
		{"exact size limit", strings.Repeat(" ", int(MaxBodyBytes)-len(`{"name":"clip"}`)), nil},
		{"closing array", "]", ErrTrailingData},
		{"closing object", "}", ErrTrailingData},
		{"garbage", "oops", ErrTrailingData},
		{"incomplete value", "{", ErrTrailingData},
		{"extra null", "null", ErrTrailingData},
		{"extra scalar", "42", ErrTrailingData},
		{"oversized whitespace", strings.Repeat(" ", int(MaxBodyBytes)), ErrBodyTooLarge},
		{"oversized second value", `"` + strings.Repeat("a", int(MaxBodyBytes)) + `"`, ErrBodyTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"clip"}`+tt.suffix))
			req.Header.Set("Content-Type", "application/json")
			_, err := DecodeJSON[payload](httptest.NewRecorder(), req)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestWriteJSONEncodingFailure(t *testing.T) {
	t.Parallel()
	for _, value := range []any{make(chan int), math.NaN()} {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Length", "1")
		if err := WriteJSON(rec, http.StatusCreated, value); err == nil {
			t.Fatal("expected encoding error")
		}
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if rec.Header().Get("Content-Length") != "" {
			t.Fatal("stale content length retained")
		}
		var body APIError
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid fallback JSON: %v", err)
		}
		if body.Code != "internal_error" || body.Message != "internal server error" {
			t.Fatalf("unexpected error body: %+v", body)
		}
	}
}

type failingWriter struct {
	header   http.Header
	statuses []int
	writes   int
	err      error
}

func (w *failingWriter) Header() http.Header    { return w.header }
func (w *failingWriter) WriteHeader(status int) { w.statuses = append(w.statuses, status) }
func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	return 0, w.err
}

func TestWriteJSONWriteFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("connection failed")
	w := &failingWriter{header: make(http.Header), err: want}
	if err := WriteJSON(w, http.StatusCreated, payload{Name: "clip"}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if len(w.statuses) != 1 || w.statuses[0] != http.StatusCreated || w.writes != 1 {
		t.Fatalf("attempted another response: statuses=%v writes=%d", w.statuses, w.writes)
	}
}

func TestWriteErrorEncodingFailure(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	err := WriteError(w, httptest.NewRequest(http.MethodGet, "/", nil),
		http.StatusBadRequest, "bad_request", "bad request", make(chan int))
	if err == nil || w.Code != http.StatusInternalServerError || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("error = %v, status = %d, body = %q", err, w.Code, w.Body.String())
	}
}
