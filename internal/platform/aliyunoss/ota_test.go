package aliyunoss

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

func TestOTAUploadRetainsSignedHeadersAndForbidsOverwrite(t *testing.T) {
	// 真实 SDK 的 SignedHeaders 使用其规范大小写；这里必须与线上观测一致，否则发现不了重复键。
	fake := &fakeAPI{presignResult: &oss.PresignResult{URL: "https://signed.example/ota", SignedHeaders: map[string]string{"X-Oss-Forbid-Overwrite": "true", "x-oss-test-header": "signed"}}}
	client := newClient(fake, fake, "test-bucket")
	target, err := client.PrepareUpload(context.Background(), "ota/release.tar.gz", 7, strings.Repeat("a", 64), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req := fake.presignInput.(*oss.PutObjectRequest)
	if oss.ToString(req.ForbidOverwrite) != "true" {
		t.Fatal("presign did not forbid overwrite")
	}
	// 客户端会把这些头原样交给 fetch；大小写不同的同名键会被合并成 "true, true"，
	// 使 OSS V4 签名不匹配（403 SignatureDoesNotMatch）。
	seen := map[string]string{}
	for name, value := range target.Headers {
		lowered := strings.ToLower(name)
		if previous, ok := seen[lowered]; ok {
			t.Fatalf("duplicate case-insensitive header %q: %q vs %q", lowered, previous, value)
		}
		seen[lowered] = value
	}
	for header, want := range map[string]string{"content-type": "application/gzip", "content-length": "7", "x-oss-forbid-overwrite": "true", "x-oss-test-header": "signed"} {
		if seen[header] != want {
			t.Fatalf("upload header %q = %q, want %q", header, seen[header], want)
		}
	}
	if target.Method != "PUT" || target.Headers["Content-Length"] != "7" {
		t.Fatal("upload metadata mismatch")
	}
	for _, key := range []string{"media/image.jpg", "ota/../media/image.jpg", "ota/line\n"} {
		if _, err := client.PrepareUpload(context.Background(), key, 7, strings.Repeat("a", 64), time.Minute); err == nil {
			t.Fatal("accepted non-OTA key", key)
		}
	}
}

func TestOTAVerifyChecksActualBytesAndLength(t *testing.T) {
	sum := sha256.Sum256([]byte("payload"))
	hash := hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		body string
		size int64
		hash string
		good bool
	}{
		{"payload", 7, hash, true}, {"changed", 7, hash, false}, {"payload!", 7, hash, false}, {"short", 7, hash, false}, {"payload", 7, strings.Repeat("b", 64), false},
	} {
		fake := &fakeAPI{getResult: &oss.GetObjectResult{Body: io.NopCloser(strings.NewReader(tc.body))}}
		err := newClient(fake, fake, "bucket").Verify(context.Background(), "ota/release.tar.gz", tc.size, tc.hash)
		if (err == nil) != tc.good {
			t.Fatalf("body=%q good=%t err=%v", tc.body, tc.good, err)
		}
	}
}

func TestOTAReaderStreamsBoundedObject(t *testing.T) {
	var _ ota.ArtifactReader = (*Client)(nil)
	fake := &fakeAPI{getResult: &oss.GetObjectResult{Body: io.NopCloser(strings.NewReader("payload")), ContentLength: 7}}
	client := newClient(fake, fake, "bucket")
	body, size, err := client.Open(context.Background(), "ota/release.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	b, _ := io.ReadAll(body)
	if size != 7 || string(b) != "payload" {
		t.Fatal("bad object read")
	}
	fake.getResult = &oss.GetObjectResult{Body: io.NopCloser(strings.NewReader("bad")), ContentLength: 4 << 20}
	if _, _, err = client.Open(context.Background(), "ota/too-large.tar.gz"); err == nil {
		t.Fatal("accepted oversized object")
	}
}
