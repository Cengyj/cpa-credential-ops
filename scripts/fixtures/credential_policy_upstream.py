"""Synthetic chat and Responses endpoints for the isolated credential-policy smoke test."""

import json
import ssl
import sys
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, HTTPServer, ThreadingHTTPServer
from pathlib import Path
from threading import Lock, Thread
from urllib.parse import parse_qs, urlsplit


ROOT = Path('/probe')


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        model = payload['model']
        plan = json.loads((ROOT / 'round.json').read_text())
        calls = ROOT / 'calls.jsonl'
        lines = calls.read_text().splitlines()[plan['start']:] if calls.exists() else []
        for path in (ROOT / 'auths').glob('*.json'):
            try:
                credential = json.loads(path.read_text())
            except FileNotFoundError:
                continue
            if self.headers['Authorization'] == 'Bearer ' + credential['access_token']:
                break
        else:
            name = plan.get('provider_keys', {}).get(self.headers.get('Authorization', ''))
            if not name:
                self.send_error(500, 'Unknown synthetic credential')
                return
            credential = {'email': name}
        if 'accounts' in plan:
            email = credential['email']
            position = sum(json.loads(line)['account'] == email for line in lines)
            outcome = plan['accounts'][email][position]
        else:
            outcome = plan['outcomes'][len(lines)]
        with calls.open('a') as record:
            entry = {'model': model, 'outcome': outcome, 'disabled': bool(credential.get('disabled'))}
            if 'accounts' in plan:
                entry['account'] = email
            record.write(json.dumps(entry) + '\n')

        if isinstance(outcome, dict):
            self.respond('application/json', json.dumps({'error': outcome['error']}).encode(),
                         outcome['status'], outcome.get('headers'))
            return
        if outcome == 'timeout':
            self.send_error(504)
            return
        if self.path.endswith('/chat/completions'):
            choice = {'message': {'role': 'assistant', 'content': outcome}, 'finish_reason': 'stop'}
            body = json.dumps({'model': model, 'choices': [choice]}).encode()
            self.respond('application/json', body)
            return
        if outcome in ('duplicate_status_wrong', 'duplicate_status_correct', 'completion_after_done',
                       'incomplete_details_wrong', 'incomplete_details_correct', 'joined_ids_wrong', 'joined_ids_correct'):
            answer = '4' if outcome.endswith('_correct') else '5'
            response = {'status': 'completed', 'model': model, 'output': [
                {'type': 'message', 'status': 'completed', 'content': [{'type': 'output_text', 'text': answer}]}]}
            if outcome.startswith('incomplete_details_'):
                response['incomplete_details'] = {'reason': 'max_output_tokens'}
            if outcome.startswith('joined_ids_'):
                response['id'] = 'different-response'
            raw = json.dumps({'type': 'response.completed', 'response': response})
            if outcome.startswith('duplicate_status_'):
                raw = raw.replace('"status": "completed"', '"status": "completed", "status": "incomplete"', 1)
            body = ('data: ' + raw + '\n\n').encode()
            if outcome == 'completion_after_done':
                body = b'data: [DONE]\n\n' + body
            if outcome.startswith('joined_ids_'):
                created = {'type': 'response.created', 'response': {'id': 'first-response', 'model': model, 'status': 'in_progress'}}
                body = ('data: ' + json.dumps(created) + '\n\n').encode() + body
            self.respond('text/event-stream', body)
            return
        if outcome == 'truncated':
            events = [{'type': 'response.output_text.delta', 'delta': '4'}]
        else:
            content = {'type': 'output_text', 'text': outcome}
            if outcome == 'refusal':
                content = {'type': 'refusal', 'refusal': 'Synthetic refusal'}
            response = {
                'id': 'offline-response', 'object': 'response', 'status': 'completed', 'model': model,
                'output': [{'type': 'message', 'role': 'assistant', 'status': 'completed', 'content': [content]}],
                'usage': {'input_tokens': 2, 'output_tokens': 1, 'total_tokens': 3},
            }
            message = response['output'][0]
            if outcome in ('commentary_then_final', 'commentary_only', 'missing_item_status'):
                message.update(phase='commentary', content=[{'type': 'output_text', 'text': '5'}])
                if outcome != 'commentary_only':
                    response['output'].append({**message, 'phase': 'final_answer', 'content': [{'type': 'output_text', 'text': '4'}]})
                if outcome == 'missing_item_status':
                    for item in response['output']:
                        del item['status']
            elif outcome == 'ambiguous_answers':
                message['content'] = [{'type': 'output_text', 'text': '4'}]
                response['output'].append(dict(message))
            elif outcome == 'user_message':
                message.update(role='user', content=[{'type': 'output_text', 'text': '4'}])
            events = [{'type': 'response.completed', 'response': response}]
            if plan.get('emit_text_delta'):
                events.insert(0, {'type': 'response.output_text.delta', 'delta': outcome})
        body = ''.join('data: ' + json.dumps(event) + '\n\n' for event in events).encode()
        self.respond('text/event-stream', body)

    def respond(self, media_type, body, status=200, headers=None):
        self.send_response(status)
        self.send_header('Content-Type', media_type)
        self.send_header('Content-Length', str(len(body)))
        for name, value in (headers or {}).items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)


class RecordingRelay(BaseHTTPRequestHandler):
    lock = Lock()

    def log_message(self, *args):
        pass

    def forward(self):
        route = urlsplit(self.path)
        if not route.path.startswith('/v8/management/'):
            self.send_error(404)
            return
        data = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        access = None
        if self.command == 'POST' and route.path == '/v8/management/requests/api-call':
            access = {'operation': 'probe', 'auth_index': json.loads(data).get('auth_index')}
        if self.command == 'GET' and route.path == '/v8/management/credentials/download':
            access = {'operation': 'file_read', 'name': parse_qs(route.query).get('name', [None])[0]}
        if access:
            with self.lock, (ROOT / 'target-reads.jsonl').open('a') as stream:
                stream.write(json.dumps(access) + '\n')
        conn = HTTPConnection('127.0.0.1', 18379, timeout=15)
        try:
            conn.request(self.command, self.path, data, {
                'Content-Type': 'application/json', 'Authorization': self.headers['Authorization'],
            })
            response = conn.getresponse()
            body = response.read()
        finally:
            conn.close()
        write = None
        if self.command == 'PATCH' and route.path == '/v8/management/credentials/status':
            change = json.loads(data)
            write = {'method': 'PATCH', 'name': change['name'], 'auth_index': change.get('auth_index'),
                     'disabled': change['disabled']}
        elif self.command == 'DELETE' and route.path == '/v8/management/credentials':
            write = {'method': 'DELETE', 'name': parse_qs(route.query).get('name', [None])[0]}
        if write:
            write['cpa_status'] = response.status
            with self.lock, (ROOT / 'writes.jsonl').open('a') as stream:
                stream.write(json.dumps(write) + '\n')
        self.send_response(response.status)
        self.send_header('Content-Type', response.getheader('Content-Type', 'application/json'))
        if response.getheader('ETag'):
            self.send_header('ETag', response.getheader('ETag'))
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = do_PATCH = do_DELETE = forward


if __name__ == '__main__':
    if '--business-tls' in sys.argv:
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(ROOT / 'fixture-cert.pem', ROOT / 'fixture-key.pem')
        business = HTTPServer(('127.0.0.1', 443), Upstream)
        business.socket = tls.wrap_socket(business.socket, server_side=True)
        Thread(target=business.serve_forever, daemon=True).start()
    if '--record-writes' in sys.argv:
        relay = ThreadingHTTPServer(('127.0.0.1', 18479), RecordingRelay)
        Thread(target=relay.serve_forever, daemon=True).start()
    HTTPServer(('127.0.0.1', 18887), Upstream).serve_forever()
