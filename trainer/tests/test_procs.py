"""procs.py：子进程的进程组 / Job 对象、结束整个进程树，以及退出码的说明。

Windows 的 Job 对象只能在 Windows 上测试（windows_only）；其余逻辑（参数、退回用 psutil 结束进程树、
退出码翻译）在任何系统上都测试。
"""

from __future__ import annotations

import contextlib
import signal
import subprocess
import sys
import textwrap
import time
from pathlib import Path

import psutil
import pytest
from fakes import WINDOWS, posix_only, windows_only

from trainmaster_trainer import procs

# 子进程启动一个孙进程（睡眠 60 秒），打印孙进程的 PID 后自己也睡眠。
_PARENT = """
import subprocess, sys, time
child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
print(child.pid, flush=True)
time.sleep(60)
"""


def _spawn_tree(tmp_path: Path, windows: bool) -> tuple[subprocess.Popen[str], int]:
    script = tmp_path / "parent.py"
    script.write_text(textwrap.dedent(_PARENT), encoding="utf-8")
    proc = subprocess.Popen(
        [sys.executable, str(script)],
        stdout=subprocess.PIPE,
        text=True,
        **procs.popen_kwargs(windows),
    )
    procs.attach(proc, windows)
    assert proc.stdout is not None
    grandchild = int(proc.stdout.readline().strip())
    return proc, grandchild


def _gone(pid: int, timeout: float = 10.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if psutil.Process(pid).status() == psutil.STATUS_ZOMBIE:
                return True
        except psutil.NoSuchProcess:
            return True
        time.sleep(0.05)
    return False


def _cleanup(*pids: int) -> None:
    for pid in pids:
        with contextlib.suppress(psutil.Error):
            psutil.Process(pid).kill()


# ---- Popen 参数 ----


def test_popen_kwargs():
    assert procs.popen_kwargs(windows=False) == {"start_new_session": True}
    flags = procs.popen_kwargs(windows=True)["creationflags"]
    assert flags & procs.CREATE_NEW_PROCESS_GROUP and flags & procs.CREATE_NO_WINDOW


def test_quiet_kwargs():
    assert procs.quiet_kwargs(windows=False) == {}
    assert procs.quiet_kwargs(windows=True) == {"creationflags": procs.CREATE_NO_WINDOW}


# ---- 结束整个进程树 ----


@posix_only
def test_kill_process_group(tmp_path: Path):
    proc, grandchild = _spawn_tree(tmp_path, windows=False)
    try:
        procs.kill(proc, grace=2.0, windows=False)
        assert proc.poll() is not None
        assert _gone(grandchild)
    finally:
        _cleanup(proc.pid, grandchild)


@posix_only
def test_kill_escalates_to_sigkill(tmp_path: Path):
    script = tmp_path / "stubborn.py"
    script.write_text(
        "import signal, time\nsignal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
        "print('ready', flush=True)\ntime.sleep(60)\n"
    )
    proc = subprocess.Popen(
        [sys.executable, str(script)], stdout=subprocess.PIPE, text=True, start_new_session=True
    )
    assert proc.stdout is not None and proc.stdout.readline().strip() == "ready"
    started = time.monotonic()
    procs.kill(proc, grace=0.5, windows=False)
    assert procs.killed_by_sigkill(proc.returncode)
    assert time.monotonic() - started < 10


def test_kill_tree_fallback_without_job(tmp_path: Path):
    # 没有 Job 对象（不是 Windows，或 Job 创建失败）时用 psutil 结束进程树：在任何系统上都能运行。
    proc, grandchild = _spawn_tree(tmp_path, windows=WINDOWS)
    procs._jobs.pop(proc, None)
    try:
        procs.kill(proc, grace=5.0, windows=True)
        assert proc.poll() is not None
        assert _gone(grandchild)
    finally:
        _cleanup(proc.pid, grandchild)


def test_kill_already_exited_process():
    proc = subprocess.Popen([sys.executable, "-c", "pass"], **procs.popen_kwargs())
    procs.attach(proc)
    proc.wait(timeout=30)
    procs.kill(proc)  # 不报错
    assert proc.returncode == 0


@posix_only
def test_attach_without_kernel32_falls_back():
    # 在非 Windows 上按 Windows 处理：没有 kernel32，attach 只记录日志，kill 退回 psutil。
    proc = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
    try:
        procs.attach(proc, windows=True)
        assert proc not in procs._jobs
        procs.kill(proc, grace=5.0, windows=True)
        assert proc.poll() is not None
    finally:
        _cleanup(proc.pid)


class FakeJob:
    def __init__(self, terminated: bool) -> None:
        self.terminated = terminated
        self.calls: list[str] = []

    def terminate(self, exit_code: int = 1) -> bool:
        self.calls.append("terminate")
        return self.terminated

    def wait_empty(self, timeout: float) -> bool:
        self.calls.append("wait")
        return True

    def close(self) -> None:
        self.calls.append("close")


@pytest.mark.parametrize("terminated", [True, False])
def test_kill_windows_uses_job_then_tree(monkeypatch, terminated):
    proc = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
    job = FakeJob(terminated)
    trees: list[int] = []
    monkeypatch.setattr(procs, "_kill_tree", lambda p: trees.append(p.pid) or p.kill())
    procs._jobs[proc] = job  # type: ignore[assignment]
    try:
        if terminated:
            proc.kill()  # 假的 Job 不会真的结束进程
        procs.kill(proc, grace=5.0, windows=True)
        # 结束后等 Job 中的进程（包括虚拟环境启动器启动的解释器）全部退出
        assert job.calls == (
            ["terminate", "wait", "close"] if terminated else ["terminate", "close"]
        )
        assert trees == ([] if terminated else [proc.pid])
        assert proc not in procs._jobs
    finally:
        _cleanup(proc.pid)


@windows_only
def test_job_object_kills_grandchildren(tmp_path: Path):
    proc, grandchild = _spawn_tree(tmp_path, windows=True)
    try:
        assert proc in procs._jobs
        procs.kill(proc, grace=5.0, windows=True)
        assert proc.poll() is not None
        assert _gone(grandchild)
    finally:
        _cleanup(proc.pid, grandchild)


@windows_only
def test_job_object_closing_kills_the_tree(tmp_path: Path):
    # 训练节点自己被结束时 Job 的句柄随之关闭：这里直接关闭句柄模拟。
    proc, grandchild = _spawn_tree(tmp_path, windows=True)
    try:
        procs._jobs.pop(proc).close()
        assert _gone(proc.pid)
        assert _gone(grandchild)
    finally:
        _cleanup(proc.pid, grandchild)


@windows_only
def test_windows_child_has_no_console_window(tmp_path: Path):
    proc = subprocess.Popen(
        [sys.executable, "-c", "import ctypes; print(ctypes.windll.kernel32.GetConsoleWindow())"],
        stdout=subprocess.PIPE,
        text=True,
        **procs.popen_kwargs(),
    )
    out, _ = proc.communicate(timeout=60)
    assert out.strip() == "0"


# ---- 退出码 ----


def test_signal_name():
    assert procs.signal_name(None) is None
    assert procs.signal_name(0) is None
    assert procs.signal_name(3) is None
    assert procs.signal_name(-signal.SIGTERM) == "SIGTERM"
    assert procs.signal_name(-250) == "250"


@posix_only
def test_killed_by_sigkill():
    assert procs.killed_by_sigkill(-9)
    assert not procs.killed_by_sigkill(-15)
    assert not procs.killed_by_sigkill(None)
    assert not procs.killed_by_sigkill(9)


@pytest.mark.parametrize(
    ("code", "expected"),
    [
        (None, None),
        (0, None),
        (1, None),
        (255, None),
        (0xC0000005, "0xC0000005，访问冲突"),
        (-1073741819, "0xC0000005，访问冲突"),  # 有符号形式
        (0xC000001D, "0xC000001D，非法指令，CPU 可能不支持程序使用的指令集"),
        (0xC0000409, "0xC0000409，程序检测到严重错误后立即退出"),
        (0xC0001234, "0xC0001234"),
    ],
)
def test_windows_crash(code, expected):
    assert procs.windows_crash(code) == expected


def test_out_of_memory_crash():
    assert procs.out_of_memory_crash(0xC0000017)
    assert procs.out_of_memory_crash(0xC000012D)
    assert not procs.out_of_memory_crash(0xC0000005)
    assert not procs.out_of_memory_crash(None)


@windows_only
def test_windows_crash_exit_code_from_real_process():
    # os._exit 的参数是 C 的 int：0xC0000005 超出范围（OverflowError，退出码为 1），用有符号形式，
    # 系统按 32 位无符号数作为进程的退出码。
    proc = subprocess.run(
        [sys.executable, "-c", "import os; os._exit(-1073741819)"], timeout=60, check=False
    )
    assert proc.returncode & 0xFFFFFFFF == 0xC0000005
    assert procs.windows_crash(proc.returncode) == "0xC0000005，访问冲突"
