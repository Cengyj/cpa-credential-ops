#!/usr/bin/env python3
"""Replace only the local authorized CPA plugin after a stopped-instance backup.

Public-asset checks never authenticate management, start a login, or call a model.
Private backups and stable database snapshots stay outside the workspace. A failed
upgrade restores only the previous library and restarts the original container.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
from datetime import datetime
from urllib.error import HTTPError, URLError
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener
from zoneinfo import ZoneInfo
from release_support import UI_NAMES as NAMES, validate_package_ui, verify_public_assets

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / 'artifacts/docker-8.0.76/runtime'
STORE_NAME = 'credential-login.db'
ACCOUNT_STORES = (STORE_NAME, 'login-receipts.db')
CPA_KV_NAME = 'kv.db'


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


HTTP = build_opener(ProxyHandler({}), NoRedirect())


def digest(data):
    return hashlib.sha256(data).hexdigest()


def docker(*args):
    return subprocess.check_output(['docker', *args], stderr=subprocess.PIPE)


def inventory(directory):
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError('expected a regular runtime directory')
    result = {}
    for p in sorted(directory.rglob('*')):
        if p.is_symlink():
            raise ValueError('runtime backup contains a symbolic link')
        if p.is_file():
            result[digest(p.relative_to(directory).as_posix().encode())] = digest(p.read_bytes())
    return result


def get(path, etag=None):
    request = Request('http://127.0.0.1:8317' + path, headers={'If-None-Match': etag} if etag else {})
    try:
        response = HTTP.open(request, timeout=3)
    except HTTPError as error:
        response = error
    with response:
        return response.status, response.read(), response.headers


def validate_container(container):
    if not container['State']['Running']:
        raise ValueError('expected the existing CPA container to be running')
    expected = {'/CLIProxyAPI/config': 'config', '/root/.cli-proxy-api': 'auths',
                '/CLIProxyAPI/plugins': 'plugins', '/CLIProxyAPI/plugin-data': 'plugin-data'}
    mounts = container['Mounts']
    for destination, relative in expected.items():
        rows = [row for row in mounts if row['Destination'] == destination]
        source = RUNTIME / relative
        if (len(rows) != 1 or rows[0]['Type'] != 'bind' or not rows[0]['RW'] or
                Path(rows[0]['Source']).resolve() != source.resolve() or source.is_symlink() or not source.is_dir()):
            raise ValueError('container mount does not identify the authorized runtime: ' + relative)
        if any(row['Destination'].startswith(destination + '/') for row in mounts):
            raise ValueError('a nested mount shadows an authorized runtime directory')
    command = container['Config'].get('Cmd') or []
    if command.count('--config') != 1:
        raise ValueError('container configuration command cannot be verified')
    offset = command.index('--config')
    if offset + 1 >= len(command) or command[offset + 1] != '/CLIProxyAPI/config/config.yaml':
        raise ValueError('container does not use the authorized mounted configuration')
    config = RUNTIME / 'config/config.yaml'
    if config.is_symlink() or not config.is_file():
        raise ValueError('mounted CPA configuration must be a regular file')
    ports = container.get('NetworkSettings', {}).get('Ports', {}).get('8317/tcp') or []
    if not any(row['HostIp'] == '127.0.0.1' and row['HostPort'] == '8317' for row in ports):
        raise ValueError('container is not the authorized local CPA endpoint')


def build_inspector(private):
    executable = private / 'inspect-private-store'
    subprocess.run(['go', 'build', '-o', str(executable), str(ROOT / 'scripts/inspect_private_store.go')],
                   cwd=ROOT / 'plugin', check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    return executable


def verify_configuration(inspector):
    try:
        reply = subprocess.run([str(inspector), '--verify-config', str(RUNTIME / 'config/config.yaml')],
                               check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        result = json.loads(reply.stdout)
        if (set(result) != {'verified', 'auth_kv_verified'} or result['verified'] is not True or
                type(result['auth_kv_verified']) is not bool):
            raise ValueError('unexpected configuration verification response')
        return result
    except (subprocess.CalledProcessError, ValueError):
        raise ValueError('configured credential or plugin storage does not match the authorized mounts') from None


def stable_store_digest(source, inspector, private, retained_snapshot=None):
    """Analyze a stable private copy, never the live bbolt file or its secrets."""
    snapshot = retained_snapshot or private / STORE_NAME
    for attempt in range(8):
        retained = False
        try:
            before = source.stat()
            if source.is_symlink() or not source.is_file():
                raise ValueError('account database must be a regular file')
            with source.open('rb') as incoming, snapshot.open('wb') as outgoing:
                os.chmod(snapshot, 0o600)
                shutil.copyfileobj(incoming, outgoing)
            after = source.stat()
            stamp = lambda st: (st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns, st.st_ctime_ns)
            if stamp(before) != stamp(after):
                continue
            copied = digest(snapshot.read_bytes())
            current = digest(source.read_bytes())
            if copied != current or stamp(after) != stamp(source.stat()):
                continue
            reply = subprocess.run([str(inspector), str(snapshot)], check=True,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            result = json.loads(reply.stdout)
            if (result.get('format') != 'bbolt-logical-v1' or len(result.get('sha256', '')) != 64 or
                    not all(char in '0123456789abcdef' for char in result['sha256']) or
                    not isinstance(result.get('buckets'), int) or not isinstance(result.get('keys'), int)):
                raise ValueError('invalid private database digest')
            retained = retained_snapshot is not None
            return result
        except (OSError, subprocess.CalledProcessError, ValueError):
            if attempt == 7:
                raise ValueError('cannot obtain a stable logical account database snapshot') from None
        finally:
            if not retained:
                snapshot.unlink(missing_ok=True)
        time.sleep(.05)
    raise ValueError('account database kept changing during snapshot verification')


def account_inventory(directory, inspector, private):
    result = inventory(directory)
    if not (directory / STORE_NAME).is_file():
        raise ValueError('expected the existing account database')
    for name in ACCOUNT_STORES:
        store = directory / name
        if store.is_file():
            result[digest(name.encode())] = stable_store_digest(store, inspector, private)
    return result


def auth_inventory(directory, inspector, private, auth_kv_verified):
    result = inventory(directory)
    store = directory / CPA_KV_NAME
    if auth_kv_verified and store.is_file():
        result[digest(CPA_KV_NAME.encode())] = stable_store_digest(store, inspector, private)
    return result


def database_inventory(directory, inspector, private, snapshot_prefix=None):
    result = {}
    for database in sorted(directory.rglob('*.db')):
        key = digest(database.relative_to(directory).as_posix().encode())
        kind = database.name if database.name in ('quality.db', STORE_NAME, 'login-receipts.db') else 'other-database'
        snapshot = None
        if snapshot_prefix and database == directory / 'model-quality/quality.db':
            snapshot = private / (snapshot_prefix + '-quality.db')
        result[key] = {'kind': kind, 'logical': stable_store_digest(database, inspector, private, snapshot)}
        if snapshot is not None:
            result[key]['snapshot'] = snapshot
    return result


def compare_quality_snapshots(before, after, inspector):
    try:
        reply = subprocess.run([str(inspector), '--compare-quality', str(before), str(after)],
                               check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        result = json.loads(reply.stdout)
        if (result.get('format') != 'quality-preservation-v1' or type(result.get('preserved')) is not bool or
                not all(type(result.get(key)) is int and result[key] >= 0 for key in
                        ('scheduled_empty_audits_added', 'rule_next_run_updated', 'unexpected_changes'))):
            raise ValueError('unexpected quality preservation response')
        return result
    except (subprocess.CalledProcessError, ValueError):
        raise ValueError('cannot verify the limited empty-schedule changes in quality storage') from None


def compare_persistent_data(before_files, before_databases, directory, inspector, private):
    after_files = inventory(directory)
    summary = {'file_set_preserved': set(before_files) <= set(after_files),
               'existing_files': len(before_files), 'added_files': len(set(after_files) - set(before_files)),
               'database_comparisons': [], 'quality_state_changed': False,
               'scheduled_empty_audits_added': 0, 'rule_next_run_updated': 0}
    if not summary['file_set_preserved']:
        raise ValueError('existing plugin data files disappeared during deployment')
    after_databases = database_inventory(directory, inspector, private, 'after')
    summary['existing_non_database_files_changed'] = sum(
        before_files[key] != after_files[key] for key in before_files if key not in before_databases)
    strict_changes = False
    for key, before in before_databases.items():
        after = after_databases[key]
        unchanged = before['logical'] == after['logical']
        comparison = {'kind': before['kind'], 'path_fingerprint': key, 'logical_unchanged': unchanged,
                      'before': before['logical'], 'after': after['logical']}
        allowed_change = False
        if before['kind'] == 'quality.db':
            summary['quality_state_changed'] = summary['quality_state_changed'] or not unchanged
            if not unchanged and before.get('snapshot') and after.get('snapshot'):
                preservation = compare_quality_snapshots(before['snapshot'], after['snapshot'], inspector)
                comparison['preservation'] = preservation
                allowed_change = preservation['preserved']
                summary['scheduled_empty_audits_added'] += preservation['scheduled_empty_audits_added']
                summary['rule_next_run_updated'] += preservation['rule_next_run_updated']
        comparison['permitted_empty_schedule_change'] = allowed_change and bool(preservation['scheduled_empty_audits_added'] or preservation['rule_next_run_updated'])
        if allowed_change and preservation.get('quality_host_input_migrated'):
            comparison['verified_input_migration'] = True
        summary['database_comparisons'].append(comparison)
        strict_changes = strict_changes or (not unchanged and not allowed_change)
    summary['quality_state_review_required'] = any(
        row['kind'] == 'quality.db' and not row['logical_unchanged'] and not (row['permitted_empty_schedule_change'] or row.get('verified_input_migration'))
        for row in summary['database_comparisons'])
    return summary, strict_changes


def replace_library(source, destination, expected_digest):
    descriptor, name = tempfile.mkstemp(prefix=destination.name + '.', suffix='.next', dir=destination.parent)
    os.close(descriptor)
    stage = Path(name)
    try:
        shutil.copy2(source, stage)
        if digest(stage.read_bytes()) != expected_digest:
            raise ValueError('staged plugin library digest mismatch')
        os.replace(stage, destination)
    finally:
        stage.unlink(missing_ok=True)


def write_report(output, report):
    with os.fdopen(os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as stream:
        json.dump(report, stream, indent=2, ensure_ascii=False)
        stream.write('\n')


def update(package, output):
    if output.exists():
        raise ValueError('refusing to replace an existing deployment report')
    manifest = json.loads((package / 'build-manifest.json').read_text())
    revalidates = validate_package_ui(manifest, ROOT)
    container = json.loads(docker('inspect', 'cli-proxy-api'))[0]
    validate_container(container)
    container_id = container['Id']
    image = json.loads(docker('image', 'inspect', container['Image']))[0]
    if manifest['platform'] != image['Os'] or manifest['arch'] != image['Architecture']:
        raise ValueError('plugin architecture does not match running CPA')
    library = package / 'model-quality.so'
    if digest(library.read_bytes()) != manifest['checksums'][library.name]:
        raise ValueError('library digest differs from build manifest')
    backup_parent = Path.home() / '.local/share/cpa-quality-backups'
    backup_parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if backup_parent.is_symlink() or backup_parent.stat().st_mode & 0o077:
        raise ValueError('backup directory is not private')
    old_library = RUNTIME / 'plugins/model-quality.so'
    if old_library.is_symlink() or not old_library.is_file():
        raise ValueError('existing plugin must be a regular library file')
    output.parent.mkdir(parents=True, exist_ok=True)
    # Compilation happens before stopping CPA. Temporary private DB copies are
    # removed on success and failure, and never enter the source workspace.
    with tempfile.TemporaryDirectory(prefix='inspect-', dir=backup_parent) as private_name:
        private = Path(private_name)
        inspector = build_inspector(private)
        configuration = verify_configuration(inspector)
        backup = Path(tempfile.mkdtemp(prefix='plugin-' + manifest['plugin_version'] + '-', dir=backup_parent))
        report = {'plugin_version': manifest['plugin_version'], 'ui_version': manifest['ui_version'],
                  'runtime_version': manifest['runtime_version'], 'platform': manifest['platform'], 'arch': manifest['arch'],
                  'backup': str(backup), 'previous_plugin_sha256': digest(old_library.read_bytes()),
                  'installed_plugin_sha256': manifest['checksums'][library.name], 'authenticated_verification_requests': 0,
                  'model_or_account_operations': 0, 'status': 'started', 'data_restored': False}
        replaced = False
        try:
            # Address the immutable original container ID, even if its name is changed.
            docker('stop', '--time', '30', container_id)
            if json.loads(docker('inspect', container_id))[0]['State']['Running']:
                raise ValueError('CPA did not stop')
            if verify_configuration(inspector) != configuration:
                raise ValueError('configured storage changed before the stopped-instance backup')
            before = {}
            for name in ('config', 'auths', 'plugin-data', 'plugins'):
                before[name] = inventory(RUNTIME / name)
                shutil.copytree(RUNTIME / name, backup / name)
                if inventory(backup / name) != before[name]:
                    raise ValueError('stopped-instance backup digest mismatch')
            write_report(backup / 'inventory.json', before)
            private_auths = auth_inventory(RUNTIME / 'auths', inspector, private, configuration['auth_kv_verified'])
            accounts = RUNTIME / 'plugin-data/model-quality/accounts'
            private_accounts = account_inventory(accounts, inspector, private)
            private_databases = database_inventory(RUNTIME / 'plugin-data', inspector, private, 'before')
            report['database_count_before'] = len(private_databases)
            if digest((backup / 'plugins/model-quality.so').read_bytes()) != report['previous_plugin_sha256']:
                raise ValueError('stopped plugin differs from preflight library')
            replace_library(library, old_library, manifest['checksums'][library.name])
            replaced = True
            docker('start', container_id)
            deadline = time.monotonic() + 40
            while True:
                try:
                    status, body, _ = get('/v0/resource/plugins/model-quality/index.html')
                    if status == 200 and digest(body) == manifest['ui_source_sha256']['index.html']:
                        break
                except (OSError, URLError):
                    pass
                if time.monotonic() >= deadline:
                    raise ValueError('CPA did not serve the built plugin')
                time.sleep(.2)
            assets = manifest['ui_source_sha256']
            verify_public_assets(assets, revalidates, get)
            if inventory(RUNTIME / 'config') != before['config']:
                raise ValueError('existing configuration changed during deployment')
            after_auths = auth_inventory(RUNTIME / 'auths', inspector, private, configuration['auth_kv_verified'])
            kv_key = digest(CPA_KV_NAME.encode())
            kv_before, kv_after = private_auths.get(kv_key), after_auths.get(kv_key)
            logical_kv = isinstance(kv_before, dict)
            report['credential_directory'] = {
                'comparison': 'exact file SHA-256 except verified CPA kv.db logical digest' if logical_kv else 'exact file SHA-256',
                'verified_cpa_kv_logical_comparison': logical_kv,
                'preserved': private_auths == after_auths}
            if logical_kv:
                report['credential_directory']['cpa_kv'] = {
                    'logical_unchanged': kv_before == kv_after, 'before': kv_before, 'after': kv_after}
            if after_auths != private_auths:
                raise ValueError('existing credential files or logical CPA KV storage changed during deployment')
            if account_inventory(accounts, inspector, private) != private_accounts:
                raise ValueError('logical account storage changed during deployment')
            state_summary, changed_private_database = compare_persistent_data(
                before['plugin-data'], private_databases, RUNTIME / 'plugin-data', inspector, private)
            report['persistent_state'] = state_summary
            if changed_private_database:
                raise ValueError('logical persistent database state changed during deployment; review the digest summary')
            if state_summary['existing_non_database_files_changed']:
                raise ValueError('existing plugin data files changed during deployment; review the stopped backup')
            after = json.loads(docker('inspect', 'cli-proxy-api'))[0]
            if (after['Id'] != container_id or after['Image'] != container['Image'] or
                    after['Mounts'] != container['Mounts'] or after['Config'].get('Cmd') != container['Config'].get('Cmd')):
                raise ValueError('CPA container identity, image, mounts or configuration command changed')
            validate_container(after)
            if get('/management.html')[0] != 200 or get('/v0/management/plugins/model-quality/settings')[0] != 401:
                raise ValueError('management assets or authentication boundary failed')
            report.update(status='passed', served_resource_sha256=assets, all_public_assets_revalidated=revalidates, host_image_unchanged=True,
                          mounts_unchanged=True, credential_files_unchanged=True, account_storage_unchanged=True,
                          account_database_comparison='bbolt bucket/key/value/sequence logical digest',
                          stopped_backup_comparison='exact file SHA-256',
                          credential_directory_files=len(before['auths']), account_storage_files=len(private_accounts),
                          verified_at=datetime.now(ZoneInfo('Asia/Shanghai')).isoformat())
        except BaseException as error:
            report.update(status='failed', failure_type=type(error).__name__,
                          failure_reason=str(error) if type(error) is ValueError else type(error).__name__)
            try:
                running = json.loads(docker('inspect', container_id))[0]['State']['Running']
                if replaced:
                    if running:
                        docker('stop', '--time', '30', container_id)
                    if json.loads(docker('inspect', container_id))[0]['State']['Running']:
                        raise ValueError('cannot restore library while CPA is running')
                    rollback_library = backup / 'plugins/model-quality.so'
                    if digest(rollback_library.read_bytes()) != report['previous_plugin_sha256']:
                        raise ValueError('backup library digest mismatch')
                    replace_library(rollback_library, old_library, report['previous_plugin_sha256'])
                    report['previous_library_restored'] = True
                if not json.loads(docker('inspect', container_id))[0]['State']['Running']:
                    docker('start', container_id)
                restored = json.loads(docker('inspect', container_id))[0]
                if not restored['State']['Running']:
                    raise ValueError('original CPA container did not restart')
                report['original_container_restarted'] = True
                report['status'] = 'failed_rolled_back' if replaced else 'failed_original_restarted'
            except BaseException as rollback_error:
                report['rollback_failure_type'] = type(rollback_error).__name__
                report['status'] = 'failed_rollback_unconfirmed'
                # A failed stop/start command can still have changed Docker state.
                # Make a final best-effort start of the original container only.
                try:
                    docker('start', container_id)
                    report['original_container_restarted'] = bool(json.loads(docker('inspect', container_id))[0]['State']['Running'])
                except BaseException:
                    report['original_container_restarted'] = False
            raise RuntimeError('Local plugin update failed; see the private report. Runtime data was not restored.') from None
        finally:
            write_report(output, report)
    print(json.dumps({'status': report['status'], 'plugin_version': manifest['plugin_version'],
                      'ui_version': manifest['ui_version'], 'report': str(output), 'backup': str(backup)}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('package', type=Path)
    parser.add_argument('output', type=Path)
    args = parser.parse_args()
    update(args.package.resolve(), args.output.resolve())
