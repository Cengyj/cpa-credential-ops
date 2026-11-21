import contextlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import prepare_official_context
import release_support


class PrivateSourceInventoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'checkout'
        self.private = {
            'plugin/local-volume/management/connection.json',
            'plugin/local-volume/management/.connection-123.json',
            'login-runtime-go/runtime-volume/management/connection.json',
            'login-runtime-go/runtime-volume/management/.connection-123',
            'plugin/nested/model-quality-data/accounts/job.json',
            'plugin/arbitrary-volume/accounts/security/fake-job.json',
            'plugin/nested/auths/fake-token.json',
            'login-runtime-go/plugin-data/fake-job.json',
            'plugin/local-config/config.yaml',
        }
        self.public = {
            'plugin/management/routes.go',
            'plugin/local-volume/management/connection.schema.json',
            'plugin/settings/connection.json',
            'login-runtime-go/maps/management/metadata.json',
        }
        for name in self.private | self.public | {
            'scripts/build_official_plugin.py', 'deploy/docker/build_plugin.py', 'deploy/docker/Dockerfile',
        }:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('synthetic fixture')
        baseline = self.root / 'host-patches/official-8.0.76-baseline.json'
        baseline.parent.mkdir()
        baseline.write_text(json.dumps({'sha256': {}}))

    def assert_source_only(self, names):
        self.assertEqual(self.private & names, set())
        self.assertEqual(self.public - names, set())

    def test_official_docker_context_and_manifest(self):
        context = self.root.parent / 'context'
        with patch.object(prepare_official_context, 'ROOT', self.root), contextlib.redirect_stdout(io.StringIO()):
            prepare_official_context.prepare(context)
        names = {p.relative_to(context).as_posix() for p in context.rglob('*') if p.is_file()}
        self.assert_source_only(names)
        manifest = json.loads((context.parent / 'context-manifest.json').read_text())
        self.assert_source_only(set(manifest['source_sha256']))

    def test_release_snapshots(self):
        names = set(release_support.snapshot('plugin', self.root)) | set(release_support.snapshot('runtime', self.root))
        self.assert_source_only(names)

    def test_git_ignores_only_runtime_records(self):
        subprocess.run(['git', 'init', '-q', self.root], check=True)
        (self.root / '.gitignore').write_text((SCRIPTS.parent / '.gitignore').read_text())
        result = subprocess.run(['git', '-C', self.root, 'check-ignore', '--no-index', '--stdin'],
                                input='\n'.join(sorted(self.private | self.public)) + '\n',
                                capture_output=True, text=True)
        self.assertIn(result.returncode, (0, 1))
        self.assertEqual(set(result.stdout.splitlines()), self.private)


if __name__ == '__main__':
    unittest.main()
