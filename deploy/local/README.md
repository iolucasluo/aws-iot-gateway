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
