package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/upload"
	"github.com/krishkishore972/clip_ai/internal/auth"
)

// This simulates S3 metadata only. It does NOT prove real video transfer or S3
// connectivity; it exercises the real handlers, service, tokens and HTTP stack.
type controlFlowStore struct {
	mu        sync.Mutex
	key       string
	mediaType string
	parts     []storage.Part
	completed bool
	aborted   bool
	calls     int
}

func (s *controlFlowStore) SaveCompleted(ctx context.Context,_ upload.CompletedAsset) error {
	if _, ok := auth.FromContext(ctx); !ok { return upload.ErrUnauthenticated }; return nil
}

func (s *controlFlowStore) Begin(_ context.Context, key, mediaType string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.key, s.mediaType = key, mediaType
	return "aws-id", nil
}

func (s *controlFlowStore) PresignPart(_ context.Context, _, _ string, _ int32, _ int64, ttl ...time.Duration) (storage.PresignedPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return storage.PresignedPart{URL: "https://example.invalid/part", Method: http.MethodPut,
		Headers: http.Header{}, ExpiresAt: time.Now().UTC().Add(ttl[0])}, nil
}

func (s *controlFlowStore) ListParts(_ context.Context, key, _ string) ([]storage.Part, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.completed || s.aborted || key != s.key {
		return nil, storage.ErrUploadNotFound
	}
	return slices.Clone(s.parts), nil
}

func (s *controlFlowStore) Complete(_ context.Context, _, _ string, _ []storage.Part) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.completed = true
	return nil
}

func (s *controlFlowStore) Abort(_ context.Context, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.aborted {
		return storage.ErrUploadNotFound
	}
	s.aborted = true
	return nil
}

func (s *controlFlowStore) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if !s.completed || s.key != key {
		return storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	var size int64
	for _, p := range s.parts {
		size += p.SizeBytes
	}
	return storage.ObjectInfo{SizeBytes: size, ContentType: s.mediaType}, nil
}

func TestStatelessUploadHTTPControlFlow(t *testing.T) {
	t.Parallel()
	store := &controlFlowStore{}
	service, err := upload.New(store, upload.Config{Assets:store,Bucket:"test-bucket",SigningSecret: []byte(strings.Repeat("test-secret-", 4)),
		Scope: "test-region/test-bucket", TokenTTL: time.Hour, PresignTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
		uploadRouter(service).ServeHTTP(w,r.WithContext(auth.WithPrincipal(r.Context(),auth.Principal{UserID:"test-user", WorkspaceID:"test-workspace"})))
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	do := func(method, path, token string, body any, want int) []byte {
		t.Helper()
		var encoded []byte
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Upload-Token", token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d err=%v", method, path, response.StatusCode, want, err)
		}
		if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Request-Id") == "" {
			t.Fatal("missing control response headers")
		}
		return data
	}
	var created upload.UploadSession
	data := do(http.MethodPost, "/uploads", "", upload.CreateUploadRequest{
		Filename: "sample.mp4", ContentType: "video/mp4", SizeBytes: upload.PartSizeBytes + 1024,
	}, http.StatusCreated)
	if err := json.Unmarshal(data, &created); err != nil || created.UploadToken == "" || created.PartCount != 2 {
		t.Fatal("invalid create response")
	}
	base := "/uploads/" + created.UploadID
	do(http.MethodPost, base+"/parts/sign", "", upload.SignUploadPartsRequest{PartNumbers: []int{1}}, http.StatusForbidden)
	store.mu.Lock()
	before := store.calls
	store.mu.Unlock()
	do(http.MethodPost, base+"/parts/sign", created.UploadToken+"x", upload.SignUploadPartsRequest{PartNumbers: []int{1}}, http.StatusForbidden)
	store.mu.Lock()
	if store.calls != before {
		t.Error("tampered token reached storage")
	}
	store.mu.Unlock()
	var signed upload.SignedUploadParts
	data = do(http.MethodPost, base+"/parts/sign", created.UploadToken,
		upload.SignUploadPartsRequest{PartNumbers: []int{1, 2}}, http.StatusOK)
	if err := json.Unmarshal(data, &signed); err != nil || len(signed.Parts) != 2 {
		t.Fatal("invalid signing response")
	}
	// Simulate metadata from the client's successful PUTs, not video transfer.
	store.mu.Lock()
	store.parts = []storage.Part{{Number: 1, SizeBytes: upload.PartSizeBytes, ETag: `"tag-1"`}, {Number: 2, SizeBytes: 1024, ETag: `"tag-2"`}}
	store.mu.Unlock()
	var progress upload.UploadSession
	data = do(http.MethodGet, base, created.UploadToken, nil, http.StatusOK)
	if err := json.Unmarshal(data, &progress); err != nil || len(progress.UploadedParts) != 2 {
		t.Fatal("status did not report S3 parts")
	}
	in := upload.CompleteUploadRequest{Parts: []upload.CompleteUploadPart{
		{PartNumber: 1, ETag: `"tag-1"`}, {PartNumber: 2, ETag: `"tag-2"`},
	}}
	for range 2 {
		var completed upload.UploadSession
		data = do(http.MethodPost, base+"/complete", created.UploadToken, in, http.StatusOK)
		if err := json.Unmarshal(data, &completed); err != nil || completed.Status != "uploaded_unvalidated" {
			t.Fatal("completion did not preserve validation gate")
		}
	}
	do(http.MethodDelete, base, created.UploadToken, nil, http.StatusConflict)
}
