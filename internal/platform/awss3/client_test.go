package awss3

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const otaSHA256Hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func otaSHA256Base64(t *testing.T) string {
	t.Helper()
	digest, err := hex.DecodeString(otaSHA256Hex)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(digest)
}

func TestPutURLWithSizeSignsContentLengthAndType(t *testing.T) {
	t.Parallel()
	client, err := NewClient(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
	}, "media-bucket")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	rawURL, err := client.PutURLWithSize(
		context.Background(), "thumbnail.jpg", "image/jpeg", 123, 15*time.Minute,
	)
	if err != nil {
		t.Fatalf("PutURLWithSize() error = %v", err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse presigned URL: %v", err)
	}
	signedHeaders := parsed.Query().Get("X-Amz-SignedHeaders")
	if signedHeaders != "content-length;content-type;host" {
		t.Fatalf("signed headers = %q", signedHeaders)
	}
}

type s3APIStub struct {
	body    string
	headErr error
	head    *s3.HeadObjectOutput
	headIn  *s3.HeadObjectInput
}

func (stub *s3APIStub) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return &s3.PutObjectOutput{}, nil
}
func (stub *s3APIStub) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	length := int64(len(stub.body))
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(stub.body)), ContentLength: &length}, nil
}
func (stub *s3APIStub) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	stub.headIn = input
	if stub.head != nil {
		return stub.head, stub.headErr
	}
	return &s3.HeadObjectOutput{}, stub.headErr
}
func (stub *s3APIStub) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, nil
}

func TestVerifyOTAObjectRequiresS3ValidatedSHA256Checksum(t *testing.T) {
	t.Parallel()
	length := int64(1234)
	checksum := otaSHA256Base64(t)
	stub := &s3APIStub{head: &s3.HeadObjectOutput{
		ContentLength: &length, ChecksumSHA256: &checksum,
	}}
	client := &Client{client: stub, bucket: "ota-bucket"}
	if err := client.Verify(context.Background(), "ota/firmware.tar.gz", length, otaSHA256Hex); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if stub.headIn == nil || stub.headIn.ChecksumMode != types.ChecksumModeEnabled {
		t.Fatalf("HeadObject checksum mode = %#v", stub.headIn)
	}
	if err := client.Verify(context.Background(), "ota/firmware.tar.gz", length+1, otaSHA256Hex); err == nil {
		t.Fatal("Verify() accepted mismatched size")
	}
}

func TestPrepareOTAUploadBindsS3NativeChecksum(t *testing.T) {
	t.Parallel()
	client, err := NewClient(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("key", "secret", ""),
	}, "ota-bucket")
	if err != nil {
		t.Fatal(err)
	}
	target, err := client.PrepareUpload(context.Background(), "ota/firmware.tar.gz", 1234, otaSHA256Hex, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if target.Headers["x-amz-checksum-sha256"] != otaSHA256Base64(t) {
		t.Fatalf("upload checksum headers = %#v", target.Headers)
	}
	if _, found := target.Headers["x-amz-meta-sha256"]; found {
		t.Fatalf("uploader-controlled digest metadata is not a checksum: %#v", target.Headers)
	}
}

func TestDownloadReturnsBoundedS3Object(t *testing.T) {
	t.Parallel()
	client := &Client{client: &s3APIStub{body: "h264-idr"}, bucket: "media-bucket"}

	payload, err := client.Download(context.Background(), "snapshots/id.h264", 32)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(payload) != "h264-idr" {
		t.Fatalf("Download() = %q", payload)
	}
}

func TestExistsDistinguishesMissingObjectFromS3Failure(t *testing.T) {
	t.Parallel()
	missing := &Client{client: &s3APIStub{headErr: &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}}, bucket: "media-bucket"}
	exists, err := missing.Exists(context.Background(), "snapshots/id.jpg")
	if err != nil || exists {
		t.Fatalf("missing Exists() = %v, %v", exists, err)
	}

	denied := &Client{client: &s3APIStub{headErr: &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}}, bucket: "media-bucket"}
	if _, err := denied.Exists(context.Background(), "snapshots/id.jpg"); err == nil {
		t.Fatal("Exists() must propagate non-not-found S3 errors")
	}
}
