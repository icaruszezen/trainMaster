"""PyTorch 构建变体表（torch_variants.txt）与自动选择规则。

规则与 scripts/lib/torch-variant.sh 完全一致（部署脚本、裸机安装脚本用 bash 版本，训练节点用这里的
Python 版本诊断已安装的 PyTorch 是否匹配本机驱动与显卡）；tests/test_variants.py 会对比两者的结果。

Windows（system="windows"）只有这里的 Python 版本（Windows 安装脚本 install.ps1 通过
``python -m trainmaster_trainer recommend --json`` 调用）：最低驱动取 min_driver_win 列（Windows 驱动的版本号
与 Linux 不同），ARM 版 Windows 没有 CUDA 版 PyTorch。bash 版本只用于 Linux。
"""

from __future__ import annotations

import functools
import platform
import re
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from importlib import resources
from pathlib import Path

# Jetson 集成显卡（5.3、6.2、7.2、8.7）只能运行为它单独编译的代码。
JETSON_CCS = frozenset({53, 62, 72, 87})
WINDOWS = "windows"
# platform.machine() 的取值：Linux 为 x86_64 / aarch64，macOS 为 arm64，Windows 为 AMD64 / ARM64（大写）。
X86_64 = ("x86_64", "amd64")
ARM64 = ("aarch64", "arm64")
TABLE_COLUMNS = 8


@dataclass(frozen=True)
class Variant:
    """变体表中的一行。cpu 变体的 cuda、min_driver、min_driver_win 为 None，archs 为空。"""

    name: str
    torch: str
    torchvision: str
    source: str
    cuda: str | None
    min_driver: str | None
    archs: tuple[int, ...]
    min_driver_win: str | None = None

    def driver_for(self, system: str | None = None) -> str | None:
        """该系统上的最低驱动版本：Windows 为 min_driver_win，其它系统为 min_driver。"""
        return self.min_driver_win if _is_windows(system) else self.min_driver


@dataclass(frozen=True)
class Selection:
    """select_variant 的结果，对应 bash 的 TM_VARIANT、TM_VARIANT_REASON、TM_VARIANT_UNSUPPORTED。"""

    variant: str
    reason: str
    unsupported: tuple[int, ...] = ()


class VariantTableError(ValueError):
    """变体表格式不正确。"""


def _dash_none(value: str) -> str | None:
    return None if value == "-" else value


def _is_windows(system: str | None) -> bool:
    return (system or "").lower() == WINDOWS


def normalize_machine(machine: str | None) -> str:
    """platform.machine() 的结果转小写（Windows 为 AMD64 / ARM64）；为空时取当前机器。"""
    return (machine or platform.machine() or "").lower()


def parse_table(text: str) -> list[Variant]:
    """解析变体表：跳过空行与 # 注释行，每行 8 列（空白分隔）。"""
    variants: list[Variant] = []
    for lineno, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        cols = stripped.split()
        if len(cols) != TABLE_COLUMNS:
            raise VariantTableError(
                f"line {lineno}: expected {TABLE_COLUMNS} columns, got {len(cols)}"
            )
        name, torch, torchvision, source, cuda, min_driver, archs, min_driver_win = cols
        if source not in ("pypi", "pytorch"):
            raise VariantTableError(f"line {lineno}: unknown source {source!r}")
        try:
            arch_list = () if archs == "-" else tuple(int(a) for a in archs.split(",") if a)
        except ValueError as exc:
            raise VariantTableError(f"line {lineno}: invalid archs {archs!r}") from exc
        variants.append(
            Variant(
                name=name,
                torch=torch,
                torchvision=torchvision,
                source=source,
                cuda=_dash_none(cuda),
                min_driver=_dash_none(min_driver),
                archs=arch_list,
                min_driver_win=_dash_none(min_driver_win),
            )
        )
    if not variants:
        raise VariantTableError("variant table is empty")
    return variants


@functools.lru_cache(maxsize=1)
def _default_variants() -> tuple[Variant, ...]:
    text = resources.files(__package__).joinpath("torch_variants.txt").read_text(encoding="utf-8")
    return tuple(parse_table(text))


def load_variants(path: str | Path | None = None) -> list[Variant]:
    """读取变体表；path 为空时读取包内的 torch_variants.txt。"""
    if path is None:
        return list(_default_variants())
    return parse_table(Path(path).read_text(encoding="utf-8"))


def get_variant(name: str | None, variants: Sequence[Variant] | None = None) -> Variant | None:
    if not name:
        return None
    for v in variants if variants is not None else _default_variants():
        if v.name == name:
            return v
    return None


_LEADING_INT = re.compile(r"\s*([+-]?\d+)")


def _awk_number(part: str) -> int:
    # awk 把字符串转换为数字时取开头的数字部分，没有数字时为 0。
    m = _LEADING_INT.match(part)
    return int(m.group(1)) if m else 0


def version_ge(a: str, b: str) -> bool:
    """点分数字版本 a >= b（缺少的部分按 0 处理，如 580 = 580.0.0），与 tm_version_ge 一致。"""
    xs = [_awk_number(p) for p in a.split(".")] if a != "" else []
    ys = [_awk_number(p) for p in b.split(".")] if b != "" else []
    for i in range(max(len(xs), len(ys))):
        xi = xs[i] if i < len(xs) else 0
        yi = ys[i] if i < len(ys) else 0
        if xi != yi:
            return xi > yi
    return True


_CC_GLOB = re.compile(r"[0-9].*\.[0-9].*", re.DOTALL)
_DIGITS = re.compile(r"[0-9]*")


def cc_to_int(cc: str) -> int | None:
    """把计算能力 8.6、12.0 转换为 86、120；无法解析时返回 None（与 tm_cc_to_int 一致）。"""
    if not _CC_GLOB.fullmatch(cc):
        return None
    major, _, rest = cc.partition(".")
    minor = rest.split(".", 1)[0]
    if not _DIGITS.fullmatch(major + minor) or not minor:
        return None
    return int(major) * 10 + int(minor[0])


def cc_covered(device: int, archs: Iterable[int]) -> bool:
    """显卡计算能力 device（如 89）能否运行 archs 中的某个构建（与 tm_cc_covered 一致）。

    同一主版本、次版本号不小于构建的次版本号即可运行（86 的构建也能在 8.9 上运行）；
    Jetson 显卡与为 Jetson 编译的构建只能精确匹配。
    """
    for arch in archs:
        if device == arch:
            return True
        if device in JETSON_CCS or arch in JETSON_CCS:
            continue
        if device // 10 == arch // 10 and device % 10 >= arch % 10:
            return True
    return False


def cc_list(ccs: Iterable[int]) -> str:
    """[86, 120] → "8.6、12.0"，用于提示文字（与 tm_cc_list 一致）。"""
    return "、".join(f"{cc // 10}.{cc % 10}" for cc in ccs)


def normalize_ccs(raw: Iterable[str | int] | str | None) -> list[int]:
    """把 "8.6 89" 或 ["8.6", 89] 这样的输入转换为整数列表，跳过无法解析的项。"""
    if raw is None:
        return []
    items: Iterable[str | int] = raw.split() if isinstance(raw, str) else raw
    out: list[int] = []
    for item in items:
        if isinstance(item, int) and not isinstance(item, bool):
            if item >= 0:
                out.append(item)
            continue
        token = str(item).strip()
        n = cc_to_int(token)
        if n is not None:
            out.append(n)
        elif token and _DIGITS.fullmatch(token):
            out.append(int(token))
    return out


def select_variant(
    driver: str | None,
    ccs: Iterable[str | int] | str | None,
    machine: str | None = None,
    variants: Sequence[Variant] | None = None,
    system: str | None = None,
) -> Selection:
    """按驱动版本与显卡计算能力选择 PyTorch 变体（Linux 上与 tm_select_variant 一致）。

    driver：NVIDIA 驱动版本（如 550.54.15），没有驱动时为 None 或 ""；
    ccs：各显卡的计算能力（"8.6" 或 86），没有显卡时为空；
    machine：platform.machine()（不区分大小写），默认取当前机器；
    system：Hardware.platform（"windows" 时按 Windows 的最低驱动选择），默认按 Linux。
    """
    table = list(variants) if variants is not None else list(_default_variants())
    cc_ints = normalize_ccs(ccs)
    machine = normalize_machine(machine)
    windows = _is_windows(system)

    if not cc_ints:
        return Selection("cpu", "未检测到 NVIDIA 显卡，使用 CPU 版 PyTorch")
    if not driver:
        return Selection(
            "cpu",
            "检测到 NVIDIA 显卡，但没有可用的驱动（nvidia-smi 无法运行），使用 CPU 版 PyTorch",
        )

    if windows and machine not in X86_64:
        return Selection(
            "cpu",
            f"Windows 上只有 x64（AMD64）电脑有 CUDA 版 PyTorch，当前为 {machine}，使用 CPU 版",
        )
    if machine in ARM64:
        # ARM 服务器（Grace Hopper、GB10 等）只有 CUDA 13 的通用构建（sbsa），需要 Ampere 及更新的显卡。
        cu130 = get_variant("cu130", table)
        min_driver = (cu130.min_driver if cu130 else None) or ""
        if all(cc >= 80 for cc in cc_ints) and cu130 and version_ge(driver, min_driver):
            return Selection(
                "cu130",
                f"ARM 服务器：驱动 {driver} 支持 CUDA 13，使用 PyPI 上的 CUDA 13 版 PyTorch",
            )
        return Selection(
            "cpu",
            f"ARM 服务器只能使用 CUDA 13 版 PyTorch（需要驱动 ≥ {min_driver} 且显卡为 Ampere 或更新），"
            "当前不满足，使用 CPU 版",
        )
    if machine not in X86_64:
        return Selection("cpu", f"不支持的平台 {machine}，使用 CPU 版 PyTorch")

    total = len(cc_ints)
    best: Variant | None = None
    best_cov = 0
    best_unsupported: tuple[int, ...] = ()
    driver_ok_any = False
    for v in table:
        if v.name == "cpu":
            continue
        min_driver = v.driver_for(system)
        if windows and not min_driver:
            continue  # 表中没有 Windows 的最低驱动：该变体没有 Windows 版
        if not version_ge(driver, min_driver or ""):
            continue
        driver_ok_any = True
        unsupported = tuple(cc for cc in cc_ints if not cc_covered(cc, v.archs))
        covered = total - len(unsupported)
        if covered == total:
            return Selection(
                v.name,
                f"驱动 {driver} 支持 CUDA {v.cuda or '-'}，PyTorch {v.torch}（{v.name}）"
                f"支持全部 {total} 块显卡",
            )
        if covered > best_cov:
            best, best_cov, best_unsupported = v, covered, unsupported

    if best is not None:
        return Selection(
            best.name,
            f"没有同时支持全部显卡的 PyTorch 版本，选择支持最多显卡的 {best.name}（{best_cov}/{total} 块）；"
            f"计算能力为 {cc_list(best_unsupported)} 的显卡无法用于训练",
            best_unsupported,
        )
    if not driver_ok_any:
        cu118 = get_variant("cu118", table)
        oldest = (cu118.driver_for(system) if cu118 else None) or ""
        return Selection(
            "cpu",
            f"驱动 {driver} 过旧：CUDA 版 PyTorch 至少需要驱动 {oldest}（推荐升级到 580 或更新），使用 CPU 版",
        )
    return Selection(
        "cpu",
        f"显卡（计算能力 {cc_list(cc_ints)}）过旧，没有支持它的 PyTorch 版本，使用 CPU 版",
    )


def lowest_cuda_driver(
    machine: str | None = None,
    variants: Sequence[Variant] | None = None,
    system: str | None = None,
) -> str | None:
    """本机架构上 CUDA 版 PyTorch 最低需要的驱动版本：驱动低于它时 select_variant 只能选择 cpu。

    x86_64：变体表中最低的最低驱动；ARM 服务器：cu130 的（只有它有 ARM 版，见 select_variant）；
    其它平台（以及 ARM 版 Windows）没有 CUDA 版 PyTorch，为 None。machine 默认取当前机器；
    system 为 "windows" 时取 Windows 的最低驱动（min_driver_win）。
    """
    table = list(variants) if variants is not None else list(_default_variants())
    machine = normalize_machine(machine)
    if machine in ARM64 and not _is_windows(system):
        cu130 = get_variant("cu130", table)
        return cu130.min_driver if cu130 else None
    if machine not in X86_64:
        return None
    lowest: str | None = None
    for v in table:
        min_driver = v.driver_for(system)
        if v.cuda and min_driver and (lowest is None or not version_ge(min_driver, lowest)):
            lowest = min_driver
    return lowest


def min_driver_for_cuda(
    cuda: str | None, variants: Sequence[Variant] | None = None, system: str | None = None
) -> str | None:
    """某个 CUDA 版本的 PyTorch 需要的最低驱动：先找 cuda 完全相同的变体，再找同一主版本的变体。
    system 为 "windows" 时取 Windows 的最低驱动。"""
    if not cuda:
        return None
    table = variants if variants is not None else _default_variants()
    for v in table:
        if v.cuda == cuda and v.driver_for(system):
            return v.driver_for(system)
    major = cuda.split(".", 1)[0]
    for v in table:
        if v.cuda and v.cuda.split(".", 1)[0] == major and v.driver_for(system):
            return v.driver_for(system)
    return None


def infer_variant(
    torch_version: str | None,
    torch_cuda_version: str | None,
    variants: Sequence[Variant] | None = None,
) -> str | None:
    """由已安装的 torch 推断变体：版本号后缀（2.14.1+cu126）优先，其次按 torch.version.cuda 匹配表中的 cuda。"""
    if not torch_version:
        return None
    table = variants if variants is not None else _default_variants()
    names = {v.name for v in table}
    _, _, local = torch_version.partition("+")
    if local in names:
        return local
    if torch_cuda_version is None:
        return "cpu" if "cpu" in names else None
    for v in table:
        if v.cuda == torch_cuda_version:
            return v.name
    return None
