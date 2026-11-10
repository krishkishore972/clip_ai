package httpserver

import (
	"encoding/json"
	"errors"
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
	WriteJSON(rec, http.StatusCreated, payload{Name: "clip"})

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
	WriteJSON(rec, http.StatusNoContent, nil)

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
