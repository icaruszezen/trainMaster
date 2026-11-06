"""hardware.py：/etc/os-release、cpuinfo、cgroup v1 / v2、/dev/shm、容器检测与 Metrics（临时目录夹具）。"""

from __future__ import annotations

import os
import sys
from pathlib import Path

import pytest
from fakes import write_files

from trainmaster_trainer import gpu, hardware


def test_os_release_pretty_name(tmp_path: Path):
    write_files(
        tmp_path,
        {
            "etc/os-release": 'NAME="Ubuntu"\nVERSION="22.04.4 LTS (Jammy Jellyfish)"\n'
            'PRETTY_NAME="Ubuntu 22.04.4 LTS"\n'
        },
    )
    assert hardware.os_pretty_name(tmp_path) == "Ubuntu 22.04.4 LTS"


def test_os_release_quotes_escapes_and_fallbacks(tmp_path: Path):
    write_files(
        tmp_path, {"usr/lib/os-release": "# comment\nNAME='Debian GNU/Linux'\nVERSION=12\n"}
    )
    assert hardware.os_pretty_name(tmp_path) == "Debian GNU/Linux 12"
    assert hardware.parse_os_release('PRETTY_NAME="A \\"quoted\\" \\$name"\nBROKEN\n') == {
        "PRETTY_NAME": 'A "quoted" $name'
    }
    assert hardware.os_pretty_name(tmp_path / "missing") is None


def test_cpu_model_x86_and_arm(tmp_path: Path):
    x86 = write_files(
        tmp_path / "x86",
        {
            "proc/cpuinfo": "processor\t: 0\nmodel\t\t: 106\nmodel name\t: Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz\n"
        },
    )
    assert hardware.cpu_model(x86) == "Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz"
    jetson = write_files(
        tmp_path / "jetson",
        {
            "proc/cpuinfo": "processor\t: 0\nCPU implementer\t: 0x41\nCPU part\t: 0xd42\n",
            "proc/device-tree/model": "NVIDIA Jetson AGX Orin Developer Kit\x00",
        },
    )
    assert hardware.cpu_model(jetson) == "NVIDIA Jetson AGX Orin Developer Kit"
    grace = write_files(
        tmp_path / "grace",
        {"proc/cpuinfo": "processor : 0\nCPU implementer : 0x41\nCPU part : 0xd4f\n"},
    )
    assert hardware.cpu_model(grace) == "ARM Neoverse-V2"
    unknown = write_files(
        tmp_path / "unknown", {"proc/cpuinfo": "processor : 0\nCPU implementer : 0x61\n"}
    )
    assert hardware.cpu_model(unknown) is None
    assert hardware.cpu_model(tmp_path / "missing") is None


def test_cpu_max_mhz_falls_back_to_sysfs(tmp_path: Path, monkeypatch):
    monkeypatch.setattr(hardware.psutil, "cpu_freq", lambda: None)
    assert hardware.cpu_max_mhz(tmp_path) is None
    write_files(tmp_path, {"sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq": "3100000\n"})
    assert hardware.cpu_max_mhz(tmp_path) == 3100.0

    def broken():
        raise OSError("no cpufreq")

    monkeypatch.setattr(hardware.psutil, "cpu_freq", broken)
    assert hardware.cpu_max_mhz(tmp_path) == 3100.0


def test_in_container(tmp_path: Path):
    assert hardware.in_container(tmp_path) is False
    write_files(tmp_path / "docker", {".dockerenv": ""})
    assert hardware.in_container(tmp_path / "docker") is True
    write_files(tmp_path / "podman", {"run/.containerenv": ""})
    assert hardware.in_container(tmp_path / "podman") is True
    write_files(tmp_path / "v1", {"proc/1/cgroup": "12:cpu,cpuacct:/docker/0123abcd\n"})
    assert hardware.in_container(tmp_path / "v1") is True
    write_files(tmp_path / "host", {"proc/1/cgroup": "0::/init.scope\n"})
    assert hardware.in_container(tmp_path / "host") is False


# ---- cgroup ----


def test_cgroup_v2_container_limits(tmp_path: Path):
    write_files(
        tmp_path,
        {
            "sys/fs/cgroup/cgroup.controllers": "cpu memory\n",
            "sys/fs/cgroup/cpu.max": "800000 100000\n",
            "sys/fs/cgroup/memory.max": "17179869184\n",
            "proc/self/cgroup": "0::/\n",
        },
    )
    assert hardware.cgroup_limits(tmp_path) == (8.0, 17179869184)


def test_cgroup_v2_unlimited(tmp_path: Path):
    write_files(
        tmp_path,
        {
            "sys/fs/cgroup/cgroup.controllers": "cpu memory\n",
            "sys/fs/cgroup/cpu.max": "max 100000\n",
            "sys/fs/cgroup/memory.max": "max\n",
        },
    )
    assert hardware.cgroup_limits(tmp_path) == (None, None)


def test_cgroup_v2_nested_systemd_service_takes_smallest(tmp_path: Path):
    write_files(
        tmp_path,
        {
            "sys/fs/cgroup/cgroup.controllers": "cpu memory\n",
            "sys/fs/cgroup/system.slice/cpu.max": "400000 100000\n",
            "sys/fs/cgroup/system.slice/memory.max": "max\n",
            "sys/fs/cgroup/system.slice/trainer.service/cpu.max": "150000 100000\n",
            "sys/fs/cgroup/system.slice/trainer.service/memory.max": "8589934592\n",
            "proc/self/cgroup": "0::/system.slice/trainer.service\n",
        },
    )
    assert hardware.cgroup_limits(tmp_path) == (1.5, 8589934592)


def test_cgroup_v2_path_not_visible_falls_back_to_root(tmp_path: Path):
    write_files(
        tmp_path,
        {
            "sys/fs/cgroup/cgroup.controllers": "cpu\n",
            "sys/fs/cgroup/cpu.max": "200000 100000\n",
            "proc/self/cgroup": "0::/docker/abc\n",
        },
    )
    assert hardware.cgroup_limits(tmp_path) == (2.0, None)


def test_cgroup_v2_garbage_values(tmp_path: Path):
    write_files(
        tmp_path,
        {
            "sys/fs/cgroup/cgroup.controllers": "cpu\n",
            "sys/fs/cgroup/cpu.max": "abc def\n",
            "sys/fs/cgroup/memory.max": "lots\n",
        },
    )
    assert hardware.cgroup_limits(tmp_path) == (None, None)


def test_cgroup_v1_limits(tmp_path: Path):
    write_files(
        tmp_path,
        {
            "sys/fs/cgroup/cpu,cpuacct/cpu.cfs_quota_us": "250000\n",
            "sys/fs/cgroup/cpu,cpuacct/cpu.cfs_period_us": "100000\n",
            "sys/fs/cgroup/memory/memory.limit_in_bytes": "4294967296\n",
        },
    )
    assert hardware.cgroup_limits(tmp_path) == (2.5, 4294967296)


def test_cgroup_v1_unlimited(tmp_path: Path):
    write_files(
        tmp_path,
        {
            "sys/fs/cgroup/cpu/cpu.cfs_quota_us": "-1\n",
            "sys/fs/cgroup/cpu/cpu.cfs_period_us": "100000\n",
            "sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n",
        },
    )
    assert hardware.cgroup_limits(tmp_path) == (None, None)


def test_no_cgroup(tmp_path: Path):
    assert hardware.cgroup_limits(tmp_path) == (None, None)


@pytest.mark.skipif(not hasattr(os, "statvfs"), reason="Windows 没有 statvfs 与 /dev/shm")
def test_shm_bytes(tmp_path: Path):
    assert hardware.shm_bytes(tmp_path) is None
    (tmp_path / "dev" / "shm").mkdir(parents=True)
    st = os.statvfs(tmp_path / "dev" / "shm")
    assert hardware.shm_bytes(tmp_path) == st.f_blocks * st.f_frsize


# ---- probe ----


def _fake_gpu_probe():
    return gpu.GpuProbe(
        driver={"driver_version": "580.65.06", "cuda_driver_version": "13.0", "nvml_version": None},
        gpus=[{"index": 0, "name": "NVIDIA GeForce RTX 4090", "compute_capability": "8.9"}],
        detection={"method": "nvml", "error": None, "host_gpu_count": 1},
    )


def test_probe_builds_contract_shape(tmp_path: Path, monkeypatch):
    write_files(
        tmp_path,
        {
            "etc/os-release": 'PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"\n',
            ".dockerenv": "",
            "proc/cpuinfo": "model name : Test CPU\n",
            "sys/fs/cgroup/cgroup.controllers": "cpu memory\n",
            "sys/fs/cgroup/cpu.max": "200000 100000\n",
            "sys/fs/cgroup/memory.max": "1073741824\n",
            "dev/nvidia0": "",
        },
    )
    (tmp_path / "dev" / "shm").mkdir()
    monkeypatch.setattr(hardware.psutil, "cpu_count", lambda logical=True: 16 if logical else 8)
    result = hardware.probe(tmp_path, gpu_probe=_fake_gpu_probe, windows=False)
    hw = result.hardware
    assert set(hw) == {
        "os",
        "kernel",
        "arch",
        "platform",
        "in_container",
        "cpu",
        "memory",
        "shm_bytes",
        "gpu_driver",
        "gpus",
        "gpu_detection",
    }
    assert hw["os"] == "Debian GNU/Linux 12 (bookworm)"
    assert hw["platform"] == hardware.platform_name(False)
    assert hw["in_container"] is True
    assert hw["cpu"]["model"] == "Test CPU"
    assert hw["cpu"]["logical_cores"] == 16 and hw["cpu"]["physical_cores"] == 8
    assert hw["cpu"]["limit_cores"] == 2.0
    assert hw["memory"]["limit_bytes"] == 1073741824
    assert hw["memory"]["total_bytes"] > 0
    assert hw["gpus"][0]["name"] == "NVIDIA GeForce RTX 4090"
    assert hw["gpu_detection"]["method"] == "nvml"
    assert result.device_nodes == ["nvidia0"]
    assert result.pci_gpu_count is None  # 已经检测到显卡，不需要再扫描 PCI
    assert result.host_driver_version is None  # 驱动版本来自 NVML


def test_probe_drops_limits_not_below_host(tmp_path: Path, monkeypatch):
    write_files(
        tmp_path,
        {
            "sys/fs/cgroup/cgroup.controllers": "cpu memory\n",
            "sys/fs/cgroup/cpu.max": "6400000 100000\n",
            "sys/fs/cgroup/memory.max": str(1 << 60) + "\n",
        },
    )
    monkeypatch.setattr(hardware.psutil, "cpu_count", lambda logical=True: 16 if logical else None)
    no_gpu = gpu.GpuProbe(
        driver=None, gpus=[], detection={"method": "none", "error": None, "host_gpu_count": None}
    )
    result = hardware.probe(tmp_path, gpu_probe=lambda: no_gpu, windows=False)
    assert result.hardware["cpu"]["limit_cores"] is None
    assert result.hardware["cpu"]["physical_cores"] is None
    assert result.hardware["memory"]["limit_bytes"] is None
    assert result.hardware["shm_bytes"] is None
    assert result.pci_gpu_count is None  # 没有 /sys/bus/pci
    assert result.host_driver_version is None  # 没有 /proc/driver/nvidia/version


def test_probe_reads_host_driver_when_no_gpu_is_visible(tmp_path: Path):
    # 容器中没有传入显卡：NVML 看不到显卡，宿主机的驱动版本只能从 /proc 读取（只用于诊断，不上报）。
    write_files(
        tmp_path,
        {
            ".dockerenv": "",
            "proc/driver/nvidia/version": "NVRM version: NVIDIA UNIX x86_64 Kernel Module  "
            "440.100  Fri May 29 08:45:51 UTC 2020\n",
            "proc/driver/nvidia/gpus/0000:01:00.0/information": "Model: x\n",
        },
    )
    no_gpu = gpu.GpuProbe(
        driver=None, gpus=[], detection={"method": "none", "error": None, "host_gpu_count": 1}
    )
    result = hardware.probe(tmp_path, gpu_probe=lambda: no_gpu, windows=False)
    assert result.host_driver_version == "440.100"
    assert result.hardware["gpu_driver"] is None
    assert "440.100" not in repr(result.hardware)


# ---- Windows（注册表与 PCI 显卡统计用替换函数，在 Linux 上也能测试）----

WINDOWS_REGISTRY = {
    ("SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion", "ProductName"): "Windows 10 Pro",
    ("SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion", "DisplayVersion"): "23H2",
    ("SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion", "CurrentBuildNumber"): "22631",
    ("SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion", "CurrentMajorVersionNumber"): 10,
    ("SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion", "CurrentMinorVersionNumber"): 0,
    ("SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion", "UBR"): 4317,
    ("HARDWARE\\DESCRIPTION\\System\\CentralProcessor\\0", "ProcessorNameString"): (
        "13th Gen Intel(R) Core(TM) i9-13900K  "
    ),
    ("HARDWARE\\DESCRIPTION\\System\\CentralProcessor\\0", "~MHz"): 2995,
}


def _registry(key, name):
    return WINDOWS_REGISTRY.get((key, name))


def test_probe_on_windows(tmp_path: Path, monkeypatch):
    # Linux 的文件（cgroup、/dev/shm、os-release）在 Windows 上不读取。
    write_files(
        tmp_path,
        {
            "etc/os-release": 'PRETTY_NAME="Debian"\n',
            ".dockerenv": "",
            "sys/fs/cgroup/cgroup.controllers": "cpu memory\n",
            "sys/fs/cgroup/cpu.max": "200000 100000\n",
        },
    )
    monkeypatch.setattr(hardware.psutil, "cpu_freq", lambda: None)
    counted = []
    result = hardware.probe(
        tmp_path,
        gpu_probe=_fake_gpu_probe,
        windows=True,
        read_registry=_registry,
        windows_pci_gpu_count=lambda: counted.append(1) or 1,
    )
    hw = result.hardware
    assert hw["platform"] == "windows"
    assert hw["os"] == "Windows 11 Pro 23H2"
    assert hw["kernel"] == "10.0.22631.4317"
    assert hw["in_container"] is False
    assert hw["shm_bytes"] is None
    assert hw["cpu"]["model"] == "13th Gen Intel(R) Core(TM) i9-13900K"
    assert hw["cpu"]["max_mhz"] == 2995.0
    assert hw["cpu"]["limit_cores"] is None
    assert hw["memory"]["limit_bytes"] is None
    assert result.device_nodes == []
    assert result.host_driver_version is None
    # 已经检测到显卡：不统计 PCI 设备
    assert result.pci_gpu_count is None and not counted


def test_probe_on_windows_counts_pci_gpus_without_driver(tmp_path: Path):
    no_gpu = gpu.GpuProbe(
        driver=None,
        gpus=[],
        detection={
            "method": "none",
            "error": "NVML Shared Library Not Found",
            "host_gpu_count": None,
        },
    )
    result = hardware.probe(
        tmp_path,
        gpu_probe=lambda: no_gpu,
        windows=True,
        read_registry=lambda key, name: None,
        windows_pci_gpu_count=lambda: 2,
    )
    assert result.pci_gpu_count == 2
    assert result.hardware["os"] is None  # 注册表读不到时为 null
    assert result.hardware["platform"] == "windows"


def test_platform_name():
    assert hardware.platform_name(True) == "windows"
    if sys.platform.startswith("linux"):
        assert hardware.platform_name(False) == "linux"


def test_probe_real_machine_smoke():
    # 不依赖夹具：在当前机器上探测，结构正确且不抛异常。
    result = hardware.probe()
    assert isinstance(result.hardware["cpu"]["logical_cores"], int)
    assert isinstance(result.hardware["gpus"], list)
    result.gpu.sampler.close()


# ---- Metrics ----


class FakeSampler:
    def __init__(self, metrics=None, error=None):
        self.metrics = metrics or []
        self.error = error

    def sample(self):
        if self.error:
            raise self.error
        return self.metrics

    def close(self):
        pass


def test_metrics_first_cpu_percent_is_null(tmp_path: Path):
    collector = hardware.MetricsCollector(tmp_path, FakeSampler([{"index": 0}]))
    first = collector.collect()
    assert first["cpu_percent"] is None
    assert first["disk_path"] == str(tmp_path)
    assert first["disk_total_bytes"] > 0 and first["disk_free_bytes"] >= 0
    assert (
        first["memory_used_bytes"] > 0 and first["memory_total_bytes"] >= first["memory_used_bytes"]
    )
    assert first["gpus"] == [{"index": 0}]
    assert len(first["load_average"]) == 3
    second = collector.collect()
    assert isinstance(second["cpu_percent"], float)
    assert 0 <= second["cpu_percent"] <= 100


def test_metrics_after_prime_and_failures(tmp_path: Path, monkeypatch):
    collector = hardware.MetricsCollector(
        tmp_path / "missing", FakeSampler(error=RuntimeError("GPU is lost"))
    )
    collector.prime()

    def no_loadavg():
        raise OSError("not supported")

    monkeypatch.setattr(hardware.os, "getloadavg", no_loadavg)
    m = collector.collect()
    assert m["cpu_percent"] is not None
    assert m["load_average"] is None
    assert m["disk_total_bytes"] is None and m["disk_free_bytes"] is None
    assert m["disk_path"] == str(tmp_path / "missing")
    assert m["gpus"] == []


def test_metrics_default_sampler(tmp_path: Path):
    assert hardware.MetricsCollector(tmp_path).collect()["gpus"] == []


def test_metrics_memory_failure(tmp_path: Path, monkeypatch):
    def broken():
        raise RuntimeError("no /proc/meminfo")

    monkeypatch.setattr(hardware.psutil, "virtual_memory", broken)
    m = hardware.MetricsCollector(tmp_path).collect()
    assert m["memory_used_bytes"] is None and m["memory_total_bytes"] is None
