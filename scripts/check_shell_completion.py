#!/usr/bin/env python3
"""Exercise the generated Bash, Zsh and Fish completion entry points."""
import argparse
import json
import os
from pathlib import Path
import shlex
import shutil
import socketserver
import subprocess
import tempfile
import threading
import time

from check_tui_session import Terminal


class Fixture(socketserver.StreamRequestHandler):
    def handle(self):
        request = json.loads(self.rfile.readline())
        method = request["method"]
        assert request["params"] == {"cached": True}, request
        if method == "instance.list":
            value = [
                {"id": "1234", "name": "lab-node"},
                {"id": "1235", "name": "lab-node"},
                {"id": "2345", "name": "gpu classroom"},
                {"id": "9999", "name": "bad\ncontrol"},
            ]
        elif method == "resources.overview":
            value = {"pools": [{"templates": [{"id": "9", "name": "gpu-a"}]}]}
        else:
            raise AssertionError(f"unexpected completion RPC: {method}")
        self.wfile.write(json.dumps({"id": request["id"], "result": value}).encode() + b"\n")


def check(binary, fish):
    with tempfile.TemporaryDirectory(prefix="bupt-comp-") as temp:
        root = Path(temp)
        bin_dir = root / "bin"
        bin_dir.mkdir()
        (bin_dir / "bupt").symlink_to(binary)
        config = root / "config.toml"
        config.write_text("version=1\n", encoding="utf-8")
        sock = root / "a.sock"
        args = ["bupt", "--config", str(config), "--socket", str(sock), "--no-project"]
        env = {k: v for k, v in os.environ.items() if not k.startswith(("BUPT_", "XDG_"))}
        env.update(HOME=temp, PATH=f"{bin_dir}:{env['PATH']}")
        with socketserver.UnixStreamServer(str(sock), Fixture) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                for shell in ("bash", "zsh", "fish"):
                    source = subprocess.check_output([binary, "completion", shell], timeout=4, env=env)
                    (root / f"comp.{shell}").write_bytes(source)
                commands = [
                    (["compute", "instance", "show", "12"], {"1234", "1235"}),
                    (["compute", "catalog", "show", "gpu"], {"gpu-a"}),
                    (["compute", "instance", "create", "--spec", "gpu"], {"gpu-a"}),
                ]
                for words, expected in commands:
                    command = args + words
                    line = shlex.join(command)
                    bash = """
source comp.bash
COMP_WORDS=({words})
COMP_CWORD={index}
COMP_LINE={line}
COMP_POINT=${{#COMP_LINE}}
__start_bupt
printf '%s\\n' "${{COMPREPLY[@]}}"
""".format(words=line, index=len(command) - 1, line=shlex.quote(line))
                    out = subprocess.check_output(["bash", "-c", bash], cwd=temp, env=env, timeout=5, text=True)
                    assert set(out.splitlines()) == expected, out
                    if fish:
                        code = "source comp.fish; complete -C " + shlex.quote(line)
                        out = subprocess.check_output([fish, "--no-config", "-c", code], cwd=temp, env=env, timeout=5, text=True)
                        assert set(line.split("\t", 1)[0] for line in out.splitlines()) == expected, out
                print("Bash: generated COMP_WORDS → instance IDs, template and --spec")
                if fish:
                    print("Fish: complete -C → instance IDs, template and --spec")
                else:
                    print("Fish: skipped, interpreter unavailable")

                t = Terminal(["zsh", "-d", "-f"], temp)
                try:
                    setup = f"export PATH={shlex.quote(str(bin_dir))}:$PATH; PS1='ready> '; autoload -Uz compinit; compinit -u; source ./comp.zsh; compdef _bupt bupt\r"
                    t.send(setup)
                    t.expect("ready>")
                    t.settle(.35)
                    # POSIX tty commands are edited with TAB. No resource command executes.
                    t.send(shlex.join(args + ["compute", "catalog", "show", "gp"]) + "\t")
                    t.until(lambda: "gpu-a" in t.screen.text(), "zsh tab completion for a template")
                    t.send(b"\x03")
                    t.settle()
                    t.send(shlex.join(args + ["compute", "instance", "show", "23"]) + "\t")
                    t.until(lambda: "2345" in t.screen.text(), "zsh tab completion for instance ID")
                    t.send(b"\x03")
                    t.settle()
                    t.send("\x15exit 0\r")
                    assert t.wait() == 0
                    print("Zsh: normal TAB on template and instance ID")
                finally:
                    t.close()
            finally:
                server.shutdown()

        start = time.monotonic()
        completed = subprocess.run([binary, "__completeNoDesc", *args[1:], "compute", "instance", "show", ""],
                                   cwd=temp, env=env, capture_output=True, timeout=3, text=True)
        assert completed.returncode == 0 and completed.stdout.strip() == ":4" and time.monotonic() - start < 2, completed
        print("Unavailable controller: no candidates, no command failure")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=lambda path: str(Path(path).resolve()))
    parser.add_argument("--fish", default=shutil.which("fish"))
    opts = parser.parse_args()
    check(opts.binary, opts.fish)
