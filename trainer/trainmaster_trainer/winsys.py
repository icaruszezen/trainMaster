r"""Windows 上的系统信息：系统名称与版本、CPU 型号（注册表），以及 PCI 总线上的 NVIDIA 显卡数（CIM）。

只在 Windows 上调用。读取注册表与运行 PowerShell 的函数都可以替换（参数 read / runner），在 Linux 上也能测试。

- 系统名称：HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion 的 ProductName 与 DisplayVersion（如 23H2）。
  Windows 11 的 ProductName 仍是 “Windows 10 …”（微软为兼容保留），按版本号（22000 起为 Windows 11）改正。
- 系统版本：CurrentMajorVersionNumber.CurrentMinorVersionNumber.CurrentBuildNumber.UBR（如 10.0.22631.4317）。
- CPU 型号：HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0 的 ProcessorNameString。
- NVIDIA 显卡数：驱动没有安装时 NVML 与 nvidia-smi 都看不到显卡，用 Win32_PnPEntity 中 PCI 厂商 10DE、
  类别 0300（VGA）或 0302（3D 控制器）的设备数提示“有显卡但没有驱动”（与 Linux 读取 /sys/bus/pci 相同的用途）。
"""

from __future__ import annotations

import base64
import logging
import os
import platform
import re
import subprocess
from collections.abc import Callable
from pathlib import Path
from typing import Any

from . import procs

log = logging.getLogger(__name__)

CURRENT_VERSION_KEY = r"SOFTWARE\Microsoft\Windows NT\CurrentVersion"
CPU_KEY = r"HARDWARE\DESCRIPTION\System\CentralProcessor\0"
WINDOWS_11_BUILD = 22000
PNP_TIMEOUT = 60.0

Reader = Callable[[str, str], Any]
Runner = Callable[..., subprocess.CompletedProcess]

# 统计 PCI 总线上的 NVIDIA 显卡（没有驱动时显示为“Microsoft 基本显示适配器”或“其它设备”）。只列出当前存在的设备。
# WQL 字符串中的反斜杠写成 \\，正则中的反斜杠同样转义。用 -EncodedCommand 传给 PowerShell，不经过命令行的引号规则。
PCI_GPU_SCRIPT = (
    r"""@(Get-CimInstance -ClassName Win32_PnPEntity -Filter "PNPDeviceID LIKE 'PCI\\VEN_10DE%'" """
    r"""-ErrorAction Stop | Where-Object { @($_.CompatibleID) -match '^PCI\\CC_030[02]' }).Count"""
)


def read_hklm(key: str, name: str) -> Any:
    """读取 HKEY_LOCAL_MACHINE 下的一个值（64 位视图）；不存在或无法读取时为 None。"""
    import winreg  # 只在 Windows 上有

    try:
        with winreg.OpenKey(
            winreg.HKEY_LOCAL_MACHINE, key, 0, winreg.KEY_READ | winreg.KEY_WOW64_64KEY
        ) as handle:
            value, _kind = winreg.QueryValueEx(handle, name)
    except OSError:
        return None
    return value


def _text(value: Any) -> str:
    return re.sub(r"\s+", " ", value).strip() if isinstance(value, str) else ""


def _int(value: Any) -> int | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, str) and value.strip().isdigit():
        return int(value.strip())
    return None


def os_name(read: Reader = read_hklm) -> str | None:
    """如 "Windows 11 Pro 23H2"、"Windows Server 2022 Datacenter 21H2"；读不到时为 None。"""
    product = _text(read(CURRENT_VERSION_KEY, "ProductName"))
    if not product:
        return None
    build = _int(read(CURRENT_VERSION_KEY, "CurrentBuildNumber"))
    if build is None:
        build = _int(read(CURRENT_VERSION_KEY, "CurrentBuild"))
    if build is not None and build >= WINDOWS_11_BUILD and product.startswith("Windows 10"):
        product = "Windows 11" + product[len("Windows 10") :]
    release = _text(read(CURRENT_VERSION_KEY, "DisplayVersion")) or _text(
        read(CURRENT_VERSION_KEY, "ReleaseId")
    )
    if release and release not in product:
        product = f"{product} {release}"
    return product


def os_version(read: Reader = read_hklm) -> str | None:
    """如 "10.0.22631.4317"；注册表中没有时用 platform.version()（如 "10.0.22631"）。"""
    major = _int(read(CURRENT_VERSION_KEY, "CurrentMajorVersionNumber"))
    minor = _int(read(CURRENT_VERSION_KEY, "CurrentMinorVersionNumber"))
    build = _int(read(CURRENT_VERSION_KEY, "CurrentBuildNumber"))
    if major is not None and minor is not None and build is not None:
        ubr = _int(read(CURRENT_VERSION_KEY, "UBR"))
        version = f"{major}.{minor}.{build}"
        return f"{version}.{ubr}" if ubr is not None else version
    return platform.version() or None


def cpu_model(read: Reader = read_hklm) -> str | None:
    """如 "Intel(R) Core(TM) i9-14900K"、"AMD Ryzen 9 7950X 16-Core Processor"。"""
    name = _text(read(CPU_KEY, "ProcessorNameString"))
    if name:
        return name
    return _text(platform.processor()) or None


def cpu_mhz(read: Reader = read_hklm) -> float | None:
    """注册表中 CPU 的标称频率（MHz）；psutil 读不到频率时使用。"""
    mhz = _int(read(CPU_KEY, "~MHz"))
    return float(mhz) if mhz and mhz > 0 else None


def powershell() -> str:
    """Windows PowerShell 5.1 的完整路径（不依赖 PATH）；找不到时为 "powershell"。"""
    root = os.environ.get("SYSTEMROOT") or r"C:\Windows"  # Windows 的环境变量名不区分大小写
    path = Path(root) / "System32" / "WindowsPowerShell" / "v1.0" / "powershell.exe"
    return str(path) if path.is_file() else "powershell"


def pci_gpu_count(runner: Runner = subprocess.run, timeout: float = PNP_TIMEOUT) -> int | None:
    """PCI 总线上的 NVIDIA 显卡数（不需要驱动）；查询失败时为 None。"""
    try:
        proc = runner(
            [
                powershell(),
                "-NoProfile",
                "-NonInteractive",
                "-EncodedCommand",
                base64.b64encode(PCI_GPU_SCRIPT.encode("utf-16-le")).decode("ascii"),
            ],
            capture_output=True,
            text=True,
            timeout=timeout,
            check=False,
            **procs.quiet_kwargs(),
        )
    except (OSError, subprocess.SubprocessError) as exc:
        log.debug("cannot count NVIDIA PCI devices: %s", exc)
        return None
    if proc.returncode != 0:
        log.debug("counting NVIDIA PCI devices failed: %s", (proc.stderr or "").strip()[:300])
        return None
    lines = [line.strip() for line in (proc.stdout or "").splitlines() if line.strip()]
    if not lines or not lines[-1].isdigit():
        return None
    return int(lines[-1])
