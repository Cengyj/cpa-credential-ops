"""Build the native Linux plugin against the supplied unchanged CPA source."""
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import zipfile

ROOT = Path('/src')
OUT = Path('/out')
OUT.mkdir()
ARCH = subprocess.check_output(['go', 'env', 'GOARCH'], text=True).strip()


def run(args, cwd=None):
    subprocess.run(args, cwd=cwd, check=True)


run(['go', 'mod', 'edit', '-replace=github.com/router-for-me/CLIProxyAPI/v8=../host'], ROOT / 'plugin')
run(['go', 'mod', 'tidy'], ROOT / 'plugin')
run(['python3', 'generate_manifest.py'], ROOT / 'login-runtime-go')
run(['go', 'test', './...'], ROOT / 'login-runtime-go')
bundle = OUT / f'login-runtime-linux-{ARCH}'
run(['python3', 'build.py', '--go', '/usr/local/go/bin/go', '--output', str(bundle)], ROOT / 'login-runtime-go')

spec = importlib.util.spec_from_file_location('official_builder', ROOT / 'scripts/build_official_plugin.py')
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)
payload, checksum = builder.embed_runtime(bundle, 'linux', ARCH, '/usr/local/go/bin/go')
run(['go', 'test', '-tags', 'loginbundle_embedded', './...'], ROOT / 'plugin')
run(['go', 'vet', '-tags', 'loginbundle_embedded', './...'], ROOT / 'plugin')
library = OUT / 'model-quality.so'
run(['go', 'build', '-tags', 'loginbundle_embedded', '-buildmode=c-shared', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', '-o', str(library), './cmd/model-quality'], ROOT / 'plugin')
archive = OUT / f'model-quality-v{builder.PLUGIN_VERSION}-linux-{ARCH}.zip'
with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as package:
    package.write(library, library.name)
checksums = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (library, archive)}
(OUT / 'SHA256SUMS').write_text(''.join(f'{value}  {name}\n' for name, value in checksums.items()))
(OUT / 'build-manifest.json').write_text(json.dumps({
    'host_version': '8.0.76', 'plugin_version': builder.PLUGIN_VERSION, 'runtime_version': builder.RUNTIME_VERSION,
    'platform': 'linux', 'arch': ARCH, 'embedded_archive_sha256': checksum,
    'go_version': subprocess.check_output(['go', 'version'], text=True).strip(),
    'checksums': checksums,
    'ui_version': json.loads((ROOT / 'plugin/ui/index.html').read_text().split('<script type="application/json" id="build-info">', 1)[1].split('</script>', 1)[0])['ui_version'],
    'ui_source_sha256': {name: hashlib.sha256((ROOT / 'plugin/ui' / name).read_bytes()).hexdigest() for name in json.loads((ROOT / 'plugin/ui/public_assets.json').read_text())},
}, indent=2) + '\n')
