"""Compare offline probes, catalog checks and non-runnable disabled objects."""

import hashlib
import json
import subprocess
import time
from datetime import datetime


def verify_scope_skips(prefix, request, restart, root, targets, recorded_calls, mutation_log, package, output):
    by_email = {t['email']: t for t in targets}
    disabled = by_email['operator@example.test']
    codex = by_email['missing-model@example.test']
    native = by_email['native@example.test']
    unprobeable = by_email['native-disabled@example.test']
    assert disabled['manual_disabled'] and codex['provider'] == 'codex' and native['provider'] == 'kimi'
    model = 'gpt-6.1-sol'
    assert codex['models'] and model not in codex['models']
    rule = request(prefix + '/rules', {
        'name': 'Synthetic mixed directory checks', 'object_types': ['oauth'], 'target_scope': 'all_credentials',
        'models': [model], 'samples': 1, 'question_id': 'logic-boxes',
        'judge': {'mode': 'deterministic'}, 'action': 'disable_credential',
        'auto_restore': False, 'enabled': True, 'activation': 'keep',
    })
    def credential_hashes():
        return {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (root / 'auths').glob('*.json')}

    def current_rule():
        saved = next(r for r in request(prefix + '/rules')['items'] if r['id'] == rule['id'])
        return {k: saved[k] for k in ('id', 'revision', 'enabled', 'last_manual_skips', 'last_scope_dispatch') if k in saved}

    hashes = credential_hashes()
    preview = request(prefix + '/rules/scope-preview', rule)
    assert preview['verified'] and preview['candidates']=={'oauth':4,'provider':0}
    assert len(recorded_calls())==0 and not mutation_log()
    (root / 'round.json').write_text(json.dumps({'start': 0, 'outcomes': ['4'] * 3}))
    receipt = request(prefix + '/rules/run', {'id': rule['id'], 'revision': rule['revision']})
    deadline = time.monotonic() + 20
    while True:
        members = request(prefix + '/runs?scope_batch_id=' + receipt['scope_batch_id'])['items']
        if len(members) == 4 and all(row.get('completed_at') for row in members):
            break
        if time.monotonic() >= deadline:
            raise AssertionError(('catalog observation did not complete', receipt))
        time.sleep(.1)
    before = current_rule()
    restart_states = restart(await_management=True)
    deadline = time.monotonic() + 30
    while True:
        try:
            after = current_rule()
            break
        except (subprocess.CalledProcessError, AssertionError, StopIteration, ValueError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(.2)
    rows = request(prefix + '/runs?source=scheduled_probe&rule_id=' + rule['id'])
    report = {
        'plugin_version': package['plugin_version'], 'host_version': package['host_version'],
        'package_sha256': package['checksums'][f"model-quality-v{package['plugin_version']}-linux-arm64.zip"],
        'network': 'none', 'model': model, 'receipt': receipt, 'rule_before_restart': before, 'rule_after_restart': after,
        'restart_authorization_states': restart_states,
        'targets': [{k: target[k] for k in ('auth_id', 'email', 'provider', 'manual_disabled')} for target in targets],
        'runs_after_restart': rows['total'], 'fake_model_calls': len(recorded_calls()), 'cpa_writes': mutation_log(),
        'credential_hashes_unchanged': hashes == credential_hashes(), 'live_account_calls': 0,
    }
    assert (receipt['count'], receipt['skipped']) == (3, 1) and rows['total'] == 4, report
    assert all(receipt[k]==preview[k] for k in ('candidates','count','skipped','recovery_count'))
    assert report['fake_model_calls']==1 and recorded_calls()[0]['disabled'] and not report['cpa_writes'] and report['credential_hashes_unchanged'], report
    details = receipt.get('skip_details', [])
    assert len(details) == 1, ('incorrect individual skip reasons', receipt)
    expected = {unprobeable['auth_id']: 'unsupported_provider'}
    assert {item['auth_id']: item['code'] for item in details} == expected, details
    assert {item['auth_id']: item.get('email') for item in details} == {t['auth_id']: t['email'] for t in targets if t['auth_id'] in expected}, details
    assert all(item['reason'].strip() for item in details), details
    stored = before['last_scope_dispatch']
    assert (stored['matched'], stored['skipped'], stored['details']) == (3, 1, details), stored
    assert datetime.strptime(stored['at'][:19], '%Y-%m-%dT%H:%M:%S').year > 1970
    assert before == after and before['enabled'] and before['revision'] == rule['revision'], (before, after)
    assert {row['config']['scope_batch_id'] for row in rows['items']} == {receipt['scope_batch_id']}
    assert len({row['id'] for row in rows['items']}) == 4
    observed_disabled = next(row for row in rows['items'] if row['config']['auth_id']==disabled['auth_id'])
    assert observed_disabled['config']['managed_probe']['disabled'] and observed_disabled['action_status']=='credential_kept_disabled'
    assert [s['quality_verdict'] for s in observed_disabled['samples']]==['correct']
    assert all(not row['samples'] for row in rows['items'] if row['id']!=observed_disabled['id'])
    skipped = [row for row in rows['items'] if row['status'] == 'skipped']
    assert {row['config']['auth_id']: row['skip_detail']['code'] for row in skipped} == expected
    observed = next(row for row in rows['items'] if row['config']['auth_id'] == codex['auth_id'])
    unsupported = next(row for row in rows['items'] if row['config']['auth_id'] == native['auth_id'])
    assert unsupported['status'] == 'failed' and unsupported['preflight_detail']['code'] == 'unsupported_provider' and not unsupported.get('skip_detail')
    assert observed['status'] == 'completed' and observed['action_status'] == 'observe_only' and not observed.get('skip_detail')
    assert observed['preflight_detail']['code'] == 'missing_model' and observed['preflight_detail']['model'] == model
    report['skipped_run_ids'] = [row['id'] for row in skipped]
    report['catalog_observation'] = observed['preflight_detail']
    report['unsupported_provider'] = unsupported['preflight_detail']

    def handle_catalog(action, subject):
        policy = request(prefix + '/rules', {
            'name': 'Synthetic catalog ' + action, 'target_scope': 'all_enabled',
            'models': [model], 'samples': 1, 'question_id': 'logic-boxes',
            'action': 'disable_credential' if action == 'disable_credential' else 'observe_only',
            'auto_restore': action == 'disable_credential', 'upstream_error_actions': {'model_unsupported': action},
            'enabled': True, 'activation': 'keep',
        })
        start = len(mutation_log())
        batch = request(prefix + '/rules/run', {'id': policy['id'], 'revision': policy['revision']})
        deadline = time.monotonic() + 30
        while True:
            records = request(prefix + '/runs?scope_batch_id=' + batch['scope_batch_id'])['items']
            row = next((r for r in records if r['config']['auth_id'] == subject), None)
            if row and row['action_status'] in ('credential_suspended', 'credential_removed'):
                break
            if time.monotonic() >= deadline:
                raise AssertionError((action, batch, records))
            time.sleep(.1)
        assert batch['count'] == 2 and len(records) == batch['count'] + batch['skipped']
        assert all(not r['samples'] for r in records) and row['status'] == 'completed'
        assert not row.get('skip_detail') and row['preflight_detail']['code'] == 'missing_model' and row['config']['catalog_missing_model'] == model
        assert row['credential_action'] == action and not row.get('upstream_condition')
        writes = mutation_log()[start:]
        follow = None
        if action == 'disable_credential':
            assert writes == [('PATCH', '/v8/management/credentials/status')]
            assert json.loads((root / 'auths' / subject).read_bytes())['disabled'] is True
            follow = request(prefix + '/rules/run', {'id': policy['id'], 'revision': policy['revision']})
            assert follow['count'] == 1 and follow['skipped'] == 0, follow
            follow_rows = request(prefix + '/runs?scope_batch_id=' + follow['scope_batch_id'])['items']
            assert [r['config']['auth_id'] for r in follow_rows] == [native['auth_id']]
            records_after = request(prefix + '/runs?rule_id=' + policy['id'])['items']
            assert [r['id'] for r in records_after if r['config']['auth_id'] == subject] == [row['id']]
            assert mutation_log()[start:] == writes and json.loads((root / 'auths' / subject).read_bytes())['disabled'] is True
        else:
            assert writes == [('DELETE', '/v8/management/credentials?name=' + subject)]
            assert not (root / 'auths' / subject).exists()
        assert len(recorded_calls())==1 and recorded_calls()[0]['disabled']
        return {'action': action, 'run': row['id'], 'model': model, 'status': row['status'],
                'condition_source': 'credential model catalog', 'action_status': row['action_status'],
                'cpa_writes': writes, 'model_calls': 0, 'records': len(records),
                'later_receipt': follow}

    free = json.loads((root / 'auths' / 'missing-model.json').read_bytes())
    catalog_actions = [handle_catalog('disable_credential', 'missing-model.json')]
    free.update(email='delete-free@example.test', account_id='synthetic-delete-free', access_token='offline-only-delete-free')
    request('/v8/management/credentials?name=delete-free.json', free)
    deadline = time.monotonic() + 15
    while not any(t['auth_id'] == 'delete-free.json' and t['models'] for t in request(prefix + '/targets')['items']):
        if time.monotonic() >= deadline:
            raise AssertionError('Replacement synthetic Free credential did not appear')
        time.sleep(.2)
    catalog_actions.append(handle_catalog('delete_credential', 'delete-free.json'))
    assert json.loads((root / 'auths' / 'operator.json').read_bytes())['disabled'] is True
    assert not json.loads((root / 'auths' / 'native.json').read_bytes()).get('disabled', False)
    report['catalog_actions'] = catalog_actions
    output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
