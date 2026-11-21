"""Verify a partial quality-store restore and recovery in a scratch official CPA."""

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

from release_support import ROOT, digest
from smoke_credential_policy import HOST, PREFIX, docker
from smoke_host_oauth_resources import await_registration, isolated_native_host
from verify_host_oauth_qa import checked_library, image_id, write_json


def healthy(get, version):
    await_registration(get, version)
    status, raw, _ = get(PREFIX + '/rules', auth=True)
    assert status == 200 and json.loads(raw)['items'] == []
    connection_status, raw, _ = get(PREFIX + '/connection', auth=True)
    assert connection_status == 200 and json.loads(raw)['state'] == 'unconfigured'
    return dict(registered=True, effective_enabled=True, version=version, rules_status=status,
                connection_status=connection_status, rule_count=0)


def observe_partial_store(name, get, zero_byte):
    started_at = docker('inspect', '--format', '{{.State.StartedAt}}', name).decode().strip()
    expected = (b'incomplete quality store: existing file is empty' if zero_byte
                else b'incomplete quality store: missing required bucket idempotency')
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            status, raw, _ = get('/v8/management/plugins', auth=True)
            if status == 200:
                entry = next((x for x in json.loads(raw)['plugins'] if x['id'] == 'model-quality'), None)
                if entry and entry['configured'] and entry['enabled']:
                    # The host first discovers the file, then attempts registration.
                    # A temporary registered=false during discovery proves nothing.
                    logs = subprocess.check_output(['docker', 'logs', '--since', started_at, '--tail', '200', name],
                                                   stderr=subprocess.STDOUT)
                    rejected = any(b'plugin model-quality plugin.register failed' in line and expected in line
                                   for line in logs.splitlines())
                    if rejected or entry['registered']:
                        return dict(registry_status=status, configured=True, enabled=True, registered=entry['registered'],
                                    effective_enabled=entry['effective_enabled'], missing_receipt_bucket_reported=rejected and not zero_byte,
                                    zero_byte_file_reported=rejected and zero_byte,
                                    rules_status=get(PREFIX + '/rules', auth=True)[0],
                                    connection_status=get(PREFIX + '/connection', auth=True)[0])
        except (ValueError, subprocess.CalledProcessError):
            pass
        time.sleep(.2)
    raise AssertionError('No conclusive registration outcome on the restored scratch store')


def stop(name):
    docker('stop', '--time', '10', name)
    if docker('inspect', '--format', '{{.State.Status}}', name).strip() != b'exited':
        raise RuntimeError('Scratch CPA still running during cold storage replacement')


def check(native, output, zero_byte=False):
    build, library = checked_library(native)
    if build['target_image_id'] != image_id(HOST):
        raise ValueError('QA library and official CPA image do not match')
    version = build['plugin_version']
    with tempfile.TemporaryDirectory(prefix='mq-qa-quality-restore-') as tmp:
        scratch = Path(tmp)
        root = scratch / 'host'
        root.mkdir(mode=0o700)
        damaged = scratch / 'incomplete.db'
        if zero_byte:
            damaged.touch(mode=0o600, exist_ok=False)
        else:
            # This helper only creates a previously nonexistent file in our private scratch directory.
            subprocess.run(['go', '-C', str(ROOT / 'plugin'), 'run', '-mod=readonly',
                            str(ROOT / 'scripts/fixtures/incomplete_quality_store/main.go'), str(damaged)],
                           env={**os.environ, 'GOPROXY': 'off', 'GOSUMDB': 'off'}, check=True)
        original_damaged = digest(damaged)
        original_bytes = damaged.stat().st_size
        healthy_backup = scratch / 'healthy.db'
        db = root / 'data/quality.db'
        get_attempts = Counter()
        get_responses = Counter()
        with isolated_native_host(root, library) as (name, read):
            def get(path, *, auth=False):
                get_attempts[path] += 1
                result = read(path, auth=auth)
                get_responses[result[0]] += 1
                return result

            initial = healthy(get, version)
            stop(name)
            shutil.copy2(db, healthy_backup)
            original_healthy = digest(db)
            assert digest(healthy_backup) == original_healthy
            damaged.replace(db)
            assert digest(db) == original_damaged
            docker('start', name)
            partial = observe_partial_store(name, get, zero_byte)
            stop(name)
            partial['bytes_before'], partial['bytes_after'] = original_bytes, db.stat().st_size
            partial['store_sha256_before'], partial['store_sha256_after'] = original_damaged, digest(db)
            partial['store_bytes_unchanged'] = partial['store_sha256_after'] == original_damaged
            healthy_backup.replace(db)
            assert digest(db) == original_healthy
            docker('start', name)
            restored = healthy(get, version)
            stop(name)
            assert not (root / 'data/management/connection.json').exists()
            assert not any((root / 'auths').iterdir())

    expected_report = 'zero_byte_file_reported' if zero_byte else 'missing_receipt_bucket_reported'
    passed = (not partial['registered'] and not partial['effective_enabled'] and
              partial[expected_report] and partial['store_bytes_unchanged'] and
              partial['rules_status'] == 404 and partial['connection_status'] == 404)
    report = dict(passed=passed, host=HOST, image_id=build['target_image_id'], build_tag=build['build_tag'],
                  scenario='zero_byte_store' if zero_byte else 'missing_receipt_bucket',
                  library_sha256=build['library_sha256'], network='none', request_methods=['GET'],
                  authorized_mutations=0, existing_accounts=0, model_calls=0, real_account_calls=0,
                  get_attempts=dict(get_attempts), get_response_statuses=dict(get_responses), cold_backup_sha256=original_healthy,
                  initial=initial, partial=partial, restored=restored, restored_from_saved_bytes=True,
                  management_material_created=False)
    output.parent.mkdir(parents=True, exist_ok=True)
    write_json(output, report)
    if not passed:
        raise SystemExit('Partial-store integrity check failed; observed registration, route status and byte comparison in ' + str(output))
    print(output)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--native-dir', type=Path, required=True, help='Verified Linux arm64 host-OAuth QA build')
    parser.add_argument('--output', type=Path, required=True, help='Path for the sanitized result JSON')
    parser.add_argument('--zero-byte', action='store_true', help='Restore a zero-byte file in place of the missing-bucket fixture')
    args = parser.parse_args()
    if not __debug__:
        parser.error('QA fixtures require Python assertions')
    check(args.native_dir.resolve(), args.output.resolve(), args.zero_byte)
