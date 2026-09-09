package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

type httpDownloadGrantStore struct{ grant ota.DownloadGrant }

func (store *httpDownloadGrantStore) Store(context.Context, string, ota.DownloadGrant, int, time.Duration) error {
	return nil
}
func (store *httpDownloadGrantStore) Redeem(_ context.Context, token, deviceSN string) (ota.DownloadGrant, error) {
	if token != "0123456789abcdef0123456789abcdef" || deviceSN != store.grant.DeviceSN {
		return ota.DownloadGrant{}, ota.ErrDownloadNotFound
	}
	return store.grant, nil
}

type httpArtifactReader struct{}

func (httpArtifactReader) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return io.NopCloser(strings.NewReader("signed-ota")), 10, nil
}

func TestDeviceOTADownloadEndpointStreamsNoStoreArtifactAndRejectsWrongDevice(t *testing.T) {
	store := &httpDownloadGrantStore{grant: ota.DownloadGrant{DeviceSN: "fdr-device-1", DeploymentID: "22222222-2222-4222-8222-222222222222", ArtifactKey: "ota/private.tar.gz", SHA256: strings.Repeat("a", 64), FileSize: 10}}
	proxy, err := ota.NewDownloadProxy("https://gateway.example", store, httpArtifactReader{}, func() (string, error) { return "0123456789abcdef0123456789abcdef", nil })
	if err != nil {
		t.Fatal(err)
	}
	service := ota.NewService(nil, nil, nil, "staging", nil, nil, nil, ota.WithDownloadProxy(proxy))
	handler := NewInternalAPI(nil, nil, Dependencies{OTA: service})
	valid := httptest.NewRequest(http.MethodGet, "/api/v1/device-ota/download/fdr-device-1/0123456789abcdef0123456789abcdef", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, valid)
	if response.Code != http.StatusOK || response.Body.String() != "signed-ota" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Length") != "10" {
		t.Fatalf("response=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	wrong := httptest.NewRequest(http.MethodGet, "/api/v1/device-ota/download/fdr-wrong/0123456789abcdef0123456789abcdef", nil)
	wrongResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongResponse, wrong)
	if wrongResponse.Code != http.StatusNotFound || strings.Contains(wrongResponse.Body.String(), "0123456789abcdef0123456789abcdef") {
		t.Fatalf("wrong-device response=%d body=%q", wrongResponse.Code, wrongResponse.Body.String())
	}
}
