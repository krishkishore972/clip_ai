package upload_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

// An offline S3 double, not an application session store or runtime fallback.
type storeStub struct {
	key, contentType string
	parts            []storage.Part
	object           *storage.ObjectInfo
	aborted          bool
	calls            []string
	errs             map[string]error
	signSizes        []int64
	signTTLs         []time.Duration
	now              func() time.Time
	signDelay        time.Duration
	completeThenErr  bool
	abortRace        bool
	ctx              context.Context
	records          *assetStub
	clock            time.Time
}

func (f *storeStub) call(ctx context.Context, op string) error {
	f.ctx = ctx
	f.calls = append(f.calls, op)
	return f.errs[op]
}

func (f *storeStub) Begin(ctx context.Context, key, contentType string) (string, error) {
	if err := f.call(ctx, "begin"); err != nil {
		return "", err
	}
	f.key, f.contentType = key, contentType
	return "aws-upload-1", nil
}

func (f *storeStub) PresignPart(ctx context.Context, key, id string, n int32, size int64, ttl ...time.Duration) (storage.PresignedPart, error) {
	if err := f.call(ctx, "sign"); err != nil {
		return storage.PresignedPart{}, err
	}
	if key != f.key || id != "aws-upload-1" {
		return storage.PresignedPart{}, errors.New("wrong signing target")
	}
	f.signSizes = append(f.signSizes, size)
	f.signTTLs = append(f.signTTLs, ttl[0])
	return storage.PresignedPart{URL: "https://example.invalid/signed", Method: http.MethodPut,
		Headers:   http.Header{"Host": {"example.invalid"}, "X-Test": {"a", "b"}},
		ExpiresAt: f.now().Add(ttl[0] + f.signDelay)}, nil
}

func (f *storeStub) ListParts(ctx context.Context, key, id string) ([]storage.Part, error) {
	if err := f.call(ctx, "list"); err != nil {
		return nil, err
	}
	if key != f.key || id != "aws-upload-1" || f.object != nil || f.aborted {
		return nil, storage.ErrUploadNotFound
	}
	return slices.Clone(f.parts), nil
}

func (f *storeStub) Complete(ctx context.Context, key, id string, parts []storage.Part) error {
	err := f.call(ctx, "complete")
	if err != nil && !f.completeThenErr {
		return err
	}
	if key != f.key || id != "aws-upload-1" {
		return errors.New("wrong completion target")
	}
	var size int64
	for _, part := range parts {
		size += part.SizeBytes
	}
	f.object = &storage.ObjectInfo{SizeBytes: size, ContentType: f.contentType}
	return err
}

func (f *storeStub) Abort(ctx context.Context, key, id string) error {
	if err := f.call(ctx, "abort"); err != nil {
		return err
	}
	if key != f.key || id != "aws-upload-1" {
		return errors.New("wrong abort target")
	}
	if f.abortRace {
		f.object = &storage.ObjectInfo{SizeBytes: 1024, ContentType: f.contentType}
		return storage.ErrUploadNotFound
	}
	if f.aborted {
		return storage.ErrUploadNotFound
	}
	f.aborted = true
	return nil
}

func (f *storeStub) Head(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := f.call(ctx, "head"); err != nil {
		return storage.ObjectInfo{}, err
	}
	if key != f.key || f.object == nil {
		return storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return *f.object, nil
}

func testConfig() upload.Config {
	return upload.Config{Assets: &assetStub{}, Bucket: "test-bucket", SigningSecret: []byte(strings.Repeat("test-key-", 8)), Scope: "test-region/test-bucket",
		TokenTTL: time.Hour, PresignTTL: 15 * time.Minute, Now: func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }}
}

type assetStub struct {
	err   error
	saved []upload.CompletedAsset
}

func (s *assetStub) SaveCompleted(_ context.Context, a upload.CompletedAsset) error {
	s.saved = append(s.saved, a)
	return s.err
}
func ownerContext() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{UserID: "test-user"})
}

func testService(t *testing.T) (*upload.Service, *storeStub) {
	t.Helper()
	f := &storeStub{errs: make(map[string]error)}
	cfg := testConfig()
	f.clock = cfg.Now()
	cfg.Now = func() time.Time { return f.clock }
	f.records = cfg.Assets.(*assetStub)
	s, err := upload.New(f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.now = cfg.Now
	return s, f
}

func start(t *testing.T, s *upload.Service, size int64) upload.UploadSession {
	t.Helper()
	out, err := s.Create(ownerContext(), upload.CreateUploadRequest{
		Filename: "sample.mp4", ContentType: "video/mp4", SizeBytes: size,
		ClipPreferences: upload.ClipPreferences{CandidateCount: 5, MinDurationMs: 15000, MaxDurationMs: 60000},
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func accessFor(state upload.UploadSession) upload.Access {
	return upload.Access{UploadID: state.UploadID, Token: state.UploadToken}
}

func TestUploadOwnershipAndLegacyTokens(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	state := start(t, s, 1024)
	for _, p := range []auth.Principal{{UserID: "other-user"}} {
		ctx := auth.WithPrincipal(context.Background(), p)
		before := len(f.calls)
		checks := []func() error{
			func() error { _, err := s.Get(ctx, accessFor(state)); return err },
			func() error { _, err := s.SignParts(ctx, accessFor(state), []int{1}); return err },
			func() error { _, err := s.Complete(ctx, accessFor(state), upload.CompleteUploadRequest{}); return err },
			func() error { return s.Abort(ctx, accessFor(state)) },
		}
		for _, check := range checks {
			if err := check(); !errors.Is(err, upload.ErrNotFound) {
				t.Fatalf("cross-owner request was not hidden: %v", err)
			}
		}
		if len(f.calls) != before {
			t.Fatal("cross-owner request reached S3")
		}
	}
	if _, err := s.Create(context.Background(), upload.CreateUploadRequest{Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1}); !errors.Is(err, upload.ErrUnauthenticated) {
		t.Fatal("unauthenticated create accepted")
	}
	c := decodeTokenFixture(t, state.UploadToken, testConfig().SigningSecret)
	if c["v"] != float64(3) || c["userId"] != "test-user" {
		t.Fatal("new upload capability must bind the current user")
	}
	for _, version := range []int{1, 2} {
		c["v"] = version
		legacy := signTokenFixture(t, c, testConfig().SigningSecret)
		before := len(f.calls)
		if _, err := s.Get(ownerContext(), upload.Access{UploadID: state.UploadID, Token: legacy}); !errors.Is(err, upload.ErrToken) {
			t.Fatal("legacy upload token accepted")
		}
		if len(f.calls) != before {
			t.Fatal("legacy token reached S3")
		}
	}
}

func TestCompletionPersistenceFailureIsRecoverable(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	records := f.records
	state := start(t, s, 1024)
	f.parts = []storage.Part{{Number: 1, ETag: "tag", SizeBytes: 1024}}
	records.err = errors.New("database offline")
	in := upload.CompleteUploadRequest{Parts: []upload.CompleteUploadPart{{PartNumber: 1, ETag: "tag"}}}
	if _, err := s.Complete(ownerContext(), accessFor(state), in); !errors.Is(err, upload.ErrUnavailable) {
		t.Fatalf("unpersisted completion returned success: %v", err)
	}
	records.err = nil
	if _, err := s.Complete(ownerContext(), accessFor(state), in); err != nil {
		t.Fatal(err)
	}
	if count(f.calls, "complete") != 1 || records.saved[len(records.saved)-1].Filename != "sample.mp4" {
		t.Fatal("retry repeated S3 completion or lost signed filename")
	}
}

func TestCreateAndSign(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	state := start(t, s, upload.PartSizeBytes+1024)
	if len(state.UploadID) != 32 || state.UploadToken == "" || state.Status != "uploading" || state.PartCount != 2 || state.PartSizeBytes != upload.PartSizeBytes {
		t.Fatal("invalid upload plan")
	}
	if f.key != "dev-uploads/"+state.UploadID+"/source.mp4" || !slices.Equal(f.calls, []string{"begin"}) {
		t.Fatal("object key was not server generated")
	}
	out, err := s.SignParts(ownerContext(), accessFor(state), []int{1, 2})
	if err != nil || len(out.Parts) != 2 || !slices.Equal(f.signSizes, []int64{upload.PartSizeBytes, 1024}) {
		t.Fatalf("incorrect signed part sizes: %v", err)
	}
	for _, p := range out.Parts {
		if p.Headers["Host"] != "" || p.Headers["X-Test"] != "a,b" || p.ExpiresAt.After(state.ExpiresAt) {
			t.Fatal("invalid headers or expiry")
		}
	}
}

func TestCreateDoesNotPretendToDeduplicate(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	first := start(t, s, 1024)
	second := start(t, s, 1024)
	if first.UploadID == second.UploadID || first.UploadToken == second.UploadToken {
		t.Fatal("create calls reused an upload")
	}
}

func TestTokenRejectionBeforeStorage(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"tampered", "path mismatch", "expired", "wrong secret", "wrong scope", "missing", "too long"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, f := testService(t)
			state := start(t, s, 1024)
			a := accessFor(state)
			switch kind {
			case "tampered":
				a.Token = "x" + a.Token[1:]
			case "path mismatch":
				a.UploadID = "other-id"
			case "expired":
				f.clock = state.ExpiresAt
			case "wrong secret":
				c := decodeTokenFixture(t, a.Token, testConfig().SigningSecret)
				a.Token = signTokenFixture(t, c, []byte(strings.Repeat("other", 10)))
			case "wrong scope":
				c := decodeTokenFixture(t, a.Token, testConfig().SigningSecret)
				c["scope"] = "other-bucket"
				a.Token = signTokenFixture(t, c, testConfig().SigningSecret)
			case "missing":
				a.Token = ""
			case "too long":
				a.Token = strings.Repeat("x", upload.MaxTokenBytes+1)
			}
			before := len(f.calls)
			checks := []func() error{
				func() error { _, err := s.Get(ownerContext(), a); return err },
				func() error { _, err := s.SignParts(ownerContext(), a, []int{1}); return err },
				func() error { _, err := s.Complete(ownerContext(), a, upload.CompleteUploadRequest{}); return err },
				func() error { return s.Abort(ownerContext(), a) },
			}
			for _, check := range checks {
				if !errors.Is(check(), upload.ErrToken) {
					t.Fatal("invalid token accepted")
				}
			}
			if len(f.calls) != before {
				t.Fatal("invalid token reached storage")
			}
		})
	}
}

func TestTokenWorksAcrossServiceRestart(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	state := start(t, s, 1024)
	restarted, err := upload.New(f, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Get(ownerContext(), accessFor(state)); err != nil {
		t.Fatalf("stateless restart failed: %v", err)
	}
}

func TestSigningBounds(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	state := start(t, s, 1024)
	a := accessFor(state)
	for _, parts := range [][]int{nil, {0}, {2}, {1, 1}, make([]int, 101)} {
		before := len(f.calls)
		if _, err := s.SignParts(ownerContext(), a, parts); !errors.Is(err, upload.ErrInvalid) || len(f.calls) != before {
			t.Fatal("invalid signing plan reached storage")
		}
	}
	f.clock = state.ExpiresAt.Add(-5 * time.Second)
	if _, err := s.SignParts(ownerContext(), a, []int{1}); err != nil || f.signTTLs[0] != 5*time.Second {
		t.Fatalf("URL TTL not clamped: %v", err)
	}
	f.signDelay = time.Second
	if _, err := s.SignParts(ownerContext(), a, []int{1}); !errors.Is(err, upload.ErrToken) {
		t.Fatal("late SDK signing produced a URL beyond token expiry")
	}
	f.clock = state.ExpiresAt.Add(-500 * time.Millisecond)
	if _, err := s.SignParts(ownerContext(), a, []int{1}); !errors.Is(err, upload.ErrToken) {
		t.Fatal("less than one second remaining was accepted")
	}
}

func TestCompleteAndRetry(t *testing.T) {
	t.Parallel()
	for _, ambiguous := range []bool{false, true} {
		s, f := testService(t)
		state := start(t, s, upload.PartSizeBytes+1024)
		f.parts = []storage.Part{{Number: 2, ETag: "tag-2", SizeBytes: 1024}, {Number: 1, ETag: "tag-1", SizeBytes: upload.PartSizeBytes}}
		in := upload.CompleteUploadRequest{Parts: []upload.CompleteUploadPart{{PartNumber: 1, ETag: "tag-1"}, {PartNumber: 2, ETag: "tag-2"}}}
		if ambiguous {
			f.errs["complete"] = errors.New("lost completion response")
			f.completeThenErr = true
		}
		for range 2 {
			out, err := s.Complete(ownerContext(), accessFor(state), in)
			if err != nil || out.Status != "uploaded_unvalidated" || out.UploadID != state.UploadID {
				t.Fatalf("completion/recovery failed: %v", err)
			}
		}
		if count(f.calls, "complete") != 1 {
			t.Fatal("completion retried a storage write")
		}
		if out, err := s.Get(ownerContext(), accessFor(state)); err != nil || out.Status != "uploaded_unvalidated" {
			t.Fatalf("completed status failed: %v", err)
		}
		if _, err := s.SignParts(ownerContext(), accessFor(state), []int{1}); !errors.Is(err, upload.ErrConflict) {
			t.Fatal("signed a completed upload")
		}
		if err := s.Abort(ownerContext(), accessFor(state)); !errors.Is(err, upload.ErrConflict) || count(f.calls, "abort") != 0 {
			t.Fatal("attempted to abort a completed object")
		}
	}
}

func count(calls []string, op string) int {
	n := 0
	for _, call := range calls {
		if call == op {
			n++
		}
	}
	return n
}

func TestCompletionRejectsWrongParts(t *testing.T) {
	t.Parallel()
	for name, parts := range map[string][]storage.Part{
		"missing":      nil,
		"wrong size":   {{Number: 1, ETag: "tag", SizeBytes: 1023}},
		"wrong etag":   {{Number: 1, ETag: "different", SizeBytes: 1024}},
		"wrong number": {{Number: 2, ETag: "tag", SizeBytes: 1024}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, f := testService(t)
			state := start(t, s, 1024)
			f.parts = parts
			_, err := s.Complete(ownerContext(), accessFor(state), upload.CompleteUploadRequest{Parts: []upload.CompleteUploadPart{{PartNumber: 1, ETag: "tag"}}})
			if err == nil || count(f.calls, "complete") != 0 {
				t.Fatal("unverified parts completed")
			}
		})
	}
}

func TestAbortAndRace(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	state := start(t, s, 1024)
	for range 2 {
		if err := s.Abort(ownerContext(), accessFor(state)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Get(ownerContext(), accessFor(state)); !errors.Is(err, upload.ErrNotFound) {
		t.Fatal("aborted upload reported as active")
	}
	if _, err := s.SignParts(ownerContext(), accessFor(state), []int{1}); !errors.Is(err, upload.ErrConflict) {
		t.Fatal("aborted upload signed")
	}
	s, f = testService(t)
	state = start(t, s, 1024)
	f.abortRace = true
	if err := s.Abort(ownerContext(), accessFor(state)); !errors.Is(err, upload.ErrConflict) {
		t.Fatal("completion/abort race reported cancellation")
	}
}

func TestBackendFailures(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"begin", "sign", "list", "head", "complete", "abort"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			s, f := testService(t)
			state := start(t, s, 1024)
			f.parts = []storage.Part{{Number: 1, ETag: "tag", SizeBytes: 1024}}
			f.errs[op] = errors.New("provider failed")
			var err error
			switch op {
			case "begin":
				_, err = s.Create(ownerContext(), upload.CreateUploadRequest{
					Filename: "a.mp4", SizeBytes: 1, ContentType: "video/mp4",
					ClipPreferences: upload.ClipPreferences{CandidateCount: 5, MinDurationMs: 15000, MaxDurationMs: 60000},
				})
			case "sign":
				_, err = s.SignParts(ownerContext(), accessFor(state), []int{1})
			case "list":
				_, err = s.Get(ownerContext(), accessFor(state))
			case "head", "complete":
				_, err = s.Complete(ownerContext(), accessFor(state), upload.CompleteUploadRequest{Parts: []upload.CompleteUploadPart{{PartNumber: 1, ETag: "tag"}}})
			case "abort":
				err = s.Abort(ownerContext(), accessFor(state))
			}
			if !errors.Is(err, upload.ErrUnavailable) {
				t.Fatalf("expected classified provider failure, got %v", err)
			}
		})
	}
}

func TestSourceMismatch(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	state := start(t, s, 1024)
	f.object = &storage.ObjectInfo{SizeBytes: 99, ContentType: "video/mp4"}
	if _, err := s.Get(ownerContext(), accessFor(state)); !errors.Is(err, upload.ErrConflict) {
		t.Fatal("wrong final size accepted")
	}
	f.object = &storage.ObjectInfo{SizeBytes: 1024, ContentType: "text/plain"}
	if _, err := s.Get(ownerContext(), accessFor(state)); !errors.Is(err, upload.ErrConflict) {
		t.Fatal("wrong declared final content type accepted")
	}
}

func TestCreateInvalidMetadataAndCancellation(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	for _, in := range []upload.CreateUploadRequest{
		{}, {Filename: "../a.mp4", ContentType: "video/mp4", SizeBytes: 1},
		{Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: upload.MaxUploadBytes + 1},
	} {
		if _, err := s.Create(ownerContext(), in); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
	ctx, cancel := context.WithCancel(ownerContext())
	cancel()
	if _, err := s.Create(ctx, upload.CreateUploadRequest{Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	cfg := testConfig()
	cfg.Random = strings.NewReader("")
	var err error
	s, err = upload.New(f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ownerContext(), upload.CreateUploadRequest{
		Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1,
		ClipPreferences: upload.ClipPreferences{CandidateCount: 5, MinDurationMs: 15000, MaxDurationMs: 60000},
	}); !errors.Is(err, io.EOF) {
		t.Fatal("random source failure ignored")
	}
	if len(f.calls) != 0 {
		t.Fatal("invalid create reached storage")
	}
}

func TestConfigurationAndSecretCopy(t *testing.T) {
	t.Parallel()
	for _, change := range []func(*upload.Config){
		func(c *upload.Config) { c.SigningSecret = nil }, func(c *upload.Config) { c.Scope = "" },
		func(c *upload.Config) { c.TokenTTL = 25 * time.Hour }, func(c *upload.Config) { c.TokenTTL = 0 },
		func(c *upload.Config) { c.PresignTTL = 16 * time.Minute },
	} {
		cfg := testConfig()
		change(&cfg)
		if _, err := upload.New(&storeStub{}, cfg); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	if _, err := upload.New(nil, testConfig()); err == nil {
		t.Fatal("nil store accepted")
	}
	cfg := testConfig()
	want := slices.Clone(cfg.SigningSecret)
	s, err := upload.New(&storeStub{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SigningSecret[0]++
	state := start(t, s, 1024)
	// Independently verify the emitted capability against the original key.
	decodeTokenFixture(t, state.UploadToken, want)
}

// Token fixtures implement the public wire format independently of production
// signing/verification. They allow authenticated legacy and scope-negative cases.
func signTokenFixture(t *testing.T, claims map[string]any, secret []byte) string {
	t.Helper()
	data, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func decodeTokenFixture(t *testing.T, token string, secret []byte) map[string]any {
	t.Helper()
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatal("token has no signature")
	}
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(got, mac.Sum(nil)) {
		t.Fatal("token signature does not match original signing secret")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}
