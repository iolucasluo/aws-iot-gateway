package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/timebootstrap"
)

type timeBootstrapStub struct {
	request timebootstrap.Request
	err     error
}

func (stub *timeBootstrapStub) Bootstrap(_ context.Context, request timebootstrap.Request) (timebootstrap.Response, error) {
	stub.request = request
	return timebootstrap.Response{COSESign1: []byte{0xd2, 0x84, 0x43}}, stub.err
}

type bootstrapLimiterStub struct {
	allowed bool
	err     error
	keys    []string
}

func (stub *bootstrapLimiterStub) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, error) {
	stub.keys = append(stub.keys, key)
	return stub.allowed, stub.err
}

func TestTimeBootstrapPublicRouteUsesExactWireContractAndSharedLimits(t *testing.T) {
	service := &timeBootstrapStub{}
	limiter := &bootstrapLimiterStub{allowed: true}
	handler := NewInternalAPI(nil, nil, Dependencies{TimeBootstrap: service, TimeBootstrapLimiter: limiter})
	nonce := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, timebootstrap.NonceSize))
	body := fmt.Sprintf(`{"version":1,"chip_uid":"b5eb3f00adc1","nonce":%q}`, nonce)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/device-time/bootstrap", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.7")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if !bytes.Equal(service.request.ChipUID, []byte{0xb5, 0xeb, 0x3f, 0x00, 0xad, 0xc1}) || len(service.request.Nonce) != timebootstrap.NonceSize {
		t.Fatalf("decoded request=%+v", service.request)
	}
	if len(limiter.keys) != 2 || limiter.keys[0] != "time-bootstrap:ip:203.0.113.7" || limiter.keys[1] != "time-bootstrap:global" {
		t.Fatalf("rate-limit keys=%v", limiter.keys)
	}
	if got := response.Body.String(); got != "{\"time_cose_sign1\":\"0oRD\"}\n" {
		t.Fatalf("body=%q", got)
	}
}

func TestTimeBootstrapRejectsNonExactInputBeforeSigning(t *testing.T) {
	for name, testCase := range map[string][2]string{
		"content-type":  {"application/json; charset=utf-8", `{"version":1}`},
		"unknown-field": {"application/json", `{"version":1,"chip_uid":"b5eb3f00adc1","nonce":"QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=","project_id":"removed"}`},
	} {
		t.Run(name, func(t *testing.T) {
			service := &timeBootstrapStub{}
			handler := NewInternalAPI(nil, nil, Dependencies{TimeBootstrap: service, TimeBootstrapLimiter: &bootstrapLimiterStub{allowed: true}})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/device-time/bootstrap", bytes.NewBufferString(testCase[1]))
			request.Header.Set("Content-Type", testCase[0])
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code < 400 || service.request.Version != 0 {
				t.Fatalf("status=%d signed=%+v body=%s", response.Code, service.request, response.Body.String())
			}
		})
	}
}

func TestTimeBootstrapFailsClosedWhenRateLimiterUnavailableOrExhausted(t *testing.T) {
	for name, limiter := range map[string]*bootstrapLimiterStub{
		"unavailable": {allowed: false, err: fmt.Errorf("redis unavailable")},
		"exhausted":   {allowed: false},
	} {
		t.Run(name, func(t *testing.T) {
			service := &timeBootstrapStub{}
			handler := NewInternalAPI(nil, nil, Dependencies{TimeBootstrap: service, TimeBootstrapLimiter: limiter})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/device-time/bootstrap", bytes.NewBufferString(`{"version":1}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			want := http.StatusTooManyRequests
			if limiter.err != nil {
				want = http.StatusServiceUnavailable
			}
			if response.Code != want || service.request.Version != 0 {
				t.Fatalf("status=%d want=%d signed=%+v", response.Code, want, service.request)
			}
		})
	}
}
