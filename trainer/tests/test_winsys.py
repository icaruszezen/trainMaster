"""winsys.py：Windows 的系统名称、版本、CPU 型号（注册表）与 PCI 显卡数（PowerShell CIM）。

读取注册表与运行 PowerShell 的函数都用替身，在任何系统上都能测试；windows_only 的测试读取真实的 Windows。
"""

from __future__ import annotations

import base64
import subprocess
from typing import Any

import pytest
from fakes import windows_only

from trainmaster_trainer import winsys

CV = winsys.CURRENT_VERSION_KEY
CPU = winsys.CPU_KEY


def reader(values: dict[str, Any], key: str = CV):
    def read(k: str, name: str) -> Any:
        return values.get(name) if k == key else None

    return read


def registry(cv: dict[str, Any], cpu: dict[str, Any] | None = None):
    cpu = cpu or {}

    def read(key: str, name: str) -> Any:
        if key == CV:
            return cv.get(name)
        if key == CPU:
            return cpu.get(name)
        return None

    return read


@pytest.mark.parametrize(
    ("values", "expected"),
    [
        # Windows 11 的 ProductName 仍是 Windows 10（微软为兼容保留）
        (
            {"ProductName": "Windows 10 Pro", "CurrentBuildNumber": "22631", "DisplayVersion": "23H2"},
            "Windows 11 Pro 23H2",
        ),
        (
            {"ProductName": "Windows 10 专业版", "CurrentBuild": "26100", "DisplayVersion": "24H2"},
            "Windows 11 专业版 24H2",
        ),
        (
            {"ProductName": "Windows 10 Home", "CurrentBuildNumber": "19045", "DisplayVersion": "22H2"},
            "Windows 10 Home 22H2",
        ),
        # 旧版 Windows 10 没有 DisplayVersion，用 ReleaseId
        (
            {"ProductName": "Windows 10 Enterprise LTSC 2019", "CurrentBuildNumber": "17763", "ReleaseId": "1809"},
            "Windows 10 Enterprise LTSC 2019 1809",
        ),
        # Windows Server 不改名
        (
            {"ProductName": "Windows Server 2022 Datacenter", "CurrentBuildNumber": "20348", "DisplayVersion": "21H2"},
            "Windows Server 2022 Datacenter 21H2",
        ),
        # 版本号已经在名称中时不重复；多余的空白合并
        ({"ProductName": "  Windows 10   Pro 22H2 ", "DisplayVersion": "22H2"}, "Windows 10 Pro 22H2"),
        ({"ProductName": ""}, None),
        ({}, None),
        ({"ProductName": 42}, None),
    ],
)  # fmt: skip
def test_os_name(values, expected):
    assert winsys.os_name(reader(values)) == expected


def test_os_version():
    values = {
        "CurrentMajorVersionNumber": 10,
        "CurrentMinorVersionNumber": 0,
        "CurrentBuildNumber": "22631",
        "UBR": 4317,
    }
    assert winsys.os_version(reader(values)) == "10.0.22631.4317"
    values.pop("UBR")
    assert winsys.os_version(reader(values)) == "10.0.22631"


def test_os_version_falls_back_to_platform(monkeypatch):
    monkeypatch.setattr(winsys.platform, "version", lambda: "10.0.17763")
    assert winsys.os_version(reader({"CurrentBuildNumber": "17763"})) == "10.0.17763"
    monkeypatch.setattr(winsys.platform, "version", lambda: "")
    assert winsys.os_version(reader({})) is None


def test_cpu_model_and_mhz(monkeypatch):
    read = registry(
        {}, {"ProcessorNameString": "AMD Ryzen 9 7950X 16-Core Processor    ", "~MHz": 4500}
    )
    assert winsys.cpu_model(read) == "AMD Ryzen 9 7950X 16-Core Processor"
    assert winsys.cpu_mhz(read) == 4500.0
    monkeypatch.setattr(
        winsys.platform, "processor", lambda: "Intel64 Family 6 Model 183 Stepping 1"
    )
    assert winsys.cpu_model(registry({})) == "Intel64 Family 6 Model 183 Stepping 1"
    monkeypatch.setattr(winsys.platform, "processor", lambda: "")
    assert winsys.cpu_model(registry({})) is None
    assert winsys.cpu_mhz(registry({}, {"~MHz": 0})) is None
    assert winsys.cpu_mhz(registry({}, {"~MHz": True})) is None


def test_int_parsing():
    assert winsys._int(5) == 5
    assert winsys._int(" 7 ") == 7
    assert winsys._int(True) is None
    assert winsys._int("x") is None
    assert winsys._int(None) is None


# ---- PCI 显卡数 ----


def fake_run(
    returncode: int = 0, stdout: str = "", stderr: str = "", error: Exception | None = None
):
    calls: list[tuple[list[str], dict[str, Any]]] = []

    def run(cmd, **kwargs):
        calls.append((cmd, kwargs))
        if error is not None:
            raise error
        return subprocess.CompletedProcess(cmd, returncode, stdout, stderr)

    run.calls = calls  # type: ignore[attr-defined]
    return run


def test_pci_gpu_count_runs_encoded_powershell_script():
    run = fake_run(stdout="2\r\n")
    assert winsys.pci_gpu_count(runner=run, timeout=5) == 2
    cmd, kwargs = run.calls[0]  # type: ignore[attr-defined]
    assert cmd[1:4] == ["-NoProfile", "-NonInteractive", "-EncodedCommand"]
    assert base64.b64decode(cmd[4]).decode("utf-16-le") == winsys.PCI_GPU_SCRIPT
    assert kwargs["timeout"] == 5 and kwargs["check"] is False and kwargs["capture_output"]


def test_pci_gpu_count_script_filters_nvidia_display_devices():
    # PCI 厂商 10DE（NVIDIA），类别 0300（VGA）或 0302（3D 控制器）；不包括 NVIDIA 的声卡、USB 控制器
    script = winsys.PCI_GPU_SCRIPT
    assert "PCI\\\\VEN_10DE%" in script
    assert "^PCI\\\\CC_030[02]" in script
    assert "-ErrorAction Stop" in script


@pytest.mark.parametrize(
    ("run", "expected"),
    [
        (fake_run(stdout="0\n"), 0),
        (fake_run(stdout="warning: x\n3\n"), 3),
        (fake_run(returncode=1, stderr="Get-CimInstance : Access denied"), None),
        (fake_run(stdout=""), None),
        (fake_run(stdout="abc\n"), None),
        (fake_run(error=FileNotFoundError(2, "powershell")), None),
        (fake_run(error=subprocess.TimeoutExpired("powershell", 60)), None),
    ],
)
def test_pci_gpu_count_failures(run, expected):
    assert winsys.pci_gpu_count(runner=run) == expected


def test_powershell_path(tmp_path, monkeypatch):
    exe = tmp_path / "System32" / "WindowsPowerShell" / "v1.0" / "powershell.exe"
    monkeypatch.setenv("SYSTEMROOT", str(tmp_path))
    assert winsys.powershell() == "powershell"
    exe.parent.mkdir(parents=True)
    exe.write_text("")
    assert winsys.powershell() == str(exe)


# ---- 真实的 Windows ----


@windows_only
def test_real_windows_registry():
    name = winsys.os_name()
    assert name and name.startswith("Windows")
    version = winsys.os_version()
    assert version and version.startswith("10.0.")
    assert winsys.cpu_model()


@windows_only
def test_real_windows_pci_gpu_count():
    # CI 的虚拟机没有 NVIDIA 显卡：查询成功，结果为 0
    count = winsys.pci_gpu_count()
    assert isinstance(count, int) and count >= 0
