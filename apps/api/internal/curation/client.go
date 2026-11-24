package curation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/krishkishore972/clip_ai/internal/config"
)

const (
	ServiceTimeout   = 70 * time.Second // Allows the LLM's 65-second overall budget.
	MaxRequestBytes  = 1024 * 1024
	MaxResponseBytes = 64 * 1024
)

var (
	ErrServiceAuth        = errors.New("curation: service authentication failed")
	ErrServiceRejected    = errors.New("curation: service rejected request")
	ErrServiceUnavailable = errors.New("curation: service unavailable")
	ErrInputTooLarge      = errors.New("curation: input too large")
)

// ClientOptions contains a private bearer token; never log it.
type ClientOptions struct{ URL, Token string }

func (o ClientOptions) Validate() error {
	if err := config.ValidateCurationService(o.URL, o.Token); err != nil {
		return ErrConfiguration
	}
	return nil
}

// Word is the LLM wire projection, not a second stored transcript model.
type Word struct {
	Index   int    `json:"index"`
	Text    string `json:"text"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

type Constraints struct {
	CandidateCount int   `json:"candidateCount"`
	MinDurationMs  int64 `json:"minDurationMs"`
	MaxDurationMs  int64 `json:"maxDurationMs"`
}

type Request struct {
	TranscriptHash string      `json:"transcriptHash"`
	Words          []Word      `json:"words"`
	Constraints    Constraints `json:"constraints"`
}

func (r Request) valid() bool {
	hash, ok := strings.CutPrefix(r.TranscriptHash, "sha256:")
	c := r.Constraints
	if !ok || !hashPattern.MatchString(hash) || len(r.Words) == 0 || len(r.Words) > MaxTranscriptWords ||
		c.CandidateCount < 1 || c.CandidateCount > 5 ||
		c.MinDurationMs < 1 || c.MaxDurationMs < c.MinDurationMs || c.MaxDurationMs > 60_000 {
		return false
	}
	for i, w := range r.Words {
		if w.Index != i || !utf8.ValidString(w.Text) || strings.TrimSpace(w.Text) == "" ||
			len(w.Text) > 512 || strings.ContainsRune(w.Text, 0) || w.EndMs <= w.StartMs ||
			w.StartMs < -9007199254740991 || w.EndMs > 9007199254740991 ||
			(i > 0 && w.StartMs < r.Words[i-1].StartMs) {
			return false
		}
	}
	return true
}

type Client struct {
	options ClientOptions
	http    *http.Client
}

// NewClient performs no network requests. Redirects and automatic application
// retries are disabled. An injected transport must honor request cancellation.
func NewClient(options ClientOptions, client *http.Client) (*Client, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	var safe http.Client
	if client != nil {
		safe = *client
	} else {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil // Do not send a private token through ambient proxies.
		transport.DisableCompression = true
		transport.ResponseHeaderTimeout = ServiceTimeout
		transport.MaxResponseHeaderBytes = 16 * 1024
		safe.Transport = transport
	}
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	safe.Jar = nil
	if safe.Timeout <= 0 || safe.Timeout > ServiceTimeout {
		safe.Timeout = ServiceTimeout
	}
	return &Client{options: options, http: &safe}, nil
}

func (c *Client) Curate(ctx context.Context, input Request) ([]Proposal, error) {
	if c == nil || !input.valid() {
		return nil, ErrResult
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, ErrResult
	}
	if len(body) > MaxRequestBytes {
		return nil, ErrInputTooLarge
	}
	op, cancel := context.WithTimeout(ctx, ServiceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(op, http.MethodPost, c.options.URL, bytes.NewReader(body))
	if err != nil {
		return nil, ErrConfiguration
	}
	req.Header.Set("Authorization", "Bearer "+c.options.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	res, err := c.http.Do(req)
	if err != nil {
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		if op.Err() != nil {
			return nil, op.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrServiceUnavailable
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if op.Err() != nil {
		return nil, op.Err()
	}
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if len(data) > MaxResponseBytes || !utf8.Valid(data) {
		return nil, ErrResult
	}
	if res.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &failure)
		switch {
		case res.StatusCode == 401 || res.StatusCode == 403:
			return nil, ErrServiceAuth
		case res.StatusCode == 413:
			return nil, ErrInputTooLarge
		case res.StatusCode == 400 || res.StatusCode == 404 || res.StatusCode == 415 || failure.Code == "CONFIGURATION":
			return nil, ErrServiceRejected
		case res.StatusCode == 502 && failure.Code == "INVALID_RESPONSE":
			return nil, ErrResult
		case res.StatusCode == 504:
			return nil, context.DeadlineExceeded
		case res.StatusCode == 429 || res.StatusCode >= 500:
			return nil, ErrServiceUnavailable
		default:
			return nil, ErrServiceRejected
		}
	}
	contentType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" ||
		(res.Header.Get("Content-Encoding") != "" && res.Header.Get("Content-Encoding") != "identity") {
		return nil, ErrResult
	}
	// Pointer fields distinguish missing indexes from the legitimate index zero.
	var output struct {
		Candidates *[]struct {
			Start  *int   `json:"startWordIndex"`
			End    *int   `json:"endWordIndexExclusive"`
			Title  string `json:"title"`
			Hook   string `json:"hook"`
			Reason string `json:"reason"`
		} `json:"candidates"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil || output.Candidates == nil ||
		len(*output.Candidates) > input.Constraints.CandidateCount {
		return nil, ErrResult
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrResult
	}
	proposals := make([]Proposal, 0, len(*output.Candidates))
	for _, p := range *output.Candidates {
		if p.Start == nil || p.End == nil || *p.Start < 0 || *p.End <= *p.Start ||
			*p.End > len(input.Words) || !validMetadata(p.Title) || !validMetadata(p.Hook) || !validMetadata(p.Reason) {
			return nil, ErrResult
		}
		proposals = append(proposals, Proposal{
			StartWordIndex: *p.Start, EndWordIndexExclusive: *p.End,
			Title: p.Title, Hook: p.Hook, Reason: p.Reason,
		})
	}
	return proposals, nil
}
