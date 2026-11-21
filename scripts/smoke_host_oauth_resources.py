"""Verify tagged native assets and receipt lookup on an empty, network-isolated official CPA."""

import argparse
from collections import Counter
from contextlib import contextmanager
import hashlib
from http.client import parse_headers
import io
import json
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import uuid

from release_support import ROOT, UI_NAMES, verify_public_assets
from smoke_credential_policy import CURL, HOST, PREFIX, docker
from verify_host_oauth_qa import checked_library, write_json


RESOURCES = '/v0/resource/plugins/model-quality/'


def asset_hashes(sources):
    def source(name):
        path = 'plugin/ui/' + name
        raw = (ROOT / path).read_bytes()
        if sources[path] != hashlib.sha256(raw).hexdigest():
            raise ValueError('Asset source differs from this QA library: ' + path)
        return raw

    # Independently reconstruct the two-edition page with its static Go template blocks.
    definitions = re.findall(rb'<!--\{\{define "([^"]+)"\}\}-->(.*?)<!--\{\{end\}\}-->',
                             source('host_oauth/accounts.html'), re.S)
    overrides = dict(definitions)
    if not overrides or len(overrides) != len(definitions):
        raise ValueError('Expected distinct host-OAuth template definitions')
    page = source('index.html')
    block = rb'<!--\{\{block "([^"]+)" \.\}\}-->(.*?)<!--\{\{end\}\}-->'
    if set(overrides) - {name for name, _ in re.findall(block, page, re.S)}:
        raise ValueError('Host-OAuth overrides no longer match the shared page')
    rendered = re.sub(block, lambda m: overrides.get(m[1], m[2]), page, flags=re.S)
    if b'<!--{{' in rendered:
        raise ValueError('Unsupported template syntax in static asset verification')
    assets = {name: source(name) for name in UI_NAMES if name not in ('index.html', 'login.js')}
    assets.update({'index.html': rendered, 'login.js': source('host_oauth/login.js')})
    return {name: hashlib.sha256(raw).hexdigest() for name, raw in assets.items()}


def await_registration(get, version):
    until = time.monotonic() + 30
    while time.monotonic() < until:
        try:
            status, raw, _ = get('/v8/management/plugins', auth=True)
            entries = json.loads(raw)['plugins'] if status == 200 else []
            for entry in entries:
                if entry['id'] == 'model-quality' and entry['registered'] and entry['effective_enabled']:
                    assert entry['metadata']['version'] == version
                    return
        except (ValueError, subprocess.CalledProcessError):
            pass
        time.sleep(.2)
    raise AssertionError('Official CPA did not register the expected QA library')


def check_receipts(get):
    errors = {}
    for path in ('/runs/detail', '/rules/commands'):
        route = PREFIX + path + '?idempotency_key=qa-receipt-never-submitted'
        assert get(route)[0] == 401
        status, raw, _ = get(route, auth=True)
        error = json.loads(raw)['error']
        assert status == 404 and error['code'] == 'command_not_accepted'
        assert error['retryable'] is False and error['outcome_unknown'] is False
        errors[path] = dict(http_status=status, code=error['code'], source=error['source'])
    assert json.loads(get(PREFIX + '/rules', auth=True)[1])['items'] == []
    assert json.loads(get(PREFIX + '/runs', auth=True)[1])['total'] == 0
    return dict(authorized_followup_get=errors, unauthenticated_status=401, rule_count=0,
                run_count=0, management_material_created=False)


def check_resources(get, expected):
    assert get(PREFIX + '/login/capabilities')[0] == 401
    for path in ('connection', 'login/accounts'):
        assert get(RESOURCES + path)[0] == 404
    status, raw, headers = get(PREFIX + '/login/capabilities', auth=True)
    assert status == 200 and json.loads(raw) == {'login_mode': 'host_oauth'}
    assert headers.get('Cache-Control') == 'no-store'
    for _ in range(3):
        status, raw, _ = get(PREFIX + '/connection', auth=True)
        assert status == 200 and json.loads(raw)['state'] == 'unconfigured'
    counts = Counter()

    def asset(route, previous):
        status, data, metadata = get(route, previous)
        counts[status] += 1
        return status, data, metadata

    verify_public_assets(expected, True, asset)
    assert counts == {200: 2 * len(expected), 304: len(expected)}
    tag = f'W/"{expected["index.html"]}"'
    for prerequisite, expected_status in ((tag, 412), ('"old-package"', 412), ('*', 304)):
        status, body, headers = get(RESOURCES + 'index.html', tag, prerequisite=prerequisite)
        assert status == expected_status and body == b'' and headers.get('ETag') == tag
    return dict(registered=True, login_mode='host_oauth', unauthenticated_status=401,
                public_business_paths_status=404, ordinary_connection_reads=3, management_material_created=False,
                conditional_asset_responses=dict(counts), verified_assets=len(expected))


@contextmanager
def isolated_native_host(root, library):
    """Start an offline CPA on a scratch volume and expose its GET client."""
    name = 'mq-qa-assets-' + uuid.uuid4().hex[:12]
    key = secrets.token_urlsafe(32)

    def get(route, previous=None, *, auth=False, prerequisite=None):
        args = ['run', '--rm', '--pull=never', '--network', 'container:' + name, '-i', CURL,
                'curl', '-qsS', '--noproxy', '*', '--max-time', '5', '--dump-header', '-', '-w', '\n%{http_code}']
        if previous is not None:
            args.extend(['-H', 'If-None-Match: ' + previous])
        if prerequisite is not None:
            args.extend(['-H', 'If-Match: ' + prerequisite])
        if auth:
            args.extend(['-H', '@-'])
        raw = docker(*args, 'http://127.0.0.1:18379' + route,
                     input=(f'Authorization: Bearer {key}\n'.encode() if auth else b''))
        response, status = raw.rsplit(b'\n', 1)
        header, body = response.split(b'\r\n\r\n', 1)
        metadata = parse_headers(io.BytesIO(header.split(b'\r\n', 1)[1] + b'\r\n\r\n'))
        return int(status), body, metadata

    for directory in ('plugins', 'auths', 'data'):
        (root / directory).mkdir(exist_ok=True)
    (root / 'plugins/model-quality.so').write_bytes(library)
    config = {
        'config-version': 8,
        'server': {'host': '127.0.0.1', 'port': 18379, 'discovery': {'enabled': False},
                   'kv-store': {'path': '/probe/kv.db'}},
        'management': {'secret-key': key, 'disable-control-panel': True, 'disable-auto-update-panel': True},
        'oauth': {'auth-dir': '/probe/auths'},
        'plugins': {'enabled': True, 'dir': '/probe/plugins', 'configs': {
            'model-quality': {'enabled': True, 'data_dir': '/probe/data', 'scheduling_enabled': False}}},
    }
    write_json(root / 'config.yaml', config)
    docker('run', '-d', '--pull=never', '--network', 'none', '--name', name, '--mount',
           f'type=bind,source={root},target=/probe', HOST, './CLIProxyAPI', '--config',
           '/probe/config.yaml', '--standalone', '--local-model')
    try:
        yield name, get
    finally:
        subprocess.run(['docker', 'rm', '-f', name], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def exercise_native_reads(root, library, version, phases, inspect):
    results = {}
    with isolated_native_host(root, library) as (name, get):
        for phase in phases:
            if phase == 'restart':
                docker('restart', name)
            await_registration(get, version)
            results[phase] = inspect(get)
            assert not (root / 'data/management/connection.json').exists()
    return results


def check(output, native, receipt_queries):
    build, library = checked_library(native)
    expected = {} if receipt_queries else asset_hashes(json.loads((native / 'input-sources.json').read_bytes()))
    inspect = check_receipts if receipt_queries else lambda get: check_resources(get, expected)
    with tempfile.TemporaryDirectory(prefix='mq-qa-empty-assets-') as tmp:
        phases = exercise_native_reads(Path(tmp), library, build['plugin_version'],
                                       ('load',) if receipt_queries else ('load', 'restart'), inspect)
    report = dict(build_tag=build['build_tag'], library_sha256=build['library_sha256'], host=HOST,
                  network='none', existing_accounts=0, model_calls=0, live_account_calls=0,
                  source_asset_sha256=expected, phases=phases)
    file = output / ('qa-native-receipt-queries.json' if receipt_queries else 'qa-assets.json')
    write_json(file, report)
    print(file)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path, required=True, help='Directory for verification results')
    parser.add_argument('--native-dir', type=Path, help='QA native build directory; defaults to OUTPUT_DIR/qa-native')
    parser.add_argument('--receipt-queries', action='store_true', help='Verify absent receipts without submitting commands')
    args = parser.parse_args()
    if not __debug__:
        parser.error('QA fixtures require Python assertions')
    out = args.output_dir.resolve()
    out.mkdir(parents=True, exist_ok=True)
    check(out, args.native_dir.resolve() if args.native_dir else out / 'qa-native', args.receipt_queries)
