#!/usr/bin/env python3
"""Build one official-CPA native plugin with its Go/Sobek runtime embedded."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME_VERSION = '2.2.1'
PLUGIN_VERSION = '0.32.2'
ENGINE = 'go_sobek_protocol'
MODULE = 'github.com/router-for-me/CLIProxyAPI/v8'


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run(command, **kwargs):
    return subprocess.run(command, check=True, **kwargs)


def native_target(go):
    target = json.loads(run([go, 'env', '-json', 'GOOS', 'GOARCH', 'CGO_ENABLED'], capture_output=True).stdout)
    actual_os = {'Darwin': 'darwin', 'Linux': 'linux', 'FreeBSD': 'freebsd', 'Windows': 'windows'}.get(platform.system())
    actual_arch = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64', 'AMD64': 'amd64'}.get(platform.machine())
    if target['GOOS'] != actual_os or target['GOARCH'] != actual_arch or target['CGO_ENABLED'] != '1':
        raise ValueError('build on the target OS/architecture with CGO_ENABLED=1 and a native C toolchain')
    if target['GOOS'] not in ('darwin', 'linux') or target['GOARCH'] not in ('arm64', 'amd64'):
        raise ValueError('this plugin login launcher currently supports only macOS/Linux on arm64/amd64')
    return target['GOOS'], target['GOARCH']


def verify_sdk(go):
    module = json.loads(run([go, 'mod', 'edit', '-json'], cwd=ROOT / 'plugin', capture_output=True).stdout)
    replacements = [item['New']['Path'] for item in module.get('Replace', []) if item['Old']['Path'] == MODULE]
    expected = (ROOT / 'workspaces/CLIProxyAPI-official').resolve()
    if len(replacements) != 1 or (ROOT / 'plugin' / replacements[0]).resolve() != expected or not (expected / 'go.mod').is_file():
        raise ValueError('plugin SDK must reference the unchanged workspaces/CLIProxyAPI-official checkout')
    baseline_path = ROOT / 'host-patches/official-8.0.76-baseline.json'
    baseline = json.loads(baseline_path.read_bytes())
    files = baseline['sha256']
    changed = [name for name, checksum in files.items()
               if not (expected / name).is_file() or digest(expected / name) != checksum]
    actual = {path.relative_to(expected).as_posix() for path in expected.rglob('*') if path.is_file()}
    if changed or actual != set(files):
        raise ValueError('official CPA source differs from its recorded source-only baseline')
    return {'source_file_count': len(files), 'baseline_manifest_sha256': digest(baseline_path)}


def verify_runtime(bundle, target_os, target_arch):
    manifest_path = bundle / 'manifest.json'
    manifest = json.loads(manifest_path.read_bytes())
    expected_entrypoint = 'bin/login-runtime' + ('.exe' if target_os == 'windows' else '')
    for key, value in {'schema_version': 2, 'runtime_version': RUNTIME_VERSION, 'engine': ENGINE, 'platform': target_os, 'arch': target_arch, 'entrypoint': expected_entrypoint}.items():
        if manifest.get(key) != value:
            raise ValueError(f'runtime manifest {key} does not match this plugin')
    actual_files = {p.relative_to(bundle).as_posix() for p in bundle.rglob('*') if p.is_file()}
    expected_files = set(manifest['files']) | {'manifest.json'}
    if actual_files != expected_files:
        raise ValueError('runtime manifest has missing or unexpected files')
    for name, checksum in manifest['files'].items():
        if not re.fullmatch(r'[A-Za-z0-9._@+/-]+', name) or any(part in ('', '.', '..') for part in name.split('/')):
            raise ValueError('runtime manifest contains an unsafe filename')
        path = bundle / name
        if path.is_symlink() or not path.is_file() or digest(path) != checksum:
            raise ValueError(f'runtime checksum mismatch: {name}')
    if any(path.is_symlink() for path in bundle.rglob('*')):
        raise ValueError('runtime bundle cannot contain symbolic links')
    executable = bundle / expected_entrypoint
    with tempfile.TemporaryDirectory(prefix='mq-native-selftest-') as private:
        env = {'PATH': '', 'HOME': private, 'TMPDIR': private, 'TEMP': private, 'TMP': private}
        for variable in ('SystemRoot', 'WINDIR'):
            if variable in os.environ:
                env[variable] = os.environ[variable]
        result = run([str(executable), '--self-test'], capture_output=True, timeout=20, env=env)
    probe = json.loads(result.stdout)
    if result.stderr or probe.get('engine') != ENGINE or probe.get('runtime_version') != RUNTIME_VERSION or probe.get('platform') != target_os or probe.get('arch') != target_arch or probe.get('browser_required') is not False or probe.get('node_required') is not False:
        raise ValueError('runtime self-test failed without external interpreters')
    return manifest_path


def verify_local_runtime_sources(bundle):
    provenance = json.loads((bundle / 'SOURCE_MANIFEST.json').read_bytes())
    source = ROOT / 'login-runtime-go'
    changed = [name for name, checksum in provenance.get('source_sha256', {}).items()
               if not (source / name).is_file() or digest(source / name) != checksum]
    if not provenance.get('source_sha256') or changed:
        raise ValueError('native runtime sources changed; build a fresh directory with --runtime-bundle instead of reusing the existing artifact')


def replace_if_changed(staged, destination):
    if destination.is_file() and digest(staged) == digest(destination):
        staged.unlink()
    else:
        os.replace(staged, destination)


def embed_runtime(bundle, target_os, target_arch, go):
    manifest_path = verify_runtime(bundle, target_os, target_arch)
    package = ROOT / 'plugin/loginbundle'
    payload = package / 'payload/runtime.tar.gz'
    payload.parent.mkdir(parents=True, exist_ok=True)
    # Only regular-file entries are emitted; extraction creates private directories.
    with tempfile.TemporaryFile() as raw:
        with tarfile.open(fileobj=raw, mode='w', format=tarfile.USTAR_FORMAT) as archive:
            for file in sorted(bundle.rglob('*')):
                if not file.is_file():
                    continue
                data = file.read_bytes()
                header = tarfile.TarInfo(file.relative_to(bundle).as_posix())
                header.size = len(data)
                header.mode = 0o600
                header.uid = header.gid = header.mtime = 0
                archive.addfile(header, io.BytesIO(data))
        raw.seek(0)
        with tempfile.NamedTemporaryFile(prefix='.runtime-', dir=payload.parent, delete=False) as output:
            staged_payload = Path(output.name)
            try:
                with gzip.GzipFile(filename='', fileobj=output, mode='wb', mtime=0, compresslevel=9) as compressed:
                    while chunk := raw.read(1 << 20):
                        compressed.write(chunk)
                output.flush()
                os.fsync(output.fileno())
            except BaseException:
                staged_payload.unlink(missing_ok=True)
                raise
        replace_if_changed(staged_payload, payload)
    checksum = digest(payload)
    generated = f'''//go:build loginbundle_embedded

// Code generated by scripts/build_official_plugin.py; DO NOT EDIT.
package loginbundle

var generatedSpec = bundleSpec{{
    sha256: "{checksum}",
    manifestSHA256: "{digest(manifest_path)}",
    version: "{RUNTIME_VERSION}",
    platform: "{target_os}",
    arch: "{target_arch}",
}}
'''
    formatted = run([str(Path(go).with_name('gofmt'))], input=generated.encode(), capture_output=True).stdout
    with tempfile.NamedTemporaryFile(prefix='.bundle-metadata-', dir=package, delete=False) as output:
        staged_metadata = Path(output.name)
        output.write(formatted)
    replace_if_changed(staged_metadata, package / 'bundle_metadata_generated.go')
    return payload, checksum


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', default='/usr/local/go/bin/go')
    parser.add_argument('--runtime-bundle', type=Path)
    parser.add_argument('--output', type=Path, default=ROOT / 'artifacts/official-plugin')
    parser.add_argument('--embed-only', action='store_true', help='prepare verified embedded files without compiling the plugin')
    args = parser.parse_args()
    target_os, target_arch = native_target(args.go)
    sdk = verify_sdk(args.go)
    bundle = (args.runtime_bundle or ROOT / f'artifacts/login-go-{RUNTIME_VERSION}/login-runtime-{target_os}-{target_arch}').resolve()
    if not bundle.exists():
        run([sys.executable, str(ROOT / 'login-runtime-go/generate_manifest.py')])
        run([sys.executable, str(ROOT / 'login-runtime-go/build.py'), '--go', args.go, '--output', str(bundle)])
    if args.runtime_bundle is None:
        verify_local_runtime_sources(bundle)
    payload, checksum = embed_runtime(bundle, target_os, target_arch, args.go)
    report = {'plugin_version': PLUGIN_VERSION, 'runtime_version': RUNTIME_VERSION, 'engine': ENGINE, 'platform': target_os, 'arch': target_arch, 'runtime_manifest': str(bundle / 'manifest.json'), 'embedded_archive_sha256': checksum, 'embedded_bytes': payload.stat().st_size, 'official_sdk': sdk}
    if not args.embed_only:
        output = args.output.resolve()
        output.mkdir(parents=True, exist_ok=True)
        extension = {'darwin': 'dylib', 'windows': 'dll'}.get(target_os, 'so')
        library = output / f'model-quality.{extension}'
        run([args.go, 'build', '-tags', 'loginbundle_embedded', '-buildmode=c-shared', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', '-o', str(library), './cmd/model-quality'], cwd=ROOT / 'plugin')
        archive = output / f'model-quality-v{PLUGIN_VERSION}-{target_os}-{target_arch}.zip'
        with zipfile.ZipFile(archive, mode='w', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as package:
            package.write(library, arcname=library.name)
        report.update(library=str(library), library_sha256=digest(library), library_bytes=library.stat().st_size, archive=str(archive), archive_sha256=digest(archive), archive_bytes=archive.stat().st_size)
        (output / 'build-manifest.json').write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
        (output / 'SHA256SUMS').write_text(f'{report["library_sha256"]}  {library.name}\n{report["archive_sha256"]}  {archive.name}\n')
    print(json.dumps(report, sort_keys=True))


if __name__ == '__main__':
    main()
