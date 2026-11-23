package storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/storage"
)

type derivedPutStub struct {
	input *s3.PutObjectInput
	body  []byte
	err   error
	retry int
}

func (s *derivedPutStub) PutObject(_ context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	s.input = in
	var options s3.Options
	for _, apply := range opts {
		apply(&options)
	}
	s.retry = options.RetryMaxAttempts
	s.body, _ = io.ReadAll(in.Body)
	return &s3.PutObjectOutput{ChecksumSHA256: in.ChecksumSHA256}, s.err
}

func TestDerivedUploadImmutableNamespace(t *testing.T) {
	cfg := config.S3{Region: "offline-region", Bucket: "private-bucket"}
	stub := &derivedPutStub{}
	uploader, err := storage.NewDerivedUploaderWithClient(cfg, stub)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("locally verified prepared bytes")
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := uploader.UploadDerived(context.Background(), "asset-id", "random-output-id", "audio", path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if out.Key != "dev-uploads/asset-id/derived/v1/random-output-id/audio.wav" || out.SizeBytes != int64(len(data)) || out.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("wrong object: %+v", out)
	}
	in := stub.input
	if aws.ToString(in.Key) != out.Key || aws.ToString(in.Bucket) != cfg.Bucket || aws.ToString(in.ContentType) != "audio/wav" || aws.ToString(in.CacheControl) != "no-store" || aws.ToString(in.IfNoneMatch) != "*" || aws.ToInt64(in.ContentLength) != int64(len(data)) || aws.ToString(in.ChecksumSHA256) != base64.StdEncoding.EncodeToString(sum[:]) || string(stub.body) != string(data) || stub.retry != 1 || in.ACL != "" {
		t.Fatal("unsafe put request")
	}
	for _, tc := range []struct{ asset, output, kind, path string }{{"../original", "out", "audio", path}, {"asset", "out/evil", "audio", path}, {"asset", "out", "original", path}, {"asset", "out", "audio", t.TempDir()}} {
		if _, err := uploader.UploadDerived(context.Background(), tc.asset, tc.output, tc.kind, tc.path); !errors.Is(err, storage.ErrDerivedInput) {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := uploader.UploadDerived(context.Background(), "asset", "out", "audio", link); !errors.Is(err, storage.ErrDerivedInput) {
		t.Fatal(err)
	}
	stub.err = errors.New("provider private secret")
	if _, err := uploader.UploadDerived(context.Background(), "asset", "out", "audio", path); !errors.Is(err, storage.ErrDerivedUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestDerivedRealSDKOfflineCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "offline-test-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "offline-test-secret")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
	if _, err := storage.NewDerivedUploader(context.Background(), config.S3{Region: "us-east-1", Bucket: "private-offline-bucket"}); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptUploadIsPrivateAndImmutable(t *testing.T) {
	stub := &derivedPutStub{}
	uploader, err := storage.NewDerivedUploaderWithClient(config.S3{Region: "offline", Bucket: "private"}, stub)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "transcript.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := uploader.UploadDerived(context.Background(), "asset-id", "output-id", "transcript", path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Key != "dev-uploads/asset-id/derived/v1/output-id/transcript.json" ||
		aws.ToString(stub.input.ContentType) != "application/json" ||
		aws.ToString(stub.input.IfNoneMatch) != "*" ||
		aws.ToString(stub.input.CacheControl) != "no-store" || stub.input.ACL != "" {
		t.Fatal("unsafe transcript object")
	}
}
