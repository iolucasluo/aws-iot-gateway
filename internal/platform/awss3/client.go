package awss3

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

type API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

type Client struct {
	client  API
	presign *s3.PresignClient
	bucket  string
}

func NewClient(config aws.Config, bucket string) (*Client, error) {
	if bucket == "" {
		return nil, fmt.Errorf("AWS S3 bucket is required")
	}
	client := s3.NewFromConfig(config)
	return &Client{client: client, presign: s3.NewPresignClient(client), bucket: bucket}, nil
}

func (client *Client) PrepareUpload(ctx context.Context, key string, size int64, sha256 string, ttl time.Duration) (ota.UploadTarget, error) {
	if client == nil || client.presign == nil || key == "" || size <= 0 || sha256 == "" || ttl <= 0 {
		return ota.UploadTarget{}, fmt.Errorf("presign OTA upload: invalid input")
	}
	checksum, err := sha256Base64(sha256)
	if err != nil {
		return ota.UploadTarget{}, fmt.Errorf("presign OTA upload: %w", err)
	}
	result, err := client.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(client.bucket), Key: aws.String(key),
		ContentType: aws.String("application/gzip"), ContentLength: aws.Int64(size),
		ChecksumSHA256: aws.String(checksum),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return ota.UploadTarget{}, fmt.Errorf("presign OTA upload: %w", err)
	}
	return ota.UploadTarget{
		URL: result.URL, Method: http.MethodPut,
		Headers: map[string]string{
			"Content-Type": "application/gzip", "Content-Length": strconv.FormatInt(size, 10),
			"x-amz-checksum-sha256": checksum,
		},
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

func (client *Client) Verify(ctx context.Context, key string, size int64, sha256 string) error {
	if client == nil || client.client == nil || key == "" || size <= 0 || sha256 == "" {
		return fmt.Errorf("verify OTA object: invalid input")
	}
	checksum, err := sha256Base64(sha256)
	if err != nil {
		return fmt.Errorf("verify OTA object: %w", err)
	}
	result, err := client.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(client.bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		return fmt.Errorf("verify OTA object: %w", err)
	}
	if result.ContentLength == nil || *result.ContentLength != size || result.ChecksumSHA256 == nil || *result.ChecksumSHA256 != checksum {
		return fmt.Errorf("verify OTA object: size or S3 checksum mismatch")
	}
	return nil
}

func sha256Base64(value string) (string, error) {
	digest, err := hex.DecodeString(value)
	if err != nil || len(digest) != 32 {
		return "", fmt.Errorf("invalid SHA-256")
	}
	return base64.StdEncoding.EncodeToString(digest), nil
}

func (client *Client) DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return client.GetURL(ctx, key, ttl)
}

func (client *Client) PutURL(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	result, err := client.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(client.bucket), Key: aws.String(key), ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign S3 upload: %w", err)
	}
	return result.URL, nil
}

func (client *Client) GetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	result, err := client.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(client.bucket), Key: aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign S3 download: %w", err)
	}
	return result.URL, nil
}

func (client *Client) Upload(ctx context.Context, key, contentType string, payload []byte) error {
	length := int64(len(payload))
	if _, err := client.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(client.bucket), Key: aws.String(key), ContentType: aws.String(contentType),
		ContentLength: aws.Int64(length), Body: bytes.NewReader(payload),
	}); err != nil {
		return fmt.Errorf("upload S3 object: %w", err)
	}
	return nil
}

func (client *Client) Download(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("download S3 object: maximum size is invalid")
	}
	result, err := client.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(client.bucket), Key: aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("download S3 object: %w", err)
	}
	defer result.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(result.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read S3 object: %w", err)
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("download S3 object: object exceeds %d bytes", maxBytes)
	}
	return payload, nil
}

func (client *Client) Exists(ctx context.Context, key string) (bool, error) {
	if _, err := client.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(client.bucket), Key: aws.String(key),
	}); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("head S3 object: %w", err)
	}
	return true, nil
}

func isNotFound(err error) bool {
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NotFound", "NoSuchKey", "404":
			return true
		}
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusNotFound
}
