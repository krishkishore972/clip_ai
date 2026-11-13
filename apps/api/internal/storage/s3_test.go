package storage

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/krishkishore972/clip_ai/internal/config"
)

type sdkStub struct {
	err       error
	ctx       context.Context
	input     any
	create    *s3.CreateMultipartUploadOutput
	pages     []*s3.ListPartsOutput
	listCalls []*s3.ListPartsInput
	head      *s3.HeadObjectOutput
}

func (f *sdkStub) CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	f.ctx, f.input = ctx, in
	return f.create, f.err
}

func (f *sdkStub) ListParts(ctx context.Context, in *s3.ListPartsInput, _ ...func(*s3.Options)) (*s3.ListPartsOutput, error) {
	f.ctx, f.input = ctx, in
	f.listCalls = append(f.listCalls, in)
	if f.err != nil {
		return nil, f.err
	}
	i := len(f.listCalls) - 1
	if i >= len(f.pages) {
		return nil, errors.New("unexpected extra list request")
	}
	return f.pages[i], nil
}

func (f *sdkStub) CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	f.ctx, f.input = ctx, in
	return &s3.CompleteMultipartUploadOutput{}, f.err
}

func (f *sdkStub) AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.ctx, f.input = ctx, in
	return &s3.AbortMultipartUploadOutput{}, f.err
}

func (f *sdkStub) HeadObject(ctx context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.ctx, f.input = ctx, in
	return f.head, f.err
}

type presignerStub struct {
	err   error
	ctx   context.Context
	input *s3.UploadPartInput
	ttl   time.Duration
	out   *v4.PresignedHTTPRequest
}

func (f *presignerStub) PresignUploadPart(ctx context.Context, in *s3.UploadPartInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	f.ctx, f.input = ctx, in
	var options s3.PresignOptions
	for _, apply := range opts {
		apply(&options)
	}
	f.ttl = options.Expires
	return f.out, f.err
}

func testStore(client *sdkStub) *S3 {
	return &S3{client: client, bucket: "private-example-bucket", presignTTL: 15 * time.Minute}
}

func TestS3Begin(t *testing.T) {
	t.Parallel()
	fake := &sdkStub{create: &s3.CreateMultipartUploadOutput{UploadId: aws.String("aws-upload-1")}}
	id, err := testStore(fake).Begin(context.Background(), "sources/server-generated-id", "video/mp4")
	if err != nil || id != "aws-upload-1" {
		t.Fatalf("Begin() = %q, %v", id, err)
	}
	in := fake.input.(*s3.CreateMultipartUploadInput)
	if aws.ToString(in.Bucket) != "private-example-bucket" || aws.ToString(in.Key) != "sources/server-generated-id" ||
		aws.ToString(in.ContentType) != "video/mp4" || in.ACL != "" {
		t.Fatalf("unexpected initiation input: %+v", in)
	}
}

func TestS3PresignPart(t *testing.T) {
	t.Parallel()
	fake := &presignerStub{out: &v4.PresignedHTTPRequest{
		URL: "https://example.invalid/signed", Method: http.MethodPut,
		SignedHeader: http.Header{"Content-Length": {"1024"}},
	}}
	store := testStore(&sdkStub{})
	store.presigner = fake
	before := time.Now().UTC()
	out, err := store.PresignPart(context.Background(), "sources/id", "aws-upload-1", 2, 1024)
	if err != nil || out.URL != fake.out.URL || out.Method != http.MethodPut || out.Headers.Get("Content-Length") != "1024" {
		t.Fatalf("unexpected signing result: %+v, %v", out, err)
	}
	if out.ExpiresAt.Before(before.Add(store.presignTTL)) || out.ExpiresAt.After(time.Now().Add(store.presignTTL)) {
		t.Fatal("incorrect expiration")
	}
	if fake.ttl != 15*time.Minute || aws.ToInt32(fake.input.PartNumber) != 2 || aws.ToInt64(fake.input.ContentLength) != 1024 ||
		aws.ToString(fake.input.Bucket) != store.bucket || aws.ToString(fake.input.Key) != "sources/id" || aws.ToString(fake.input.UploadId) != "aws-upload-1" {
		t.Fatalf("unexpected signing input: %+v, ttl=%s", fake.input, fake.ttl)
	}
	out.Headers.Set("Content-Length", "changed")
	if fake.out.SignedHeader.Get("Content-Length") != "1024" {
		t.Fatal("returned headers alias SDK response")
	}
}

func s3Part(number int32, size int64, etag string) types.Part {
	return types.Part{PartNumber: aws.Int32(number), Size: aws.Int64(size), ETag: aws.String(etag)}
}

func TestS3ListPartsPagination(t *testing.T) {
	t.Parallel()
	fake := &sdkStub{pages: []*s3.ListPartsOutput{
		{Parts: []types.Part{s3Part(1, 16<<20, "etag-1")}, IsTruncated: aws.Bool(true), NextPartNumberMarker: aws.String("1")},
		{Parts: []types.Part{s3Part(2, 1024, "etag-2")}, IsTruncated: aws.Bool(false)},
	}}
	out, err := testStore(fake).ListParts(context.Background(), "sources/id", "aws-upload-1")
	want := []Part{{Number: 1, SizeBytes: 16 << 20, ETag: "etag-1"}, {Number: 2, SizeBytes: 1024, ETag: "etag-2"}}
	if err != nil || !reflect.DeepEqual(out, want) {
		t.Fatalf("ListParts() = %+v, %v", out, err)
	}
	if len(fake.listCalls) != 2 || fake.listCalls[0].PartNumberMarker != nil || aws.ToString(fake.listCalls[1].PartNumberMarker) != "1" {
		t.Fatalf("wrong pagination: %+v", fake.listCalls)
	}
	for _, in := range fake.listCalls {
		if aws.ToString(in.Bucket) != "private-example-bucket" || aws.ToString(in.Key) != "sources/id" || aws.ToString(in.UploadId) != "aws-upload-1" {
			t.Fatal("incorrect list-parts target")
		}
	}
}

func TestS3ListPartsRejectsMalformedResponses(t *testing.T) {
	t.Parallel()
	for name, pages := range map[string][]*s3.ListPartsOutput{
		"nil output":     {nil},
		"missing marker": {{IsTruncated: aws.Bool(true)}},
		"repeated marker": {
			{IsTruncated: aws.Bool(true), NextPartNumberMarker: aws.String("1")},
			{IsTruncated: aws.Bool(true), NextPartNumberMarker: aws.String("1")},
		},
		"duplicate part": {{Parts: []types.Part{s3Part(1, 1, "a"), s3Part(1, 1, "a")}}},
		"zero part":      {{Parts: []types.Part{s3Part(0, 1, "a")}}},
		"negative size":  {{Parts: []types.Part{s3Part(1, -1, "a")}}},
		"missing etag":   {{Parts: []types.Part{s3Part(1, 1, "")}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &sdkStub{pages: pages}
			if _, err := testStore(fake).ListParts(context.Background(), "sources/id", "aws-upload-1"); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}

func TestS3CompleteSortsCopy(t *testing.T) {
	t.Parallel()
	fake := &sdkStub{}
	parts := []Part{{Number: 2, ETag: "etag-2"}, {Number: 1, ETag: "etag-1"}}
	if err := testStore(fake).Complete(context.Background(), "sources/id", "aws-upload-1", parts); err != nil {
		t.Fatal(err)
	}
	in := fake.input.(*s3.CompleteMultipartUploadInput)
	if aws.ToString(in.Bucket) != "private-example-bucket" || aws.ToString(in.Key) != "sources/id" || aws.ToString(in.UploadId) != "aws-upload-1" {
		t.Fatal("incorrect completion target")
	}
	if aws.ToInt32(in.MultipartUpload.Parts[0].PartNumber) != 1 || aws.ToString(in.MultipartUpload.Parts[0].ETag) != "etag-1" ||
		aws.ToInt32(in.MultipartUpload.Parts[1].PartNumber) != 2 || parts[0].Number != 2 {
		t.Fatal("parts not sorted or caller input was mutated")
	}
}

func TestS3AbortAndHead(t *testing.T) {
	t.Parallel()
	fake := &sdkStub{head: &s3.HeadObjectOutput{ContentLength: aws.Int64(1234), ContentType: aws.String("video/mp4"), ETag: aws.String("object-etag")}}
	store := testStore(fake)
	if err := store.Abort(context.Background(), "sources/id", "aws-upload-1"); err != nil {
		t.Fatal(err)
	}
	abort := fake.input.(*s3.AbortMultipartUploadInput)
	if aws.ToString(abort.Bucket) != store.bucket || aws.ToString(abort.Key) != "sources/id" || aws.ToString(abort.UploadId) != "aws-upload-1" {
		t.Fatal("incorrect abort target")
	}
	out, err := store.Head(context.Background(), "sources/id")
	if err != nil || out != (ObjectInfo{SizeBytes: 1234, ContentType: "video/mp4", ETag: "object-etag"}) {
		t.Fatalf("Head() = %+v, %v", out, err)
	}
	head := fake.input.(*s3.HeadObjectInput)
	if aws.ToString(head.Bucket) != store.bucket || aws.ToString(head.Key) != "sources/id" {
		t.Fatal("incorrect head target")
	}
}

func TestS3ProviderErrorsAndContext(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"begin", "sign", "list", "complete", "abort", "head"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			fake := &sdkStub{err: context.Canceled}
			signer := &presignerStub{err: context.Canceled}
			store := testStore(fake)
			store.presigner = signer
			var err error
			switch op {
			case "begin":
				_, err = store.Begin(ctx, "sources/id", "video/mp4")
			case "sign":
				_, err = store.PresignPart(ctx, "sources/id", "aws-upload-1", 1, 1234)
			case "list":
				_, err = store.ListParts(ctx, "sources/id", "aws-upload-1")
			case "complete":
				err = store.Complete(ctx, "sources/id", "aws-upload-1", []Part{{Number: 1, ETag: "etag-1"}})
			case "abort":
				err = store.Abort(ctx, "sources/id", "aws-upload-1")
			case "head":
				_, err = store.Head(ctx, "sources/id")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error does not wrap cause: %v", err)
			}
			got := fake.ctx
			if op == "sign" {
				got = signer.ctx
			}
			if got != ctx {
				t.Fatal("request context was not forwarded")
			}
		})
	}
}

func TestS3RejectsInvalidInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Nil clients ensure invalid inputs cannot reach an SDK method.
	store := &S3{}
	checks := []func() error{
		func() error { _, err := store.Begin(ctx, "", "video/mp4"); return err },
		func() error { _, err := store.PresignPart(ctx, "k", "u", 0, 1); return err },
		func() error { _, err := store.PresignPart(ctx, "k", "u", 10001, 1); return err },
		func() error { _, err := store.PresignPart(ctx, "k", "u", 1, 0); return err },
		func() error { _, err := store.ListParts(ctx, "k", ""); return err },
		func() error { return store.Complete(ctx, "k", "u", nil) },
		func() error {
			return store.Complete(ctx, "k", "u", []Part{{Number: 1, ETag: "a"}, {Number: 1, ETag: "b"}})
		},
		func() error { return store.Complete(ctx, "k", "u", []Part{{Number: 1}}) },
		func() error { return store.Complete(ctx, "k", "u", []Part{{Number: 0, ETag: "a"}}) },
		func() error { return store.Abort(ctx, "", "u") },
		func() error { _, err := store.Head(ctx, ""); return err },
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Errorf("check %d accepted invalid input", i)
		}
	}
}

func TestS3RejectsMalformedOutputs(t *testing.T) {
	t.Parallel()
	store := testStore(&sdkStub{})
	if _, err := store.Begin(context.Background(), "k", "video/mp4"); err == nil {
		t.Fatal("missing AWS upload ID accepted")
	}
	if _, err := store.Head(context.Background(), "k"); err == nil {
		t.Fatal("missing object metadata accepted")
	}
	store.presigner = &presignerStub{}
	if _, err := store.PresignPart(context.Background(), "k", "u", 1, 1); err == nil {
		t.Fatal("missing signed request accepted")
	}
}

// This HTTP client fails closed: the real SDK presigner must stay offline.
type denyHTTP struct{ t *testing.T }

func (d denyHTTP) Do(*http.Request) (*http.Response, error) {
	d.t.Error("unexpected HTTP call")
	return nil, errors.New("network disabled in test")
}

func TestRealSDKPresigningOffline(t *testing.T) {
	t.Parallel()
	client := s3.NewFromConfig(aws.Config{
		Region: "ap-south-1", HTTPClient: denyHTTP{t},
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "TESTKEY", SecretAccessKey: "test-secret-not-real"}, nil
		}),
	})
	store := &S3{client: client, presigner: s3.NewPresignClient(client), bucket: "private-example-bucket", presignTTL: 10 * time.Minute}
	out, err := store.PresignPart(context.Background(), "sources/id", "aws-upload-1", 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	if out.Method != http.MethodPut || u.Scheme != "https" || !strings.HasSuffix(u.Host, ".amazonaws.com") ||
		query.Get("partNumber") != "2" || query.Get("uploadId") != "aws-upload-1" ||
		query.Get("X-Amz-Expires") != "600" || query.Get("X-Amz-Signature") == "" {
		t.Fatal("SDK did not produce the expected signed PUT")
	}
}

func TestNewS3(t *testing.T) {
	// Isolate from the developer's actual CLI credentials/config and metadata.
	for key, value := range map[string]string{
		"AWS_ACCESS_KEY_ID": "TESTKEY", "AWS_SECRET_ACCESS_KEY": "not-real", "AWS_SESSION_TOKEN": "",
		"AWS_PROFILE": "", "AWS_DEFAULT_PROFILE": "", "AWS_EC2_METADATA_DISABLED": "true",
		"AWS_CONFIG_FILE":             filepath.Join(t.TempDir(), "missing-config"),
		"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(t.TempDir(), "missing-credentials"),
	} {
		t.Setenv(key, value)
	}
	store, err := NewS3(context.Background(), config.S3{Region: "ap-south-1", Bucket: "private-example-bucket", PresignTTL: 15 * time.Minute})
	if err != nil || store == nil || store.bucket != "private-example-bucket" || store.presignTTL != 15*time.Minute {
		t.Fatalf("NewS3() = %+v, %v", store, err)
	}
}

func TestNewS3RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	for _, cfg := range []config.S3{
		{},
		{Region: "ap-south-1", Bucket: "s3://bucket", PresignTTL: time.Minute},
		{Region: "ap-south-1", Bucket: "bucket", PresignTTL: 16 * time.Minute},
		{Region: "ap-south-1", Bucket: "bucket", PresignTTL: time.Millisecond},
	} {
		if _, err := NewS3(context.Background(), cfg); err == nil {
			t.Fatal("invalid S3 configuration accepted")
		}
	}
}

func TestS3MissingResourceClassification(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"list", "complete", "abort", "head"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			code, want := "NoSuchUpload", ErrUploadNotFound
			if op == "head" {
				code, want = "NotFound", ErrObjectNotFound
			}
			cause := &smithy.GenericAPIError{Code: code, Message: "test failure"}
			store := testStore(&sdkStub{err: cause})
			var err error
			switch op {
			case "list":
				_, err = store.ListParts(context.Background(), "key", "aws-id")
			case "complete":
				err = store.Complete(context.Background(), "key", "aws-id", []Part{{Number: 1, ETag: "tag"}})
			case "abort":
				err = store.Abort(context.Background(), "key", "aws-id")
			case "head":
				_, err = store.Head(context.Background(), "key")
			}
			if !errors.Is(err, want) || !errors.Is(err, cause) {
				t.Fatalf("classification/cause lost: %v", err)
			}
		})
	}
}

func TestS3PartTTLOverride(t *testing.T) {
	t.Parallel()
	signer := &presignerStub{out: &v4.PresignedHTTPRequest{URL: "https://example.invalid/part", Method: http.MethodPut}}
	store := testStore(&sdkStub{})
	store.presigner = signer
	if _, err := store.PresignPart(context.Background(), "key", "aws-id", 1, 1024, 5*time.Second); err != nil || signer.ttl != 5*time.Second {
		t.Fatalf("TTL override not applied: %v", err)
	}
	for _, ttl := range [][]time.Duration{{0}, {time.Millisecond}, {16 * time.Minute}, {time.Second, time.Second}} {
		if _, err := store.PresignPart(context.Background(), "key", "aws-id", 1, 1024, ttl...); err == nil {
			t.Fatal("invalid TTL override accepted")
		}
	}
}
