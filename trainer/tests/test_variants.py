"""variants.py：变体表解析与选择规则，并与 scripts/lib/torch-variant.sh 逐项对比。"""

from __future__ import annotations

import itertools
import shutil
import subprocess

import pytest
from fakes import TRAINER_DIR

from trainmaster_trainer import variants
from trainmaster_trainer.variants import (
    VariantTableError,
    cc_covered,
    cc_list,
    cc_to_int,
    infer_variant,
    min_driver_for_cuda,
    parse_table,
    select_variant,
    version_ge,
)

LIB = TRAINER_DIR / "scripts" / "lib" / "torch-variant.sh"
needs_bash = pytest.mark.skipif(shutil.which("bash") is None, reason="bash is not available")


# ---- 与 scripts/tests/test-torch-variant.sh 相同的用例 ----


def test_table_names_and_fields():
    table = variants.load_variants()
    assert [v.name for v in table] == ["cu130", "cu126", "cu128", "cu118", "cpu"]
    assert variants.get_variant("cu126").torch == "2.14.1"
    assert variants.get_variant("cu130").source == "pypi"
    assert variants.get_variant("cu126").archs == (50, 60, 70, 75, 80, 86, 90)
    assert variants.get_variant("nope") is None
    cpu = variants.get_variant("cpu")
    assert (cpu.cuda, cpu.min_driver, cpu.archs) == (None, None, ())


@pytest.mark.parametrize(
    ("a", "b", "expected"),
    [
        ("580", "580.65.06", False),
        ("580.65.06", "580", True),
        ("550.54.15", "525.60.13", True),
        ("525.60.13", "525.60.13", True),
        ("525.60.12", "525.60.13", False),
        ("1000", "580", True),
        ("470.256.02", "525.60.13", False),
    ],
)
def test_version_ge(a, b, expected):
    assert version_ge(a, b) is expected


@pytest.mark.parametrize(
    ("cc", "expected"),
    [("8.6", 86), ("12.0", 120), ("10.3", 103), ("abc", None), ("8", None), ("8.", None)],
)
def test_cc_to_int(cc, expected):
    assert cc_to_int(cc) == expected


@pytest.mark.parametrize(
    ("device", "archs", "expected"),
    [
        (89, "86,90", True),
        (90, "86,90", True),
        (100, "90", False),
        (103, "100", True),
        (121, "120", True),
        (61, "60", True),
        (87, "80,86", False),
        (87, "87", True),
        (75, "-", False),
        (37, "37,50", True),
        (35, "37", False),
    ],
)
def test_cc_covered(device, archs, expected):
    arch_list = () if archs == "-" else tuple(int(a) for a in archs.split(","))
    assert cc_covered(device, arch_list) is expected


SELECT_CASES = [
    # 驱动 ≥ 580、Turing 及更新：CUDA 13（PyPI）
    ("rtx4090 driver 580", "580.82.07", "8.9", "x86_64", "cu130", ()),
    ("h100+a100 driver 590", "590.44.01", "9.0 8.0", "x86_64", "cu130", ()),
    ("rtx5090 driver 580", "580.65.06", "12.0", "x86_64", "cu130", ()),
    ("b300 driver 580", "580.65.06", "10.3", "x86_64", "cu130", ()),
    # 驱动 525–579：CUDA 12.6；Blackwell 需要 CUDA 12.8
    ("rtx3090 driver 550", "550.54.15", "8.6", "x86_64", "cu126", ()),
    ("rtx4090 driver 570", "570.86.15", "8.9", "x86_64", "cu126", ()),
    ("rtx5090 driver 575", "575.57.08", "12.0", "x86_64", "cu128", ()),
    # 旧显卡：CUDA 13 不支持 Maxwell / Pascal / Volta
    ("p40 driver 580", "580.65.06", "6.1", "x86_64", "cu126", ()),
    ("v100 driver 590", "590.44.01", "7.0 7.0", "x86_64", "cu126", ()),
    ("gtx1080 + rtx3090 driver 560", "560.35.03", "6.1 8.6", "x86_64", "cu126", ()),
    # 驱动 450–524：CUDA 11.8
    ("t4 driver 470", "470.256.02", "7.5", "x86_64", "cu118", ()),
    ("k80 driver 470", "470.256.02", "3.7 3.7", "x86_64", "cu118", ()),
    # 无法同时支持：选支持最多显卡的变体
    ("p40 + rtx5090 driver 580", "580.65.06", "6.1 12.0 12.0", "x86_64", "cu130", (61,)),
    # 退回 CPU
    ("no gpu", "", "", "x86_64", "cpu", ()),
    ("gpu without driver", "", "8.6", "x86_64", "cpu", ()),
    ("driver 440", "440.100", "7.5", "x86_64", "cpu", ()),
    ("kepler 3.0", "470.256.02", "3.0", "x86_64", "cpu", ()),
    # ARM 服务器
    ("gh200 driver 580", "580.65.06", "9.0", "aarch64", "cu130", ()),
    ("arm driver 550", "550.54.15", "9.0", "aarch64", "cpu", ()),
    ("unknown machine", "580.65.06", "9.0", "riscv64", "cpu", ()),
]


@pytest.mark.parametrize(
    ("name", "driver", "ccs", "machine", "expected", "unsupported"),
    SELECT_CASES,
    ids=[c[0] for c in SELECT_CASES],
)
def test_select_variant(name, driver, ccs, machine, expected, unsupported):
    sel = select_variant(driver, ccs, machine)
    assert sel.variant == expected
    assert sel.unsupported == unsupported
    assert sel.reason


def test_cc_list():
    assert cc_list([86, 120]) == "8.6、12.0"


# ---- Python 侧额外的接口 ----


def test_select_variant_accepts_lists_and_ints():
    assert select_variant("580.65.06", ["8.9", 120], "x86_64").variant == "cu130"
    assert select_variant("580.65.06", None, "x86_64").variant == "cpu"


def test_select_variant_reasons_are_chinese_and_specific():
    assert "驱动 440.100 过旧" in select_variant("440.100", "7.5", "x86_64").reason
    assert "过旧，没有支持它的 PyTorch 版本" in select_variant("470.256.02", "3.0", "x86_64").reason
    assert "无法用于训练" in select_variant("580.65.06", "6.1 12.0", "x86_64").reason


def test_parse_table_rejects_bad_rows():
    with pytest.raises(VariantTableError):
        parse_table("cu130 2.14.1 0.29.1 pypi 13.0 580\n")
    with pytest.raises(VariantTableError):
        parse_table("cu130 2.14.1 0.29.1 conda 13.0 580 75\n")
    with pytest.raises(VariantTableError):
        parse_table("cu130 2.14.1 0.29.1 pypi 13.0 580 7x\n")
    with pytest.raises(VariantTableError):
        parse_table("# only comments\n\n")


def test_load_variants_from_path(tmp_path):
    path = tmp_path / "table.txt"
    path.write_text("# test\ncu999 9.9 9.9 pytorch 99.9 999 120\ncpu 9.9 9.9 pytorch - - -\n")
    table = variants.load_variants(path)
    assert [v.name for v in table] == ["cu999", "cpu"]
    assert select_variant("999", "12.0", "x86_64", table).variant == "cu999"
    assert select_variant("998", "12.0", "x86_64", table).variant == "cpu"


def test_package_data_is_readable():
    # 安装为 wheel 后通过 importlib.resources 读取（pyproject 的 package-data）。
    assert variants.load_variants()[0].name == "cu130"


@pytest.mark.parametrize(
    ("cuda", "expected"),
    [("13.0", "580"), ("12.6", "525.60.13"), ("12.4", "525.60.13"), ("11.8", "450.80.02"),
     ("10.2", None), (None, None)],
)  # fmt: skip
def test_min_driver_for_cuda(cuda, expected):
    assert min_driver_for_cuda(cuda) == expected


@pytest.mark.parametrize(
    ("version", "cuda", "expected"),
    [
        ("2.14.1+cu126", "12.6", "cu126"),
        ("2.14.1+cpu", None, "cpu"),
        ("2.14.1", "13.0", "cu130"),
        ("2.14.1", None, "cpu"),
        ("2.5.0+cu124", "12.4", None),
        (None, None, None),
    ],
)
def test_infer_variant(version, cuda, expected):
    assert infer_variant(version, cuda) == expected


# ---- 与 bash 实现逐项对比 ----

_BASH_SELECT = r"""
set -euo pipefail
. "$1"
while IFS='|' read -r driver ccs machine; do
	tm_select_variant "$driver" "$ccs" "$machine"
	printf '%s|%s|%s\n' "$TM_VARIANT" "$TM_VARIANT_UNSUPPORTED" "$TM_VARIANT_REASON"
done
"""

_BASH_PRIMITIVES = r"""
set -euo pipefail
. "$1"
while IFS='|' read -r kind a b; do
	case "$kind" in
	ge) if tm_version_ge "$a" "$b"; then echo 1; else echo 0; fi ;;
	# 与 tm_select_variant 一样在子 shell 中调用：8..6 这类输入会让 bash 的算术展开报错
	cc) (tm_cc_to_int "$a") 2>/dev/null || echo none ;;
	cov) if tm_cc_covered "$a" "$b"; then echo 1; else echo 0; fi ;;
	esac
done
"""


def _run_bash(script: str, lines: list[str]) -> list[str]:
    proc = subprocess.run(
        ["bash", "-c", script, "bash", str(LIB)],
        input="\n".join(lines) + "\n",
        capture_output=True,
        text=True,
        check=True,
        timeout=600,
    )
    out = proc.stdout.splitlines()
    assert len(out) == len(lines), proc.stderr
    return out


DRIVERS = [
    "",
    "440.100",
    "450.80.02",
    "470.256.02",
    "525.60.12",
    "525.60.13",
    "550.54.15",
    "575.57.08",
    "579.99",
    "580",
    "580.65.06",
    "590.44.01",
]
CC_SETS = [
    "",
    "3.0",
    "3.7",
    "5.0",
    "5.3",
    "6.1",
    "7.0",
    "7.2",
    "7.5",
    "8.0",
    "8.6",
    "8.7",
    "8.9",
    "9.0",
    "10.0",
    "10.3",
    "12.0",
    "12.1",
    "6.1 12.0 12.0",
    "3.7 8.6",
    "7.5 12.0",
    "5.3 8.7",
    "3.0 12.0",
    "86 89",
    "abc 8.6",
]


@needs_bash
def test_select_variant_matches_bash():
    cases = [(d, c, "x86_64") for d, c in itertools.product(DRIVERS, CC_SETS)]
    cases += [
        (d, c, m)
        for d, c, m in itertools.product(
            ["", "550.54.15", "580", "590.44.01"],
            ["", "7.5", "8.0", "9.0 12.0", "8.7"],
            ["aarch64", "arm64", "amd64", "riscv64", "ppc64le"],
        )
    ]
    out = _run_bash(_BASH_SELECT, ["|".join(c) for c in cases])
    mismatches = []
    for (driver, ccs, machine), line in zip(cases, out, strict=True):
        sel = select_variant(driver, ccs, machine)
        got = f"{sel.variant}|{' '.join(map(str, sel.unsupported))}|{sel.reason}"
        if got != line:
            mismatches.append(f"{driver!r} {ccs!r} {machine}: python={got!r} bash={line!r}")
    assert not mismatches, "\n".join(mismatches[:20])


@needs_bash
def test_primitives_match_bash():
    versions = ["580", "580.65.06", "525.60.13", "525.60.12", "1000", "470.256.02", "550", "0", ""]
    ccs = ["8.6", "12.0", "10.3", "8.65", "08.6", "8.6.1", "abc", "8", "8.", ".5", "8..6", "1x.2"]
    devices = [35, 37, 50, 53, 61, 62, 72, 75, 86, 87, 89, 90, 100, 103, 120, 121]
    arch_sets = ["86,90", "90", "100", "120", "37,50", "80,86", "87", "53,62,72,87", "-", ""]
    lines = [f"ge|{a}|{b}" for a, b in itertools.product(versions, versions)]
    lines += [f"cc|{c}|" for c in ccs]
    lines += [f"cov|{d}|{a}" for d, a in itertools.product(devices, arch_sets)]
    out = _run_bash(_BASH_PRIMITIVES, lines)
    for line, got in zip(lines, out, strict=True):
        kind, a, b = line.split("|")
        if kind == "ge":
            assert version_ge(a, b) is (got == "1"), line
        elif kind == "cc":
            expected = cc_to_int(a)
            assert (str(expected) if expected is not None else "none") == got, line
        else:
            archs = () if b in ("-", "") else tuple(int(x) for x in b.split(","))
            assert cc_covered(int(a), archs) is (got == "1"), line
