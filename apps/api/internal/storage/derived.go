package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/krishkishore972/clip_ai/internal/config"
)

var (
	ErrDerivedInput       = errors.New("storage: invalid derived object input")
	ErrDerivedUnavailable = errors.New("storage: derived object operation unavailable")
)

// ObjectPutter intentionally has no original, multipart, or delete operations.
type ObjectPutter interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}
type DerivedObject struct {
	Key       string
	SizeBytes int64
	SHA256    string
}
type DerivedUploader struct {
	client ObjectPutter
	bucket string
}

var derivedIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func validDerivedConfig(cfg config.S3) bool {
	return strings.TrimSpace(cfg.Region) != "" && strings.TrimSpace(cfg.Bucket) != "" && !strings.ContainsAny(cfg.Bucket, "/\\ \t\r\n")
}
func NewDerivedUploader(ctx context.Context, cfg config.S3) (*DerivedUploader, error) {
	if !validDerivedConfig(cfg) {
		return nil, ErrDerivedInput
	}
	sdk, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, ErrDerivedUnavailable
	}
	return NewDerivedUploaderWithClient(cfg, s3.NewFromConfig(sdk))
}
func NewDerivedUploaderWithClient(cfg config.S3, client ObjectPutter) (*DerivedUploader, error) {
	if !validDerivedConfig(cfg) || client == nil {
		return nil, ErrDerivedInput
	}
	return &DerivedUploader{client: client, bucket: cfg.Bucket}, nil
}

// UploadDerived accepts only server-generated identifiers and a private local
// output. Callers must prevent concurrent writes to that file. S3 validates the
// exact length and SHA256; conditional creation forbids replacing any object.
// No ACL is set: bucket policy must deny public access. Errors never embed SDK
// responses, paths or capabilities. The caller supplies a bounded job context.
func (u *DerivedUploader) UploadDerived(ctx context.Context, assetID, outputID, kind, path string) (DerivedObject, error) {
	var result DerivedObject
	if u == nil || u.client == nil || !derivedIdentifier.MatchString(assetID) || !derivedIdentifier.MatchString(outputID) {
		return result, ErrDerivedInput
	}
	name, contentType, capBytes := "", "", int64(0)
	switch kind {
	case "audio":
		name, contentType, capBytes = "audio.wav", "audio/wav", 240_000_000
	default:
		return result, ErrDerivedInput
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > capBytes {
		return result, ErrDerivedInput
	}
	file, err := os.Open(path)
	if err != nil {
		return result, ErrDerivedInput
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, ErrDerivedInput
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var size int64
	for {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		n, readErr := file.Read(buffer)
		size += int64(n)
		if size > capBytes {
			return result, ErrDerivedInput
		}
		_, _ = hash.Write(buffer[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, ErrDerivedInput
		}
	}
	if size != info.Size() {
		return result, ErrDerivedInput
	}
	sum := hash.Sum(nil)
	checksum := base64.StdEncoding.EncodeToString(sum)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return result, ErrDerivedInput
	}
	key := "dev-uploads/" + assetID + "/derived/v1/" + outputID + "/" + name
	response, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(u.bucket), Key: aws.String(key), Body: file, ContentLength: aws.Int64(size), ContentType: aws.String(contentType), CacheControl: aws.String("no-store"), IfNoneMatch: aws.String("*"), ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: aws.String(checksum),
	}, func(options *s3.Options) { options.RetryMaxAttempts = 1 })
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, ErrDerivedUnavailable
	}
	if response == nil || aws.ToString(response.ChecksumSHA256) != checksum {
		return result, ErrDerivedUnavailable
	}
	finalInfo, err := file.Stat()
	if err != nil || finalInfo.Size() != size || !finalInfo.ModTime().Equal(info.ModTime()) {
		return result, ErrDerivedInput
	}
	return DerivedObject{Key: key, SizeBytes: size, SHA256: hex.EncodeToString(sum)}, nil
}
