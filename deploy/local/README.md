# Shared local environment

## Database initialization

Gateway-owned launcher 会在 API 前显式执行可重复的 seed runner；根工作区入口只委托同一 contract：

```sh
tool/dev.sh up --environment-id cwj
# 聚合工作区等价入口：
docs/coding-rules/skills/local-dev/scripts/dev.sh up --env cwj
```

`tool/dev.sh status/up/down/test` 的 contract version 为 `1`。`up` 按 `GATEWAY_LOCAL_*_PORT` 探测依赖，
并要求 `DATABASE_URL`/`REDIS_URL` 使用匹配端口的 loopback endpoint；不满足时不会执行 compose、seed 或 API。
为防止驱动 query 覆盖已校验目标，PostgreSQL query 只允许省略或精确 `sslmode=disable`，Redis query 必须省略。
seed/API 使用的最终环境快照会再次校验。只有声明端口未监听时才启动固定名
`aws-iot-gateway-local` compose project 的 fallback service；`down` 显式指定同一 project，不接受调用方
`COMPOSE_PROJECT_NAME` 覆盖，也不停止开发者已有进程。

手工使用 `deploy/local/compose.yaml` 的新库时，加载开发者自己的 `.env.<name>` 后执行同样流程：

```sh
docker compose --env-file .env.cwj -f deploy/local/compose.yaml up -d --wait
set -a
. ./.env.cwj
set +a
go run ./cmd/aws-iot-gateway-db-seed
go run ./cmd/aws-iot-gateway
```

seed 命令可重复执行且仅允许 `GATEWAY_ENVIRONMENT=development|staging`。API/worker 只自动执行 migration，不会
自动 seed；未执行时 `GET /api/v1/openapi/device-products` 返回空列表，activation 不可用。

## Device bootstrap contract

The shared development Contract is published at
`deploy/device-bootstrap/contracts/development/` for the operator-approved LAN Gateway:

```text
Gateway:        http://192.168.11.131:14081
Time bootstrap: http://192.168.11.131:14081/api/v1/device-time/bootstrap
Key ID:         time-development-g1
```

The directory is public and contains only the endpoint and P-256 public SPKI. The matching signer
PEM remains in the owner's ignored local configuration and must never be committed. If the shared
endpoint or signer changes, generate a new candidate with
`deploy/device-bootstrap/scripts/render-local-contract.sh --out <absolute-new-directory>`, review it,
and replace the complete published directory in one Git change. Do not publish a developer laptop's
unreserved DHCP address or copy only one file from a candidate.
