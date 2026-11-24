package curation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/curation"
)

func serviceRequest() curation.Request {
	return curation.Request{
		TranscriptHash: "sha256:" + strings.Repeat("c", 64),
		Words:          []curation.Word{{Index: 0, Text: "తెలుగు", StartMs: 0, EndMs: 1000}},
		Constraints:    curation.Constraints{CandidateCount: 1, MinDurationMs: 100, MaxDurationMs: 1000},
	}
}

func TestCurationClientHTTP(t *testing.T) {
	token := strings.Repeat("x", 32)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/curate" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token ||
			r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("wrong endpoint, authentication or content headers")
		}
		var input curation.Request
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || input.Words[0].Text != "తెలుగు" ||
			input.TranscriptHash != serviceRequest().TranscriptHash || input.Constraints != serviceRequest().Constraints {
			t.Error("wire request differs from contract")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"startWordIndex":0,"endWordIndexExclusive":1,"title":"Title","hook":"Hook","reason":"Reason"}]}`)
	}))
	defer server.Close()
	client, err := curation.NewClient(curation.ClientOptions{URL: server.URL + "/internal/curate", Token: token}, nil)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := client.Curate(context.Background(), serviceRequest())
	if err != nil || len(proposals) != 1 || proposals[0].StartWordIndex != 0 || calls != 1 {
		t.Fatalf("HTTP curation failed: %v", err)
	}
}

func TestCurationClientErrorsAreBoundedAndSafe(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{401, `{"message":"secret"}`, curation.ErrServiceAuth},
		{403, `{"message":"secret"}`, curation.ErrServiceAuth},
		{400, `{"message":"secret"}`, curation.ErrServiceRejected},
		{413, `{"code":"PROMPT_TOO_LARGE"}`, curation.ErrInputTooLarge},
		{503, `{"code":"BUSY"}`, curation.ErrServiceUnavailable},
		{503, `{"code":"CONFIGURATION"}`, curation.ErrServiceRejected},
		{502, `{"code":"INVALID_RESPONSE"}`, curation.ErrResult},
		{502, `{"code":"PROVIDER_FAILED"}`, curation.ErrServiceUnavailable},
		{504, `{"code":"TIMEOUT"}`, context.DeadlineExceeded},
		{429, `{}`, curation.ErrServiceUnavailable},
		{500, `secret`, curation.ErrServiceUnavailable},
		{200, `{"candidates":null}`, curation.ErrResult},
		{200, `{"candidates":[]} {}`, curation.ErrResult},
		{200, `{"candidates":[],"secret":1}`, curation.ErrResult},
		{200, `{"candidates":[{"endWordIndexExclusive":1,"title":"x","hook":"x","reason":"x"}]}`, curation.ErrResult},
		{200, `{"candidates":[{"startWordIndex":0,"endWordIndexExclusive":2,"title":"x","hook":"x","reason":"x"}]}`, curation.ErrResult},
		{200, `{"candidates":[{"startWordIndex":0,"endWordIndexExclusive":1,"title":"x","hook":"x","reason":"x","startMs":0}]}`, curation.ErrResult},
		{200, strings.Repeat(" ", curation.MaxResponseBytes+1), curation.ErrResult},
		{200, "\xff", curation.ErrResult},
	} {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.body[:min(len(tc.body), 30)]), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := curation.NewClient(curation.ClientOptions{URL: server.URL + "/internal/curate", Token: strings.Repeat("t", 32)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Curate(context.Background(), serviceRequest())
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") || calls != 1 {
				t.Fatalf("unsafe error/retry: %v calls=%d", err, calls)
			}
		})
	}
}

func TestCurationClientNoRedirectAndCancellation(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, _ := curation.NewClient(curation.ClientOptions{URL: server.URL + "/internal/curate", Token: strings.Repeat("t", 32)}, nil)
	if _, err := client.Curate(context.Background(), serviceRequest()); !errors.Is(err, curation.ErrServiceRejected) || targetCalls != 0 {
		t.Fatal("redirect followed or accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Curate(ctx, serviceRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer slow.Close()
	client, _ = curation.NewClient(curation.ClientOptions{URL: slow.URL + "/internal/curate", Token: strings.Repeat("t", 32)}, nil)
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Curate(ctx, serviceRequest()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not propagated: %v", err)
	}
}

func TestCurationClientConfigurationAndInput(t *testing.T) {
	token := strings.Repeat("x", 32)
	for _, endpoint := range []string{
		"http://public.example/internal/curate", "https://user:secret@example.com/internal/curate",
		"https://example.com/internal/curate?q=secret", "https://example.com/wrong", "ftp://example.com/internal/curate",
	} {
		if _, err := curation.NewClient(curation.ClientOptions{URL: endpoint, Token: token}, nil); !errors.Is(err, curation.ErrConfiguration) ||
			strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe endpoint accepted or exposed")
		}
	}
	for _, token := range []string{"", "short", strings.Repeat("x", 257), strings.Repeat("x", 32) + "\r\n"} {
		if _, err := curation.NewClient(curation.ClientOptions{URL: "http://localhost:8090/internal/curate", Token: token}, nil); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	client, _ := curation.NewClient(curation.ClientOptions{URL: "https://example.invalid/internal/curate", Token: token}, nil)
	input := serviceRequest()
	input.Words[0].Index = 1
	if _, err := client.Curate(context.Background(), input); !errors.Is(err, curation.ErrResult) {
		t.Fatal("invalid input reached transport")
	}
	input = serviceRequest()
	input.Words = make([]curation.Word, curation.MaxTranscriptWords)
	for i := range input.Words {
		input.Words[i] = curation.Word{Index: i, Text: strings.Repeat("a", 512), StartMs: int64(i), EndMs: int64(i + 1)}
	}
	if _, err := client.Curate(context.Background(), input); !errors.Is(err, curation.ErrInputTooLarge) {
		t.Fatal("oversized request reached transport")
	}
}

func TestCurationClientRejectsUnexpectedResponseEncoding(t *testing.T) {
	for _, encoding := range []string{"wrong-type", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if encoding == "wrong-type" {
					w.Header().Set("Content-Type", "text/plain")
				} else {
					w.Header().Set("Content-Encoding", "gzip")
				}
				_, _ = io.WriteString(w, `{"candidates":[]}`)
			}))
			defer server.Close()
			client, err := curation.NewClient(curation.ClientOptions{URL: server.URL + "/internal/curate", Token: strings.Repeat("t", 32)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Curate(context.Background(), serviceRequest()); !errors.Is(err, curation.ErrResult) {
				t.Fatalf("unexpected encoding accepted: %v", err)
			}
		})
	}
}
