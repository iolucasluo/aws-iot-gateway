package aliyunoss

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

var (
	regionPattern = regexp.MustCompile(`^cn-[a-z0-9-]+$`)
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

type Config struct {
	Region          string
	Endpoint        string
	PublicEndpoint  string
	Bucket          string
	AccessKeyID     string
	AccessKeySecret string
}

type API interface {
	PutObject(context.Context, *oss.PutObjectRequest, ...func(*oss.Options)) (*oss.PutObjectResult, error)
	GetObject(context.Context, *oss.GetObjectRequest, ...func(*oss.Options)) (*oss.GetObjectResult, error)
	HeadObject(context.Context, *oss.HeadObjectRequest, ...func(*oss.Options)) (*oss.HeadObjectResult, error)
	DeleteObject(context.Context, *oss.DeleteObjectRequest, ...func(*oss.Options)) (*oss.DeleteObjectResult, error)
}

type Presigner interface {
	Presign(context.Context, any, ...func(*oss.PresignOptions)) (*oss.PresignResult, error)
}

type Client struct {
	api       API
	presigner Presigner
	bucket    string
}

func New(config Config) (*Client, error) {
	config.Region = strings.TrimSpace(config.Region)
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	config.PublicEndpoint = strings.TrimSpace(config.PublicEndpoint)
	config.Bucket = strings.TrimSpace(config.Bucket)
	config.AccessKeyID = strings.TrimSpace(config.AccessKeyID)
	config.AccessKeySecret = strings.TrimSpace(config.AccessKeySecret)
	if !regionPattern.MatchString(config.Region) {
		return nil, fmt.Errorf("configure OSS: region is invalid")
	}
	if err := validateEndpoint(config.Endpoint, false); err != nil {
		return nil, fmt.Errorf("configure OSS API endpoint: %w", err)
	}
	if err := validateEndpoint(config.PublicEndpoint, true); err != nil {
		return nil, fmt.Errorf("configure OSS public endpoint: %w", err)
	}
	if !bucketPattern.MatchString(config.Bucket) {
		return nil, fmt.Errorf("configure OSS: bucket is invalid")
	}
	if config.AccessKeyID == "" || config.AccessKeySecret == "" ||
		strings.ContainsAny(config.AccessKeyID+config.AccessKeySecret, "\r\n") {
		return nil, fmt.Errorf("configure OSS: access credentials are invalid")
	}
	credentialProvider := credentials.NewStaticCredentialsProvider(
		config.AccessKeyID, config.AccessKeySecret,
	)
	apiConfig := oss.LoadDefaultConfig().
		WithRegion(config.Region).
		WithEndpoint(config.Endpoint).
		WithCredentialsProvider(credentialProvider).
		WithConnectTimeout(5 * time.Second).
		WithReadWriteTimeout(30 * time.Second)
	presignConfig := oss.LoadDefaultConfig().
		WithRegion(config.Region).
		WithEndpoint(config.PublicEndpoint).
		WithCredentialsProvider(credentialProvider).
		WithConnectTimeout(5 * time.Second).
		WithReadWriteTimeout(30 * time.Second)
	return newClient(oss.NewClient(apiConfig), oss.NewClient(presignConfig), config.Bucket), nil
}

func validateEndpoint(raw string, public bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be a plain HTTPS origin")
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.HasSuffix(host, ".aliyuncs.com") || !strings.Contains(host, "oss-") {
		return fmt.Errorf("must be an Alibaba Cloud OSS endpoint")
	}
	if public && strings.Contains(host, "-internal.") {
		return fmt.Errorf("public endpoint must be Internet-reachable")
	}
	return nil
}

func newClient(api API, presigner Presigner, bucket string) *Client {
	return &Client{api: api, presigner: presigner, bucket: bucket}
}

func (client *Client) PrepareUpload(ctx context.Context, key string, size int64, checksum string, ttl time.Duration) (ota.UploadTarget, error) {
	if client == nil || client.presigner == nil || !validOTAKey(key) || size <= 0 || size > 3<<20 || ttl <= 0 || ttl > 15*time.Minute || !validSHA256(checksum) {
		return ota.UploadTarget{}, fmt.Errorf("presign OTA upload: invalid input")
	}
	result, err := client.presigner.Presign(ctx, &oss.PutObjectRequest{
		Bucket: oss.Ptr(client.bucket), Key: oss.Ptr(key), ContentType: oss.Ptr("application/gzip"),
		ContentLength: oss.Ptr(size), ForbidOverwrite: oss.Ptr("true"),
	}, oss.PresignExpires(ttl))
	if err != nil {
		return ota.UploadTarget{}, fmt.Errorf("presign OTA upload: %w", err)
	}
	if result == nil || result.URL == "" {
		return ota.UploadTarget{}, fmt.Errorf("presign OTA upload: empty result")
	}
	headers := map[string]string{"Content-Type": "application/gzip", "Content-Length": fmt.Sprintf("%d", size), "x-oss-forbid-overwrite": "true"}
	for name, value := range result.SignedHeaders {
		headers[name] = value
	}
	return ota.UploadTarget{URL: result.URL, Method: "PUT", Headers: headers, ExpiresAt: time.Now().UTC().Add(ttl)}, nil
}

func (client *Client) Verify(ctx context.Context, key string, size int64, checksum string) error {
	if client == nil || !validOTAKey(key) || size <= 0 || size > 3<<20 || !validSHA256(checksum) {
		return fmt.Errorf("verify OTA object: invalid input")
	}
	payload, err := client.Download(ctx, key, size)
	if err != nil {
		return fmt.Errorf("verify OTA object: %w", err)
	}
	digest := sha256.Sum256(payload)
	if int64(len(payload)) != size || hex.EncodeToString(digest[:]) != checksum {
		return fmt.Errorf("verify OTA object: size or SHA-256 mismatch")
	}
	return nil
}

func (client *Client) DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if !validOTAKey(key) {
		return "", fmt.Errorf("OTA key is invalid")
	}
	return client.GetURL(ctx, key, ttl)
}

func validOTAKey(key string) bool {
	return strings.HasPrefix(key, "ota/") && len(key) > 4 && len(key) <= 512 && !strings.Contains(key, "..") && !strings.ContainsAny(key, "\r\n")
}

func (client *Client) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if client == nil || client.api == nil || !validOTAKey(key) {
		return nil, 0, fmt.Errorf("open OTA object: invalid input")
	}
	result, err := client.api.GetObject(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(client.bucket), Key: oss.Ptr(key)})
	if err != nil {
		return nil, 0, fmt.Errorf("open OTA object: %w", err)
	}
	if result == nil || result.Body == nil || result.ContentLength <= 0 || result.ContentLength > 3<<20 {
		if result != nil && result.Body != nil {
			_ = result.Body.Close()
		}
		return nil, 0, fmt.Errorf("open OTA object: invalid length")
	}
	return result.Body, result.ContentLength, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (client *Client) PutURL(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	if err := client.validRequest(key, ttl); err != nil || contentType == "" {
		return "", fmt.Errorf("presign OSS upload: invalid input")
	}
	result, err := client.presigner.Presign(ctx, &oss.PutObjectRequest{
		Bucket: oss.Ptr(client.bucket), Key: oss.Ptr(key), ContentType: oss.Ptr(contentType),
	}, oss.PresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign OSS upload: %w", err)
	}
	if result == nil || result.URL == "" {
		return "", fmt.Errorf("presign OSS upload: empty URL")
	}
	return result.URL, nil
}

func (client *Client) GetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := client.validRequest(key, ttl); err != nil {
		return "", fmt.Errorf("presign OSS download: invalid input")
	}
	result, err := client.presigner.Presign(ctx, &oss.GetObjectRequest{
		Bucket: oss.Ptr(client.bucket), Key: oss.Ptr(key),
	}, oss.PresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign OSS download: %w", err)
	}
	if result == nil || result.URL == "" {
		return "", fmt.Errorf("presign OSS download: empty URL")
	}
	return result.URL, nil
}

func (client *Client) Upload(ctx context.Context, key, contentType string, payload []byte) error {
	if client == nil || client.api == nil || key == "" || contentType == "" || len(payload) == 0 {
		return fmt.Errorf("upload OSS object: invalid input")
	}
	length := int64(len(payload))
	if _, err := client.api.PutObject(ctx, &oss.PutObjectRequest{
		Bucket: oss.Ptr(client.bucket), Key: oss.Ptr(key), ContentType: oss.Ptr(contentType),
		ContentLength: oss.Ptr(length), Body: bytes.NewReader(payload),
	}); err != nil {
		return fmt.Errorf("upload OSS object: %w", err)
	}
	return nil
}

func (client *Client) Download(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	if client == nil || client.api == nil || key == "" || maxBytes <= 0 {
		return nil, fmt.Errorf("download OSS object: invalid input")
	}
	result, err := client.api.GetObject(ctx, &oss.GetObjectRequest{
		Bucket: oss.Ptr(client.bucket), Key: oss.Ptr(key),
	})
	if err != nil {
		return nil, fmt.Errorf("download OSS object: %w", err)
	}
	if result == nil || result.Body == nil {
		return nil, fmt.Errorf("download OSS object: empty response")
	}
	defer result.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(result.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read OSS object: %w", err)
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("download OSS object: object exceeds %d bytes", maxBytes)
	}
	return payload, nil
}

func (client *Client) Exists(ctx context.Context, key string) (bool, error) {
	if client == nil || client.api == nil || key == "" {
		return false, fmt.Errorf("head OSS object: invalid input")
	}
	_, err := client.api.HeadObject(ctx, &oss.HeadObjectRequest{
		Bucket: oss.Ptr(client.bucket), Key: oss.Ptr(key),
	})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("head OSS object: %w", err)
}

func (client *Client) Delete(ctx context.Context, key string) error {
	if client == nil || client.api == nil || key == "" {
		return fmt.Errorf("delete OSS object: invalid input")
	}
	if _, err := client.api.DeleteObject(ctx, &oss.DeleteObjectRequest{
		Bucket: oss.Ptr(client.bucket), Key: oss.Ptr(key),
	}); err != nil {
		return fmt.Errorf("delete OSS object: %w", err)
	}
	return nil
}

func (client *Client) validRequest(key string, ttl time.Duration) error {
	if client == nil || client.presigner == nil || client.bucket == "" || key == "" || ttl <= 0 {
		return fmt.Errorf("invalid request")
	}
	return nil
}

func isNotFound(err error) bool {
	var serviceError *oss.ServiceError
	return errors.As(err, &serviceError) && serviceError.StatusCode == 404
}
