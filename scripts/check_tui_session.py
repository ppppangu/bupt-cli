#!/usr/bin/env python3
"""Drive one real bupt TUI process through an isolated Controller and SSH."""
import codecs
import errno
import fcntl
import os
from pathlib import Path
import pty
import re
import select
import struct
import subprocess
import sys
import termios
import time
import unicodedata


class Screen:
    """Interpret the cursor and erase sequences used by tcell's xterm screen."""
    csi = re.compile(r"\x1b\[([0-?]*)([ -/]*)([@-~])")

    def __init__(self, cols, rows):
        self.decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
        self.pending = ""
        self.alt = False
        self.resize(cols, rows)

    def resize(self, cols, rows):
        self.cols, self.rows = cols, rows
        self.clear()

    def clear(self):
        self.cells = [[" "] * self.cols for _ in range(self.rows)]
        self.x = self.y = 0

    def feed(self, raw):
        text = self.pending + self.decoder.decode(raw)
        self.pending = ""
        i = 0
        while i < len(text):
            r = text[i]
            if r == "\x1b":
                if i + 1 == len(text):
                    break
                kind = text[i + 1]
                if kind == "[":
                    token = self.csi.match(text, i)
                    if token is None:
                        break
                    self.control(token[1], token[3])
                    i = token.end()
                    continue
                if kind in "]P_":
                    rest = text[i + 2:]
                    stop = re.search(r"\x07|\x1b\\", rest)
                    if stop is None:
                        break
                    i += 2 + stop.end()
                    continue
                if kind in "()":
                    if i + 2 == len(text):
                        break
                    i += 3
                else:
                    i += 2
                continue
            if r == "\r":
                self.x = 0
            elif r == "\n":
                self.y = min(self.rows - 1, self.y + 1)
            elif r == "\b":
                self.x = max(0, self.x - 1)
            elif r >= " " and r != "\x7f" and self.x < self.cols:
                if unicodedata.combining(r) and self.x:
                    self.cells[self.y][self.x - 1] += r
                else:
                    self.cells[self.y][self.x] = r
                    width = 2 if unicodedata.east_asian_width(r) in ("W", "F") else 1
                    if width == 2 and self.x + 1 < self.cols:
                        self.cells[self.y][self.x + 1] = ""
                    self.x += width
            i += 1
        self.pending = text[i:]

    def control(self, params, op):
        if params == "?1049" and op in "hl":
            self.alt = op == "h"
            self.clear()
            return
        if params.startswith(("?", ">")):
            return
        values = [int(x or 0) if x.isdigit() or not x else 0 for x in params.split(";")]
        n = values[0] or 1
        if op in "Hf":
            self.y = min(self.rows - 1, n - 1)
            self.x = min(self.cols - 1, ((values[1] or 1) if len(values) > 1 else 1) - 1)
        elif op == "J" and values[0] in (2, 3):
            self.clear()
        elif op == "K":
            start = self.x if values[0] == 0 else 0
            end = min(self.cols, self.x + 1) if values[0] == 1 else self.cols
            self.cells[self.y][start:end] = [" "] * (end - start)
        elif op == "A":
            self.y = max(0, self.y - n)
        elif op == "B":
            self.y = min(self.rows - 1, self.y + n)
        elif op == "C":
            self.x = min(self.cols - 1, self.x + n)
        elif op == "D":
            self.x = max(0, self.x - n)
        elif op == "G":
            self.x = min(self.cols - 1, n - 1)
        elif op == "d":
            self.y = min(self.rows - 1, n - 1)

    def text(self):
        return "\n".join("".join(row).rstrip() for row in self.cells)


class Terminal:
    def __init__(self, argv, workdir, cols=80, rows=24):
        self.master, self.slave = pty.openpty()
        self.original = termios.tcgetattr(self.slave)
        self.screen = Screen(cols, rows)
        self.resize(cols, rows)
        self.transcript = bytearray()

        def controlling_tty():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)

        env = {k: v for k, v in os.environ.items() if not k.startswith(("BUPT_", "XDG_", "TCELL_"))}
        env.update(HOME=workdir, TERM="xterm-256color", LC_ALL="en_US.UTF-8", NO_COLOR="1")
        self.proc = subprocess.Popen(
            argv, cwd=workdir, env=env,
            stdin=self.slave, stdout=self.slave, stderr=self.slave,
            preexec_fn=controlling_tty,
        )

    def resize(self, cols, rows):
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        self.screen.resize(cols, rows)

    def close(self):
        running = self.proc.poll() is None
        if running:
            self.proc.kill()
        os.close(self.master)
        os.close(self.slave)
        if running:
            self.proc.wait(timeout=5)

    def read(self, wait=0.1):
        if select.select([self.master], [], [], wait)[0]:
            try:
                data = os.read(self.master, 65536)
            except OSError as err:
                if err.errno != errno.EIO:
                    raise
                data = b""
            self.transcript += data
            self.screen.feed(data)

    def settle(self, seconds=0.15):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            self.read(0.02)

    def until(self, check, description, seconds=10):
        end = time.monotonic() + seconds
        while time.monotonic() < end and self.proc.poll() is None:
            self.read()
            if check():
                return
        raise AssertionError(
            f"expected {description}, exit={self.proc.poll()}\n"
            f"{self.screen.text()}\nrecent terminal bytes: {bytes(self.transcript[-350:])!r}"
        )

    def expect(self, *parts, seconds=10):
        self.until(lambda: all(x in self.screen.text() for x in parts), repr(parts), seconds)

    def raw(self, text):
        self.until(lambda: text.encode() in self.transcript, f"terminal stream {text!r}")

    def send(self, keys):
        os.write(self.master, keys.encode() if isinstance(keys, str) else keys)

    def esc(self):
        self.send(b"\x1b")
        self.settle(.20)

    def root(self):
        for _ in range(9):
            self.settle()
            if "校园算力平台" in self.screen.text() and "创建、连接和管理" in self.screen.text():
                return
            self.esc()
        self.expect("校园算力平台", "创建、连接和管理")

    def choose(self, title, picker=False):
        self.send("/\x15" + title)
        self.settle()
        self.send(b"\r")
        self.settle()
        if not picker:
            self.send(b"\r")
            self.settle()

    def edit(self, title, value):
        self.choose(title)
        self.send("\x15" + value + "\r")
        self.settle()

    def question(self, value):
        self.choose("你的问题")
        self.expect("编辑 · 你的问题", "Esc 保留草稿")
        self.send("\x15" + value + "\x13")
        self.settle()

    def quit(self):
        end = len(self.transcript)
        self.send("q")
        if self.wait() != 0:
            raise AssertionError(self.screen.text())
        self.settle(.05)
        assert b"\x1b[?1049l" in self.transcript[end:], "the TUI did not leave the alternate screen"
        try:
            restored = termios.tcgetattr(self.slave)
        except termios.error as err:
            # Darwin can invalidate the controlling slave as its session
            # leader exits. The SSH round trip checks modes while it is alive.
            if sys.platform != "darwin" or err.args[0] != errno.ENOTTY:
                raise
        else:
            assert restored == self.original, "terminal settings were not restored on exit"

    def wait(self, seconds=5):
        deadline = time.monotonic() + seconds
        while self.proc.poll() is None and time.monotonic() < deadline:
            self.read()
        return self.proc.wait(timeout=1)


def open_create(t):
    t.choose("裸算力")
    t.expect("我的实例")
    t.send("3")
    t.expect("资源与价格", "gpu-a")
    t.choose("gpu-a")
    t.expect("用此模板创建")
    t.choose("用此模板创建")
    t.expect("实例名称", "运行上限")


def journey(t, workdir):
    t.expect("校园算力平台")
    open_create(t)
    t.choose("实例名称")
    t.send("discard-this")
    t.esc()
    t.expect("原值保留")
    t.edit("实例名称", "pty-gpu")
    t.edit("运行上限", "21m")
    t.choose("核对创建实例")
    t.expect("1.25", "确认创建实例")
    t.choose("确认创建实例")
    t.expect("pty-gpu", "连接 SSH", seconds=25)
    tui_mode = termios.tcgetattr(t.slave)
    t.choose("连接 SSH")
    t.raw("fixture-shell> ")
    assert not t.screen.alt, "the TUI did not yield the alternate screen"
    t.send("terminal-check\n")
    t.raw("fixture-echo: terminal-check")
    t.send("exit\n")
    t.until(lambda: t.screen.alt and "pty-gpu" in t.screen.text(), "same instance page after SSH")
    assert termios.tcgetattr(t.slave) == tui_mode, "SSH did not restore the TUI terminal modes"
    t.choose("仅停止运行")
    t.expect("确认仅停止运行")
    t.choose("确认仅停止运行")
    t.expect("已停止", seconds=21)
    print("A: priced create → SSH input/output → same TUI → confirmed stop", flush=True)

    t.root()
    t.resize(70, 22)
    t.expect("校园算力平台")
    t.choose("模型 API")
    t.expect("模型与价格")
    t.resize(110, 34)
    t.send("2")
    t.expect("fixture-chat", "0.25")
    t.choose("fixture-chat")
    t.expect("在线调试")
    t.choose("在线调试")
    t.expect("你的问题", "fixture-chat")
    t.choose("调用密钥")
    t.expect("Primary key", "Backup key")
    t.choose("Primary key", picker=True)
    t.choose("发送请求")
    t.expect("请先补全输入")
    t.question("first-question")
    t.choose("发送请求")
    t.expect("answer-to-first-question", "1.25")
    t.question("second-question")
    t.choose("发送请求")
    t.expect("answer-to-second-question", "1.25", "6 Token")

    t.choose("生成调用代码")
    t.expect("代码工作台", "示例格式")
    t.choose("示例格式")
    t.expect("Python 标准库")
    t.choose("Python 标准库", picker=True)
    t.choose("调用密钥")
    t.expect("Primary key")
    t.choose("Primary key", picker=True)
    t.send("s")
    t.expect("保存到新文件")
    t.send("example.py\r")
    t.until(lambda: (Path(workdir) / "example.py").is_file(), "Python export")
    t.choose("在线调试")
    t.expect("本页筛选  /生成调用代码")
    t.send(b"\x7f")
    t.expect("second-question", "Primary key")
    print("C: Python export and retained draft/key in the same TUI", flush=True)

    t.question("unknown-response")
    t.choose("发送请求")
    t.expect("核查近期 API 用量")
    t.choose("发送请求")
    t.expect("结果未知")
    t.choose("反馈此问题")
    t.expect("反馈邮件预览", "MODEL_RESULT_UNKNOWN", "结果或扣费待核查")
    t.esc()
    t.expect("结果未知")
    t.choose("核查近期 API 用量")
    t.expect("已核查")
    t.choose("已核查，允许手动发起新请求", picker=True)
    t.question("expired-key")
    t.choose("发送请求")
    t.expect("所选 Key 已失效")
    t.send("r")
    t.send(b"\x7f")
    t.expect("Backup key")
    t.question("insufficient")
    t.choose("发送请求")
    t.expect("余额不足")
    print("B: edit, resend, tokens/cost, invalid input, unknown response, expired key, insufficient balance", flush=True)

    t.root()
    t.choose("余额与用量")
    t.expect("余额与用量", "模型账户余额")
    t.send("2")
    t.expect("2  模型 API")
    t.choose("统计周期")
    t.expect("近 30 天")
    t.choose("近 30 天", picker=True)
    t.expect("统计周期  近 30 天")
    t.send("3")
    t.expect("本期计量", "3.00 积分")
    t.choose("查看这些账单的明细")
    t.expect("账单明细 1/2")
    t.choose("下一页账单")
    t.expect("账单明细 2/2", "bill-01")
    t.send("1")
    t.expect("模型账户余额")
    t.send("r")
    t.expect("模型账户余额", "27.25")
    print("D: shared time range, two school pages, deduplicated points, detail pagination", flush=True)
    t.quit()


def exercise_drafts(argv, workdir):
    steps = [
        (None, "37m", "normal"),
        ("37m", "42m", "abrupt"),
        ("42m", None, "clear"),
    ]
    for before, after, action in steps:
        t = Terminal(argv, workdir)
        try:
            t.expect("校园算力平台")
            open_create(t)
            if before:
                t.expect(before, "仅申请或分配")
                assert "session-only-label" not in t.screen.text()
            if after:
                t.edit("实例名称", "session-only-label")
                t.edit("运行上限", after)
                if not before:
                    t.choose("创建方式")
                    t.expect("仅申请或分配")
            if action == "abrupt":
                t.proc.kill()
                t.wait()
            else:
                if action == "clear":
                    t.choose("清除本机草稿")
                    t.expect("已清除本机草稿")
                t.quit()
        finally:
            t.close()
    print("drafts: normal exit, SIGKILL recovery, clear; free text did not persist", flush=True)


if __name__ == "__main__":
    binary, cfg, socket, workdir = sys.argv[1:]
    argv = [binary, "--config", cfg, "--data-dir", workdir, "--state-dir", workdir, "--socket", socket,
            "--no-project", "--timeout", "6s", "tui"]
    t = Terminal(argv, workdir)
    try:
        journey(t, workdir)
    finally:
        t.close()
    exercise_drafts(argv, workdir)
