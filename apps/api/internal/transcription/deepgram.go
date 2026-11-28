// Package transcription provides the server-side speech-to-text adapter.
package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	listenrest "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest"
	response "github.com/deepgram/deepgram-go-sdk/pkg/api/listen/v1/rest/interfaces"
	"github.com/deepgram/deepgram-go-sdk/pkg/client/interfaces"
	sdkerrors "github.com/deepgram/deepgram-go-sdk/pkg/client/interfaces/v1"
	"github.com/deepgram/deepgram-go-sdk/pkg/client/listen"
	"github.com/krishkishore972/clip_ai/internal/media"
	"k8s.io/klog/v2"
)

const requestTimeout = 12 * time.Minute

// DefaultLanguage enables Nova-3 multilingual recognition and code-switching.
// Malayalam is not covered by this mode; callers must explicitly select ml.
const DefaultLanguage = "multi"

var (
	ErrConfiguration    = errors.New("transcription: invalid client configuration")
	ErrInput            = errors.New("transcription: readable nonempty local audio file required")
	ErrProvider         = errors.New("transcription: provider request failed")
	ErrResponse         = errors.New("transcription: provider returned no transcription result")
	ErrLanguage         = errors.New("transcription: language must be multi, te, hi, en, ta, kn or ml")
	ErrProviderAuth     = errors.New("transcription: provider authentication rejected")
	ErrProviderRejected = errors.New("transcription: provider input rejected")
)

// Client wraps Deepgram's pre-recorded REST SDK. It does not enqueue jobs,
// persist transcripts, print user content or load environment files.
type Client struct {
	sdk *listenrest.Client
}

// NewClient accepts the server-side API key. Pass nil for the default HTTP
// transport, or inject an HTTP client for offline tests. The supplied client is
// copied, deadlines are bounded, and redirects are disabled.
//
// SDK request dumping must remain disabled because it includes audio and
// credentials. Do not enable SDK verbose logging in the calling application.
func NewClient(apiKey string, httpClient *http.Client) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n") ||
		os.Getenv("DEEPGRAM_DEBUG_REST") != "" || klog.V(1).Enabled() {
		return nil, ErrConfiguration
	}

	c := listen.NewREST(apiKey, &interfaces.ClientOptions{})
	if c == nil {
		return nil, ErrConfiguration
	}
	if httpClient != nil {
		c.HTTPClient.Client = *httpClient
	}
	if c.HTTPClient.Client.Timeout <= 0 || c.HTTPClient.Client.Timeout > requestTimeout {
		c.HTTPClient.Client.Timeout = requestTimeout
	}
	c.HTTPClient.Client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	transport := c.HTTPClient.Client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	c.HTTPClient.Client.Transport = boundedTransport{next: transport}

	return &Client{sdk: listenrest.New(c)}, nil
}

// TranscribeFile sends an already-prepared local audio file to Deepgram.
// The returned SDK response is internal-only, not a public API contract.
// Provider errors are deliberately not wrapped: they can contain private data.
func (c *Client) TranscribeFile(ctx context.Context, filePath string) (*response.PreRecordedResponse, error) {
	return c.TranscribeFileWithLanguage(ctx, filePath, DefaultLanguage)
}

// ValidLanguage reports whether the language is in the current product scope.
func ValidLanguage(language string) bool {
	switch language {
	case DefaultLanguage, "te", "hi", "en", "ta", "kn", "ml":
		return true
	default:
		return false
	}
}

// TranscribeFileWithLanguage allows an explicit language override. It does not
// combine language detection with a forced language or silently retry in English.
func (c *Client) TranscribeFileWithLanguage(ctx context.Context, filePath, language string) (*response.PreRecordedResponse, error) {
	if c == nil || c.sdk == nil {
		return nil, ErrConfiguration
	}
	if !ValidLanguage(language) {
		return nil, ErrLanguage
	}
	if os.Getenv("DEEPGRAM_DEBUG_REST") != "" || klog.V(1).Enabled() {
		return nil, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 ||
		info.Size() > media.MaxPreparedAudioBytes {
		return nil, ErrInput
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	options := interfaces.PreRecordedTranscriptionOptions{
		Model:       "nova-3",
		Language:    language,
		SmartFormat: true,
		Punctuate:   true,
	}
	result, err := c.sdk.FromFile(ctx, filePath, &options)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, ErrResponse) {
			// Preserve only our closed diagnostic value, not the SDK wrapper,
			// which can contain request/provider details.
			return nil, &outputError{diagnostic: DiagnoseOutput(err)}
		}
		var status *sdkerrors.StatusError
		if errors.As(err, &status) && status.Resp != nil {
			switch status.Resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return nil, ErrProviderAuth
			case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType:
				return nil, ErrProviderRejected
			}
		}
		return nil, ErrProvider
	}
	if result == nil || result.Metadata == nil || result.Results == nil {
		return nil, invalidOutput("RESPONSE_STRUCTURE", -1)
	}
	if len(result.Results.Channels) == 0 {
		return nil, invalidOutput("RESPONSE_CHANNELS", -1)
	}
	if len(result.Results.Channels[0].Alternatives) == 0 {
		return nil, invalidOutput("RESPONSE_ALTERNATIVES", -1)
	}
	return result, nil
}

type boundedTransport struct{ next http.RoundTripper }

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.deepgram.com" {
		return nil, ErrConfiguration
	}
	r.Header.Set("Content-Type", "audio/wav")
	res, err := t.next.RoundTrip(r)
	if err != nil || res == nil || res.Body == nil {
		return res, err
	}
	res.Body = &boundedBody{ReadCloser: res.Body, remaining: MaxDocumentBytes}
	if res.StatusCode == http.StatusOK {
		// The SDK decoder accepts trailing values and replaces invalid UTF-8.
		// Check one complete bounded JSON document before its typed decoding.
		data, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if errors.Is(readErr, ErrResponse) {
			return nil, &outputError{diagnostic: DiagnoseOutput(readErr)}
		}
		if readErr != nil || closeErr != nil {
			return nil, ErrProvider
		}
		if !utf8.Valid(data) {
			return nil, invalidOutput("RESPONSE_UTF8", -1)
		}
		if !json.Valid(data) {
			return nil, invalidOutput("RESPONSE_JSON", -1)
		}
		res.Body = io.NopCloser(bytes.NewReader(data))
	}
	return res, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.ReadCloser.Read(extra[:])
		if n > 0 {
			return 0, invalidOutput("RESPONSE_SIZE_LIMIT", -1)
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}
