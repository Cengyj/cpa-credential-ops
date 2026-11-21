"""Shared, source-only release inventories and integrity helpers."""
from pathlib import Path
import hashlib
import json
import os
import re

ROOT = Path(__file__).resolve().parents[1]
LEGACY_UI_NAMES = ('index.html', 'app.js', 'client.js', 'view.js', 'login.js', 'style.css')
UI_NAMES = tuple(json.loads((ROOT / 'plugin/ui/public_assets.json').read_text()))


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def validate_package_ui(manifest, root):
    version = manifest['plugin_version']
    if not isinstance(version, str) or not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        raise ValueError('invalid plugin version in build manifest')
    assets = manifest['ui_source_sha256']
    names = UI_NAMES if tuple(map(int, version.split('.'))) >= (0, 32, 0) else LEGACY_UI_NAMES
    if set(assets) != set(names) or not all(isinstance(v, str) and re.fullmatch(r'[0-9a-f]{64}', v) for v in assets.values()):
        raise ValueError('invalid public asset inventory in build manifest')
    checkout = re.search(r'^const Version = "([^"]+)"$', (root / 'plugin/quality/types.go').read_text(), re.M)
    if not checkout:
        raise ValueError('cannot determine checkout plugin version')
    if checkout[1] == version:
        for name, expected in assets.items():
            if digest(root / 'plugin/ui' / name) != expected:
                raise ValueError('current UI differs from built package: ' + name)
    # Archived releases have their own asset digests and cache contract.
    return tuple(map(int, version.split('.'))) >= (0, 30, 10)


def verify_public_assets(assets, revalidates, get):
    for name in assets:
        path = '/v0/resource/plugins/model-quality/' + name
        expected = assets[name]
        policy = 'private, no-cache' if revalidates else 'no-store'
        tag = f'W/"{expected}"'
        for previous in (None, '"stale-resource"') if revalidates else (None,):
            status, body, headers = get(path, previous)
            if status != 200 or hashlib.sha256(body).hexdigest() != expected or headers.get('Cache-Control') != policy:
                raise ValueError('served plugin asset differs: ' + name)
            if revalidates:
                types = {'index.html': ('text/html',), 'style.css': ('text/css',)}.get(name, ('application/javascript', 'text/javascript'))
                if headers.get('Content-Type', '').split(';')[0].strip().lower() not in types:
                    raise ValueError('served plugin asset has an unusable content type: ' + name)
                if headers.get('ETag') != tag:
                    raise ValueError('served plugin asset has an unexpected validator: ' + name)
        if revalidates:
            status, body, headers = get(path, tag)
            if status != 304 or body or headers.get('ETag') != tag or headers.get('Cache-Control') != policy:
                raise ValueError('served plugin asset did not revalidate: ' + name)


def source_candidates(directory, excluded_dirs):
    # A custom data volume can live inside a source checkout. Never traverse
    # recognized runtime stores, including partial backups of account security receipts.
    excluded_dirs = excluded_dirs | {'model-quality-data', 'auths', 'plugin-data', 'logs'}
    if directory.name in excluded_dirs:
        return
    for parent, dirs, names in os.walk(directory):
        dirs[:] = [name for name in dirs if name not in excluded_dirs]
        parent = Path(parent)
        if {'quality.db', 'credential-login.db', 'login-receipts.db'}.intersection(names) or (
                parent.name == 'security' and parent.parent.name == 'accounts'):
            dirs.clear()
            continue
        for name in names:
            if name == 'config.yaml':
                continue
            if parent.name == 'management' and (name == 'connection.json' or name.startswith('.connection-')):
                continue
            yield parent / name


def host_sources(root=ROOT):
    baseline = json.loads((root / 'host-patches/baseline.json').read_text())
    names = set(baseline['sha256'])
    for line in (root / 'host-patches/model-quality.patch').read_text().splitlines():
        if line.startswith('+++ b/'):
            names.add(line[6:])
    directory = root / 'workspaces/CLIProxyAPI'
    known = {directory / name for name in names}
    unexpected = sorted(p for p in source_candidates(directory, {'.git'})
                        if p.is_file() and p not in known)
    if unexpected:
        raise ValueError('Host files are missing from the baseline/patch inventory: ' +
                         ', '.join(str(p.relative_to(directory)) for p in unexpected[:8]))
    return [directory / name for name in sorted(names) if (directory / name).is_file()]


def source_files(directory):
    allowed = {'.go', '.mod', '.sum', '.html', '.js', '.css', '.md', '.json', '.yaml', '.yml', '.sh', '.py', '.cjs', '.mjs', '.txt', '.h', '.c', '.patch'}
    excluded = {'__pycache__', '.git', 'auths', 'node_modules', 'plugin-data', '.cache', 'playwright-report', 'test-results'}
    return [p for p in source_candidates(directory, excluded) if not p.is_symlink() and p.is_file()
            and (p.suffix in allowed or p.name in {'LICENSE', 'NOTICE', 'CPA_LICENSE'})]


def snapshot(scope, root=ROOT):
    files = set()
    if scope in {'host', 'native', 'all'}:
        files.update(host_sources(root))
        files.update(root / 'host-patches' / name for name in ('baseline.json', 'model-quality.patch'))
    if scope in {'plugin', 'native', 'all'}:
        files.update(source_files(root / 'plugin'))
    if scope in {'runtime', 'all'}:
        files.update(source_files(root / 'login-runtime'))
        files.update(source_files(root / 'login-runtime-go'))
    if scope in {'ui', 'all'}:
        files.update(root / 'plugin/ui' / name for name in UI_NAMES)
        files.update(root / 'scripts' / name for name in ('verify_ui.cjs', 'test_question_test.mjs', 'build_ui.sh', 'verify_host_ui.cjs', 'verify_login_ui.cjs', 'verify_account_ui.cjs'))
    if scope == 'native':
        files.update(root / 'artifacts' / name for name in ('model-quality.dylib', 'cli-proxy-api-quality'))
        files.update(root / 'scripts' / name for name in ('demo.py', 'verify_native_ui.cjs', 'verify_question_test.py'))
    return {str(p.relative_to(root)): digest(p) for p in sorted(files)}


def require_matching(record, root=ROOT):
    expected = record['input_sha256']
    current = snapshot(record['scope'], root)
    changed = sorted(rel for rel in expected.keys() | current.keys()
                     if expected.get(rel) != current.get(rel))
    if changed:
        raise ValueError('Validated input inventory changed: ' + ', '.join(changed[:8]))
