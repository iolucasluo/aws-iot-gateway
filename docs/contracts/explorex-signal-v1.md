# ExploreX signaling protocol v1

状态：Phase 9.7 readiness audit；仍是 adapter/spike 契约。该文档定义 provider-neutral 的信令边界；它不代表当前 Gateway 二进制已经启动该服务，也不代表 Anyka 固件已经消费该协议。

## 0.1 当前实现审计（2026-09-02）

当前国内运行时有意保持 fail-closed，以下事实在接入真实 provider 前不得改写成“已支持”：

- `cmd/aws-iot-gateway/domestic_runtime.go` 只装配 identity、binding、device command、MQTT ingest 和 signed-time；`httpapi.Dependencies.Media` 未注册，因此国内 `POST /streams` 返回 `503 device_service_unavailable`。
- `internal/platform/explorexsignal.Client` 只实现本契约的 HTTP `POST/DELETE /v1/sessions` 与 `GET /healthz` adapter；它不实现 WebSocket server、session authority、ticket 一次性消费或 SDP/ICE 转发。
- `internal/modules/media.StreamService` 仍消费 `provider.PlaybackProvider`，创建 KVS-style channel/playback；它没有把 `provider.RealtimeProvider` descriptor 接入设备 stream 生命周期。
- Anyka feeder 的 `explorex_webrtc` 仍链接 KVS SDK 并调用 `kvs_master_run`，且要求 AWS Credential Provider、Role Alias、`ap-east-1` 和 AWS Root CA。国内 profile 缺少这些字段时必须拒绝启动；不能通过伪造配置绕过。

因此，真实国内 WebRTC 的最小前置不是改 endpoint，而是同时具备：一个已部署且有认证/授权/一次性 ticket/TTL 证据的 signaling service、Gateway media→realtime composition wiring，以及 Anyka provider-neutral signaling consumer。三者缺一，cn-staging 维持 `BLOCKED`。

## 1. 边界

`explorex-signal-v1` 是自建 WebRTC signaling 的最小 wire contract。它只负责短期 session、SDP offer/answer 和 ICE candidate 转发；媒体流量仍由 WebRTC direct/ICE relay 承载。

- Gateway 通过 `RealtimeProvider` 创建和关闭 session。
- provider 返回的 `client_config` 由 provider 拥有，领域层不解释其额外字段。
- coturn 只提供 ICE relay，不伪装成 signaling provider，也不承担 session 权威状态。
- AWS KVS 使用自己的 envelope；不能把 KVS 的 base64 `messagePayload` 混入本协议。

## 2. Session HTTP API

### `POST /v1/sessions`

请求必须包含：

```json
{
  "device_id": "device-1",
  "binding_id": "binding-1",
  "request_id": "req-1",
  "requested_protocol": "explorex-signal-v1",
  "capabilities": {
    "video_receive": true,
    "audio_receive": false,
    "audio_send": false,
    "data_channels": []
  },
  "expires_at": "2030-01-01T00:05:00Z"
}
```

`Idempotency-Key` 必须等于 `request_id`。服务端拒绝空标识符、未知协议、超过 15 分钟的 TTL 和过期请求。

成功响应必须严格包含以下字段：

```json
{
  "session_id": "session-1",
  "provider_instance": "realtime.cn_staging",
  "protocol": "explorex-signal-v1",
  "client_config": {
    "wss_url": "wss://rtc.example.test/v1/sessions/session-1?ticket=one-time",
    "ice_servers": [
      {"urls": ["turn:rtc.example.test:3478"], "username": "u", "credential": "c"}
    ]
  },
  "capabilities": {"protocols": ["explorex-signal-v1"]},
  "expires_at": "2030-01-01T00:05:00Z"
}
```

`wss_url` 中的 ticket 必须是短期、单 session、不可日志复用的凭据；API 和 adapter 均不得记录完整 URL、Authorization 或 ICE credential。响应的 `expires_at` 不得晚于请求值。

### `DELETE /v1/sessions/{session_id}`

关闭是幂等的。session 关闭后，ticket 不得再接受新的 WebSocket 连接；自然过期也必须达到同一效果。服务端不依赖客户端提供的 provider instance 来决定资源边界，adapter 会携带并校验该身份。

### `GET /healthz`

只表示 signaling control-plane 可接收请求，不表示 TURN、设备在线或媒体链路可用。

## 3. WebSocket wire messages

WebSocket 连接使用响应中的 `client_config.wss_url`。消息是 UTF-8 JSON object，禁止 KVS envelope、未知消息类型和超过实现上限的 payload。

Viewer → signaling：

```json
{"type":"offer","sdp_type":"offer","sdp":"v=0..."}
{"type":"ice-candidate","candidate":"candidate:...","sdp_mid":"0","sdp_mline_index":0}
```

Signaling → viewer：

```json
{"type":"answer","sdp_type":"answer","sdp":"v=0..."}
{"type":"ice-candidate","candidate":"candidate:...","sdp_mid":"0","sdp_mline_index":0}
```

`candidate`、`sdp_mid`、`sdp_mline_index` 可按 WebRTC API 约定为 `null`，但字段类型不能漂移；`answer` 必须是 `sdp_type=answer`。未知类型、非法 JSON、类型错误或 session 失效均终止该连接并由客户端清理 stream。

## 4. 安全与验收边界

- API authentication、session authorization、device/binding ownership 和 ticket 一次性消费必须由 signaling service 实现；adapter 只做 transport/shape 校验。
- 每次创建/关闭都必须带 request/session identity 和 TTL；重试只能复用同一 idempotency key。
- 日志只允许记录 provider instance、request id、session 状态和错误类别，不记录 SDP、完整 URL、token 或 TURN credential。
- 本阶段只验收 adapter 的 strict HTTP contract、App 的协议分流和 coturn 进程形状；真实 signaling service、真实 TURN 公网可达性、Anyka HIL 和生产性能留到后续验收。
