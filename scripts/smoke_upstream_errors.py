"""Upstream-error scenarios for smoke_credential_policy.py --upstream-errors."""

import json
import time
from collections import Counter


# Each file is a distinct synthetic credential in one all-enabled rule batch.
# Controls include the same status family as the quota and model conditions.
POSITIVE = {
    'one': ('unauthorized', 401, 'invalid_api_key', 'Invalid synthetic API key'),
    'payment': ('payment_required', 402, 'billing_required', 'Synthetic payment required'),
    'quota': ('quota_exhausted', 429, 'insufficient_quota', 'Synthetic quota exhausted'),
    'unsupported': ('model_unsupported', 400, 'model_not_found', 'The requested synthetic model is unsupported'),
}
NEGATIVE = {
    'bad-parameter': (None, 400, 'invalid_request_error', 'Invalid temperature parameter'),
    'throttled': (None, 429, 'rate_limit_exceeded', 'Request rate limited, retry later'),
    'request-window': (None, 429, 'quota_exceeded', 'Quota exceeded'),
    'unavailable': (None, 503, 'server_error', 'Synthetic service is unavailable'),
}
CONTROLS = [*NEGATIVE, 'healthy']
INITIAL_ACCOUNTS = [*POSITIVE, *CONTROLS]
MATRIX = {
    'unauthorized': 'disable_credential', 'payment_required': 'delete_credential',
    'quota_exhausted': 'disable_credential', 'model_unsupported': 'delete_credential',
}


def verify(prefix, request, root, targets, credential, recorded_calls, mutation_log):
    models = next(row['models'][:2] for row in targets if row['email'] == 'one@example.test')
    records = []

    def rule(name, actions, answer_action):
        saved = request(prefix + '/rules', {
            'name': name, 'target_scope': 'all_enabled', 'models': models, 'samples': 2,
            'question_id': 'logic-boxes', 'judge': {'mode': 'deterministic'},
            'parameters': {'max_tokens': 128, 'reasoning_effort': 'high'},
            'action': answer_action, 'upstream_error_actions': actions,
            'enabled': True, 'activation': 'keep',
        })
        # Explicit observation and an absent entry both select observation.
        for kind, value in actions.items():
            assert (saved.get('upstream_error_actions') or {}).get(kind, 'observe_only') == value, (kind, saved)
        return saved

    def writes():
        path = root / 'writes.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def file_state(account):
        file = root / 'auths' / (account + '.json')
        return ('disabled' if json.loads(file.read_text()).get('disabled') else 'enabled') if file.exists() else 'deleted'

    def batch(name, saved, subjects, positives, expected, inject=True):
        calls_start, writes_start = len(recorded_calls()), len(writes())
        cpa_start = len(mutation_log())
        patterns = {}
        for account in subjects:
            fault = None
            if inject:
                fault = positives.get(account) or NEGATIVE.get(account)
            if fault:
                _, status, code, message = fault
                body = {'status': status, 'error': {'code': code, 'type': code, 'message': message}}
                if account == 'request-window':
                    body['headers'] = {'X-RateLimit-Remaining-Requests': '0'}
                patterns[account + '@example.test'] = [body, '4', '4', '4']
            else:
                patterns[account + '@example.test'] = ['4'] * 4
            assert file_state(account) == 'enabled', account
        plan = root / 'round.json'
        tmp = plan.with_suffix('.next')
        tmp.write_text(json.dumps({'start': calls_start, 'accounts': patterns}))
        tmp.replace(plan)
        receipt = request(prefix + '/rules/run', {'id': saved['id'], 'revision': saved['revision']})
        assert receipt['count'] == len(subjects), (name, receipt, subjects)
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            all_rows = request(prefix + '/runs?limit=100&scope_batch_id=' + receipt['scope_batch_id'])['items']
            rows = [row for row in all_rows if row['status'] != 'skipped']
            if len(all_rows) == receipt['count'] + receipt['skipped'] and len(rows) == len(subjects) and all(r.get('completed_at') and not r['action_status'].endswith('_pending') for r in rows):
                break
            time.sleep(.1)
        else:
            raise AssertionError(('synthetic batch remained active', name, receipt))
        by_account = {row['credential_email'].split('@')[0]: row for row in rows}
        assert set(by_account) == set(subjects), (name, by_account.keys(), subjects)
        actual = writes()[writes_start:]
        expected_writes = Counter(('PATCH' if state == 'disabled' else 'DELETE', key + '.json') for key, state in expected.items())
        assert Counter((write['method'], write['name']) for write in actual) == expected_writes, (name, expected_writes, actual)
        assert all(write['cpa_status'] == 200 for write in actual), (name, actual)
        for write in actual:
            if write['method'] == 'PATCH':
                account = write['name'].removesuffix('.json')
                assert write['disabled'] is True and write.get('auth_index') == by_account[account]['config']['subject_auth_index'], write
        cpa_writes = mutation_log()[cpa_start:]
        assert Counter(method for method, route in cpa_writes) == Counter(write['method'] for write in actual), (name, cpa_writes, actual)
        received = recorded_calls()[calls_start:]
        assert set(call['account'] for call in received) == set(patterns), (name, received)
        summaries = {}
        for account in subjects:
            row = by_account[account]
            email = account + '@example.test'
            calls = [call for call in received if call['account'] == email]
            proposed = patterns[email]
            outcomes = [call['outcome'] for call in calls]
            assert 0 < len(calls) <= 4 and outcomes == proposed[:len(calls)], (name, account, outcomes)
            assert not any(call['disabled'] for call in calls), (name, account, calls)
            state = expected.get(account, 'enabled')
            assert file_state(account) == state, (name, account, state)
            samples = row['samples']
            assert len(samples) == len(calls), (name, account, row)
            assert row['config'].get('upstream_error_actions', {}) == saved.get('upstream_error_actions', {}), (name, row)
            for i, (sample, call) in enumerate(zip(samples, calls)):
                assert sample['model'] == call['model'] == models[i//2] and sample['index'] == i%2, (name, account, sample)
                if isinstance(call['outcome'], dict):
                    assert sample['execution_status'] != 'completed' and sample['quality_verdict'] == 'unknown', (name, account, sample)
                    condition = positives[account][0] if account in positives else ''
                    assert sample.get('upstream_condition', '') == condition, (name, account, sample)
                    assert sample['error'] == f"upstream HTTP {call['outcome']['status']}", (name, account, sample['error'])
                else:
                    assert sample['execution_status'] == 'completed' and sample['quality_verdict'] == 'correct', (name, account, sample)
                    assert not sample.get('upstream_condition'), (name, account, sample)
            if account in expected:
                end_state = 'credential_suspended' if state == 'disabled' else 'credential_removed'
                action = 'disable_credential' if state == 'disabled' else 'delete_credential'
                assert len(calls) == 1 and row['action_status'] == end_state, (name, account, len(calls), row['action_status'])
                assert row.get('upstream_condition') == positives[account][0] and row.get('credential_action') == action, (name, row)
            else:
                assert len(calls) == 4 and not row.get('credential_action'), (name, account, calls, row['action_status'])
            summaries[account] = {
                'run_id': row['id'], 'action_status': row['action_status'], 'file_after': state,
                'credential_action': row.get('credential_action'), 'upstream_condition': row.get('upstream_condition'),
                'samples': [{key: sample[key] for key in ('model', 'index', 'execution_status', 'quality_verdict', 'error', 'upstream_condition') if key in sample}
                            for sample in samples],
                'injected': proposed[0], 'upstream_calls': len(calls),
            }
        current = next(rule for rule in request(prefix + '/rules')['items'] if rule['id'] == saved['id'])
        assert current['enabled'] and current['revision'] == saved['revision'], (name, current)
        assert all(item['state'] in ('disabled', 'deleted') for item in current.get('handling', [])), (name, current)
        records.append({'batch': name, 'receipt': receipt, 'rule_id': saved['id'],
                        'error_actions': saved.get('upstream_error_actions', {}), 'runs': summaries,
                        'cpa_writes': actual, 'cpa_logged_routes': cpa_writes,
                        'rule_enabled_after': current['enabled'], 'handling_after': current.get('handling', [])})

    observed = rule('Observe each upstream error', dict.fromkeys(MATRIX, 'observe_only'), 'delete_credential')
    batch('explicit observation', observed, INITIAL_ACCOUNTS, POSITIVE, {})
    first = rule('Handle four upstream errors', MATRIX, 'observe_only')
    first_writes = {account: ('disabled' if MATRIX[fault[0]] == 'disable_credential' else 'deleted')
                    for account, fault in POSITIVE.items()}
    batch('direct actions', first, INITIAL_ACCOUNTS, POSITIVE, first_writes)
    batch('next batch without acknowledgement', first, CONTROLS, {}, {}, inject=False)
    reverse = {kind: 'disable_credential' if action == 'delete_credential' else 'delete_credential' for kind, action in MATRIX.items()}
    fresh = {account + '-new': fault for account, fault in POSITIVE.items()}
    for account in fresh:
        request('/v8/management/credentials?name=' + account + '.json', credential(account))
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        ready = {row.get('email') for row in request(prefix + '/targets')['items'] if len(row.get('models', [])) >= 2}
        if all(account + '@example.test' in ready for account in fresh):
            break
        time.sleep(.15)
    else:
        raise AssertionError('New synthetic credential did not register')
    second = rule('Reverse error actions', reverse, 'observe_only')
    second_writes = {account: ('disabled' if reverse[fault[0]] == 'disable_credential' else 'deleted')
                     for account, fault in fresh.items()}
    batch('opposite actions on new accounts', second, [*fresh, *CONTROLS], fresh, second_writes)
    batch('next batch after opposite actions', second, CONTROLS, {}, {}, inject=False)
    for account, state in {**first_writes, **second_writes}.items():
        assert file_state(account) == state
    return {'rounds': records, 'fake_model_calls': len(recorded_calls()), 'cpa_writes': writes(),
            'live_account_calls': 0, 'manual_acknowledgements': 0}
