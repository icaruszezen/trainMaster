"""测试公共工具：假的 NVML 模块（使用 nvidia-ml-py 真实的错误类型与常量）、临时根目录中的文件。"""

from __future__ import annotations

import types
from collections.abc import Callable
from pathlib import Path
from typing import Any

import pynvml

TRAINER_DIR = Path(__file__).resolve().parent.parent


class Err:
    """在假 NVML 设备配置中表示“调用该函数时返回这个 NVML 错误码”。"""

    def __init__(self, code: int) -> None:
        self.code = code


NOT_SUPPORTED = Err(pynvml.NVML_ERROR_NOT_SUPPORTED)
GPU_IS_LOST = Err(pynvml.NVML_ERROR_GPU_IS_LOST)
# 驱动的 libnvidia-ml 比 nvidia-ml-py 旧：不带版本号的函数抛出 NVMLLibraryMismatchError（不是 NVMLError）。
LIBRARY_MISMATCH = object()


def _value(v: Any) -> Any:
    if isinstance(v, Err):
        raise pynvml.NVMLError(v.code)
    if v is LIBRARY_MISMATCH:
        raise pynvml.NVMLLibraryMismatchError("Unversioned function called (fake)")
    return v


def rtx4090(**overrides: Any) -> dict[str, Any]:
    dev = {
        "name": "NVIDIA GeForce RTX 4090",
        "uuid": "GPU-5c1d0000-0000-0000-0000-000000000000",
        "memory_total": 25757220864,
        "memory_used": 20401094656,
        "cc": (8, 9),
        "bus_id": b"00000000:01:00.0",
        "power_limit": 450000,
        "mig": NOT_SUPPORTED,
        "util": 87,
        "temp": 71,
        "power": 312400,
        "fan": 55,
        "procs": [object()],
    }
    dev.update(overrides)
    return dev


def make_nvml(
    devices: list[dict[str, Any]] | None = None,
    *,
    init_error: int | None = None,
    driver: Any = "550.54.15",
    cuda: Any = 12040,
    nvml_version: Any = "12.550.54.15",
    count_error: int | None = None,
    missing: tuple[str, ...] = (),
) -> types.ModuleType:
    """构造一个与 pynvml 接口相同的模块；devices 中每个设备的值可以是 Err(code)。"""
    devices = devices if devices is not None else [rtx4090()]
    mod = types.ModuleType("pynvml")
    for name in dir(pynvml):
        if name.startswith(("NVML_", "NVMLError", "NVMLLibraryMismatchError")):
            setattr(mod, name, getattr(pynvml, name))
    state = {"init": 0, "shutdown": 0}
    mod.state = state  # type: ignore[attr-defined]

    def init() -> None:
        if init_error is not None:
            raise pynvml.NVMLError(init_error)
        state["init"] += 1

    def shutdown() -> None:
        state["shutdown"] += 1

    def count() -> int:
        if count_error is not None:
            raise pynvml.NVMLError(count_error)
        return len(devices)

    def handle(i: int) -> dict[str, Any]:
        dev = devices[i]
        if "handle_error" in dev:
            raise pynvml.NVMLError(dev["handle_error"])
        return dev

    def field(key: str, wrap: Callable[[Any], Any] = lambda v: v) -> Callable[..., Any]:
        def fn(h: dict[str, Any], *args: Any) -> Any:
            return wrap(_value(h.get(key, NOT_SUPPORTED)))

        return fn

    def memory(h: dict[str, Any]) -> Any:
        total = _value(h.get("memory_total", NOT_SUPPORTED))
        used = _value(h.get("memory_used", 0))
        return types.SimpleNamespace(total=total, used=used, free=total - used)

    funcs: dict[str, Callable[..., Any]] = {
        "nvmlInit": init,
        "nvmlShutdown": shutdown,
        "nvmlSystemGetDriverVersion": lambda: _value(driver),
        "nvmlSystemGetCudaDriverVersion": lambda: _value(cuda),
        "nvmlSystemGetCudaDriverVersion_v2": lambda: _value(cuda),
        "nvmlSystemGetNVMLVersion": lambda: _value(nvml_version),
        "nvmlDeviceGetCount": count,
        "nvmlDeviceGetHandleByIndex": handle,
        "nvmlDeviceGetName": field("name"),
        "nvmlDeviceGetUUID": field("uuid"),
        "nvmlDeviceGetMemoryInfo": memory,
        "nvmlDeviceGetCudaComputeCapability": field("cc"),
        "nvmlDeviceGetPciInfo": field("bus_id", lambda v: types.SimpleNamespace(busId=v)),
        "nvmlDeviceGetEnforcedPowerLimit": field("power_limit"),
        "nvmlDeviceGetPowerManagementLimit": field("power_limit"),
        "nvmlDeviceGetMigMode": field("mig"),
        "nvmlDeviceGetUtilizationRates": field(
            "util", lambda v: types.SimpleNamespace(gpu=v, memory=0)
        ),
        "nvmlDeviceGetTemperature": field("temp"),
        "nvmlDeviceGetPowerUsage": field("power"),
        "nvmlDeviceGetFanSpeed": field("fan"),
        "nvmlDeviceGetComputeRunningProcesses": field("procs"),
        "nvmlDeviceGetComputeRunningProcesses_v2": field("procs_v2"),
    }
    for name, fn in funcs.items():
        if name not in missing:
            setattr(mod, name, fn)
    return mod


def write_files(root: Path, files: dict[str, str]) -> Path:
    """在 root 下按相对路径写入文件（模拟 /proc、/sys、/etc）。"""
    for rel, content in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
    return root


# ---------------------------------------------------------------------------
# 假的 torch / torchvision / ultralytics（环境检测子进程的逻辑测试用）


class FakeTensor:
    def __init__(self, device: str = "cpu", finite: bool = True) -> None:
        self.device = device
        self.finite = finite

    def __matmul__(self, other: FakeTensor) -> FakeTensor:
        return FakeTensor(self.device, self.finite and other.finite)

    def float(self) -> FakeTensor:
        return self


class _Bool:
    def __init__(self, value: bool) -> None:
        self.value = value

    def all(self) -> _Bool:
        return self

    def __bool__(self) -> bool:
        return self.value


class _Context:
    def __init__(self, log: list[str], name: str, error: BaseException | None = None) -> None:
        self.log, self.name, self.error = log, name, error

    def __enter__(self) -> None:
        self.log.append(f"enter {self.name}")
        if self.error is not None:
            raise self.error

    def __exit__(self, *exc: Any) -> None:
        self.log.append(f"exit {self.name}")


def make_torch(
    *,
    version: str = "2.14.1+cpu",
    cuda: str | None = None,
    available: bool = False,
    device_count: int = 0,
    arch_list: list[str] | None = None,
    cudnn: int | None = None,
    availability_warning: str | None = None,
    init_error: BaseException | None = None,
    matmul_nan_on: int | None = None,
    autocast_error: BaseException | None = None,
    capabilities: list[tuple[int, int]] | None = None,
) -> types.ModuleType:
    """一个只实现自检用到的接口的 torch；calls 记录调用顺序。"""
    import warnings

    torch = types.ModuleType("torch")
    calls: list[str] = []
    torch.calls = calls  # type: ignore[attr-defined]
    torch.__version__ = version  # type: ignore[attr-defined]
    torch.version = types.SimpleNamespace(cuda=cuda)  # type: ignore[attr-defined]
    torch.Tensor = FakeTensor  # type: ignore[attr-defined]
    torch.float16 = "float16"  # type: ignore[attr-defined]
    torch.backends = types.SimpleNamespace(cudnn=types.SimpleNamespace(version=lambda: cudnn))  # type: ignore[attr-defined]

    def is_available() -> bool:
        if availability_warning:
            warnings.warn(availability_warning, UserWarning, stacklevel=2)
        return available

    def init() -> None:
        if init_error is not None:
            raise init_error

    def synchronize(i: int) -> None:
        calls.append(f"synchronize {i}")

    def get_device_capability(i: int) -> tuple[int, int]:
        caps = capabilities or []
        return caps[i] if i < len(caps) else (8, 9)  # 没有指定时按 RTX 4090（8.9）处理

    torch.cuda = types.SimpleNamespace(  # type: ignore[attr-defined]
        is_available=is_available,
        device_count=lambda: device_count,
        get_arch_list=lambda: list(arch_list or []),
        init=init,
        synchronize=synchronize,
        get_device_capability=get_device_capability,
    )

    def randn(*shape: int, device: str = "cpu") -> FakeTensor:
        calls.append(f"randn {device}")
        index = int(device.split(":")[1]) if device.startswith("cuda:") else None
        return FakeTensor(device, finite=index != matmul_nan_on)

    def rand(*shape: int, device: str = "cpu") -> FakeTensor:
        calls.append(f"rand {device}")
        return FakeTensor(device)

    torch.randn = randn  # type: ignore[attr-defined]
    torch.rand = rand  # type: ignore[attr-defined]
    torch.manual_seed = lambda seed: None  # type: ignore[attr-defined]
    torch.isfinite = lambda t: _Bool(t.finite)  # type: ignore[attr-defined]
    torch.inference_mode = lambda: _Context(calls, "inference_mode")  # type: ignore[attr-defined]
    torch.autocast = lambda device_type, dtype: _Context(  # type: ignore[attr-defined]
        calls, f"autocast {device_type} {dtype}", autocast_error
    )
    return torch


class FakeNet:
    def __init__(
        self, calls: list[str], error: BaseException | None = None, finite: bool = True
    ) -> None:
        self.calls, self.error, self.finite = calls, error, finite
        self.device = "cpu"

    def to(self, device: str) -> FakeNet:
        self.device = device
        self.calls.append(f"to {device}")
        return self

    def eval(self) -> FakeNet:
        return self

    def __call__(self, x: FakeTensor) -> tuple[FakeTensor, list[FakeTensor]]:
        self.calls.append(f"forward {x.device}")
        if self.error is not None:
            raise self.error
        return (FakeTensor(x.device, self.finite), [FakeTensor(x.device)])


def make_ultralytics(
    calls: list[str],
    *,
    version: str = "8.4.198",
    error: BaseException | None = None,
    finite: bool = True,
) -> types.ModuleType:
    mod = types.ModuleType("ultralytics")
    mod.__version__ = version  # type: ignore[attr-defined]

    class YOLO:
        def __init__(self, cfg: str) -> None:
            calls.append(f"YOLO {cfg}")
            print("ultralytics prints its model summary to stdout")
            self.model = FakeNet(calls, error, finite)

    mod.YOLO = YOLO  # type: ignore[attr-defined]
    return mod


def make_torchvision(version: str = "0.29.1+cpu") -> types.ModuleType:
    mod = types.ModuleType("torchvision")
    mod.__version__ = version  # type: ignore[attr-defined]
    return mod
