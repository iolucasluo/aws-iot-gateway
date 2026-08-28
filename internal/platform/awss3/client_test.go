package awss3

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

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
}

func (stub *s3APIStub) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return &s3.PutObjectOutput{}, nil
}
func (stub *s3APIStub) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	length := int64(len(stub.body))
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(stub.body)), ContentLength: &length}, nil
}
func (stub *s3APIStub) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return &s3.HeadObjectOutput{}, stub.headErr
}
func (stub *s3APIStub) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, nil
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
