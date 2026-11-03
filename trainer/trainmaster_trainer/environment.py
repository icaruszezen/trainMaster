"""CUDA / PyTorch / YOLO 环境检测。

检测分两部分：
- 子进程（python -m trainmaster_trainer.environment）：导入 torch / torchvision / ultralytics，收集版本与
  CUDA 状态并做自检（YOLO 前向推理），把结果以 JSON 输出到 stdout。torch 只在这个短命的子进程中导入，
  常驻进程不会创建 CUDA 上下文、不会占用显存；导入失败、驱动问题导致的崩溃也不会影响常驻进程。
- 父进程（EnvironmentDetector）：启动子进程并限制时间，把子进程的结果与硬件信息合并，
  由 issues.build_environment 生成 Environment。
"""

from __future__ import annotations

import contextlib
import json
import logging
import os
import platform
import signal
import subprocess
import sys
import threading
import time
import traceback
from collections.abc import Callable, Mapping, Sequence
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING, Any

from . import issues as issues_mod

if TYPE_CHECKING:
    from .config import Config

log = logging.getLogger(__name__)

MAX_DETAIL = 2000
CHILD_MODULE = "trainmaster_trainer.environment"
# YOLO11n 的配置随 ultralytics 一起安装，从配置构建模型不需要下载权重。
YOLO_CONFIG = "yolo11n.yaml"
YOLO_LABEL = "YOLO11n"
INPUT_SIZE = 320

# 子进程 JSON 中各字段的类型（父进程据此清洗，类型不对的值按未知处理）。
_STR_FIELDS = (
    "python_version",
    "torch_version",
    "torch_cuda_version",
    "cudnn_version",
    "torchvision_version",
    "ultralytics_version",
    "torch_error",
    "torchvision_error",
    "ultralytics_error",
    "cuda_error",
)


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def summarize_exception(exc: BaseException, limit: int = MAX_DETAIL) -> str:
    """异常摘要：类型、消息与回溯的最后几帧，最多 limit 个字符（保留结尾，最有用的信息通常在最后）。"""
    text = "".join(traceback.format_exception(type(exc), exc, exc.__traceback__)).strip()
    if len(text) > limit:
        text = "…" + text[-(limit - 1) :]
    return text


def _short_error(exc: BaseException) -> str:
    message = str(exc).strip().splitlines()[0] if str(exc).strip() else ""
    return f"{type(exc).__name__}: {message}" if message else type(exc).__name__


def format_cudnn_version(value: Any) -> str | None:
    """torch.backends.cudnn.version()：cuDNN 9 起为 主*10000+次*100+补丁（91002 → 9.10.2），
    之前为 主*1000+次*100+补丁（8902 → 8.9.2）。"""
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        return None
    if value >= 10000:
        return f"{value // 10000}.{(value % 10000) // 100}.{value % 100}"
    return f"{value // 1000}.{(value % 1000) // 100}.{value % 100}"


# ---------------------------------------------------------------------------
# 子进程


def _import(name: str) -> tuple[Any | None, str | None]:
    try:
        module = __import__(name)
    except (
        BaseException
    ) as exc:  # ImportError、缺少 .so 时的 OSError、驱动问题导致的 RuntimeError 等
        if isinstance(exc, (KeyboardInterrupt, SystemExit)):
            raise
        return None, _short_error(exc)
    return module, None


def _first_tensor(torch: Any, value: Any) -> Any:
    if isinstance(value, torch.Tensor):
        return value
    if isinstance(value, (list, tuple)):
        for item in value:
            found = _first_tensor(torch, item)
            if found is not None:
                return found
    if isinstance(value, dict):
        for item in value.values():
            found = _first_tensor(torch, item)
            if found is not None:
                return found
    return None


def _forward(torch: Any, net: Any, x: Any, label: str) -> None:
    with torch.inference_mode():
        out = net(x)
    tensor = _first_tensor(torch, out)
    if tensor is None:
        raise RuntimeError(f"{label}: model returned no tensor")
    if not bool(torch.isfinite(tensor.float()).all()):
        raise RuntimeError(f"{label}: output contains NaN or Inf")


def unsupported_devices(torch: Any, cuda_count: int) -> dict[int, str]:
    """已安装的 PyTorch 没有为其编译代码的 GPU：{CUDA 序号: 计算能力}。无法判断时视为都支持。"""
    try:
        arch_list = [str(a) for a in torch.cuda.get_arch_list()]
    except Exception as exc:
        log.debug("get_arch_list failed: %s", exc)
        return {}
    if not arch_list:
        return {}
    out: dict[int, str] = {}
    for i in range(cuda_count):
        try:
            major, minor = (int(v) for v in torch.cuda.get_device_capability(i))
        except Exception as exc:
            log.debug("get_device_capability(%d) failed: %s", i, exc)
            continue
        if not issues_mod.arch_covers(major * 10 + minor, arch_list):
            out[i] = f"{major}.{minor}"
    return out


def run_self_test(torch: Any, ultralytics: Any, cuda_count: int) -> dict[str, Any]:
    """执行自检。有 CUDA 时先在每块 GPU 上做小矩阵乘法，再在第一块 GPU 上做 YOLO 前向推理（FP32 与 FP16）；
    没有 CUDA 时在 CPU 上做前向推理。不访问网络、不下载权重。

    新旧显卡混用时，已安装的 PyTorch 不支持的显卡（如 cu130 与计算能力 6.1 的显卡）不参加自检，
    其余显卡照常测试，结果中注明跳过了哪些；全部都不支持时照常测试，报告实际的错误。
    """
    skipped = unsupported_devices(torch, cuda_count) if cuda_count > 0 else {}
    devices = [i for i in range(cuda_count) if i not in skipped]
    if not devices:
        devices, skipped = list(range(cuda_count)), {}
    device = f"cuda:{devices[0]}" if devices else "cpu"
    start = time.monotonic()
    stage = "初始化"
    try:
        torch.manual_seed(0)
        for i in devices:
            stage = f"GPU {i} 矩阵乘法"
            a = torch.randn(256, 256, device=f"cuda:{i}")
            b = a @ a
            torch.cuda.synchronize(i)
            if not bool(torch.isfinite(b).all()):
                raise RuntimeError(f"cuda:{i}: matmul result contains NaN or Inf")

        stage = f"构建 {YOLO_LABEL} 模型"
        net = ultralytics.YOLO(YOLO_CONFIG).model
        net = net.to(device).eval()
        x = torch.rand(1, 3, INPUT_SIZE, INPUT_SIZE, device=device)

        stage = f"{YOLO_LABEL} 前向推理（{device}，FP32）"
        _forward(torch, net, x, "fp32")

        precision = "FP32"
        fp16_error = None
        if cuda_count > 0:
            stage = f"{YOLO_LABEL} 前向推理（{device}，FP16）"
            try:
                with torch.autocast(device_type="cuda", dtype=torch.float16):
                    _forward(torch, net, x, "fp16")
                precision = "FP16"
            except Exception as exc:  # FP16 不可用时仍可用 FP32 训练，只在说明中注明
                fp16_error = _short_error(exc)
    except Exception as exc:
        return {
            "status": "failed",
            "device": device,
            "duration_ms": int((time.monotonic() - start) * 1000),
            "message": f"{stage}失败：{_short_error(exc)}"[:300],
            "detail": summarize_exception(exc),
        }

    if cuda_count > 0:
        message = f"{YOLO_LABEL} 前向推理正常（{len(devices)} 块 GPU，{precision}）"
        if fp16_error:
            message += f"；FP16 不可用：{fp16_error}"[:200]
        if skipped:
            message += (
                "；跳过已安装的 PyTorch 不支持的 "
                + "、".join(f"GPU {i}（计算能力 {cc}）" for i, cc in skipped.items())
            )[:200]
    else:
        message = f"{YOLO_LABEL} 前向推理正常（CPU）"
    return {
        "status": "passed",
        "device": device,
        "duration_ms": int((time.monotonic() - start) * 1000),
        "message": message,
        "detail": None,
    }


def collect(self_test: bool = True) -> dict[str, Any]:
    """子进程中执行：收集 torch / torchvision / ultralytics 的版本与 CUDA 状态，并按需执行自检。"""
    result: dict[str, Any] = {
        "python_version": platform.python_version(),
        "torch_version": None,
        "torch_cuda_version": None,
        "cudnn_version": None,
        "torch_arch_list": [],
        "torchvision_version": None,
        "ultralytics_version": None,
        "cuda_available": False,
        "cuda_device_count": 0,
        "torch_error": None,
        "torchvision_error": None,
        "ultralytics_error": None,
        "cuda_error": None,
        "self_test": None,
    }

    torch, result["torch_error"] = _import("torch")
    if torch is not None:
        result["torch_version"] = str(torch.__version__)
        result["torch_cuda_version"] = getattr(torch.version, "cuda", None)
        try:
            result["cudnn_version"] = format_cudnn_version(torch.backends.cudnn.version())
        except Exception as exc:
            log.debug("cudnn version unavailable: %s", exc)

        import warnings

        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            try:
                available = bool(torch.cuda.is_available())
            except Exception as exc:
                available = False
                result["cuda_error"] = _short_error(exc)
        if available:
            result["cuda_available"] = True
            result["cuda_device_count"] = int(torch.cuda.device_count())
            try:
                result["torch_arch_list"] = [str(a) for a in torch.cuda.get_arch_list()]
            except Exception as exc:
                log.debug("get_arch_list failed: %s", exc)
        elif result["torch_cuda_version"]:
            # is_available() 只返回 False；torch.cuda.init() 会抛出带原因的异常（如驱动过旧）。
            reasons = [str(w.message).strip() for w in caught if str(w.message).strip()]
            try:
                torch.cuda.init()
            except Exception as exc:
                reasons.append(_short_error(exc))
            if reasons and not result["cuda_error"]:
                result["cuda_error"] = "; ".join(dict.fromkeys(reasons))[:MAX_DETAIL]

    torchvision, result["torchvision_error"] = _import("torchvision")
    if torchvision is not None:
        result["torchvision_version"] = str(getattr(torchvision, "__version__", "")) or None

    ultralytics, result["ultralytics_error"] = _import("ultralytics")
    if ultralytics is not None:
        result["ultralytics_version"] = str(getattr(ultralytics, "__version__", "")) or None

    missing = [
        name
        for name, mod in (
            ("PyTorch", torch),
            ("torchvision", torchvision),
            ("Ultralytics", ultralytics),
        )
        if mod is None
    ]
    if not self_test:
        result["self_test"] = _skipped("已关闭自检（TM_SELF_TEST=false）")
    elif missing:
        result["self_test"] = _skipped(f"缺少 {'、'.join(missing)}，未执行自检")
    else:
        result["self_test"] = run_self_test(torch, ultralytics, result["cuda_device_count"])
    return result


def _skipped(message: str) -> dict[str, Any]:
    return {
        "status": "skipped",
        "device": None,
        "duration_ms": None,
        "message": message,
        "detail": None,
    }


def child_main(argv: Sequence[str] | None = None) -> int:
    """子进程入口：结果 JSON 写到 stdout，其它输出（ultralytics 的日志、C 扩展的打印）一律转到 stderr。"""
    argv = list(sys.argv[1:] if argv is None else argv)
    self_test = "--no-self-test" not in argv
    os.environ.setdefault("YOLO_OFFLINE", "1")
    os.environ.setdefault("YOLO_VERBOSE", "False")
    logging.basicConfig(stream=sys.stderr, level=logging.WARNING)

    sys.stdout.flush()
    json_out = os.fdopen(os.dup(1), "w", encoding="utf-8")
    os.dup2(2, 1)  # 文件描述符级别的重定向：C 扩展直接写 fd 1 的内容也不会混入 JSON
    with contextlib.redirect_stdout(sys.stderr):
        result = collect(self_test=self_test)
    json.dump(result, json_out, ensure_ascii=False)
    json_out.write("\n")
    json_out.flush()
    return 0


# ---------------------------------------------------------------------------
# 父进程


def _as_str(value: Any) -> str | None:
    return value if isinstance(value, str) and value != "" else None


def sanitize_raw(data: Any) -> dict[str, Any]:
    """校验子进程输出的结构：类型不对的字段按未知处理；结构完全不对时抛出 ValueError。"""
    if not isinstance(data, dict):
        raise ValueError("expected a JSON object")
    raw: dict[str, Any] = {key: _as_str(data.get(key)) for key in _STR_FIELDS}
    archs = data.get("torch_arch_list")
    raw["torch_arch_list"] = (
        [a for a in archs if isinstance(a, str)][:64] if isinstance(archs, list) else []
    )
    raw["cuda_available"] = data.get("cuda_available") is True
    count = data.get("cuda_device_count")
    raw["cuda_device_count"] = (
        count if isinstance(count, int) and not isinstance(count, bool) and count >= 0 else 0
    )
    st = data.get("self_test")
    if not isinstance(st, dict):
        raise ValueError("missing self_test")
    status = st.get("status") if st.get("status") in ("passed", "failed", "skipped") else "failed"
    duration = st.get("duration_ms")
    if isinstance(duration, bool) or not isinstance(duration, int) or duration < 0:
        duration = None
    detail = _as_str(st.get("detail"))
    raw["self_test"] = {
        "status": status,
        "device": _as_str(st.get("device")),
        "duration_ms": duration,
        "message": _as_str(st.get("message")),
        "detail": detail[:MAX_DETAIL] if detail else None,
    }
    raw["probe_error"] = None
    return raw


def failure_raw(
    message: str, detail: str | None = None, duration_ms: int | None = None
) -> dict[str, Any]:
    """子进程整体失败（超时、崩溃、输出无法解析）时的检测结果：只知道自检失败。"""
    raw: dict[str, Any] = dict.fromkeys(_STR_FIELDS)
    raw.update(
        python_version=platform.python_version(),
        torch_arch_list=[],
        cuda_available=False,
        cuda_device_count=0,
        probe_error=message,
        self_test={
            "status": "failed",
            "device": None,
            "duration_ms": duration_ms,
            "message": message,
            "detail": detail[-MAX_DETAIL:] if detail else None,
        },
    )
    return raw


def _tail(text: str | bytes | None, limit: int = MAX_DETAIL) -> str | None:
    if text is None:
        return None
    if isinstance(text, bytes):
        text = text.decode("utf-8", "replace")
    text = text.strip()
    if not text:
        return None
    return text if len(text) <= limit else "…" + text[-(limit - 1) :]


def child_env(
    state_dir: Path, self_test: bool | None, base: Mapping[str, str] | None = None
) -> dict[str, str]:
    """子进程（环境检测、模型转换）的环境变量。self_test 为 None 时不设置 TM_SELF_TEST（转换子进程不需要）。"""
    env = dict(os.environ if base is None else base)
    env["YOLO_OFFLINE"] = "1"
    env["YOLO_CONFIG_DIR"] = str(state_dir / "ultralytics")
    env.setdefault("YOLO_VERBOSE", "False")
    env["PYTHONUNBUFFERED"] = "1"
    if self_test is not None:
        env["TM_SELF_TEST"] = "true" if self_test else "false"
    # CUDA 默认把最快的显卡排在前面；按 PCI 总线顺序编号才与 NVML / nvidia-smi（页面上的 N 号显卡）一致。
    env.setdefault("CUDA_DEVICE_ORDER", "PCI_BUS_ID")
    # 从源码目录运行（python -m trainmaster_trainer）时让子进程也能导入本包；
    # 已安装到 site-packages 时不需要，也不要把 site-packages 放进 PYTHONPATH（会排在标准库之前）。
    pkg_parent = str(Path(__file__).resolve().parent.parent)
    if Path(pkg_parent).name not in ("site-packages", "dist-packages"):
        paths = [p for p in env.get("PYTHONPATH", "").split(os.pathsep) if p]
        if pkg_parent not in paths:
            env["PYTHONPATH"] = os.pathsep.join([*paths, pkg_parent])
    return env


def deployment_from(cfg: Config) -> issues_mod.Deployment:
    """由配置得到部署方式（修复建议中的命令按它生成）；裸机安装时解释器与安装目录就是当前的 Python，
    CUDA_VISIBLE_DEVICES（trainer.env 中设置，检测子进程与训练继承它）决定哪些显卡用于训练。"""
    return issues_mod.Deployment(
        server_url=cfg.server_url,
        service=cfg.trainer_service,
        mode=cfg.trainer_mode,
        pinned_variant=cfg.trainer_variant,
        python=sys.executable or "python3",
        prefix=sys.prefix,
        cuda_visible_devices=os.environ.get("CUDA_VISIBLE_DEVICES"),
    )


class EnvironmentDetector:
    """在子进程中检测 CUDA 环境（限时），合并硬件信息得到 Environment。可从其它线程调用 cancel()。"""

    def __init__(
        self,
        *,
        state_dir: Path,
        torch_variant: str | None,
        self_test: bool = True,
        timeout: float = 300.0,
        command: Sequence[str] | None = None,
        clock: Callable[[], float] = time.monotonic,
        deployment: issues_mod.Deployment | None = None,
    ) -> None:
        self.state_dir = state_dir
        self.torch_variant = torch_variant
        self.deployment = deployment
        self.self_test = self_test
        self.timeout = timeout
        self.command = list(command) if command else [sys.executable, "-m", CHILD_MODULE]
        if not self_test and not command:
            self.command.append("--no-self-test")
        self.clock = clock
        self._lock = threading.Lock()
        self._proc: subprocess.Popen[bytes] | None = None
        self._cancelled = False

    def cancel(self) -> None:
        with self._lock:
            self._cancelled = True
            proc = self._proc
        if proc is not None:
            _kill(proc)

    def run_child(self) -> dict[str, Any]:
        """运行子进程并返回清洗后的检测结果；任何失败都转换为 failure_raw，不抛出异常。"""
        try:
            (self.state_dir / "ultralytics").mkdir(parents=True, exist_ok=True)
        except OSError as exc:
            log.debug("cannot create ultralytics config dir: %s", exc)
        start = self.clock()
        try:
            with self._lock:
                if self._cancelled:
                    return failure_raw("环境检测已取消")
                self._proc = subprocess.Popen(
                    self.command,
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    env=child_env(self.state_dir, self.self_test),
                    start_new_session=True,  # 超时或停止时连同它启动的进程一起结束
                )
            proc = self._proc
        except OSError as exc:
            return failure_raw(f"无法启动环境检测进程：{exc}")

        try:
            stdout, stderr = proc.communicate(timeout=self.timeout)
        except BaseException as exc:
            if not isinstance(exc, subprocess.TimeoutExpired):
                _kill(proc)  # Ctrl+C 等：不留下孤儿子进程
                raise
            _kill(proc)
            try:
                _, stderr = proc.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                # 子进程的子进程仍占着管道（不在同一进程组）时不再等待输出。
                proc.kill()
                stderr = None
            return failure_raw(
                f"环境检测超时（{self.timeout:g} 秒内没有完成）",
                _tail(stderr),
                int((self.clock() - start) * 1000),
            )
        finally:
            with self._lock:
                self._proc = None
        elapsed = int((self.clock() - start) * 1000)

        if self._cancelled:
            return failure_raw("环境检测已取消", _tail(stderr), elapsed)
        if proc.returncode < 0:
            try:
                name = signal.Signals(-proc.returncode).name
            except ValueError:
                name = str(-proc.returncode)
            return failure_raw(
                f"环境检测进程被信号 {name} 终止（可能是内存不足或驱动崩溃）",
                _tail(stderr),
                elapsed,
            )
        if proc.returncode != 0:
            return failure_raw(
                f"环境检测进程异常退出（退出码 {proc.returncode}）", _tail(stderr), elapsed
            )
        lines = [line for line in stdout.decode("utf-8", "replace").splitlines() if line.strip()]
        try:
            raw = sanitize_raw(json.loads(lines[-1]))
        except (IndexError, ValueError) as exc:
            detail = _tail(
                "\n".join(filter(None, [f"stdout: {_tail(stdout, 500)}", _tail(stderr)]))
            )
            return failure_raw(f"环境检测进程的输出无法解析（{exc}）", detail, elapsed)
        if stderr:
            log.debug("environment probe stderr:\n%s", _tail(stderr))
        return raw

    def detect(
        self,
        hardware: Mapping[str, Any],
        *,
        device_nodes: Sequence[str] = (),
        pci_gpu_count: int | None = None,
        host_driver_version: str | None = None,
    ) -> dict[str, Any]:
        raw = self.run_child()
        return issues_mod.build_environment(
            hardware,
            raw,
            torch_variant=self.torch_variant,
            probed_at=utc_now(),
            device_nodes=device_nodes,
            pci_gpu_count=pci_gpu_count,
            host_driver_version=host_driver_version,
            deployment=self.deployment,
        )


def _kill(proc: subprocess.Popen[bytes]) -> None:
    """结束子进程所在的整个进程组：先 SIGTERM，2 秒后仍未退出则 SIGKILL。"""
    if proc.poll() is not None:
        return
    _signal_group(proc, signal.SIGTERM)
    try:
        proc.wait(timeout=2)
    except subprocess.TimeoutExpired:
        _signal_group(proc, signal.SIGKILL)
        with contextlib.suppress(subprocess.TimeoutExpired):
            proc.wait(timeout=2)


def _signal_group(proc: subprocess.Popen[bytes], sig: int) -> None:
    try:
        os.killpg(proc.pid, sig)
    except ProcessLookupError:
        pass
    except PermissionError:  # 不应发生（子进程是本进程启动的），退回只结束子进程本身
        with contextlib.suppress(ProcessLookupError):
            proc.send_signal(sig)


if __name__ == "__main__":
    sys.exit(child_main())
