"""Exercise credential policies on an unchanged, network-isolated CPA with fake credentials."""
import argparse
import hashlib
import json
import re
import secrets
import shutil
import subprocess
import tempfile
import time
import uuid
import zipfile
from pathlib import Path

HOST = 'cliproxyapi:8.0.76-local'
CURL = 'golang:1.26-bookworm'
PREFIX = '/v0/management/plugins/model-quality'


def docker(*args, **kwargs):
    return subprocess.run(['docker', *args], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs).stdout


def main(package, output, interrupt_status=False, upstream_errors=False, scope_skips=False, error_suspensions=False, credential_replacements=False, runtime_policy=False, manual_readmission=False, scheduled_addition=False, rule_history_clear=False, quality_operations=False):
    manifest = json.loads((package / 'build-manifest.json').read_text())
    archive = package / f'model-quality-v{manifest["plugin_version"]}-linux-arm64.zip'
    assert hashlib.sha256(archive.read_bytes()).hexdigest() == manifest['checksums'][archive.name]
    name = 'mq-policy-check-' + uuid.uuid4().hex[:10]
    mock = name + '-upstream'
    key = secrets.token_urlsafe(32)
    business_key = secrets.token_urlsafe(32)

    def request(path, body=None, method=None, allowed=(200, 202)):
        args = ['run', '--rm', '--pull=never', '--network', 'container:' + name, '-i', CURL, 'curl', '-qsS', '--noproxy', '*', '--max-time', '10', '-w', '\n%{http_code}', '--header', '@-']
        if body is not None:
            args += ['--header', 'Content-Type: application/json', '--data-binary', json.dumps(body)]
        if method:
            args += ['-X', method]
        bearer = business_key if path.startswith('/v1/') else key
        raw = docker(*args, 'http://127.0.0.1:18379' + path, input=f'Authorization: Bearer {bearer}\n'.encode())
        raw, status = raw.rsplit(b'\n', 1)
        data = json.loads(raw)
        assert int(status) in allowed, (path, status, data)
        return data

    def rule(action, restore=False, scope='all_enabled'):
        return request(PREFIX + '/rules', {'name': 'isolated policy check', 'object_types': ['oauth'], 'target_scope': scope, 'models': models, 'samples': 2, 'question_id': 'logic-boxes', 'judge': {'mode': 'deterministic'}, 'parameters': {'max_tokens': 128, 'reasoning_effort': 'high'}, 'action': action, 'auto_restore': restore, 'enabled': True, 'activation': 'keep'})

    def recorded_calls():
        path = root / 'calls.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    previous_mutations = []

    def mutation_log():
        log = subprocess.run(['docker', 'logs', name], check=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT).stdout.decode()
        return previous_mutations + re.findall(r'\|\s*(PATCH|DELETE)\s+"(/v8/management/credentials(?:/status|\?[^"]*))"', log)

    measured = []

    def run(rule, outcomes=('4', '4', '4', '4'), write=None, target='one.json'):
        calls_before = len(recorded_calls())
        writes_before = len(mutation_log())
        preview = request(PREFIX + '/rules/scope-preview', rule)
        assert preview['verified'] and preview['candidates']['provider'] == 0
        assert len(recorded_calls()) == calls_before and len(mutation_log()) == writes_before
        (root / 'round.json').write_text(json.dumps({'start': calls_before, 'outcomes': outcomes}))
        receipt = request(PREFIX + '/rules/run', {'id': rule['id'], 'revision': rule['revision']})
        for field in ('candidates','count','skipped','recovery_count'):
            assert receipt[field] == preview[field], (field, preview, receipt)
        rows = []
        if receipt['count']:
            until = time.monotonic() + 25
            while time.monotonic() < until:
                all_rows = request(PREFIX + '/runs?scope_batch_id=' + receipt['scope_batch_id'])['items']
                assert len(all_rows) == receipt['count'] + receipt['skipped']
                if all(r['status'] not in ('queued', 'running') and not r['action_status'].endswith('_pending') for r in all_rows):
                    rows = [r for r in all_rows if not r.get('preflight_detail') and r['status'] != 'skipped']
                    break
                time.sleep(.1)
            else:
                raise AssertionError('Synthetic rule did not finish')
        writes = mutation_log()[writes_before:]
        expected = []
        if write in ('disable', 'enable'):
            expected = [('PATCH', '/v8/management/credentials/status')]
        elif write == 'delete':
            expected = [('DELETE', '/v8/management/credentials?name=' + target)]
        assert writes == expected, (outcomes, writes, expected)
        file = root / 'auths' / target
        disabled = bool(json.loads(file.read_text()).get('disabled')) if file.exists() else None
        if write:
            assert disabled is {'disable': True, 'enable': False, 'delete': None}[write], (write, target, disabled)
        probes = recorded_calls()[calls_before:]
        if rows:
            assert len(rows) == 1 and [c['outcome'] for c in probes] == list(outcomes), (
                probes, [(r['status'], r.get('error'), [s.get('error') for s in r.get('samples', [])]) for r in rows])
            assert [s['model'] for s in rows[0]['samples']] == ([models[0]] * 2 + [models[1]] * 2)[:len(outcomes)]
            for probe, sample in zip(probes, rows[0]['samples']):
                probe.update(index=sample['index'], plugin_output=sample.get('output'), verdict=sample['quality_verdict'],
                             execution_status=sample['execution_status'], reason=sample.get('reason') or sample.get('error'))
        else:
            assert not probes, probes
        measured.append({'policy': rule['action'], 'scope': rule['target_scope'], 'file': target,
                         'action_status': rows[0]['action_status'] if rows else 'skipped',
                         'candidates': receipt['candidates'], 'recovery_count': receipt['recovery_count'],
                         'samples': probes, 'cpa_writes': writes, 'file_exists_after': file.exists(),
                         'file_disabled_after': disabled})
        return rows

    with tempfile.TemporaryDirectory(prefix='mq-policy-offline-') as tmp:
        root = Path(tmp)
        for directory in ['auths', 'plugins', 'data']:
            (root / directory).mkdir()
        with zipfile.ZipFile(archive) as z:
            (root / 'plugins/model-quality.so').write_bytes(z.read('model-quality.so'))
        def credential(name):
            return {'type': 'codex', 'access_token': 'offline-only-' + name, 'account_id': 'synthetic-' + name, 'email': name + '@example.test', 'base_url': 'http://127.0.0.1:18887/v1'}
        if credential_replacements or runtime_policy or manual_readmission or scheduled_addition or quality_operations:
            credentials = {'one': credential('one')}
        elif error_suspensions or rule_history_clear:
            from smoke_error_suspensions import ACCOUNTS
            credentials = {account: {**credential(account), 'plan_type': 'free' if account == 'catalog' else 'plus'} for account in ACCOUNTS}
        elif upstream_errors:
            from smoke_upstream_errors import INITIAL_ACCOUNTS, verify
            credentials = {account: credential(account) for account in INITIAL_ACCOUNTS}
        else:
            credentials = {'one': credential('one'), 'native': {
                **credential('native'), 'type': 'kimi', 'proxy_url': 'direct', 'expired': '2099-01-01T00:00:00Z',
            }}
            if scope_skips:
                credentials = {'operator': {**credential('operator'), 'disabled': True},
                               'missing-model': {**credential('missing-model'), 'plan_type': 'free'}, 'native': credentials['native'],
                               'native-disabled': {**credentials['native'], **credential('native-disabled'), 'type': 'kimi', 'disabled': True}}
        for account, data in credentials.items():
            (root / 'auths' / (account + '.json')).write_text(json.dumps(data))
        config = {'config-version': 8, 'server': {'host': '127.0.0.1', 'port': 18379, 'discovery': {'enabled': False}, 'kv-store': {'path': '/probe/kv.db'}}, 'management': {'secret-key': key, 'disable-control-panel': True, 'disable-auto-update-panel': True}, 'oauth': {'auth-dir': '/probe/auths'}, 'plugins': {'enabled': True, 'dir': '/probe/plugins', 'configs': {'model-quality': {'enabled': True, 'data_dir': '/probe/data', 'scheduling_enabled': False}}}}
        native_codex = runtime_policy or manual_readmission or quality_operations
        if native_codex:
            config['access'] = {'api-keys': [business_key]}
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                            '-subj', '/CN=chatgpt.com', '-addext', 'subjectAltName=DNS:chatgpt.com',
                            '-keyout', str(root / 'fixture-key.pem'), '-out', str(root / 'fixture-cert.pem')],
                           check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        (root / 'config.yaml').write_text(json.dumps(config))
        shutil.copyfile(Path(__file__).with_name('fixtures') / 'credential_policy_upstream.py', root / 'fake.py')
        record_writes = upstream_errors or error_suspensions or credential_replacements or native_codex or scheduled_addition or rule_history_clear
        adapter_port = 18479 if interrupt_status or record_writes else 18379
        business_fixture = ['--add-host', 'chatgpt.com:127.0.0.1', '-e', 'SSL_CERT_FILE=/probe/fixture-cert.pem'] if native_codex else []
        def start_host():
            docker('run', '-d', '--pull=never', '--network', 'none', '--name', name, *business_fixture, '-e', f'MODEL_QUALITY_CPA_URL=http://127.0.0.1:{adapter_port}', '--mount', f'type=bind,source={root},target=/probe', HOST, './CLIProxyAPI', '--config', '/probe/config.yaml', '--standalone', '--local-model')

        start_host()
        try:
            def start_fixture():
                fixture_args = ['--record-writes'] if record_writes else []
                if native_codex:
                    fixture_args.append('--business-tls')
                docker('run', '-d', '--pull=never', '--name', mock, '--network', 'container:' + name, '--mount', f'type=bind,source={root},target=/probe', CURL, 'python3', '-u', '/probe/fake.py', *fixture_args)

            def restart_with_fixture(recreate_after=None, *, await_management=False):
                docker('rm', '-f', mock)
                if recreate_after is None:
                    docker('restart', name)
                else:
                    docker('stop', name)
                    old_mutations = mutation_log()
                    previous_mutations[:] = old_mutations
                    docker('rm', name)
                    time.sleep(max(0, recreate_after - time.time()))
                    start_host()
                start_fixture()
                if await_management:
                    # The plugin can serve local queries while its verifier is
                    # waiting for the restarted loopback relay. Observe the
                    # saved revision before issuing a management-dependent command.
                    deadline = time.monotonic() + 30
                    states = []
                    status = {}
                    while time.monotonic() < deadline:
                        try:
                            status = request(PREFIX + '/connection')
                        except (subprocess.CalledProcessError, ValueError):
                            time.sleep(.2)
                            continue
                        state = status.get('state')
                        if not states or states[-1] != state:
                            states.append(state)
                        if state == 'ready':
                            assert status['source'] == 'saved' and status['revision'] == configured['revision']
                            return states
                        if state not in ('waiting', 'rate_limited', 'restricted'):
                            raise AssertionError(('persisted management authorization did not load', state, status.get('code')))
                        time.sleep(.2)
                    raise AssertionError(('loopback authorization did not recover', states, status.get('code')))

            start_fixture()
            if interrupt_status:
                shutil.copyfile(Path(__file__).with_name('fixtures') / 'hold_status_reply.py', root / 'relay.py')
                docker('exec', '-d', mock, 'python3', '/probe/relay.py')
            until = time.monotonic() + 30
            while time.monotonic() < until:
                try:
                    targets = request(PREFIX + '/targets')['items']
                    if len(targets) == len(credentials) and all(len(t['models']) >= 2 or scope_skips and t['manual_disabled'] for t in targets):
                        break
                except (subprocess.CalledProcessError, AssertionError, ValueError):
                    pass
                time.sleep(.2)
            else:
                raise AssertionError('Synthetic credential did not register')
            connection = request(PREFIX + '/connection')
            assert connection['can_migrate'] and not connection['configured']
            configured = request(PREFIX + '/connection', {'mode':'configure','revision':connection['revision']}, 'PUT')
            assert configured['state'] == 'ready' and configured['source'] == 'saved'
            if quality_operations:
                from smoke_quality_operations import verify_operations
                report = verify_operations(PREFIX, request, restart_with_fixture, root, targets[0], credential, recorded_calls)
                report.update(plugin_version=manifest['plugin_version'], package_sha256=manifest['checksums'][archive.name],
                              host='unchanged official 8.0.76', network='none', live_account_calls=0)
                output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
                print('Verified independent policies, cross-rule ownership, current enablement and new snapshots on isolated CPA.')
                return
            if scheduled_addition:
                from smoke_scheduled_addition import verify_scheduled_addition
                report = verify_scheduled_addition(PREFIX, request, restart_with_fixture, root, targets[0], credential, recorded_calls, mutation_log)
                report.update(plugin_version=manifest['plugin_version'], package_sha256=manifest['checksums'][archive.name],
                              host='unchanged official 8.0.76', network='none', live_account_calls=0)
                output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
                print('Verified a newly uploaded credential ran on the next real cron tick after an empty round.')
                return
            if manual_readmission:
                from smoke_error_suspensions import verify_manual_readmission
                report = verify_manual_readmission(PREFIX, request, restart_with_fixture, root, targets[0], recorded_calls, mutation_log)
                report.update(plugin_version=manifest['plugin_version'], package_sha256=manifest['checksums'][archive.name],
                              host='unchanged official 8.0.76', network='none', live_account_calls=0)
                output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
                print('Verified manual readmission and automatic cleanup of confirmed policy ownership after rule deletion and restart.')
                return
            if runtime_policy:
                from smoke_runtime_policy import verify_runtime
                models = targets[0]['models'][:2]
                report = verify_runtime(PREFIX, request, root, models, recorded_calls, mutation_log)
                report.update(plugin_version=manifest['plugin_version'], package_sha256=manifest['checksums'][archive.name],
                              host='unchanged official 8.0.76', network='none', live_account_calls=0)
                output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
                print('Verified managed probes and error policy during an actual CPA runtime error.')
                return
            if credential_replacements:
                from smoke_credential_replacements import verify_replacements
                models = targets[0]['models'][:2]
                report = verify_replacements(PREFIX, request, restart_with_fixture, root, models, credential, run, recorded_calls)
                report.update(plugin_version=manifest['plugin_version'], package_sha256=manifest['checksums'][archive.name],
                              host='unchanged official 8.0.76', network='none', live_account_calls=0)
                output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
                print('Verified same-name replacement and token rotation, new action ownership and retained original run evidence.')
                return
            if error_suspensions or rule_history_clear:
                from smoke_error_suspensions import verify_error_suspensions
                report = verify_error_suspensions(PREFIX, request, restart_with_fixture, root, targets, recorded_calls,
                                                  mutation_log, clear_history=rule_history_clear)
                report.update(plugin_version=manifest['plugin_version'], package_sha256=manifest['checksums'][archive.name],
                              host='unchanged official 8.0.76', network='none', live_account_calls=0)
                output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
                if rule_history_clear:
                    print('Verified cleared rule history with six confirmed suspensions and one ordinary run through restart.')
                else:
                    print('Verified six terminal error suspensions through manual runs, scheduled runs and restart.')
                return
            if scope_skips:
                from smoke_scope_skips import verify_scope_skips
                verify_scope_skips(PREFIX, request, restart_with_fixture, root, targets, recorded_calls,
                                   mutation_log, manifest, output)
                print('Verified durable catalog preflight, independent skips, and silent exclusion after error suspension.')
                return
            if upstream_errors:
                report = verify(PREFIX, request, root, targets, credential, recorded_calls, mutation_log)
                report.update(plugin_version=manifest['plugin_version'], package_sha256=manifest['checksums'][archive.name],
                              host='unchanged official 8.0.76', network='none', only_synthetic_credentials=True)
                output.write_text(json.dumps(report, indent=2) + '\n')
                print(f"Verified upstream error handling in {len(report['rounds'])} synthetic batches, with {len(report['cpa_writes'])} confirmed CPA writes.")
                return
            codex = next(t for t in targets if t['email'] == 'one@example.test')
            if interrupt_status:
                models = codex['models'][:2]
                suspended = rule('disable_credential', True)
                (root / 'round.json').write_text(json.dumps({'start': 0, 'outcomes': ['4', '4', '4', '5']}))
                batch = request(PREFIX + '/rules/run', {'id': suspended['id'], 'revision': suspended['revision']})
                assert batch['count'] == 2 and batch['skipped'] == 0, batch
                until = time.monotonic() + 20
                while not (root / 'status-applied').exists():
                    if time.monotonic() >= until:
                        raise AssertionError('CPA did not confirm the synthetic PATCH')
                    time.sleep(.05)
                initial_rows = request(PREFIX + '/runs?scope_batch_id=' + batch['scope_batch_id'])['items']
                initial = next(row for row in initial_rows if row['credential_email'] == 'one@example.test')
                peer = next(row for row in initial_rows if row['credential_email'] == 'native@example.test')
                assert peer['status'] == 'failed' and peer['preflight_detail']['code'] == 'unsupported_provider'
                assert initial['action_status'] == 'disable_pending'
                detail_route = PREFIX + '/runs/detail?id=' + initial['id']
                snapshots = []
                docker('kill', name)
                docker('start', name)
                for restart in range(2):
                    if restart:
                        docker('restart', name)
                    until = time.monotonic() + 30
                    while True:
                        try:
                            detail = request(detail_route)
                            break
                        except (subprocess.CalledProcessError, AssertionError, ValueError):
                            if time.monotonic() >= until:
                                raise
                            time.sleep(.2)
                    handling = next(r['handling'] for r in request(PREFIX + '/rules')['items'] if r['id'] == suspended['id'])
                    assert detail['action_status'] == 'credential_action_uncertain' and detail.get('credential_action') == 'disable_credential', (
                        detail['action_status'], detail.get('credential_action'), mutation_log(), json.loads((root / 'auths/one.json').read_text())['disabled'])
                    assert len(handling) == 1 and handling[0]['state'] == 'attention'
                    assert detail['samples'] == initial['samples'] and len(recorded_calls()) == 4
                    assert json.loads((root / 'auths/one.json').read_text())['disabled']
                    assert mutation_log() == [('PATCH', '/v8/management/credentials/status')]
                    snapshots.append({'action_status': detail['action_status'], 'handling': handling[0]['state'],
                                      'samples': len(detail['samples']), 'patches': len(mutation_log()), 'disabled': True})
                assert request(PREFIX + '/rules/run-history', method='DELETE') == {'deleted': 1, 'kept': 1, 'kept_for_handling': 1}
                current_rule = next(r for r in request(PREFIX + '/rules')['items'] if r['id'] == suspended['id'])
                action = current_rule['handling'][0]
                calls_before_resolution = len(recorded_calls())
                writes_before_resolution = mutation_log()
                request(PREFIX + '/rules', {'id': current_rule['id'], 'revision': current_rule['revision']}, 'DELETE')
                rules_after_deletion = request(PREFIX + '/rules')
                assert all(r['id'] != suspended['id'] for r in rules_after_deletion['items'])
                orphaned = rules_after_deletion['orphaned_handling']
                assert len(orphaned) == 1 and orphaned[0]['auth_id'] == action['auth_id']
                assert orphaned[0]['action_id'] == action['action_id'] and orphaned[0]['state'] == 'attention'
                assert 'snapshot' not in orphaned[0]
                ack = {'rule_id': '', 'auth_id': action['auth_id'], 'action_id': action['action_id']}
                blocked_ack = request(PREFIX + '/rules/handling/acknowledge', ack, allowed=(409,))
                assert blocked_ack['error']['code'] == 'blocked', blocked_ack
                assert mutation_log() == writes_before_resolution and len(recorded_calls()) == calls_before_resolution
                request('/v8/management/credentials/status', {'name': 'one.json', 'auth_index': codex['auth_index'], 'disabled': False}, 'PATCH')
                until = time.monotonic() + 10
                while True:
                    enabled = next(t for t in request(PREFIX + '/targets')['items'] if t['auth_id'] == action['auth_id'])
                    if not enabled['manual_disabled']:
                        break
                    if time.monotonic() >= until:
                        raise AssertionError('Explicit CPA enablement did not become visible')
                    time.sleep(.1)
                assert request(PREFIX + '/rules')['orphaned_handling'][0]['state'] == 'attention'
                stale_ack = request(PREFIX + '/rules/handling/acknowledge', {**ack, 'action_id': 'stale-action'}, allowed=(409,))
                assert stale_ack['error']['code'] == 'conflict', stale_ack
                assert request(PREFIX + '/rules/handling/acknowledge', ack) == {'ok': True}
                final_rules = request(PREFIX + '/rules')
                assert not final_rules['orphaned_handling'] and all(r['id'] != suspended['id'] for r in final_rules['items'])
                assert request(detail_route)['samples'] == initial['samples']
                assert mutation_log() == writes_before_resolution + [('PATCH', '/v8/management/credentials/status')]
                assert len(recorded_calls()) == calls_before_resolution and not json.loads((root / 'auths/one.json').read_text())['disabled']
                output.write_text(json.dumps({'plugin_version': manifest['plugin_version'], 'package_sha256': manifest['checksums'][archive.name],
                                              'host': 'unchanged official 8.0.76', 'network': 'none', 'live_account_calls': 0,
                                              'before': initial['action_status'], 'after_restarts': snapshots,
                                              'fake_model_calls': len(recorded_calls()), 'orphaned_handling_visible_after_rule_deletion': True,
                                              'disabled_orphan_acknowledgement_rejected': True, 'stale_orphan_acknowledgement_rejected': True,
                                              'orphan_resolved_after_explicit_cpa_enablement': True,
                                              'plugin_writes_for_orphan_resolution': 0, 'explicit_enable_writes': 1,
                                              'source_samples_retained': len(initial['samples'])}, indent=2)+'\n')
                print('Verified unknown-write protection through two starts and explicit orphan resolution after rule deletion.')
                return
            models = next(t['models'][:2] for t in targets if t['email'] == 'native@example.test')
            settings = request(PREFIX + '/settings')
            assert settings['capabilities']['credential_management'] and settings['capabilities']['management_session']
            mixed = ('4', '4', '4', '5')
            observer = rule('observe_only')
            rows = run(observer, mixed, target='native.json')
            assert rows[0]['action_status'] == 'observe_only'
            assert [s['quality_verdict'] for s in rows[0]['samples']] == ['correct', 'correct', 'correct', 'incorrect']
            request('/v8/management/credentials?name=native.json', method='DELETE')
            models = codex['models'][:2]
            pause = rule('disable_credential', True)
            rows = run(pause, mixed, 'disable')
            assert len(rows) == 1 and rows[0]['action_status'] == 'credential_suspended', rows
            assert rows[0]['credential_email'] == 'one@example.test'
            assert json.loads((root / 'auths/one.json').read_text())['disabled'] is True
            assert run(pause, mixed)[0]['action_status'] == 'credential_kept_disabled'
            rows = run(pause, write='enable')
            assert len(rows) == 1 and rows[0]['action_status'] == 'credential_restored', rows
            assert json.loads((root / 'auths/one.json').read_text())['disabled'] is False
            assert [c['disabled'] for c in recorded_calls()] == [False] * 8 + [True] * 8
            removal = rule('delete_credential')
            rows = run(pause, ('4', '4', '4', 'commentary_then_final'))
            assert [s['quality_verdict'] for s in rows[0]['samples']] == ['correct'] * 4
            assert rows[0]['samples'][-1]['output'] == '4'
            for action in (pause, removal):
                for outcome in ('timeout', 'refusal', 'truncated', 'not an integer', 5,
                                'commentary_only', 'ambiguous_answers', 'user_message', 'missing_item_status'):
                    valid = ('4', '4', '4') if outcome == 5 else ('4', '4', '5')
                    rows = run(action, (*valid, outcome))
                    assert rows[0]['action_status'] == 'insufficient_evidence'
                    expected = ['correct' if answer == '4' else 'incorrect' for answer in valid] + ['unknown']
                    assert [s['quality_verdict'] for s in rows[0]['samples']] == expected
                    last = rows[0]['samples'][-1]
                    assert last.get('reason') or last.get('error'), last
                    if outcome == 'missing_item_status':
                        assert last.get('output') == '4' and last.get('error'), last
                    assert not json.loads((root / 'auths/one.json').read_text())['disabled']
            rows = run(pause, mixed, 'disable')
            assert rows[0]['action_status'] == 'credential_suspended', rows
            rows = run(pause, ('4', '4', '4', 'user_message'))
            assert rows[0]['samples'][-1]['quality_verdict'] == 'unknown'
            assert json.loads((root / 'auths/one.json').read_text())['disabled']
            request('/v8/management/credentials/status', {'name': 'one.json', 'auth_index': codex['auth_index'], 'disabled': True}, 'PATCH')
            # A same-entry status write between rounds does not end answer ownership.
            assert run(pause, write='enable')[0]['action_status'] == 'credential_restored'
            request('/v8/management/credentials/status', {'name': 'one.json', 'auth_index': codex['auth_index'], 'disabled': False}, 'PATCH')
            request(PREFIX + '/targets')
            current = next(r for r in request(PREFIX + '/rules')['items'] if r['id'] == pause['id'])
            assert current['enabled'] and not current.get('handling')
            directory = rule('disable_credential', True, 'all_credentials')
            request('/v8/management/credentials/status', {'name': 'one.json', 'auth_index': codex['auth_index'], 'disabled': True}, 'PATCH')
            # Wait for CPA's file watcher before freezing a fresh manual-disable round.
            time.sleep(1)
            observation_all = rule('observe_only', False, 'all_credentials')
            assert run(observation_all)[0]['action_status'] == 'observe_only'
            assert run(rule('observe_only')) == []
            assert json.loads((root / 'auths/one.json').read_text())['disabled']
            assert run(directory, mixed)[0]['action_status'] == 'credential_kept_disabled'
            assert json.loads((root / 'auths/one.json').read_text())['disabled']
            assert run(directory, write='enable')[0]['action_status'] == 'credential_restored'
            assert not json.loads((root / 'auths/one.json').read_text())['disabled']
            assert all(c['disabled'] for c in recorded_calls()[-8:])
            rows = run(removal, mixed, 'delete')
            assert rows[0]['action_status'] == 'credential_removed', rows
            assert not (root / 'auths/one.json').exists()
            assert request('/v8/management/credentials')['files'] == []
            assert run(removal) == []
            request('/v8/management/credentials?name=two.json', credential('two'))
            until = time.monotonic() + 10
            while time.monotonic() < until:
                if request(PREFIX + '/targets')['items']:
                    break
                time.sleep(.2)
            rows = run(removal, mixed, 'delete', 'two.json')
            assert len(rows) == 1 and rows[0]['action_status'] == 'credential_removed', rows
            assert not (root / 'auths/two.json').exists()
            request('/v8/management/credentials?name=three.json', credential('three'))
            until = time.monotonic() + 10
            while time.monotonic() < until:
                if any(t.get('models') for t in request(PREFIX + '/targets')['items']):
                    break
                time.sleep(.2)
            rows = run(directory, mixed, 'disable', 'three.json')
            assert len(rows) == 1 and rows[0]['action_status'] == 'credential_suspended', rows
            orphan = rows[0]['id']
            request('/v8/management/credentials?name=three.json', method='DELETE')
            assert run(directory, target='three.json') == []
            until = time.monotonic() + 10
            while time.monotonic() < until:
                saved = next(r for r in request(PREFIX + '/rules')['items'] if r['id'] == directory['id'])
                if not saved.get('handling'):
                    assert saved['enabled'] and saved['revision'] == directory['revision']
                    break
                time.sleep(2)
            else:
                raise AssertionError('Deleted credential retained its suspension')
            assert orphan in [r['id'] for r in request(PREFIX + '/runs')['items']]
            cleared = request(PREFIX + '/rules/run-history', method='DELETE')
            assert cleared['deleted'] > 0 and cleared['kept'] == 0, cleared
            assert request(PREFIX + '/runs?source=scheduled_probe')['total'] == 0
            before = len(recorded_calls())
            writes_before_restart = mutation_log()
            docker('restart', name)
            until = time.monotonic() + 30
            while True:
                try:
                    if request(PREFIX + '/settings')['capabilities']['management_session']:
                        break
                except (subprocess.CalledProcessError, AssertionError, json.JSONDecodeError):
                    pass
                if time.monotonic() >= until:
                    raise AssertionError('Plugin did not become ready after restart')
                time.sleep(.2)
            assert run(removal) == []
            assert orphan not in [r['id'] for r in request(PREFIX + '/runs')['items']]
            assert before == len(recorded_calls()) and writes_before_restart == mutation_log()
            report = {
                'plugin_version': manifest['plugin_version'], 'package_sha256': manifest['checksums'][archive.name],
                'host': 'unchanged official 8.0.76', 'network': 'none', 'only_synthetic_credentials': True,
                'suspend_and_whole_round_restore': True, 'recovery_probes_remained_disabled': True,
                'incomplete_never_mutates': True, 'operator_disable_preserved': True,
                'manual_enablement_keeps_rule_configuration': True, 'exact_file_removal': True, 'dynamic_new_credentials': True,
                'restart_no_replay': True, 'fake_model_calls': before, 'live_account_calls': 0, 'rounds': measured,
            }
            output.write_text(json.dumps(report, indent=2)+'\n')
            print('Official CPA management policy smoke checks passed; all credentials and upstream responses were synthetic.')
        finally:
            for container in [mock,name]:
                subprocess.run(['docker','rm','-f',container],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('package', type=Path)
    parser.add_argument('output', type=Path)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument('--interrupt-status', action='store_true')
    modes.add_argument('--upstream-errors', action='store_true', help='verify per-error policies and batch continuation')
    modes.add_argument('--scope-skips', action='store_true', help='verify catalog checks alongside probes and unexecuted disabled objects')
    modes.add_argument('--error-suspensions', action='store_true', help='verify error-disabled credentials stay excluded after restart and real cron triggers')
    modes.add_argument('--credential-replacements', action='store_true', help='verify an enabled replacement or renewed file can be tested and handled with current evidence')
    modes.add_argument('--runtime-policy', action='store_true', help='induce an official-host runtime error and verify current probe evidence and writes')
    modes.add_argument('--manual-readmission', action='store_true', help='verify manual and rule testing after external CPA enablement without acknowledgement')
    modes.add_argument('--scheduled-addition', action='store_true', help='verify a dynamic timer discovers a credential added after an empty round without panel traffic')
    modes.add_argument('--rule-history-clear', action='store_true', help='clear completed history while preserving confirmed error dispositions through restart')
    modes.add_argument('--quality-operations', action='store_true', help='verify independent policies, cross-rule error precedence and same-entry recovery')
    args = parser.parse_args()
    main(args.package.resolve(), args.output.resolve(), args.interrupt_status, args.upstream_errors, args.scope_skips, args.error_suspensions, args.credential_replacements, args.runtime_policy, args.manual_readmission, args.scheduled_addition, args.rule_history_clear, args.quality_operations)
