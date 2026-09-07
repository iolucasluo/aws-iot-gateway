package aliyunoss

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestIntegrationObjectLifecycle(t *testing.T) {
	region := os.Getenv("TEST_OSS_REGION")
	endpoint := os.Getenv("TEST_OSS_ENDPOINT")
	publicEndpoint := os.Getenv("TEST_OSS_PUBLIC_ENDPOINT")
	bucket := os.Getenv("TEST_OSS_BUCKET")
	accessKeyID := os.Getenv("TEST_OSS_ACCESS_KEY_ID")
	accessKeySecret := os.Getenv("TEST_OSS_ACCESS_KEY_SECRET")
	if region == "" || endpoint == "" || publicEndpoint == "" || bucket == "" ||
		accessKeyID == "" || accessKeySecret == "" {
		t.Skip("real OSS integration environment is not configured")
	}

	client, err := New(Config{
		Region: region, Endpoint: endpoint, PublicEndpoint: publicEndpoint, Bucket: bucket,
		AccessKeyID: accessKeyID, AccessKeySecret: accessKeySecret,
	})
	if err != nil {
		t.Fatalf("configure integration client: %v", err)
	}
	key := fmt.Sprintf("codex-smoke/%d.txt", time.Now().UnixNano())
	payload := []byte("cn-cloud-staging-oss-smoke")
	defer func() {
		if deleteErr := client.Delete(context.Background(), key); deleteErr != nil {
			t.Errorf("cleanup integration object: %v", deleteErr)
		}
	}()

	putURL, err := client.PutURL(context.Background(), key, "text/plain", 5*time.Minute)
	if err != nil {
		t.Fatalf("presign integration upload: %v", err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPut, putURL, bytes.NewReader(payload))
	if err != nil {
		t.Fatal("build integration upload request")
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("execute integration upload request")
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("integration upload status = %d", response.StatusCode)
	}

	exists, err := client.Exists(context.Background(), key)
	if err != nil || !exists {
		t.Fatalf("Exists() = %v, %v", exists, err)
	}
	downloaded, err := client.Download(context.Background(), key, 1024)
	if err != nil || !bytes.Equal(downloaded, payload) {
		t.Fatalf("Download() bytes = %d, error = %v", len(downloaded), err)
	}
	getURL, err := client.GetURL(context.Background(), key, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign integration download: %v", err)
	}
	getResponse, err := http.Get(getURL)
	if err != nil {
		t.Fatal("execute integration download request")
	}
	got, readErr := io.ReadAll(io.LimitReader(getResponse.Body, 1025))
	getResponse.Body.Close()
	if readErr != nil || getResponse.StatusCode != http.StatusOK || !bytes.Equal(got, payload) {
		t.Fatalf("integration download status = %d, bytes = %d, error = %v", getResponse.StatusCode, len(got), readErr)
	}
}
