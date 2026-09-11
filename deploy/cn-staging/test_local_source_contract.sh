#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
python3 - "$ROOT" <<'PY'
import pathlib
import subprocess
import sys

root = pathlib.Path(sys.argv[1])
rules = (root / '.dockerignore').read_text().splitlines()
assert 'deploy/environments' in rules, 'private deployment environments must be excluded'
subprocess.run(['git', '-C', str(root), 'check-ignore', '-q',
                'deploy/environments/aliyun-staging/config/gateway.env'], check=True)
for relative in ('deploy/aws-staging/deploy.sh',
                 'deploy/aws-staging/cloudformation/staging.yaml',
                 'deploy/aws-staging/cloudformation/bootstrap.yaml',
                 'deploy/cn-staging/compose.yaml',
                 'deploy/device-bootstrap/README.md', 'deploy/local/compose.yaml'):
    assert (root / relative).is_file(), relative
for directory in ('.local', '.runtime'):
    assert directory in rules, f'{directory} must be excluded from Docker context'
    assert not any(line.startswith('!') and directory in line for line in rules)
    subprocess.run(['git', '-C', str(root), 'check-ignore', '-q',
                    f'{directory}/private-fixture.env'], check=True)
operations = (root / 'deploy/cn-staging/operations.md').read_text()
for marker in ('deploy/environments/aliyun-staging/', '不双向自动同步', '配置不是业务数据备份'):
    assert marker in operations, marker
print('local operator source isolation contract: ok')
PY
