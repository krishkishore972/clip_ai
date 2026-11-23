package transcription_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/transcription"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func localAudio(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(path, []byte("fixture-audio"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newClient(t *testing.T, transport roundTripFunc) *transcription.Client {
	t.Helper()
	c, err := transcription.NewClient("test-key", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTranscribeLocalFile(t *testing.T) {
	calls := 0
	c := newClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Scheme != "https" ||
			r.URL.Host != "api.deepgram.com" || r.URL.Path != "/v1/listen" {
			t.Fatalf("unexpected request destination: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "token test-key" {
			t.Fatal("missing SDK authentication")
		}
		for key, want := range map[string]string{
			"model": "nova-3", "language": "multi", "smart_format": "true", "punctuate": "true",
		} {
			if r.URL.Query().Get(key) != want {
				t.Fatalf("unexpected %s option", key)
			}
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 12*time.Minute {
			t.Fatal("request deadline is not bounded")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "fixture-audio" {
			t.Fatal("SDK did not send local file bytes")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"metadata": {"duration": 1},
				"results": {"channels": [{"alternatives": [{
					"transcript": "Hello.",
					"words": [{"word": "hello", "start": 0.1, "end": 0.5, "confidence": 0.9}]
				}]}]}
			}`)),
		}, nil
	})
	path := localAudio(t)
	result, err := c.TranscribeFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Results.Channels[0].Alternatives[0].Transcript != "Hello." {
		t.Fatal("unexpected SDK result")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "fixture-audio" {
		t.Fatal("source audio changed")
	}
}

func TestTranscribeLanguageOverrides(t *testing.T) {
	for _, language := range []string{"multi", "te", "hi", "en", "ta", "kn", "ml"} {
		t.Run(language, func(t *testing.T) {
			c := newClient(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Query().Get("language") != language {
					t.Fatal("explicit language did not reach the SDK request")
				}
				if r.URL.Query().Has("detect_language") {
					t.Fatal("language detection overrides the explicit language")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"metadata": {"duration": 1},
						"results": {"channels": [{"alternatives": [{"transcript": "", "words": []}]}]}
					}`)),
				}, nil
			})
			if _, err := c.TranscribeFileWithLanguage(context.Background(), localAudio(t), language); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTranscribeRejectsUnsupportedLanguage(t *testing.T) {
	c := newClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported language reached the network")
		return nil, nil
	})
	for _, language := range []string{"", "us", "unknown", "en&model=other"} {
		if _, err := c.TranscribeFileWithLanguage(context.Background(), "unused.wav", language); !errors.Is(err, transcription.ErrLanguage) {
			t.Fatalf("unsupported language accepted: %v", err)
		}
	}
}

func TestClientRejectsInvalidConfiguration(t *testing.T) {
	for _, key := range []string{"", " ", "key\ninjected"} {
		if _, err := transcription.NewClient(key, nil); !errors.Is(err, transcription.ErrConfiguration) {
			t.Fatalf("invalid key accepted: %v", err)
		}
	}
	t.Setenv("DEEPGRAM_DEBUG_REST", "1")
	if _, err := transcription.NewClient("test-key", nil); !errors.Is(err, transcription.ErrConfiguration) {
		t.Fatal("SDK request dumping was allowed")
	}
}

func TestTranscribeRejectsInvalidInputAndCancellation(t *testing.T) {
	c := newClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid input reached the network")
		return nil, nil
	})
	empty := filepath.Join(t.TempDir(), "empty.wav")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.wav"), t.TempDir(), empty} {
		if _, err := c.TranscribeFile(context.Background(), path); !errors.Is(err, transcription.ErrInput) {
			t.Fatalf("invalid path accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.TranscribeFile(ctx, localAudio(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestTranscribePreservesInFlightCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newClient(t, func(r *http.Request) (*http.Response, error) {
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	if _, err := c.TranscribeFile(ctx, localAudio(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation lost: %v", err)
	}
}

func TestTranscribeSanitizesTransportErrors(t *testing.T) {
	c := newClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private transport and credential details")
	})
	if _, err := c.TranscribeFile(context.Background(), localAudio(t)); !errors.Is(err, transcription.ErrProvider) {
		t.Fatalf("unexpected safe transport error: %v", err)
	}
}

func TestTranscribeSanitizesFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want error
	}{
		{"authentication", 401, "private-provider-data", transcription.ErrProviderAuth},
		{"forbidden", 403, "private-provider-data", transcription.ErrProviderAuth},
		{"rejected", 400, `{"err_code":"INVALID_QUERY_PARAMETER","err_msg":"private"}`, transcription.ErrProviderRejected},
		{"rate limit", 429, "private-provider-data", transcription.ErrProvider},
		{"provider failure", 500, "private-provider-data", transcription.ErrProvider},
		{"malformed JSON", 200, "private-provider-data", transcription.ErrResponse},
		{"trailing JSON", 200, `{} {}`, transcription.ErrResponse},
		{"invalid UTF8", 200, "{\"text\":\"\xff\"}", transcription.ErrResponse},
		{"missing results", 200, `{}`, transcription.ErrResponse},
		{"redirect", 307, "", transcription.ErrProvider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := newClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: tc.code,
					Header:     http.Header{"Location": []string{"https://other.invalid"}},
					Body:       io.NopCloser(strings.NewReader(tc.body)),
				}, nil
			})
			if _, err := c.TranscribeFile(context.Background(), localAudio(t)); !errors.Is(err, tc.want) {
				t.Fatalf("unexpected safe error: %v", err)
			}
			if calls != 1 {
				t.Fatal("SDK request was retried or redirected")
			}
		})
	}
}

func TestTranscribeBoundsSDKResponse(t *testing.T) {
	c := newClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(io.LimitReader(repeatingByte(' '), transcription.MaxDocumentBytes+1)),
		}, nil
	})
	if _, err := c.TranscribeFile(context.Background(), localAudio(t)); !errors.Is(err, transcription.ErrResponse) {
		t.Fatalf("oversized SDK response accepted: %v", err)
	}
}

type repeatingByte byte

func (b repeatingByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}
