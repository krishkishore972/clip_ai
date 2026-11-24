package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadPreferenceHTTPValidation(t *testing.T) {
	t.Parallel()
	for name, preferences := range map[string]string{
		"missing":  "",
		"null":     `,"clipPreferences":null`,
		"empty":    `,"clipPreferences":{}`,
		"count":    `,"clipPreferences":{"candidateCount":6,"minDurationMs":15000,"maxDurationMs":60000}`,
		"inverted": `,"clipPreferences":{"candidateCount":1,"minDurationMs":60000,"maxDurationMs":15000}`,
		"too long": `,"clipPreferences":{"candidateCount":1,"minDurationMs":15000,"maxDurationMs":60001}`,
	} {
		t.Run(name, func(t *testing.T) {
			service := &uploadServiceStub{}
			body := `{"filename":"sample.mp4","sizeBytes":1024,"contentType":"video/mp4"` + preferences + `}`
			w := httptest.NewRecorder()
			uploadRouter(service).ServeHTTP(w, request(http.MethodPost, "/uploads", body))
			assertAPIError(t, w, http.StatusBadRequest, "invalid_clip_preferences")
			if service.calls != 0 {
				t.Fatal("invalid preferences reached upload service")
			}
		})
	}
}

func TestUploadCompletionCannotReplacePreferences(t *testing.T) {
	t.Parallel()
	service := &uploadServiceStub{}
	w := httptest.NewRecorder()
	uploadRouter(service).ServeHTTP(w, request(
		http.MethodPost,
		"/uploads/session-1/complete",
		`{"parts":[{"partNumber":1,"eTag":"tag"}],"clipPreferences":{"candidateCount":1,"minDurationMs":1,"maxDurationMs":1}}`,
	))
	assertAPIError(t, w, http.StatusBadRequest, "invalid_json")
	if service.calls != 0 {
		t.Fatal("completion accepted replacement preferences")
	}
}
