package platformapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

const maxGatewayResponseBytes = 1 << 20

// GatewayClient 以"项目自身身份"调用 gateway 内部 API。
//
// 为什么不让平台进程直接连 AWS IoT / S3：
//   - 设备域授权只应该有一份实现（gateway 的 binding 归属 + X-Device-Token）；
//   - 平台进程因此不需要持有 IoT/KVS/S3 凭据，公开面与设备信任面的爆炸半径保持分离。
//
// 平台进程**不会**凭空获得比项目更大的授权：它必须用项目自己持有的、
// 具备所需权限的 active 凭证去签名。
type GatewayClient struct {
	baseURL  string
	client   *http.Client
	projects *project.Service
	now      func() time.Time
}

func NewGatewayClient(baseURL string, projects *project.Service, client *http.Client, now func() time.Time) (*GatewayClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("gateway client requires a base url")
	}
	if projects == nil {
		return nil, fmt.Errorf("gateway client requires a project service")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("gateway client base url must be absolute")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	return &GatewayClient{baseURL: baseURL, client: client, projects: projects, now: now}, nil
}

// Response 是 gateway 的原始响应；平台层只做透传，不解释设备域语义。
type Response struct {
	StatusCode int
	Body       []byte
}

// Call 签名并转发一次调用。deviceToken 非空时附加 X-Device-Token
// （设备 I/O 需要它，且 gateway 会再校验 binding 归属）。
func (client *GatewayClient) Call(
	ctx context.Context, projectID, permission, method, path string,
	query url.Values, body []byte, deviceToken string,
) (Response, error) {
	apiKey, secret, err := client.projects.ProjectAPISecret(ctx, projectID, permission)
	if err != nil {
		return Response{}, err
	}
	nonce, err := randomNonce()
	if err != nil {
		return Response{}, fmt.Errorf("generate request nonce: %w", err)
	}
	target := client.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build gateway request: %w", err)
	}
	timestamp := fmt.Sprintf("0000001%013d", client.now().UnixMilli())
	signature, err := internalauth.Sign(secret, internalauth.SignatureRequest{
		APIKey: apiKey, Timestamp: timestamp, Nonce: nonce, Method: method,
		Path: path, RawQuery: query.Encode(), Body: body,
	})
	if err != nil {
		return Response{}, fmt.Errorf("sign gateway request: %w", err)
	}
	request.Header.Set("X-Api-Key", apiKey)
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Nonce", nonce)
	request.Header.Set("X-Signature", signature)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if deviceToken != "" {
		request.Header.Set("X-Device-Token", deviceToken)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("call gateway: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxGatewayResponseBytes))
	if err != nil {
		return Response{}, fmt.Errorf("read gateway response: %w", err)
	}
	return Response{StatusCode: response.StatusCode, Body: payload}, nil
}

// DeviceToken 为项目自己名下的设备签发 device token。
// 它走的是既有 POST /api/v1/openapi/devices/token：只接受 device_sn，
// 并且 gateway 只允许主体为自己的 active binding 签发——不存在"冒充终端用户"。
func (client *GatewayClient) DeviceToken(ctx context.Context, projectID, deviceSN string) (string, error) {
	body, err := json.Marshal(map[string]string{"device_sn": deviceSN})
	if err != nil {
		return "", fmt.Errorf("encode device token request: %w", err)
	}
	response, err := client.Call(ctx, projectID, "devices:read", http.MethodPost,
		"/api/v1/openapi/devices/token", nil, body, "")
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: gateway refused device token with %d", errGatewayRejected, response.StatusCode)
	}
	var payload struct {
		DeviceToken string `json:"device_token"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		return "", fmt.Errorf("decode device token response: %w", err)
	}
	if payload.DeviceToken == "" {
		return "", fmt.Errorf("gateway returned an empty device token")
	}
	return payload.DeviceToken, nil
}

func randomNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// writeGatewayResponse 透传 gateway 的状态码与响应体。
// 平台层不重写设备域错误语义——否则同一件事会有两套错误码。
func writeGatewayResponse(writer http.ResponseWriter, response Response, err error) {
	if err != nil {
		if errors.Is(err, project.ErrCredentialNotFound) {
			writeError(writer, http.StatusConflict, "credential_required",
				"this project needs an active credential with the required permission")
			return
		}
		if errors.Is(err, errGatewayRejected) {
			writeError(writer, http.StatusBadGateway, "gateway_rejected", "gateway refused the request")
			return
		}
		writeError(writer, http.StatusBadGateway, "gateway_unavailable", "gateway is unavailable")
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(response.Body)
}
