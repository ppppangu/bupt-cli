#!/usr/bin/env python3
"""Frame player for vhs demo recordings.

Usage: play.py FRAMES_DIR

Shows the first frame, then advances one frame per keystroke read from the
terminal. Holds the last frame briefly, then exits and restores the shell.
Used by assets/demo/bin/bupt so recordings never touch real accounts.
"""

import os
import select
import sys
import termios
import time
import tty


def read_key(fd):
    """Read one keystroke; CJK runes arrive as several UTF-8 bytes."""
    b = os.read(fd, 1)
    if not b:
        return None
    n = 0
    if b[0] & 0xE0 == 0xC0:
        n = 1
    elif b[0] & 0xF0 == 0xE0:
        n = 2
    elif b[0] & 0xF8 == 0xF0:
        n = 3
    for _ in range(n):
        rr, _, _ = select.select([fd], [], [], 0.05)
        if not rr:
            break
        try:
            b += os.read(fd, 1)
        except OSError:
            pass
    return b


def main() -> None:
    frames_dir = sys.argv[1]
    frames = sorted(f for f in os.listdir(frames_dir) if f.endswith(".ans"))
    if not frames:
        sys.exit(1)

    fd = sys.stdin.fileno()
    old = termios.tcgetattr(fd)
    try:
        tty.setraw(fd)
        sys.stdout.write("\x1b[2J\x1b[H\x1b[?25l")
        sys.stdout.flush()
        last = len(frames) - 1
        for i in range(len(frames)):
            sys.stdout.write("\x1b[2J\x1b[H")
            with open(os.path.join(frames_dir, frames[i]), encoding="utf-8") as fh:
                sys.stdout.write(fh.read())
            sys.stdout.flush()
            if i == last:
                time.sleep(1.5)
                break
            while True:
                r, _, _ = select.select([fd], [], [], 30)
                if not r:
                    # Keystroke never arrived; leave without hanging the tape.
                    time.sleep(0.1)
                    break
                ch = read_key(fd)
                if ch == b"\x1b":
                    # Swallow the rest of an escape sequence (arrows, etc.).
                    for _ in range(2):
                        rr, _, _ = select.select([fd], [], [], 0.05)
                        if not rr:
                            break
                        try:
                            os.read(fd, 1)
                        except OSError:
                            pass
                if ch:
                    break
        sys.stdout.write("\x1b[2J\x1b[H\x1b[?25h")
        sys.stdout.flush()
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old)


if __name__ == "__main__":
    main()
