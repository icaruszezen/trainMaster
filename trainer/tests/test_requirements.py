"""依赖清单：版本固定，运行与开发依赖一致，与 pyproject 的下限及镜像约定相符。"""

from __future__ import annotations

import re
from pathlib import Path

import pytest
from fakes import TRAINER_DIR

try:
    import tomllib
except ImportError:  # Python 3.10 没有 tomllib：跳过读取 pyproject 的检查
    tomllib = None

needs_tomllib = pytest.mark.skipif(tomllib is None, reason="需要 Python 3.11+ 的 tomllib")

_REQ = re.compile(r"^([A-Za-z0-9][A-Za-z0-9._-]*)==([0-9][0-9A-Za-z.+!-]*)$")


def pins(path: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        m = _REQ.fullmatch(line)
        assert m, f"{path.name}: 依赖必须固定版本（name==version）：{raw!r}"
        name = m.group(1).lower().replace("_", "-")
        assert name not in result, f"{path.name}: {name} 重复"
        result[name] = m.group(2)
    return result


def _version_tuple(text: str) -> tuple[int, ...]:
    return tuple(int(p) for p in re.findall(r"\d+", text))


def test_runtime_requirements():
    runtime = pins(TRAINER_DIR / "requirements.txt")
    assert set(runtime) == {"ultralytics", "nvidia-ml-py", "psutil"}
    assert runtime["ultralytics"] == "8.4.198"
    # torch / torchvision 由 install-torch.sh 按变体安装，不能出现在这里
    assert "torch" not in runtime and "torchvision" not in runtime


def test_dev_requirements_match_runtime():
    runtime = pins(TRAINER_DIR / "requirements.txt")
    dev = pins(TRAINER_DIR / "requirements-dev.txt")
    assert {"pytest", "ruff"} <= set(dev)
    assert "torch" not in dev and "ultralytics" not in dev  # 测试不安装 torch
    for name in ("nvidia-ml-py", "psutil"):
        assert dev[name] == runtime[name], name


@needs_tomllib
def test_pyproject_dependencies_cover_pins():
    data = tomllib.loads((TRAINER_DIR / "pyproject.toml").read_text(encoding="utf-8"))
    project = data["project"]
    assert project["requires-python"] == ">=3.10"
    runtime = pins(TRAINER_DIR / "requirements.txt")
    lower: dict[str, str] = {}
    for dep in project["dependencies"]:
        m = re.fullmatch(r"([A-Za-z0-9._-]+)>=([0-9.]+)", dep)
        assert m, dep
        lower[m.group(1).lower()] = m.group(2)
    # 常驻进程只依赖 nvidia-ml-py 与 psutil
    assert set(lower) == {"nvidia-ml-py", "psutil"}
    for name, minimum in lower.items():
        assert _version_tuple(runtime[name]) >= _version_tuple(minimum), name


@needs_tomllib
def test_variant_table_is_packaged():
    data = tomllib.loads((TRAINER_DIR / "pyproject.toml").read_text(encoding="utf-8"))
    package_data = data["tool"]["setuptools"]["package-data"]["trainmaster_trainer"]
    assert "torch_variants.txt" in package_data
    assert (TRAINER_DIR / "trainmaster_trainer" / "torch_variants.txt").is_file()
