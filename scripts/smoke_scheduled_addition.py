"""Dynamic cron inventory scenario for smoke_credential_policy.py --scheduled-addition."""

import json
import time
from datetime import datetime


def verify_scheduled_addition(prefix, request, restart, root, initial, credential, recorded_calls, mutation_log):
    model = initial['models'][0]
    assert initial['auth_id'] == 'one.json' and initial['effective_available']
    auth_failure = {'status': 401, 'error': {'code': 'invalid_api_key', 'message': 'Synthetic invalid API key'}}
    (root / 'round.json').write_text(json.dumps({
        'start': 0, 'accounts': {'one@example.test': [auth_failure] * 4, 'new@example.test': ['4'] * 4},
    }))
    saved = request(prefix + '/rules', {
        'name': 'Dynamic inventory after an empty tick', 'object_types': ['oauth', 'provider'], 'target_scope': 'all_enabled',
        'models': [model], 'samples': 1, 'question_id': 'logic-boxes', 'judge': {'mode': 'deterministic'},
        'parameters': {'reasoning_effort': 'high'}, 'action': 'disable_credential', 'auto_restore': True,
        'upstream_error_actions': {'unauthorized': 'disable_credential'}, 'activation': 'start', 'cron': '* * * * *',
    })
    first_due = datetime.fromisoformat(saved['next_run'].replace('Z', '+00:00')).timestamp()
    assert saved['timer_active'] and first_due < time.time() + 62

    # Reading only fixture files until the third tick proves browser polling did not revive the scheduler.
    def wait_until(deadline, ready, message):
        while time.time() < deadline:
            if ready():
                return
            time.sleep(.25)
        raise AssertionError(message)

    old_file = root / 'auths' / 'one.json'
    wait_until(first_due + 25, lambda: json.loads(old_file.read_text()).get('disabled'), 'First scheduled run did not suspend one.json')
    assert [c['account'] for c in recorded_calls()] == ['one@example.test']
    # Keep the directory empty of eligible credentials across the second minute boundary.
    time.sleep(max(0, first_due + 65 - time.time()))
    assert len(recorded_calls()) == 1
    added = request('/v8/management/credentials?name=new.json', credential('new'))
    assert (root / 'auths' / 'new.json').is_file(), added
    wait_until(first_due + 145, lambda: len(recorded_calls()) > 1, 'New credential did not reach the fake upstream on the third scheduled tick')
    until = time.monotonic() + 12
    while time.monotonic() < until:
        history = request(prefix + '/runs?rule_id=' + saved['id'])['items']
        if len(history) >= 2 and all(row.get('completed_at') for row in history):
            break
        time.sleep(.2)
    by_auth = {r['config']['auth_id']: r for r in history}
    assert len(history) == 2 and set(by_auth) == {'one.json', 'new.json'}, [(r['config']['auth_id'], r['status']) for r in history]
    assert all(r['source'] == 'scheduled_probe' and r['config'].get('scheduled_dispatch') for r in history)
    before, after = by_auth['one.json'], by_auth['new.json']
    assert before['upstream_condition'] == 'unauthorized' and before['action_status'] == 'credential_suspended'
    assert after['status'] == 'completed' and [s['quality_verdict'] for s in after['samples']] == ['correct']
    assert before['config']['scope_batch_id'] != after['config']['scope_batch_id']
    calls = recorded_calls()
    assert [c['account'] for c in calls] == ['one@example.test', 'new@example.test']
    assert all(not c['disabled'] and c['model'] == model for c in calls)
    assert json.loads(old_file.read_text()).get('disabled') and not json.loads((root / 'auths' / 'new.json').read_text()).get('disabled')
    assert mutation_log() == [('PATCH', '/v8/management/credentials/status')]
    audits = request(prefix + '/history?limit=100')['items']
    rounds = sorted((a for a in audits if a.get('rule_id') == saved['id'] and a['kind'] == 'scope_dispatched'), key=lambda a: a['time'])
    assert [a['detail'] for a in rounds] == ['匹配 1 个凭证，跳过 0 个', '匹配 0 个凭证，跳过 0 个', '匹配 1 个凭证，跳过 0 个'], rounds
    current = next(r for r in request(prefix + '/rules')['items'] if r['id'] == saved['id'])
    assert current['enabled'] and current['timer_active'] and current['revision'] == saved['revision']
    assert current['last_scope_batch'] == after['config']['scope_batch_id']
    assert [r['auth_id'] for r in current['handling']] == ['one.json']

    # Recreate the isolated container on the original volume after missing two minutes.
    # Only read the fake upstream log until the coalesced catch-up actually executes.
    next_due = datetime.fromisoformat(current['next_run'].replace('Z', '+00:00')).timestamp()
    restart(recreate_after=next_due + 65)
    wait_until(next_due + 95, lambda: len(recorded_calls()) >= 3, 'No browser-free catch-up after recreation')
    recovered_check = [r for r in request(prefix + '/runs?rule_id=' + saved['id'])['items'] if r['id'] not in {before['id'], after['id']}]
    assert len(recovered_check) == 1 and recovered_check[0]['config']['auth_id'] == 'new.json'
    assert recovered_check[0]['config'].get('scheduled_dispatch') and len(recorded_calls()) == 3
    assert request(prefix + '/connection')['state'] == 'ready'

    provider_name, provider_key = 'scheduled-provider', 'offline-scheduled-provider-key'
    wire_model = 'fixture-wire-model'
    plan = json.loads((root / 'round.json').read_text())
    plan['provider_keys'] = {'Bearer ' + provider_key: provider_name}
    plan['accounts'][provider_name] = ['4'] * 4
    (root / 'round.json').write_text(json.dumps(plan))
    request('/v8/management/config/api-keys/openai-compatibility', [{
        'name': provider_name, 'base-url': 'http://127.0.0.1:18887/v1',
        'models': [{'name': wire_model, 'alias': model}], 'keys': [{'api-key': provider_key}],
    }], 'PUT')
    wait_until(next_due + 145, lambda: len(recorded_calls()) >= 5, 'Authenticated next tick did not reach both OAuth and new provider route')
    assert {(c['account'], c['model'], c['outcome']) for c in recorded_calls()[3:]} == {
        ('new@example.test', model, '4'), (provider_name, wire_model, '4')}
    assert not any(c['disabled'] for c in recorded_calls()[3:])
    def recovered_receipts():
        entries = request(prefix + '/runs?rule_id=' + saved['id'])['items']
        return [r for r in entries if r['id'] not in {before['id'], after['id'], recovered_check[0]['id']} and r.get('completed_at')]
    wait_until(time.time() + 12, lambda: len(recovered_receipts()) >= 2, 'Missing recovered scheduled receipts')
    new_files = recovered_receipts()
    assert len(new_files) == 2 and len({r['config']['scope_batch_id'] for r in new_files}) == 1
    assert all(r['config'].get('scheduled_dispatch') and [s['quality_verdict'] for s in r['samples']] == ['correct'] for r in new_files)
    provider, = [r for r in new_files if r['config']['auth_id'] != 'new.json']
    assert provider['config']['provider_name'] == provider_name and provider['config']['subject_auth_index']
    assert mutation_log() == [('PATCH', '/v8/management/credentials/status')]
    return {'rule_id': saved['id'], 'revision': current['revision'], 'new_credential': 'new.json', 'scope': current['target_scope'],
            'object_types': current['object_types'], 'timer_active': current['timer_active'], 'next_run': current['next_run'],
            'scheduled_rounds': [{'at': a['time'], 'detail': a['detail']} for a in rounds],
            'scheduled_receipts': [{'id': row['id'], 'auth_id': row['config']['auth_id'], 'scope_batch_id': row['config']['scope_batch_id'],
                                    'action_status': row['action_status']} for row in [before, after]],
            'fake_upstream_accounts': [c['account'] for c in recorded_calls()], 'credential_status_writes': 1,
            'restart_without_panel': {'catchup_run': recovered_check[0]['id'], 'missed_minutes': 2,
                                      'model_calls': 1, 'writes': 0, 'new_calls_on_next_cron_tick': 2},
            'provider_added_while_scheduled': {'provider': provider_name, 'id': provider['config']['auth_id'],
                                              'run_id': provider['id'], 'wire_model': wire_model},
            'calls_to_suspended_credential': 0, 'plugin_requests_between_activation_and_new_probe': 0}
