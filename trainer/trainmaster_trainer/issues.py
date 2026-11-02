"""由硬件探测结果与环境检测子进程的结果生成 Environment：issues、status、accelerator、recommended_variant。

全部是纯函数（不读文件、不调用外部命令），便于逐条测试。issue 的 message / hint 是直接展示给用户的中文。
"""

from __future__ import annotations

import re
from collections.abc import Iterable, Mapping, Sequence
from typing import Any

from . import variants as variants_mod
from .variants import Selection, Variant

LEVEL_ORDER = {"error": 0, "warning": 1, "info": 2}
SHM_MIN_BYTES = 1 << 30
MAX_MESSAGE = 500
# nvidia-smi 不支持 compute_cap 的旧驱动无法得知计算能力：选择变体时按 7.5（Turing）及更新的显卡处理，
# 与部署脚本 deploy/scripts/setup-compute.sh 的做法相同。
ASSUMED_CC = "7.5"

REBUILD_HINT = (
    "在 .env 中设置 TM_TORCH_VARIANT={variant} 后重新构建镜像："
    "docker compose build trainer && docker compose up -d"
)
REINSTALL_HINT = "重新运行 trainer/scripts/install.sh --variant {variant}"
SETUP_HINT = (
    "在宿主机运行 make setup（会检查并可安装 NVIDIA Container Toolkit），然后 docker compose up -d"
)


def _truncate(text: str, limit: int = MAX_MESSAGE) -> str:
    text = text.strip()
    return text if len(text) <= limit else text[: limit - 1] + "…"


def issue(level: str, code: str, message: str, hint: str | None = None) -> dict[str, Any]:
    return {
        "level": level,
        "code": code,
        "message": _truncate(message),
        "hint": _truncate(hint) if hint else None,
    }


def sort_issues(items: Iterable[dict[str, Any]]) -> list[dict[str, Any]]:
    """按严重程度排序（error → warning → info），同一级别保持生成顺序。"""
    return sorted(items, key=lambda i: LEVEL_ORDER.get(i.get("level", "info"), 3))


def format_bytes(n: int) -> str:
    value = float(n)
    for unit in ("B", "KiB", "MiB", "GiB"):
        if value < 1024 or unit == "GiB":
            return f"{value:.0f} {unit}" if unit == "B" else f"{value:.1f} {unit}"
        value /= 1024
    return f"{n} B"  # 不会到达


def _first_line(text: str | None) -> str:
    return (text or "").strip().splitlines()[0] if (text or "").strip() else ""


def gpu_label(gpu: Mapping[str, Any]) -> str:
    cc = gpu.get("compute_capability")
    name = gpu.get("name") or "NVIDIA GPU"
    return f"{gpu.get('index', '?')} 号 {name}" + (f"（计算能力 {cc}）" if cc else "")


def recommend(hardware: Mapping[str, Any], table: Sequence[Variant] | None = None) -> Selection:
    """按本机驱动与显卡推荐变体（与部署脚本使用同一规则）。"""
    gpus = hardware.get("gpus") or []
    driver = (hardware.get("gpu_driver") or {}).get("driver_version")
    ccs = [g.get("compute_capability") or ASSUMED_CC for g in gpus]
    return variants_mod.select_variant(driver, ccs, hardware.get("arch") or None, table)


_ARCH_RE = re.compile(r"(sm|compute)_(\d+)([a-z]?)")


def arch_covers(device_cc: int, arch_list: Iterable[str]) -> bool:
    """torch.cuda.get_arch_list() 中的某个架构能否在该显卡上运行。

    sm_XX：同一主版本、次版本不小于它（sm_XXa 只能在完全相同的显卡上运行）；
    compute_XX（PTX）：可以由驱动即时编译到计算能力不低于它的显卡。
    """
    family: list[int] = []
    for arch in arch_list:
        m = _ARCH_RE.fullmatch(arch.strip())
        if not m:
            continue
        kind, number, suffix = m.group(1), int(m.group(2)), m.group(3)
        if kind == "compute":
            if device_cc >= number:
                return True
        elif suffix == "a":
            if device_cc == number:
                return True
        else:
            family.append(number)
    return variants_mod.cc_covered(device_cc, family)


def _hint(in_container: bool, variant: str) -> str:
    """改用指定变体的方法：容器中重新构建镜像，裸机上重新运行安装脚本。"""
    return (REBUILD_HINT if in_container else REINSTALL_HINT).format(variant=variant)


def _reinstall_hint(in_container: bool) -> str:
    if in_container:
        return "重新构建训练节点镜像：docker compose build trainer && docker compose up -d"
    return "重新运行 trainer/scripts/install.sh"


def _nvml_hint(error: str, in_container: bool) -> str:
    text = error.lower()
    if "mismatch" in text:
        return (
            "驱动已升级但内核模块仍是旧版本：重启机器（或重新加载 nvidia 内核模块）后再启动训练节点"
        )
    if "not loaded" in text:
        return "NVIDIA 内核模块没有加载：在宿主机运行 nvidia-smi 检查驱动，必要时重新安装驱动并重启"
    if "permission" in text:
        return "当前用户无权访问 /dev/nvidia* 设备，检查设备权限"
    if "not found" in text and in_container:
        return "容器中没有 NVIDIA 驱动库：" + SETUP_HINT
    if "not found" in text:
        return "没有找到 NVIDIA 驱动库（libnvidia-ml）：安装或重新安装 NVIDIA 驱动"
    return "在宿主机运行 nvidia-smi 检查驱动状态"


def _gpu_detection_issues(
    hardware: Mapping[str, Any],
    device_nodes: Sequence[str],
    pci_gpu_count: int | None,
) -> list[dict[str, Any]]:
    gpus = hardware.get("gpus") or []
    detection = hardware.get("gpu_detection") or {}
    error = detection.get("error")
    host_count = detection.get("host_gpu_count") or 0
    container = bool(hardware.get("in_container"))

    if gpus:
        if detection.get("method") == "nvidia-smi" and error:
            return [
                issue(
                    "warning",
                    "nvml_error",
                    f"NVML 初始化失败（{_first_line(error)}），已改用 nvidia-smi 读取显卡信息",
                    _nvml_hint(error, container),
                )
            ]
        return []

    if container and host_count > 0 and not device_nodes:
        return [
            issue(
                "warning",
                "gpu_not_passed_through",
                f"容器内能看到宿主机有 {host_count} 块 NVIDIA 显卡，但容器中没有可用的 GPU，将使用 CPU 训练",
                SETUP_HINT,
            )
        ]
    if error and (device_nodes or host_count > 0):
        return [
            issue(
                "warning",
                "nvml_error",
                f"检测到 NVIDIA 设备，但 NVML 初始化失败：{_first_line(error)}",
                _nvml_hint(error, container),
            )
        ]
    if pci_gpu_count:
        return [
            issue(
                "warning",
                "no_gpu",
                f"检测到 {pci_gpu_count} 块 NVIDIA 显卡（PCI 设备），但 NVIDIA 驱动没有加载，将使用 CPU 训练",
                "在宿主机安装 NVIDIA 驱动（推荐 580 或更新）并重启，然后运行 make setup && make up",
            )
        ]
    return [issue("info", "no_gpu", "未检测到 NVIDIA 显卡，将使用 CPU 训练（速度比 GPU 慢很多）")]


def build_environment(
    hardware: Mapping[str, Any],
    raw: Mapping[str, Any],
    *,
    torch_variant: str | None,
    probed_at: str,
    device_nodes: Sequence[str] = (),
    pci_gpu_count: int | None = None,
    table: Sequence[Variant] | None = None,
) -> dict[str, Any]:
    """合并硬件与子进程检测结果（raw，见 environment.collect）得到数据契约中的 Environment。

    device_nodes（容器内的 /dev/nvidia*）、pci_gpu_count（PCI 总线上的 NVIDIA 显卡数）只用于诊断，不上报。
    """
    gpus: list[Mapping[str, Any]] = list(hardware.get("gpus") or [])
    driver = (hardware.get("gpu_driver") or {}).get("driver_version")
    container = bool(hardware.get("in_container"))
    selection = recommend(hardware, table)
    rec = selection.variant

    torch_version = raw.get("torch_version")
    torch_cuda = raw.get("torch_cuda_version")
    arch_list = [a for a in (raw.get("torch_arch_list") or []) if isinstance(a, str)]
    cuda_available = bool(raw.get("cuda_available"))
    self_test = dict(raw.get("self_test") or {})
    installed = torch_variant or variants_mod.infer_variant(torch_version, torch_cuda, table)
    installed_info = variants_mod.get_variant(installed, table)

    issues: list[dict[str, Any]] = []
    issues += _gpu_detection_issues(hardware, device_nodes, pci_gpu_count)

    # 子进程整体失败（超时、崩溃、输出无法解析）时，torch 等信息未知，只报告自检失败。
    child_failed = bool(raw.get("probe_error"))
    torch_ok = torch_version is not None and not raw.get("torch_error")
    blocking = False  # 已安装的 PyTorch 无法在这些显卡上工作

    if not child_failed:
        if not torch_ok:
            issues.append(
                issue(
                    "error",
                    "torch_missing",
                    f"无法导入 PyTorch：{_first_line(raw.get('torch_error')) or '未安装'}",
                    _reinstall_hint(container),
                )
            )
        elif raw.get("torchvision_error") or raw.get("torchvision_version") is None:
            issues.append(
                issue(
                    "error",
                    "torch_missing",
                    f"无法导入 torchvision：{_first_line(raw.get('torchvision_error')) or '未安装'}",
                    _hint(container, installed or rec),
                )
            )
        if raw.get("ultralytics_error") or raw.get("ultralytics_version") is None:
            issues.append(
                issue(
                    "error",
                    "ultralytics_missing",
                    f"无法导入 Ultralytics（YOLO）：{_first_line(raw.get('ultralytics_error')) or '未安装'}",
                    _reinstall_hint(container),
                )
            )

    if not child_failed and torch_ok and gpus:
        if torch_cuda is None:
            blocking = True
            if rec == "cpu":
                code, hint = "torch_cpu_build", None
                driver_ok_any = driver and any(
                    v.min_driver and variants_mod.version_ge(driver, v.min_driver)
                    for v in (table or variants_mod.load_variants())
                )
                if (hardware.get("arch") or "") in ("x86_64", "amd64") and driver:
                    if not driver_ok_any:
                        code = "driver_too_old"
                        hint = "升级 NVIDIA 驱动（推荐 580 或更新），然后重新运行 make setup && make up"
                    else:
                        code = "arch_unsupported"
                issues.append(
                    issue(
                        "warning",
                        code,
                        f"检测到 {len(gpus)} 块 NVIDIA 显卡，但无法用于训练：{selection.reason}",
                        hint,
                    )
                )
            else:
                issues.append(
                    issue(
                        "error",
                        "torch_cpu_build",
                        f"检测到 {len(gpus)} 块 NVIDIA 显卡，但安装的 PyTorch {torch_version} 是 CPU 版，"
                        "无法使用 GPU 训练",
                        _hint(container, rec),
                    )
                )
        else:
            min_driver = variants_mod.min_driver_for_cuda(torch_cuda, table)
            driver_ok = not (
                driver and min_driver and not variants_mod.version_ge(driver, min_driver)
            )
            if not driver_ok:
                blocking = True
                if rec not in ("cpu", installed):
                    hint = f"升级驱动到 {min_driver} 或更新；或者" + _hint(container, rec)
                else:
                    hint = f"升级 NVIDIA 驱动到 {min_driver} 或更新（推荐 580 或更新）"
                issues.append(
                    issue(
                        "error",
                        "driver_too_old",
                        f"NVIDIA 驱动 {driver} 低于已安装的 PyTorch {torch_version}（CUDA {torch_cuda}）"
                        f"要求的最低版本 {min_driver}",
                        hint,
                    )
                )

            effective_archs = arch_list or [
                f"sm_{a}" for a in (installed_info.archs if installed_info else ())
            ]
            if effective_archs:
                bad = []
                for g in gpus:
                    cc = variants_mod.cc_to_int(str(g.get("compute_capability") or ""))
                    if cc is not None and not arch_covers(cc, effective_archs):
                        bad.append(g)
                if bad:
                    blocking = True
                    issues.append(
                        issue(
                            "error",
                            "arch_unsupported",
                            f"已安装的 PyTorch {torch_version} 不支持显卡 "
                            + "、".join(gpu_label(g) for g in bad)
                            + f"（编译的架构：{', '.join(effective_archs)}）",
                            _hint(container, rec) if rec not in ("cpu", installed) else None,
                        )
                    )

            if driver_ok and not cuda_available:
                blocking = True
                detail = _first_line(raw.get("cuda_error"))
                issues.append(
                    issue(
                        "error",
                        "cuda_unavailable",
                        "PyTorch 无法使用 CUDA（torch.cuda.is_available() 为 False）"
                        + (f"：{detail}" if detail else ""),
                        "确认容器以 GPU 方式启动（compose 中的 GPU 设备预留，或 docker run --gpus all）；"
                        "驱动刚升级过时先重启机器"
                        if container
                        else "在本机运行 nvidia-smi 检查驱动；驱动刚升级过时先重启机器",
                    )
                )

            rec_info = variants_mod.get_variant(rec, table)
            if (
                not blocking
                and installed
                and rec not in ("cpu", installed)
                and rec_info is not None
                and cuda_available
            ):
                issues.append(
                    issue(
                        "info",
                        "variant_upgrade_available",
                        f"推荐使用 {rec}（PyTorch {rec_info.torch}，CUDA {rec_info.cuda}），"
                        f"当前安装的是 {installed}，也能正常使用",
                        _hint(container, rec),
                    )
                )

    if self_test.get("status") == "failed":
        detail = (self_test.get("detail") or "").lower()
        if "out of memory" in detail:
            hint: str | None = "显存不足：显卡可能被其它程序占用，在宿主机运行 nvidia-smi 查看"
        elif "no kernel image" in detail and rec != "cpu":
            hint = _hint(container, rec)
        else:
            hint = (
                "查看节点详情中的错误信息，或运行 docker compose run --rm trainer selftest 重现"
                if container
                else "查看节点详情中的错误信息，或运行 python -m trainmaster_trainer selftest 重现"
            )
        message = self_test.get("message") or "自检失败"
        issues.append(issue("error", "self_test_failed", f"自检失败：{message}", hint))

    shm = hardware.get("shm_bytes")
    if gpus and isinstance(shm, int) and shm < SHM_MIN_BYTES:
        issues.append(
            issue(
                "warning",
                "shm_small",
                f"/dev/shm 只有 {format_bytes(shm)}，PyTorch 多进程加载数据（DataLoader workers）时可能出错",
                "在 .env 中调大 TM_TRAINER_SHM_SIZE（默认 8gb），然后 docker compose up -d"
                if container
                else "调大 /dev/shm，如 sudo mount -o remount,size=8G /dev/shm",
            )
        )

    mig = [g for g in gpus if g.get("mig_enabled")]
    if mig:
        issues.append(
            issue(
                "info",
                "mig_enabled",
                "显卡 "
                + "、".join(gpu_label(g) for g in mig)
                + " 开启了 MIG，PyTorch 只能使用分配给本节点的 MIG 实例",
                "需要整卡训练时，在宿主机用 nvidia-smi -i <序号> -mig 0 关闭 MIG（需要先停止使用该卡的程序）",
            )
        )

    issues = sort_issues(issues)
    accelerator = "cuda" if cuda_available and (raw.get("cuda_device_count") or 0) > 0 else "cpu"
    if any(i["level"] == "error" for i in issues):
        status = "error"
    elif accelerator == "cuda":
        status = "ready"
    else:
        status = "cpu_only"

    return {
        "status": status,
        "accelerator": accelerator,
        "python_version": raw.get("python_version"),
        "torch_version": torch_version,
        "torch_cuda_version": torch_cuda,
        "cudnn_version": raw.get("cudnn_version"),
        "torch_arch_list": arch_list,
        "torchvision_version": raw.get("torchvision_version"),
        "ultralytics_version": raw.get("ultralytics_version"),
        "torch_variant": torch_variant,
        "recommended_variant": rec,
        "cuda_available": cuda_available,
        "cuda_device_count": int(raw.get("cuda_device_count") or 0) if cuda_available else 0,
        "self_test": {
            "status": self_test.get("status") or "skipped",
            "device": self_test.get("device"),
            "duration_ms": self_test.get("duration_ms"),
            "message": self_test.get("message"),
            "detail": self_test.get("detail"),
        },
        "issues": issues,
        "probed_at": probed_at,
    }
