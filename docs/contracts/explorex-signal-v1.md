# ExploreX signaling protocol v1

状态：Phase 11.3 DELL LAN HIL 已验证；production 仍为 `NO-SHIP`。该文档定义 provider-neutral 的信令边界；协议可以由自建服务实现，也不改变 AWS KVS signaling 的独立兼容路径。

## 0.1 历史 readiness 审计（2026-09-02）

下列事实记录接入前的 fail-closed 基线，已由后续实现和 2026-09-03 HIL 取代，不能再作为当前阻塞状态：

- `cmd/aws-iot-gateway/domestic_runtime.go` 只装配 identity、binding、device command、MQTT ingest 和 signed-time；`httpapi.Dependencies.Media` 未注册，因此国内 `POST /streams` 返回 `503 device_service_unavailable`。
- `internal/platform/explorexsignal.Client` 只实现本契约的 HTTP `POST/DELETE /v1/sessions` 与 `GET /healthz` adapter；它不实现 WebSocket server、session authority、ticket 一次性消费或 SDP/ICE 转发。
- `internal/modules/media.StreamService` 仍消费 `provider.PlaybackProvider`，创建 KVS-style channel/playback；它没有把 `provider.RealtimeProvider` descriptor 接入设备 stream 生命周期。
- Anyka feeder 的 `explorex_webrtc` 仍链接 KVS SDK 并调用 `kvs_master_run`，且要求 AWS Credential Provider、Role Alias、`ap-east-1` 和 AWS Root CA。国内 profile 缺少这些字段时必须拒绝启动；不能通过伪造配置绕过。

因此，真实国内 WebRTC 的最小前置不是改 endpoint，而是同时具备：一个已部署且有认证/授权/一次性 ticket/TTL 证据的 signaling service、Gateway media→realtime composition wiring，以及 Anyka provider-neutral signaling consumer。三者缺一，cn-staging 维持 `BLOCKED`。

## 0.2 当前实现与 HIL（2026-09-03）

- DELL 独立 single-node Compose 已运行真实 `explorex-signal-v1` HTTP/WebSocket 服务，Gateway 通过 provider-neutral realtime adapter 创建和关闭 session；ticket、TTL、幂等和设备/Viewer 角色均由 signaling 服务校验。
- Anyka `composition-cn-staging` 消费 device config，通过 provider-neutral WebSocket/runtime 组合直接创建 PeerConnection；复用 KVS WebRTC core library 只作为 PeerConnection 实现，不访问 KVS signaling、AWS credential 或 AWS data plane。
- Demo App 依据响应中的显式 `protocol` 分流，真实播放 feeder 视频；1920×1080、640×360、会话关闭后立即重连和设备断电重连均通过。
- 任一 peer 断开会关闭整个 session 和对端连接，避免残留到 TTL 后才释放所导致的 `device_busy`。
- 该 HIL 只覆盖 LAN HTTP/WS 和 direct ICE。公网 HTTPS/WSS、TURN relay、双向音频、DataChannel、长稳与容量仍未验证，production 继续 `NO-SHIP`。

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

### `POST /v1/sessions/{session_id}/renew`

此内部接口必须携带 Gateway bearer 认证；请求和成功响应均为
`{"expires_at":"2030-01-01T00:09:00Z"}`。请求期限必须晚于服务端当前时间，且最多为
当前时间后五分钟。仅允许仍未过期且 Viewer/Device 均已连接的 session；期限单调延长，
不创建 session、不重新发 ticket、不修改原始 descriptor 的 `expires_at`（入场期限）。
原始 ticket 的期限与一次性消费状态不因续期改变。响应为 200；无认证 401、非法期限 400、
关闭/不存在/已过期 404、双方尚未连接 409。过期 session 不可复活。

Gateway 对 App 暴露 `POST .../{device_sn}/streams/{stream_id}/renew`，请求仅允许空 JSON
对象 `{}`。每次重新校验 HMAC、Device Token、设备与 active binding；期限由 Gateway 决定。
返回 `{"stream_id":"…","expires_at":"…"}` 并禁止缓存。provider 续期后必须成功持久化
才能向 App 报成功；持久化失败会尝试关闭 provider。并发解绑最多留下一个已批准的有限
lease（最长五分钟，服务端清扫另有最多约 30 秒延迟），不能无限续期绕过撤权。

只有 provider 和 stream repository 均支持续期时，创建 stream 的响应才附带
`"session_lease":{"expires_at":"…"}`。App 据此提前一分钟续期；失败、超时或过期会关闭
所属 PeerConnection，退出页面/切环境会取消计时并忽略迟到结果。缺少此字段时不续期，
AWS KVS adapter 保持原路径；不支持的续期请求返回 409。部署顺序为 signaling → API → App，
不要求修改设备固件。回退旧 App 会恢复原有限观看时长，不放宽 session 授权。

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
- DELL LAN 已验收 strict HTTP/WebSocket contract、App 协议分流、真实 signaling service、Anyka 视频 HIL 和 peer 断开清理。
- coturn 仅完成进程/端口形状；真实 TURN 公网可达性、生产 TLS、性能与长稳留到国内云验收。
