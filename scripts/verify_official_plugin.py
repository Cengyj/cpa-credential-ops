#!/usr/bin/env python3
"""Install and exercise the native plugin on an unchanged official CPA, offline.

The only upstream and registry are local HTTP fixtures. No login is started and
no real account, existing CPA instance, or existing plugin data is accessed.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import traceback
from http.server import ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import ProxyHandler, Request, build_opener
import uuid
import zipfile

from demo import FakeUpstream


ROOT = Path(__file__).resolve().parents[1]
PREFIX = "/v0/management/plugins/model-quality"
LOGIN = PREFIX + "/login"
VERSION = "0.15.0"
ADMIN_KEY = "official-plugin-offline-only-management-key"
BUSINESS_KEY = "official-plugin-offline-only-client-key"
HTTP = build_opener(ProxyHandler({}))


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(1 << 20):
            value.update(chunk)
    return value.hexdigest()


def write_json(path: Path, value):
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def baseline_snapshot():
    manifest = json.loads((ROOT / "host-patches/baseline.json").read_text())
    result = {}
    for source in [Path(manifest["source"]), ROOT / "workspaces/CLIProxyAPI-official"]:
        hashes = {}
        for relative, expected in manifest["sha256"].items():
            actual = digest(source / relative)
            require(actual == expected, f"official source baseline mismatch: {source / relative}")
            hashes[relative] = actual
        result[str(source)] = {
            "files": len(hashes),
            "sha256": hashlib.sha256(json.dumps(hashes, sort_keys=True).encode()).hexdigest(),
        }
    return result


def request(base, path, method="GET", data=None, authenticated=True, timeout=4):
    headers = {"Accept": "application/json"}
    if authenticated:
        headers["Authorization"] = "Bearer " + ADMIN_KEY
    body = None
    if data is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(data).encode()
    if method not in {"GET", "HEAD"}:
        headers["Idempotency-Key"] = "offline-smoke-" + uuid.uuid4().hex
    try:
        response = HTTP.open(Request(base + path, data=body, headers=headers, method=method), timeout=timeout)
    except HTTPError as error:
        response = error
    with response:
        raw = response.read(8 << 20)
        value = None
        if "json" in response.headers.get("Content-Type", ""):
            value = json.loads(raw)
        return response.status, value, raw, dict(response.headers)


def get_json(base, path, expected=200, **kwargs):
    status, value, raw, _ = request(base, path, **kwargs)
    require(status == expected, f"{kwargs.get('method', 'GET')} {path}: expected {expected}, got {status}: {raw[:1000]!r}")
    require(value is not None, f"{path} did not return JSON")
    return value


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    require(port != 8317, "refusing to use the existing CPA port")
    return port


class Fixture(FakeUpstream):
    calls = []
    mode = "correct"
    lock = threading.Lock()
    release = threading.Event()
    stop = threading.Event()
    registry = {}
    archive = None
    registry_calls = []
    blocked_outbound = []
    origin = ""

    def handle(self):
        try:
            super().handle()
        except (ConnectionResetError, BrokenPipeError):
            pass

    def normalize_path(self):
        parsed = urlsplit(self.path)
        if parsed.netloc:
            if parsed.netloc != self.origin:
                self.blocked_outbound.append(parsed.netloc)
                self.json_response(502, {"error": "offline fixture blocks external traffic"})
                return False
            self.path = parsed.path
        return True

    def do_CONNECT(self):
        self.blocked_outbound.append(self.path)
        self.json_response(502, {"error": "offline fixture blocks external traffic"})

    def do_POST(self):
        if self.normalize_path():
            super().do_POST()

    def do_GET(self):
        if not self.normalize_path():
            return
        if self.path == "/registry.json":
            self.registry_calls.append(self.path)
            self.json_response(200, self.registry)
            return
        if self.path == "/plugin.zip":
            self.registry_calls.append(self.path)
            self.send_response(200)
            self.send_header("Content-Type", "application/zip")
            self.send_header("Content-Length", str(self.archive.stat().st_size))
            self.end_headers()
            with self.archive.open("rb") as source:
                shutil.copyfileobj(source, self.wfile)
            return
        super().do_GET()


class Host:
    def __init__(self, binary, session, config, blocking_proxy):
        self.binary, self.session, self.config = binary, session, config
        self.blocking_proxy = blocking_proxy
        self.process = None
        self.log = None
        self.starts = []

    def start(self):
        require(self.process is None, "host is already running")
        number = len(self.starts) + 1
        log_path = self.session / f"host-{number}.log"
        self.log = log_path.open("wb")
        env = {
            "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
            "HOME": str(self.session / "home"),
            "TMPDIR": str(self.session / "tmp"),
            "LANG": "en_US.UTF-8",
            "HTTP_PROXY": self.blocking_proxy,
            "HTTPS_PROXY": self.blocking_proxy,
            "NO_PROXY": "127.0.0.1,localhost",
        }
        self.process = subprocess.Popen(
            [str(self.binary), "--config", str(self.config), "--standalone", "--local-model"],
            cwd=self.session, env=env, stdin=subprocess.DEVNULL,
            stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True,
        )
        self.starts.append({"pid": self.process.pid, "log": str(log_path), "stopped": False})

    def ready(self, base, path, timeout=45):
        deadline = time.monotonic() + timeout
        last = "not reachable"
        while time.monotonic() < deadline:
            require(self.process.poll() is None, f"official CPA exited: see {self.starts[-1]['log']}")
            try:
                status, value, raw, _ = request(base, path, timeout=2)
                if status == 200:
                    return value
                last = f"HTTP {status}: {raw[:300]!r}"
            except (URLError, TimeoutError, OSError) as error:
                last = type(error).__name__
            time.sleep(0.2)
        raise AssertionError(f"official CPA route {path} did not become ready: {last}; see {self.starts[-1]['log']}")

    def stop(self):
        if self.process is None:
            return
        process = self.process
        forced = False
        try:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    forced = True
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
        finally:
            # Reap descendants even if the host exited before its cleanup ran.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            self.starts[-1].update(stopped=True, returncode=process.returncode, forced=forced)
            self.log.close()
            self.log = None
            self.process = None


def inspect_bundle(data_dir):
    manifests = list((data_dir / "login-runtime").glob("*/manifest.json"))
    require(len(manifests) == 1, "expected exactly one private native runtime installation")
    manifest_path = manifests[0]
    manifest = json.loads(manifest_path.read_text())
    require(manifest.get("schema_version") == 2 and manifest.get("runtime_version") == "2.1.0" and manifest.get("engine") == "go_sobek_protocol", "wrong embedded runtime identity")
    require("node" not in manifest and "browser" not in manifest, "embedded runtime still depends on browser automation")
    bundle = manifest_path.parent
    actual_files = {str(path.relative_to(bundle)) for path in bundle.rglob("*") if path.is_file()}
    require(actual_files == set(manifest["files"]) | {"manifest.json"}, "runtime contains unlisted files")
    for relative, expected in manifest["files"].items():
        require(digest(bundle / relative) == expected, f"embedded runtime checksum mismatch: {relative}")
    for path in [data_dir, bundle.parent, bundle, *bundle.rglob("*")]:
        require(not path.is_symlink() and path.stat().st_mode & 0o077 == 0, f"runtime path is not private: {path}")
    executable = bundle / manifest["entrypoint"]
    result = subprocess.run([str(executable), "--self-test"], cwd=bundle, env={"PATH": "/usr/bin:/bin", "HOME": str(data_dir)}, capture_output=True, timeout=10)
    require(result.returncode == 0 and not result.stderr, "installed native Sobek self-test failed")
    tested = json.loads(result.stdout)
    require(tested.get("engine") == "go_sobek_protocol" and tested.get("runtime_version") == "2.1.0" and tested.get("node_required") is False and tested.get("browser_required") is False, "installed engine self-test reported browser dependencies")
    return {"manifest": str(manifest_path), "manifest_sha256": digest(manifest_path), "executable_sha256": digest(executable), "self_test": tested}


def exercise(base, output):
    endpoints = ["/targets", "/questions", "/runs", "/rules", "/actions", "/settings", "/metrics", "/history", "/presets", "/login/capabilities", "/login/targets", "/login/accounts", "/login/jobs", "/login/vault/status", "/login/accounts/settings"]
    for path in endpoints:
        status, _, _, _ = request(base, PREFIX + path, authenticated=False)
        require(status == 401, f"{path} is not protected by official management authentication")
        get_json(base, PREFIX + path)
    status, _, _, _ = request(base, LOGIN + "/accounts/import", method="POST", data={"entries": []}, authenticated=False)
    require(status == 401, "account mutation is not management authenticated")
    for path in ["/accounts/security", "/accounts/security/complete"]:
        require(request(base, LOGIN + path, method="POST", data={}, authenticated=False)[0] == 401, "security action is not management authenticated")
        get_json(base, LOGIN + path, method="POST", data={}, expected=400)
    for path in ["/v8/management/plugins/model-quality/settings", "/v8/management/credential-logins/capabilities"]:
        require(request(base, path)[0] == 404, f"official host unexpectedly exposes patched endpoint {path}")
    static = {}
    for name in json.loads((ROOT / "plugin/ui/public_assets.json").read_text()):
        status, _, raw, headers = request(base, "/v0/resource/plugins/model-quality/" + name, authenticated=False)
        require(status == 200 and len(raw) > 0, f"embedded UI resource missing: {name}")
        static[name] = {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw), "content_type": headers.get("Content-Type")}
    require(request(base, "/v0/resource/plugins/model-quality/login/accounts", authenticated=False)[0] == 404, "public resources exposed account data")
    capabilities = get_json(base, LOGIN + "/capabilities")
    require(capabilities.get("available") is True and capabilities.get("automatic_commit") is True and capabilities.get("commit_mode") == "account_auto_commit" and capabilities.get("manual_oauth_commit") is True, "account login does not advertise automatic commit with a separate manual OAuth flow")
    require(capabilities.get("runtime") == {"engine": "go_sobek_protocol", "runtime_version": "2.1.0", "schema_version": "2"}, "login runtime metadata does not identify Go/Sobek 2.1.0")
    require(capabilities.get("security_actions") == {"reset_totp": True, "logout_all": True}, "native account security actions are unavailable")
    require(capabilities["methods"]["password_totp"]["available"] is True, "native password/TOTP login is unavailable")

    credentials = {"email": "official-smoke@example.test", "password": "OFFLINE-PASSWORD-ONLY", "totp_secret": "JBSWY3DPEHPK3PXP"}
    imported = get_json(base, LOGIN + "/accounts/import", method="POST", data={"entries": [credentials], "proxy_url": "http://127.0.0.1:9"})
    require(len(imported["items"]) == 1, "account import failed")
    account = imported["items"][0]
    require(account["health_status"] == "not_linked", "offline account was incorrectly linked to CPA")
    credentials["password"] = "OFFLINE-EDITED-PASSWORD-ONLY"
    edited = get_json(base, LOGIN + "/accounts/entry", method="PUT", data={"id": account["id"], "revision": account["revision"], "label": "Offline persistence fixture", "auto_relogin": False, "proxy_url": "socks5h://127.0.0.1:9", "credentials": credentials})
    detail = get_json(base, LOGIN + "/accounts/detail?" + urlencode({"id": account["id"]}))
    require(detail["credentials"] == credentials and detail["account"]["label"] == "Offline persistence fixture", "account edit or plaintext detail failed")
    inspected = get_json(base, LOGIN + "/accounts/inspect", method="POST", data={"id": account["id"]})
    require(len(inspected["items"]) == 1 and inspected["items"][0]["health_status"] == "not_linked", "offline inspection attempted an unauthorized login")
    settings = get_json(base, LOGIN + "/accounts/settings")
    settings.update(inspection_enabled=True, auto_relogin_enabled=False, interval_minutes=30, login_proxy_url="http://127.0.0.1:9")
    settings = get_json(base, LOGIN + "/accounts/settings", method="PUT", data=settings)
    require(settings["inspection_enabled"] is True and settings["auto_relogin_enabled"] is False, "inspection settings were not saved")

    quality = get_json(base, PREFIX + "/settings")
    caps = quality.pop("capabilities")
    require(caps.get("mode") == "official_observation" and caps.get("quality_pause") is False and caps.get("verified_evidence") is False, "official quality mode advertised unsupported authority")
    invalid = dict(quality, automatic_pause=True)
    error = get_json(base, PREFIX + "/settings", method="PUT", data=invalid, expected=422)
    require(error["error"]["code"] == "unsupported", "automatic pause settings were not explicitly rejected")
    error = get_json(base, PREFIX + "/rules", method="POST", data={"action": "pause_credential"}, expected=422)
    require(error["error"]["code"] == "unsupported", "pause rule was not explicitly rejected")
    quality["daily_calls"] = 77
    persisted_quality = get_json(base, PREFIX + "/settings", method="PUT", data=quality)

    targets = get_json(base, PREFIX + "/targets")
    write_json(output / "quality-targets.json", targets)
    write_json(output / "official-credentials.json", get_json(base, "/v8/management/credentials"))
    available = [target for target in targets["items"] if target["effective_available"] and target["models"]]
    require(available, "official host did not expose the local fake model credential")
    target = available[0]
    require(len(available) == 1 and target["auth_id"] == "offline-kimi.json" and target["provider"] == "kimi", "test selected an unexpected credential")
    require(not target["can_pause"] and not target["can_retest"], "target advertised private quality actions")
    run_config = {"auth_id": target["auth_id"], "provider": target["provider"], "models": [target["models"][0]], "question_id": "logic-boxes", "samples": 1, "parameters": {"max_tokens": 64, "stream": False}, "judge": {"mode": "deterministic"}}
    results = []
    for streaming in [False, True]:
        run_config["parameters"]["stream"] = streaming
        run = get_json(base, PREFIX + "/runs", method="POST", data=run_config, expected=202)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            result = get_json(base, PREFIX + "/runs/detail?" + urlencode({"id": run["run_id"]}))
            if result["status"] not in {"queued", "running"}:
                break
            time.sleep(0.1)
        write_json(output / ("manual-stream.json" if streaming else "manual-run.json"), result)
        require(result["status"] == "completed" and len(result["samples"]) == 1, f"official callback model test did not complete: {result}")
        sample = result["samples"][0]
        require(sample["execution_status"] == "completed" and sample["output"] == "4" and sample["quality_verdict"] == "correct", f"fake model answer was not evaluated: {sample}")
        require(sample["evidence"]["verified"] is False and sample["comparable"] is False, "official callback invented trusted final evidence")
        results.append(result)
    require(not get_json(base, LOGIN + "/jobs")["items"], "smoke unexpectedly started a real account login")
    return {"account_id": account["id"], "credentials": credentials, "account": edited, "account_settings": settings, "quality_settings": persisted_quality, "capabilities": capabilities, "ui": static, "main_endpoints": endpoints, "run_id": results[0]["id"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "artifacts/official-plugin/cli-proxy-api-official")
    package = parser.add_mutually_exclusive_group(required=True)
    package.add_argument("--library", type=Path)
    package.add_argument("--archive", type=Path)
    parser.add_argument("--output", type=Path, default=ROOT / "artifacts/official-plugin/smoke" / (time.strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex[:6]))
    args = parser.parse_args()
    output = args.output.resolve()
    require(not output.exists() or not any(output.iterdir()), "output must be a new or empty directory")
    output.mkdir(parents=True, exist_ok=True)
    os.chmod(output, 0o700)
    binary = args.binary.resolve()
    require(binary.is_file(), f"missing official CPA binary: {binary}")
    report = {"status": "running", "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "binary": str(binary), "output": str(output), "checks": {}, "scope": "offline official-host plugin store installation; fake model only; no live OpenAI login or credential commit"}
    host = None
    server = None
    server_thread = None
    generated_archive = None
    failed = None
    try:
        report["binary_sha256_before"] = digest(binary)
        report["baseline_before"] = baseline_snapshot()
        goos = "darwin" if sys.platform == "darwin" else "linux"
        goarch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64", "AMD64": "amd64"}[platform.machine()]
        extension = ".dylib" if goos == "darwin" else ".so"
        if args.library:
            library = args.library.resolve()
            require(library.is_file(), f"missing plugin library: {library}")
            generated_archive = output / "offline-plugin.zip"
            with zipfile.ZipFile(generated_archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=1) as archive:
                archive.write(library, "model-quality" + extension)
            archive_path = generated_archive
        else:
            archive_path = args.archive.resolve()
        require(archive_path.is_file(), f"missing plugin ZIP: {archive_path}")
        with zipfile.ZipFile(archive_path) as archive:
            candidates = [entry for entry in archive.infolist() if Path(entry.filename).name in {"model-quality" + extension, f"model-quality-v{VERSION}" + extension}]
            require(len(candidates) == 1 and "/" not in candidates[0].filename, "plugin ZIP must contain one expected library at its root")
            with archive.open(candidates[0]) as source:
                library_digest = hashlib.file_digest(source, "sha256").hexdigest() if hasattr(hashlib, "file_digest") else hashlib.sha256(source.read()).hexdigest()
        report["archive"] = {"path": str(archive_path), "sha256": digest(archive_path), "library_sha256": library_digest}
        for name in ["auths", "plugins", "plugin-data", "home", "tmp"]:
            (output / name).mkdir(mode=0o700)
        server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
        server.daemon_threads = True
        upstream_base = f"http://127.0.0.1:{server.server_address[1]}"
        Fixture.origin = urlsplit(upstream_base).netloc
        registry_url = upstream_base + "/registry.json"
        Fixture.archive = archive_path
        Fixture.registry = {"schema_version": 2, "plugins": [{"id": "model-quality", "name": "Offline Model Quality", "description": "Local offline verification fixture", "author": "offline-smoke", "version": VERSION, "install": {"type": "direct", "artifacts": [{"goos": goos, "goarch": goarch, "url": upstream_base + "/plugin.zip", "sha256": digest(archive_path), "size": archive_path.stat().st_size}]}}]}
        server_thread = threading.Thread(target=server.serve_forever, daemon=True)
        server_thread.start()
        port = free_port()
        base = f"http://127.0.0.1:{port}"
        report["host_url"], report["fixture_url"] = base, upstream_base
        # Official auth callbacks intentionally omit config-only API keys. A
        # physical Kimi fixture uses the official credential file path and its
        # documented local base_url override for both execution modes.
        write_json(output / "auths/offline-kimi.json", {
            "type": "kimi", "email": "model-fixture@example.test",
            "access_token": "OFFLINE-FAKE-UPSTREAM-TOKEN",
            "base_url": upstream_base + "/v1", "proxy_url": "direct",
            "expired": "2099-01-01T00:00:00Z",
        })
        config = {
            "config-version": 8,
            "server": {"host": "127.0.0.1", "port": port, "discovery": {"enabled": False}, "kv-store": {"path": str(output / "kv.db")}},
            "management": {"allow-remote": False, "secret-key": ADMIN_KEY, "disable-control-panel": True, "disable-auto-update-panel": True},
            "access": {"api-keys": [BUSINESS_KEY]}, "oauth": {"auth-dir": str(output / "auths")},
            "plugins": {"enabled": True, "dir": str(output / "plugins"), "store-sources": [registry_url], "store-auth": [{"match": upstream_base + "/", "type": "none", "apply-to": ["registry", "artifact"], "allow-insecure": True}], "configs": {"model-quality": {"enabled": True, "data_dir": str(output / "plugin-data"), "scheduling_enabled": False, "automatic_pause": False}}},
            "requests": {"proxy-url": upstream_base},
            "routing": {"retry": {"request-retry": 0}},
            "observability": {"logs": {"logging-to-file": False, "request-log": False}},
        }
        config_path = output / "config.yaml"
        write_json(config_path, config)
        host = Host(binary, output, config_path, upstream_base)
        host.start()
        host.ready(base, "/v8/management/plugins")
        require(not any((output / "plugins").iterdir()), "plugin directory was not empty before installation")
        require(not any((output / "plugin-data").iterdir()), "private helper was present before installation")
        source_id = "source-" + hashlib.sha256(registry_url.encode()).hexdigest()[:12]
        install = get_json(base, "/v8/management/plugins/store/model-quality/install?" + urlencode({"source": source_id}), method="POST", data={}, timeout=45)
        require(install.get("status") == "installed", f"official plugin store installation failed: {install}")
        write_json(output / "installation.json", install)
        host.ready(base, LOGIN + "/capabilities")
        plugins = get_json(base, "/v8/management/plugins")
        entries = [entry for entry in plugins["plugins"] if entry["id"] == "model-quality"]
        require(len(entries) == 1 and entries[0]["registered"] and entries[0]["effective_enabled"], "official host did not load the installed native plugin")
        require(entries[0]["metadata"]["version"] == VERSION, "wrong plugin version loaded")
        installed = Path(entries[0]["path"]).resolve()
        require(installed.is_relative_to(output / "plugins") and digest(installed) == library_digest, "plugin store did not install the exact library")
        report["checks"]["official_store_installation"] = {"status": "passed", "path": str(installed), "registry_requests": Fixture.registry_calls.copy()}
        report["embedded_runtime"] = inspect_bundle(output / "plugin-data")
        require(not list((output / "auths").rglob("login-runtime")), "helper extracted into the official auth directory")
        tested = exercise(base, output)
        report["checks"]["authenticated_routes_ui_manual_test"] = "passed"
        report["capabilities"] = tested["capabilities"]
        report["ui"] = tested["ui"]
        report["main_endpoints"] = [PREFIX + path for path in tested["main_endpoints"]]
        host.stop()
        host.start()
        host.ready(base, LOGIN + "/capabilities")
        detail = get_json(base, LOGIN + "/accounts/detail?" + urlencode({"id": tested["account_id"]}))
        require(detail["credentials"] == tested["credentials"] and detail["account"]["label"] == "Offline persistence fixture" and detail["account"]["proxy_url"] == "socks5h://127.0.0.1:9", "account credentials, label or proxy did not persist across official CPA restart")
        require(get_json(base, LOGIN + "/accounts/settings") == tested["account_settings"], "inspection settings did not persist")
        require(get_json(base, PREFIX + "/settings")["daily_calls"] == 77, "quality settings did not persist")
        require(get_json(base, PREFIX + "/runs/detail?" + urlencode({"id": tested["run_id"]}))["status"] == "completed", "manual test history did not persist")
        require(not get_json(base, LOGIN + "/jobs")["items"], "restart automatically launched a login")
        require(inspect_bundle(output / "plugin-data")["executable_sha256"] == report["embedded_runtime"]["executable_sha256"], "restart replaced the private helper")
        report["checks"]["persistence_after_restart"] = "passed"
        report["fake_model_calls"] = Fixture.calls.copy()
        report["blocked_background_update_hosts"] = Fixture.blocked_outbound.copy()
        require(len(Fixture.calls) == 2 and {call["stream"] for call in Fixture.calls} == {False, True}, "verification made unexpected model calls")
        report["checks"]["offline_only_stream_and_nonstream_fake_calls"] = "passed"
        report["status"] = "passed"
    except BaseException as error:
        failed = error
        report["status"] = "failed"
        report["error"] = f"{type(error).__name__}: {error}"
        (output / "failure.txt").write_text(traceback.format_exc())
    finally:
        report["blocked_background_update_hosts"] = Fixture.blocked_outbound.copy()
        if host:
            host.stop()
            report["host_processes"] = host.starts
        if server:
            if server_thread and server_thread.is_alive():
                server.shutdown()
                server_thread.join(timeout=5)
            server.server_close()
        try:
            report["binary_sha256_after"] = digest(binary)
            require(report["binary_sha256_after"] == report.get("binary_sha256_before"), "official CPA binary changed during plugin installation")
            report["baseline_after"] = baseline_snapshot()
            require(report["baseline_after"] == report.get("baseline_before"), "official source baseline changed")
            report["checks"]["unchanged_official_binary_and_source"] = "passed"
            for log in output.glob("host-*.log"):
                text = log.read_text(errors="replace")
                require("panic:" not in text and "fatal error:" not in text, f"host/runtime panic found in {log}")
            report["checks"]["host_logs_no_panic"] = "passed"
        except BaseException as error:
            failed = failed or error
            report["status"] = "failed"
            report["verification_error"] = str(error)
        # Retain offline databases, configuration, logs and hashes; remove only
        # this invocation's bulky disposable installed libraries and helper.
        for path in [output / "plugins", output / "plugin-data/login-runtime"]:
            if path.exists():
                shutil.rmtree(path)
        if generated_archive and generated_archive.exists():
            generated_archive.unlink()
        report["cleanup"] = "all host processes stopped; disposable installed library and private helper removed; offline data/logs retained"
        report["finished_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        write_json(output / "report.json", report)
    print(json.dumps({"status": report["status"], "report": str(output / "report.json"), "error": report.get("error", report.get("verification_error"))}, ensure_ascii=False))
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
