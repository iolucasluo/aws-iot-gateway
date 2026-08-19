package awsiot

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestDirectACKTopicUsesExactNamespace(t *testing.T) {
	t.Parallel()
	if got := directACKTopic("aws-iot-gateway-staging"); got != "cmd/aws-iot-gateway-staging/+/res" {
		t.Fatalf("directACKTopic() = %q", got)
	}
}

func TestDirectACKTopicPrefixMustBeOneMQTTSegment(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", "nested/prefix", "prefix/+", "prefix/#", " leading", "trailing "} {
		if validDirectACKTopicPrefix(prefix) {
			t.Fatalf("validDirectACKTopicPrefix(%q) = true", prefix)
		}
	}
	for _, prefix := range []string{"aws-iot-gateway-staging", "gateway.dev_1"} {
		if !validDirectACKTopicPrefix(prefix) {
			t.Fatalf("validDirectACKTopicPrefix(%q) = false", prefix)
		}
	}
}

func TestPresignIoTWebSocketURL(t *testing.T) {
	t.Parallel()
	signed, err := presignIoTWebSocketURL(
		context.Background(),
		"example-ats.iot.ap-east-1.amazonaws.com",
		"ap-east-1",
		aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret-example"},
		time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("presignIoTWebSocketURL() error = %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Scheme != "wss" || parsed.Host != "example-ats.iot.ap-east-1.amazonaws.com" || parsed.Path != "/mqtt" {
		t.Fatalf("signed URL target = %s", parsed.Redacted())
	}
	query := parsed.Query()
	for _, key := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature"} {
		if strings.TrimSpace(query.Get(key)) == "" {
			t.Fatalf("signed URL missing %s", key)
		}
	}
	if query.Get("X-Amz-Expires") != "900" {
		t.Fatalf("X-Amz-Expires = %q", query.Get("X-Amz-Expires"))
	}
}
