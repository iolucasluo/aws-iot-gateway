#!/usr/bin/env sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TEMPLATE="$ROOT/deploy/aws-staging/cloudformation/staging.yaml"
SERVER="$ROOT/internal/httpapi/server.go"
count=$(grep -c 'Name: PET_PHOTO_PUBLIC_ORIGIN' "$TEMPLATE" || true)
[ "$count" -eq 2 ] || { echo "pet photo origin must reach API and event worker (got $count)" >&2; exit 1; }
grep -Fq "https://\${HTTPAPI}.execute-api.\${AWS::Region}.\${AWS::URLSuffix}" "$TEMPLATE"
grep -Fq 'case "pet-photos":' "$SERVER"
printf '%s\n' 'test_pet_photo_origin_contract: OK'
