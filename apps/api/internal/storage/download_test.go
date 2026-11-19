package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type getObjectFunc func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error)

func (f getObjectFunc) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return f(ctx, in)
}

type downloadTestBody struct {
	io.Reader
	closeErr error
	closed   bool
}

func (b *downloadTestBody) Close() error { b.closed = true; return b.closeErr }

type errorDownloadReader struct{ err error }

func (r errorDownloadReader) Read([]byte) (int, error) { return 0, r.err }

func assertDownloadAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial destination remains: %v", err)
	}
}

func TestDownloadStreaming(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, data string
		length     *int64
		want       error
	}{
		{"exact", "video", aws.Int64(5), nil},
		{"unknown length", "video", nil, nil},
		{"short stream", "vide", nil, ErrDownloadSizeMismatch},
		{"empty stream", "", nil, ErrDownloadSizeMismatch},
		{"oversize stream", "video plus more bytes", nil, ErrDownloadSizeMismatch},
		{"short advertised", "video", aws.Int64(4), ErrDownloadSizeMismatch},
		{"oversize advertised", "video", aws.Int64(6), ErrDownloadSizeMismatch},
		{"negative advertised", "video", aws.Int64(-1), ErrDownloadSizeMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := strings.NewReader(tc.data)
			body := &downloadTestBody{Reader: reader}
			d := &Downloader{bucket: "trusted-bucket", client: getObjectFunc(func(ctx context.Context, in *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
				if aws.ToString(in.Bucket) != "trusted-bucket" || aws.ToString(in.Key) != "private/key" || ctx == nil {
					t.Fatal("incorrect GetObject request")
				}
				return &s3.GetObjectOutput{Body: body, ContentLength: tc.length}, nil
			})}
			path := filepath.Join(t.TempDir(), "original")
			if err := d.Download(context.Background(), "private/key", path, 5); !errors.Is(err, tc.want) {
				t.Fatalf("Download = %v, want %v", err, tc.want)
			}
			if !body.closed {
				t.Fatal("response body not closed")
			}
			if tc.length != nil && *tc.length != 5 && reader.Len() != len(tc.data) {
				t.Fatal("read body with mismatched metadata")
			}
			if len(tc.data)-reader.Len() > 6 {
				t.Fatal("read beyond expected size plus one")
			}
			if tc.want != nil {
				assertDownloadAbsent(t, path)
				return
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != tc.data {
				t.Fatalf("downloaded data = %q, %v", data, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("expected private 0600 file: %v, %v", info, err)
			}
		})
	}
}

func TestDownloadSanitizedFailures(t *testing.T) {
	t.Parallel()
	secret := "SECRET/private-key https://credentials:password@host/local-path"
	for _, tc := range []struct {
		name string
		out  *s3.GetObjectOutput
		err  error
		want error
	}{
		{"provider error", nil, errors.New(secret), ErrDownloadUnavailable},
		{"missing", nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: secret}, ErrObjectNotFound},
		{"access denied", nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: secret}, ErrDownloadUnavailable},
		{"nil response", nil, nil, ErrDownloadUnavailable},
		{"nil body", &s3.GetObjectOutput{}, nil, ErrDownloadUnavailable},
		{"read error", &s3.GetObjectOutput{Body: &downloadTestBody{Reader: errorDownloadReader{errors.New(secret)}}}, nil, ErrDownloadUnavailable},
		{"close error", &s3.GetObjectOutput{Body: &downloadTestBody{Reader: strings.NewReader("video"), closeErr: errors.New(secret)}}, nil, ErrDownloadUnavailable},
		{"truncated stream", &s3.GetObjectOutput{Body: &downloadTestBody{Reader: errorDownloadReader{io.ErrUnexpectedEOF}}}, nil, ErrDownloadSizeMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := &Downloader{bucket: "bucket", client: getObjectFunc(func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) { return tc.out, tc.err })}
			path := filepath.Join(t.TempDir(), "original")
			err := d.Download(context.Background(), secret, path, 5)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), path) {
				t.Fatalf("incorrect or unsanitized error: %v", err)
			}
			if tc.err != nil && errors.Is(err, tc.err) {
				t.Fatal("raw provider error retained")
			}
			assertDownloadAbsent(t, path)
		})
	}
}

func TestDownloadRejectsExistingPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	existing, link, dangling := filepath.Join(dir, "existing"), filepath.Join(dir, "link"), filepath.Join(dir, "dangling")
	if err := os.WriteFile(existing, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	d := &Downloader{bucket: "bucket", client: getObjectFunc(func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		t.Fatal("GetObject called for existing destination")
		return nil, nil
	})}
	for _, path := range []string{existing, link, dangling, dir} {
		if err := d.Download(context.Background(), "key", path, 5); !errors.Is(err, ErrDownloadDestination) {
			t.Fatalf("Download = %v", err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("existing path removed", err)
		}
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
}

func TestDownloadInvalidInputs(t *testing.T) {
	t.Parallel()
	d := &Downloader{bucket: "bucket", client: getObjectFunc(func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		t.Fatal("GetObject called for invalid input")
		return nil, nil
	})}
	path := filepath.Join(t.TempDir(), "original")
	for _, size := range []int64{-1, 0, maxDownloadBytes + 1, 1<<63 - 1} {
		if err := d.Download(context.Background(), "key", path, size); !errors.Is(err, ErrDownloadInvalidInput) {
			t.Fatalf("size %d = %v", size, err)
		}
	}
	for _, in := range [][2]string{{"", path}, {"key", ""}} {
		if err := d.Download(context.Background(), in[0], in[1], 5); !errors.Is(err, ErrDownloadInvalidInput) {
			t.Fatal(err)
		}
	}
	assertDownloadAbsent(t, path)
	for _, cfg := range [][2]string{{"", "bucket"}, {"region", ""}, {"region", "bucket/key"}} {
		if _, err := NewDownloader(context.Background(), cfg[0], cfg[1]); !errors.Is(err, ErrDownloadInvalidInput) {
			t.Fatal(err)
		}
	}
}

type blockingDownloadBody struct {
	started, done chan struct{}
	once          sync.Once
}

func (b *blockingDownloadBody) Read([]byte) (int, error) {
	close(b.started)
	<-b.done
	return 0, errors.New("secret transport close error")
}
func (b *blockingDownloadBody) Close() error { b.once.Do(func() { close(b.done) }); return nil }

func TestDownloadCancellationInterruptsRead(t *testing.T) {
	t.Parallel()
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			want, contextErr := ErrDownloadCanceled, context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				want, contextErr = ErrDownloadTimeout, context.DeadlineExceeded
			}
			defer cancel()
			body := &blockingDownloadBody{started: make(chan struct{}), done: make(chan struct{})}
			d := &Downloader{bucket: "bucket", client: getObjectFunc(func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
				return &s3.GetObjectOutput{Body: body}, nil
			})}
			path := filepath.Join(t.TempDir(), "original")
			result := make(chan error, 1)
			go func() { result <- d.Download(ctx, "key", path, 5) }()
			select {
			case <-body.started:
			case <-time.After(3 * time.Second):
				t.Fatal("read never started")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) || !errors.Is(err, contextErr) {
					t.Fatalf("cancellation = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation did not interrupt Read")
			}
			assertDownloadAbsent(t, path)
		})
	}
}

func TestDownloadAlreadyCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewDownloader(ctx, "region", "bucket"); !errors.Is(err, ErrDownloadCanceled) {
		t.Fatal(err)
	}
	d := &Downloader{bucket: "bucket", client: getObjectFunc(func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		t.Fatal("GetObject called after cancellation")
		return nil, nil
	})}
	path := filepath.Join(t.TempDir(), "original")
	if err := d.Download(ctx, "key", path, 5); !errors.Is(err, ErrDownloadCanceled) {
		t.Fatal(err)
	}
	assertDownloadAbsent(t, path)
}

func TestDownloadPreservesReplacementOnCleanup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, moved := filepath.Join(dir, "original"), filepath.Join(dir, "moved")
	d := &Downloader{bucket: "bucket", client: getObjectFunc(func(context.Context, *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		// Both paths are known absolute paths inside this test's private directory.
		if !filepath.IsAbs(path) || !filepath.IsAbs(moved) {
			t.Fatal("expected absolute test paths")
		}
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("provider failure")
	})}
	if err := d.Download(context.Background(), "key", path, 5); !errors.Is(err, ErrDownloadUnavailable) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement removed or changed: %q %v", data, err)
	}
}
