#!/usr/bin/env python3
"""Verify current question-test APIs through the actual native plugin and an isolated mock."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import threading
import time
from urllib.error import HTTPError
from urllib.parse import urlencode, urlparse
from urllib.request import ProxyHandler, Request, build_opener


ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "artifacts/host-alignment-4.3.0"
PLUGIN_VERSION = re.search(r'const Version = "([^\"]+)"', (ROOT / "plugin/quality/types.go").read_text()).group(1)
UI_VERSION = re.search(r'"ui_version":"([^\"]+)"', (ROOT / "plugin/ui/index.html").read_text()).group(1)
UI_NAMES = json.loads((ROOT / "plugin/ui/public_assets.json").read_text())
API_PREFIX = "/v8/management/plugins/model-quality"


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def utc_now():
    return datetime.now(timezone.utc).isoformat()


def input_hashes():
    extension = "dylib" if sys.platform == "darwin" else "so"
    paths = ["artifacts/cli-proxy-api-quality", f"artifacts/model-quality.{extension}",
             "scripts/demo.py", "scripts/verify_question_test.py"]
    paths.extend("plugin/ui/" + name for name in UI_NAMES)
    return {name: digest(ROOT / name) for name in paths}


class HTTPClient:
    def __init__(self):
        self.opener = build_opener(ProxyHandler({}))

    def request(self, url, body=None, key=None, method=None, expected=200, raw=False):
        require(urlparse(url).hostname == "127.0.0.1", "verification only permits loopback HTTP")
        headers = {"Content-Type": "application/json"}
        if key:
            headers["Authorization"] = "Bearer " + key
        request = Request(url, json.dumps(body).encode() if body is not None else None,
                          headers=headers, method=method)
        try:
            response = self.opener.open(request, timeout=5)
        except HTTPError as error:
            response = error
        with response:
            data = response.read()
            require(response.status == expected,
                    f"{request.get_method()} {urlparse(url).path}: expected {expected}, got {response.status}: "
                    + data.decode(errors="replace")[:800])
            return data if raw else json.loads(data)


def save_report(path, report):
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists():
        history = path.parent / (path.stem + "-history")
        history.mkdir(exist_ok=True)
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
        previous = history / (stamp + ".json")
        shutil.copy2(path, previous)
        report["previous_report_preserved"] = str(previous.relative_to(ROOT))
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    temporary.replace(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path, default=OUTPUT / "native-api.json")
    parser.add_argument("--demo-dir", type=Path, default=OUTPUT / "api-demo")
    args = parser.parse_args()
    report_path, demo_dir = args.report.resolve(), args.demo_dir.resolve()
    require(demo_dir.is_relative_to((ROOT / "artifacts").resolve()), "demo directory must be inside artifacts")
    require(report_path.is_relative_to((ROOT / "artifacts").resolve()), "report must be inside artifacts")
    demo_dir.mkdir(parents=True, exist_ok=True)
    lock = demo_dir / ".verification-lock"
    report = {"result": "failed", "started_at": utc_now(), "plugin_version": PLUGIN_VERSION,
              "ui_version": UI_VERSION, "platform": f"{sys.platform}/{platform.machine()}",
              "scope": "actual patched host and C shared library; isolated loopback fake credentials and upstream",
              "checks": [], "demo_directory": str(demo_dir.relative_to(ROOT))}
    env = {**os.environ, "MQ_DEMO_DIR": str(demo_dir)}
    client = HTTPClient()
    owned, locked = False, False

    def demo_command(command):
        process = subprocess.run([sys.executable, str(ROOT / "scripts/demo.py"), command],
                                 cwd=ROOT, env=env, capture_output=True, text=True, timeout=75)
        require(process.returncode == 0, f"demo {command} failed: {process.stderr.strip()}")
        return json.loads(process.stdout)

    def passed(message):
        report["checks"].append(message)

    try:
        lock.mkdir()
        locked = True
        state_path = demo_dir / "state.json"
        previous = json.loads(state_path.read_text()) if state_path.exists() else {}
        require(previous.get("status") not in {"ready", "starting"},
                "an existing demo owns this directory; choose a different --demo-dir")
        report["input_sha256"] = input_hashes()
        owned = True
        state = demo_command("start")
        base, key = state["host_url"], state["admin_key"]
        require(urlparse(base).port != 8317, "verification must not use the persistent CPA installation")
        report["session_directory"] = str(Path(state["session_dir"]).relative_to(ROOT))
        report["host_url"] = base
        api = lambda route, body=None, method=None, expected=200: client.request(
            base + API_PREFIX + route, body, key, method, expected)
        stats = lambda: client.request(state["upstream_url"] + "/stats")
        mode = lambda value: client.request(state["upstream_url"] + "/control",
                                            {"mode": value}, state["control_token"])

        def finish(run_id):
            deadline = time.monotonic() + 30
            wake = threading.Event()
            while time.monotonic() < deadline:
                run = api("/runs/detail?" + urlencode({"id": run_id}))
                if run["status"] not in {"queued", "running"}:
                    return run
                wake.wait(0.05)
            raise RuntimeError("explicit mock run did not finish within the verification window")

        installed = next((ROOT / report["session_directory"] / "plugins").glob("model-quality.*"))
        library_key = f"artifacts/model-quality.{installed.suffix.removeprefix('.')}"
        require(digest(installed) == report["input_sha256"][library_key], "loaded library differs from the snapshotted artifact")
        plugins = client.request(base + "/v8/management/plugins", key=key)
        plugin = next(item for item in plugins["plugins"] if item["id"] == "model-quality")
        require(plugin["effective_enabled"] and plugin["metadata"]["version"] == PLUGIN_VERSION, "native plugin version is not effective")
        served = {}
        for name in UI_NAMES:
            data = client.request(base + "/v0/resource/plugins/model-quality/" + name, raw=True)
            served[name] = hashlib.sha256(data).hexdigest()
            require(served[name] == report["input_sha256"]["plugin/ui/" + name], f"served {name} differs from source")
        report["served_ui_sha256"] = served
        passed(f"native plugin {PLUGIN_VERSION} loads and serves all {len(UI_NAMES)} UI {UI_VERSION} resources byte-identical to source")

        client.request(base + API_PREFIX + "/targets", expected=401)
        targets = api("/targets")["items"]
        target = next(item for item in targets if "mq-reasoner" in item["models"])
        require(target["can_pause"], "mock credential must support precise quality pause")
        questions = api("/questions")["items"]
        candy = next(item for item in questions if item["id"] == "logic-candy")
        require(candy["version"] == "sub2api-ccf997c7-v1", "unexpected candy version")
        require(all(not item.get("answer") for item in questions), "question discovery exposed an answer")
        require("21" not in candy["prompt"], "candy question discovery contains the reference answer")
        require(all(any(q["id"] == legacy for q in questions) for legacy in
                    ("logic-boxes", "random-linear", "visual-pelican")), "legacy question compatibility missing")
        require(len(stats()["calls"]) == 0, "static resources or management reads invoked a model")
        passed("management requires authentication; question discovery omits answers and retains legacy IDs; reads make zero model calls")

        config = {"auth_id": target["auth_id"], "provider": target["provider"], "models": ["mq-reasoner"],
                  "question_id": "logic-candy", "samples": 2,
                  "parameters": {"max_tokens": 2048, "stream": True}}
        created = api("/runs", {**config, "idempotency_key": "question-candy"}, expected=202)
        candy_run = finish(created["run_id"])
        require(candy_run["status"] == "completed" and len(candy_run["samples"]) == 2, "candy double sample did not complete")
        require(candy_run["config"]["judge"]["mode"] == "manual", "omitted manual judge did not default to manual")
        require(all(s["output"] == "21" and s["quality_verdict"] == "unknown" and s["comparable"]
                    and s["evidence"]["auth_id"] == target["auth_id"]
                    and s["evidence"]["resolved_model"] == "mock-reasoner-v1" for s in candy_run["samples"]),
                "candy answer, fixed credential, alias mapping or manual verdict differs")
        require(candy_run["action_status"] == "not_applicable" and not api("/actions")["items"], "manual candy test caused an action")
        require(len(stats()["calls"]) == 2, "candy samples made unexpected upstream calls")
        passed("default-manual candy test returns two independent 21/unknown samples on the fixed credential and mapped model without actions")

        changed_prompt = candy["prompt"] + "\n补充条件：请按你自己的理解作答。"
        api("/runs", {**config, "samples": 1, "prompt": changed_prompt}, expected=400)
        custom = finish(api("/runs", {**config, "samples": 1, "question_id": "custom", "prompt": changed_prompt}, expected=202)["run_id"])
        require(custom["status"] == "completed" and custom["config"]["question"]["id"] == "custom"
                and custom["config"]["question"]["prompt"] == changed_prompt
                and not custom["config"]["question"].get("answer")
                and custom["samples"][0]["quality_verdict"] == "unknown", "custom edit inherited an answer or was not preserved")
        require(len(stats()["calls"]) == 3, "invalid built-in edit reached upstream")
        passed("edited built-in prompt is rejected; explicit custom submission freezes the edit without an inherited answer")

        html_run = finish(api("/runs", {**config, "question_id": "visual-pelican-html", "models": ["mq-visual"], "samples": 1}, expected=202)["run_id"])
        html = html_run["samples"][0]["output"]
        require(html_run["status"] == "completed" and html.lower().startswith("<!doctype html>")
                and "<svg" in html and html.lower().endswith("</html>")
                and html_run["samples"][0]["quality_verdict"] == "unknown", "HTML question did not return an ungraded complete HTML document")
        require(len(stats()["calls"]) == 4 and not api("/actions")["items"], "HTML manual test caused unexpected calls or actions")
        passed("pelican HTML question returns a complete standalone HTML/SVG document without automatic grading")

        settings = api("/settings")
        api("/settings", {**settings, "automatic_pause": True, "scheduling_enabled": False}, "PUT")
        rule = api("/rules", {**config, "question_id": "logic-boxes", "samples": 1,
                              "judge": {"mode": "deterministic"}, "name": "Question UI legacy guard",
                              "action": "pause_credential", "enabled": True, "scheduling_enabled": False,
                              "automatic_pause": True, "fail_threshold": 3})
        require(len(stats()["calls"]) == 4, "saving settings or a rule called a model")
        passed("saving an existing logic-boxes quality rule and settings makes no model calls")
        mode("incorrect")
        for round_index in range(3):
            run = finish(api("/rules/run", {"id": rule["id"]}, expected=202)["run_id"])
            require(run["status"] == "completed" and run["samples"][0]["quality_verdict"] == "incorrect"
                    and run["samples"][0]["comparable"], "legacy rule round is not complete incorrect evidence")
            if round_index < 2:
                require(not api("/actions")["items"], "credential paused before the third valid failure")
        actions = api("/actions")["items"]
        require(len(actions) == 1 and actions[0]["auth_id"] == target["auth_id"], "three failed rounds did not create exactly the target's pause")
        paused_target = next(item for item in api("/targets")["items"] if item["auth_id"] == target["auth_id"])
        require(paused_target["quality_paused"] and not paused_target["effective_available"], "host target is not effectively paused")
        api("/runs", {**config, "idempotency_key": "paused-candy"}, expected=409)
        api("/rules/run", {"id": rule["id"]}, expected=409)
        require(len(stats()["calls"]) == 7, "paused manual or rule request reached upstream")
        passed("three valid incorrect legacy-rule rounds pause the credential; paused candy and rule execution are rejected without calls")

        action = actions[0]
        binding = {"rule_id": rule["id"], "action_id": action["action_id"], "action_revision": action["action_revision"]}
        preview = api("/actions/preview", binding)
        require(preview["manual_release_only"] and preview["rules_remain_paused"], "release preview lost manual-only scope")
        api("/actions/release", {**binding, "pause_rule": True})
        require(not api("/actions")["items"], "explicit owned release did not clear the pause")
        released = next(item for item in api("/rules")["items"] if item["id"] == rule["id"])
        require(not released["enabled"] and released["status"] == "paused" and released["fail_count"] == 0, "released rule resumed or retained stale failure evidence")
        api("/rules/run", {"id": rule["id"]}, expected=409)
        require(len(stats()["calls"]) == 7, "manual release or stopped rule called a model")
        passed("explicit owned manual release clears only the pause and stale failure count while the rule remains stopped")
        report["upstream_calls"] = len(stats()["calls"])
        require(input_hashes() == report["input_sha256"], "source, script or build artifacts changed during verification")
        report["result"] = "passed"
    except Exception as error:
        report["error"] = f"{type(error).__name__}: {error}"
    finally:
        if owned:
            try:
                stopped = demo_command("stop")
                require(stopped.get("status") == "stopped", "owned demo did not stop")
                report["cleanup"] = "stopped only this script's isolated API demo; state and logs retained"
            except Exception as error:
                report["result"] = "failed"
                report["cleanup_error"] = f"{type(error).__name__}: {error}"
        if locked:
            lock.rmdir()
        report["finished_at"] = utc_now()
        save_report(report_path, report)
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if report["result"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
