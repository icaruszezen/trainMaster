"""NVIDIA 显卡探测与实时指标。

优先使用 NVML（nvidia-ml-py 提供的 pynvml 模块，直接调用驱动自带的 libnvidia-ml）：快、信息全，而且不会
创建 CUDA 上下文、不占用显存，可以在常驻进程里长期使用。NVML 不可用时（库没有挂载进容器、驱动与库版本
不一致等）回退到解析 nvidia-smi 的输出；都不可用时视为没有显卡，并记录原因供诊断。
"""

from __future__ import annotations

import csv
import io
import logging
import os
import re
import shutil
import subprocess
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Protocol

log = logging.getLogger(__name__)

MIB = 1024 * 1024
SMI_TIMEOUT = 15.0
# 与设计文档一致的查询字段；旧驱动（< 510）的 nvidia-smi 不支持 compute_cap，会去掉它重试。
SMI_FIELDS = (
    "index",
    "uuid",
    "name",
    "memory.total",
    "memory.used",
    "utilization.gpu",
    "temperature.gpu",
    "power.draw",
    "power.limit",
    "fan.speed",
    "compute_cap",
    "pci.bus_id",
)
DEFAULT_GPU_NAME = "NVIDIA GPU"

Runner = Callable[..., subprocess.CompletedProcess]


class GpuSampler(Protocol):
    """读取各显卡的实时指标（Metrics.gpus）。"""

    def sample(self) -> list[dict[str, Any]]: ...

    def close(self) -> None: ...


class NullSampler:
    def sample(self) -> list[dict[str, Any]]:
        return []

    def close(self) -> None:
        return None


@dataclass
class GpuProbe:
    """一次显卡探测的结果：Hardware 中的 gpu_driver、gpus、gpu_detection，以及读取实时指标的 sampler。"""

    driver: dict[str, Any] | None
    gpus: list[dict[str, Any]]
    detection: dict[str, Any]
    sampler: GpuSampler = field(default_factory=NullSampler)


def _text(value: Any) -> str | None:
    """NVML 旧版本返回 bytes，新版本返回 str；去掉结尾的 NUL 与空白。"""
    if value is None:
        return None
    if isinstance(value, bytes):
        value = value.decode("utf-8", "replace")
    text = str(value).replace("\x00", "").strip()
    return text or None


def _watts(milliwatts: Any) -> float | None:
    if not isinstance(milliwatts, (int, float)) or isinstance(milliwatts, bool) or milliwatts < 0:
        return None
    return round(milliwatts / 1000.0, 1)


def _int_or_none(value: Any) -> int | None:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value < 0:
        return None
    return int(value)


def format_cuda_driver_version(value: Any) -> str | None:
    """NVML 的 cudaDriverVersion（如 12040）→ "12.4"。"""
    v = _int_or_none(value)
    if not v:
        return None
    return f"{v // 1000}.{(v % 1000) // 10}"


# ---------------------------------------------------------------------------
# NVML


def load_nvml() -> Any:
    import pynvml  # nvidia-ml-py

    return pynvml


class NvmlSession:
    """nvmlInit 之后的会话。每个字段单独读取：某项不支持（如数据中心卡没有风扇）只让该字段为 null。"""

    def __init__(self, nvml: Any) -> None:
        self.nvml = nvml
        nvml.nvmlInit()
        self._closed = False

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            self.nvml.nvmlShutdown()
        except Exception as exc:  # 关闭失败不影响后续探测
            log.debug("nvmlShutdown failed: %s", exc)

    def _is_not_supported(self, exc: BaseException) -> bool:
        code = getattr(self.nvml, "NVML_ERROR_NOT_SUPPORTED", 3)
        return getattr(exc, "value", None) == code

    def call(self, name: str, *args: Any) -> Any:
        """调用 NVML 函数；函数不存在（旧版 nvidia-ml-py）或返回 NVML 错误时为 None。"""
        fn = getattr(self.nvml, name, None)
        if fn is None:
            return None
        try:
            return fn(*args)
        except self.nvml.NVMLError as exc:
            log.debug("%s failed: %s", name, exc)
            return None

    def driver_info(self) -> dict[str, Any] | None:
        driver = _text(self.call("nvmlSystemGetDriverVersion"))
        if driver is None:
            return None
        cuda = self.call("nvmlSystemGetCudaDriverVersion_v2")
        if cuda is None:
            cuda = self.call("nvmlSystemGetCudaDriverVersion")
        return {
            "driver_version": driver,
            "cuda_driver_version": format_cuda_driver_version(cuda),
            "nvml_version": _text(self.call("nvmlSystemGetNVMLVersion")),
        }

    def handles(self) -> tuple[list[tuple[int, Any]], list[str]]:
        """所有可访问的显卡句柄；无法访问的显卡（如 GPU is lost）记录在错误列表中。"""
        count = self.nvml.nvmlDeviceGetCount()
        handles: list[tuple[int, Any]] = []
        errors: list[str] = []
        for index in range(count):
            try:
                handles.append((index, self.nvml.nvmlDeviceGetHandleByIndex(index)))
            except self.nvml.NVMLError as exc:
                errors.append(f"GPU {index}: {exc}")
        return handles, errors

    def mig_enabled(self, handle: Any) -> bool | None:
        fn = getattr(self.nvml, "nvmlDeviceGetMigMode", None)
        if fn is None:
            return None
        try:
            current, _pending = fn(handle)
        except self.nvml.NVMLError as exc:
            # 不支持 MIG 的显卡（绝大多数）返回 NOT_SUPPORTED：MIG 当然没有开启。
            return False if self._is_not_supported(exc) else None
        return current == getattr(self.nvml, "NVML_DEVICE_MIG_ENABLE", 1)

    def static_info(self, index: int, handle: Any) -> dict[str, Any]:
        memory = self.call("nvmlDeviceGetMemoryInfo", handle)
        cc = self.call("nvmlDeviceGetCudaComputeCapability", handle)
        pci = self.call("nvmlDeviceGetPciInfo", handle)
        power = self.call("nvmlDeviceGetEnforcedPowerLimit", handle)
        if power is None:
            power = self.call("nvmlDeviceGetPowerManagementLimit", handle)
        compute_capability = None
        if isinstance(cc, (tuple, list)) and len(cc) == 2:
            compute_capability = f"{int(cc[0])}.{int(cc[1])}"
        return {
            "index": index,
            "uuid": _text(self.call("nvmlDeviceGetUUID", handle)),
            "name": _text(self.call("nvmlDeviceGetName", handle)) or DEFAULT_GPU_NAME,
            "memory_total_bytes": _int_or_none(getattr(memory, "total", None)),
            "compute_capability": compute_capability,
            "pci_bus_id": _text(getattr(pci, "busId", None)),
            "power_limit_watts": _watts(power),
            "mig_enabled": self.mig_enabled(handle),
        }

    def metrics(self, index: int, handle: Any) -> dict[str, Any]:
        util = self.call("nvmlDeviceGetUtilizationRates", handle)
        memory = self.call("nvmlDeviceGetMemoryInfo", handle)
        temp_sensor = getattr(self.nvml, "NVML_TEMPERATURE_GPU", 0)
        processes = self.call("nvmlDeviceGetComputeRunningProcesses", handle)
        return {
            "index": index,
            "utilization_percent": _int_or_none(getattr(util, "gpu", None)),
            "memory_used_bytes": _int_or_none(getattr(memory, "used", None)),
            "memory_total_bytes": _int_or_none(getattr(memory, "total", None)),
            "temperature_c": _int_or_none(
                self.call("nvmlDeviceGetTemperature", handle, temp_sensor)
            ),
            "power_watts": _watts(self.call("nvmlDeviceGetPowerUsage", handle)),
            "fan_percent": _int_or_none(self.call("nvmlDeviceGetFanSpeed", handle)),
            "process_count": len(processes) if isinstance(processes, (list, tuple)) else None,
        }


class NvmlSampler:
    def __init__(self, session: NvmlSession, handles: Sequence[tuple[int, Any]]) -> None:
        self.session = session
        self.handles = list(handles)

    def sample(self) -> list[dict[str, Any]]:
        return [self.session.metrics(index, handle) for index, handle in self.handles]

    def close(self) -> None:
        self.session.close()


def _probe_nvml(
    nvml: Any,
) -> tuple[dict[str, Any] | None, list[dict[str, Any]], str | None, NvmlSampler]:
    session = NvmlSession(nvml)
    try:
        handles, errors = session.handles()
        gpus = [session.static_info(index, handle) for index, handle in handles]
        driver = session.driver_info()
    except BaseException:
        session.close()
        raise
    return driver, gpus, "; ".join(errors) or None, NvmlSampler(session, handles)


def _nvml_error_text(exc: BaseException) -> str:
    text = str(exc).strip()
    return text or type(exc).__name__


# ---------------------------------------------------------------------------
# nvidia-smi


class SmiError(RuntimeError):
    pass


def find_nvidia_smi(env: dict[str, str] | None = None) -> str | None:
    """TM_NVIDIA_SMI 可指定替身命令（测试用）；否则在 PATH 中查找。"""
    env = os.environ if env is None else env
    override = env.get("TM_NVIDIA_SMI", "").strip()
    if override:
        return override
    return shutil.which("nvidia-smi")


def smi_value(raw: str | None) -> str | None:
    """nvidia-smi 用 [N/A]、[Not Supported]、[Unknown Error] 等表示没有该值。"""
    if raw is None:
        return None
    value = raw.strip()
    if not value or value.upper() == "N/A" or (value.startswith("[") and value.endswith("]")):
        return None
    return value


def _smi_float(raw: str | None) -> float | None:
    value = smi_value(raw)
    if value is None:
        return None
    try:
        number = float(value)
    except ValueError:
        return None
    if number != number or number in (float("inf"), float("-inf")) or number < 0:
        return None
    return number


def _smi_int(raw: str | None) -> int | None:
    number = _smi_float(raw)
    return None if number is None else int(round(number))


def parse_smi_csv(text: str, fields: Sequence[str]) -> list[dict[str, str | None]]:
    """解析 --format=csv,noheader,nounits 的输出；列数不对的行忽略。"""
    rows: list[dict[str, str | None]] = []
    for row in csv.reader(io.StringIO(text), skipinitialspace=True):
        if not row or all(not c.strip() for c in row):
            continue
        if len(row) != len(fields):
            log.debug("ignoring nvidia-smi row with %d columns: %r", len(row), row)
            continue
        rows.append({f: smi_value(v) for f, v in zip(fields, row, strict=True)})
    return rows


_DRIVER_RE = re.compile(r"Driver Version:\s*([0-9][0-9.]*)")
_CUDA_RE = re.compile(r"CUDA Version:\s*([0-9][0-9.]*)")


def parse_smi_header(text: str) -> tuple[str | None, str | None]:
    """从 nvidia-smi 默认输出的表头取驱动版本与驱动支持的最高 CUDA 版本。"""
    driver = _DRIVER_RE.search(text)
    cuda = _CUDA_RE.search(text)
    return (driver.group(1) if driver else None, cuda.group(1) if cuda else None)


def smi_static(row: dict[str, str | None]) -> dict[str, Any]:
    memory_total = _smi_float(row.get("memory.total"))
    cc = row.get("compute_cap")
    return {
        "index": _smi_int(row.get("index")),
        "uuid": row.get("uuid"),
        "name": row.get("name") or DEFAULT_GPU_NAME,
        "memory_total_bytes": None if memory_total is None else int(memory_total * MIB),
        "compute_capability": cc if cc and re.fullmatch(r"\d+\.\d+", cc) else None,
        "pci_bus_id": row.get("pci.bus_id"),
        "power_limit_watts": _smi_float(row.get("power.limit")),
        "mig_enabled": None,
    }


def smi_metrics(row: dict[str, str | None], process_count: int | None) -> dict[str, Any]:
    used = _smi_float(row.get("memory.used"))
    total = _smi_float(row.get("memory.total"))
    power = _smi_float(row.get("power.draw"))
    return {
        "index": _smi_int(row.get("index")),
        "utilization_percent": _smi_float(row.get("utilization.gpu")),
        "memory_used_bytes": None if used is None else int(used * MIB),
        "memory_total_bytes": None if total is None else int(total * MIB),
        "temperature_c": _smi_float(row.get("temperature.gpu")),
        "power_watts": None if power is None else round(power, 1),
        "fan_percent": _smi_float(row.get("fan.speed")),
        "process_count": process_count,
    }


class NvidiaSmi:
    """调用 nvidia-smi；runner 可替换（测试用）。"""

    def __init__(self, path: str, runner: Runner = subprocess.run, timeout: float = SMI_TIMEOUT):
        self.path = path
        self.runner = runner
        self.timeout = timeout
        self.fields: tuple[str, ...] = SMI_FIELDS

    def run(self, *args: str) -> str:
        try:
            proc = self.runner(
                [self.path, *args],
                capture_output=True,
                text=True,
                timeout=self.timeout,
                check=False,
            )
        except (OSError, subprocess.SubprocessError) as exc:
            raise SmiError(f"cannot run {self.path}: {exc}") from exc
        if proc.returncode != 0:
            message = (proc.stdout or "").strip() or (proc.stderr or "").strip()
            raise SmiError(message.splitlines()[0] if message else f"exit status {proc.returncode}")
        return proc.stdout or ""

    def query(self) -> list[dict[str, str | None]]:
        try:
            out = self.run(f"--query-gpu={','.join(self.fields)}", "--format=csv,noheader,nounits")
        except SmiError:
            if "compute_cap" not in self.fields:
                raise
            # 旧驱动不认识 compute_cap 字段，整条查询失败；去掉它再试，计算能力记为未知。
            fields = tuple(f for f in self.fields if f != "compute_cap")
            out = self.run(f"--query-gpu={','.join(fields)}", "--format=csv,noheader,nounits")
            self.fields = fields
        return parse_smi_csv(out, self.fields)

    def process_counts(self) -> dict[str, int] | None:
        try:
            out = self.run("--query-compute-apps=gpu_uuid", "--format=csv,noheader")
        except SmiError:
            return None
        counts: dict[str, int] = {}
        for line in out.splitlines():
            uuid = smi_value(line)
            if uuid:
                counts[uuid] = counts.get(uuid, 0) + 1
        return counts

    def driver_info(self) -> dict[str, Any] | None:
        driver, cuda = parse_smi_header(self.run())
        if driver is None:
            return None
        return {"driver_version": driver, "cuda_driver_version": cuda, "nvml_version": None}


class SmiSampler:
    def __init__(self, smi: NvidiaSmi) -> None:
        self.smi = smi

    def sample(self) -> list[dict[str, Any]]:
        try:
            rows = self.smi.query()
        except SmiError as exc:
            log.debug("nvidia-smi query failed: %s", exc)
            return []
        counts = self.smi.process_counts()
        out = []
        for row in rows:
            uuid = row.get("uuid")
            count = None if counts is None else counts.get(uuid or "", 0)
            out.append(smi_metrics(row, count))
        return [m for m in out if m["index"] is not None]

    def close(self) -> None:
        return None


def _probe_smi(smi: NvidiaSmi) -> tuple[dict[str, Any] | None, list[dict[str, Any]]]:
    rows = smi.query()
    gpus = [g for g in (smi_static(r) for r in rows) if g["index"] is not None]
    try:
        driver = smi.driver_info()
    except SmiError as exc:
        log.debug("cannot read nvidia-smi header: %s", exc)
        driver = None
    return driver, gpus


# ---------------------------------------------------------------------------
# /proc 与 /dev


def host_gpu_count(root: str | Path = "/") -> int | None:
    """/proc/driver/nvidia/gpus 下每块显卡一个目录。容器中也能看到宿主机驱动的这个目录，
    用来判断“宿主机有显卡、但没有传入容器”。读不到时为 None。"""
    path = Path(root) / "proc" / "driver" / "nvidia" / "gpus"
    try:
        return sum(1 for entry in path.iterdir() if entry.is_dir())
    except OSError:
        return None


def pci_gpu_count(root: str | Path = "/") -> int | None:
    """PCI 总线上的 NVIDIA 显卡数（vendor 0x10de，class 0x0300 VGA 或 0x0302 3D 控制器）。

    驱动没有加载时 NVML 与 /proc/driver/nvidia 都看不到显卡，用它提示“有显卡但没有驱动”。读不到时为 None。
    """
    devices = Path(root) / "sys" / "bus" / "pci" / "devices"
    try:
        entries = list(devices.iterdir())
    except OSError:
        return None
    count = 0
    for entry in entries:
        try:
            vendor = (entry / "vendor").read_text(encoding="ascii").strip().lower()
            klass = (entry / "class").read_text(encoding="ascii").strip().lower()
        except (OSError, UnicodeDecodeError):
            continue
        if vendor == "0x10de" and klass.startswith(("0x0300", "0x0302")):
            count += 1
    return count


def nvidia_device_nodes(root: str | Path = "/") -> list[str]:
    """容器中的 /dev/nvidia0、/dev/nvidia1 …（GPU 传入容器后才有）。"""
    dev = Path(root) / "dev"
    try:
        return sorted(p.name for p in dev.glob("nvidia[0-9]*"))
    except OSError:
        return []


# ---------------------------------------------------------------------------


def probe(
    *,
    root: str | Path = "/",
    nvml_loader: Callable[[], Any] = load_nvml,
    smi_path: str | None = None,
    find_smi: bool = True,
    runner: Runner = subprocess.run,
) -> GpuProbe:
    """探测显卡：NVML → nvidia-smi → 没有显卡。

    smi_path 为空且 find_smi 为 True 时自动查找 nvidia-smi（TM_NVIDIA_SMI 或 PATH）。
    """
    host_count = host_gpu_count(root)
    nvml_error: str | None = None
    try:
        nvml = nvml_loader()
    except ImportError as exc:
        nvml_error = f"nvidia-ml-py is not installed: {exc}"
    else:
        try:
            driver, gpus, device_errors, sampler = _probe_nvml(nvml)
        except Exception as exc:  # NVMLError 或加载 libnvidia-ml 时的其它错误
            nvml_error = _nvml_error_text(exc)
        else:
            return GpuProbe(
                driver=driver,
                gpus=gpus,
                detection={"method": "nvml", "error": device_errors, "host_gpu_count": host_count},
                sampler=sampler,
            )

    path = smi_path or (find_nvidia_smi() if find_smi else None)
    if path:
        smi = NvidiaSmi(str(path), runner=runner)
        try:
            driver, gpus = _probe_smi(smi)
        except SmiError as exc:
            nvml_error = f"{nvml_error}; nvidia-smi: {exc}" if nvml_error else f"nvidia-smi: {exc}"
        else:
            return GpuProbe(
                driver=driver,
                gpus=gpus,
                detection={
                    "method": "nvidia-smi",
                    "error": nvml_error,
                    "host_gpu_count": host_count,
                },
                sampler=SmiSampler(smi),
            )

    return GpuProbe(
        driver=None,
        gpus=[],
        detection={"method": "none", "error": nvml_error, "host_gpu_count": host_count},
        sampler=NullSampler(),
    )
