package upload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

type multipartStarter interface {
	Begin(context.Context, string, string) (string, error)
	PresignPart(context.Context, string, string, int32, int64, ...time.Duration) (storage.PresignedPart, error)
}

type multipartFinisher interface {
	ListParts(context.Context, string, string) ([]storage.Part, error)
	Complete(context.Context, string, string, []storage.Part) error
	Abort(context.Context, string, string) error
}

type objectReader interface {
	Head(context.Context, string) (storage.ObjectInfo, error)
}

type Store interface {
	multipartStarter
	multipartFinisher
	objectReader
}

type Config struct {
	Assets AssetRecorder
	Bucket string
	SigningSecret []byte
	Scope         string // bucket/region binding, not an authorization identity
	TokenTTL      time.Duration
	PresignTTL    time.Duration
}

type Service struct {
	assets AssetRecorder
	bucket string
	store      Store
	secret     []byte
	scope      string
	tokenTTL   time.Duration
	presignTTL time.Duration
	now        func() time.Time
	random     io.Reader
}

func New(store Store, cfg Config) (*Service, error) {
	if store == nil || cfg.Assets == nil || cfg.Bucket == "" || len(cfg.SigningSecret) < 32 || cfg.Scope == "" ||
		cfg.TokenTTL < time.Minute || cfg.TokenTTL > 24*time.Hour ||
		cfg.PresignTTL < time.Second || cfg.PresignTTL > 15*time.Minute {
		return nil, errors.New("upload: storage, a secret of at least 32 bytes, scope, token TTL (1m–24h), and signing TTL (1s–15m) are required")
	}
	return &Service{store: store, assets:cfg.Assets, bucket:cfg.Bucket, secret: append([]byte(nil), cfg.SigningSecret...), scope: cfg.Scope,
		tokenTTL: cfg.TokenTTL, presignTTL: cfg.PresignTTL, now: time.Now, random: rand.Reader}, nil
}

// Create always initiates a fresh S3 upload. Without persistence it cannot promise
// deduplication after a lost HTTP response; lifecycle cleanup must handle orphans.
func (s *Service) Create(ctx context.Context, in CreateUploadRequest) (UploadSession, error) {
	if in.SizeBytes > MaxUploadBytes {
		return UploadSession{}, ErrTooLarge
	}
	ext := strings.ToLower(path.Ext(in.Filename))
	want := map[string]string{".mp4": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm"}[ext]
	if in.SizeBytes <= 0 || len(in.Filename) == 0 || len(in.Filename) > 255 || want == "" || want != in.ContentType ||
		strings.ContainsAny(in.Filename, `/\`) || strings.IndexFunc(in.Filename, unicode.IsControl) >= 0 {
		return UploadSession{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return UploadSession{}, err
	}
	p, ok := auth.FromContext(ctx)
	if !ok { return UploadSession{}, ErrUnauthenticated }
	var id [16]byte
	if _, err := io.ReadFull(s.random, id[:]); err != nil {
		return UploadSession{}, fmt.Errorf("upload: generate ID: %w", err)
	}
	c := claims{V: 2, UserID:p.UserID, WorkspaceID:p.WorkspaceID, Filename:in.Filename, ID: hex.EncodeToString(id[:]), Scope: s.scope, Size: in.SizeBytes, PartSize: PartSizeBytes,
		Type: in.ContentType, ExpiresAt: s.now().Add(s.tokenTTL).Unix()}
	c.Key = "dev-uploads/" + c.ID + "/source" + ext
	awsID, err := s.store.Begin(ctx, c.Key, c.Type)
	if err != nil {
		return UploadSession{}, backendError(err)
	}
	c.AWSID = awsID
	if awsID == "" {
		return UploadSession{}, ErrUnavailable
	}
	token, err := s.sign(c)
	if err != nil || len(token) > MaxTokenBytes {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.store.Abort(cleanupCtx, c.Key, c.AWSID)
		return UploadSession{}, ErrUnavailable
	}
	return state(c, token, "uploading", nil), nil
}

func partCount(c claims) int { return int((c.Size + c.PartSize - 1) / c.PartSize) }

func expectedSize(c claims, number int) int64 {
	if number == partCount(c) {
		return c.Size - int64(number-1)*c.PartSize
	}
	return c.PartSize
}

func state(c claims, token, status string, parts []storage.Part) UploadSession {
	out := UploadSession{UploadID: c.ID, UploadToken: token, Status: status, PartSizeBytes: c.PartSize,
		PartCount: partCount(c), ExpiresAt: time.Unix(c.ExpiresAt, 0).UTC(), UploadedParts: make([]UploadedPart, 0, len(parts))}
	for _, p := range parts {
		out.UploadedParts = append(out.UploadedParts, UploadedPart{PartNumber: int(p.Number), ETag: p.ETag, SizeBytes: p.SizeBytes})
	}
	return out
}

func backendError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.Join(ErrUnavailable, err)
}

func (s *Service) completed(ctx context.Context, c claims, token string) (UploadSession, error) {
	info, err := s.store.Head(ctx, c.Key)
	if errors.Is(err, storage.ErrObjectNotFound) {
		return UploadSession{}, ErrNotFound
	}
	if err != nil {
		return UploadSession{}, backendError(err)
	}
	if info.SizeBytes != c.Size || info.ContentType != c.Type {
		return UploadSession{}, ErrConflict
	}
	if err := s.assets.SaveCompleted(ctx,CompletedAsset{ID:c.ID, Bucket:s.bucket, Key:c.Key, Filename:c.Filename, ContentType:c.Type, SizeBytes:info.SizeBytes}); err != nil {
		if errors.Is(err,ErrNotFound) { return UploadSession{}, ErrNotFound }
		return UploadSession{}, backendError(err)
	}
	return state(c, token, "uploaded_unvalidated", nil), nil
}

func (s *Service) Get(ctx context.Context, a Access) (UploadSession, error) {
	c, err := s.verify(ctx,a)
	if err != nil {
		return UploadSession{}, err
	}
	parts, err := s.store.ListParts(ctx, c.Key, c.AWSID)
	if errors.Is(err, storage.ErrUploadNotFound) {
		return s.completed(ctx, c, a.Token)
	}
	if err != nil {
		return UploadSession{}, backendError(err)
	}
	return state(c, a.Token, "uploading", parts), nil
}

func (s *Service) SignParts(ctx context.Context, a Access, numbers []int) (SignedUploadParts, error) {
	c, err := s.verify(ctx,a)
	if err != nil {
		return SignedUploadParts{}, err
	}
	seen := make(map[int]bool, len(numbers))
	if len(numbers) == 0 || len(numbers) > MaxSignBatch {
		return SignedUploadParts{}, ErrInvalid
	}
	for _, n := range numbers {
		if n < 1 || n > partCount(c) || seen[n] {
			return SignedUploadParts{}, ErrInvalid
		}
		seen[n] = true
	}
	// Do not issue URLs for an already completed or aborted upload.
	if _, err := s.store.ListParts(ctx, c.Key, c.AWSID); err != nil {
		if errors.Is(err, storage.ErrUploadNotFound) {
			return SignedUploadParts{}, ErrConflict
		}
		return SignedUploadParts{}, backendError(err)
	}
	ttl := min(s.presignTTL, time.Unix(c.ExpiresAt, 0).Sub(s.now())).Truncate(time.Second)
	if ttl < time.Second {
		return SignedUploadParts{}, ErrToken
	}
	out := SignedUploadParts{Parts: make([]SignedUploadPart, 0, len(numbers))}
	for _, n := range numbers {
		// Recompute for each part: signing may resolve credentials over the network.
		ttl = min(s.presignTTL, time.Unix(c.ExpiresAt, 0).Sub(s.now())).Truncate(time.Second)
		if ttl < time.Second {
			return SignedUploadParts{}, ErrToken
		}
		part, err := s.store.PresignPart(ctx, c.Key, c.AWSID, int32(n), expectedSize(c, n), ttl)
		if err != nil {
			return SignedUploadParts{}, backendError(err)
		}
		if part.ExpiresAt.After(time.Unix(c.ExpiresAt, 0)) || !part.ExpiresAt.After(s.now()) {
			return SignedUploadParts{}, ErrToken
		}
		headers := make(map[string]string, len(part.Headers))
		for key, values := range part.Headers {
			// The HTTP client derives Host from the signed URL automatically.
			if !strings.EqualFold(key, "Host") {
				headers[key] = strings.Join(values, ",")
			}
		}
		out.Parts = append(out.Parts, SignedUploadPart{PartNumber: n, URL: part.URL, Headers: headers,
			ExpiresAt: part.ExpiresAt})
	}
	return out, nil
}

func (s *Service) Complete(ctx context.Context, a Access, in CompleteUploadRequest) (UploadSession, error) {
	c, err := s.verify(ctx,a)
	if err != nil {
		return UploadSession{}, err
	}
	if len(in.Parts) != partCount(c) {
		return UploadSession{}, ErrInvalid
	}
	clientTags := make(map[int]string, len(in.Parts))
	for _, p := range in.Parts {
		if p.PartNumber < 1 || p.PartNumber > partCount(c) || strings.TrimSpace(p.ETag) == "" ||
			len(p.ETag) > 1024 || strings.IndexFunc(p.ETag, unicode.IsControl) >= 0 || clientTags[p.PartNumber] != "" {
			return UploadSession{}, ErrInvalid
		}
		clientTags[p.PartNumber] = p.ETag
	}
	parts, err := s.store.ListParts(ctx, c.Key, c.AWSID)
	if errors.Is(err, storage.ErrUploadNotFound) {
		return s.completed(ctx, c, a.Token)
	}
	if err != nil {
		return UploadSession{}, backendError(err)
	}
	if len(parts) != partCount(c) {
		return UploadSession{}, ErrConflict
	}
	seen := make(map[int32]bool, len(parts))
	for _, p := range parts {
		if p.Number < 1 || int(p.Number) > partCount(c) || seen[p.Number] ||
			p.SizeBytes != expectedSize(c, int(p.Number)) || p.ETag != clientTags[int(p.Number)] {
			return UploadSession{}, ErrInvalid
		}
		seen[p.Number] = true
	}
	if err := s.store.Complete(ctx, c.Key, c.AWSID, parts); err != nil {
		// A timeout or a concurrent completion can occur after S3 assembled the
		// object. HEAD may establish success without another destructive write.
		if out, headErr := s.completed(ctx, c, a.Token); headErr == nil {
			return out, nil
		}
		return UploadSession{}, backendError(err)
	}
	return s.completed(ctx, c, a.Token)
}

func (s *Service) Abort(ctx context.Context, a Access) error {
	c, err := s.verify(ctx,a)
	if err != nil {
		return err
	}
	// HEAD first prevents aborting a completed object even if an unusual provider
	// response still exposes its old upload. Never delete the source object.
	if _, err := s.store.Head(ctx, c.Key); err == nil {
		return ErrConflict
	} else if !errors.Is(err, storage.ErrObjectNotFound) {
		return backendError(err)
	}
	if err := s.store.Abort(ctx, c.Key, c.AWSID); err != nil {
		if !errors.Is(err, storage.ErrUploadNotFound) {
			return backendError(err)
		}
	}
	// Resolve a possible race with completion rather than claiming cancellation.
	if _, headErr := s.store.Head(ctx, c.Key); headErr == nil {
		return ErrConflict
	} else if !errors.Is(headErr, storage.ErrObjectNotFound) {
		return backendError(headErr)
	}
	return nil
}
