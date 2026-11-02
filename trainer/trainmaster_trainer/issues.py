"""由硬件探测结果与环境检测子进程的结果生成 Environment：issues、status、accelerator、recommended_variant。

全部是纯函数（不读文件、不调用外部命令），便于逐条测试。issue 的 message / hint 是直接展示给用户的中文；
hint 中的命令用反引号包住（前端显示为代码，整条 hint 只有一条命令时可以复制），命令按节点的部署方式
（同一个 compose 中的平台、其它机器上的 compose、裸机安装）给出，见 Deployment。
"""

from __future__ import annotations

import re
import shlex
import sys
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any
from urllib.parse import urlsplit

from . import variants as variants_mod
from .variants import Selection, Variant

LEVEL_ORDER = {"error": 0, "warning": 1, "info": 2}
SHM_MIN_BYTES = 1 << 30
MAX_MESSAGE = 500
# nvidia-smi 不支持 compute_cap 的旧驱动无法得知计算能力：选择变体时按 7.5（Turing）及更新的显卡处理，
# 与部署脚本 deploy/scripts/setup-compute.sh 的做法相同。
ASSUMED_CC = "7.5"

# 与平台在同一个 compose 中时，训练节点通过服务名 app 访问平台（docker-compose.yml 中 TM_SERVER_URL 的默认值）。
LOCAL_SERVER_HOST = "app"
DEFAULT_SERVICE = "trainer"
DEFAULT_PREFIX = "/opt/trainmaster-trainer"
INSTALL_SCRIPT = "trainer/scripts/install.sh"


@dataclass(frozen=True)
class Deployment:
    """训练节点的部署方式（来自配置），决定修复建议给出哪些命令。

    是否在容器中由硬件探测（Hardware.in_container）决定：
    - 容器中、平台地址的主机是 app：与平台在同一个 compose 中，用 make up（它先运行 make setup）；
    - 容器中、平台在其它机器上：只启动本机的训练节点服务（make up 或不带服务名的 docker compose up -d
      会在这台机器上再启动一套 postgres 与 app），即 make setup 之后 docker compose up -d --build <服务>；
    - 不在容器中：裸机安装，重新运行 install.sh（它沿用已有配置文件中的平台地址、令牌等）。
    """

    server_url: str | None = None  # TM_SERVER_URL，没有设置时按 compose 的默认值 http://app:8080
    service: str | None = None  # TM_TRAINER_SERVICE：trainer / trainer-cpu，没有设置时按 trainer
    mode: str | None = None  # TM_TRAINER_MODE：make setup 保存的模式（auto / gpu / cpu / off）
    pinned_variant: str | None = None  # TM_TRAINER_VARIANT：make setup --variant 固定的变体
    python: str = field(default_factory=lambda: sys.executable or "python3")  # 裸机：解释器
    prefix: str = field(default_factory=lambda: sys.prefix)  # 裸机：install.sh 的安装目录


def _code(command: str) -> str:
    return f"`{command}`"


class Fixes:
    """按部署方式生成修复建议中的命令。"""

    def __init__(
        self,
        deployment: Deployment,
        in_container: bool,
        table: Sequence[Variant] | None = None,
    ) -> None:
        self.container = in_container
        self.service = deployment.service or DEFAULT_SERVICE
        try:
            host = urlsplit(deployment.server_url).hostname if deployment.server_url else None
        except ValueError:
            host = None
        self.local = deployment.server_url is None or host == LOCAL_SERVER_HOST
        self.mode = deployment.mode
        # make setup 只接受 CUDA 变体作为固定值，其它值按自动选择处理（与 setup-compute.sh 相同）。
        pinned = deployment.pinned_variant
        info = variants_mod.get_variant(pinned, table) if pinned else None
        self.pinned = pinned if info is not None and info.cuda else None
        self.python = deployment.python
        self.prefix = deployment.prefix.rstrip("/") or "/"

    # ---- Docker（docker compose）----

    def setup(self, args: str = "", *, to_gpu: bool = False) -> str:
        """重新运行 make setup（可带 SETUP_ARGS）并启动训练节点。

        to_gpu：改用 GPU 的操作。make setup 之后服务可能从 trainer-cpu 变为 trainer，其它机器上按它
        最后输出的服务名启动。
        """
        setup_args = f' SETUP_ARGS="{args}"' if args else ""
        if self.local:
            return _code(f"make up{setup_args}")
        if to_gpu:
            return (
                _code(f"make setup{setup_args}")
                + "，再按它最后输出的服务名启动（通常是 "
                + _code(f"docker compose up -d --build {DEFAULT_SERVICE}")
                + "）"
            )
        return (
            _code(f"make setup{setup_args}")
            + " 然后 "
            + _code(f"docker compose up -d --build {self.service}")
        )

    def restart(self) -> str:
        """修改 .env 中 compose 的设置（如 TM_TRAINER_SHM_SIZE）后重新创建训练节点容器。"""
        return _code("make up") if self.local else _code(f"docker compose up -d {self.service}")

    def toolkit(self) -> str:
        return (
            "安装并配置 NVIDIA Container Toolkit（需要 sudo），然后以 GPU 模式启动："
            + self.setup("--install-toolkit", to_gpu=True)
        )

    # ---- 裸机（install.sh）----

    def install(self, variant: str | None = None) -> str:
        """重新运行安装脚本（在 trainMaster 仓库目录中）：沿用已有配置文件中的平台地址、令牌等，
        已安装 systemd 服务时同时更新并重启它。"""
        args = ["sudo", INSTALL_SCRIPT]
        if variant:
            args += ["--variant", variant]
        if self.prefix != DEFAULT_PREFIX:
            args += ["--prefix", self.prefix]
        return "（在 trainMaster 仓库目录中运行）：" + _code(shlex.join(args))

    # ---- 按部署方式选择 ----

    def variant(self, variant: str) -> str:
        """改用指定的 PyTorch 变体。

        Docker：通过 make setup --variant 选择，它写入 TM_TRAINER_VARIANT，之后的 make setup / make up
        都沿用（直接修改 .env 中的 TM_TORCH_VARIANT 会在下一次 make setup 时被改回）。已经固定了其它
        变体时恢复自动选择：按驱动与显卡自动选择的结果就是这里推荐的变体。
        """
        if not self.container:
            return f"改用 PyTorch 变体 {variant} 重新安装" + self.install(variant)
        if self.pinned == variant:
            return f"重新构建镜像（PyTorch 变体 {variant}）：" + self.setup()
        if self.pinned:
            return (
                f"PyTorch 变体固定为 {self.pinned}（TM_TRAINER_VARIANT），恢复自动选择（{variant}）"
                "并重新构建镜像：" + self.setup("--variant auto")
            )
        return f"改用 PyTorch 变体 {variant} 重新构建镜像：" + self.setup(f"--variant {variant}")

    def reinstall(self) -> str:
        if self.container:
            return "重新构建训练节点镜像：" + self.setup()
        return "重新运行安装脚本" + self.install()

    def after_driver(self, *, toolkit: bool = False) -> str:
        """安装或升级 NVIDIA 驱动之后：重新检测并以 GPU 模式启动（Docker），或重新运行安装脚本（裸机）。"""
        if not self.container:
            return "重新运行安装脚本" + self.install()
        if toolkit:
            return "安装 NVIDIA Container Toolkit 并以 GPU 模式启动（需要 sudo）：" + self.setup(
                "--install-toolkit", to_gpu=True
            )
        return "重新检测并启动：" + self.setup(to_gpu=True)

    def selftest(self) -> str:
        if self.container:
            return _code(
                f"docker compose exec {self.service} python -m trainmaster_trainer selftest"
            )
        return _code(f"{shlex.quote(self.python)} -m trainmaster_trainer selftest")

    def exclude_gpus(self) -> str:
        """只把受支持的显卡交给训练节点（新旧显卡混用、没有同时支持全部显卡的 PyTorch 变体时）。"""
        if self.container:
            return (
                "只把受支持的显卡分配给训练节点：在 docker-compose.override.yml 中设置 device_ids"
                "（见 docs/deployment.md 第 10.8 节），然后运行 " + self.restart()
            )
        return (
            f"训练只使用受支持的显卡：在 {self.prefix}/trainer.env 中把 CUDA_VISIBLE_DEVICES "
            "设置为这些显卡的序号（如 0），然后运行 "
            + _code("sudo systemctl restart trainmaster-trainer")
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


def _nvml_hint(error: str, fixes: Fixes) -> str:
    text = error.lower()
    if "mismatch" in text:
        return (
            "驱动已升级但内核模块仍是旧版本：重启机器（或重新加载 nvidia 内核模块）后再启动训练节点"
        )
    if "not loaded" in text:
        return (
            "NVIDIA 内核模块没有加载：在宿主机运行 "
            + _code("nvidia-smi")
            + " 检查驱动，必要时重新安装驱动并重启"
        )
    if "permission" in text:
        return "当前用户无权访问 /dev/nvidia* 设备，检查设备权限"
    if "not found" in text and fixes.container:
        return "容器中没有 NVIDIA 驱动库：" + fixes.toolkit()
    if "not found" in text:
        return "没有找到 NVIDIA 驱动库（libnvidia-ml）：安装或重新安装 NVIDIA 驱动"
    return "在宿主机运行 " + _code("nvidia-smi") + " 检查驱动状态"


def _cpu_mode_issue(gpu_count: int, fixes: Fixes, *, driver_missing: bool) -> dict[str, Any]:
    """用户选择了 CPU 模式（TM_TRAINER_MODE=cpu）：本机的显卡不用于训练是预期的，只作提示。"""
    if driver_missing:
        hint = (
            "改用 GPU：在宿主机安装 NVIDIA 驱动（推荐 580 或更新）并重启，然后运行 "
            + fixes.setup("--mode auto --install-toolkit", to_gpu=True)
        )
    else:
        hint = "改用 GPU：" + fixes.setup("--mode auto", to_gpu=True)
    return issue(
        "info",
        "cpu_mode_selected",
        f"已选择 CPU 模式（TM_TRAINER_MODE=cpu），本机的 {gpu_count} 块 NVIDIA 显卡不用于训练",
        hint,
    )


def _gpu_detection_issues(
    hardware: Mapping[str, Any],
    device_nodes: Sequence[str],
    pci_gpu_count: int | None,
    fixes: Fixes,
) -> list[dict[str, Any]]:
    gpus = hardware.get("gpus") or []
    detection = hardware.get("gpu_detection") or {}
    error = detection.get("error")
    host_count = detection.get("host_gpu_count") or 0
    container = fixes.container
    cpu_mode = container and fixes.mode == "cpu"

    if gpus:
        if detection.get("method") == "nvidia-smi" and error:
            return [
                issue(
                    "warning",
                    "nvml_error",
                    f"NVML 初始化失败（{_first_line(error)}），已改用 nvidia-smi 读取显卡信息",
                    _nvml_hint(error, fixes),
                )
            ]
        return []

    if container and host_count > 0 and not device_nodes:
        if cpu_mode:
            return [_cpu_mode_issue(host_count, fixes, driver_missing=False)]
        return [
            issue(
                "warning",
                "gpu_not_passed_through",
                f"容器内能看到宿主机有 {host_count} 块 NVIDIA 显卡，但容器中没有可用的 GPU，将使用 CPU 训练",
                fixes.toolkit(),
            )
        ]
    if error and (device_nodes or host_count > 0):
        return [
            issue(
                "warning",
                "nvml_error",
                f"检测到 NVIDIA 设备，但 NVML 初始化失败：{_first_line(error)}",
                _nvml_hint(error, fixes),
            )
        ]
    if pci_gpu_count:
        if cpu_mode:
            return [_cpu_mode_issue(pci_gpu_count, fixes, driver_missing=True)]
        return [
            issue(
                "warning",
                "no_gpu",
                f"检测到 {pci_gpu_count} 块 NVIDIA 显卡（PCI 设备），但 NVIDIA 驱动没有加载，将使用 CPU 训练",
                "在宿主机安装 NVIDIA 驱动（推荐 580 或更新）并重启，然后"
                + fixes.after_driver(toolkit=True),
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
    deployment: Deployment | None = None,
) -> dict[str, Any]:
    """合并硬件与子进程检测结果（raw，见 environment.collect）得到数据契约中的 Environment。

    device_nodes（容器内的 /dev/nvidia*）、pci_gpu_count（PCI 总线上的 NVIDIA 显卡数）只用于诊断，不上报；
    deployment（部署方式）只用于生成 hint 中的命令。
    """
    gpus: list[Mapping[str, Any]] = list(hardware.get("gpus") or [])
    driver = (hardware.get("gpu_driver") or {}).get("driver_version")
    container = bool(hardware.get("in_container"))
    fixes = Fixes(deployment or Deployment(), container, table)
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
    issues += _gpu_detection_issues(hardware, device_nodes, pci_gpu_count, fixes)

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
                    fixes.reinstall(),
                )
            )
        elif raw.get("torchvision_error") or raw.get("torchvision_version") is None:
            issues.append(
                issue(
                    "error",
                    "torch_missing",
                    f"无法导入 torchvision：{_first_line(raw.get('torchvision_error')) or '未安装'}",
                    fixes.variant(installed or rec),
                )
            )
        if raw.get("ultralytics_error") or raw.get("ultralytics_version") is None:
            issues.append(
                issue(
                    "error",
                    "ultralytics_missing",
                    f"无法导入 Ultralytics（YOLO）：{_first_line(raw.get('ultralytics_error')) or '未安装'}",
                    fixes.reinstall(),
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
                        hint = (
                            "升级 NVIDIA 驱动（推荐 580 或更新）并重启，然后" + fixes.after_driver()
                        )
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
                        fixes.variant(rec),
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
                    hint = f"升级驱动到 {min_driver} 或更新；或者" + fixes.variant(rec)
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
                    message = (
                        f"已安装的 PyTorch {torch_version} 不支持显卡 "
                        + "、".join(gpu_label(g) for g in bad)
                        + f"（编译的架构：{', '.join(effective_archs)}）"
                    )
                    if rec not in ("cpu", installed):
                        blocking = True
                        issues.append(
                            issue("error", "arch_unsupported", message, fixes.variant(rec))
                        )
                    elif len(bad) < len(gpus):
                        # 没有同时支持全部显卡的变体，但其它显卡可以训练（自检会跳过不支持的显卡）。
                        issues.append(
                            issue(
                                "warning",
                                "arch_unsupported",
                                message + "，这些显卡不能用于训练",
                                fixes.exclude_gpus(),
                            )
                        )
                    else:
                        blocking = True
                        issues.append(issue("error", "arch_unsupported", message, None))

            if driver_ok and not cuda_available:
                blocking = True
                detail = _first_line(raw.get("cuda_error"))
                issues.append(
                    issue(
                        "error",
                        "cuda_unavailable",
                        "PyTorch 无法使用 CUDA（torch.cuda.is_available() 为 False）"
                        + (f"：{detail}" if detail else ""),
                        "确认容器以 GPU 方式启动（compose 中的 GPU 设备预留，或 "
                        + _code("docker run --gpus all")
                        + "）；驱动刚升级过时先重启机器"
                        if container
                        else "在本机运行 "
                        + _code("nvidia-smi")
                        + " 检查驱动；驱动刚升级过时先重启机器",
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
                        fixes.variant(rec),
                    )
                )

    if self_test.get("status") == "failed":
        detail = (self_test.get("detail") or "").lower()
        if "out of memory" in detail:
            hint: str | None = (
                "显存不足：显卡可能被其它程序占用，在宿主机运行 " + _code("nvidia-smi") + " 查看"
            )
        elif "no kernel image" in detail and rec not in ("cpu", installed):
            hint = fixes.variant(rec)
        else:
            hint = "查看节点详情中的错误信息，或运行 " + fixes.selftest() + " 重现"
        message = self_test.get("message") or "自检失败"
        issues.append(issue("error", "self_test_failed", f"自检失败：{message}", hint))

    shm = hardware.get("shm_bytes")
    if gpus and isinstance(shm, int) and shm < SHM_MIN_BYTES:
        issues.append(
            issue(
                "warning",
                "shm_small",
                f"/dev/shm 只有 {format_bytes(shm)}，PyTorch 多进程加载数据（DataLoader workers）时可能出错",
                "在 .env 中调大 TM_TRAINER_SHM_SIZE（默认 8gb），然后运行 " + fixes.restart()
                if container
                else "调大 /dev/shm，如 " + _code("sudo mount -o remount,size=8G /dev/shm"),
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
                "需要整卡训练时，在宿主机用 "
                + _code("nvidia-smi -i <序号> -mig 0")
                + " 关闭 MIG（需要先停止使用该卡的程序）",
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
