"""Verify current-state error suspensions across all scopes and through a restart."""

import json
import subprocess
import time
from collections import Counter
from datetime import datetime, timezone

from smoke_upstream_errors import POSITIVE


FAULTS = {**POSITIVE, 'unsupported-404': ('model_unsupported', 404, 'model_not_found', 'Synthetic model not found')}
SUSPENDED = {*FAULTS, 'catalog'}
ACCOUNTS = [*FAULTS, 'catalog', 'healthy']


def verify_manual_readmission(prefix, request, restart, root, target, recorded_calls, mutation_log):
    """Check the manual entry point with a real CPA state transition and an offline Codex upstream."""
    settings = request(prefix + '/settings')
    assert 'daily_calls' not in settings and 'max_samples' not in settings
    # Old management clients can still submit a complete settings document.
    legacy = {**settings, 'daily_calls': 1, 'max_samples': 1}
    del legacy['capabilities']
    assert request(prefix + '/settings', legacy, 'PUT') == settings
    assert request(prefix + '/settings') == settings
    auth_id, index = target['auth_id'], target['auth_index']
    assert auth_id == 'one.json' and target['provider'] == 'codex' and target['effective_available']
    model = target['models'][0]
    missing = 'fixture-unregistered-model'
    assert missing not in target['models']
    (root / 'round.json').write_text(json.dumps({'start': 0, 'outcomes': ['4'] * 8, 'emit_text_delta': True}))

    def await_value(read, ready):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            value = read()
            if ready(value):
                return value
            time.sleep(.15)
        raise AssertionError(('fixture state did not converge', value))

    def manual(key, allowed=(202,), samples=1):
        return request(prefix + '/runs', {'auth_id': auth_id, 'provider': 'codex', 'models': [model],
                                         'samples': samples, 'question_id': 'logic-boxes', 'judge': {'mode': 'manual'},
                                         'parameters': {'stream': True}, 'idempotency_key': key}, allowed=allowed)

    def completed(receipt):
        row = await_value(lambda: request(prefix + '/runs/detail?id=' + receipt['run_id']), lambda r: r.get('completed_at'))
        sample, = row['samples']
        assert row['status'] == sample['execution_status'] == 'completed' and sample['output'] == '4', (row['status'], sample)
        assert sample['answer_match'] == 'correct' and row['source'] == 'manual_test'
        return row['id']

    baseline = completed(manual('readmission-before-policy'))
    assert len(recorded_calls()) == 1 and not mutation_log()
    create = {'name': 'Synthetic error-policy exclusion', 'target_scope': 'all_enabled',
              'models': [missing], 'samples': 1, 'question_id': 'logic-boxes',
              'action': 'disable_credential', 'upstream_error_actions': {'model_unsupported': 'disable_credential'},
              'enabled': True, 'activation': 'keep', 'idempotency_key': 'readmission-create-rule'}
    rule = request(prefix + '/rules', create)
    assert request(prefix + '/rules', create)['id'] == rule['id']
    def command(key, allowed=(200,)):
        return request(prefix + '/rules/commands?idempotency_key=' + key, allowed=allowed)
    assert command(create['idempotency_key'])['rule_id'] == rule['id']

    def run_rule():
        return request(prefix + '/rules/run', {'id': rule['id'], 'revision': rule['revision']})

    batch = run_rule()['scope_batch_id']
    rows = await_value(lambda: request(prefix + '/runs?scope_batch_id=' + batch)['items'],
                       lambda items: len(items) == 1 and items[0]['action_status'] == 'credential_suspended')
    assert rows[0]['config']['catalog_missing_model'] == missing and not rows[0]['samples']
    def current():
        return next((t for t in request(prefix + '/targets')['items'] if t['auth_id'] == auth_id), None)

    await_value(current, lambda t: t is not None and t['manual_disabled'])
    blocked = manual('readmission-while-disabled', allowed=(409,))
    assert len(recorded_calls()) == 1 and request(prefix + '/runs?source=manual_test')['total'] == 1
    assert mutation_log() == [('PATCH', '/v8/management/credentials/status')]
    res = request('/v8/management/credentials/status', {'name': auth_id, 'auth_index': index, 'disabled': False}, 'PATCH')
    assert res['disabled'] is False
    # The submission itself reads CPA; no acknowledgement or polling unlock is required.
    after = completed(manual('readmission-after-enable'))
    assert len(recorded_calls()) == 2 and all(not c['disabled'] for c in recorded_calls())
    enabled_target = current()
    assert not enabled_target['manual_disabled'] and not enabled_target.get('error_policy_block')
    assert request(prefix + '/runs?rule_id=' + rule['id'])['total'] == 1 and len(recorded_calls()) == 2
    saved = next(r for r in request(prefix + '/rules')['items'] if r['id'] == rule['id'])
    assert saved['enabled'] and not saved.get('handling'), saved
    assert mutation_log() == [('PATCH', '/v8/management/credentials/status')] * 2
    # A fresh round may act again, while the original catalog evidence never replays itself.
    run_key = 'readmission-rule-command-after-enable'
    run_input = {'id': rule['id'], 'revision': rule['revision'], 'idempotency_key': run_key}
    assert command(run_key, allowed=(404,))['error']['code'] == 'command_not_accepted'
    accepted = request(prefix + '/rules/run', run_input, allowed=(202,))
    assert accepted['state'] == 'accepted' and accepted['rule_id'] == rule['id']
    assert request(prefix + '/rules/run', run_input, allowed=(202,))['idempotency_key'] == run_key
    resolved = await_value(lambda: command(run_key), lambda r: r['state'] == 'completed')
    readmitted = resolved['result']
    assert readmitted['count'] == 1 and readmitted['skipped'] == 0 and readmitted['scope_batch_id'] != batch, readmitted
    new_rows = await_value(lambda: request(prefix + '/runs?scope_batch_id=' + readmitted['scope_batch_id'])['items'],
                          lambda items: len(items) == 1 and items[0]['action_status'] == 'credential_suspended')
    assert new_rows[0]['id'] != rows[0]['id'] and not new_rows[0]['samples']
    saved = next(r for r in request(prefix + '/rules')['items'] if r['id'] == rule['id'])
    handling, = saved['handling']
    assert saved['enabled'] and handling['state'] == 'disabled'
    assert mutation_log() == [('PATCH', '/v8/management/credentials/status')] * 3
    plugin_writes = [json.loads(line) for line in (root / 'writes.jsonl').read_text().splitlines()]
    assert len(plugin_writes) == 2 and all(w['disabled'] and w['cpa_status'] == 200 for w in plugin_writes), plugin_writes
    deleted = request(prefix + '/rules', {'id': saved['id'], 'revision': saved['revision']}, 'DELETE')
    assert deleted['ok'], deleted
    restart()
    deadline = time.monotonic() + 25
    while True:
        try:
            listing = request(prefix + '/rules')
            break
        except (subprocess.CalledProcessError, AssertionError, ValueError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(.2)
    assert not listing['items']
    restored = request(prefix + '/settings')
    # The loopback connection recovers asynchronously; compare persisted settings.
    assert {k: v for k, v in restored.items() if k != 'capabilities'} == {
        k: v for k, v in settings.items() if k != 'capabilities'}
    assert command(create['idempotency_key'])['rule_id'] == rule['id']
    assert command(run_key)['result']['scope_batch_id'] == readmitted['scope_batch_id']
    assert request(prefix + '/rules/run', run_input, allowed=(202,))['result']['scope_batch_id'] == readmitted['scope_batch_id']
    retained = request(prefix + '/runs?rule_id=' + saved['id'])
    assert retained['total'] == 1 and retained['items'][0]['id'] == new_rows[0]['id'], retained
    assert request(prefix + '/runs/detail?id=' + rows[0]['id'], allowed=(404,))
    assert request(prefix + '/runs/detail?id=' + new_rows[0]['id'])['action_status'] == 'credential_suspended'
    disabled_after_delete = current()
    assert disabled_after_delete['manual_disabled'] and disabled_after_delete.get('error_policy_block'), disabled_after_delete
    replacement = request(prefix + '/rules', {'name': 'Synthetic peer after delete', 'target_scope': 'all_enabled',
                                               'models': [model], 'samples': 1, 'question_id': 'logic-boxes', 'enabled': True})
    peer = request(prefix + '/rules/scope-preview', replacement)
    assert peer['count'] == 0 and peer['skipped'] == 0, peer
    assert mutation_log() == [('PATCH', '/v8/management/credentials/status')] * 3 and len(recorded_calls()) == 2
    request('/v8/management/credentials/status', {'name': auth_id, 'auth_index': index, 'disabled': False}, 'PATCH')
    peer_enabled = request(prefix + '/rules/scope-preview', replacement)
    assert peer_enabled['count'] == 1 and peer_enabled['skipped'] == 0, peer_enabled
    enabled_after_delete = current()
    assert not enabled_after_delete['manual_disabled'] and not enabled_after_delete.get('error_policy_block')
    assert mutation_log() == [('PATCH', '/v8/management/credentials/status')] * 4 and len(recorded_calls()) == 2
    samples = 33
    start = len(recorded_calls())
    (root / 'round.json').write_text(json.dumps({'start': start, 'outcomes': ['4'] * samples, 'emit_text_delta': True}))
    large = manual('after-restart-large-round', samples=samples)
    result = await_value(lambda: request(prefix + '/runs/detail?id=' + large['run_id']), lambda r: r.get('completed_at'))
    assert result['status'] == 'completed' and result['config']['samples'] == samples
    assert [(s['index'], s['output']) for s in result['samples']] == [(i, '4') for i in range(samples)]
    assert len(recorded_calls()) == start + samples
    # The history window trims only after all selected samples have executed.
    start = len(recorded_calls())
    (root / 'round.json').write_text(json.dumps({'start': start, 'outcomes': ['4'] * samples, 'emit_text_delta': True}))
    large_rule = request(prefix + '/rules', {'name': 'Large observation round', 'models': [model],
                         'target_scope': 'all_enabled', 'samples': samples, 'question_id': 'logic-boxes',
                         'enabled': True, 'action': 'observe_only', 'max_results': 2})
    receipt = request(prefix + '/rules/run', {'id': large_rule['id'], 'revision': large_rule['revision']})
    large_rows = await_value(lambda: request(prefix + '/runs?scope_batch_id=' + receipt['scope_batch_id'])['items'],
                             lambda items: len(items) == 1 and items[0].get('pruned_samples') == samples - 2)
    large_result = request(prefix + '/runs/detail?id=' + large_rows[0]['id'])
    assert large_result['status'] == 'completed' and large_result['config']['samples'] == samples
    assert [s['index'] for s in large_result['samples']] == [samples-2, samples-1]
    assert len(recorded_calls()) == start + samples
    return {'model': model, 'missing_rule_model': missing, 'manual_before_suspension': baseline,
            'large_manual_samples_after_restart': samples,
            'large_rule_samples_after_restart': samples, 'rule_history_window': 2,
            'create_and_run_receipts_survived_rule_delete_and_restart': True,
            'blocked_while_disabled': blocked, 'manual_after_cpa_enablement': after,
            'original_error_run': rows[0]['id'], 'rule_after_enablement': readmitted,
            'fresh_error_run': new_rows[0]['id'], 'retained_disposition': handling['state'], 'original_rule_enabled': saved['enabled'],
            'deleted_owner_error_source_retained_after_restart': True, 'superseded_record_removed': True, 'active_source_evidence_retained': True,
            'peer_rule_preview_after_delete': peer, 'peer_rule_preview_after_external_enable': peer_enabled, 'credential_mutations_for_deletion': 0,
            'fake_model_calls': len(recorded_calls()), 'calls_while_disabled': 0,
            'plugin_disable_writes': 2, 'explicit_external_enable_writes': 2}


def verify_error_suspensions(prefix, request, restart, root, targets, recorded_calls, mutation_log, *, clear_history=False):
    models = ['gpt-6-astra', 'gpt-6.1-sol']
    directory = {row['auth_id'].removesuffix('.json'): row for row in targets}
    assert set(directory) == set(ACCOUNTS)
    assert all(set(models) <= set(row['models']) for name, row in directory.items() if name != 'catalog')
    assert directory['catalog']['models'] and models[1] not in directory['catalog']['models']
    config = {
        'name': 'Synthetic persistent error suspensions', 'object_types': ['oauth'], 'target_scope': 'all_credentials',
        'models': models, 'samples': 2, 'question_id': 'logic-boxes', 'judge': {'mode': 'deterministic'},
        'parameters': {'max_tokens': 128, 'reasoning_effort': 'high'},
        'action': 'disable_credential', 'auto_restore': True,
        'upstream_error_actions': {fault[0]: 'disable_credential' for fault in FAULTS.values()},
        'enabled': True, 'activation': 'keep', 'cron': '* * * * *',
    }
    rule = request(prefix + '/rules', config)
    fixed = request(prefix + '/rules', {**config, 'name': 'Synthetic fixed scope', 'target_scope': 'single',
                                        'auth_id': directory['one']['auth_id'], 'provider': directory['one']['provider']})
    enabled = request(prefix + '/rules', {**config, 'name': 'Synthetic enabled scope', 'target_scope': 'all_enabled'})
    phases = []
    externally_enabled = set()
    read_cutoffs = {}
    monitored_indices = {name: {target['auth_index']} for name, target in directory.items()}

    def current():
        return next(row for row in request(prefix + '/rules')['items'] if row['id'] == rule['id'])

    def manual(selected):
        calls = len(recorded_calls())
        writes_before = mutation_log()
        preview = request(prefix + '/rules/scope-preview', selected)
        assert preview['verified'] and preview['candidates']['provider'] == 0
        assert len(recorded_calls()) == calls and mutation_log() == writes_before
        receipt = request(prefix + '/rules/run', {'id': selected['id'], 'revision': selected['revision']})
        for field in ('candidates', 'count', 'skipped', 'recovery_count'):
            assert receipt[field] == preview[field], (field, preview, receipt)
        assert receipt['recovery_count'] == 0
        return receipt['scope_batch_id']

    def schedule_after(previous):
        next_run = datetime.fromisoformat(current()['next_run'].replace('Z', '+00:00'))
        time.sleep(max(0, min(55, (next_run - datetime.now(timezone.utc)).total_seconds() - 1)))
        deadline = time.monotonic() + 75
        while time.monotonic() < deadline:
            batch = current().get('last_scope_batch')
            if batch and batch != previous:
                return batch
            time.sleep(.5)
        raise AssertionError(('cron did not produce a batch', previous))

    def writes():
        path = root / 'writes.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def target_reads():
        path = root / 'target-reads.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def plan(first=False):
        accounts = {name + '@example.test': ['4'] * 32 for name in directory}
        if first:
            for name, (_, status, code, message) in FAULTS.items():
                accounts[name + '@example.test'][0] = {'status': status, 'error': {'code': code, 'type': code, 'message': message}}
        tmp = root / 'round.next'
        tmp.write_text(json.dumps({'start': len(recorded_calls()), 'accounts': accounts}))
        tmp.replace(root / 'round.json')
        return len(recorded_calls())

    def check(label, selected, batch, start, scheduled=False, first=False):
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            rows = request(prefix + '/runs?scope_batch_id=' + batch)['items']
            if rows and all(row.get('completed_at') and not row['action_status'].endswith('_pending') for row in rows):
                break
            time.sleep(.1)
        else:
            raise AssertionError((label, 'batch did not finish', batch))
        by_name = {row['config']['auth_id'].removesuffix('.json'): row for row in rows}
        assert set(by_name) == (set(ACCOUNTS) if first else {'healthy'} | externally_enabled), (label, {name: row['status'] for name, row in by_name.items()})
        assert len(rows) == len(by_name), (label, 'duplicate activity for a credential', len(rows), by_name.keys())
        assert all(row['config']['rule_id'] == selected['id'] and bool(row['config'].get('scheduled_dispatch')) == scheduled for row in rows)
        seen = recorded_calls()[start:]
        counts = Counter(call['account'].split('@')[0] for call in seen)
        expected = Counter({'healthy': 4})
        if first:
            expected.update(FAULTS.keys())
        else:
            expected.update({name: 4 for name in externally_enabled})
        if counts != expected or any(call['disabled'] for call in seen):
            raise AssertionError({
                'phase': label, 'actual_calls': dict(counts), 'expected_calls': dict(expected),
                'admission': {name: [row['status'], (row.get('preflight_detail') or {}).get('code'), row['action_status']]
                              for name, row in by_name.items()},
                'calls_while_disabled': sum(call['disabled'] for call in seen),
                'confirmed_reenables': [row['name'] for row in writes() if row['method'] == 'PATCH' and not row['disabled'] and row['cpa_status'] == 200],
            })
        assert [sample['quality_verdict'] for sample in by_name['healthy']['samples']] == ['correct'] * 4
        monitored = target_reads()
        if first:
            read_cutoffs.update(dict.fromkeys(SUSPENDED, len(monitored)))
        for name, cutoff in read_cutoffs.items():
            if name in externally_enabled:
                continue
            extra = [entry for entry in monitored[cutoff:]
                     if entry.get('operation') == 'probe' and entry.get('auth_index') in monitored_indices[name]]
            assert not extra, (label, name, extra)
        actions = writes()
        expect_methods = dict.fromkeys(SUSPENDED, 'PATCH')
        assert Counter((w['name'], w['method']) for w in actions) == Counter((name + '.json', method) for name, method in expect_methods.items()), (label, actions)
        assert all(w['cpa_status'] == 200 and (w['method'] == 'DELETE' or w['disabled'] is True) for w in actions)
        cpa_calls = Counter(mutation_log())
        assert cpa_calls == Counter({('PATCH', '/v8/management/credentials/status'): len(SUSPENDED) + len(externally_enabled)}), (label, cpa_calls)
        evidence = {}
        for account in ACCOUNTS:
            assert bool(json.loads((root / 'auths' / (account + '.json')).read_text()).get('disabled')) == (account in SUSPENDED - externally_enabled)
            if first and account in SUSPENDED:
                row = by_name[account]
                assert row['action_status'] == 'credential_suspended' and row['credential_action'] == 'disable_credential', (account, row)
                if account == 'catalog':
                    assert row['status'] == 'completed' and row['config']['catalog_missing_model'] in models and not row['samples'] and not row.get('skip_detail')
                    assert row['preflight_detail']['code'] == 'missing_model'
                else:
                    assert row['upstream_condition'] == FAULTS[account][0] and len(row['samples']) == 1
                evidence[account] = {'run_id': row['id'], 'status': row['status'], 'action_status': row['action_status'],
                                     'preflight_detail': row.get('preflight_detail'), 'upstream_condition': row.get('upstream_condition'),
                                     'catalog_missing_model': row['config'].get('catalog_missing_model'), 'sample_count': len(row['samples'])}
        saved = current()
        assert saved['auto_restore'] and saved['enabled']
        dispatched = saved if selected['id'] == rule['id'] else next(item for item in request(prefix + '/rules')['items'] if item['id'] == selected['id'])
        summary = dispatched['last_scope_dispatch']
        assert dispatched['last_scope_batch'] == batch and summary['matched'] == len(rows) and summary['skipped'] == 0 and not summary.get('details'), (label, summary)
        held = {item['auth_id'].removesuffix('.json'): item['action_id'] for item in saved['handling'] if item['state'] == 'disabled'}
        assert set(held) == SUSPENDED - externally_enabled and all(held.values())
        if phases:
            assert held == {name: action for name, action in phases[0]['owned_actions'].items() if name not in externally_enabled}
        phases.append({'phase': label, 'rule_id': selected['id'], 'target_scope': selected['target_scope'], 'scope_batch_id': batch, 'scheduled': scheduled, 'calls_by_account': dict(counts),
                       'matched': summary['matched'], 'skipped': summary['skipped'], 'healthy_sample_count': len(by_name['healthy']['samples']), 'credential_status_writes_total': len(actions),
                       'owned_actions': held, 'error_credentials': evidence})

    start = plan(first=True)
    initial_batch = manual(rule)
    check('initial error suspensions', rule, initial_batch, start, first=True)
    if clear_history:
        return verify_cleared_rule_history(prefix, request, restart, root, current, fixed, initial_batch,
                                           phases[0], recorded_calls, mutation_log)
    start = plan()
    batch = manual(rule)
    check('manual after suspension', rule, batch, start)
    start = plan()
    receipt = request(prefix + '/rules/run', {'id': fixed['id'], 'revision': fixed['revision']})
    assert receipt['count'] == 0 and receipt['skipped'] == 0 and not receipt.get('run_id') and not receipt.get('scope_batch_id'), receipt
    assert request(prefix + '/runs?rule_id=' + fixed['id'])['total'] == 0
    assert len(recorded_calls()) == start and len(writes()) == len(SUSPENDED)
    for name in ('one',):
        request('/v8/management/credentials/status', {'name': name + '.json', 'auth_index': directory[name]['auth_index'], 'disabled': False}, 'PATCH')
        externally_enabled.add(name)
    fixed_receipts = {'one': receipt}
    start = plan()
    check('all-enabled peer after suspension', enabled, manual(enabled), start)
    start = plan()
    rule = request(prefix + '/rules/timer', {'id': rule['id'], 'revision': rule['revision'], 'enabled': True}, 'PUT')
    batch = schedule_after(batch)
    check('cron before restart', rule, batch, start, scheduled=True)
    before_calls = len(recorded_calls())
    restart_states = restart(await_management=True)
    deadline = time.monotonic() + 30
    while True:
        try:
            if current().get('last_scope_batch') == batch:
                break
        except (subprocess.CalledProcessError, AssertionError, StopIteration, ValueError):
            pass
        if time.monotonic() >= deadline:
            raise AssertionError('Plugin did not recover the previous batch after restart')
        time.sleep(.2)
    assert len(recorded_calls()) == before_calls
    start = plan()
    batch = manual(rule)
    check('manual after restart', rule, batch, start)
    start = plan()
    check('cron after restart', rule, schedule_after(batch), start, scheduled=True)
    request(prefix + '/rules/timer', {'id': rule['id'], 'revision': rule['revision'], 'enabled': False}, 'PUT')
    history = request(prefix + '/runs?source=scheduled_probe&limit=100')['items']
    originals = phases[0]['error_credentials']
    diagnostic_history = {name: [row['id'] for row in history if row['config']['auth_id'] == name + '.json'] for name in originals}
    assert all(ids == [originals[name]['run_id']] for name, ids in diagnostic_history.items() if name not in externally_enabled), diagnostic_history
    for name in externally_enabled:
        assert originals[name]['run_id'] in diagnostic_history[name] and len(diagnostic_history[name]) > 1
        later = [row for row in history if row['config']['auth_id'] == name + '.json' and row['id'] != originals[name]['run_id']]
        assert all(row['action_status'] not in ('credential_suspended', 'credential_restored') and
                   len(row['samples']) == 4 and all(sample['quality_verdict'] == 'correct' for sample in row['samples']) for row in later)
    return {'scope': 'all_credentials', 'fixed_receipts': fixed_receipts, 'restart_authorization_states': restart_states,
            'retained_original_rows': diagnostic_history, 'operator_enabled': sorted(externally_enabled),
            'later_upstream_probes_while_suspended': dict.fromkeys(sorted(SUSPENDED - externally_enabled), 0),
            'state_reads_are_allowed': True,
            'auto_restore': True, 'models': models, 'samples_per_model': 2,
            'rounds': phases, 'cpa_writes': writes(), 'total_fake_model_calls': len(recorded_calls())}


def verify_cleared_rule_history(prefix, request, restart, root, current, fixed, batch, first, recorded_calls, mutation_log):
    """Clear a confirmed error batch without modifying its CPA credentials or disposition history."""
    runs_path = prefix + '/runs?source=scheduled_probe'
    clear_path = prefix + '/rules/run-history'
    old_rows = request(runs_path)['items']
    assert len(old_rows) == len(SUSPENDED) + 1
    before = current()
    calls_before, writes_before = len(recorded_calls()), mutation_log()
    receipt = request(clear_path, method='DELETE')
    assert receipt['archived'] == len(SUSPENDED) and receipt['deleted'] == 1 and receipt['kept'] == 0, receipt

    def check_hidden():
        for path in (prefix + '/runs', runs_path, runs_path + '&view=summary',
                     runs_path + '&rule_id=' + before['id'], runs_path + '&scope_batch_id=' + batch):
            page = request(path)
            assert page['total'] == 0 and not page['items'], (path, page)
        for row in old_rows:
            assert request(prefix + '/runs/detail?id=' + row['id'], allowed=(404,))['error']['code'] == 'not_found'
        rule = current()
        assert rule['revision'] == before['revision'] and rule['enabled'] == before['enabled']
        assert rule['timer_active'] == before['timer_active'] and rule['handling'] == before['handling']
        assert not rule.get('last_scope_batch') and not rule.get('last_run')
        for name in ACCOUNTS:
            disabled = json.loads((root / 'auths' / (name + '.json')).read_text()).get('disabled', False)
            assert disabled == (name in SUSPENDED)
        audit = request(prefix + '/history?limit=100')['items']
        action_ids = set(first['owned_actions'].values())
        preserved = {entry.get('action_id') for entry in audit if entry['kind'] == 'credential_suspended'}
        assert action_ids <= preserved
        return len(preserved)

    confirmed_audits = check_hidden()
    assert mutation_log() == writes_before and len(recorded_calls()) == calls_before
    repeated = request(clear_path, method='DELETE')
    assert not any(repeated.values()), repeated
    restart()
    deadline = time.monotonic() + 30
    while True:
        try:
            current()
            break
        except (subprocess.CalledProcessError, AssertionError, StopIteration, ValueError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(.2)
    assert check_hidden() == confirmed_audits
    excluded = request(prefix + '/rules/run', {'id': fixed['id'], 'revision': fixed['revision']})
    assert excluded['count'] == excluded['skipped'] == 0 and not excluded.get('scope_batch_id'), excluded
    assert mutation_log() == writes_before and len(recorded_calls()) == calls_before
    return {'confirmed_dispositions': first['error_credentials'], 'clear_receipt': receipt, 'repeat_receipt': repeated,
            'public_runs_after_clear_and_restart': 0, 'public_detail_404': len(old_rows),
            'confirmed_audit_entries_retained': confirmed_audits, 'fixed_rule_after_restart': excluded,
            'cpa_writes_for_clear_and_restart': 0, 'model_calls_for_clear_and_restart': 0}
