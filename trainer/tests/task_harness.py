"""任务线程测试的公共工具：主循环的替身（一直处于已注册状态）、假时钟、记录等待的 wait、运行 TaskWorker
的上下文管理器，以及代替子进程的脚本。test_tasks.py（模型转换）与 test_review_inference.py（审图识别）共用。"""

from __future__ import annotations

import os
import random
import sys
import textwrap
import time
from pathlib import Path
from typing import Any

from fake_platform import FakePlatform
from fakes import WINDOWS

from trainmaster_trainer.client import Client
from trainmaster_trainer.tasks import TaskWorker

TOKEN = "right-token-0123456789"
NODE_KEY = "node-key-1"


class FakeRegistration:
    """主循环的替身：一直处于已注册状态；registration_lost 时在假平台上重新注册（epoch 加一）。"""

    def __init__(self, platform: FakePlatform) -> None:
        self.platform = platform
        self.epoch = 1
        self.lost: list[int] = []
        platform.register({"node_key": NODE_KEY, "name": "gpu-01"})

    def wait_registered(self, timeout: float) -> int | None:
        return self.epoch

    def registration_lost(self, epoch: int) -> None:
        self.lost.append(epoch)
        if epoch == self.epoch:
            self.platform.register({"node_key": NODE_KEY, "name": "gpu-01"})
            self.epoch += 1


class FakeClock:
    def __init__(self) -> None:
        self.now = 1000.0

    def __call__(self) -> float:
        return self.now


class Waits:
    """TaskWorker 的 wait 替身：记录等待时间并推进假时钟，只真实等待一小会。"""

    def __init__(self, clock: FakeClock | None = None) -> None:
        self.clock = clock
        self.calls: list[float] = []

    def __call__(self, seconds: float) -> bool:
        self.calls.append(seconds)
        if self.clock is not None:
            self.clock.now += seconds
        time.sleep(0.005)
        return False


def script(tmp_path: Path, body: str, name: str = "child.py") -> list[str]:
    """把 body 写成脚本，返回运行它的命令（代替转换 / 识别子进程）。"""
    path = tmp_path / name
    path.write_text(textwrap.dedent(body), encoding="utf-8")
    return [sys.executable, str(path)]


def _alive(pid: int) -> bool:
    if WINDOWS:
        # Windows 上 os.kill(pid, 0) 是向控制台进程组发送 Ctrl+C（signal.CTRL_C_EVENT 为 0），可能中断测试进程
        # 本身，不能用来检查进程。用 OpenProcess 打开它：已经结束（只是句柄还没关闭）的进程处于有信号状态。
        import ctypes
        from ctypes import wintypes

        synchronize, wait_object_0 = 0x00100000, 0
        k32 = ctypes.WinDLL("kernel32", use_last_error=True)
        k32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        k32.OpenProcess.restype = wintypes.HANDLE
        k32.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        k32.WaitForSingleObject.restype = wintypes.DWORD
        k32.CloseHandle.argtypes = [wintypes.HANDLE]
        handle = k32.OpenProcess(synchronize, False, pid)
        if not handle:  # ERROR_INVALID_PARAMETER：没有这个进程
            return False
        try:
            return k32.WaitForSingleObject(handle, 0) != wait_object_0
        finally:
            k32.CloseHandle(handle)
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


def gone(pid: int, timeout: float = 5.0) -> bool:
    """进程 pid 已经结束（最多等待 timeout 秒：Windows 上结束 Job 中的进程是异步的，在 venv 中运行时
    脚本的 PID 是 venv 启动器的子进程，训练节点只等待了启动器）。"""
    deadline = time.monotonic() + timeout
    while _alive(pid):
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.05)
    return True


class Run:
    """在 with 块中运行一个任务线程（对假平台领取任务），退出时停止它并确认线程已结束。"""

    def __init__(self, tmp_path: Path, platform: FakePlatform, **kw: Any) -> None:
        self.platform = platform
        self.registration = FakeRegistration(platform)
        self.waits: Waits = kw.pop("waits", None) or Waits()
        self.state_dir = tmp_path / "state"
        kw.setdefault("export_timeout", 60)
        kw.setdefault("find_spec", lambda name: object())
        self.worker = TaskWorker(
            client=Client(platform.url, TOKEN),
            node_key=NODE_KEY,
            registration=self.registration,
            state_dir=self.state_dir,
            node_name="gpu-01",
            wait=self.waits,
            rng=random.Random(1),
            **kw,
        )

    def __enter__(self) -> Run:
        self.worker.start()
        return self

    def __exit__(self, *exc: Any) -> None:
        self.worker.stop()
        deadline = time.monotonic() + 15
        while self.worker.alive and time.monotonic() < deadline:
            # 正在进行的长轮询立即返回（停止前刚发出的领取请求可能还没到达服务端，所以重复几次）
            self.platform.release_claims()
            self.worker._thread.join(timeout=0.1)  # type: ignore[union-attr]
        assert not self.worker.alive
