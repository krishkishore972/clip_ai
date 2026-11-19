package storage

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var (
	ErrDownloadTimeout      = errors.New("storage: download timed out")
	ErrDownloadCanceled     = errors.New("storage: download canceled")
	ErrDownloadUnavailable  = errors.New("storage: download unavailable")
	ErrDownloadSizeMismatch = errors.New("storage: download size mismatch")
	ErrDownloadInvalidInput = errors.New("storage: invalid download input")
	ErrDownloadDestination  = errors.New("storage: download destination unavailable")
)

const maxDownloadBytes int64 = 2_000_000_000

type objectGetter interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// Downloader reads immutable originals from one trusted, configured bucket.
// It never changes or deletes the source object.
type Downloader struct {
	client objectGetter
	bucket string
}

// NewDownloader uses the AWS default credential chain, including CLI profiles,
// SSO and workload roles. Construction does not contact or probe the bucket.
func NewDownloader(ctx context.Context, region, bucket string) (*Downloader, error) {
	if strings.TrimSpace(region) == "" || strings.TrimSpace(bucket) == "" ||
		strings.ContainsAny(bucket, "/\\ \t\r\n") {
		return nil, ErrDownloadInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, downloadError(ctx, err)
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, downloadError(ctx, err)
	}
	return &Downloader{client: s3.NewFromConfig(cfg), bucket: bucket}, nil
}

// Download streams at most expectedBytes+1 bytes to a new exclusive 0600 file.
// The caller must provide a destination in its private temporary directory,
// with trusted parent directories, and a context with a finite job deadline.
// Existing files and symlinks are never overwritten or removed. Failed new
// files are removed only if the path still identifies the file we created.
func (d *Downloader) Download(ctx context.Context, key, destination string, expectedBytes int64) (result error) {
	if d == nil || d.client == nil || d.bucket == "" || key == "" || destination == "" ||
		expectedBytes <= 0 || expectedBytes > maxDownloadBytes {
		return ErrDownloadInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return downloadError(ctx, err)
	}
	path, err := filepath.Abs(destination)
	if err != nil {
		return ErrDownloadDestination
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrDownloadDestination
	}
	owned, statErr := file.Stat()
	defer func() {
		if err := file.Close(); err != nil && result == nil {
			result = ErrDownloadDestination
		}
		if result != nil && statErr == nil {
			current, err := os.Lstat(path)
			if err == nil && current.Mode().IsRegular() && os.SameFile(owned, current) {
				// path is absolute and still names our exclusively created file.
				_ = os.Remove(path)
			}
		}
	}()
	if statErr != nil {
		return ErrDownloadDestination
	}

	out, err := d.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(key),
	})
	if err != nil {
		if out != nil && out.Body != nil {
			_ = out.Body.Close()
		}
		return downloadError(ctx, err)
	}
	if out == nil || out.Body == nil {
		return ErrDownloadUnavailable
	}

	// Closing an AWS HTTP response body interrupts a pending Read. SDK context
	// cancellation also aborts the request itself. Test adapters must likewise
	// implement ReadCloser.Close so that it unblocks a pending Read.
	var closeOnce sync.Once
	var closeErr error
	closeBody := func() { closeOnce.Do(func() { closeErr = out.Body.Close() }) }
	stop := context.AfterFunc(ctx, closeBody)
	defer func() {
		stop()
		closeBody()
		if err := ctx.Err(); err != nil {
			result = downloadError(ctx, err)
		} else if closeErr != nil && result == nil {
			result = ErrDownloadUnavailable
		}
	}()

	if out.ContentLength != nil && *out.ContentLength != expectedBytes {
		return ErrDownloadSizeMismatch
	}
	count, copyErr := io.CopyN(file, &downloadReader{ctx: ctx, reader: out.Body}, expectedBytes+1)
	if err := ctx.Err(); err != nil {
		return downloadError(ctx, err)
	}
	if count > expectedBytes {
		return ErrDownloadSizeMismatch
	}
	if copyErr != nil && !errors.Is(copyErr, io.EOF) && !errors.Is(copyErr, io.ErrUnexpectedEOF) {
		return downloadError(ctx, copyErr)
	}
	if count != expectedBytes {
		return ErrDownloadSizeMismatch
	}
	return nil
}

type downloadReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *downloadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Return only fixed local errors: never retain provider responses, object keys,
// host paths, credentials, or raw transport errors in the error chain.
func downloadError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrDownloadTimeout, context.DeadlineExceeded)
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return errors.Join(ErrDownloadCanceled, context.Canceled)
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ErrDownloadTimeout
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return ErrObjectNotFound
		}
	}
	return ErrDownloadUnavailable
}
