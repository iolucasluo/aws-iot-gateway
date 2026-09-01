# HTTP 与 Activation 契约

## 认证域

- `GET /healthz` 无认证，只证明进程存活。
- `/api/v1/openapi/*` 全部使用 `ExploreX-OpenAPI-HMAC-v1`；`openapi` 仅为 LivPet 路径兼容。
- 设备安装端点位于 `/api/v1/device-activations/*`，使用短时一次性 activation code 和 operational-key installation proof。
- Bearer `/v1/*`、开发者 JWT、登录、Project/Product/credential CRUD 均不存在。`GET /api/v1/openapi/device-products`
  是 HMAC 保护的只读激活目录，不是产品管理 API。

HMAC 必须绑定 api key、20 位 timestamp、nonce、method、canonical path/query 和精确 body hash。Redis 原子消费 nonce；缺失 replay storage 时返回 503。签名 body 上限 1 MiB，JSON handler 可以设置更窄上限。

## Activation

```text
pending -> activating -> installing -> finalizing -> activated
                                      \-> failed/expired/cancelled
```

- create 绑定可信 `internal_app_id` principal、`binding_external_user_id`、method、显式 PID 和 idempotency key。
- 调用方先读取 `GET /api/v1/openapi/device-products`，只能提交当前 `products.status='active'` 的 PID；缺失 PID
  返回 `400 invalid_request`，未知或 disabled PID 返回 `400 product_unavailable`，不提供默认 PID 或 method/PID fallback。
- 产品目录同时返回受控 `capabilities`；每项只允许小写字母/数字/`.`/`-`，每个产品
  最多 32 项且不得重复。capability 是 Gateway 对外协议准入，不是 App UI 配置或设备自报信任源。
- activation code 为 256 bit 随机值，只在创建响应出现，持久化只保存 SHA-256。
- Gateway 首次 prepare 生成随机 `device_sn`；AWS ThingName、MQTT client ID 和设备运行 ID 使用同值。
- QR prepare 只接受 QR activation；session prepare 只接受显式 AP/BLE method，不跨 method fallback。
- 设备本地生成 operational key/CSR。Gateway 只接收 CSR，certificate 初始 inactive；proof 通过后才激活。
- polling 只按 HMAC principal 限定 internal app，响应不重复 activation code。
- 同一 device 只能有一条 active binding；跨 App 必须先完成 unbind 或 recommission。

## 错误与输入

- 拒绝未知 JSON 字段、畸形 percent escape、超限 body、过期/重放 nonce 和错误 method。
- `reset_wifi`、`factory_reset(scope=wifi)` 和缺失显式 scope 的 `factory_reset` 必须在 MQTT publish
  前以 `400 invalid_request` 拒绝；设备移交走 ACK-based `recommission(runtime)`。物理恢复出厂后
  仍由原绑定用户换网时必须走下一条预签名授权路径，防止设备运行身份与 Gateway binding/token 分裂。
- 物理按键恢复出厂不等于解绑：设备保留运行身份与 binding，但清除 Wi-Fi 和用户数据。重新写入 Wi-Fi
  必须转发原绑定 App 预签名的 `network-reconfiguration-authorizations` 请求，并同时通过 HMAC v1、
  nonce/时效、`binding_external_user_id` 精确比较和该 binding 的 `X-Device-Token`；body 只能携带
  Wi-Fi 凭据摘要，不能上传明文。摘要字段依次为 domain、device_sn、wifi_ssid、wifi_password；每个
  UTF-8 字段前置 4 字节无符号大端长度，最后取 SHA-256 小写 hex。`device_sn` 使用受限可打印 ASCII；
  App 与设备对 SSID/密码均拒绝 C0 控制字符和 DEL，密码只允许空值、8–63-byte passphrase 或
  64-byte hex PSK。
- 稳定业务错误沿用兼容扁平格式 `{ "error_code": "...", "message": "...", "error": "..." }`，
  不得返回 AWS/SQL/Redis 内部错误。
- token、code、secret、CSR 私钥和设备私钥不得进入响应之外的日志；Gateway 永不接收 operational private key。

## 局域网媒体门禁

- 只有 active 产品显式声明 `local-media.v1` 才允许创建租约；未知 PID 和不支持的产品
  都映射为 `409 local_media_unsupported`，不做设备类型猜测。
- 临时 ONVIF/RTSP 凭据不进入 `device_commands`、PostgreSQL、Redis 或日志；设备 ACK 只可
  回传 generation 和 SHA-256 指纹。对外响应必须 `no-store`。
- `local_media` 首次成功 ACK 的 `result` 为 `applied`；仅相同 revision/payload 的 MQTT QoS 1
  幂等重投允许 `duplicate`，且其余 ACK 字段仍须精确匹配。
- Gateway 只对设备 ACK 中的私网 IPv4 locator 做结构校验，绝不从服务端拨号、代理或
  探测该地址；App 必须在同一 LAN 直连设备。
- close 必须带原租约 `credential_generation`，设备只关闭精确匹配的 generation。
- `local_media` 不是通用 DP command；PTZ、喂食和其他控制继续经过 OpenAPI → MQTT。
- `lm_` session 前缀只属于 typed `local_media` 路径；通用 DP command 必须在落库和 MQTT
  publish 前拒绝该前缀，避免 ACK 路由冲突和副作用重试。

## 产品 DP 合同

- `products` 与 `product_dps` 是 Gateway-owned 的只读运行合同目录；产品/DP 变更必须通过 reviewed
  migration/seed 完成，当前不提供运行时 CRUD。
- 通用 `commands` 和 `schedules` 在写入 `device_commands` 或发布 MQTT 前，按设备登记的 PID 加载
  `product_dps`，拒绝未知、disabled、只读方向或 JSON 形状不匹配的 DP；typed media、local media、OTA
  和 recommission 仍走各自的受控服务路径。
