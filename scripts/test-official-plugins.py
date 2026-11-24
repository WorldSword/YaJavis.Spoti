"""Exercise official plugin installation via the same API as the first-run wizard."""
import functools
import hashlib
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request


class QuietServer(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


def check(core, worker, work):
    catalog = json.loads(Path("plugins/index.json").read_text(encoding="utf-8"))
    ids = [item["id"] for item in catalog["plugins"] if item["kind"] == "rust"]
    assets = work / "assets"
    assets.mkdir()
    names = []
    for plugin_id in ids:
        subprocess.run([sys.executable, "scripts/pack-official.py", assets, "x86_64", plugin_id, worker], check=True)
        names.append(f"plugin-{plugin_id}-x86_64.zip")
    (assets / "SHA256SUMS").write_text(
        "".join(f"{hashlib.sha256((assets / name).read_bytes()).hexdigest()}  {name}\n" for name in names),
        encoding="utf-8",
    )
    data = work / "data"
    data.mkdir()
    (data / "wake.bin").write_bytes(Path("models/wake.bin").read_bytes())
    with http.server.ThreadingHTTPServer(("127.0.0.1", 18079), functools.partial(QuietServer, directory=assets)) as assets_server:
        threading.Thread(target=assets_server.serve_forever, daemon=True).start()
        with (work / "station.log").open("wb") as log:
            station = subprocess.Popen(
                [core, "-data", data, "-listen", "127.0.0.1:18080", "-mic", work / "missing.sock"],
                stdout=log, stderr=log,
                env={**os.environ, "JARVIS_NO_AUTOUPDATE": "1"},
            )
            try:
                lifecycle(data, ids)
            finally:
                station.terminate()
                try:
                    station.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    station.kill()
                    station.wait(timeout=5)
        assets_server.shutdown()


def lifecycle(data, ids):
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(path, method="GET", payload=None, raw=None):
        headers = {"X-Jarvis": "1"}
        if payload is not None:
            headers["Content-Type"] = "application/json"
            raw = json.dumps(payload).encode()
        req = urllib.request.Request("http://127.0.0.1:18080" + path, raw, headers, method=method)
        with opener.open(req, timeout=25) as response:
            body = response.read()
            return json.loads(body) if "application/json" in response.headers["Content-Type"] else body

    for attempt in range(60):
        try:
            health = request("/healthz")
            break
        except OSError:
            if attempt == 59:
                raise
            time.sleep(0.15)
    assert health["ready"] and health["version"] == "0.10.0" and not health["modules"], health
    assert request("/api/setup", "POST", {"password": "test setup password"})["ok"]
    state = request("/api/state")
    assert not state["settings"]["onboarded"] and not state["settings"]["wake_training"]
    plugins_summary = request("/api/plugins")
    assert not plugins_summary["installed"]
    assert set(state["plugins"].get("builtin", [])) == {"chatgpt", "openai", "gemini", "grok", "elevenlabs"}, state["plugins"]
    profile, = state["profiles"]
    assert profile["engine"] == "" and profile["voice"] == ""
    assert request("/api/profile", "PUT", {**profile, "name": "Jarvis"})["ok"]
    assert request("/api/state")["profiles"][0]["engine"] == ""

    for plugin_id in ids:
        row = request("/api/plugins/install", "POST", {"id": plugin_id})["plugin"]
        assert row["id"] == plugin_id and row["kind"] == "rust", row
        plugins = request("/api/plugins")
        assert len(plugins["installed"]) == 1 and plugins["installed"][0]["running"], plugins
        root = data / "plugins" / plugin_id
        assert (root / row["main"]).is_file()
        if plugin_id == "vpn":
            try:
                request("/api/plugins/install", "POST", {"id": plugin_id, "update": True, "expected_version": "100.0.0"})
            except urllib.error.HTTPError as error:
                assert error.code == 400 and "100.0.0" in error.read().decode(), error
            else:
                raise AssertionError("A stale package was accepted as the requested update")
            same = {"id": plugin_id, "expected_version": row["version"]}
            batch = request("/api/plugins/update-all", "POST", [same, {**same, "expected_version": "100.0.0"}])
            assert not batch["updated"] and not batch["reboot_from"], batch
            assert len(batch["errors"]) == 1 and batch["errors"][0]["id"] == plugin_id, batch
            current, = request("/api/plugins")["installed"]
            assert current["running"] and current["version"] == row["version"], current
        if plugin_id == "training":
            assert request("/api/train/records")["local"] == 0
            assert request("/api/train/model", "PUT", raw=(data / "wake.bin").read_bytes())["ok"]
            assert (root / "data" / "wake.bin.tuned").is_file()
        assert request(f"/api/plugins/{plugin_id}/enable", "POST", {"on": False})["ok"]
        assert not request("/api/plugins")["installed"][0]["running"]
        assert request(f"/api/plugins/{plugin_id}", "DELETE")["ok"]
        assert not root.exists()
        assert not request("/api/plugins")["installed"]
        print(f"{plugin_id}: install, start, stop and removal verified", flush=True)

    assert request("/api/onboarded", "POST")["ok"]
    assert request("/api/state")["settings"]["onboarded"]
    assert request("/healthz")["version"] == health["version"]


with tempfile.TemporaryDirectory(prefix="yajarvis-smoke-") as scratch:
    work = Path(scratch)
    try:
        check(*(Path(p).resolve() for p in sys.argv[1:]), work)
    except Exception:
        log = work / "station.log"
        if log.is_file():
            print(log.read_text(encoding="utf-8", errors="replace"), file=sys.stderr)
        raise
