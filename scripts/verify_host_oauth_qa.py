"""Build the offline Linux arm64 host-OAuth QA library and run isolated official CPA fixtures."""

import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
import zipfile

from release_support import ROOT, UI_NAMES, digest
from smoke_credential_policy import CURL as GO_IMAGE, HOST as CPA_IMAGE


TAG = 'credential_ops_host_oauth'
OFFICIAL = Path('workspaces/CLIProxyAPI-official')
SCENARIOS = {'scheduled-addition': 'cron', 'manual-readmission': 'manual',
             'quality-operations': 'quality', 'interrupt-status': 'unknown'}
BUILD = """set -eu
go version | tee /out/go-version.txt
test "$(go env GOOS)/$(go env GOARCH)/$(go env CGO_ENABLED)" = linux/arm64/1
go list -mod=readonly -tags "$QA_TAG" -deps ./cmd/model-quality > /out/linked-packages.txt
if grep -E '^github.com/(cliproxyapi-plugins/model-quality/login|cli-proxy-api/model-quality-login-runtime)' /out/linked-packages.txt; then exit 11; fi
go build -mod=readonly -tags "$QA_TAG" -buildmode=c-shared -trimpath -buildvcs=false -ldflags='-s -w' -o /out/model-quality.so ./cmd/model-quality
readelf -h /out/model-quality.so
go version -m /out/model-quality.so
"""


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def image_id(name):
    return subprocess.check_output(['docker', 'image', 'inspect', '--format', '{{.Id}}', name], text=True).strip()


def checked_library(native):
    info = json.loads((native / 'qa-build.json').read_bytes())
    if (info.get('kind'), info.get('build_tag'), info.get('platform'), info.get('embedded_login_runtime')) != (
            'QA-only', TAG, 'linux/arm64', False):
        raise ValueError('Expected the Linux arm64 host-OAuth QA build')
    library = (native / 'model-quality.so').read_bytes()
    if (hashlib.sha256(library).hexdigest() != info['library_sha256'] or
            digest(native / 'input-sources.json') != info['input_manifest_sha256']):
        raise ValueError('QA library or source inventory differs from its build manifest')
    return info, library


def build_inputs():
    ui = Path('plugin/ui')
    entry = Path('plugin/cmd/model-quality')
    if any(Path(name).name != name or Path(name).suffix not in {'.html', '.css', '.js'} for name in UI_NAMES):
        raise ValueError('Unexpected public asset path')
    embeds = re.search(r'^//go:embed (.*)$', (ROOT / ui / 'embed_host_oauth.go').read_text(), re.M)[1].split()
    expected = (set(UI_NAMES) - {'login.js'}) | {'host_oauth/accounts.html', 'host_oauth/login.js'}
    if set(embeds) != expected:
        raise ValueError('Tagged go:embed inputs differ from the public asset inventory')
    result = [Path('plugin/go.mod'), Path('plugin/go.sum'), ui / 'embed_host_oauth.go', *(ui / f for f in embeds)]
    for directory, pattern in ((Path('plugin/quality'), '*.go'), (Path('plugin/quality/questiondata'), '*.txt'),
                               (entry, '*.go')):
        result.extend(path.relative_to(ROOT) for path in (ROOT / directory).glob(pattern)
                      if not path.name.endswith('_test.go') and path.name != 'application.go')
    result.extend(OFFICIAL / name for name in (
        'go.mod', 'go.sum', 'sdk/pluginapi/types.go', 'sdk/pluginabi/types.go', 'sdk/proxyutil/proxy.go'))
    return sorted(set(result))


def build(output, downloads, baseline):
    if not downloads.is_dir():
        raise ValueError('An existing offline Go module download cache is required')
    if baseline is None:
        manifests = sorted((ROOT / 'host-patches').glob('official-*-baseline.json'))
        if len(manifests) != 1:
            raise ValueError('Use --baseline to select the official host source manifest')
        baseline = manifests[0]
    host_record = json.loads(baseline.read_bytes())
    host_version = re.search(r'\d+\.\d+\.\d+', host_record['version'])[0]
    native = output / 'qa-native'
    with (output / 'qa-build.log').open('w') as log, tempfile.TemporaryDirectory(dir=output, prefix='.qa-build-') as tmp:
        subprocess.run([sys.executable, 'scripts/verify_baseline.py', '--manifest', str(baseline),
                        '--source', str(OFFICIAL)], cwd=ROOT, check=True, stdout=log, stderr=subprocess.STDOUT)
        stage = Path(tmp)
        src = stage / 'src'
        out = stage / 'out'
        out.mkdir()
        sources = {}
        for path in build_inputs():
            file = ROOT / path
            if file.resolve() != file or not file.is_file():
                raise ValueError('Expected a regular source file: ' + str(path))
            data = file.read_bytes()
            checksum = hashlib.sha256(data).hexdigest()
            if path.is_relative_to(OFFICIAL) and checksum != host_record['sha256'][str(path.relative_to(OFFICIAL))]:
                raise ValueError('Public SDK input changed after baseline verification: ' + str(path))
            (src / path).parent.mkdir(parents=True, exist_ok=True)
            (src / path).write_bytes(data)
            sources[str(path)] = checksum
        subprocess.run(['docker', 'run', '--rm', '--platform', 'linux/arm64', '--network', 'none', '--pull=never',
                        '--mount', f'type=bind,source={src},target=/src,readonly',
                        '--mount', f'type=bind,source={downloads},target=/offline,readonly',
                        '--mount', f'type=bind,source={out},target=/out',
                        '-v', 'model-quality-qa-arm64-modcache:/gomods', '-v', 'model-quality-qa-arm64-gocache:/gocache',
                        '-e', 'GOMODCACHE=/gomods', '-e', 'GOCACHE=/gocache', '-e', 'GOPROXY=file:///offline',
                        '-e', 'GOSUMDB=off', '-e', 'GOTOOLCHAIN=local', '-e', 'CGO_ENABLED=1', '-e', 'QA_TAG=' + TAG,
                        '-w', '/src/plugin', GO_IMAGE, 'sh', '-c', BUILD], check=True, stdout=log, stderr=subprocess.STDOUT)
        write_json(out / 'input-sources.json', sources)
        so = out / 'model-quality.so'
        version = re.search(r'^const Version = "([\d.]+)"$', (src / 'plugin/quality/types.go').read_text(), re.M)[1]
        info = dict(kind='QA-only', build_tag=TAG, embedded_login_runtime=False, network='none', platform='linux/arm64',
                    plugin_version=version, host_version=host_version, go_version=(out / 'go-version.txt').read_text().split()[2],
                    official_baseline_files=len(host_record['sha256']), baseline_manifest_sha256=digest(baseline),
                    go_image_id=image_id(GO_IMAGE), target_image_id=image_id(CPA_IMAGE), input_count=len(sources),
                    input_manifest_sha256=digest(out / 'input-sources.json'), library_sha256=digest(so),
                    library_bytes=so.stat().st_size, dependency_count=len((out / 'linked-packages.txt').read_text().splitlines()))
        write_json(out / 'qa-build.json', info)
        (out / 'model-quality.h').unlink()
        (out / 'go-version.txt').unlink()
        # Publish the verified library, sources and manifest as one directory.
        # Repeated verification in one report directory can reuse identical bytes.
        if native.exists():
            prior, _ = checked_library(native)
            if (prior['input_manifest_sha256'], prior['library_sha256']) != (
                    info['input_manifest_sha256'], info['library_sha256']):
                raise ValueError('This report directory contains a different QA build; select another --output-dir')
        else:
            out.rename(native)
        print(native / 'model-quality.so', info['library_sha256'])


def fixtures(output, native, scenarios):
    info, library = checked_library(native)
    with tempfile.TemporaryDirectory(prefix='mq-qa-fixtures-') as tmp:
        package = Path(tmp)
        archive = package / f'model-quality-v{info["plugin_version"]}-linux-arm64.zip'
        member = zipfile.ZipInfo('model-quality.so')
        member.create_system = 3
        member.external_attr = 0o100755 << 16
        with zipfile.ZipFile(archive, 'w') as f:
            f.writestr(member, library, compress_type=zipfile.ZIP_DEFLATED, compresslevel=6)
        checksum = digest(archive)
        write_json(package / 'build-manifest.json',
                   dict(plugin_version=info['plugin_version'], qa_variant=TAG, embedded_login_runtime=False,
                        checksums={archive.name: checksum, 'model-quality.so': info['library_sha256']}))

        def run(case):
            prefix = 'qa-' + SCENARIOS[case]
            start = time.monotonic()
            report = output / (prefix + '.json')
            with tempfile.TemporaryDirectory(dir=output, prefix='.' + prefix + '-') as tmp_result:
                staged = Path(tmp_result) / report.name
                with (output / (prefix + '.log')).open('w') as log:
                    result = subprocess.run([sys.executable, '-B', 'scripts/smoke_credential_policy.py',
                                             str(package), str(staged), '--' + case], cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
                if result.returncode == 0:
                    value = json.loads(staged.read_bytes())
                    assert value['network'] == 'none' and value['package_sha256'] == checksum and value['live_account_calls'] == 0
                    os.replace(staged, report)
            return case, dict(exit_code=result.returncode, duration_seconds=round(time.monotonic() - start, 3),
                              report=report.name if result.returncode == 0 else None)

        with concurrent.futures.ThreadPoolExecutor(max_workers=min(3, len(scenarios))) as pool:
            results = dict(pool.map(run, scenarios))
        summary = dict(build_tag=TAG, embedded_login_runtime=False, library_sha256=info['library_sha256'],
                       package_sha256=checksum, plugin_version=info['plugin_version'], network='none',
                       target_image_id=image_id(CPA_IMAGE), release_build=False, local_deploy=False, scenarios=results)
        write_json(output / 'qa-integration.json', summary)
        print(output / 'qa-integration.json')
    return any(record['exit_code'] for record in results.values())


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    builder = commands.add_parser('build', help='Build from allowlisted source and the local Go module cache')
    builder.add_argument('--output-dir', required=True, type=Path, help='Report directory; the QA library goes in qa-native/')
    cache = Path(os.environ.get('GOMODCACHE', Path.home() / 'go/pkg/mod')) / 'cache/download'
    builder.add_argument('--offline-downloads', type=Path, default=cache, help='Go module cache/download directory')
    builder.add_argument('--baseline', type=Path, help='Official host source manifest; defaults to the unique official baseline')
    suite = commands.add_parser('fixtures', help='Run the existing fake-credential fixtures on the native library')
    suite.add_argument('--output-dir', required=True, type=Path, help='Directory for scenario reports and logs')
    suite.add_argument('--native-dir', type=Path, help='QA build directory; defaults to OUTPUT_DIR/qa-native')
    suite.add_argument('scenarios', nargs='*', metavar='SCENARIO', help=', '.join(SCENARIOS))
    args = parser.parse_args()
    if not __debug__:
        parser.error('QA fixtures require Python assertions')
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if args.command == 'build':
        build(output, args.offline_downloads.resolve(), args.baseline.resolve() if args.baseline else None)
    else:
        selected = args.scenarios or list(SCENARIOS)
        if set(selected) - set(SCENARIOS) or len(selected) != len(set(selected)):
            parser.error('Unknown or duplicate fixture')
        sys.exit(fixtures(output, args.native_dir.resolve() if args.native_dir else output / 'qa-native', selected))
