#!/usr/bin/env python3
"""Exercise a built bupt against a private Unix socket and synthetic data."""
import json
import os
import re
import select
import shlex
import signal
import socket
import socketserver
import struct
import subprocess
import sys
import tempfile
import threading
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

if len(sys.argv) != 2:
    sys.exit("usage: python3 scripts/check_cli_acceptance.py PATH_TO_BUPT")
binary = str(Path(sys.argv[1]).resolve())
work = tempfile.TemporaryDirectory(prefix='bupt-cli-check-')
root = Path(work.name)
cfg = root/'config.toml'
cfg.write_text("version = 1\ndefault_profile = 'audit'\n[profiles.audit]\nplatform_url = 'https://school.invalid'\n")
reqs = []
bytes_sample = b'\x00\x1b[31m\xff\n'
fault = None
model = {"id": "audit-chat", "name": "Audit Chat", "kind": "chat", "context": "1234"}
campus_today = datetime.now(timezone(timedelta(hours=8))).date()


class Mock(socketserver.StreamRequestHandler):
    def handle(self):
        q = json.loads(self.rfile.readline())
        reqs.append(q)
        m, p = q['method'], q['params'] or {}
        assert q["profile"] == "audit"

        def reply(data=None, error=None):
            self.wfile.write(json.dumps({"id": q["id"], "result": data, "error": error}).encode() + b'\n')

        def frame(t, data):
            self.wfile.write(struct.pack(">BI", t, len(data)) + data)

        try:
            if fault == "timeout":
                time.sleep(.35)
                return
            if fault == "drop":
                return
            if fault == "denied":
                reply(error={"code":"OWNER_REQUIRED","message":"audit rejection"})
                return
            if fault in ("ledger", "ledger-interrupted") and m == "compute.read":
                assert p["section"] == "billing" and p["limit"] == 100
                first = {"id": 2, "appName": "lab", "endAt": campus_today.isoformat(), "usagePoints": "0.10"}
                if p["page"] == 1:
                    reply({"items": [first], "total": 2, "page": 1})
                elif fault == "ledger-interrupted":
                    reply(error={"code": "CONTROLLER_IO", "message": "isolated second page interruption"})
                else:
                    second = {**first, "id": 1, "usagePoints": "0.20"}
                    reply({"items": [first, second], "total": 2, "page": 2})
                return
            if m == 'connect.proxy':
                reply({})
                self.wfile.write(bytes_sample)
            elif m in {'connect.session', 'connect.download', 'connect.forward', 'data.download', 'job.logs'}:
                reply({})
                frame(1, bytes_sample)
                if m == 'connect.session':
                    frame(2,b'remote stderr\n')
                frame(4, json.dumps({"code":124 if m == "connect.session" else 0}).encode())
            elif m in {'connect.upload','data.upload'}:
                reply({})
                data = b''
                while True:
                    header = self.rfile.read(5)
                    if not header: return
                    k,n = struct.unpack(">BI",header)
                    b = self.rfile.read(n)
                    if k == 6: break
                    assert k == 0
                    data += b
                assert data == bytes_sample and len(data) == p["size"]
                frame(4, b'{"code":0}')
            elif m == 'portal.read':
                days = p.get("days", 30)
                dates = [(campus_today - timedelta(days=n)).isoformat() for n in reversed(range(days))]
                reply({
                    "section": p["section"], "source": "audit", "models": [model],
                    "keys": [{"id": "k-audit", "status": "active", "models": ["audit-chat"]}],
                    "account": {"balance_points": 12.5},
                    "data": {"base_url": "https://school.invalid/v1", "days": days, "dates": dates,
                             "usageAvailable": True, "periodCalls": 0, "periodCostPoints": 0},
                })
            elif m == "compute.read" and p["section"] == "billing":
                reply({"items": [], "total": 0, "page": p["page"]})
            elif m in {'status.get','resources.overview','quota.get','spec.estimate','compute.read','portal.change','client.list','key.list','doctor','auth.logout'}:
                reply({})
            elif m in {'instance.list','job.list','data.list','data.config'}:
                reply([])
            elif m == 'auth.start':
                reply({"stage":"webvpn","state":"pending"})
            elif m == 'portal.trial':
                reply({"model":model["id"],"kind":"chat","text":"ack","request_id":"gateway-audit"})
            else:
                reply({"id":"audit-receipt","state":"preparing","name":"lab"})
        except BrokenPipeError:
            pass

class Server(socketserver.ThreadingUnixStreamServer):
    daemon_threads = True


sock = str(root/'a.sock')
srv = Server(sock, Mock)
threading.Thread(target=srv.serve_forever, daemon=True).start()
env = {k: v for k, v in os.environ.items() if not k.startswith(("BUPT_", "XDG_"))}
env.update(TERM="dumb", HOME=str(root))
base = [binary, '--config',str(cfg),'--data-dir',str(root/'data'),'--state-dir',str(root/'state'),'--socket',sock,'--no-project','--non-interactive']
def run(s, data=b'', bare=False):
    return subprocess.run(([binary] if bare else base)+shlex.split(s), input=data, cwd=root, env=env, capture_output=True, timeout=4)
def case(s, expected, status=0, input=b'', check=None):
    reqs.clear()
    r = run(s, input)
    got = [(q['method'], (q['params'] or {}).get('section') or (q['params'] or {}).get('action')) for q in reqs]
    fmt = ','.join(n+(':'+d if d else '') for n,d in got)
    good = fmt==expected and r.returncode==status
    if good and check:
        try:
            good = check(r)
        except (ValueError, KeyError):
            good = False
    print(('PASS' if good else 'FAIL'), s, f'exit={r.returncode} calls={fmt}', f'diagnostic={r.stderr[:350]!r}' if not good or status not in (0, 124) else '')
    if not good and reqs:
        print('  params', [q['params'] for q in reqs])
    return good


def check_argv_and_options(command):
    if command.startswith("exec lab --"):
        return reqs[-1]["params"]["options"]["command"] == ["echo", "--json", "-q"]
    if command.startswith("compute instance create lab") and reqs:
        params = reqs[-1]["params"]
        return params["idempotency_key"] == "create-audit" and params["max_points_per_day"] == "12"
    if command.startswith("compute job run"):
        params = reqs[-1]["params"]
        return params["target"] == "lab" and params["command"] == ["echo", "--x"]
    return True

good = []
help_ = run('help --all',bare=True)
paths = [m[0].strip() for m in re.findall(r"^((?:  )+[a-z0-9-]+(?: [a-z0-9-]+)*)( +)(?=\S)",help_.stdout.decode().split("全局选项")[0],re.M)]
print("public/advanced paths",len(paths))
for path in paths:
    help_result = run("help "+path,bare=True)
    if help_result.returncode or not help_result.stdout:
        print("HELP FAIL",path,help_result.returncode)
    good += [help_result.returncode==0 and bool(help_result.stdout)]
for cmd, want in [
    ('login --device --no-wait --json','auth.start'), ('logout --json','auth.logout'),
    ('api --json','portal.read:overview'),
    ('api model --json','portal.read:model'), ('api model ls --jsonl','portal.read:model'),
    ('api model show audit-chat --output id','portal.read:model'),
    ('api model example audit-chat --format python','portal.read:model,portal.read:guide'),
    ('api model invoke audit-chat --sample --key k-audit --json','portal.read:model,portal.trial'),
    ('api key --output id','portal.read:key'), ('api key ls --json','portal.read:key'),
    ('api key show k-audit --json','portal.read:key'), ('api key create tool --model audit-chat --json','portal.change:key.create'),
    ('api key enable k-audit --json','portal.change:key.enable'), ('api key disable k-audit --json','portal.change:key.disable'),
    ('api key delete k-audit --confirm k-audit --json','portal.change:key.delete'),
    ('api usage --field balance','portal.read:usage'), ('api leaderboard --json','portal.read:leaderboard'),
    ('compute --json','status.get'), ('compute catalog --json','resources.overview'),
    ('compute catalog ls --json','resources.overview'), ('compute catalog show 9 --json','spec.show'),
    ('compute catalog estimate --spec 9 --json','spec.estimate'),
    ('compute instance --json','instance.list'), ('compute instance ls --json','instance.list'),
    ('compute instance show lab --json','instance.inspect'),
    ('compute instance create lab --spec 9 --max-runtime 30m --max-points-per-day 12 --output id --idempotency-key create-audit','instance.create'),
    ('compute instance start lab --max-runtime 30m --json','instance.start'),
    ('compute instance stop lab --json','instance.stop'), ('compute instance restart lab --json','instance.restart'),
    ('compute instance release lab --json','instance.release'), ('compute instance restore lab --max-runtime 30m --json','instance.restore'),
    ('compute instance delete lab --confirm 123 --json','instance.delete'),
    ('compute instance wait lab --state running --json','instance.wait'), ('compute instance events lab --json','instance.events'),
    ('compute instance cancel-queue lab --json','instance.cancel-queue'),
    ('compute storage --json','data.config'), ('compute storage ls / --json','data.list'),
    ('compute storage mkdir /work --json','data.edit:mkdir'), ('compute storage rename /work next --json','data.edit:rename'),
    ('compute storage rm /work --confirm work --json','data.edit:remove'),
    ('compute job --json','job.list'), ('compute job ls --json','job.list'),
    ('compute job run --instance lab --detach --json -- echo --x','job.run'),
    ('compute job show j --json','job.get'), ('compute job cancel j --json','job.cancel'),
    ('compute scheduler job --json','compute.read:job'), ('compute scheduler job ls --json','compute.read:job'),
    ('compute scheduler job show 123 --cluster c1 --json','compute.read:job.show'), ('compute scheduler job logs 123 --cluster c1 --json','compute.read:job.log'),
    ('compute scheduler request --json','compute.read:job.request'), ('compute scheduler request show 123 --json','compute.read:job.request.show'),
    ('compute scheduler template --json','compute.read:job.template'), ('compute scheduler template show 123 --json','compute.read:job.template.show'),
    ('compute quota --json','quota.get'), ('compute usage --json','compute.read:billing'),
    ('usage --json','portal.read:usage,compute.read:billing'), ('doctor --json','doctor'),
    ('config validate --json',''), ('agent status --json','agent.status'), ('client ls --json','client.list'),
    ('client key ls --json','key.list'), ('profile ls --json',''),
    ('remote --json','compute.read:remote.order'), ('project --json','compute.read:project'),
    ('cluster --json','compute.read:cluster'), ('admin --json','compute.read:admin'),
]:
    def data_check(r):
        if not check_argv_and_options(cmd):
            return False
        if cmd.startswith("api model example"):
            return b'audit-chat' in r.stdout and b'import ' in r.stdout and not r.stderr
        if "--output id" in cmd:
            return r.stdout.endswith(b"\n") and b" " not in r.stdout and not r.stderr
        if cmd.endswith("--field balance"):
            return r.stdout == b"12.5\n" and not r.stderr
        if cmd.endswith("--jsonl"):
            return all(json.loads(line) for line in r.stdout.splitlines()) and bool(r.stdout) and not r.stderr
        return json.loads(r.stdout) is not None and (not r.stderr or cmd.startswith("login "))
    good += [case(cmd, want, check=data_check)]
for cmd, want, status, out, err, stdin in [
    ('compute job logs j','job.logs',0,bytes_sample,b'',b''),
    ('exec lab -- echo --json -q','connect.session',124,bytes_sample,b'remote stderr\n',b''),
    ('ssh lab','connect.session',124,bytes_sample,b'remote stderr\n',b''),
    ('proxy lab','connect.proxy',0,bytes_sample,b'',b''),
    ('cp lab:path -','connect.download',0,bytes_sample,b'',b''),
    ('compute storage pull path -','data.download',0,bytes_sample,b'',b''),
    (f'cp - lab:path --size {len(bytes_sample)} --json','connect.upload',0,None,b'',bytes_sample),
    (f'compute storage push - path --size {len(bytes_sample)} --json','data.upload',0,None,b'',bytes_sample),
    ('exec lab echo --json','',2,b'',None,b''),
    ('forward lab 8888 --dry-run --json','',0,None,b'',b''),
    ('compute instance create lab --spec 9 --max-runtime 30m --output json','',2,b'',None,b'y\n'),
    ('compute instance stop lab -q','',2,b'',None,b''),
    ('tui','',2,b'',None,b''),
]:
    def stream_check(r):
        stdout_ok = bool(r.stdout) if out is None else r.stdout == out
        stderr_ok = bool(r.stderr) if err is None else r.stderr == err
        return stdout_ok and stderr_ok and check_argv_and_options(cmd)
    good += [case(cmd, want, status, stdin, stream_check)]
fault = "denied"
good += [case('admin quota set us u --quota-field maxGpu --value 5 --revision r --json','compute.admin.update',1,check=lambda r:not r.stdout and json.loads(r.stderr)['error']['code']=='OWNER_REQUIRED')]
fault = "drop"
good += [case('api model ls --json', 'portal.read:model', 1, check=lambda r: not r.stdout and json.loads(r.stderr)['error']['code'] == 'CONTROLLER_IO')]
fault = "timeout"
good += [case('api model ls --timeout 50ms --json','portal.read:model',1,check=lambda r:not r.stdout and json.loads(r.stderr)['error']['code']=='TIMEOUT')]
reqs.clear()
with subprocess.Popen(
    base + ["compute", "instance", "wait", "lab", "--json"],
    cwd=root, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
) as waiter:
    try:
        for _ in range(80):
            if reqs:
                break
            time.sleep(.01)
        waiter.send_signal(signal.SIGINT)
        output, diagnostic = waiter.communicate(timeout=3)
        success = waiter.returncode == 130 and not output and json.loads(diagnostic)["error"]["code"] == "INTERRUPTED"
        print("PASS" if success else "FAIL", "instance wait SIGINT", waiter.returncode)
        good += [success]
    finally:
        if waiter.poll() is None:
            waiter.kill()
            waiter.wait()


def complete_ledger(result):
    report = json.loads(result.stdout)
    ledger = report["compute"]["data"]
    return (not result.stderr and report["api"]["state"] == "ok" and ledger["complete"] and ledger["pages_read"] == 2
            and len(ledger["items"]) == 2 and ledger["reported_bill_points"] == "0.30" and ledger["duplicates"] == 1)


def partial_ledger(result):
    report = json.loads(result.stdout)
    ledger = report["compute"]["data"]
    return (not result.stderr and report["compute"]["state"] == "partial" and not ledger["complete"]
            and ledger["next_page"] == 2 and len(ledger["items"]) == 1 and "reported_bill_points" not in ledger)


for fault, verify in (("ledger", complete_ledger), ("ledger-interrupted", partial_ledger)):
    good += [case("usage --days 7 --json", "portal.read:usage,compute.read:billing,compute.read:billing", check=verify)]

fault = None
reqs.clear()
with subprocess.Popen(
    base + ["forward", "lab", "8080", "--json"],
    cwd=root, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
) as forward:
    try:
        if not select.select([forward.stdout], [], [], 3)[0]:
            raise TimeoutError("bupt forward did not publish its listening address")
        ready = json.loads(forward.stdout.readline())
        host, port = ready["listen"].rsplit(":", 1)
        with socket.create_connection((host, int(port)), timeout=2) as tunnel:
            reply = tunnel.makefile("rb").read()
        assert host == "127.0.0.1" and reply == bytes_sample
        assert len(reqs) == 1 and reqs[0]["method"] == "connect.forward" and reqs[0]["params"]["port"] == 8080
        forward.send_signal(signal.SIGINT)
        output, diagnostic = forward.communicate(timeout=3)
        success = forward.returncode == 130 and not output and json.loads(diagnostic)["error"]["code"] == "INTERRUPTED"
        print("PASS" if success else "FAIL", "forward loopback byte stream and SIGINT", forward.returncode)
        good += [success]
    finally:
        if forward.poll() is None:
            forward.kill()
            forward.wait()

v = run("version", bare=True)
flag = run("--version", bare=True)
machine = run("version --json", bare=True)
success = v.returncode == flag.returncode == machine.returncode == 0 and v.stdout == flag.stdout
success = success and v.stdout.decode().strip().endswith(json.loads(machine.stdout)["version"])
print("PASS" if success else "FAIL", "version, --version and --json")
good += [success]
print("TOTAL", sum(good), "/", len(good))
srv.shutdown()
srv.server_close()
work.cleanup()
sys.exit(0 if all(good) else 1)
