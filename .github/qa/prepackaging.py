"""Exercise both standard binaries on a clean host profile."""
import json
import os
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

workspace = Path.cwd()
env = dict(os.environ, CGO_ENABLED="0", GOTOOLCHAIN="local")
apps = [
    ("Meloader", "monloader", workspace, "/queue", "/api/v1/queue"),
    ("Mebooru", "monbooru", workspace / "mebooru-src", "/", "/api/v1/images"),
]


def reserve_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def fetch(url):
    with urllib.request.urlopen(url, timeout=3) as response:
        return response.read()


def verify_process(binary, app, legacy, page, api, directory):
    port = reserve_port()
    origin = f"http://127.0.0.1:{port}"
    config = directory / f"{legacy}.toml"
    instance_env = dict(
        env,
        **{f"{app.upper()}_SERVER_BIND_ADDRESS": f"127.0.0.1:{port}",
           f"{app.upper()}_SERVER_BASE_URL": origin},
    )
    log_path = directory / "app.log"
    with log_path.open("w", encoding="utf-8") as log:
        process = subprocess.Popen(
            [str(binary), "-desktop=false", "-config", str(config)],
            env=instance_env, cwd=directory, stdout=log, stderr=subprocess.STDOUT,
        )
        try:
            deadline = time.monotonic() + 30
            while True:
                try:
                    assert json.loads(fetch(origin + "/health"))["app"] == legacy
                    break
                except OSError:
                    if process.poll() is not None or time.monotonic() >= deadline:
                        raise
                    time.sleep(0.25)
            subprocess.run([str(binary), "healthcheck", "-config", str(config)], check=True, env=instance_env)
            assert app.lower().encode() in fetch(origin + page).lower()
            assert json.loads(fetch(origin + "/api/v1/openapi.json"))["info"]["title"] == app + " API"
            try:
                fetch(origin + api)
            except urllib.error.HTTPError as error:
                assert error.code == 503 and b"api_disabled" in error.read(), error.code
            else:
                raise AssertionError("Unauthenticated request unexpectedly accepted")
        except BaseException:
            print(log_path.read_text(encoding="utf-8", errors="replace"))
            raise
        finally:
            process.terminate()
            process.wait(timeout=10)


with tempfile.TemporaryDirectory() as scratch:
    for app, legacy, repo, page, api in apps:
        directory = Path(scratch) / app
        directory.mkdir()
        binary = directory / (legacy + (".exe" if os.name == "nt" else ""))
        subprocess.run(["go", "test", "./..."], cwd=repo, env=env, check=True)
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/" + legacy], cwd=repo, env=env, check=True)
        assert subprocess.check_output([str(binary), "-version"], text=True).startswith(app + " ")
        verify_process(binary, app, legacy, page, api, directory)
        verify_process(binary, app, legacy, page, api, directory)
        print(f"{app}: version, startup, host config reopen, web UI, healthcheck, default API access refusal passed", flush=True)
