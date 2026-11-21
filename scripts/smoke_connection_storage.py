"""Exercise management authorization on real read-only and full mounts in an offline CPA."""
import argparse
import json
import secrets
import shutil
import subprocess
import tempfile
import time
import uuid
from pathlib import Path

from smoke_ui_package import CURL, HOST, PREFIX, digest, docker


def check(library, output):
    reports = {}
    for fault in ('readonly', 'full'):
        with tempfile.TemporaryDirectory(prefix='mq-connection-storage-') as tmp:
            root = Path(tmp)
            private = root / 'data/management'
            private.mkdir(mode=0o700, parents=True)
            (root / 'auths').mkdir()
            (root / 'plugins').mkdir()
            shutil.copyfile(library, root / 'plugins/model-quality.so')
            key = secrets.token_urlsafe(32)
            config = {
                'config-version': 8,
                'server': {'host': '127.0.0.1', 'port': 18379, 'discovery': {'enabled': False}, 'kv-store': {'path': '/probe/kv.db'}},
                'management': {'secret-key': key, 'disable-control-panel': True, 'disable-auto-update-panel': True},
                'oauth': {'auth-dir': '/probe/auths'},
                'plugins': {'enabled': True, 'dir': '/probe/plugins', 'configs': {'model-quality': {'enabled': True, 'data_dir': '/probe/data', 'scheduling_enabled': False}}},
            }
            (root / 'config.yaml').write_text(json.dumps(config))
            name = 'mq-auth-storage-' + uuid.uuid4().hex[:12]
            sidecar = name + '-http'
            counts = {'GET': 0, 'PUT': 0, 'DELETE': 0}

            def stop():
                for cid in (sidecar, name):
                    subprocess.run(['docker', 'rm', '-f', cid], capture_output=True)

            def request(method='GET', body=None):
                cmd = ['exec', '-i', sidecar, 'curl', '-qsS', '--noproxy', '*', '--max-time', '3',
                       '-w', '\n%{http_code}', '-H', '@-', '-X', method]
                if body is not None:
                    cmd.extend(['-H', 'Content-Type: application/json', '--data-binary', json.dumps(body)])
                raw = docker(*cmd, 'http://127.0.0.1:18379' + PREFIX + '/connection',
                             input=('Authorization: Bearer ' + key + '\n').encode(), stderr=subprocess.PIPE)
                assert key.encode() not in raw
                data, status = raw.rsplit(b'\n', 1)
                counts[method] += 1
                return int(status), json.loads(data)

            def state(wanted):
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    try:
                        status, value = request()
                        if status == 200 and value.get('state') == wanted:
                            return value
                    except (subprocess.CalledProcessError, ValueError):
                        pass
                    time.sleep(.2)
                raise AssertionError('Timed out reading state ' + wanted)

            def start(mounts):
                docker('run', '-d', '--pull=never', '--network', 'none', '--name', name,
                       '-e', 'MODEL_QUALITY_CPA_URL=http://127.0.0.1:18379', '--mount',
                       f'type=bind,source={root},target=/probe', *mounts, HOST, './CLIProxyAPI',
                       '--config', '/probe/config.yaml', '--standalone', '--local-model')
                docker('run', '-d', '--pull=never', '--network', 'container:' + name, '--name', sidecar,
                       CURL, 'sleep', 'infinity')

            def configure(current):
                status, value = request('PUT', {'mode': 'configure', 'revision': current['revision']})
                assert status == 200 and value['state'] == 'ready' and value['configured']
                assert current['revision'] != value['revision']
                return value

            def check_failure(method, previous):
                body = {'revision': previous['revision']}
                if method == 'PUT':
                    body['mode'] = 'configure'
                status, value = request(method, body)
                assert status == 507 and value['error']['code'] == 'connection_save_failed', (status, value)
                assert not value['error']['outcome_unknown']
                status, value = request()
                assert status == 200 and value == previous

            def installed_digest():
                path = '/probe/data/management/connection.json'
                return docker('exec', name, 'sha256sum', path).split()[0]

            try:
                if fault == 'readonly':
                    mount = ['--mount', f'type=bind,source={private},target=/probe/data/management,readonly']
                    start(mount)
                    initial = state('unconfigured')
                    probe = subprocess.run(['docker', 'exec', '-e', 'LC_ALL=C', name, 'touch',
                                            '/probe/data/management/readonly-probe'], capture_output=True)
                    assert probe.returncode and b'Read-only file system' in probe.stderr, probe.stderr
                    check_failure('PUT', initial)
                    assert not list(private.iterdir())
                    stop()
                    start([])
                    saved = configure(state('unconfigured'))
                    fingerprint = installed_digest()
                    stop()
                    start(mount)
                    current = state('ready')
                    assert current['revision'] == saved['revision'] and current['source'] == 'saved'
                    check_failure('DELETE', current)
                    assert installed_digest() == fingerprint
                else:
                    # All filler bytes are confined to this disposable 64 KiB tmpfs.
                    mount = ['--tmpfs', '/probe/data/management:rw,size=65536,mode=0700']
                    start(mount)
                    initial = state('unconfigured')

                    def fill():
                        result = subprocess.run(['docker', 'exec', '-e', 'LC_ALL=C', name, 'dd', 'if=/dev/zero',
                                                 'of=/probe/data/management/fill', 'bs=4096', 'count=17'],
                                                capture_output=True)
                        assert result.returncode and b'No space left on device' in result.stderr, result.stderr

                    fill()
                    check_failure('PUT', initial)
                    docker('exec', name, 'rm', '/probe/data/management/fill')
                    saved = configure(initial)
                    fingerprint = installed_digest()
                    fill()
                    check_failure('DELETE', saved)
                    assert installed_digest() == fingerprint
                    docker('exec', name, 'rm', '/probe/data/management/fill')
                    status, cleared = request('DELETE', {'revision': saved['revision']})
                    assert status == 200 and cleared['state'] == 'unconfigured'
                    assert not cleared['configured'] and cleared['revision'] != saved['revision']
                reports[fault] = {'requests': counts, 'mount_error': 'EROFS' if fault == 'readonly' else 'ENOSPC',
                                  'failed_first_save': 507, 'failed_clear': 507,
                                  'prior_state_and_record_preserved': True, 'explicit_save_after_recovery': True}
            finally:
                stop()
    output.write_text(json.dumps({'image': HOST, 'network': 'none', 'library_sha256': digest(library.read_bytes()),
                                  'scenarios': reports}, indent=2) + '\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('library', type=Path)
    parser.add_argument('output', type=Path)
    args = parser.parse_args()
    check(args.library, args.output)
