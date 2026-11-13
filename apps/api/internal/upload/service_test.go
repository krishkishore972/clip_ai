package upload

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/storage"
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

func testConfig() Config {
	return Config{Assets: &assetStub{}, Bucket: "test-bucket", SigningSecret: []byte(strings.Repeat("test-key-", 8)), Scope: "test-region/test-bucket",
		TokenTTL: time.Hour, PresignTTL: 15 * time.Minute}
}

type assetStub struct {
	err   error
	saved []CompletedAsset
}

func (s *assetStub) SaveCompleted(_ context.Context, a CompletedAsset) error {
	s.saved = append(s.saved, a)
	return s.err
}
func ownerContext() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{UserID: "test-user", WorkspaceID: "test-workspace"})
}

func testService(t *testing.T) (*Service, *storeStub) {
	t.Helper()
	f := &storeStub{errs: make(map[string]error)}
	s, err := New(f, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }
	f.now = func() time.Time { return s.now() }
	return s, f
}

func start(t *testing.T, s *Service, size int64) UploadSession {
	t.Helper()
	out, err := s.Create(ownerContext(), CreateUploadRequest{Filename: "sample.mp4", ContentType: "video/mp4", SizeBytes: size})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func accessFor(state UploadSession) Access {
	return Access{UploadID: state.UploadID, Token: state.UploadToken}
}

func TestUploadOwnershipAndLegacyTokens(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	state := start(t, s, 1024)
	for _, p := range []auth.Principal{{UserID: "other-user", WorkspaceID: "other-workspace"}, {UserID: "other-user", WorkspaceID: "test-workspace"}, {UserID: "test-user", WorkspaceID: "other-workspace"}} {
		ctx := auth.WithPrincipal(context.Background(), p)
		before := len(f.calls)
		checks := []func() error{
			func() error { _, err := s.Get(ctx, accessFor(state)); return err },
			func() error { _, err := s.SignParts(ctx, accessFor(state), []int{1}); return err },
			func() error { _, err := s.Complete(ctx, accessFor(state), CompleteUploadRequest{}); return err },
			func() error { return s.Abort(ctx, accessFor(state)) },
		}
		for _, check := range checks {
			if err := check(); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cross-owner request was not hidden: %v", err)
			}
		}
		if len(f.calls) != before {
			t.Fatal("cross-owner request reached S3")
		}
	}
	if _, err := s.Create(context.Background(), CreateUploadRequest{Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("unauthenticated create accepted")
	}
	c, err := s.verify(ownerContext(), accessFor(state))
	if err != nil {
		t.Fatal(err)
	}
	c.V = 1
	c.UserID = ""
	c.WorkspaceID = ""
	legacy, err := s.sign(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ownerContext(), Access{UploadID: state.UploadID, Token: legacy}); !errors.Is(err, ErrToken) {
		t.Fatal("legacy upload token accepted")
	}
}

func TestCompletionPersistenceFailureIsRecoverable(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	records := s.assets.(*assetStub)
	state := start(t, s, 1024)
	f.parts = []storage.Part{{Number: 1, ETag: "tag", SizeBytes: 1024}}
	records.err = errors.New("database offline")
	in := CompleteUploadRequest{Parts: []CompleteUploadPart{{PartNumber: 1, ETag: "tag"}}}
	if _, err := s.Complete(ownerContext(), accessFor(state), in); !errors.Is(err, ErrUnavailable) {
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
	state := start(t, s, PartSizeBytes+1024)
	if len(state.UploadID) != 32 || state.UploadToken == "" || state.Status != "uploading" || state.PartCount != 2 || state.PartSizeBytes != PartSizeBytes {
		t.Fatal("invalid upload plan")
	}
	if f.key != "dev-uploads/"+state.UploadID+"/source.mp4" || !slices.Equal(f.calls, []string{"begin"}) {
		t.Fatal("object key was not server generated")
	}
	out, err := s.SignParts(ownerContext(), accessFor(state), []int{1, 2})
	if err != nil || len(out.Parts) != 2 || !slices.Equal(f.signSizes, []int64{PartSizeBytes, 1024}) {
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
				s.now = func() time.Time { return state.ExpiresAt }
			case "wrong secret":
				s.secret = []byte(strings.Repeat("other", 10))
			case "wrong scope":
				s.scope = "other-bucket"
			case "missing":
				a.Token = ""
			case "too long":
				a.Token = strings.Repeat("x", MaxTokenBytes+1)
			}
			before := len(f.calls)
			checks := []func() error{
				func() error { _, err := s.Get(ownerContext(), a); return err },
				func() error { _, err := s.SignParts(ownerContext(), a, []int{1}); return err },
				func() error { _, err := s.Complete(ownerContext(), a, CompleteUploadRequest{}); return err },
				func() error { return s.Abort(ownerContext(), a) },
			}
			for _, check := range checks {
				if !errors.Is(check(), ErrToken) {
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
	restarted, err := New(f, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = s.now
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
		if _, err := s.SignParts(ownerContext(), a, parts); !errors.Is(err, ErrInvalid) || len(f.calls) != before {
			t.Fatal("invalid signing plan reached storage")
		}
	}
	s.now = func() time.Time { return state.ExpiresAt.Add(-5 * time.Second) }
	if _, err := s.SignParts(ownerContext(), a, []int{1}); err != nil || f.signTTLs[0] != 5*time.Second {
		t.Fatalf("URL TTL not clamped: %v", err)
	}
	f.signDelay = time.Second
	if _, err := s.SignParts(ownerContext(), a, []int{1}); !errors.Is(err, ErrToken) {
		t.Fatal("late SDK signing produced a URL beyond token expiry")
	}
	s.now = func() time.Time { return state.ExpiresAt.Add(-500 * time.Millisecond) }
	if _, err := s.SignParts(ownerContext(), a, []int{1}); !errors.Is(err, ErrToken) {
		t.Fatal("less than one second remaining was accepted")
	}
}

func TestCompleteAndRetry(t *testing.T) {
	t.Parallel()
	for _, ambiguous := range []bool{false, true} {
		s, f := testService(t)
		state := start(t, s, PartSizeBytes+1024)
		f.parts = []storage.Part{{Number: 2, ETag: "tag-2", SizeBytes: 1024}, {Number: 1, ETag: "tag-1", SizeBytes: PartSizeBytes}}
		in := CompleteUploadRequest{Parts: []CompleteUploadPart{{PartNumber: 1, ETag: "tag-1"}, {PartNumber: 2, ETag: "tag-2"}}}
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
		if _, err := s.SignParts(ownerContext(), accessFor(state), []int{1}); !errors.Is(err, ErrConflict) {
			t.Fatal("signed a completed upload")
		}
		if err := s.Abort(ownerContext(), accessFor(state)); !errors.Is(err, ErrConflict) || count(f.calls, "abort") != 0 {
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
			_, err := s.Complete(ownerContext(), accessFor(state), CompleteUploadRequest{Parts: []CompleteUploadPart{{PartNumber: 1, ETag: "tag"}}})
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
	if _, err := s.Get(ownerContext(), accessFor(state)); !errors.Is(err, ErrNotFound) {
		t.Fatal("aborted upload reported as active")
	}
	if _, err := s.SignParts(ownerContext(), accessFor(state), []int{1}); !errors.Is(err, ErrConflict) {
		t.Fatal("aborted upload signed")
	}
	s, f = testService(t)
	state = start(t, s, 1024)
	f.abortRace = true
	if err := s.Abort(ownerContext(), accessFor(state)); !errors.Is(err, ErrConflict) {
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
				_, err = s.Create(ownerContext(), CreateUploadRequest{Filename: "a.mp4", SizeBytes: 1, ContentType: "video/mp4"})
			case "sign":
				_, err = s.SignParts(ownerContext(), accessFor(state), []int{1})
			case "list":
				_, err = s.Get(ownerContext(), accessFor(state))
			case "head", "complete":
				_, err = s.Complete(ownerContext(), accessFor(state), CompleteUploadRequest{Parts: []CompleteUploadPart{{PartNumber: 1, ETag: "tag"}}})
			case "abort":
				err = s.Abort(ownerContext(), accessFor(state))
			}
			if !errors.Is(err, ErrUnavailable) {
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
	if _, err := s.Get(ownerContext(), accessFor(state)); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong final size accepted")
	}
	f.object = &storage.ObjectInfo{SizeBytes: 1024, ContentType: "text/plain"}
	if _, err := s.Get(ownerContext(), accessFor(state)); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong declared final content type accepted")
	}
}

func TestCreateInvalidMetadataAndCancellation(t *testing.T) {
	t.Parallel()
	s, f := testService(t)
	for _, in := range []CreateUploadRequest{
		{}, {Filename: "../a.mp4", ContentType: "video/mp4", SizeBytes: 1},
		{Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: MaxUploadBytes + 1},
	} {
		if _, err := s.Create(ownerContext(), in); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
	ctx, cancel := context.WithCancel(ownerContext())
	cancel()
	if _, err := s.Create(ctx, CreateUploadRequest{Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	s.random = strings.NewReader("")
	if _, err := s.Create(ownerContext(), CreateUploadRequest{Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1}); !errors.Is(err, io.EOF) {
		t.Fatal("random source failure ignored")
	}
	if len(f.calls) != 0 {
		t.Fatal("invalid create reached storage")
	}
}

func TestConfigurationAndSecretCopy(t *testing.T) {
	t.Parallel()
	for _, change := range []func(*Config){
		func(c *Config) { c.SigningSecret = nil }, func(c *Config) { c.Scope = "" },
		func(c *Config) { c.TokenTTL = 25 * time.Hour }, func(c *Config) { c.TokenTTL = 0 },
		func(c *Config) { c.PresignTTL = 16 * time.Minute },
	} {
		cfg := testConfig()
		change(&cfg)
		if _, err := New(&storeStub{}, cfg); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	if _, err := New(nil, testConfig()); err == nil {
		t.Fatal("nil store accepted")
	}
	cfg := testConfig()
	want := slices.Clone(cfg.SigningSecret)
	s, err := New(&storeStub{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SigningSecret[0]++
	if !reflect.DeepEqual(s.secret, want) {
		t.Fatal("service secret aliases configuration")
	}
}
