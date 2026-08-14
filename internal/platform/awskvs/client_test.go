package awskvs

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestSignWSSClampsFiveMinuteTTLToKVSMaximum(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC)
	client := &Client{
		config: aws.Config{
			Region: "ap-east-1",
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret", SessionToken: "session",
				}, nil
			}),
		},
		region: "ap-east-1",
		now:    func() time.Time { return now },
	}

	signedURL, expiresAt, err := client.signWSS(
		context.Background(),
		"wss://v-example.kinesisvideo.ap-east-1.amazonaws.com",
		"arn:aws:kinesisvideo:ap-east-1:123456789012:channel/example/1",
		"viewer-1",
		5*time.Minute,
	)
	if err != nil {
		t.Fatalf("signWSS() error = %v", err)
	}
	parsed, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if got := parsed.Query().Get("X-Amz-Expires"); got != "299" {
		t.Fatalf("X-Amz-Expires = %q, want 299", got)
	}
	if want := now.Add(299 * time.Second); !expiresAt.Equal(want) {
		t.Fatalf("expiresAt = %s, want %s", expiresAt, want)
	}
}
