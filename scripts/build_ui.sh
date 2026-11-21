#!/bin/sh
set -eu
TASK_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ -n "${NODE_BIN:-}" ]; then
  TASK_NODE=$NODE_BIN
elif command -v node >/dev/null 2>&1; then
  TASK_NODE=$(command -v node)
else
  TASK_NODE=/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node
fi
for TASK_SCRIPT in app.js client.js view.js login.js workspace.js commands.js conversion.js markup.js; do
  "$TASK_NODE" --check "$TASK_ROOT/plugin/ui/$TASK_SCRIPT"
done
python3 - "$TASK_ROOT" <<'PY'
from pathlib import Path
import hashlib, json, shutil, sys
root = Path(sys.argv[1])
source = root / 'plugin/ui'
output = root / 'artifacts/ui'
output.mkdir(parents=True, exist_ok=True)
manifest = {}
for name in json.loads((source / 'public_assets.json').read_text()):
    data = (source / name).read_bytes()
    shutil.copy2(source / name, output / name)
    manifest[name] = {'bytes': len(data), 'sha256': hashlib.sha256(data).hexdigest()}
(output / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
print('Static UI checked and packaged:', output)
PY
