# Staging deployment

This deployment is intentionally limited to `ap-east-1` staging. It creates two API tasks, two competing `iot-gateway-event-worker` tasks, PostgreSQL, Redis, IoT rules and policy, SQS/DLQ, KVS permissions, private media storage, a KMS P-256 signed-time key, and an AWS-managed HTTPS API Gateway URL. Status-heartbeat and LWT rules feed PostgreSQL-backed device presence through the same ingest queue. There is no Developer/Project/Product/Factory/RMA/OTA service.

The `INTERNAL_APPS_JSON` value lives in one explicitly created Secrets Manager secret. Do not pass it as a CloudFormation parameter or commit it. RDS creates and rotates its own master secret.

Deployment order:

1. Deploy `deploy/cloudformation/bootstrap.yaml`.
2. Build the container for `linux/amd64`, push an immutable ECR tag, and capture its URI.
3. Create/update the internal-app secret from a local file using `aws secretsmanager create-secret --secret-string file://...`.
4. Deploy `deploy/cloudformation/staging.yaml` with the image URI, secret ARN, and account credential-provider endpoint.
5. Wait for both ECS services to stabilize, then verify `/healthz`, signed `/api/v1/openapi/*`, and the `TimeBootstrapURL` stack output.

After a deployment, run the owner-side HMAC v1 smoke from the Gateway repository:

```sh
python3 deploy/staging/test_internal_apps_smoke.py
python3 deploy/staging/smoke_internal_apps.py \
  --profile explorex-staging-hk --region ap-east-1 \
  --stack aws-iot-gateway-staging --cluster aws-iot-gateway-staging
```

The smoke discovers the currently injected `INTERNAL_APPS_JSON` secret ARN from the active API task definition,
but never prints API secrets, device tokens, or response bodies. It is intentionally limited to HMAC v1
device-list/token isolation and nonce-replay checks; it does not activate, bind, unbind, command, or modify a device.
Use explicit `--app-id`, `--peer-app-id`, and `--binding-external-user-id` values when the staging fixture changes.

The ECS tasks use public egress addresses because staging deliberately avoids a permanent NAT Gateway. The task security group allows port 14080 only from the internal NLB and the signed-time ALB. The Internet-facing ALB listener forwards only exact `POST /api/v1/device-time/bootstrap` path traffic; every other path receives its fixed 404 response. PostgreSQL and Redis have no public endpoint and accept traffic only from the task security group.
