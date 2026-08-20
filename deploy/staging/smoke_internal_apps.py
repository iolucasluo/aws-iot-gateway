#!/usr/bin/env python3
"""Run a secret-safe HMAC v1 isolation smoke against Internal AWS staging.

The script obtains the already-injected Internal App secret ARN from the active
API task definition. It never prints API secrets, device tokens, or response
bodies. Its requests are limited to product/list/token/replay checks; it does not
activate, bind, unbind, command, or modify a device.
"""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


SIGNATURE_DOMAIN = "ExploreX-OpenAPI-HMAC-v1"
PRODUCTS_PATH = "/api/v1/openapi/device-products"
LIST_PATH = "/api/v1/openapi/devices/list"
TOKEN_PATH = "/api/v1/openapi/devices/token"
EXPECTED_PRODUCT_PIDS = {"feeder-x1", "minion-cam-x1"}


class SmokeError(RuntimeError):
    """A stable, non-sensitive smoke failure."""


def timestamp_from_milliseconds(milliseconds: int) -> str:
    return f"0000001{milliseconds:013d}"


def canonicalize_path(path: str) -> str:
    if not path.startswith("/") or "?" in path or "#" in path:
        raise ValueError("invalid path")
    return "/".join(
        urllib.parse.quote(urllib.parse.unquote(segment), safe="-._~")
        for segment in path.split("/")
    )


def canonicalize_query(raw_query: str) -> str:
    if not raw_query:
        return ""
    pairs = urllib.parse.parse_qsl(raw_query, keep_blank_values=True, strict_parsing=True)
    encoded = [
        (
            urllib.parse.quote(key, safe="-._~"),
            urllib.parse.quote(value, safe="-._~"),
        )
        for key, value in pairs
    ]
    return "&".join(f"{key}={value}" for key, value in sorted(encoded))


def build_canonical_request(
    *,
    api_key: str,
    timestamp: str,
    nonce: str,
    method: str,
    path: str,
    raw_query: str = "",
    body: bytes,
) -> str:
    return "\n".join(
        (
            SIGNATURE_DOMAIN,
            api_key,
            timestamp,
            nonce,
            method.upper(),
            canonicalize_path(path),
            canonicalize_query(raw_query),
            hashlib.sha256(body).hexdigest(),
        )
    )


def build_signed_headers(
    *,
    api_key: str,
    api_secret: str,
    timestamp: str,
    nonce: str,
    method: str,
    path: str,
    raw_query: str = "",
    body: bytes,
) -> dict[str, str]:
    canonical = build_canonical_request(
        api_key=api_key,
        timestamp=timestamp,
        nonce=nonce,
        method=method,
        path=path,
        raw_query=raw_query,
        body=body,
    )
    signature = hmac.new(api_secret.encode(), canonical.encode(), hashlib.sha256).hexdigest()
    return {
        "Content-Type": "application/json",
        "X-Api-Key": api_key,
        "X-Timestamp": timestamp,
        "X-Nonce": nonce,
        "X-Signature": signature,
    }


def aws_text(profile: str, region: str, *args: str) -> str:
    result = subprocess.run(
        ["aws", *args, "--profile", profile, "--region", region, "--output", "text"],
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if result.returncode != 0:
        raise SmokeError("AWS metadata lookup failed")
    return result.stdout.strip()


def load_credentials(
    *, profile: str, region: str, stack: str, cluster: str, api_service: str
) -> tuple[str, dict[str, dict[str, str]]]:
    base_url = aws_text(
        profile,
        region,
        "cloudformation",
        "describe-stacks",
        "--stack-name",
        stack,
        "--query",
        "Stacks[0].Outputs[?OutputKey==`APIBaseURL`].OutputValue | [0]",
    )
    task_definition = aws_text(
        profile,
        region,
        "ecs",
        "describe-services",
        "--cluster",
        cluster,
        "--services",
        api_service,
        "--query",
        "services[0].taskDefinition",
    )
    secret_arn = aws_text(
        profile,
        region,
        "ecs",
        "describe-task-definition",
        "--task-definition",
        task_definition,
        "--query",
        "taskDefinition.containerDefinitions[0].secrets[?name==`INTERNAL_APPS_JSON`].valueFrom | [0]",
    )
    secret = aws_text(
        profile,
        region,
        "secretsmanager",
        "get-secret-value",
        "--secret-id",
        secret_arn,
        "--query",
        "SecretString",
    )
    try:
        credentials = {item["id"]: item for item in json.loads(secret)}
    except (json.JSONDecodeError, KeyError, TypeError) as error:
        raise SmokeError("Internal App configuration is invalid") from error
    if not base_url.startswith("https://"):
        raise SmokeError("staging API origin is invalid")
    return base_url, credentials


def post_json(
    *,
    base_url: str,
    credential: dict[str, str],
    path: str,
    payload: dict[str, str],
    timestamp: str | None = None,
    nonce: str | None = None,
) -> tuple[int, dict[str, object]]:
    body = json.dumps(payload, separators=(",", ":")).encode()
    headers = build_signed_headers(
        api_key=credential["api_key"],
        api_secret=credential["api_secret"],
        timestamp=timestamp or timestamp_from_milliseconds(int(time.time() * 1000)),
        nonce=nonce or str(uuid.uuid4()),
        method="POST",
        path=path,
        raw_query="",
        body=body,
    )
    request = urllib.request.Request(base_url + path, data=body, method="POST", headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


def get_json(
    *, base_url: str, credential: dict[str, str], path: str
) -> tuple[int, dict[str, object]]:
    body = b""
    headers = build_signed_headers(
        api_key=credential["api_key"],
        api_secret=credential["api_secret"],
        timestamp=timestamp_from_milliseconds(int(time.time() * 1000)),
        nonce=str(uuid.uuid4()),
        method="GET",
        path=path,
        raw_query="",
        body=body,
    )
    request = urllib.request.Request(base_url + path, method="GET", headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


def error_code(body: dict[str, object]) -> str:
    value = body.get("error_code")
    return value if isinstance(value, str) else ""


def require_status(label: str, actual: int, expected: int) -> None:
    if actual != expected:
        raise SmokeError(f"{label} returned HTTP {actual}, expected {expected}")


def require_expected_products(body: dict[str, object]) -> None:
    products = body.get("products")
    if not isinstance(products, list):
        raise SmokeError("staging product catalog is invalid")
    actual_pids = {
        item.get("pid") for item in products if isinstance(item, dict) and isinstance(item.get("pid"), str)
    }
    if not EXPECTED_PRODUCT_PIDS.issubset(actual_pids):
        raise SmokeError("staging product catalog is missing required products")


def run(args: argparse.Namespace) -> None:
    base_url, credentials = load_credentials(
        profile=args.profile,
        region=args.region,
        stack=args.stack,
        cluster=args.cluster,
        api_service=args.api_service,
    )
    try:
        demo = credentials[args.app_id]
        peer = credentials[args.peer_app_id]
    except KeyError as error:
        raise SmokeError("requested Internal App ID is not configured") from error

    products_status, products_body = get_json(
        base_url=base_url, credential=demo, path=PRODUCTS_PATH
    )
    require_status("product catalog", products_status, 200)
    require_expected_products(products_body)

    list_payload = {"binding_external_user_id": args.binding_external_user_id}
    demo_status, demo_body = post_json(
        base_url=base_url, credential=demo, path=LIST_PATH, payload=list_payload
    )
    require_status("configured app list", demo_status, 200)
    demo_devices = demo_body.get("devices")
    if not isinstance(demo_devices, list) or not demo_devices:
        raise SmokeError("configured app has no bound staging device")
    first_device = demo_devices[0]
    if not isinstance(first_device, dict) or not isinstance(first_device.get("device_sn"), str):
        raise SmokeError("configured app returned an invalid device list")
    device_sn = first_device["device_sn"]

    peer_status, peer_body = post_json(
        base_url=base_url, credential=peer, path=LIST_PATH, payload=list_payload
    )
    require_status("peer app list", peer_status, 200)
    peer_devices = peer_body.get("devices")
    if not isinstance(peer_devices, list) or peer_devices:
        raise SmokeError("peer app can see the configured app binding")

    demo_token_status, demo_token_body = post_json(
        base_url=base_url, credential=demo, path=TOKEN_PATH, payload={"device_sn": device_sn}
    )
    require_status("configured app token", demo_token_status, 200)
    if not isinstance(demo_token_body.get("device_token"), str):
        raise SmokeError("configured app token response is missing its token")

    peer_token_status, peer_token_body = post_json(
        base_url=base_url, credential=peer, path=TOKEN_PATH, payload={"device_sn": device_sn}
    )
    require_status("peer app token", peer_token_status, 403)
    if error_code(peer_token_body) != "device_operation_forbidden":
        raise SmokeError("peer app token denial used an incompatible error code")

    replay_timestamp = timestamp_from_milliseconds(int(time.time() * 1000))
    replay_nonce = str(uuid.uuid4())
    replay_first_status, _ = post_json(
        base_url=base_url,
        credential=demo,
        path=LIST_PATH,
        payload=list_payload,
        timestamp=replay_timestamp,
        nonce=replay_nonce,
    )
    replay_second_status, replay_second_body = post_json(
        base_url=base_url,
        credential=demo,
        path=LIST_PATH,
        payload=list_payload,
        timestamp=replay_timestamp,
        nonce=replay_nonce,
    )
    require_status("first nonce request", replay_first_status, 200)
    require_status("replayed nonce request", replay_second_status, 409)
    if error_code(replay_second_body) != "nonce_replayed":
        raise SmokeError("nonce replay denial used an incompatible error code")

    print("configured_app_list=200")
    print(f"configured_app_device_count={len(demo_devices)}")
    print("peer_app_list=200")
    print("peer_app_device_count=0")
    print("configured_app_token=200")
    print("peer_app_token=403:device_operation_forbidden")
    print("nonce_replay=409:nonce_replayed")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", default="explorex-staging-hk")
    parser.add_argument("--region", default="ap-east-1")
    parser.add_argument("--stack", default="aws-iot-gateway-staging")
    parser.add_argument("--cluster", default="aws-iot-gateway-staging")
    parser.add_argument("--api-service", default="api")
    parser.add_argument("--app-id", default="demo-staging")
    parser.add_argument("--peer-app-id", default="livpet-staging")
    parser.add_argument("--binding-external-user-id", default="aws-iot-demo-user")
    return parser.parse_args()


if __name__ == "__main__":
    try:
        run(parse_args())
    except SmokeError as error:
        print(f"FAIL: {error}", file=sys.stderr)
        raise SystemExit(1)
