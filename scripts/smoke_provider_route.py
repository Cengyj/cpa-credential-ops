"""Exercise CPA's per-key wildcard disable/restore with two real, isolated routes."""
import argparse
import json
import shutil
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from smoke_provider_targets import AuditManagement


MODEL = 'route-scope-fixture'
WIRE = 'fixture-wire'
KEY_PREFIX = 'test-route-secret-'


class ResponsesFixture(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        key = self.headers.get('Authorization', '').removeprefix('Bearer ' + KEY_PREFIX)
        if self.path.split('?')[0] != '/v1/responses' or key not in ('first', 'second'):
            self.send_error(400)
            return
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        if body['model'] != WIRE or self.headers.get('X-Fixture-Channel') != 'channel-' + key:
            self.send_error(400)
            return
        # Read the configuration's actual applied model view while a recovery
        # sample is in flight. This endpoint makes no model calls.
        import urllib.request
        with urllib.request.urlopen('http://127.0.0.1:18379/v1/models', timeout=3) as response:
            business_models = {model['id'] for model in json.load(response)['data']}
        event = {'route': key, 'wire_model': body['model'], 'first_business': 'first-business' in business_models,
                 'second_business': 'second-business' in business_models}
        with Path('/probe/route-calls.jsonl').open('a') as out:
            print(json.dumps(event), file=out)
        correct = key == 'second' or Path('/probe/pass-first').exists()
        terminal = {'type': 'response.completed', 'response': {'status': 'completed','model': WIRE,
                    'output': [{'type':'message','status':'completed','role':'assistant',
                                'content':[{'type':'output_text','text':'21' if correct else '22'}]}]}}
        payload = ('data: ' + json.dumps(terminal) + '\n\ndata: [DONE]\n\n').encode()
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


class RouteFixture:
    def __init__(self, root, config):
        self.root = root
        self.rule = None
        self.rounds = []
        for script in (Path(__file__), Path(__file__).with_name('smoke_provider_targets.py')):
            shutil.copyfile(script, root / script.name)
        config['plugins']['configs']['model-quality']['workers'] = 1
        config['api-keys'] = {'codex': [{'name': 'Codex fake', 'base-url': 'http://127.0.0.1:18887/v1',
                    'excluded-models': ['legacy-*'], 'keys': [
                        {'api-key': KEY_PREFIX + key, 'headers': {'X-Fixture-Channel': 'channel-' + key},
                         'models': [{'name': WIRE, 'alias': MODEL}, {'name': key + '-wire', 'alias': key + '-business'}],
                         **({'excluded-models': ['other-*']} if key == 'second' else {})}
                        for key in ('first', 'second')]}]}

    def records(self, name):
        path = self.root / name
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def verify(self, phase, get, fetch, docker, container, curl_image, prefix):
        mock = container + '-per-route'
        docker('run', '-d', '--pull=never', '--name', mock, '--network', 'container:' + container,
               '--mount', f'type=bind,source={self.root},target=/probe', curl_image,
               'python3', '-u', '/probe/smoke_provider_route.py', '--serve')
        try:
            from smoke_ui_package import check_backend_connection
            check_backend_connection(phase, get, fetch, prefix)
            def request(path, body):
                status, raw = get(prefix + path, True, body, method='POST')
                assert status in (200, 202), (path, status, raw[:300])
                return json.loads(raw)

            def keys():
                group, = fetch('/v8/management/config/api-keys/codex')
                return group['keys']

            def business():
                status, raw = get('/v1/models')
                assert status == 200
                return {model['id'] for model in json.loads(raw)['data']}

            def dispatch(expect_writes, first_enabled, recovery):
                before = len(self.records('route-calls.jsonl'))
                old_writes = len(self.records('config-writes.jsonl'))
                preview = request('/rules/scope-preview', self.rule)
                assert preview['verified'] and preview['recovery_count'] == recovery, preview
                assert len(self.records('route-calls.jsonl')) == before
                receipt = request('/rules/run', {'id': self.rule['id'], 'revision': self.rule['revision']})
                assert receipt['recovery_count'] == recovery, receipt
                end = time.monotonic() + 30
                while time.monotonic() < end:
                    rows = fetch(prefix + '/runs?scope_batch_id=' + receipt['scope_batch_id'])['items']
                    if len(rows) == 2 and all(r.get('completed_at') and not r['action_status'].endswith('_pending') for r in rows):
                        break
                    time.sleep(.1)
                else:
                    raise AssertionError('isolated route batch did not finish')
                calls = self.records('route-calls.jsonl')[before:]
                writes = self.records('config-writes.jsonl')[old_writes:]
                assert sorted(c['route'] for c in calls) == ['first', 'second'], (calls, [(r['status'], r['action_status'], r.get('skip_detail'), r.get('error')) for r in rows])
                assert all(c['second_business'] for c in calls), calls
                if recovery:
                    assert not next(c for c in calls if c['route'] == 'first')['first_business'], calls
                assert len(writes) == expect_writes and all(w['method'] == 'PUT' and w['status'] == 200 and w['conditional'] for w in writes), (writes, [(r['action_status'],r.get('error')) for r in rows])
                a,b = keys()
                assert b == self.before_keys[1]
                assert ('first-business' in business()) == first_enabled and 'second-business' in business()
                if first_enabled:
                    assert a == self.before_keys[0]
                else:
                    assert a['excluded-models'] == ['legacy-*', '*'] and 'excluded-models' not in self.before_keys[0]
                assert all(r['samples'][0]['evidence']['path'] == 'provider_config_probe' for r in rows)
                self.rounds.append({'phase':phase,'recovery_count':recovery,'first_enabled_after':first_enabled,
                                    'config_writes':writes,'upstream_calls':calls})

            if phase == 'load':
                self.before_keys = keys()
                candidates = fetch(prefix + '/targets')['items']
                assert len(candidates) == 2 and all(t['provider_disable_unit'] == 'route' and MODEL in t['models'] for t in candidates), candidates
                self.rule = request('/rules', {'name':'Individual route status', 'enabled':True, 'object_types':['provider'],
                                    'target_scope':'all_enabled','models':[MODEL], 'samples':1,'question_id':'logic-candy',
                                    'action':'disable_credential','auto_restore':True})
                assert not self.records('route-calls.jsonl') and not self.records('config-writes.jsonl')
                dispatch(1, False, 0)
                dispatch(0, False, 1)
            else:
                assert 'first-business' not in business() and 'second-business' in business()
                (self.root/'pass-first').touch()
                dispatch(1, True, 1)
                assert keys() == self.before_keys
            return {'per_route_rounds':self.rounds, 'original_exclusion_inheritance_restored':phase=='restart'}
        finally:
            docker('rm', '-f', mock)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--serve', action='store_true')
    parser.add_argument('--package', type=Path)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    if args.serve:
        audit = ThreadingHTTPServer(('127.0.0.1', 18888), AuditManagement)
        threading.Thread(target=audit.serve_forever, daemon=True).start()
        ThreadingHTTPServer(('127.0.0.1', 18887), ResponsesFixture).serve_forever()
    else:
        from smoke_ui_package import check
        if not (args.package and args.output):
            parser.error('specify --package and --output')
        check(args.package, args.output, provider_targets=True, provider_fixture=RouteFixture)
