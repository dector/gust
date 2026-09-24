#!/usr/bin/env python3
"""Relay an interactive terminal to Gust through a PTY for dev:self.

Exit 42 after a first Ctrl+C (restart Gust), 43 after a second one (stop
supervisor). Other Gust exits, including q, return 0. No dependencies beyond
Python's standard library.
"""

import errno
import fcntl
import os
import selectors
import signal
import subprocess
import sys
import termios
import time


def main() -> int:
    if len(sys.argv) < 2:
        return 2
    if not os.isatty(0):
        print("[dev:self] interactive relay requires a terminal", file=sys.stderr)
        return 2

    master, slave = os.openpty()

    def control_tty() -> None:
        fcntl.ioctl(0, termios.TIOCSCTTY, 0)

    try:
        child = subprocess.Popen(
            sys.argv[1:], stdin=slave, stdout=slave, stderr=slave,
            start_new_session=True, preexec_fn=control_tty,
        )
    finally:
        os.close(slave)

    saved = termios.tcgetattr(0)
    raw = termios.tcgetattr(0)
    raw[0] &= ~(termios.BRKINT | termios.ICRNL | termios.INPCK | termios.ISTRIP | termios.IXON)
    raw[3] &= ~(termios.ECHO | termios.ICANON | termios.IEXTEN | termios.ISIG)
    raw[6][termios.VMIN] = 1
    raw[6][termios.VTIME] = 0
    prior_ctrl_c = os.environ.get("DEV_SELF_PREVIOUS_CTRL_C") == "1"
    ctrl_c = False
    stop_wrapper = False
    stopping = False
    stop_at = 0.0

    def request_stop(_signum: int, _frame: object) -> None:
        nonlocal stopping, stop_at
        if not stopping:
            stopping = True
            stop_at = time.monotonic()
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass

    selector = selectors.DefaultSelector()
    try:
        termios.tcsetattr(0, termios.TCSANOW, raw)
        signal.signal(signal.SIGTERM, request_stop)
        signal.signal(signal.SIGHUP, request_stop)
        selector.register(0, selectors.EVENT_READ)
        selector.register(master, selectors.EVENT_READ)
        while True:
            if stopping and time.monotonic() - stop_at > 2:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            if child.poll() is not None:
                # Drain any output queued before the child closed its PTY.
                try:
                    data = os.read(master, 65536)
                    if data:
                        os.write(1, data)
                        continue
                except OSError:
                    pass
                break
            for key, _ in selector.select(timeout=0.1):
                if key.fd == master:
                    try:
                        data = os.read(master, 65536)
                        if data:
                            os.write(1, data)
                    except OSError as exc:
                        if exc.errno != errno.EIO:
                            raise
                elif key.fd == 0 and not stopping:
                    data = os.read(0, 4096)
                    for byte in data:
                        if byte == 3:
                            if ctrl_c or prior_ctrl_c:
                                stop_wrapper = True
                                request_stop(signal.SIGTERM, None)
                                break
                            ctrl_c = True
                        elif byte in (ord("q"), ord("Q")):
                            ctrl_c = False
                        try:
                            os.write(master, bytes((byte,)))
                        except OSError as exc:
                            if exc.errno != errno.EIO:
                                raise
    finally:
        selector.close()
        termios.tcsetattr(0, termios.TCSANOW, saved)
        os.close(master)
        if child.poll() is None:
            request_stop(signal.SIGTERM, None)
            try:
                child.wait(timeout=2)
            except subprocess.TimeoutExpired:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait()
        else:
            child.wait()

    if stop_wrapper:
        return 43
    if ctrl_c and not stopping:
        return 42
    return 0


if __name__ == "__main__":
    sys.exit(main())
