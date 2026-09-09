#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
LIB="$ROOT/deploy/aws-staging/lib/release_source.sh"
DEPLOY="$ROOT/deploy/aws-staging/deploy.sh"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

[ -f "$LIB" ] || fail "release source helper is missing"
. "$LIB"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/release-source-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM
fixture="$tmp/fixture"
archive="$tmp/archive"
mkdir -p "$fixture" "$archive"
git -C "$fixture" init -q
git -C "$fixture" config user.name release-source-test
git -C "$fixture" config user.email release-source-test@example.invalid
printf 'committed\n' > "$fixture/tracked.txt"
git -C "$fixture" add tracked.txt
git -C "$fixture" commit -qm fixture
commit=$(git -C "$fixture" rev-parse HEAD)

create_release_source "$fixture" "$commit" "$archive"
printf 'live-worktree-change\n' > "$fixture/tracked.txt"
printf 'untracked\n' > "$fixture/untracked.txt"

[ "$(cat "$archive/tracked.txt")" = committed ] || fail "archive followed live worktree changes"
[ ! -e "$archive/untracked.txt" ] || fail "archive copied untracked worktree content"
grep -F 'create_release_source "$GATEWAY_ROOT" "$SOURCE_COMMIT" "$RELEASE_SOURCE_ROOT"' "$DEPLOY" >/dev/null || \
    fail "deploy entrypoint does not create a pinned release source"
grep -F '"$RELEASE_SOURCE_ROOT"' "$DEPLOY" >/dev/null || \
    fail "deploy entrypoint does not consume the pinned release source"

archive_gate="$tmp/archive-gate"
mkdir -p "$archive_gate"
(cd "$ROOT" && tar --exclude=.git --exclude=.runtime --exclude=.local \
    --exclude=./deploy/environments --exclude=.env --exclude='.env.*' -cf - .) | tar -xf - -C "$archive_gate"
[ ! -e "$archive_gate/deploy/environments" ] || fail "archive gate copied private deployment configuration"
[ ! -e "$archive_gate/.local" ] || fail "archive gate copied private local configuration"
bash "$archive_gate/deploy/device-bootstrap/tests/test_contract_publishers.sh" >/dev/null || \
    fail "device bootstrap contract gate is not archive-safe"

printf 'gate-mutated\n' > "$archive/tracked.txt"
build_archive="$tmp/build-archive"
mkdir -p "$build_archive"
create_release_source "$fixture" "$commit" "$build_archive"
[ "$(cat "$build_archive/tracked.txt")" = committed ] || \
    fail "build-facing release source reused gate mutations"
grep -F 'prepare_build_release_source' "$DEPLOY" >/dev/null || \
    fail "deploy entrypoint does not rematerialize build-facing source"
python3 - "$DEPLOY" <<'PY'
from pathlib import Path
import sys

source = Path(sys.argv[1]).read_text(encoding="utf-8")
deploy = source[source.index("deploy_mode() {") :]
gates = deploy.index("run_local_release_gates")
refresh = deploy.index("prepare_build_release_source")
build = deploy.index("build_or_resolve_image")
if not gates < refresh < build:
    raise SystemExit("build-facing source must be rematerialized after gates and before build")
PY

echo "test_release_source: OK"
