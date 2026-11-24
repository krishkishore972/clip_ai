package migrations_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

// Offline S3 metadata only: no object bytes, credentials or provider requests.
type preferencesS3 struct {
	key       string
	completed bool
}

func (s *preferencesS3) Begin(_ context.Context, key, _ string) (string, error) {
	s.key = key
	return "fixture-upload", nil
}

func (s *preferencesS3) PresignPart(context.Context, string, string, int32, int64, ...time.Duration) (storage.PresignedPart, error) {
	return storage.PresignedPart{}, errors.New("not used in metadata replay")
}

func (s *preferencesS3) ListParts(_ context.Context, key, id string) ([]storage.Part, error) {
	if s.completed || key != s.key || id != "fixture-upload" {
		return nil, storage.ErrUploadNotFound
	}
	return []storage.Part{{Number: 1, ETag: `"fixture-tag"`, SizeBytes: 1024}}, nil
}

func (s *preferencesS3) Complete(_ context.Context, key, id string, _ []storage.Part) error {
	if key != s.key || id != "fixture-upload" {
		return storage.ErrUploadNotFound
	}
	s.completed = true
	return nil
}

func (s *preferencesS3) Abort(context.Context, string, string) error {
	return errors.New("not used in metadata replay")
}

func (s *preferencesS3) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	if !s.completed || key != s.key {
		return storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return storage.ObjectInfo{SizeBytes: 1024, ContentType: "video/mp4"}, nil
}

func TestUploadPreferencesServiceToDatabase(t *testing.T) {
	ctx, pool, provider := transcriptFileDatabase(t)
	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	owner, err := (auth.Store{DB: pool}).Resolve(ctx, "https://fixture.clerk.accounts.dev", "preferences-flow-owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx = auth.WithPrincipal(ctx, owner)
	s3 := &preferencesS3{}
	service, err := upload.New(s3, upload.Config{
		Assets:        upload.SQLAssets{DB: pool},
		Bucket:        "fixture-bucket",
		SigningSecret: []byte(strings.Repeat("offline-key-", 4)),
		Scope:         "fixture-region/fixture-bucket",
		TokenTTL:      time.Hour,
		PresignTTL:    time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	preferences := upload.ClipPreferences{CandidateCount: 2, MinDurationMs: 10000, MaxDurationMs: 40000}
	session, err := service.Create(ctx, upload.CreateUploadRequest{
		Filename: "original.mp4", SizeBytes: 1024, ContentType: "video/mp4",
		ClipPreferences: preferences,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		completed, err := service.Complete(ctx, upload.Access{
			UploadID: session.UploadID, Token: session.UploadToken,
		}, upload.CompleteUploadRequest{
			Parts: []upload.CompleteUploadPart{{PartNumber: 1, ETag: `"fixture-tag"`}},
		})
		if err != nil || completed.Status != "uploaded_unvalidated" {
			t.Fatalf("completion replay failed: %v", err)
		}
	}
	var userID, key, assetStatus, jobStatus, taskType string
	var stored upload.ClipPreferences
	err = pool.QueryRowContext(ctx, `
		SELECT
		    a.user_id,
		    a.storage_key,
		    a.status,
		    j.status,
		    j.candidate_count,
		    j.min_duration_ms,
		    j.max_duration_ms,
		    e.task_type
		FROM media_assets a
		JOIN media_curation_jobs j ON j.asset_id = a.id
		JOIN outbox_events e ON e.asset_id = a.id
		WHERE a.id = $1
	`, session.UploadID).Scan(
		&userID, &key, &assetStatus, &jobStatus,
		&stored.CandidateCount, &stored.MinDurationMs, &stored.MaxDurationMs,
		&taskType,
	)
	if err != nil || userID != owner.UserID || key != s3.key ||
		assetStatus != "uploaded_unvalidated" || jobStatus != "pending" ||
		stored != preferences || taskType != "media:validate" {
		t.Fatalf("create/token/complete/persistence flow drift: %v", err)
	}
	assertAssetEventCounts(t, ctx, pool, session.UploadID, 1, 1)
}
