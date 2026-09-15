package platformapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

// stubGateway 是 gateway 内部 API 的替身，记录平台进程转发过来的每一次调用。
type stubGateway struct {
	mutex     sync.Mutex
	server    *httptest.Server
	requests  []recordedGatewayRequest
	responder func(path string) (int, string)
}

type recordedGatewayRequest struct {
	Path        string
	RawQuery    string
	Method      string
	APIKey      string
	Signature   string
	Nonce       string
	DeviceToken string
	Body        []byte
}

func newStubGateway(t *testing.T) *stubGateway {
	t.Helper()
	stub := &stubGateway{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		stub.mutex.Lock()
		stub.requests = append(stub.requests, recordedGatewayRequest{
			Path: request.URL.Path, RawQuery: request.URL.RawQuery, Method: request.Method,
			APIKey: request.Header.Get("X-Api-Key"), Signature: request.Header.Get("X-Signature"),
			Nonce: request.Header.Get("X-Nonce"), DeviceToken: request.Header.Get("X-Device-Token"),
			Body: body,
		})
		responder := stub.responder
		stub.mutex.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if responder != nil {
			status, payload := responder(request.URL.Path)
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(payload))
			return
		}
		if strings.HasSuffix(request.URL.Path, "/token") {
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"device_token":"device-token-from-gateway"}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (stub *stubGateway) recorded() []recordedGatewayRequest {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return append([]recordedGatewayRequest{}, stub.requests...)
}

func developerActor() project.Actor {
	return project.Actor{AccountID: developerOneID, Kind: portalaccount.KindDeveloper, Role: portalaccount.RoleDeveloper}
}

type gatewayProxyFixture struct {
	developerFixture
	stub  *stubGateway
	token string
}

// newGatewayProxyFixture 在 developer fixture 之上接一个替身 gateway，
// 这样代理契约不依赖真实 gateway 进程。
func newGatewayProxyFixture(t *testing.T, respond func(path string) (int, string)) gatewayProxyFixture {
	t.Helper()
	fixture := newDeveloperFixture(t)
	stub := newStubGateway(t)
	if respond != nil {
		stub.responder = respond
	}
	client, err := NewGatewayClient(stub.server.URL, fixture.projects, stub.server.Client(), func() time.Time {
		return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewGatewayClient() error = %v", err)
	}
	fixture.server.dependencies.Gateway = client
	return gatewayProxyFixture{developerFixture: fixture, stub: stub, token: fixture.login(t, developerOneEmail)}
}

// createProjectWithDevice 建项目、按给定权限签发凭证，并登记一台绑定到该项目的设备。
func (fixture gatewayProxyFixture) createProjectWithDevice(t *testing.T, permissions []string) string {
	t.Helper()
	created := fixture.call(t, http.MethodPost, "/api/v1/developer/projects",
		`{"project_type":"cloud","name":"Proxy Project"}`, fixture.token)
	if created.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", created.Code, created.Body.String())
	}
	projectID := decodeJSON[struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}](t, created).Project.ID
	if projectID == "" {
		t.Fatal("create project returned an empty id")
	}
	if _, err := fixture.projects.CreateCredential(context.Background(), developerActor(), projectID,
		project.CreateCredentialInput{Name: "platform", Permissions: permissions}); err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	fixture.ops.devices = []operations.Device{{
		ID: "018f0000-0000-7000-8000-000000000201", DeviceSN: opsFixtureDeviceSN, PID: "feeder-x1",
		Status: "active", Online: true, CreatedAt: time.Now(),
		BindingProjectID: projectID, BindingExternalUserID: "user-1",
	}}
	return projectID
}

func (fixture gatewayProxyFixture) attachProduct(t *testing.T, projectID, productID, pid string) {
	t.Helper()
	fixture.repository.AddProduct(productID, pid)
	if _, err := fixture.projects.AttachProduct(context.Background(), developerActor(), projectID, productID); err != nil {
		t.Fatalf("AttachProduct() error = %v", err)
	}
}

// 设备命令经 gateway 代理：平台层不自己实现设备授权，只按项目身份签名转发，
// 并附上 gateway 为该项目名下设备签发的 device token。
func TestDeveloperDeviceCommandProxiesThroughGateway(t *testing.T) {
	t.Parallel()
	fixture := newGatewayProxyFixture(t, nil)
	fixture.createProjectWithDevice(t, []string{"devices:read", "devices:operate"})

	recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/devices/"+opsFixtureDeviceSN+"/commands",
		`{"commands":[{"code":"led","value":{"on":true}}],"wait_ack":false}`, fixture.token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("command status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	requests := fixture.stub.recorded()
	if len(requests) != 2 {
		t.Fatalf("gateway saw %d requests, want 2 (device token + command)", len(requests))
	}
	tokenRequest, commandRequest := requests[0], requests[1]
	if tokenRequest.Path != "/api/v1/openapi/devices/token" || tokenRequest.Method != http.MethodPost {
		t.Fatalf("first gateway call = %+v, want the device token endpoint", tokenRequest)
	}
	if string(tokenRequest.Body) != `{"device_sn":"`+opsFixtureDeviceSN+`"}` {
		t.Fatalf("device token body = %s", tokenRequest.Body)
	}
	if commandRequest.Path != "/api/v1/openapi/devices/"+opsFixtureDeviceSN+"/commands" ||
		commandRequest.Method != http.MethodPost {
		t.Fatalf("command request = %+v", commandRequest)
	}
	if commandRequest.DeviceToken != "device-token-from-gateway" {
		t.Fatalf("command request device token = %q", commandRequest.DeviceToken)
	}
	if commandRequest.APIKey == "" || commandRequest.Signature == "" || commandRequest.Nonce == "" {
		t.Fatalf("command request was not signed: %+v", commandRequest)
	}
	// 平台层不重写设备域请求体：命令体按 gateway openapi 形状原样转发。
	if string(commandRequest.Body) != `{"commands":[{"code":"led","value":{"on":true}}],"wait_ack":false}` {
		t.Fatalf("command body = %s", commandRequest.Body)
	}
}

// OTA 固件目录在 gateway 侧是全局 pid 索引；平台层必须收窄到项目白名单。
func TestDeveloperOTARequiresPIDInProjectScope(t *testing.T) {
	t.Parallel()
	fixture := newGatewayProxyFixture(t, nil)
	projectID := fixture.createProjectWithDevice(t, []string{"ota:publish", "ota:deploy"})

	// 未挂到项目上的 pid：拒绝，且不产生任何 gateway 调用。
	outOfScope := fixture.call(t, http.MethodGet, "/api/v1/developer/ota/firmware-versions?pid=lamp-x1", "", fixture.token)
	if outOfScope.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope pid status = %d body = %s, want 403", outOfScope.Code, outOfScope.Body.String())
	}
	if requests := fixture.stub.recorded(); len(requests) != 0 {
		t.Fatalf("out-of-scope pid must not reach gateway: %+v", requests)
	}
	if missing := fixture.call(t, http.MethodGet, "/api/v1/developer/ota/firmware-versions", "", fixture.token); missing.Code != http.StatusBadRequest {
		t.Fatalf("missing pid status = %d, want 400", missing.Code)
	}

	// 挂到项目白名单后放行，pid 原样透传。
	fixture.attachProduct(t, projectID, "30000000-0000-0000-0000-0000000000f1", "lamp-x1")
	allowed := fixture.call(t, http.MethodGet, "/api/v1/developer/ota/firmware-versions?pid=lamp-x1", "", fixture.token)
	if allowed.Code != http.StatusOK {
		t.Fatalf("in-scope pid status = %d body = %s", allowed.Code, allowed.Body.String())
	}
	requests := fixture.stub.recorded()
	if len(requests) != 1 || requests[0].RawQuery != "pid=lamp-x1" {
		t.Fatalf("gateway requests = %+v", requests)
	}
}

// OTA 发布体（含 release_statement 与签名）必须整包转发：平台层只借 pid 做项目收窄，
// 不能因为出现未知字段就 400 —— 否则两段式发布第一步就死。
func TestDeveloperOTAPublishForwardsTheFullBody(t *testing.T) {
	t.Parallel()
	fixture := newGatewayProxyFixture(t, nil)
	projectID := fixture.createProjectWithDevice(t, []string{"ota:publish", "ota:deploy"})
	fixture.attachProduct(t, projectID, "30000000-0000-0000-0000-0000000000f2", "lamp-x1")

	body := `{"idempotency_key":"ci-42","pid":"lamp-x1","version":"1.4.0","firmware_build":140,` +
		` "sha256":"` + strings.Repeat("a", 64) + `","manifest_sha256":"` + strings.Repeat("b", 64) + `",` +
		` "file_size":1048576,"release_statement":"schema=explorex-ota-v2|pid=lamp-x1","signature":"c2lnbmF0dXJl",` +
		` "upstream_build_id":"ci-42"}`
	recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/ota/firmware-versions", body, fixture.token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("publish status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	requests := fixture.stub.recorded()
	if len(requests) != 1 {
		t.Fatalf("gateway saw %d requests, want 1", len(requests))
	}
	if requests[0].Path != "/api/v1/openapi/ota/firmware-versions" || requests[0].Method != http.MethodPost {
		t.Fatalf("publish request = %+v", requests[0])
	}
	if string(requests[0].Body) != body {
		t.Fatalf("publish body was rewritten: got %s want %s", requests[0].Body, body)
	}
	if requests[0].APIKey == "" || requests[0].Signature == "" {
		t.Fatalf("publish request was not signed with the project credential: %+v", requests[0])
	}
}

// 完成上传同样整包转发；pid 决定用哪个项目的凭证。
func TestDeveloperOTACompleteForwardsPidBody(t *testing.T) {
	t.Parallel()
	fixture := newGatewayProxyFixture(t, nil)
	projectID := fixture.createProjectWithDevice(t, []string{"ota:publish"})
	fixture.attachProduct(t, projectID, "30000000-0000-0000-0000-0000000000f3", "lamp-x1")

	body := `{"pid":"lamp-x1","artifact_key":"ota/lamp-x1/1.4.0.bin"}`
	recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/ota/firmware-versions/018f0000-0000-7000-8000-0000000000aa/complete", body, fixture.token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("complete status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	requests := fixture.stub.recorded()
	if len(requests) != 1 || requests[0].Path != "/api/v1/openapi/ota/firmware-versions/018f0000-0000-7000-8000-0000000000aa/complete" {
		t.Fatalf("complete requests = %+v", requests)
	}
	if string(requests[0].Body) != body {
		t.Fatalf("complete body was rewritten: %s", requests[0].Body)
	}
}

// 项目缺少所需权限的凭证时给出明确冲突，而不是静默降级或换一把别的钥匙。
func TestDeveloperDeviceProxyRequiresPermissionedCredential(t *testing.T) {
	t.Parallel()
	fixture := newGatewayProxyFixture(t, nil)
	fixture.createProjectWithDevice(t, []string{"devices:read"})

	recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/devices/"+opsFixtureDeviceSN+"/commands",
		`{"code":"led","value":{"on":true}}`, fixture.token)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("command without devices:operate status = %d body = %s, want 409", recorder.Code, recorder.Body.String())
	}
	for _, request := range fixture.stub.recorded() {
		if strings.Contains(request.Path, "/commands") {
			t.Fatalf("command must not reach gateway without devices:operate: %+v", request)
		}
	}
}

// gateway 的状态码与响应体原样透传，避免同一件事出现两套错误语义。
func TestDeveloperDeviceProxyPassesThroughGatewayErrors(t *testing.T) {
	t.Parallel()
	fixture := newGatewayProxyFixture(t, func(path string) (int, string) {
		if strings.HasSuffix(path, "/token") {
			return http.StatusOK, `{"device_token":"device-token-from-gateway"}`
		}
		return http.StatusBadGateway, `{"error_code":"invalid_device_response","message":"device said no"}`
	})
	fixture.createProjectWithDevice(t, []string{"devices:read", "devices:operate"})

	recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/devices/"+opsFixtureDeviceSN+"/commands",
		`{"code":"led","value":{"on":true}}`, fixture.token)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("gateway error status = %d body = %s, want 502 passthrough", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "invalid_device_response") {
		t.Fatalf("gateway error body was rewritten: %s", recorder.Body.String())
	}
}

// 未配置 gateway 客户端时，代理路由必须显式不可用，而不是假装成功。
func TestDeveloperDeviceProxyWithoutGatewayClientIsUnavailable(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	token := fixture.login(t, developerOneEmail)

	recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/devices/"+opsFixtureDeviceSN+"/commands",
		`{"code":"led"}`, token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status without gateway client = %d body = %s, want 503", recorder.Code, recorder.Body.String())
	}
}
