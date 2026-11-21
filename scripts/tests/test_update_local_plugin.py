"""Isolated deployment-script tests. Docker and HTTP are always mocked."""
import contextlib
import importlib.util
import io
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from urllib.error import HTTPError
from unittest.mock import patch

SCRIPT_ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('update_local_plugin', SCRIPT_ROOT / 'update_local_plugin.py')
update = importlib.util.module_from_spec(spec)
with patch.object(sys, 'path', [str(SCRIPT_ROOT), *sys.path]):
    spec.loader.exec_module(update)


class PublicAssetVerificationTests(unittest.TestCase):
    def test_public_asset_reader_stays_on_the_requested_response(self):
        visited = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                visited.append(self.path)
                if self.path == '/resource':
                    self.send_response(302)
                    self.send_header('Location', '/different-source')
                else:
                    self.send_response(200)
                self.end_headers()

        with ThreadingHTTPServer(('127.0.0.1', 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                with self.assertRaises(HTTPError) as caught:
                    with update.HTTP.open(f'http://127.0.0.1:{server.server_port}/resource', timeout=2):
                        pass
                with caught.exception as reply:
                    self.assertEqual(reply.code, 302)
            finally:
                server.shutdown()
                thread.join()
        self.assertEqual(visited, ['/resource'])

    def test_unchanged_and_stale_revalidation_and_module_type(self):
        assets = {name: update.digest(name.encode()) for name in update.NAMES}
        for fault in ('stale returns 304', 'module served as text'):
            with self.subTest(fault=fault):
                def get(path, provided=None):
                    name = path.rsplit('/', 1)[-1]
                    etag = f'W/"{assets[name]}"'
                    media = {'index.html': 'text/html', 'style.css': 'text/css'}.get(name, 'application/javascript')
                    if fault == 'module served as text' and name == 'client.js':
                        media = 'text/plain'
                    headers = {'Cache-Control': 'private, no-cache', 'ETag': etag, 'Content-Type': media}
                    if provided == etag or provided and fault == 'stale returns 304':
                        return 304, b'', headers
                    return 200, name.encode(), headers
                with self.assertRaises(ValueError):
                    update.verify_public_assets(assets, True, get)

WRITER = r'''package main
import (
    "crypto/sha256"
    "fmt"
    "encoding/json"
    "os"
    bolt "go.etcd.io/bbolt"
)
func main() {
    db, err := bolt.Open(os.Args[1], 0600, nil); if err != nil { panic("fixture open") }
    err = db.Update(func(tx *bolt.Tx) error {
        b, e := tx.CreateBucketIfNotExists([]byte("synthetic-accounts")); if e != nil { return e }
        switch os.Args[2] {
        case "seed":
            if e = b.Put([]byte("synthetic-key"), []byte("synthetic-secret-only-for-tests")); e != nil { return e }
            child, e := b.CreateBucketIfNotExists([]byte("nested")); if e != nil { return e }
            if e = child.SetSequence(5); e != nil { return e }
            return child.Put([]byte("empty"), []byte{})
        case "qualityseed", "qualitycomplete":
            for _, name := range []string{"meta", "rules", "runs", "audit"} {
                if _, e = tx.CreateBucketIfNotExists([]byte(name)); e != nil { return e }
            }
            if e = tx.Bucket([]byte("meta")).Put([]byte("schema"), []byte("2")); e != nil { return e }
            rule, run, disposition := `{"object_types":null}`, `{"status":"queued"}`, `{"state":"disable_pending"}`
            if os.Args[2] == "qualitycomplete" {
                rule, run, disposition = `{"object_types":["oauth"]}`, `{"status":"interrupted"}`, `{"state":"attention"}`
            }
            if e = tx.Bucket([]byte("rules")).Put([]byte("synthetic-rule"), []byte(rule)); e != nil { return e }
            if e = tx.Bucket([]byte("runs")).Put([]byte("synthetic-run"), []byte(run)); e != nil { return e }
            return tx.Bucket([]byte("meta")).Put([]byte("credential-disposition:synthetic"), []byte(disposition))
        case "inputdigest": return tx.Bucket([]byte("meta")).Put([]byte("config_input"), []byte(fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(`{"workers":2}`)))))
        case "matchinput": return tx.Bucket([]byte("meta")).Put([]byte("quality_host_input"), []byte(`{"workers":2}`))
        case "otherinput": return tx.Bucket([]byte("meta")).Put([]byte("quality_host_input"), []byte(`{"workers":3}`))
        case "scheduledseed":
            if e = tx.Bucket([]byte("meta")).Put([]byte("settings"), []byte(`{"scheduling_enabled":true}`)); e != nil { return e }
            if e = tx.Bucket([]byte("audit")).Put([]byte("existing-audit"), []byte(`{"kind":"rule_saved"}`)); e != nil { return e }
            return tx.Bucket([]byte("rules")).Put([]byte("synthetic-rule"), []byte(`{"id":"synthetic-rule","enabled":true,"scheduling_enabled":true,"status":"detecting","object_types":["oauth"],"next_run":"2026-11-21T01:00:00Z"}`))
        case "scheduledempty", "schedulednonempty", "scheduledrun", "scheduledpermission":
            rules := tx.Bucket([]byte("rules"))
            var rule map[string]any
            if e = json.Unmarshal(rules.Get([]byte("synthetic-rule")), &rule); e != nil { return e }
            rule["next_run"] = "2026-11-21T01:01:00Z"
            if os.Args[2] == "scheduledpermission" { rule["enabled"] = false }
            raw, e := json.Marshal(rule); if e != nil { return e }
            if e = rules.Put([]byte("synthetic-rule"), raw); e != nil { return e }
            audit := map[string]any{"id":"new-audit","time":"2026-11-21T01:00:00Z","rule_id":"synthetic-rule","kind":"scope_dispatched","detail":"匹配 0 个凭证，跳过 0 个"}
            if os.Args[2] == "schedulednonempty" { audit["detail"] = "匹配 1 个凭证，跳过 0 个" }
            if os.Args[2] == "scheduledrun" { audit["run_id"] = "unexpected-run" }
            raw, e = json.Marshal(audit); if e != nil { return e }
            return tx.Bucket([]byte("audit")).Put([]byte("new-audit"), raw)
        case "auditrewrite": return tx.Bucket([]byte("audit")).Put([]byte("existing-audit"), []byte(`{"kind":"scope_dispatched","detail":"匹配 0 个凭证，跳过 0 个"}`))
        case "runadded": return tx.Bucket([]byte("runs")).Put([]byte("new-run"), []byte(`{"status":"queued"}`))
        case "ruleremoved": return tx.Bucket([]byte("rules")).Delete([]byte("synthetic-rule"))
        case "authorizationchanged": return tx.Bucket([]byte("meta")).Put([]byte("settings"), []byte(`{"scheduling_enabled":false}`))
        case "value": return b.Put([]byte("synthetic-key"), []byte("synthetic-changed-value"))
        case "sequence": return b.SetSequence(b.Sequence()+1)
        case "bucket": _, e := b.CreateBucketIfNotExists([]byte("empty-bucket")); return e
        }
        return nil
    }); if err != nil { panic("fixture update") }; if db.Close() != nil { panic("fixture close") }
}
'''


class LocalUpdateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.private = tempfile.TemporaryDirectory(prefix='cpa-update-script-tests-')
        cls.base = Path(cls.private.name)
        cls.inspector = update.build_inspector(cls.base)
        source = cls.base / 'fixture.go'
        source.write_text(WRITER)
        cls.writer = cls.base / 'fixture-writer'
        subprocess.run(['go', 'build', '-o', str(cls.writer), str(source)], cwd=update.ROOT / 'plugin',
                       check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    @classmethod
    def tearDownClass(cls):
        cls.private.cleanup()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=self.base)
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.root = self.directory / 'workspace'
        self.home = self.directory / 'private-home'
        self.home.mkdir(mode=0o700)
        self.runtime = self.root / 'runtime'
        for relative in ('config', 'auths', 'plugins', 'plugin-data/model-quality/accounts'):
            (self.runtime / relative).mkdir(parents=True)
        (self.runtime / 'config/config.yaml').write_text(json.dumps({
            'oauth': {'auth-dir': '/root/.cli-proxy-api'},
            'plugins': {'dir': '/CLIProxyAPI/plugins', 'configs': {'model-quality': {'data_dir': '/CLIProxyAPI/plugin-data/model-quality'}}}}))
        self.auth = self.runtime / 'auths/account.json'
        self.auth.write_text('synthetic credential unchanged')
        self.kv = self.runtime / 'auths/kv.db'
        self.write_store(self.kv, 'seed')
        self.old_library = self.runtime / 'plugins/model-quality.so'
        self.old_library.write_bytes(b'previous-library')
        self.accounts = self.runtime / 'plugin-data/model-quality/accounts'
        (self.accounts / 'vault.key').write_bytes(b'synthetic-vault-key')
        self.store = self.accounts / update.STORE_NAME
        self.write_store(self.store, 'seed')
        self.original_db = update.digest(self.store.read_bytes())
        self.quality = self.runtime / 'plugin-data/model-quality/quality.db'
        self.receipts = self.accounts / 'login-receipts.db'
        self.write_store(self.quality, 'qualityseed')
        self.write_store(self.receipts, 'seed')
        self.runtime_file = self.runtime / 'plugin-data/model-quality/runtime-existing.bin'
        self.runtime_file.write_bytes(b'keep-existing-runtime')
        self.package = self.directory / 'package'
        self.package.mkdir()
        (self.package / 'model-quality.so').write_bytes(b'new-library')
        ui = self.root / 'plugin/ui'
        ui.mkdir(parents=True)
        legacy_names = ('index.html', 'app.js', 'client.js', 'view.js', 'login.js', 'style.css')
        self.assets = {name: ('synthetic asset ' + name).encode() for name in legacy_names}
        for name, value in self.assets.items():
            (ui / name).write_bytes(value)
        quality_source = self.root / 'plugin/quality'
        quality_source.mkdir()
        (quality_source / 'types.go').write_text('const Version = "0.30.10"\n')
        self.manifest = {'platform': 'linux', 'arch': 'arm64', 'plugin_version': '0.30.10',
                         'ui_version': 'fixture-ui', 'runtime_version': 'fixture-runtime',
                         'checksums': {'model-quality.so': update.digest(b'new-library')},
                         'ui_source_sha256': {name: update.digest(value) for name, value in self.assets.items()}}
        (self.package / 'build-manifest.json').write_text(json.dumps(self.manifest))
        self.container = {'Id': 'original-container-id', 'Image': 'original-image-id', 'State': {'Running': True},
                          'Config': {'Cmd': ['./CLIProxyAPI', '--config', '/CLIProxyAPI/config/config.yaml', '--local-model']},
                          'NetworkSettings': {'Ports': {'8317/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '8317'}]}},
                          'Mounts': [{'Destination': destination, 'Source': str(self.runtime / relative), 'Type': 'bind', 'RW': True}
                                     for destination, relative in [('/CLIProxyAPI/config', 'config'), ('/root/.cli-proxy-api', 'auths'),
                                                                    ('/CLIProxyAPI/plugins', 'plugins'), ('/CLIProxyAPI/plugin-data', 'plugin-data')]]}
        self.commands, self.requests = [], []
        self.starts = 0
        self.on_start = lambda: None
        self.bad_asset = False
        self.legacy_asset_headers = False
        self.conditional_requests = []
        self.stop_error_once = False
        self.report = self.directory / 'report.json'

    def write_store(self, path, mode):
        subprocess.run([str(self.writer), str(path), mode], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def docker(self, *args):
        self.commands.append(args)
        if args[0] == 'inspect':
            self.assertIn(args[1], ('cli-proxy-api', self.container['Id']))
            return json.dumps([self.container]).encode()
        if args[:2] == ('image', 'inspect'):
            return json.dumps([{'Os': 'linux', 'Architecture': 'arm64'}]).encode()
        if args[0] == 'stop':
            self.assertEqual(args[-1], 'original-container-id')
            self.container['State']['Running'] = False
            if self.stop_error_once:
                self.stop_error_once = False
                raise subprocess.CalledProcessError(1, 'synthetic docker stop')
            return b''
        if args[0] == 'start':
            self.assertEqual(args[-1], 'original-container-id')
            self.container['State']['Running'] = True
            self.starts += 1
            if self.starts == 1:
                self.on_start()
            return b''
        self.fail('unexpected Docker operation')

    def get(self, path, etag=None):
        self.requests.append(path)
        if path == '/management.html':
            return 200, b'panel', None
        if path == '/v0/management/plugins/model-quality/settings':
            return 401, b'unauthorized', None
        prefix = '/v0/resource/plugins/model-quality/'
        self.assertTrue(path.startswith(prefix))
        name = path.removeprefix(prefix)
        data = self.assets[name]
        headers = {'Cache-Control': 'no-store'} if self.legacy_asset_headers else {
            'Cache-Control': 'private, no-cache', 'ETag': f'W/"{update.digest(data)}"',
            'Content-Type': {'index.html': 'text/html', 'style.css': 'text/css'}.get(name, 'application/javascript')}
        if etag:
            self.conditional_requests.append((name, etag))
            if etag == headers.get('ETag'):
                return 304, b'', headers
        return 200, b'mismatch' if self.bad_asset and name == 'app.js' else data, headers

    def invoke(self):
        with patch.object(update, 'ROOT', self.root), patch.object(update, 'RUNTIME', self.runtime), \
             patch.object(update.Path, 'home', return_value=self.home), \
             patch.object(update, 'build_inspector', return_value=self.inspector), \
             patch.object(update, 'docker', side_effect=self.docker), patch.object(update, 'get', side_effect=self.get), \
             contextlib.redirect_stdout(io.StringIO()):
            update.update(self.package, self.report)

    def test_logical_digest_tracks_nested_data_sequences_and_empty_buckets(self):
        private = self.directory / 'snapshots'
        private.mkdir(mode=0o700)
        baseline = update.stable_store_digest(self.store, self.inspector, private)
        self.write_store(self.store, 'noop')
        self.assertNotEqual(self.original_db, update.digest(self.store.read_bytes()))
        self.assertEqual(baseline, update.stable_store_digest(self.store, self.inspector, private))
        for mode in ('value', 'sequence', 'bucket'):
            self.write_store(self.store, mode)
            current = update.stable_store_digest(self.store, self.inspector, private)
            self.assertNotEqual(baseline['sha256'], current['sha256'])
            self.assertNotIn('synthetic-secret', json.dumps(current))
            baseline = current
        self.assertFalse(list(private.iterdir()))

    def test_noop_database_update_passes_and_backup_is_byte_exact(self):
        self.on_start = lambda: self.write_store(self.store, 'noop')
        self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'passed')
        self.assertTrue(report['all_public_assets_revalidated'])
        self.assertCountEqual(self.conditional_requests, [
            (name, tag) for name in self.assets for tag in (f'W/"{update.digest(self.assets[name])}"', '"stale-resource"')
        ])
        self.assertEqual(report['database_count_before'], 3)
        self.assertEqual(len(report['persistent_state']['database_comparisons']), 3)
        self.assertTrue(all(row['logical_unchanged'] for row in report['persistent_state']['database_comparisons']))
        self.assertEqual(self.old_library.read_bytes(), b'new-library')
        self.assertNotEqual(update.digest(self.store.read_bytes()), self.original_db)
        backup = Path(report['backup'])
        self.assertEqual(update.digest((backup / 'plugin-data/model-quality/accounts/credential-login.db').read_bytes()), self.original_db)
        self.assertEqual((backup / 'plugins/model-quality.so').read_bytes(), b'previous-library')
        self.assertFalse(report['data_restored'])
        self.assertEqual(self.starts, 1)
        self.assertEqual(self.report.stat().st_mode & 0o777, 0o600)
        self.assertTrue(all(route.startswith('/v0/resource/plugins/model-quality/') or
                            route in ('/management.html', '/v0/management/plugins/model-quality/settings')
                            for route in self.requests))

    def test_noop_cpa_kv_update_passes_with_exact_credential_and_backup(self):
        original = update.digest(self.kv.read_bytes())
        self.on_start = lambda: self.write_store(self.kv, 'noop')
        self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'passed')
        self.assertTrue(report['credential_directory']['verified_cpa_kv_logical_comparison'])
        self.assertTrue(report['credential_directory']['cpa_kv']['logical_unchanged'])
        self.assertNotEqual(original, update.digest(self.kv.read_bytes()))
        backup = Path(report['backup'])
        self.assertEqual(original, update.digest((backup / 'auths/kv.db').read_bytes()))
        self.assertEqual((backup / 'auths/account.json').read_bytes(), self.auth.read_bytes())

    def test_logical_cpa_kv_change_rolls_back_only_library(self):
        original = update.digest(self.kv.read_bytes())
        self.on_start = lambda: self.write_store(self.kv, 'value')
        with self.assertRaises(RuntimeError):
            self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'failed_rolled_back')
        self.assertFalse(report['credential_directory']['cpa_kv']['logical_unchanged'])
        self.assertFalse(report['data_restored'])
        self.assertNotEqual(original, update.digest(self.kv.read_bytes()))
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')

    def test_unknown_auth_database_remains_byte_exact(self):
        other = self.runtime / 'auths/other.db'
        self.write_store(other, 'seed')
        original = update.digest(other.read_bytes())
        self.on_start = lambda: self.write_store(other, 'noop')
        with self.assertRaises(RuntimeError):
            self.invoke()
        self.assertNotEqual(original, update.digest(other.read_bytes()))
        self.assertEqual(json.loads(self.report.read_text())['status'], 'failed_rolled_back')
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')

    def test_unverified_cpa_kv_remains_byte_exact(self):
        path = self.runtime / 'config/config.yaml'
        config = json.loads(path.read_text())
        config['server'] = {'kv-store': {'disabled': True}}
        path.write_text(json.dumps(config))
        self.on_start = lambda: self.write_store(self.kv, 'noop')
        with self.assertRaises(RuntimeError):
            self.invoke()
        report = json.loads(self.report.read_text())
        self.assertFalse(report['credential_directory']['verified_cpa_kv_logical_comparison'])
        self.assertEqual(report['status'], 'failed_rolled_back')

    def test_kv_verification_only_accepts_known_enabled_configuration(self):
        path = self.runtime / 'config/config.yaml'
        original = json.loads(path.read_text())
        for section, verified in [({}, True), ({'path': '/root/.cli-proxy-api/kv.db'}, True),
                                  ({'path': '  '}, True), ({'path': './kv.db'}, False),
                                  ({'path': '/elsewhere/kv.db'}, False), ({'disabled': True}, False)]:
            with self.subTest(section=section):
                config = dict(original, server={'kv-store': section})
                path.write_text(json.dumps(config))
                with patch.object(update, 'RUNTIME', self.runtime):
                    self.assertIs(update.verify_configuration(self.inspector)['auth_kv_verified'], verified)
        config = dict(original, **{'kv-store': {'path': '/root/.cli-proxy-api/kv.db'}})
        path.write_text(json.dumps(config))
        with patch.object(update, 'RUNTIME', self.runtime):
            self.assertFalse(update.verify_configuration(self.inspector)['auth_kv_verified'])

    def test_noop_receipts_update_passes_logical_account_comparison(self):
        original = update.digest(self.receipts.read_bytes())
        self.on_start = lambda: self.write_store(self.receipts, 'noop')
        self.invoke()
        self.assertEqual(json.loads(self.report.read_text())['status'], 'passed')
        self.assertNotEqual(original, update.digest(self.receipts.read_bytes()))

    def test_asset_failure_rolls_back_library_and_preserves_current_data(self):
        self.bad_asset = True
        self.on_start = lambda: self.auth.write_text('concurrent credential data must survive rollback')
        with self.assertRaises(RuntimeError):
            self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'failed_rolled_back')
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')
        self.assertEqual(self.auth.read_text(), 'concurrent credential data must survive rollback')
        self.assertTrue(self.container['State']['Running'])
        self.assertFalse(report['data_restored'])
        self.assertEqual(self.starts, 2)

    def test_archived_package_uses_its_own_assets_and_no_store(self):
        self.manifest['plugin_version'] = '0.30.9'
        self.manifest['ui_version'] = 'archived-ui'
        self.assets['index.html'] = b'archived index from selected package'
        self.manifest['ui_source_sha256']['index.html'] = update.digest(self.assets['index.html'])
        (self.package / 'build-manifest.json').write_text(json.dumps(self.manifest))
        self.legacy_asset_headers = True
        self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'passed')
        self.assertFalse(report['all_public_assets_revalidated'])
        self.assertEqual(report['served_resource_sha256'], self.manifest['ui_source_sha256'])
        self.assertFalse(self.conditional_requests)

    def test_current_package_cannot_silently_use_legacy_cache_policy(self):
        self.legacy_asset_headers = True
        with self.assertRaises(RuntimeError):
            self.invoke()
        self.assertEqual(json.loads(self.report.read_text())['status'], 'failed_rolled_back')
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')

    def test_same_version_source_drift_stops_before_touching_cpa(self):
        (self.root / 'plugin/ui/index.html').write_bytes(b'changed checkout')
        with self.assertRaisesRegex(ValueError, 'current UI differs'):
            self.invoke()
        self.assertEqual(self.commands, [])

    def test_rebuilt_library_after_preflight_is_never_installed(self):
        candidate = self.package / 'model-quality.so'
        original_copy = shutil.copy2

        def rebuild_during_backup(source, destination, *args, **kwargs):
            if Path(source) == candidate:
                candidate.write_bytes(b'different-build-with-identical-ui')
            return original_copy(source, destination, *args, **kwargs)

        with patch.object(update.shutil, 'copy2', side_effect=rebuild_during_backup):
            with self.assertRaises(RuntimeError):
                self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'failed_original_restarted')
        self.assertEqual(report['failure_reason'], 'staged plugin library digest mismatch')
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')
        self.assertTrue(report['original_container_restarted'])

    def test_logical_account_change_fails_without_reverting_account_data(self):
        self.on_start = lambda: self.write_store(self.store, 'value')
        with self.assertRaises(RuntimeError):
            self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'failed_rolled_back')
        self.assertIn('logical account storage', report['failure_reason'])
        self.assertNotEqual(update.digest(self.store.read_bytes()), self.original_db)
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')
        self.assertFalse(report['data_restored'])

    def test_quality_startup_changes_are_reported_and_require_review(self):
        self.on_start = lambda: self.write_store(self.quality, 'qualitycomplete')
        with self.assertRaises(RuntimeError):
            self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'failed_rolled_back')
        summary = report['persistent_state']
        self.assertTrue(summary['quality_state_changed'])
        self.assertTrue(summary['quality_state_review_required'])
        quality, = [row for row in summary['database_comparisons'] if row['kind'] == 'quality.db']
        self.assertEqual(quality['before']['quality_startup']['queued_or_running_runs'], 1)
        self.assertEqual(quality['before']['quality_startup']['pending_credential_writes'], 1)
        self.assertEqual(quality['before']['quality_startup']['legacy_scope_rules'], 1)
        self.assertEqual(quality['after']['quality_startup']['queued_or_running_runs'], 0)
        self.assertEqual(quality['after']['quality_startup']['pending_credential_writes'], 0)
        self.assertEqual(quality['after']['quality_startup']['legacy_scope_rules'], 0)
        self.assertFalse(report['data_restored'])
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')
        self.assertEqual(self.starts, 2)

    def test_receipt_database_changes_are_detected_and_never_reverted(self):
        self.on_start = lambda: self.write_store(self.receipts, 'value')
        original = update.digest(self.receipts.read_bytes())
        with self.assertRaises(RuntimeError):
            self.invoke()
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')
        self.assertNotEqual(update.digest(self.receipts.read_bytes()), original)
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'failed_rolled_back')
        self.assertIn('logical account storage', report['failure_reason'])

    def prepare_scheduled_quality(self):
        self.write_store(self.quality, 'qualitycomplete')
        self.write_store(self.quality, 'scheduledseed')

    def test_migrated_quality_input_must_match_existing_digest(self):
        self.prepare_scheduled_quality()
        self.write_store(self.quality, 'inputdigest')
        before = self.directory / 'before-quality.db'
        shutil.copyfile(self.quality, before)
        before.chmod(0o600)
        self.write_store(self.quality, 'otherinput')
        rejected = update.compare_quality_snapshots(before, self.quality, self.inspector)
        self.assertFalse(rejected['preserved'])
        self.write_store(self.quality, 'matchinput')
        accepted = update.compare_quality_snapshots(before, self.quality, self.inspector)
        self.assertTrue(accepted['preserved'])
        self.assertTrue(accepted['quality_host_input_migrated'])

    def test_only_authorized_empty_schedule_changes_are_permitted(self):
        self.prepare_scheduled_quality()
        self.on_start = lambda: self.write_store(self.quality, 'scheduledempty')
        self.invoke()
        report = json.loads(self.report.read_text())
        summary = report['persistent_state']
        self.assertEqual(report['status'], 'passed')
        self.assertTrue(summary['quality_state_changed'])
        self.assertFalse(summary['quality_state_review_required'])
        self.assertEqual(summary['scheduled_empty_audits_added'], 1)
        self.assertEqual(summary['rule_next_run_updated'], 1)
        quality, = [row for row in summary['database_comparisons'] if row['kind'] == 'quality.db']
        self.assertFalse(quality['logical_unchanged'])
        self.assertTrue(quality['permitted_empty_schedule_change'])
        self.assertEqual(quality['preservation']['unexpected_changes'], 0)
        self.assertEqual(self.old_library.read_bytes(), b'new-library')
        self.assertFalse(list((self.home / '.local/share/cpa-quality-backups').glob('inspect-*')))

    def assert_quality_write_rejected(self, mode):
        self.prepare_scheduled_quality()
        self.on_start = lambda: self.write_store(self.quality, mode)
        with self.assertRaises(RuntimeError):
            self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['status'], 'failed_rolled_back')
        self.assertTrue(report['persistent_state']['quality_state_review_required'])
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')
        self.assertFalse(report['data_restored'])

    def test_nonempty_scheduled_dispatch_is_rejected(self):
        self.assert_quality_write_rejected('schedulednonempty')

    def test_existing_audit_rewrite_is_rejected(self):
        self.assert_quality_write_rejected('auditrewrite')

    def test_rule_permission_change_is_rejected(self):
        self.assert_quality_write_rejected('scheduledpermission')

    def test_quality_comparison_rejects_other_writes(self):
        self.prepare_scheduled_quality()
        before = self.directory / 'before-quality.db'
        after = self.directory / 'after-quality.db'
        shutil.copyfile(self.quality, before)
        before.chmod(0o600)
        for mode in ('scheduledrun', 'runadded', 'ruleremoved', 'authorizationchanged', 'sequence', 'bucket'):
            with self.subTest(mode=mode):
                shutil.copyfile(before, after)
                after.chmod(0o600)
                self.write_store(after, mode)
                summary = update.compare_quality_snapshots(before, after, self.inspector)
                self.assertFalse(summary['preserved'])
                self.assertGreater(summary['unexpected_changes'], 0)
                self.assertNotIn('synthetic-secret', json.dumps(summary))

    def test_existing_runtime_files_cannot_disappear(self):
        self.on_start = lambda: self.runtime_file.unlink()
        with self.assertRaises(RuntimeError):
            self.invoke()
        self.assertFalse(self.runtime_file.exists())
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')
        self.assertIn('data files disappeared', json.loads(self.report.read_text())['failure_reason'])

    def test_changed_private_authorization_is_not_reported_as_preserved(self):
        record = self.runtime / 'plugin-data/model-quality/management/connection.json'
        record.parent.mkdir(mode=0o700)
        record.write_bytes(b'synthetic initial authorization')
        self.on_start = lambda: record.write_bytes(b'synthetic changed authorization')
        with self.assertRaises(RuntimeError):
            self.invoke()
        report = json.loads(self.report.read_text())
        self.assertEqual(report['persistent_state']['existing_non_database_files_changed'], 1)
        self.assertEqual(report['status'], 'failed_rolled_back')
        self.assertEqual(record.read_bytes(), b'synthetic changed authorization')

    def test_added_runtime_files_are_reported_and_preserved(self):
        added = self.runtime_file.with_name('new-runtime.bin')
        self.on_start = lambda: added.write_bytes(b'allowed-new-runtime')
        self.invoke()
        self.assertEqual(added.read_bytes(), b'allowed-new-runtime')
        self.assertEqual(json.loads(self.report.read_text())['persistent_state']['added_files'], 1)

    def test_non_database_account_file_still_requires_exact_hash(self):
        self.on_start = lambda: (self.accounts / 'vault.key').write_bytes(b'synthetic-new-vault-key')
        with self.assertRaises(RuntimeError):
            self.invoke()
        self.assertEqual((self.accounts / 'vault.key').read_bytes(), b'synthetic-new-vault-key')
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')

    def test_wrong_auth_mount_is_rejected_before_stopping(self):
        self.container['Mounts'][1]['Source'] = str(self.directory / 'wrong-auths')
        with self.assertRaises(ValueError):
            self.invoke()
        self.assertFalse(any(cmd[0] == 'stop' for cmd in self.commands))
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')

    def test_configured_auth_path_is_checked_without_exposing_config(self):
        (self.runtime / 'config/config.yaml').write_text(json.dumps({'oauth': {'auth-dir': '/another-directory'},
                                                                   'management': {'secret-key': 'synthetic-private-key'}}))
        with self.assertRaisesRegex(ValueError, 'configured credential or plugin storage') as error:
            self.invoke()
        self.assertNotIn('synthetic-private-key', str(error.exception))
        self.assertFalse(any(cmd[0] == 'stop' for cmd in self.commands))

    def test_nested_config_mount_is_rejected_before_stopping(self):
        self.container['Mounts'].append({'Destination': '/CLIProxyAPI/config/config.yaml', 'Source': '/synthetic/other', 'Type': 'bind', 'RW': True})
        with self.assertRaises(ValueError):
            self.invoke()
        self.assertFalse(any(cmd[0] == 'stop' for cmd in self.commands))

    def test_failed_stop_command_still_restarts_original_container(self):
        self.stop_error_once = True
        with self.assertRaises(RuntimeError):
            self.invoke()
        self.assertTrue(self.container['State']['Running'])
        self.assertEqual(self.starts, 1)
        self.assertEqual(self.old_library.read_bytes(), b'previous-library')
        self.assertFalse(json.loads(self.report.read_text())['data_restored'])


if __name__ == '__main__':
    unittest.main()
