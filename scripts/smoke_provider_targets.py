"""Check configured-provider selection through an isolated, unmodified CPA."""
import http.client
import difflib
import threading
import hashlib
import json
import shutil
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


MODEL = 'fixture-shared-alias'
ANSWERS = {'alpha-1': '11', 'alpha-2': '21', 'beta-1': '31'}
PELICAN_HTML = '''<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Pelican bicycle loop</title>
<style>
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#eef4f1;color:#174e4c;font-family:system-ui,sans-serif}
main{width:min(90vw,760px);padding:28px;border-radius:28px;background:#fff;box-shadow:0 18px 60px #174e4c18}
h1{margin:0 0 6px;font-size:clamp(22px,4vw,32px);letter-spacing:-.04em}p{margin:0;color:#54716d;line-height:1.6}
svg{display:block;width:100%;height:auto;margin-top:18px;border-radius:20px;background:#e1f0eb}
.frame{fill:none;stroke:#157d83;stroke-width:8;stroke-linecap:round;stroke-linejoin:round}
.tire{fill:#fff;stroke:#254e55;stroke-width:7}.spokes{stroke:#71948e;stroke-width:2}
.wheel{animation:spin 1.6s linear infinite;transform-box:fill-box;transform-origin:center}
.pedals{animation:spin 1.6s linear infinite;transform-box:fill-box;transform-origin:center}
.rider{animation:bob .8s ease-in-out infinite alternate}.wing{fill:#c8d9ce;stroke:#6e9488;stroke-width:2}
.leg{fill:none;stroke:#ebad49;stroke-width:7;stroke-linecap:round;animation:pedal .8s ease-in-out infinite alternate}
.far-leg{opacity:.6;animation-delay:-.8s}.road{stroke:#9cbbb0;stroke-width:3;stroke-dasharray:24 18;animation:road 1.6s linear infinite}
.cloud{fill:#fff;opacity:.75}.accent{fill:#9ac8b6}.label{font-size:13px;fill:#54716d;letter-spacing:2px}
@keyframes spin{to{transform:rotate(360deg)}}@keyframes bob{to{transform:translateY(-4px)}}
@keyframes pedal{to{transform:translate(8px,-7px)}}@keyframes road{to{stroke-dashoffset:84}}
@media(prefers-reduced-motion:reduce){.wheel,.pedals,.rider,.leg,.road{animation:none}}
</style>
</head>
<body>
<main>
<h1>A little ride by the water</h1>
<p>A long-beaked pelican pedals steadily, with both wheels turning in a seamless loop.</p>
<svg viewBox="0 0 680 430" role="img" aria-labelledby="scene-title scene-desc">
<title id="scene-title">A pelican riding a bicycle</title>
<desc id="scene-desc">Two rotating wheels, a turquoise frame, moving pedals and a white pelican with a long golden beak.</desc>
<path class="cloud" d="M52 91c0-17 23-26 36-13 10-28 54-24 58 6 24-7 40 19 20 31H58c-16-4-18-17-6-24Z"/>
<path class="cloud" d="M489 75c6-19 30-22 43-8 13-21 48-16 51 8 23-3 34 18 15 29H491c-21-4-18-23-2-29Z"/>
<path class="accent" d="M0 334Q95 285 198 329T398 326T680 315V430H0Z"/>
<path class="road" d="M0 373H680"/>
<g transform="translate(218 313)">
<circle class="tire" r="61"/>
<g class="wheel spokes">
<path d="M0-54V54M-54 0H54M-38-38L38 38M-38 38L38-38"/>
<path d="M-20-50L20 50M-50-20L50 20M-50 20L50-20M-20 50L20-50"/>
</g><circle r="7" fill="#254e55"/>
</g>
<g transform="translate(458 313)">
<circle class="tire" r="61"/>
<g class="wheel spokes">
<path d="M0-54V54M-54 0H54M-38-38L38 38M-38 38L38-38"/>
<path d="M-20-50L20 50M-50-20L50 20M-50 20L50-20M-20 50L20-50"/>
</g><circle r="7" fill="#254e55"/>
</g>
<path class="frame" d="M218 313L285 222L336 313ZM285 222H410L336 313M410 222L458 313M408 222L400 192H429M285 222L277 204"/>
<path d="M259 204H293" stroke="#254e55" stroke-width="10" stroke-linecap="round"/>
<g class="rider">
<path class="leg far-leg" d="M307 205L349 246L323 289"/>
<path fill="#fffdf2" stroke="#6e9488" stroke-width="3" d="M250 178Q252 124 301 126Q339 126 353 171L378 185Q358 222 307 224Q268 222 250 178Z"/>
<path class="wing" d="M267 164Q311 146 338 185Q304 206 277 186Z"/>
<path fill="#fffdf2" stroke="#6e9488" stroke-width="3" d="M329 158Q327 101 358 86Q388 74 405 95Q422 117 396 134L375 137L370 180Z"/>
<path fill="#efb54b" stroke="#b6812c" stroke-width="2" d="M399 108L507 126L401 137Q382 127 399 108Z"/>
<path fill="#f6d893" d="M400 126L483 127L401 135Z"/>
<circle cx="385" cy="107" r="4" fill="#243d40"/>
<path class="leg" d="M315 207L292 258L342 293"/>
</g>
<g transform="translate(336 313)"><circle r="17" fill="#254e55"/><g class="pedals"><path d="M-24-12L24 12" stroke="#ebad49" stroke-width="6"/><path d="M-34-12H-14M14 12H34" stroke="#254e55" stroke-width="6"/></g></g>
<text x="340" y="407" text-anchor="middle" class="label">KEEP ON PEDALLING</text>
</svg>
</main>
</body>
</html>'''


class ProviderFixture:
    def __init__(self, root, config):
        self.root = root
        self.calls_path = root / 'provider-calls.jsonl'
        self.calls_path.touch()
        shutil.copyfile(__file__, root / 'provider-fixture.py')
        auth = {'type': 'codex', 'email': 'ordinary@example.test', 'account_id': 'fixture-file',
                'access_token': 'fixture-file-only', 'expired': '2099-01-01T00:00:00Z'}
        (root / 'auths/ordinary.json').write_text(json.dumps(auth))
        self.credential_hashes = self.file_hashes()
        config['api-keys'] = {
            'openai-compatibility': [
                {'name': name, 'base-url': f'http://127.0.0.1:18887/{name}/v1',
                 'headers': {'X-Fixture-Channel': f'channel-{name}'},
                 'models': [{'name': f'{name}-wire-model', 'alias': MODEL,
                             **({'thinking': {'levels': ['low', 'medium', 'high', 'xhigh', 'max']}} if name == 'alpha' else {})},
                            {'name': f'{name}-business', 'alias': f'{name}-business'}],
                 'keys': [{'api-key': 'fixture-key-' + key} for key in ANSWERS if key.startswith(name + '-')]}
                for name in ('alpha', 'beta')
            ],
            'codex': [{'base-url': 'http://127.0.0.1:18887/codex/v1',
                       'models': [{'name': 'fixture-codex-only', 'alias': 'fixture-codex-only'}],
                       'keys': [{'api-key': 'fixture-key-codex'}]}],
        }
        self.rule = None
        self.checks = []
        self.manual_runs = {}
        self.manual_history = {}
        self.long_streams = []
        self.results = []
        self.batches = []

    def file_hashes(self):
        return {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (self.root / 'auths').glob('*.json')}

    def calls(self):
        return [json.loads(line) for line in self.calls_path.read_text().splitlines()]

    def verify(self, phase, get, fetch, docker, container, curl_image, prefix):
        mock = container + '-providers'
        docker('run', '-d', '--pull=never', '--name', mock, '--network', 'container:' + container,
               '--mount', f'type=bind,source={self.root},target=/probe', curl_image, 'python3', '-u', '/probe/provider-fixture.py')
        try:
            from smoke_ui_package import check_backend_connection
            check_backend_connection(phase, get, fetch, prefix)
            def request(path, body, method='POST', expected=(200, 202)):
                status, data = get(prefix + path, True, body, method=method)
                assert status in expected, (path, status, json.loads(data))
                return json.loads(data)

            targets = fetch(prefix + '/targets')['items']
            assert len(targets) == 5
            file, = [t for t in targets if not t.get('is_provider')]
            assert (file['auth_id'], file['provider']) == ('ordinary.json', 'codex')
            assert file['effective_available'] and file['runtime_status'] == 'active'
            configured = [t for t in targets if t.get('is_provider')]
            for target in configured:
                assert target['provider_name']
                if phase == 'restart' and target['provider_name'] == 'alpha':
                    assert target['runtime_status'] == 'disabled' and target['manual_disabled'] and not target['effective_available'], target
                else:
                    assert target['runtime_status'] == 'registered' and target['effective_available'], target
            codex, = [t for t in configured if t['provider'] == file['provider']]
            assert codex['auth_id'] != file['auth_id'] and 'fixture-codex-only' in codex['models']
            full_config = fetch('/v8/management/config/api-keys')
            routes_digest = hashlib.sha256(json.dumps(full_config, sort_keys=True).encode()).hexdigest()
            by_index = {t['auth_index']: t for t in configured}
            routes = {}
            for group in full_config['openai-compatibility']:
                for key in group['keys']:
                    suffix = key['api-key'].removeprefix('fixture-key-')
                    if 'auth_index' in key:
                        t = by_index[key['auth_index']]
                    else:
                        assert phase == 'restart' and group['name'] == 'alpha' and group.get('disabled'), (phase, group['name'], suffix)
                        # CPA omits runtime-only index decoration for disabled
                        # compatible groups. Verify the exact identity recorded
                        # before this fixture disabled the same unchanged key.
                        previous = self.routes[suffix]
                        t = by_index[previous['auth_index']]
                        assert t['auth_id'] == previous['auth_id'] and t['provider_group_id'] == previous['provider_group_id']
                    assert t['provider_name'] == group['name'] and MODEL in t['models']
                    expected_levels = ['low', 'medium', 'high', 'xhigh', 'max'] if group['name'] == 'alpha' else ['low', 'medium', 'high']
                    assert t['reasoning'][MODEL]['levels'] == expected_levels, (group['name'], t.get('reasoning'))
                    routes[suffix] = t
            assert set(routes) == set(ANSWERS)
            assert not any(f'fixture-key-{suffix}' in json.dumps(targets) for suffix in [*ANSWERS, 'codex'])

            def check_manual_run(key, question_id='logic-candy'):
                source, target, before = 'manual_test', routes[key], len(self.calls())
                long_html = question_id == 'visual-pelican-html'
                expected_answer = PELICAN_HTML if long_html else ANSWERS[key]
                if long_html:
                    before_writes = len(config_writes())
                    before_config = fetch('/v8/management/config/api-keys')
                effort = 'max' if phase == 'restart' else 'high' if key.startswith('alpha-') else 'low'
                receipt = request('/runs', {'auth_id': target['auth_id'], 'provider': target['provider'], 'models': [MODEL],
                                            'samples': 1, 'question_id': question_id, 'judge': {'mode': 'manual'},
                                            'parameters': {'stream': True, 'reasoning_effort': effort}})
                deadline = time.monotonic() + 20
                while True:
                    run = fetch(prefix + '/runs/detail?id=' + receipt['run_id'])
                    if run['status'] not in ('queued', 'running', 'cancelling'):
                        break
                    if time.monotonic() >= deadline:
                        raise AssertionError('Provider test did not finish')
                    time.sleep(.1)
                assert run['status'] == 'completed' and run['source'] == source, (run['status'], run.get('error'), run.get('block_reason'))
                sample, = run['samples']
                channel = key.split('-')[0]
                call, = self.calls()[before:]
                stream_fields = {'answer_bytes', 'answer_sha256', 'wire_bytes', 'sse_frames'} if long_html else set()
                assert {k: v for k, v in call.items() if k not in stream_fields} == {'key': key, 'path': f'/{channel}/v1/chat/completions', 'model': channel + '-wire-model',
                                'channel_header': 'channel-' + channel, 'stream': source == 'manual_test',
                                'reasoning_effort': effort}, (target['auth_id'], call)
                if long_html:
                    assert 3 * 1024 <= len(expected_answer.encode()) <= 5 * 1024
                    assert call['answer_bytes'] == len(expected_answer.encode())
                    assert call['answer_sha256'] == hashlib.sha256(expected_answer.encode()).hexdigest()
                    assert call['wire_bytes'] > 256 * 1024 and call['sse_frames'] == len(expected_answer) + 2, call
                    assert sample['evidence']['path'] == 'provider_native_probe', sample['evidence']
                    assert run['action_status'] == 'not_applicable' and not run.get('credential_action'), run
                    assert len(config_writes()) == before_writes and fetch('/v8/management/config/api-keys') == before_config
                    assert self.file_hashes() == self.credential_hashes and len(self.calls()) == before + 1
                assert sample['execution_status'] == 'completed' and sample['output'].encode() == expected_answer.encode(), {
                    'question_id': question_id, 'execution_status': sample['execution_status'], 'error': sample.get('error'),
                    'reason': sample.get('reason'), 'output_bytes': len(sample['output'].encode()),
                    'expected_bytes': len(expected_answer.encode()), 'action_status': run['action_status'],
                    'credential_action': run.get('credential_action'), 'evidence': sample['evidence'], 'call': call}
                assert not sample.get('comparable') and run['config']['auth_id'] == target['auth_id']
                assert run['config']['parameters']['stream'] and run['config']['judge']['mode'] == 'manual'
                assert run['config']['parameters']['reasoning_effort'] == effort
                if long_html:
                    self.long_streams.append({'phase': phase, 'run_id': run['id'], **{field: call[field] for field in stream_fields},
                                              'evidence_path': sample['evidence']['path'], 'action_status': run['action_status'],
                                              'output_bytes_verified': True, 'upstream_calls': 1, 'config_writes': 0})
                self.manual_runs[run['id']] = {'auth_id': target['auth_id'], 'provider_name': target['provider_name'],
                                               'subject_auth_index': target['auth_index']}
                self.results.append({'phase': phase, 'source': source, **call})

            def config_writes():
                path = self.root / 'config-writes.jsonl'
                return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

            group = routes['alpha-1']['provider_group_id']
            def body(**overrides):
                return dict({'name': 'group test', 'enabled': True, 'object_types': ['provider'],
                        'target_scope': 'single', 'target_group': group, 'models': [MODEL], 'samples': 1,
                        'question_id': 'logic-candy', 'action': 'disable_credential', 'auto_restore': True},
                        **overrides)

            def dispatch(saved, *, calls, writes, conflict=False):
                start, initial_writes = len(self.calls()), len(config_writes())
                receipt = request('/rules/run', {'id': saved['id'], 'revision': saved['revision']})
                assert receipt.get('scope_batch_id') or receipt.get('run_id'), receipt
                url = ('/runs?scope_batch_id=' + receipt['scope_batch_id']) if receipt.get('scope_batch_id') else None
                end = time.monotonic() + 25
                while time.monotonic() < end:
                    rows = fetch(prefix + url)['items'] if url else [fetch(prefix + '/runs/detail?id=' + receipt['run_id'])]
                    if rows and all(r.get('completed_at') for r in rows) and len(config_writes()) >= initial_writes + writes and not any(r['action_status'].endswith('_pending') for r in rows):
                        break
                    time.sleep(.1)
                actual_calls = self.calls()[start:]
                assert sorted(x['key'] for x in actual_calls) == sorted(calls), (actual_calls, [(r['status'],r['action_status'],r.get('error'),r.get('skip_detail'), r.get('preflight_detail')) for r in rows])
                actual_writes = config_writes()[initial_writes:]
                assert len(actual_writes) == writes and all(x['conditional'] and x['status'] == (412 if conflict else 200) for x in actual_writes), (actual_writes, [(r['config']['auth_id'],r['action_status']) for r in rows])
                return rows, receipt

            def business_models():
                status, reply = get('/v1/models')
                assert status == 200, status
                return {m['id'] for m in json.loads(reply)['data']}

            def operator_state(disabled):
                groups = fetch('/v8/management/config/api-keys/openai-compatibility')
                next(g for g in groups if g['name'] == 'alpha')['disabled'] = disabled
                status, _ = get('/v8/management/config/api-keys/openai-compatibility', True, groups, method='PUT')
                assert status == 200
                deadline = time.monotonic() + 8
                while ('alpha-business' in business_models()) == disabled:
                    if time.monotonic() >= deadline:
                        raise AssertionError('CPA business route did not reflect operator change')
                    time.sleep(.1)

            def read_group():
                return next(g for g in fetch('/v8/management/config/api-keys')['openai-compatibility'] if g['name'] == 'alpha')

            if phase == 'load':
                self.routes = routes
                self.routes_digest = routes_digest
                for objects, counts in [(['oauth'],(1,0)), (['provider'],(0,4)), (['oauth','provider'],(1,4))]:
                    preview = request('/rules/scope-preview', body(object_types=objects, target_scope='all_enabled', target_group='', action='observe_only', auto_restore=False))
                    assert preview['verified'] and (preview['candidates']['oauth'],preview['candidates']['provider']) == counts, preview
                blocked = request('/rules', body(auto_restore=False))
                self.rule = request('/rules', body(name='authorized alpha', target_scope='all_enabled', target_group='', authorized_groups=[group]))
                assert not self.calls() and not config_writes()
                for key in ('alpha-2', 'beta-1'):
                    check_manual_run(key)
                check_manual_run('alpha-2', 'visual-pelican-html')
                self.checks.append('manual visual HTML preserved every byte across more than 256 KiB of SSE framing with one native call and no handling')
                rows, _ = dispatch(blocked, calls=['alpha-1','alpha-2'], writes=0)
                assert not read_group().get('disabled')
                self.checks.append('two group members tested with zero writes before explicit group permission')
                rows, _ = dispatch(self.rule, calls=['alpha-1','alpha-2','beta-1'], writes=1)
                assert read_group()['disabled'] and 'alpha-business' not in business_models() and 'beta-business' in business_models()
                self.checks.append('one wrong route disabled the complete group after all its answers')
                disabled = [t for t in fetch(prefix + '/targets')['items'] if t.get('provider_group_id') == group]
                assert len(disabled) == 2 and all(t['manual_disabled'] for t in disabled)
                preview = request('/rules/scope-preview', self.rule)
                assert preview['recovery_count'] == 2, (preview, [(t['auth_id'],t.get('credential_handling'),t['auth_index']) for t in disabled], [(r['config']['auth_id'],r['action_status']) for r in rows], [a for a in fetch(prefix + '/history')['items'] if 'uncertain' in str(a)])
                rows, receipt = dispatch(self.rule, calls=['alpha-1','alpha-2','beta-1'], writes=0)
                assert receipt['recovery_count'] == 2 and read_group()['disabled']
                assert any(s['evidence']['path'] == 'provider_config_probe' for r in rows for s in r['samples'])
                self.checks.append('single passing key left disabled group unchanged; both keys probed while disabled')
                (self.root/'all-alpha-pass').touch()
                rows, _ = dispatch(self.rule, calls=['alpha-1','alpha-2','beta-1'], writes=1)
                assert not read_group().get('disabled')
                self.checks.append('all keys passed and one conditional write restored the group')
                assert {'alpha-business','beta-business'} <= business_models()

                all_objects = request('/rules', body(name='manually disabled', target_scope='all_credentials', target_group='', authorized_groups=[group]))
                observer = request('/rules', body(name='observe manually disabled', target_scope='all_credentials', target_group='', action='observe_only', auto_restore=False))
                operator_state(True)
                rows, receipt = dispatch(observer, calls=['alpha-1','alpha-2','beta-1'], writes=0)
                assert receipt.get('recovery_count',0) == 0 and read_group()['disabled'] and 'alpha-business' not in business_models()
                rows, receipt = dispatch(all_objects, calls=['alpha-1','alpha-2','beta-1'], writes=1)
                assert receipt.get('recovery_count',0) == 2 and not read_group().get('disabled')
                self.checks.append('manual group disable probed in all scope with zero writes; authorized all-pass round restored it')

                (self.root/'all-alpha-pass').unlink()
                (self.root/'competing-write').touch()
                rows, _ = dispatch(self.rule, calls=['alpha-1','alpha-2','beta-1'], writes=1, conflict=True)
                assert any(r['action_status']=='provider_config_conflict' for r in rows) and not read_group().get('disabled')
                self.checks.append('concurrent CPA config edit rejected the conditional write with 412; no retry')
                error_rule = request('/rules', body(name='group upstream errors', authorized_groups=[group], auto_restore=False,
                    action='observe_only', upstream_error_actions={'unauthorized':'disable_credential', 'quota_exhausted':'disable_credential'}))
                self.error_rule = error_rule
                limited = self.root / 'daily-request-limit'
                limited.touch()
                rows, _ = dispatch(error_rule, calls=['alpha-1','alpha-2'], writes=0)
                limited.unlink()
                limited_row, = [row for row in rows if row['config']['auth_id'] == routes['alpha-1']['auth_id']]
                sample, = limited_row['samples']
                assert sample['error'] == 'upstream HTTP 429' and sample['quality_verdict'] == 'unknown'
                assert not sample.get('upstream_condition') and not read_group().get('disabled')
                self.checks.append('daily request rate limit kept the real HTTP 429, called both group members and made zero writes')
                (self.root/'upstream-unauthorized').touch()
                # Group-wide HTTP evidence can stop a sibling before it starts.
                receipt = request('/rules/run', {'id':error_rule['id'],'revision':error_rule['revision']})
                deadline = time.monotonic()+12
                while time.monotonic() < deadline:
                    rows = fetch(prefix + '/runs?scope_batch_id='+receipt['scope_batch_id'])['items']
                    if any(r['action_status']=='credential_suspended' for r in rows): break
                    time.sleep(.1)
                assert any(r['action_status']=='credential_suspended' and r.get('upstream_condition')=='unauthorized' for r in rows)
                assert read_group()['disabled']
                operator_state(False)
                (self.root/'upstream-unauthorized').unlink()
                for _ in range(2):
                    rows, receipt = dispatch(error_rule, calls=['alpha-1','alpha-2'], writes=0)
                    assert receipt['count'] == 2 and receipt['recovery_count'] == 0 and not read_group().get('disabled')
                self.checks.append('CPA manual enable retired old group error source; current rule scope resumed with no acknowledgement')
                (self.root/'upstream-unauthorized').touch()
                receipt = request('/rules/run', {'id':error_rule['id'],'revision':error_rule['revision']})
                deadline = time.monotonic()+12
                while time.monotonic() < deadline:
                    rows = fetch(prefix + '/runs?scope_batch_id='+receipt['scope_batch_id'])['items']
                    if any(r['action_status']=='credential_suspended' for r in rows): break
                    time.sleep(.1)
                assert read_group()['disabled'] and any(r.get('upstream_condition')=='unauthorized' for r in rows)
                self.checks.append('new group upstream error after external enable created a fresh suspension')
            else:
                assert read_group().get('disabled')
                before = len(self.calls()), len(config_writes())
                receipt = request('/rules/run', {'id': self.error_rule['id'], 'revision': self.error_rule['revision']})
                assert receipt['count'] == receipt['skipped'] == 0
                assert before == (len(self.calls()), len(config_writes()))
                operator_state(False)
                (self.root/'upstream-unauthorized').unlink()
                check_manual_run('alpha-1')
                preview = request('/rules/scope-preview', self.rule)
                assert preview['verified'] and preview['recovery_count'] == 0
                self.checks.append('restart preserved error exclusion; direct CPA enable restored native manual admission')
            self.batches = self.checks
            history = fetch(prefix + '/runs?source=manual_test&view=summary')
            assert history['total'] == len(self.manual_runs)
            assert {row['id'] for row in history['items']} == set(self.manual_runs)
            for row in history['items']:
                expected = self.manual_runs[row['id']]
                detail = fetch(prefix + '/runs/detail?id=' + row['id'])
                if detail['config']['question_id'] == 'visual-pelican-html':
                    sample, = detail['samples']
                    assert sample['execution_status'] == 'completed' and sample['output'].encode() == PELICAN_HTML.encode()
                    assert sample['evidence']['path'] == 'provider_native_probe' and not sample['comparable']
                    assert detail['action_status'] == 'not_applicable' and not detail.get('credential_action')
                for record in (row, detail):
                    assert record['source'] == 'manual_test' and record['id'] == row['id']
                    identity = {field: record['config'].get(field) for field in expected}
                    assert identity == expected, (phase, row['id'], expected, identity)
            self.manual_history[phase] = dict(self.manual_runs)
            assert self.file_hashes() == self.credential_hashes
            after_config = fetch('/v8/management/config/api-keys')
            for cfg in (after_config, full_config):
                for entry in cfg['openai-compatibility']:
                    if entry['name'] == 'alpha':
                        for key in entry['keys']:
                            suffix = key['api-key'].removeprefix('fixture-key-')
                            published_index = key.pop('auth_index', None)
                            expected_index = None if entry.get('disabled') else self.routes[suffix]['auth_index']
                            assert published_index == expected_index, ('CPA runtime index decoration', phase, suffix, published_index, expected_index)
                        entry.pop('disabled', None)
            assert after_config == full_config, 'unrelated configuration or routes changed:\n' + '\n'.join(difflib.unified_diff(
                json.dumps(full_config, indent=2, sort_keys=True).splitlines(),
                json.dumps(after_config, indent=2, sort_keys=True).splitlines(),
                fromfile='synthetic-before', tofile='synthetic-after'))
            return {'tested_calls': self.results, 'directory_scopes': self.batches, 'config_writes': config_writes(), 'manual_history': self.manual_history,
                    'long_streams': self.long_streams,
                    'credential_hashes_unchanged': True, 'upstream_config_unchanged': True}
        finally:
            docker('rm', '-f', mock)


class MockUpstream(BaseHTTPRequestHandler):
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        key = self.headers.get('Authorization', '').removeprefix('Bearer fixture-key-')
        key = key if key in ANSWERS else 'unknown'
        answer = '21' if key.startswith('alpha-') and Path('/probe/all-alpha-pass').exists() else ANSWERS.get(key, '')
        if key.startswith('alpha-') and Path('/probe/all-alpha-pass').exists() and request.get('stream'):
            answer = ANSWERS[key]
        stream = request.get('stream') is True
        long_html = stream and any(isinstance(message.get('content'), str) and '鹈鹕' in message['content'] and 'HTML' in message['content']
                                   for message in request.get('messages', []))
        if long_html:
            answer = PELICAN_HTML
        record = {'path': self.path, 'key': key, 'model': request.get('model'), 'channel_header': self.headers.get('X-Fixture-Channel'), 'stream': stream}
        if 'reasoning_effort' in request:
            record['reasoning_effort'] = request['reasoning_effort']
        response = {'id': 'fixture', 'model': request.get('model')}
        if stream:
            response['object'] = 'chat.completion.chunk'
            frames = []
            for text in answer:
                choice = {'index': 0, 'delta': {'content': text}, 'finish_reason': None}
                frames.append(json.dumps({**response, 'choices': [choice]}))
            last = {'index': 0, 'delta': {}, 'finish_reason': 'stop'}
            frames.extend([json.dumps({**response, 'choices': [last]}), '[DONE]'])
            data = ''.join('data: ' + frame + '\n\n' for frame in frames).encode()
            if long_html:
                record.update({'answer_bytes': len(answer.encode()), 'answer_sha256': hashlib.sha256(answer.encode()).hexdigest(),
                               'wire_bytes': len(data), 'sse_frames': len(frames)})
        else:
            response['object'] = 'chat.completion'
            response['choices'] = [{'index': 0, 'message': {'role': 'assistant', 'content': answer}, 'finish_reason': 'stop'}]
            data = json.dumps(response).encode()
        with Path('/probe/provider-calls.jsonl').open('a') as calls:
            calls.write(json.dumps(record) + '\n')
        status = 200 if key in ANSWERS else 401
        if key == 'alpha-1' and Path('/probe/upstream-unauthorized').exists():
            status, data = 401, b'{"error":{"code":"unauthorized"}}'
        elif key == 'alpha-1' and Path('/probe/daily-request-limit').exists():
            status, data = 429, b'{"error":{"code":"quota_exceeded","message":"Requests per day quota reached."}}'
        self.send_response(status)
        self.send_header('Content-Type', 'text/event-stream' if stream else 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class AuditManagement(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def forward(self):
        upstream = http.client.HTTPConnection('127.0.0.1', 18379, timeout=10)
        try:
            body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
            marker = Path('/probe/competing-write')
            if self.command == 'PUT' and '/config/api-keys/' in self.path and marker.exists():
                marker.unlink()
                # Append a YAML comment, as an independent operator saving the
                # config would. Its contents stay semantically identical.
                with Path('/probe/config.yaml').open('a') as f:
                    f.write('\n# concurrent fixture edit\n')
            upstream.request(self.command, self.path, body or None, dict(self.headers))
            response = upstream.getresponse()
            payload = response.read()
            if self.command in ('PUT', 'PATCH', 'DELETE') and '/config/' in self.path:
                with Path('/probe/config-writes.jsonl').open('a') as output:
                    print(json.dumps({'method':self.command,'status':response.status,'conditional':bool(self.headers.get('If-Match'))}), file=output)
            self.send_response(response.status)
            for name, value in response.getheaders():
                if name.lower() in ('etag','content-type','content-encoding'):
                    self.send_header(name, value)
            self.send_header('Content-Length', str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
        finally:
            upstream.close()

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = forward


if __name__ == '__main__':
    audit = ThreadingHTTPServer(('127.0.0.1', 18888), AuditManagement)
    threading.Thread(target=audit.serve_forever, daemon=True).start()
    ThreadingHTTPServer(('127.0.0.1', 18887), MockUpstream).serve_forever()
