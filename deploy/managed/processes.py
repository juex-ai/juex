"""Owned native platform jobs; never adopt system PostgreSQL/nginx services."""

import hashlib
import json
import os
from pathlib import Path
import plistlib
import signal
import subprocess
import sys
import time
import uuid

from common import durable, run, write_json


def label(config, name):
    return "ai.juex.platform." + config["identity"] + "." + name


def domain():
    gui = "gui/" + str(os.getuid())
    return gui if run(["launchctl", "print", gui], check=False).returncode == 0 else "user/" + str(os.getuid())


def definition_path(config, name):
    if config["os"] == "darwin":
        return Path.home() / "Library/LaunchAgents" / (label(config, name) + ".plist")
    return Path(os.environ.get("XDG_CONFIG_HOME", str(Path.home() / ".config"))) / "systemd/user" / (label(config, name) + ".service")


def arguments(config, name):
    return [config["python"], str(Path(config["operator"]) / "operator.py"),
            "--root", config["root"], "run-service", "--service", name]


def definition(config, name):
    args = arguments(config, name)
    if config["os"] == "darwin":
        return plistlib.dumps({"Label": label(config, name), "JueXDeployment": config["root"],
                              "ProgramArguments": args, "WorkingDirectory": config["root"],
                              "RunAtLoad": True, "KeepAlive": {"SuccessfulExit": False},
                              "ThrottleInterval": 10, "Umask": 0o077,
                              "StandardOutPath": "/dev/null", "StandardErrorPath": "/dev/null"})
    def quote(value):
        return json.dumps(value.replace("%", "%%").replace("$", "$$"))
    return (f"# JueX deployment {config['root']}\n[Unit]\nDescription=JueX {name}\n\n[Service]\n"
            + "ExecStart=" + " ".join(quote(value) for value in args)
            + "\nUMask=0077\nRestart=on-failure\nRestartSec=10\nTimeoutStopSec=30\nSendSIGKILL=no\n"
              "StandardOutput=null\nStandardError=null\n\n[Install]\nWantedBy=default.target\n").encode()


def assert_definition(config, name):
    path = definition_path(config, name)
    if path.is_symlink():
        raise ValueError("refusing a symlink service definition")
    if path.exists() and path.read_bytes() != definition(config, name):
        raise ValueError("refusing to replace or stop a different service definition")
    return path


def service_status(config, name):
    identity = label(config, name)
    if config["os"] == "darwin":
        result = run(["launchctl", "print", domain() + "/" + identity], check=False)
        if result.returncode:
            if b"Could not find service" not in result.stderr:
                raise RuntimeError("cannot inspect owned launchd service")
            return {"loaded": False, "running": False, "pid": 0}
        pid = 0
        for line in result.stdout.decode().splitlines():
            if line.strip().startswith("pid = "):
                pid = int(line.strip().split(" = ", 1)[1])
        return {"loaded": True, "running": pid > 0, "pid": pid}
    result = run(["systemctl", "--user", "show", identity + ".service", "--property=MainPID,LoadState"], check=False)
    if result.returncode:
        raise RuntimeError("cannot inspect owned systemd service")
    fields = dict(line.split("=", 1) for line in result.stdout.decode().splitlines() if "=" in line)
    pid = int(fields.get("MainPID", "0"))
    return {"loaded": fields.get("LoadState") == "loaded", "running": pid > 0, "pid": pid}


def fingerprint(pid):
    if pid <= 1:
        return ""
    result = run(["ps", "-p", str(pid), "-o", "lstart=", "-o", "command="], check=False)
    return hashlib.sha256(result.stdout).hexdigest() if result.returncode == 0 and result.stdout.strip() else ""


def receipt(config, name):
    path = Path(config["root"]) / "run" / (name + ".json")
    if not path.exists():
        return {}
    if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077:
        raise ValueError("invalid private service receipt")
    return json.loads(path.read_text())


def start(config, name):
    path = assert_definition(config, name)
    current = service_status(config, name)
    previous = receipt(config, name)
    if current["running"]:
        if current["pid"] != previous.get("pid") or fingerprint(current["pid"]) != previous.get("fingerprint"):
            raise ValueError("service process identity is not confirmed")
        if name != "postgres" and previous.get("release") != config["release"]["id"]:
            raise ValueError("running service uses a different release; use upgrade")
    marker = Path(config["root"]) / "run" / (name + ".stop")
    if marker.is_symlink():
        raise ValueError("invalid stop marker")
    marker.unlink(missing_ok=True)
    if current["running"]:
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    durable(path, definition(config, name))
    if config["os"] == "darwin":
        target = domain() + "/" + label(config, name)
        run(["launchctl", "enable", target])
        if not current["loaded"]:
            run(["launchctl", "bootstrap", domain(), path])
        run(["launchctl", "kickstart", target])
    else:
        run(["systemctl", "--user", "daemon-reload"])
        run(["systemctl", "--user", "enable", "--now", label(config, name) + ".service"])


def stop(config, name, timeout=45):
    path = assert_definition(config, name)
    current = service_status(config, name)
    before = receipt(config, name)
    marker = Path(config["root"]) / "run" / (name + ".stop")
    durable(marker, "operator stop\n")
    if current["running"]:
        if before.get("pid") != current["pid"] or before.get("fingerprint") != fingerprint(current["pid"]):
            raise ValueError("refusing to signal an unverified service generation")
        if config["os"] == "darwin":
            run(["launchctl", "kill", "SIGTERM", domain() + "/" + label(config, name)])
        else:
            run(["systemctl", "--user", "stop", label(config, name) + ".service"], timeout=timeout)
    deadline = time.monotonic() + timeout
    while True:
        current = service_status(config, name)
        original_alive = bool(before.get("fingerprint")) and fingerprint(before.get("pid", 0)) == before["fingerprint"]
        if not current["running"] and not original_alive:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError(f"{name} has not stopped; no force stop was sent")
        time.sleep(.1)
    after = receipt(config, name)
    if before and (after.get("generation") != before.get("generation") or after.get("exit_code") != 0):
        raise RuntimeError(f"{name} exited without a clean original-generation receipt")
    if config["os"] == "darwin":
        if current["loaded"]:
            run(["launchctl", "bootout", domain() + "/" + label(config, name)])
    elif path.exists():
        run(["systemctl", "--user", "disable", label(config, name) + ".service"])
    path.unlink(missing_ok=True)


def serve(config, name, command, env):
    root = Path(config["root"])
    # This guard precedes even receipt/log creation. An empty KeepAlive restart
    # must not overwrite the original generation's failed shutdown result.
    if (root / "run" / (name + ".stop")).exists():
        return 0
    state = {"generation": str(uuid.uuid4()), "pid": os.getpid(),
             "fingerprint": fingerprint(os.getpid()), "release": config["release"]["id"],
             "exit_code": None}
    record = root / "run" / (name + ".json")
    write_json(record, state)
    child = None
    requested = []
    def forward(number, _):
        requested.append(number)
        if child is not None:
            try:
                child.send_signal(number)
            except ProcessLookupError:
                pass
    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP, signal.SIGQUIT):
        signal.signal(number, forward)
    try:
        if requested or (root / "run" / (name + ".stop")).exists():
            code = 0
        else:
            child = subprocess.Popen(command, env=env, stdin=subprocess.DEVNULL,
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            code = child.wait()
    except OSError:
        code = 1
    state["exit_code"] = code
    write_json(record, state)
    return code if code >= 0 else 128 - code
