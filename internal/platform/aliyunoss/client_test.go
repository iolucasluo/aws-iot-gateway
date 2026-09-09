package aliyunoss

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

type fakeAPI struct {
	putRequest    *oss.PutObjectRequest
	getRequest    *oss.GetObjectRequest
	headRequest   *oss.HeadObjectRequest
	deleteRequest *oss.DeleteObjectRequest
	presignInput  any
	presignResult *oss.PresignResult
	getResult     *oss.GetObjectResult
	headResult    *oss.HeadObjectResult
	err           error
}

func (fake *fakeAPI) PutObject(_ context.Context, request *oss.PutObjectRequest, _ ...func(*oss.Options)) (*oss.PutObjectResult, error) {
	fake.putRequest = request
	return &oss.PutObjectResult{}, fake.err
}

func (fake *fakeAPI) GetObject(_ context.Context, request *oss.GetObjectRequest, _ ...func(*oss.Options)) (*oss.GetObjectResult, error) {
	fake.getRequest = request
	if fake.getResult == nil {
		fake.getResult = &oss.GetObjectResult{Body: io.NopCloser(strings.NewReader("payload"))}
	}
	return fake.getResult, fake.err
}

func (fake *fakeAPI) HeadObject(_ context.Context, request *oss.HeadObjectRequest, _ ...func(*oss.Options)) (*oss.HeadObjectResult, error) {
	fake.headRequest = request
	if fake.headResult == nil {
		fake.headResult = &oss.HeadObjectResult{ContentLength: 7, ContentType: oss.Ptr("text/plain")}
	}
	return fake.headResult, fake.err
}

func (fake *fakeAPI) DeleteObject(_ context.Context, request *oss.DeleteObjectRequest, _ ...func(*oss.Options)) (*oss.DeleteObjectResult, error) {
	fake.deleteRequest = request
	return &oss.DeleteObjectResult{}, fake.err
}

func (fake *fakeAPI) Presign(_ context.Context, request any, _ ...func(*oss.PresignOptions)) (*oss.PresignResult, error) {
	fake.presignInput = request
	if fake.presignResult == nil {
		fake.presignResult = &oss.PresignResult{URL: "https://signed.example/object"}
	}
	return fake.presignResult, fake.err
}

func TestClientImplementsOTAArtifactStore(t *testing.T) {
	t.Parallel()
	var _ ota.ArtifactStore = (*Client)(nil)
}

func TestClientImplementsMediaObjectStore(t *testing.T) {
	t.Parallel()
	var _ media.ObjectStore = (*Client)(nil)
}

func TestClientUsesPublicPresignerForDeviceUpload(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{}
	presigner := &fakeAPI{}
	client := newClient(api, presigner, "livhub")

	url, err := client.PutURL(context.Background(), "development/snapshots/device/id.h264", "video/h264", 15*time.Minute)
	if err != nil {
		t.Fatalf("PutURL() error = %v", err)
	}
	if url != "https://signed.example/object" {
		t.Fatalf("PutURL() = %q", url)
	}
	if api.presignInput != nil {
		t.Fatal("PutURL() used the server API client instead of the public presigner")
	}
	request, ok := presigner.presignInput.(*oss.PutObjectRequest)
	if !ok || oss.ToString(request.Bucket) != "livhub" || oss.ToString(request.Key) != "development/snapshots/device/id.h264" || oss.ToString(request.ContentType) != "video/h264" {
		t.Fatalf("presign request = %#v", presigner.presignInput)
	}
}

func TestClientBoundsDownloads(t *testing.T) {
	t.Parallel()
	fake := &fakeAPI{getResult: &oss.GetObjectResult{Body: io.NopCloser(strings.NewReader("12345678"))}}
	client := newClient(fake, fake, "livhub")

	_, err := client.Download(context.Background(), "key", 7)
	if err == nil || !strings.Contains(err.Error(), "exceeds 7 bytes") {
		t.Fatalf("Download() error = %v, want size failure", err)
	}
}

func TestClientTreatsOnlyNotFoundAsMissing(t *testing.T) {
	t.Parallel()
	notFound := &fakeAPI{err: &oss.ServiceError{StatusCode: 404, Code: "NoSuchKey"}}
	exists, err := newClient(notFound, notFound, "livhub").Exists(context.Background(), "missing")
	if err != nil || exists {
		t.Fatalf("Exists() = %v, %v; want false, nil", exists, err)
	}

	denied := &fakeAPI{err: errors.New("access denied")}
	if _, err := newClient(denied, denied, "livhub").Exists(context.Background(), "hidden"); err == nil {
		t.Fatal("Exists() accepted a non-404 error")
	}
}

func TestNewRejectsInternalEndpointAsPublicPresignEndpoint(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		Region: "cn-shenzhen", Endpoint: "https://oss-cn-shenzhen-internal.aliyuncs.com",
		PublicEndpoint: "https://oss-cn-shenzhen-internal.aliyuncs.com", Bucket: "livhub",
		AccessKeyID: "test-key", AccessKeySecret: "test-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "public endpoint") {
		t.Fatalf("New() error = %v, want public endpoint failure", err)
	}
}
