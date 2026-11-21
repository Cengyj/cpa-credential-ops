"""Exercise current-state quality policy on synthetic credentials in isolated CPA."""
import json
import subprocess
import time


def verify_operations(prefix, request, restart, root, target, credential, calls):
    models = target['models'][:2]
    name, index = target['auth_id'], target['auth_index']
    phases = []
    fault = {'status': 401, 'error': {'code': 'invalid_api_key', 'message': 'Synthetic upstream rejection'}}

    def plan(outcomes):
        next_plan = root / 'round.next'
        next_plan.write_text(json.dumps({'start': len(calls()), 'outcomes': outcomes, 'emit_text_delta': True}))
        next_plan.replace(root / 'round.json')

    def writes():
        path = root / 'writes.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def current(rule):
        return next(row for row in request(prefix + '/rules')['items'] if row['id'] == rule['id'])

    def wait_for(read, accept):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            try:
                value = read()
                if accept(value):
                    return value
            except (subprocess.CalledProcessError, AssertionError, ValueError):
                pass
            time.sleep(.15)
        raise AssertionError('Synthetic CPA state did not converge')

    def finished(run_id):
        return wait_for(lambda: request(prefix + '/runs/detail?id=' + run_id),
                        lambda r: r.get('completed_at') and not r['action_status'].endswith('_pending'))

    def save(label, **extra):
        return request(prefix + '/rules', {
            'name': label, 'object_types': ['oauth'], 'target_scope': 'all_enabled',
            'models': models, 'samples': 2, 'question_id': 'logic-boxes',
            'judge': {'mode': 'deterministic'}, 'parameters': {'max_tokens': 128, 'reasoning_effort': 'high'},
            'action': 'disable_credential', 'auto_restore': True, 'enabled': True, 'activation': 'keep', **extra})

    def run(rule, label, outcomes=None, expected_calls=4, expected_writes=0, recovery=0):
        before, changed = len(calls()), len(writes())
        plan(outcomes or ['4'] * 4)
        saved = current(rule)
        preview = request(prefix + '/rules/scope-preview', saved)
        receipt = request(prefix + '/rules/run', {'id': saved['id'], 'revision': saved['revision']})
        for field in ('count', 'skipped', 'recovery_count'):
            assert preview[field] == receipt[field], (label, preview, receipt)
        assert receipt['recovery_count'] == recovery, (label, receipt)
        rows = []
        if receipt['count']:
            summaries = wait_for(lambda: request(prefix + '/runs?scope_batch_id=' + receipt['scope_batch_id'])['items'],
                                 lambda rows: len(rows) == receipt['count'] + receipt['skipped'] and all(r.get('completed_at') for r in rows))
            rows = [finished(row['id']) for row in summaries if row['status'] != 'skipped']
        assert len(calls()) - before == expected_calls, (label, rows, calls()[before:])
        assert len(writes()) - changed == expected_writes, (label, writes()[changed:])
        phases.append({'phase': label, 'calls': expected_calls, 'writes': expected_writes,
                       'recovery_count': recovery, 'action_status': [r['action_status'] for r in rows]})
        return rows

    def status(disabled):
        request('/v8/management/credentials/status', {'name': name, 'auth_index': index, 'disabled': disabled}, 'PATCH')

    owner = save('Answer owner A')
    peer = save('Error-only peer B', target_scope='all_credentials', action='observe_only', auto_restore=False,
                question_id='custom', prompt='Explain this synthetic fixture.', judge={'mode': 'manual'},
                upstream_error_actions={'unauthorized': 'disable_credential'})
    first, = run(owner, 'answer owner disables', ['4', '4', '4', '5'], expected_writes=1)
    original = current(owner)['handling'][0]['action_id']
    observed, = run(peer, 'peer reads disabled without ownership', recovery=0)
    assert all(s['quality_verdict'] == 'unknown' for s in observed['samples'])
    assert current(owner)['handling'][0]['action_id'] == original and not current(peer).get('handling')
    assert all(c['disabled'] for c in calls()[-4:])
    upgraded, = run(peer, 'peer upstream error overrides owner recovery', [fault], expected_calls=1)
    assert upgraded['upstream_condition'] == 'unauthorized'
    retained, = current(owner)['handling']
    assert retained['action_id'] == original and retained['answer_owner_rule_id'] == owner['id']
    assert retained['error_source']['rule_id'] == peer['id'] and retained['error_source']['run_id'] == upgraded['id']
    run(owner, 'owner excluded by peer error', expected_calls=0)
    run(peer, 'peer excluded by its error', expected_calls=0)
    restart()
    wait_for(lambda: request(prefix + '/settings'), lambda v: v['capabilities']['management_session'])
    run(owner, 'restart preserves error precedence', expected_calls=0)

    # No plugin refresh or acknowledgement between CPA enablement and submission.
    status(False)
    plan(['4'] * 4)
    before, changed = len(calls()), len(writes())
    receipt = request(prefix + '/runs', {'auth_id': name, 'provider': target['provider'], 'models': [models[0]],
                                       'samples': 1, 'question_id': 'logic-boxes', 'judge': {'mode': 'manual'},
                                       'idempotency_key': 'operations-manual-current-enable'})
    manual = finished(receipt['run_id'])
    assert manual['status'] == 'completed' and len(calls()) == before + 1 and len(writes()) == changed, manual
    assert not current(owner).get('handling')
    run(owner, 'enabled object reselected by current scope')
    for question in ('visual-pelican-html', 'custom'):
        error_rule = save('Error without reference ' + question, action='observe_only', auto_restore=False,
                          question_id=question, **({'prompt': 'Synthetic no-answer prompt'} if question == 'custom' else {}),
                          judge={'mode': 'manual'}, upstream_error_actions={'unauthorized': 'disable_credential'})
        run(error_rule, question + ' error stops early', [fault], expected_calls=1, expected_writes=1)
        request(prefix + '/rules', {'id': error_rule['id'], 'revision': error_rule['revision']}, 'DELETE')
        run(owner, 'deleting error rule preserves exclusion', expected_calls=0)
        status(False)
        run(owner, 'current enable retires deleted-rule source')

    # Same entry, new current content: retain answer owner but freeze this round anew.
    run(owner, 'answer suspension before content update', ['5', '4', '4', '4'], expected_writes=1)
    action = current(owner)['handling'][0]['action_id']
    request('/v8/management/credentials?name=' + name,
            {**credential('replacement'), 'disabled': True})
    wait_for(lambda: request(prefix + '/targets')['items'],
             lambda rows: any(r['auth_id'] == name and r.get('email') == 'replacement@example.test' and r['manual_disabled'] for r in rows))
    assert current(owner)['handling'][0]['action_id'] == action
    restored, = run(owner, 'same-entry account replacement uses fresh snapshot', expected_writes=1, recovery=1)
    assert restored['action_status'] == 'credential_restored' and restored['credential_email'] == 'replacement@example.test'
    assert restored['config']['managed_probe']['fingerprint'] != first['config']['managed_probe']['fingerprint']
    assert all(c['disabled'] for c in calls()[-4:])

    run(owner, 'answer suspension before unobserved toggle', ['5', '4', '4', '4'], expected_writes=1)
    action = current(owner)['handling'][0]['action_id']
    status(False)
    status(True)
    assert current(owner)['handling'][0]['action_id'] == action
    run(owner, 'unobserved enable-disable keeps ownership', expected_writes=1, recovery=1)
    assert not json.loads((root / 'auths' / name).read_text()).get('disabled')

    # Ambiguous protocol evidence cannot authorize any credential mutation.
    for action in ('disable_credential', 'delete_credential'):
        strict = save('Ambiguous evidence ' + action, action=action, auto_restore=False)
        for outcome in ('duplicate_status_wrong', 'completion_after_done', 'incomplete_details_wrong', 'joined_ids_wrong'):
            row, = run(strict, action + ' rejects ' + outcome, [outcome] * 4)
            assert all(s['quality_verdict'] == 'unknown' and s['execution_status'] != 'completed'
                       and not s.get('upstream_condition') for s in row['samples']), row
            assert (root / 'auths' / name).is_file() and not json.loads((root / 'auths' / name).read_text()).get('disabled')
    run(owner, 'suspension before ambiguous recovery evidence', ['5', '4', '4', '4'], expected_writes=1)
    for outcome in ('duplicate_status_correct', 'incomplete_details_correct', 'joined_ids_correct'):
        row, = run(owner, outcome + ' cannot restore', [outcome] * 4, recovery=1)
        assert all(s['quality_verdict'] == 'unknown' for s in row['samples'])
        assert json.loads((root / 'auths' / name).read_text())['disabled']
    run(owner, 'fresh unambiguous full round restores', expected_writes=1, recovery=1)
    return {'phases': phases, 'manual_after_cpa_enable_without_refresh': manual['id'],
            'cross_rule_error_source': retained['error_source'], 'model_calls': len(calls()),
            'plugin_writes': writes(), 'temporary_enable_for_probe': False}
