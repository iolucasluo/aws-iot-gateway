# Shared local environment

## Database initialization

根工作区的 cwj 统一入口会在 API 前显式执行可重复的 seed runner：

```sh
docs/coding-rules/skills/local-dev/scripts/dev.sh up --env cwj
```

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

`deploy/device-bootstrap/contracts/development/` is intentionally absent until a shared local Gateway
owner provides both of the following stable inputs:

1. a team-device-reachable hostname or reserved address for the running Gateway;
2. the owner-only P-256 signer configured in that Gateway's ignored local `.env`.

Do not turn a developer laptop's DHCP address into the shared Contract. Generate
the candidate with `deploy/device-bootstrap/scripts/render-local-contract.sh --out
<absolute-new-directory>`, review it, then manually add
`deploy/device-bootstrap/contracts/development/` in the same reviewed Git change. The resulting
directory is public and contains only the endpoint and public SPKI; the signer
PEM remains in the owner's ignored local configuration.
