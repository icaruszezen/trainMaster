"""本机硬件（Hardware）与实时指标（Metrics）：CPU、内存、cgroup 限制、/dev/shm、磁盘，显卡委托 gpu.py。

Linux 上所有文件读取都基于可替换的根目录 root（默认 /），测试用临时目录模拟 /proc、/sys、/etc。
Windows 上系统名称、版本与 CPU 型号读取注册表（winsys.py，读取函数可替换），没有 cgroup 与 /dev/shm（为 null）。
"""

from __future__ import annotations

import logging
import os
import platform
import re
import shutil
import sys
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import psutil

from . import gpu as gpu_mod
from . import winsys

log = logging.getLogger(__name__)

WINDOWS = sys.platform == "win32"

# cgroup v1 用一个接近 int64 上限的数表示“不限制”；不小于物理内存的值一律视为不限制。
_CGROUP_V1_UNLIMITED = 1 << 62


def _read(path: Path) -> str | None:
    try:
        return path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None


# ---------------------------------------------------------------------------
# 系统信息


def _unquote_os_release(value: str) -> str:
    """os-release 的值可以用单引号或双引号括起来，双引号中可以有反斜杠转义。"""
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        quote = value[0]
        value = value[1:-1]
        if quote == '"':
            value = re.sub(r'\\([\\$"`])', r"\1", value)
    return value


def parse_os_release(text: str) -> dict[str, str]:
    out: dict[str, str] = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        out[key.strip()] = _unquote_os_release(value)
    return out


def os_pretty_name(root: Path) -> str | None:
    """/etc/os-release 的 PRETTY_NAME（容器中是镜像的系统）；没有时用 NAME + VERSION。"""
    for rel in ("etc/os-release", "usr/lib/os-release"):
        text = _read(root / rel)
        if text is None:
            continue
        info = parse_os_release(text)
        pretty = info.get("PRETTY_NAME", "").strip()
        if pretty:
            return pretty
        name = " ".join(v for v in (info.get("NAME"), info.get("VERSION")) if v).strip()
        if name:
            return name
    return None


def in_container(root: Path) -> bool:
    if (root / ".dockerenv").exists() or (root / "run" / ".containerenv").exists():
        return True
    cgroup = _read(root / "proc" / "1" / "cgroup") or ""
    return bool(re.search(r"/(docker|kubepods|containerd|libpod|lxc)[/-]", cgroup))


# ARM 的 /proc/cpuinfo 只有实现者与型号编号（与 lscpu 的对照表一致，只列常见的服务器 / Jetson 核心）。
_ARM_PARTS = {
    "0xd03": "Cortex-A53",
    "0xd07": "Cortex-A57",
    "0xd08": "Cortex-A72",
    "0xd0b": "Cortex-A76",
    "0xd0c": "Neoverse-N1",
    "0xd40": "Neoverse-V1",
    "0xd41": "Cortex-A78",
    "0xd42": "Cortex-A78AE",
    "0xd49": "Neoverse-N2",
    "0xd4f": "Neoverse-V2",
}


def _cpuinfo_field(text: str, key: str) -> str | None:
    m = re.search(rf"^{re.escape(key)}\s*:\s*(.+?)\s*$", text, re.MULTILINE)
    return re.sub(r"\s+", " ", m.group(1)) if m else None


def cpu_model(root: Path) -> str | None:
    """x86 的 model name；ARM 的 /proc/cpuinfo 通常没有型号，依次退回设备树中的机器型号（如 Jetson）
    与按 CPU part 编号得到的核心名称（如 Grace 的 Neoverse-V2）。"""
    text = _read(root / "proc" / "cpuinfo") or ""
    for key in ("model name", "Model", "Hardware", "Processor", "cpu model"):
        value = _cpuinfo_field(text, key)
        if value:
            return value
    for rel in ("proc/device-tree/model", "sys/firmware/devicetree/base/model"):
        model = (_read(root / rel) or "").replace("\x00", "").strip()
        if model:
            return model
    if (_cpuinfo_field(text, "CPU implementer") or "").lower() == "0x41":
        part = _ARM_PARTS.get((_cpuinfo_field(text, "CPU part") or "").lower())
        if part:
            return f"ARM {part}"
    return None


def cpu_max_mhz(
    root: Path, *, windows: bool = False, read: winsys.Reader | None = None
) -> float | None:
    try:
        freq = psutil.cpu_freq()
    except Exception as exc:  # psutil 在部分虚拟机 / 容器中读取频率会出错
        log.debug("psutil.cpu_freq failed: %s", exc)
        freq = None
    if freq is not None and freq.max and freq.max > 0:
        return round(float(freq.max), 1)
    if windows:
        return winsys.cpu_mhz(read or winsys.read_hklm)
    raw = _read(
        root / "sys" / "devices" / "system" / "cpu" / "cpu0" / "cpufreq" / "cpuinfo_max_freq"
    )
    try:
        khz = int((raw or "").strip())
    except ValueError:
        return None
    return round(khz / 1000.0, 1) if khz > 0 else None


# ---------------------------------------------------------------------------
# cgroup


def _cgroup_v2_dirs(root: Path) -> list[Path]:
    """本进程所在 cgroup 及其各级父目录（从叶子到根）中存在的目录。

    容器有自己的 cgroup 命名空间时 /proc/self/cgroup 为 "0::/"，即 /sys/fs/cgroup 本身；
    裸机上 systemd 服务的限制在 /sys/fs/cgroup/system.slice/xxx.service 中。
    """
    base = root / "sys" / "fs" / "cgroup"
    rel = "/"
    for line in (_read(root / "proc" / "self" / "cgroup") or "").splitlines():
        if line.startswith("0::"):
            rel = line[3:].strip() or "/"
            break
    parts = [p for p in rel.split("/") if p and p not in (".", "..")]
    dirs: list[Path] = []
    for i in range(len(parts), -1, -1):
        d = base.joinpath(*parts[:i])
        if d.is_dir():
            dirs.append(d)
    return dirs


def _v2_cpu_quota(d: Path) -> float | None:
    text = _read(d / "cpu.max")
    if not text:
        return None
    fields = text.split()
    if len(fields) < 1 or fields[0] == "max":
        return None
    try:
        quota = int(fields[0])
        period = int(fields[1]) if len(fields) > 1 else 100000
    except ValueError:
        return None
    if quota <= 0 or period <= 0:
        return None
    return quota / period


def _v2_memory_max(d: Path) -> int | None:
    text = (_read(d / "memory.max") or "").strip()
    if not text or text == "max":
        return None
    try:
        value = int(text)
    except ValueError:
        return None
    return value if value > 0 else None


def _v1_dir(root: Path, controller: str) -> Path | None:
    base = root / "sys" / "fs" / "cgroup"
    for name in (controller, f"{controller},cpuacct", f"cpuacct,{controller}"):
        d = base / name
        if d.is_dir():
            return d
    return None


def cgroup_limits(root: Path) -> tuple[float | None, int | None]:
    """cgroup 的 CPU 配额（折算为核数）与内存上限；没有限制时为 None。cgroup v2 优先，其次 v1。"""
    base = root / "sys" / "fs" / "cgroup"
    if (base / "cgroup.controllers").exists():
        cpu: float | None = None
        mem: int | None = None
        for d in _cgroup_v2_dirs(root):
            q = _v2_cpu_quota(d)
            if q is not None:
                cpu = q if cpu is None else min(cpu, q)
            m = _v2_memory_max(d)
            if m is not None:
                mem = m if mem is None else min(mem, m)
        return cpu, mem

    cpu = None
    cpu_dir = _v1_dir(root, "cpu")
    if cpu_dir is not None:
        try:
            quota = int((_read(cpu_dir / "cpu.cfs_quota_us") or "").strip())
            period = int((_read(cpu_dir / "cpu.cfs_period_us") or "").strip())
        except ValueError:
            quota = period = -1
        if quota > 0 and period > 0:
            cpu = quota / period
    mem = None
    mem_dir = _v1_dir(root, "memory")
    if mem_dir is not None:
        try:
            value = int((_read(mem_dir / "memory.limit_in_bytes") or "").strip())
        except ValueError:
            value = -1
        if 0 < value < _CGROUP_V1_UNLIMITED:
            mem = value
    return cpu, mem


def shm_bytes(root: Path) -> int | None:
    statvfs = getattr(os, "statvfs", None)  # Windows 没有 statvfs，也没有 /dev/shm
    if statvfs is None:
        return None
    try:
        st = statvfs(root / "dev" / "shm")
    except OSError:
        return None
    size = st.f_blocks * st.f_frsize
    return size if size > 0 else None


# ---------------------------------------------------------------------------


@dataclass
class HardwareProbe:
    """一次探测的结果：hardware 为数据契约中的 Hardware；gpu 保留实时指标的 sampler；
    device_nodes（/dev/nvidia*）、pci_gpu_count 与 host_driver_version（/proc/driver/nvidia/version
    中宿主机的驱动版本）只用于诊断（issues），不上报。"""

    hardware: dict[str, Any]
    gpu: gpu_mod.GpuProbe
    device_nodes: list[str]
    pci_gpu_count: int | None = None
    host_driver_version: str | None = None


def platform_name(windows: bool = WINDOWS) -> str | None:
    """Hardware.platform：windows、linux（其它系统为 platform.system() 的小写，如 darwin）。"""
    if windows:
        return "windows"
    return platform.system().lower() or None


def probe(
    root: str | Path = "/",
    *,
    gpu_probe: Callable[[], gpu_mod.GpuProbe] | None = None,
    windows: bool = WINDOWS,
    read_registry: winsys.Reader | None = None,
    windows_pci_gpu_count: Callable[[], int | None] | None = None,
) -> HardwareProbe:
    """探测本机硬件。windows：按 Windows 探测（默认取当前系统）；read_registry 与 windows_pci_gpu_count
    替换读取注册表与统计 PCI 显卡的函数（测试用）。"""
    root = Path(root)
    gp = gpu_probe() if gpu_probe is not None else gpu_mod.probe(root=root, windows=windows)

    logical = psutil.cpu_count(logical=True) or os.cpu_count()
    try:
        physical = psutil.cpu_count(logical=False)
    except Exception as exc:
        log.debug("psutil physical core count failed: %s", exc)
        physical = None
    total_mem: int | None
    try:
        total_mem = int(psutil.virtual_memory().total)
    except Exception as exc:
        log.debug("psutil.virtual_memory failed: %s", exc)
        total_mem = None

    if windows:
        read = read_registry or winsys.read_hklm
        limit_cores = limit_mem = None
        os_text, kernel = winsys.os_name(read), winsys.os_version(read)
        model, container, shm = winsys.cpu_model(read), False, None
        max_mhz = cpu_max_mhz(root, windows=True, read=read)
    else:
        limit_cores, limit_mem = cgroup_limits(root)
        if limit_cores is not None and logical and limit_cores >= logical:
            limit_cores = None
        if limit_mem is not None and total_mem and limit_mem >= total_mem:
            limit_mem = None
        os_text, kernel = os_pretty_name(root), platform.release() or None
        model, container, shm = cpu_model(root), in_container(root), shm_bytes(root)
        max_mhz = cpu_max_mhz(root)

    hardware = {
        "os": os_text,
        "kernel": kernel,
        "arch": platform.machine() or None,
        "platform": platform_name(windows),
        "in_container": container,
        "cpu": {
            "model": model,
            "physical_cores": physical or None,
            "logical_cores": logical or None,
            "max_mhz": max_mhz,
            "limit_cores": None if limit_cores is None else round(limit_cores, 2),
        },
        "memory": {"total_bytes": total_mem, "limit_bytes": limit_mem},
        "shm_bytes": shm,
        "gpu_driver": gp.driver,
        "gpus": gp.gpus,
        "gpu_detection": gp.detection,
    }
    if windows:
        # 驱动没有安装时 NVML 与 nvidia-smi 都看不到显卡：用 PCI 设备提示“有显卡但没有驱动”。
        # Windows 上没有 /dev/nvidia* 与 /proc/driver/nvidia（只有容器中才需要它们诊断）。
        counter = windows_pci_gpu_count or winsys.pci_gpu_count
        return HardwareProbe(
            hardware=hardware,
            gpu=gp,
            device_nodes=[],
            pci_gpu_count=None if gp.gpus else counter(),
            host_driver_version=None,
        )
    return HardwareProbe(
        hardware=hardware,
        gpu=gp,
        device_nodes=gpu_mod.nvidia_device_nodes(root),
        pci_gpu_count=None if gp.gpus else gpu_mod.pci_gpu_count(root),
        host_driver_version=None if gp.gpus else gpu_mod.host_driver_version(root),
    )


class MetricsCollector:
    """采集 Metrics。cpu_percent 是两次调用之间的平均利用率，所以第一次采集为 null（与数据契约一致）。"""

    def __init__(self, data_dir: str | Path, sampler: gpu_mod.GpuSampler | None = None) -> None:
        self.data_dir = Path(data_dir)
        self.sampler: gpu_mod.GpuSampler = sampler or gpu_mod.NullSampler()
        self._primed = False

    def prime(self) -> None:
        """提前开始计算 CPU 利用率，之后的第一次 collect 就能得到数值。"""
        psutil.cpu_percent(interval=None)
        self._primed = True

    def collect(self) -> dict[str, Any]:
        if self._primed:
            cpu_percent: float | None = round(float(psutil.cpu_percent(interval=None)), 1)
        else:
            self.prime()
            cpu_percent = None

        try:
            load = [round(x, 2) for x in os.getloadavg()]
        except (OSError, AttributeError):
            load = None

        used = total = None
        try:
            vm = psutil.virtual_memory()
            total = int(vm.total)
            used = int(vm.total - vm.available)
        except Exception as exc:
            log.debug("psutil.virtual_memory failed: %s", exc)

        disk_total = disk_free = None
        try:
            usage = shutil.disk_usage(self.data_dir)
            disk_total, disk_free = int(usage.total), int(usage.free)
        except OSError as exc:
            log.debug("disk usage of %s failed: %s", self.data_dir, exc)

        try:
            gpus = self.sampler.sample()
        except Exception as exc:  # 显卡掉线等情况不应让心跳失败
            log.warning("reading GPU metrics failed: %s", exc)
            gpus = []

        return {
            "cpu_percent": cpu_percent,
            "load_average": load,
            "memory_used_bytes": used,
            "memory_total_bytes": total,
            "disk_path": str(self.data_dir),
            "disk_total_bytes": disk_total,
            "disk_free_bytes": disk_free,
            "gpus": gpus,
        }
