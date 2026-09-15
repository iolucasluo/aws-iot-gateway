# Platform Plane v1（平台面设计与边界）

> 状态：设计基线，随 `iot-flash/feature-platform-plane` 分支实施。
> 事实来源：`aws-iot-gateway` 当前 `iot-flash/develop`；旧平台只读参考 `explorex-cloud@b9ec707`（`ai-iot` namespace）。
> 进度与验证证据在根仓分支日志 `docs/branches/iot-flash-feature-platform-plane.md`，本文件只描述 **已批准的设计与边界**，不是进度载体。

## 1. 定位与边界

`aws-iot-gateway` 拥有两件事：

- **(a) IoT 中间层**：设备 activation、全局唯一绑定、运行身份（ThingName/证书/presence）、受控设备操作、事件与媒体、签名固件版本与单设备 OTA deployment。
- **(b) ExploreX 平台面**：门户账号与会话、Developer/Project/Product 归属、项目级 API 凭证、代工厂与产线质量证据。

仍然不做（红线，不得借本设计回流）：

- 通用多租户 IoT 平台。`Project`/`Product`/`Factory` 只在**单一平台实例内**表达归属与作用域，不作为 tenant 隔离维度；设备依旧全局唯一绑定。
- 第二套设备域。不新建 `devices`/`bindings`/`device_tokens`/`device_certificates`；门户一律读本仓既有表。
- IDevID / 出生身份、RMA、OTA campaign/批量发布/定时/自动重试、设备证书轮换与离线恢复通道、计量计费、任意第三方自助注册、WebAuthn/2FA、邮件验证与邮件找回。

命名沿用既有 `/api/v1/{admin,developer,manufacturer}/*` 前缀（旧平台已验证的路径习惯），但路由、响应字段与状态机均由本仓按自身资源模型实现（设备标识统一用 `device_sn`，不使用旧平台的 `thingName` 混用）。

## 2. 进程与部署拓扑

平台面**同一 Go module、同一 PostgreSQL、同一 Redis**，但作为**独立二进制与独立部署单元**运行：

| 项 | gateway（既有） | platform（新增） |
|---|---|---|
| 二进制 | `cmd/aws-iot-gateway` | `cmd/explorex-platform-api` |
| 默认端口 | `:14080` | `:14081` |
| 路由 | 设备侧 + `/api/v1/openapi/*`（HMAC） | `/api/v1/{admin,developer,manufacturer}/*`（会话） |
| 鉴权 | HMAC v1 + `X-Device-Token` | Portal session（Bearer） |
| 公网入口 | 仅 `/api/v1/device-time/bootstrap` | 平台三前缀（CORS 白名单） |

理由：把浏览器面（登录、密码、会话、用户输入、CORS）与设备信任进程隔离，可独立扩缩容与回滚；同时避免跨服务鉴权与第二套授权实现。

平台进程**不注册任何设备侧路由**；它通过 import 本仓既有 domain 服务（`internal/activation`、`internal/modules/*`）完成设备查询与操作。IAM 只授予 v1 实际使用的能力（IoT shadow 只读、命令发布、OTA 发布、S3 artifact），不授予设备证书签发与 KVS/KMS。

## 3. 身份、账号与会话

- **账号模型**：单表 `portal_accounts`，`kind IN ('staff','developer','manufacturer')`，`role` 与 `manufacturer_id` 由 CHECK 约束联动：staff=`super_admin`（无厂商）、developer=`developer`（无厂商）、manufacturer=`admin|operator`（必须有厂商）。不建 RBAC 权限表。
- **会话**：不透明 token（32 字节随机，base64url），DB 只存 SHA-256，登录时明文返回一次；`portal_sessions.audience` 必须与请求前缀匹配，杜绝跨门户越权。绝对过期 12h，不滑动续期；禁用账号即时失效。
- **密码**：Argon2id（`golang.org/x/crypto/argon2`），参数 m=65536 KiB / t=3 / p=2，salt 16B，key 32B，编码进标准 `$argon2id$...` 串。
- **账号来源（v1）**：全部由上级创建/邀请。staff 建 developer 与厂商 admin，厂商 admin 建 operator 与工位。一次性 setup token（哈希入库、单次、有过期）由控制台展示一次并线下转交，**不依赖 SMTP**。
- **登录保护**：复用 `internal/platform/redis.RateLimiter`，按 `(audience,email)` 与 `(audience,ip)` 双维度 5 次/15 分钟；失败统一 `401 invalid_credentials`。
- **首个 staff 账号**：由 `cmd/explorex-platform-bootstrap` 一次性创建并打印 setup token；不写 SQL seed，不把口令写进任何部署配置。

## 4. 数据模型

新增表全部走**顺序 additive migration**（`003+`），不改写 `001_initial.sql` 与其历史断言。

### 4.1 平台面新增表

| 表 | 用途 | 关键不变量 |
|---|---|---|
| `portal_accounts` | staff/developer/manufacturer 账号 | `email` 唯一且小写；kind/role/manufacturer_id 组合 CHECK |
| `portal_sessions` | 会话 | `token_hash` 唯一（32 字节）；`audience` 枚举；过期/吊销可判 |
| `portal_account_setup_tokens` | 一次性邀请/重置 | `token_hash` 唯一；purpose 枚举；`used_at` 单次 |
| `manufacturers` | 代工厂主数据 | `code` 唯一 |
| `projects` | 凭证与设备归属锚点 | `project_type` cloud/oem；oem 必须有 manufacturer，cloud 必须无 |
| `project_products` | 项目可用 PID 白名单 | `UNIQUE(project_id, product_id)` |
| `api_credentials` | 项目级 HMAC 凭证 | `api_key` 唯一；secret 只存密文 + `kek_id` |
| `project_webhooks` | 项目级 webhook 配置 | URL 必须 https 且无凭据/查询/片段 |
| `production_stations` | 工位 | `UNIQUE(manufacturer_id, code)` |
| `production_runs` | 产线批次/测试 Run | `UNIQUE(project_id, batch_code)`；open/closed 状态与关闭人/时间一致 |
| `production_station_reports` | 工位测试证据 | append-only；`UNIQUE(run_id, station_id, sequence_no)`；`units_passed + units_failed = units_total` |

### 4.2 既有表扩展（additive）

- `products` += `owner_account_id uuid NULL`、`category varchar NULL`（seed 产品保持 NULL owner）。
- `audit_logs` += `actor_kind`、`actor_account_id`、`actor_label`（无 FK，保留主体删除后的审计）。

### 4.3 不变量

- **不新增第二套设备域**；`uniq_device_bindings_active_device`、`binding_external_user_id` 不可变 trigger、DP 合同 seed 化、单设备 OTA deployment 全部保持不变。
- **产线证据与运行身份解耦**：`production_station_reports` 允许引用尚不存在于 `device_registry` 的 `device_sn/chip_uid`，因此不建强 FK。
- `Project` 不是 tenant；设备仍全局唯一绑定。

## 5. 凭证模型：`INTERNAL_APPS_JSON` → `projects + api_credentials`

1. **权威源**：HMAC 验签先按 `api_key` 查 `api_credentials`（唯一索引）。命中即用该行（`disabled`/`过期` → 401），**不回退 JSON**；DB 未命中才回退 `INTERNAL_APPS_JSON`（本地开发、应急引导、灰度回退）。
2. **主体语义**：`Principal.InternalAppID := project_id`（DB 凭证）或 legacy app id（JSON）。设备/激活表**零改动**，既有 `internal_app_id` 列语义变为「主体 id」；**归属随项目而非随凭证**，凭证轮换不丢设备归属。
3. **线上格式不变**：签名域 `ExploreX-OpenAPI-HMAC-v1`、请求头、±5 分钟窗口、Redis nonce、1 MiB body 上限、错误码全部不动 → LivPet backend 与 `aws-iot-demo-app` 零改动。
4. **权限**：`api_credentials.permissions` 为 CHECK 约束的白名单集合（`devices:read`、`devices:operate`、`ota:publish`、`ota:deploy`），默认最小集。
5. **secret 存储**：AES-256-GCM，KEK 来自环境/密钥文件（`PLATFORM_CREDENTIAL_KEK` + `PLATFORM_CREDENTIAL_KEK_ID`），与云厂商无关，AWS 用 Secrets Manager、国内用 0600 挂载文件；v1 只实现读取与 `kek_id` 记录，不实现自动重加密。
6. **产品可见性**：`GET /api/v1/openapi/device-products` 对 DB 凭证主体只返回该项目 `project_products` 内的 active 产品；legacy JSON app 保持全局列表，避免开发者自建 PID 污染所有 App 目录。
7. **`last_used_at`**：采样写（同凭证 60 秒最多一次），写失败不影响请求。

## 6. 作用域模型

`ProjectScope{ProjectID, ManufacturerID, AllowedProductIDs}` 由 `projects` + `project_products` **服务端派生**，fail-secure：产品白名单为空即全拒。门户所有跨实体查询都带 scope 谓词；越权统一返回 `404 not_found`，不泄露资源存在性。v1 不引入 `allowed_sn_prefixes`（无消费方）。

## 7. HTTP 契约

统一约定：`Authorization: Bearer <token>`；错误体沿用 `{"error_code","message","error"}`；列表统一 `?limit=`（默认 50，上限 200），响应 `{"items":[...]}`；不透明 `?cursor=`（编码 `(created_at,id)`）只用于高基数列表（设备、事件、审计、投递记录，P3 起），账号/项目/产品等有界列表 v1 不提供 cursor；写操作落审计（P3 接入）；`Idempotency-Key` 可选支持于凭证创建/轮换与 OTA deployment 创建。

- **`/api/v1/admin/*`**（audience=admin）：`auth/{login,set-password,logout,me,password}`；`accounts*`（含 `password-reset`）；`manufacturers*`；`products*`；`projects*`（只读 + 状态）；`credentials`（只读 + disable）；`devices`（只读）、`devices/{device_sn}/force-unbind`；`bindings`、`device-tokens`、`activations`、`certificates`；`ota/firmware-versions`、`ota/deployments`；`factory-test-reports`、`audit-logs`、`hook-audit`。
- **`/api/v1/developer/*`**（audience=developer）：`auth/*`；`projects*` 与 `projects/{id}/products`；`products*`；`dp-profiles`（active 只读）；`projects/{id}/credentials` 与 `credentials/{id}/{rotate,disable}`；`projects/{id}/devices`、`devices/{device_sn}`、`.../status`、`.../commands`；`projects/{id}/webhooks` 与投递记录；`ota/firmware-versions`（含 `complete`）、`devices/{device_sn}/ota-deployments`；`manufacturers`（只读目录）。
- **`/api/v1/manufacturer/*`**（audience=manufacturer）：`auth/{login,set-password,logout,me,password}`；`projects`（scope）；`production-runs*`（含 `close`）；`stations*`；`production-runs/{id}/station-reports`；`test-reports`；`devices`（scope，只读）；`workers*`（仅 role=admin）。

**产品创建规则**：`pid` 必须匹配 `^[a-z0-9][a-z0-9-]{1,62}$` 且唯一；`dp_profile_code` 必须存在且 active；开发者只能改自己 `owner_account_id` 的产品，staff 可改任意并把 owner 置空（公共目录产品）。DP 定义仍由 reviewed contract seed 发布，**门户不提供 DP CRUD**。

## 8. 安全要求

1. 平台进程独立部署单元、独立安全组，不注册设备路由；CORS 按门户 origin 白名单，禁止 `*`。
2. 凭证 secret 只在创建/轮换响应中出现一次；日志与审计不得落 secret/token/密码。
3. 登录失败统一文案与耗时；双维度限流；setup token 单次 + 短过期。
4. 越权统一 404；`audience` 与账号 `kind` 双校验。
5. 审计统一写 `audit_logs`（`actor_kind='portal_account'`），覆盖登录成功/失败与全部写操作。
6. 设备侧不变量不变：全局唯一绑定、用户不可变、DP 合同 seed 化、单设备 OTA。

## 9. 非目标

IDevID、RMA、OTA campaign/批量发布/定时/自动重试、设备证书轮换与离线通道、计量计费（usage/quota）、WebAuthn/2FA、邮件验证与邮件找回、自助注册、产线 BLE 直连与 QR 发码批次、`allowed_sn_prefixes`、`product_dps` 运行时 CRUD。

## 10. 验证与证据边界

| 层 | 手段 | 不能替代 |
|---|---|---|
| 单元 | Go `testing`（无 testify） | — |
| 契约 | 平台 schema 契约、audience 隔离、HMAC 双来源回归、scope 越权 | DB 集成 |
| 集成 | `TEST_POSTGRES_URL` 真实 PostgreSQL（Redis 用 miniredis 或真实实例） | 端到端 |
| 端到端 | 本地起 PG/Redis + gateway + platform 两个二进制，跑「登录→建项目→建凭证→真实 HMAC 调用→scope 列表」 | staging |
| 云端 | AWS staging / 国内 compose（需单独授权） | — |

未设置 `TEST_POSTGRES_URL` 时的 skip **不作为** DB 契约的回归证据。

## 11. 开放项

- AWS staging 公开 ALB 现有 listener/证书是否允许新增平台路径（部署前用 `aws-deploy` 核对）。
- 平台的 KEK 轮换流程（v1 只记录 `kek_id`）。
- 三个门户前端的仓库形态与发布形态在根仓分支日志中记录，不在本设计内展开。
