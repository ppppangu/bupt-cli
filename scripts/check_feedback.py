#!/usr/bin/env python3
"""Verify a built bupt through subprocesses and a real TUI PTY."""
import base64
import json
from pathlib import Path
import re
import shlex
import socketserver
import subprocess
import sys
import tempfile
import threading

from check_tui_session import Terminal


ROOT = Path(__file__).resolve().parents[1]
EMAIL = re.search(r"\(mailto:([^()]+)\)", (ROOT / "README.md").read_text())[1]
RECEIPT = "d81283c2-ae5f-4d03-aaee-2437342f29c3"
PRIVATE = ["fake-password", "sk-fake-key", "fake-token", "/Users/private/.ssh/id_ed25519",
           "private-instance", "student@private.example", "192.0.2.81"]
OSC52 = re.compile(rb"\x1b\]52;[^;]*;([A-Za-z0-9+/=]+)(?:\x07|\x1b\\)")


def assert_redacted(text):
    assert all(item not in text for item in PRIVATE), "private data in a feedback output"


class Fixture(socketserver.StreamRequestHandler):
    def handle(self):
        req = json.loads(self.rfile.readline())
        method = req["method"]
        result, error = {}, None
        if method == "agent.status":
            result = {"state": "running", "token": PRIVATE[2], "address": PRIVATE[6]}
        elif method == "status.get":
            result = {
                "source": "live", "snapshot_available": True, "gateway": "authenticated",
                "platform": "authenticated", "profile": "feedback-test",
                "instances": [{"id": "20", "name": PRIVATE[4], "state": "running",
                               "reserved": True, "nodes": [{"id": "40", "name": "worker"}]}],
                "routes": [{"instance_id": "20", "node_id": "40", "state": "ready",
                            "host": PRIVATE[6], "username": PRIVATE[5]}],
            }
        elif method == "account.snapshot":
            result = {"vpn": "not_required", "platform": "authenticated", "revision": 1}
        elif method == "account.watch":
            error = {"code": "NOT_FOUND", "message": "this fixture uses snapshots"}
        elif method == "connect.session":
            error = {
                "code": "SSH_CONNECTION_FAILED", "message": "连接通道不可用 " + " ".join(PRIVATE),
                "request_id": RECEIPT, "run_id": PRIVATE[3], "task_id": PRIVATE[1],
                "idempotency_key": PRIVATE[2],
            }
        else:
            error = {"code": "NOT_FOUND", "message": "unsupported fixture operation"}
        self.wfile.write(json.dumps({"id": req["id"], "result": result, "error": error}).encode() + b"\n")


class Server(socketserver.ThreadingUnixStreamServer):
    daemon_threads = True


def clipboard(t):
    payloads = [base64.b64decode(m).decode() for m in OSC52.findall(t.transcript)]
    return payloads[-1] if payloads else ""


def check_cli(binary, root, argv):
    broken = root / "broken.toml"
    broken.write_text("invalid [")
    offline = [binary, "--config", str(broken), "--no-project"]
    for args in (["feedback"], ["feedback", "--template"]):
        out = subprocess.run(offline + args, check=True, text=True, capture_output=True)
        assert EMAIL in out.stdout and out.stderr == ""
    report = subprocess.run(offline + ["doctor", "--report", "--json"], check=True, text=True, capture_output=True)
    data = json.loads(report.stdout)
    assert data["configuration"] == "invalid" and data["controller"] == "not_checked" and report.stderr == ""
    assert str(broken) not in report.stdout
    live = subprocess.run(argv + ["doctor", "--report", "--json"], check=True, text=True, capture_output=True)
    assert json.loads(live.stdout)["controller"] == "reachable"
    assert_redacted(live.stdout)
    stopped = subprocess.run(argv + ["--socket", str(root / "missing.sock"), "doctor", "--report", "--json"],
                             check=True, text=True, capture_output=True)
    assert json.loads(stopped.stdout)["controller"] == "unreachable"
    print("CLI: contact without configuration; valid, malformed and disconnected diagnostic cases", flush=True)


def check_tui(argv, work):
    t = Terminal(argv + ["tui"], work)
    try:
        t.expect("帮助与反馈")
        t.send("?")
        t.expect("反馈问题", "联系作者")
        t.choose("联系作者")
        t.expect(EMAIL, "复制作者邮箱")
        t.choose("复制作者邮箱")
        t.until(lambda: clipboard(t) == EMAIL, "OSC 52 contact copy")
        t.esc()
        t.esc()
        t.send("S")
        t.expect("我的实例")
        t.send("1")
        t.expect(PRIVATE[4])
        t.choose(PRIVATE[4])
        t.expect("连接 SSH")
        t.choose("连接 SSH")
        t.expect("SSH 连接失败", "反馈此问题")
        t.choose("反馈此问题")
        t.expect("反馈邮件预览", "复制邮件内容")
        t.choose("复制邮件内容")
        t.until(lambda: RECEIPT in clipboard(t), "OSC 52 error feedback copy")
        body = clipboard(t)
        assert "SSH_CONNECTION_FAILED" in body and "bupt ssh" in body and EMAIL in body
        assert "bupt 脱敏诊断" not in body
        assert_redacted(body)
        t.choose("附加脱敏诊断")
        t.expect("附加到邮件", "可连接")
        t.choose("附加到邮件")
        t.expect("反馈邮件预览")
        t.choose("复制邮件内容")
        t.until(lambda: "bupt 脱敏诊断" in clipboard(t), "optional diagnostics in mail")
        assert_redacted(clipboard(t))
        t.esc()
        t.expect("SSH 连接失败")
        t.send("?")
        t.expect("本页帮助")
        t.choose("帮助与反馈")
        t.expect("反馈问题", "联系作者")
        t.esc()
        t.esc()
        t.esc()
        t.expect("连接 SSH")
        t.quit()
    finally:
        t.close()
    print("TUI PTY: author copy, SSH error context, opt-in safe diagnostics, help and return", flush=True)


def check_error_hint(argv, work):
    t = Terminal(argv + ["ssh", PRIVATE[4]], work)
    try:
        assert t.wait() == 1
        t.settle()
        hint = next(line for line in t.transcript.decode().splitlines() if "反馈模板" in line)
        assert "SSH_CONNECTION_FAILED" in hint and RECEIPT in hint and "--at" in hint
        assert_redacted(hint)
        args = shlex.split(hint.partition("bupt ")[2])
        draft = subprocess.run(argv + args, check=True, text=True, capture_output=True)
        assert RECEIPT in draft.stdout and "发生时间：" in draft.stdout and draft.stderr == ""
        assert_redacted(draft.stdout)
    finally:
        t.close()
    print("CLI PTY: scoped feedback and doctor hints on SSH failure", flush=True)


if __name__ == "__main__":
    binary = str(Path(sys.argv[1]).resolve())
    with tempfile.TemporaryDirectory(prefix="bupt-feedback-", dir="/tmp") as work:
        root = Path(work)
        cfg, socket = root / "config.toml", root / "rpc.sock"
        cfg.write_text("version=1\ndefault_profile='feedback-test'\n[profiles.feedback-test]\nplatform_url='https://school.invalid'\n")
        with Server(str(socket), Fixture) as server:
            threading.Thread(target=server.serve_forever, daemon=True).start()
            argv = [binary, "--config", str(cfg), "--socket", str(socket), "--no-project",
                    "--data-dir", work, "--state-dir", work]
            try:
                check_cli(binary, root, argv)
                check_tui(argv, work)
                check_error_hint(argv, work)
            finally:
                server.shutdown()
