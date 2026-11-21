"""Verify the account management read routes on an offline official CPA with an inert test payload."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

from fixtures.native_account_query_checks import check_account_queries
from release_support import ROOT, UI_NAMES, digest, verify_public_assets
from smoke_host_oauth_resources import PREFIX, exercise_native_reads
from verify_host_oauth_qa import CPA_IMAGE, GO_IMAGE, OFFICIAL, build_inputs, image_id, write_json


def stage_inert_bundle(package):
    # The dummy file has no executable format. Its test-only version also fails
    # the login service's runtime allowlist. Never load a protocol engine here.
    entry = 'bin/login-runtime'
    inert = b'Inert account query test data.\n'
    version = 'fixture-only'
    manifest = json.dumps(dict(schema_version=2, runtime_version=version, engine='go_sobek_protocol',
                               platform='linux', arch='arm64', entrypoint=entry,
                               files={entry: hashlib.sha256(inert).hexdigest()})).encode()
    uncompressed = io.BytesIO()
    with tarfile.open(fileobj=uncompressed, mode='w') as archive:
        for path, data, mode in [('manifest.json', manifest, 0o600), (entry, inert, 0o700)]:
            item = tarfile.TarInfo(path)
            item.size, item.mode = len(data), mode
            archive.addfile(item, io.BytesIO(data))
    archive = gzip.compress(uncompressed.getvalue(), mtime=0)
    (package / 'payload').mkdir()
    (package / 'payload/runtime.tar.gz').write_bytes(archive)
    (package / 'generated_fixture.go').write_text(
        'package loginbundle\n\nvar generatedSpec = bundleSpec{\n'
        f'version: "{version}", platform: "linux", arch: "arm64",\n'
        f'manifestSHA256: "{hashlib.sha256(manifest).hexdigest()}", sha256: "{hashlib.sha256(archive).hexdigest()}",\n'
        '}\n')
    return hashlib.sha256(archive).hexdigest()


def account_sources():
    # Reuse the baseline SDK and quality-engine inventory from the host-OAuth
    # fixture. This fixture selects the standard native entry and its own assets.
    sources = {p for p in build_inputs() if not p.is_relative_to('plugin/ui')
               and p.name != 'application_host_oauth.go'}
    sources.add(Path('plugin/cmd/model-quality/application.go'))
    sources.add(Path('plugin/ui/embed.go'))
    sources.update(Path('plugin/ui') / f for f in UI_NAMES)
    sources.update(Path('plugin/loginbundle') / name for name in ('bundle.go', 'embedded.go'))
    for package in ('login', 'login/account', 'login/conversion', 'login/oauth'):
        sources.update(path.relative_to(ROOT) for path in (ROOT / 'plugin' / package).glob('*.go')
                       if not path.name.endswith('_test.go'))
    return sorted(sources)


def check(output, downloads):
    baseline = ROOT / 'host-patches/official-8.0.76-baseline.json'
    subprocess.run(['python3', 'scripts/verify_baseline.py', '--manifest', str(baseline), '--source', str(OFFICIAL)],
                   cwd=ROOT, check=True)
    official_hashes = json.loads(baseline.read_bytes())['sha256']
    with tempfile.TemporaryDirectory(prefix='mq-offline-account-reads-') as temp:
        stage = Path(temp)
        src, out, volume = stage / 'src', stage / 'out', stage / 'volume'
        out.mkdir()
        (volume / 'data').mkdir(parents=True)
        inputs = {}
        for rel in account_sources():
            original = ROOT / rel
            if original.resolve() != original or not original.is_file():
                raise ValueError('Unexpected fixture source: ' + str(rel))
            data = original.read_bytes()
            checksum = hashlib.sha256(data).hexdigest()
            if rel.is_relative_to(OFFICIAL) and checksum != official_hashes[str(rel.relative_to(OFFICIAL))]:
                raise ValueError('Official SDK input differs from its baseline: ' + str(rel))
            inputs[str(rel)] = checksum
            (src / rel).parent.mkdir(parents=True, exist_ok=True)
            (src / rel).write_bytes(data)
        bundle_digest = stage_inert_bundle(src / 'plugin/loginbundle')
        seed = ROOT / 'scripts/fixtures/native_account_query_seed_test.go'
        seed_bytes = seed.read_bytes()
        (src / 'plugin/login/native_account_query_seed_test.go').write_bytes(seed_bytes)
        env = dict(GOPROXY='file:///offline', GOSUMDB='off', GOTOOLCHAIN='local', CGO_ENABLED='1',
                   GOMODCACHE='/gomods', GOCACHE='/gocache')
        flags = [opt for k, v in env.items() for opt in ('-e', k + '=' + v)]
        command = ('set -eu\n'
                   'go test -mod=readonly -tags loginbundle_embedded -v -count=1 -run "^TestSeedNativeAccountQuery$" ./login\n'
                   'test -s /query-storage/accounts/credential-login.db\n'
                   'go build -mod=readonly -tags loginbundle_embedded -trimpath -buildvcs=false -buildmode=c-shared '
                   '-o /out/model-quality.so ./cmd/model-quality')
        subprocess.run(['docker', 'run', '--rm', '--platform', 'linux/arm64', '--network', 'none', '--pull=never',
                        '--mount', f'type=bind,source={downloads},target=/offline,readonly',
                        '--mount', f'type=bind,source={src},target=/src,readonly',
                        '--mount', f'type=bind,source={out},target=/out',
                        '--mount', f'type=bind,source={volume / "data"},target=/query-storage',
                        '-v', 'model-quality-qa-arm64-modcache:/gomods',
                        '-v', 'model-quality-qa-arm64-gocache:/gocache', *flags,
                        '-w', '/src/plugin', GO_IMAGE, 'sh', '-c', command], check=True)
        library = (out / 'model-quality.so').read_bytes()
        version = re.search(r'^const Version = "([\d.]+)"$', (src / 'plugin/quality/types.go').read_text(), re.M)[1]
        assets = {name: inputs['plugin/ui/' + name] for name in UI_NAMES}
        previous_inspection = None

        def inspect(get):
            nonlocal previous_inspection
            result = check_account_queries(get, previous_inspection)
            previous_inspection = result['inspection']
            status, raw, _ = get(PREFIX + '/login/capabilities', auth=True)
            assert status == 200
            caps = json.loads(raw)
            assert not caps['account_login_available'] and not any(caps['security_actions'].values())
            assert caps['methods']['password_totp'] == {'available': False, 'reason': 'runtime_invalid'}
            verify_public_assets(assets, True, get)
            return dict(result, verified_assets=len(assets), runtime_available=False)

        phases = exercise_native_reads(volume, library, version, ('load', 'restart'), inspect)
        report = dict(kind='account-read-QA-only', platform='linux/arm64',
                      host=CPA_IMAGE, target_image_id=image_id(CPA_IMAGE), go_image_id=image_id(GO_IMAGE),
                      plugin_version=version, network='none', real_account_actions=0,
                      model_calls=0, protocol_engine_embedded=False, fixture_archive_sha256=bundle_digest,
                      seed_source_sha256=hashlib.sha256(seed_bytes).hexdigest(),
                      query_checks_sha256=digest(ROOT / 'scripts/fixtures/native_account_query_checks.py'),
                      library_sha256=hashlib.sha256(library).hexdigest(),
                      api_methods=['GET'], source_sha256=inputs, phases=phases)
    write_json(output, report)
    print(output)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('report', type=Path, help='Report file in an existing directory')
    parser.add_argument('--offline-downloads', type=Path,
                        default=Path(os.environ.get('QA_OFFLINE_DOWNLOADS', Path.home() / 'go/pkg/mod/cache/download')))
    args = parser.parse_args()
    if not __debug__ or not args.offline_downloads.is_dir():
        parser.error('Enable Python assertions and supply an existing offline Go module cache')
    check(args.report.resolve(), args.offline_downloads.resolve())
