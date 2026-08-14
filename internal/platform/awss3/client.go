package awss3

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Client struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

func NewClient(config aws.Config, bucket string) (*Client, error) {
	if bucket == "" {
		return nil, fmt.Errorf("AWS S3 media bucket is required")
	}
	client := s3.NewFromConfig(config)
	return &Client{client: client, presign: s3.NewPresignClient(client), bucket: bucket}, nil
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

func (client *Client) Exists(ctx context.Context, key string) (bool, error) {
	if _, err := client.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(client.bucket), Key: aws.String(key),
	}); err != nil {
		return false, nil
	}
	return true, nil
}
