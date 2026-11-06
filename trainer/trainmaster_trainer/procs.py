"""子进程（环境检测、模型转换、审图识别）的进程组：超时、取消或训练节点停止时，连同它启动的进程一起结束。

- Linux / macOS：子进程在新的会话（进程组）中运行（start_new_session）。结束时向整个进程组先发 SIGTERM，
  2 秒后仍未退出则 SIGKILL。
- Windows：没有进程组信号。子进程启动后加入一个 Job 对象（JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE）：结束时用
  TerminateJobObject 结束其中的全部进程（包括孙进程）；训练节点自己被结束（如停止计划任务、在任务管理器中
  结束进程）时 Job 的句柄随之关闭，系统同样结束这些进程，不会留下继续占用显卡的孤儿进程。Job 不可用时
  退回用 psutil 结束整个进程树。子进程不弹出控制台窗口（CREATE_NO_WINDOW），也不接收控制台的 Ctrl+C
  （CREATE_NEW_PROCESS_GROUP，与 Linux 的新会话相同：由训练节点负责结束它们）。

也负责把子进程的退出码翻译成说明：Linux 上被信号结束时为负数（-9 → SIGKILL）；Windows 上进程崩溃时退出码是
NTSTATUS（如 0xC0000005 访问冲突）。
"""

from __future__ import annotations

import contextlib
import logging
import os
import signal
import subprocess
import sys
import threading
import time
import weakref
from typing import Any

log = logging.getLogger(__name__)

WINDOWS = sys.platform == "win32"

# Windows 的进程创建标志（subprocess 模块只在 Windows 上定义这些常量）。
CREATE_NEW_PROCESS_GROUP = 0x00000200
CREATE_NO_WINDOW = 0x08000000

KILL_GRACE = 2.0
# Windows 上等待子进程时每次最多等这么久（秒）：Python 3.12 及更早版本在 Windows 上等待线程或锁时不能被
# Ctrl+C 打断，KeyboardInterrupt 只在两次等待之间送达（子进程在新的进程组中，收不到控制台的 Ctrl+C）。
WAIT_SLICE = 0.5 if WINDOWS else None


def popen_kwargs(windows: bool = WINDOWS) -> dict[str, Any]:
    """启动需要整组结束的子进程时传给 Popen 的参数。"""
    if windows:
        return {"creationflags": CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW}
    return {"start_new_session": True}


def quiet_kwargs(windows: bool = WINDOWS) -> dict[str, Any]:
    """运行短命令（nvidia-smi、powershell 查询）时传给 subprocess.run 的参数：Windows 上不弹出控制台窗口。"""
    return {"creationflags": CREATE_NO_WINDOW} if windows else {}


def communicate(
    proc: subprocess.Popen[Any], timeout: float | None, wait_slice: float | None = WAIT_SLICE
) -> tuple[Any, Any]:
    """proc.communicate(timeout=timeout)；wait_slice 不为 None 时分成不超过 wait_slice 秒的多次等待（见
    WAIT_SLICE），超时同样抛出 subprocess.TimeoutExpired。communicate 超时后可以再次调用，不会丢失输出。"""
    if wait_slice is None:
        return proc.communicate(timeout=timeout)
    deadline = None if timeout is None else time.monotonic() + timeout
    while True:
        left = None if deadline is None else deadline - time.monotonic()
        step = wait_slice if left is None else max(0.0, min(left, wait_slice))
        try:
            return proc.communicate(timeout=step)
        except subprocess.TimeoutExpired:
            if left is not None and left <= wait_slice:
                raise subprocess.TimeoutExpired(proc.args, timeout or 0.0) from None


# ---------------------------------------------------------------------------
# Windows：Job 对象


class _Job:
    """一个设置了 KILL_ON_JOB_CLOSE 的 Job 对象。close() 或被回收时关闭句柄，其中仍在运行的进程随之结束。"""

    def __init__(self, handle: int, kernel32: Any) -> None:
        self.handle = handle
        self._kernel32 = kernel32
        self._lock = threading.Lock()

    def terminate(self, exit_code: int = 1) -> bool:
        with self._lock:
            if not self.handle:
                return False
            return bool(self._kernel32.TerminateJobObject(self.handle, exit_code))

    def active_processes(self) -> int | None:
        """Job 中还没有退出的进程数；查询失败时为 None。"""
        import ctypes
        from ctypes import wintypes

        class Accounting(ctypes.Structure):  # JOBOBJECT_BASIC_ACCOUNTING_INFORMATION
            _fields_ = [
                ("TotalUserTime", ctypes.c_int64),
                ("TotalKernelTime", ctypes.c_int64),
                ("ThisPeriodTotalUserTime", ctypes.c_int64),
                ("ThisPeriodTotalKernelTime", ctypes.c_int64),
                ("TotalPageFaultCount", wintypes.DWORD),
                ("TotalProcesses", wintypes.DWORD),
                ("ActiveProcesses", wintypes.DWORD),
                ("TotalTerminatedProcesses", wintypes.DWORD),
            ]

        job_object_basic_accounting_information = 1
        info = Accounting()
        with self._lock:
            if not self.handle:
                return None
            ok = self._kernel32.QueryInformationJobObject(
                self.handle,
                job_object_basic_accounting_information,
                ctypes.byref(info),
                ctypes.sizeof(info),
                None,
            )
        return int(info.ActiveProcesses) if ok else None

    def wait_empty(self, timeout: float) -> bool:
        """等待 Job 中的进程全部退出（最多 timeout 秒）；返回是否已全部退出（无法查询时为 False）。"""
        deadline = time.monotonic() + timeout
        while True:
            active = self.active_processes()
            if active is None or active == 0:
                return active == 0
            if time.monotonic() >= deadline:
                return False
            time.sleep(0.05)

    def close(self) -> None:
        with self._lock:
            handle, self.handle = self.handle, 0
        if handle:
            self._kernel32.CloseHandle(handle)


_jobs: weakref.WeakKeyDictionary[subprocess.Popen[Any], _Job] = weakref.WeakKeyDictionary()
_kernel32: Any = None


def _load_kernel32() -> Any:
    """kernel32 中用到的函数（只在 Windows 上调用）。"""
    global _kernel32
    if _kernel32 is not None:
        return _kernel32
    import ctypes
    from ctypes import wintypes

    k32 = ctypes.WinDLL("kernel32", use_last_error=True)
    k32.CreateJobObjectW.argtypes = [wintypes.LPVOID, wintypes.LPCWSTR]
    k32.CreateJobObjectW.restype = wintypes.HANDLE
    k32.SetInformationJobObject.argtypes = [
        wintypes.HANDLE,
        ctypes.c_int,
        wintypes.LPVOID,
        wintypes.DWORD,
    ]
    k32.SetInformationJobObject.restype = wintypes.BOOL
    k32.AssignProcessToJobObject.argtypes = [wintypes.HANDLE, wintypes.HANDLE]
    k32.AssignProcessToJobObject.restype = wintypes.BOOL
    k32.TerminateJobObject.argtypes = [wintypes.HANDLE, wintypes.UINT]
    k32.TerminateJobObject.restype = wintypes.BOOL
    k32.QueryInformationJobObject.argtypes = [
        wintypes.HANDLE,
        ctypes.c_int,
        wintypes.LPVOID,
        wintypes.DWORD,
        wintypes.LPDWORD,
    ]
    k32.QueryInformationJobObject.restype = wintypes.BOOL
    k32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    k32.OpenProcess.restype = wintypes.HANDLE
    k32.CloseHandle.argtypes = [wintypes.HANDLE]
    k32.CloseHandle.restype = wintypes.BOOL
    _kernel32 = k32
    return k32


def _create_job() -> _Job | None:
    import ctypes
    from ctypes import wintypes

    class IoCounters(ctypes.Structure):
        _fields_ = [
            (name, ctypes.c_ulonglong)
            for name in (
                "ReadOperationCount",
                "WriteOperationCount",
                "OtherOperationCount",
                "ReadTransferCount",
                "WriteTransferCount",
                "OtherTransferCount",
            )
        ]

    class BasicLimit(ctypes.Structure):
        _fields_ = [
            ("PerProcessUserTimeLimit", ctypes.c_int64),
            ("PerJobUserTimeLimit", ctypes.c_int64),
            ("LimitFlags", wintypes.DWORD),
            ("MinimumWorkingSetSize", ctypes.c_size_t),
            ("MaximumWorkingSetSize", ctypes.c_size_t),
            ("ActiveProcessLimit", wintypes.DWORD),
            ("Affinity", ctypes.c_size_t),
            ("PriorityClass", wintypes.DWORD),
            ("SchedulingClass", wintypes.DWORD),
        ]

    class ExtendedLimit(ctypes.Structure):
        _fields_ = [
            ("BasicLimitInformation", BasicLimit),
            ("IoInfo", IoCounters),
            ("ProcessMemoryLimit", ctypes.c_size_t),
            ("JobMemoryLimit", ctypes.c_size_t),
            ("PeakProcessMemoryUsed", ctypes.c_size_t),
            ("PeakJobMemoryUsed", ctypes.c_size_t),
        ]

    job_object_extended_limit_information = 9
    job_object_limit_kill_on_job_close = 0x2000

    k32 = _load_kernel32()
    handle = k32.CreateJobObjectW(None, None)
    if not handle:
        log.debug("CreateJobObjectW failed: WinError %d", ctypes.get_last_error())
        return None
    info = ExtendedLimit()
    info.BasicLimitInformation.LimitFlags = job_object_limit_kill_on_job_close
    if not k32.SetInformationJobObject(
        handle, job_object_extended_limit_information, ctypes.byref(info), ctypes.sizeof(info)
    ):
        log.debug("SetInformationJobObject failed: WinError %d", ctypes.get_last_error())
        k32.CloseHandle(handle)
        return None
    return _Job(handle, k32)


def _assign(job: _Job, proc: subprocess.Popen[Any]) -> bool:
    import ctypes

    k32 = _load_kernel32()
    # Popen 在 Windows 上保存了子进程的句柄（PROCESS_ALL_ACCESS）；没有时按 PID 打开。
    handle = getattr(proc, "_handle", None)
    opened = None
    if not handle:
        process_set_quota, process_terminate = 0x0100, 0x0001
        opened = k32.OpenProcess(process_set_quota | process_terminate, False, proc.pid)
        handle = opened
    try:
        if not handle or not k32.AssignProcessToJobObject(job.handle, int(handle)):
            log.debug("AssignProcessToJobObject failed: WinError %d", ctypes.get_last_error())
            return False
        return True
    finally:
        if opened:
            k32.CloseHandle(opened)


def attach(proc: subprocess.Popen[Any], windows: bool = WINDOWS) -> None:
    """子进程启动后立即调用：Windows 上把它加入一个新的 Job 对象；其它系统什么也不做（已在新会话中）。

    失败（如所在的 Job 不允许嵌套）时只记录 DEBUG 日志，kill() 退回用 psutil 结束进程树。
    """
    if not windows:
        return
    try:
        job = _create_job()
        if job is None:
            return
        if not _assign(job, proc):
            job.close()
            return
    except Exception as exc:  # ctypes 不可用等：退回 psutil
        log.debug("cannot put the child process into a job object: %s", exc)
        return
    _jobs[proc] = job
    weakref.finalize(proc, job.close)


def kill(proc: subprocess.Popen[Any], grace: float = KILL_GRACE, windows: bool = WINDOWS) -> None:
    """结束子进程及它启动的全部进程。Linux：进程组先 SIGTERM，grace 秒后 SIGKILL；Windows：立即结束。"""
    if windows:
        _kill_windows(proc, grace)
        return
    if proc.poll() is not None:
        return
    _signal_group(proc, signal.SIGTERM)
    try:
        proc.wait(timeout=grace)
    except subprocess.TimeoutExpired:
        _signal_group(proc, getattr(signal, "SIGKILL", signal.SIGTERM))
        with contextlib.suppress(subprocess.TimeoutExpired):
            proc.wait(timeout=grace)


def _signal_group(proc: subprocess.Popen[Any], sig: int) -> None:
    try:
        os.killpg(proc.pid, sig)  # type: ignore[attr-defined]  # 只在 POSIX 上调用
    except ProcessLookupError:
        pass
    except PermissionError:  # 不应发生（子进程是本进程启动的），退回只结束子进程本身
        with contextlib.suppress(ProcessLookupError):
            proc.send_signal(sig)


def _kill_windows(proc: subprocess.Popen[Any], grace: float) -> None:
    """Windows：结束 Job 中的全部进程（子进程已经退出时也结束，孙进程可能还占着管道）；没有 Job 时结束进程树。"""
    job = _jobs.pop(proc, None)
    if job is not None:
        try:
            terminated = job.terminate()
            if terminated:
                # TerminateJobObject 不等进程退出。从虚拟环境运行时 python.exe 只是启动器，真正的解释器是
                # 它的子进程：只等 proc 的话，解释器可能还占着工作目录中的文件（之后无法删除）与显存。
                job.wait_empty(grace)
        finally:
            job.close()
        if not terminated:
            _kill_tree(proc)
    else:
        _kill_tree(proc)
    with contextlib.suppress(subprocess.TimeoutExpired):
        proc.wait(timeout=grace)


def _kill_tree(proc: subprocess.Popen[Any]) -> None:
    """没有 Job 对象时：用 psutil 列出子进程的后代并全部结束（先结束后代，再结束子进程本身）。"""
    try:
        import psutil

        try:
            children = psutil.Process(proc.pid).children(recursive=True)
        except psutil.Error:
            children = []
        for child in children:
            with contextlib.suppress(psutil.Error):
                child.kill()
    except ImportError:
        pass
    if proc.poll() is None:
        with contextlib.suppress(OSError):
            proc.kill()


# ---------------------------------------------------------------------------
# 退出码


def signal_name(returncode: int | None) -> str | None:
    """Linux / macOS 上被信号结束时（退出码为负数）的信号名，如 SIGKILL；否则为 None。"""
    if returncode is None or returncode >= 0:
        return None
    try:
        return signal.Signals(-returncode).name
    except ValueError:
        return str(-returncode)


def killed_by_sigkill(returncode: int | None) -> bool:
    sigkill = getattr(signal, "SIGKILL", None)
    return returncode is not None and sigkill is not None and returncode == -sigkill


# 常见的 NTSTATUS（Windows 上进程崩溃时的退出码）。
_NTSTATUS = {
    0xC0000005: "访问冲突",
    0xC000001D: "非法指令，CPU 可能不支持程序使用的指令集",
    0xC0000017: "内存不足",
    0xC000012D: "虚拟内存不足，Windows 的页面文件可能太小",
    0xC00000FD: "栈溢出",
    0xC0000094: "整数除以零",
    0xC0000135: "找不到程序依赖的 DLL",
    0xC0000139: "DLL 中找不到入口点，DLL 的版本可能不匹配",
    0xC0000142: "DLL 初始化失败",
    0xC000013A: "被 Ctrl+C 或关闭控制台窗口终止",
    0xC0000374: "堆损坏",
    0xC0000409: "程序检测到严重错误后立即退出",
    0xC0000420: "断言失败",
}


def windows_crash(returncode: int | None) -> str | None:
    """Windows 上退出码是 NTSTATUS 错误（0xC0000000 及以上）时的说明，如 "0xC0000005，访问冲突"；
    普通的退出码（sys.exit(3) 等）返回 None。"""
    if returncode is None:
        return None
    code = returncode & 0xFFFFFFFF
    if code < 0xC0000000:
        return None
    text = _NTSTATUS.get(code)
    return f"0x{code:08X}，{text}" if text else f"0x{code:08X}"


def out_of_memory_crash(returncode: int | None) -> bool:
    """Windows 上因内存不足而崩溃的退出码。"""
    return returncode is not None and (returncode & 0xFFFFFFFF) in (0xC0000017, 0xC000012D)
