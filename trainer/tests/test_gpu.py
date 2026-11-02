"""gpu.py：NVML 路径（假 pynvml 模块）、nvidia-smi 回退解析、/proc 与 /sys 计数。"""

from __future__ import annotations

import subprocess
from pathlib import Path

import pynvml
import pytest
from fakes import (
    GPU_IS_LOST,
    LIBRARY_MISMATCH,
    NOT_SUPPORTED,
    Err,
    make_nvml,
    rtx4090,
    write_files,
)

from trainmaster_trainer import gpu

# ---- NVML ----


def test_nvml_probe_reads_driver_and_gpus():
    nvml = make_nvml(
        [rtx4090(), rtx4090(name=b"NVIDIA A100-SXM4-80GB", cc=(8, 0), uuid=b"GPU-a100")]
    )
    result = gpu.probe(nvml_loader=lambda: nvml, find_smi=False, root="/nonexistent")
    assert result.driver == {
        "driver_version": "550.54.15",
        "cuda_driver_version": "12.4",
        "nvml_version": "12.550.54.15",
    }
    assert result.detection == {"method": "nvml", "error": None, "host_gpu_count": None}
    assert result.gpus[0] == {
        "index": 0,
        "uuid": "GPU-5c1d0000-0000-0000-0000-000000000000",
        "name": "NVIDIA GeForce RTX 4090",
        "memory_total_bytes": 25757220864,
        "compute_capability": "8.9",
        "pci_bus_id": "00000000:01:00.0",
        "power_limit_watts": 450.0,
        "mig_enabled": False,
    }
    # 旧版 nvidia-ml-py 返回 bytes
    assert result.gpus[1]["name"] == "NVIDIA A100-SXM4-80GB"
    assert result.gpus[1]["uuid"] == "GPU-a100"
    assert result.gpus[1]["compute_capability"] == "8.0"
    assert nvml.state["init"] == 1


def test_nvml_metrics():
    nvml = make_nvml([rtx4090(procs=[object(), object()])])
    result = gpu.probe(nvml_loader=lambda: nvml, find_smi=False)
    assert result.sampler.sample() == [
        {
            "index": 0,
            "utilization_percent": 87,
            "memory_used_bytes": 20401094656,
            "memory_total_bytes": 25757220864,
            "temperature_c": 71,
            "power_watts": 312.4,
            "fan_percent": 55,
            "process_count": 2,
        }
    ]
    result.sampler.close()
    result.sampler.close()  # 重复关闭无害
    assert nvml.state["shutdown"] == 1


def test_nvml_unsupported_fields_become_null():
    # 数据中心卡没有风扇（NVMLError_NotSupported），MIG 显卡不支持整卡利用率等。
    dev = rtx4090(
        fan=NOT_SUPPORTED,
        util=NOT_SUPPORTED,
        power=NOT_SUPPORTED,
        power_limit=NOT_SUPPORTED,
        temp=Err(pynvml.NVML_ERROR_UNKNOWN),
        procs=Err(pynvml.NVML_ERROR_FUNCTION_NOT_FOUND),
        uuid=NOT_SUPPORTED,
        bus_id=NOT_SUPPORTED,
        cc=NOT_SUPPORTED,
    )
    result = gpu.probe(nvml_loader=lambda: make_nvml([dev]), find_smi=False)
    g = result.gpus[0]
    assert g["uuid"] is None and g["pci_bus_id"] is None and g["compute_capability"] is None
    assert g["power_limit_watts"] is None
    m = result.sampler.sample()[0]
    assert m["fan_percent"] is None
    assert m["utilization_percent"] is None
    assert m["power_watts"] is None
    assert m["temperature_c"] is None
    assert m["process_count"] is None
    assert m["memory_used_bytes"] == 20401094656


def test_nvml_library_mismatch_only_loses_that_field():
    # R470 驱动的 libnvidia-ml 没有 _v3：进程数退回 _v2，其它指标照常读取。
    dev = rtx4090(procs=LIBRARY_MISMATCH, procs_v2=[object()])
    result = gpu.probe(nvml_loader=lambda: make_nvml([dev]), find_smi=False)
    m = result.sampler.sample()[0]
    assert m["process_count"] == 1
    assert m["utilization_percent"] == 87
    assert m["memory_used_bytes"] == 20401094656
    assert m["temperature_c"] == 71 and m["power_watts"] == 312.4 and m["fan_percent"] == 55

    dev["procs_v2"] = LIBRARY_MISMATCH
    m = result.sampler.sample()[0]
    assert m["process_count"] is None
    assert m["utilization_percent"] == 87


def test_real_pynvml_library_mismatch_is_caught(monkeypatch):
    """真实的 nvidia-ml-py：驱动库缺少 nvmlDeviceGetComputeRunningProcesses_v3 时不抛出异常。"""

    class OldLib:  # R470 的 libnvidia-ml：只有 _v2
        @staticmethod
        def nvmlDeviceGetComputeRunningProcesses_v2(handle, count, infos):  # noqa: N802
            return pynvml.NVML_SUCCESS  # 没有进程

    class RealNvml:
        def __getattr__(self, name):
            return getattr(pynvml, name)

        def nvmlInit(self):  # noqa: N802
            pass

        def nvmlShutdown(self):  # noqa: N802
            pass

    monkeypatch.setattr(pynvml, "nvmlLib", OldLib())
    monkeypatch.setattr(pynvml, "_nvmlGetFunctionPointer_cache", {})
    with pytest.raises(pynvml.NVMLLibraryMismatchError):
        pynvml.nvmlDeviceGetComputeRunningProcesses(object())
    session = gpu.NvmlSession(RealNvml())
    assert session.call("nvmlDeviceGetComputeRunningProcesses", object()) is None
    m = session.metrics(0, object())
    assert m["process_count"] == 0
    assert m["index"] == 0 and m["utilization_percent"] is None


def test_nvml_mig_modes():
    devices = [
        rtx4090(mig=(1, 1)),
        rtx4090(mig=(0, 1)),
        rtx4090(mig=NOT_SUPPORTED),
        rtx4090(mig=Err(pynvml.NVML_ERROR_UNKNOWN)),
    ]
    result = gpu.probe(nvml_loader=lambda: make_nvml(devices), find_smi=False)
    assert [g["mig_enabled"] for g in result.gpus] == [True, False, False, None]


def test_nvml_missing_functions_in_old_bindings():
    nvml = make_nvml(
        [rtx4090()],
        missing=(
            "nvmlDeviceGetMigMode",
            "nvmlSystemGetCudaDriverVersion_v2",
            "nvmlDeviceGetEnforcedPowerLimit",
        ),
    )
    result = gpu.probe(nvml_loader=lambda: nvml, find_smi=False)
    assert result.gpus[0]["mig_enabled"] is None
    assert result.gpus[0]["power_limit_watts"] == 450.0  # 退回 PowerManagementLimit
    assert result.driver["cuda_driver_version"] == "12.4"


def test_nvml_gpu_lost_is_skipped_and_reported():
    devices = [rtx4090(), rtx4090(handle_error=pynvml.NVML_ERROR_GPU_IS_LOST)]
    result = gpu.probe(nvml_loader=lambda: make_nvml(devices), find_smi=False)
    assert [g["index"] for g in result.gpus] == [0]
    assert result.detection["method"] == "nvml"
    assert result.detection["error"] == "GPU 1: GPU is lost"


def test_nvml_metrics_when_gpu_falls_off_bus():
    dev = rtx4090()
    result = gpu.probe(nvml_loader=lambda: make_nvml([dev]), find_smi=False)
    for key in ("util", "memory_total", "temp", "power", "fan", "procs"):
        dev[key] = GPU_IS_LOST
    m = result.sampler.sample()[0]
    assert m["index"] == 0
    assert all(m[k] is None for k in m if k != "index")


def test_nvml_without_gpus():
    result = gpu.probe(nvml_loader=lambda: make_nvml([]), find_smi=False)
    assert result.gpus == []
    assert result.detection["method"] == "nvml"
    assert result.driver["driver_version"] == "550.54.15"
    assert result.sampler.sample() == []


def test_nvml_driver_version_unavailable():
    result = gpu.probe(
        nvml_loader=lambda: make_nvml([rtx4090()], driver=Err(pynvml.NVML_ERROR_UNKNOWN)),
        find_smi=False,
    )
    assert result.driver is None
    assert len(result.gpus) == 1


def test_cuda_driver_version_formatting():
    assert gpu.format_cuda_driver_version(12040) == "12.4"
    assert gpu.format_cuda_driver_version(13000) == "13.0"
    assert gpu.format_cuda_driver_version(11080) == "11.8"
    assert gpu.format_cuda_driver_version(0) is None
    assert gpu.format_cuda_driver_version(None) is None


@pytest.mark.parametrize(
    ("code", "text"),
    [
        (pynvml.NVML_ERROR_LIBRARY_NOT_FOUND, "NVML Shared Library Not Found"),
        (pynvml.NVML_ERROR_DRIVER_NOT_LOADED, "Driver Not Loaded"),
        (pynvml.NVML_ERROR_LIB_RM_VERSION_MISMATCH, "RM has detected an NVML/RM version mismatch."),
    ],
)
def test_nvml_init_failure_without_smi(code, text, tmp_path):
    write_files(tmp_path, {"proc/driver/nvidia/gpus/0000:01:00.0/information": "Model: x\n"})
    result = gpu.probe(
        nvml_loader=lambda: make_nvml(init_error=code), find_smi=False, root=tmp_path
    )
    assert result.gpus == []
    assert result.driver is None
    assert result.detection == {"method": "none", "error": text, "host_gpu_count": 1}
    assert result.sampler.sample() == []


def test_nvml_count_failure_closes_session():
    nvml = make_nvml(count_error=pynvml.NVML_ERROR_UNKNOWN)
    result = gpu.probe(nvml_loader=lambda: nvml, find_smi=False)
    assert result.detection["method"] == "none"
    assert result.detection["error"] == "Unknown Error"
    assert nvml.state["shutdown"] == 1


def test_nvml_not_installed():
    def loader():
        raise ImportError("No module named 'pynvml'")

    result = gpu.probe(nvml_loader=loader, find_smi=False)
    assert result.detection["method"] == "none"
    assert "nvidia-ml-py is not installed" in result.detection["error"]


def test_real_pynvml_without_driver_reports_error():
    # 测试环境没有 NVIDIA 驱动：真实的 pynvml 报告找不到库，而不是抛出异常。
    result = gpu.probe(find_smi=False, root="/nonexistent")
    if result.detection["method"] == "nvml":
        pytest.skip("this machine has a working NVIDIA driver")
    assert result.detection["method"] == "none"
    assert result.detection["error"]


# ---- nvidia-smi ----

SMI_CSV = """\
0, GPU-aaaa, NVIDIA GeForce RTX 3090, 24576, 1024, 35, 45, 120.40, 350.00, 30, 8.6, 00000000:01:00.0
1, GPU-bbbb, Tesla T4, 15360, [N/A], [Not Supported], 40, [N/A], 70.00, [Not Supported], 7.5, 00000000:02:00.0
"""

SMI_HEADER = """\
Mon Nov  2 07:30:00 2026
+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 550.54.15              Driver Version: 550.54.15      CUDA Version: 12.4     |
|-----------------------------------------+------------------------+----------------------+
"""


def fake_runner(responses: dict[str, tuple[int, str]], calls: list[list[str]] | None = None):
    """按参数中的关键字返回 (退出码, 输出)；没有匹配时退出码 9。"""

    def run(cmd, **kwargs):
        if calls is not None:
            calls.append(cmd)
        args = " ".join(cmd[1:])
        for key, (code, out) in responses.items():
            if (key and key in args) or (key == "" and not args):
                return subprocess.CompletedProcess(cmd, code, stdout=out, stderr="")
        return subprocess.CompletedProcess(cmd, 9, stdout="", stderr="unexpected call")

    return run


def test_smi_value_placeholders():
    for v in (
        "[N/A]",
        "N/A",
        "[Not Supported]",
        "[Unknown Error]",
        "[Insufficient Permissions]",
        "",
        "  ",
    ):
        assert gpu.smi_value(v) is None
    assert gpu.smi_value(" 8.6 ") == "8.6"


def test_parse_smi_csv_and_static_metrics():
    rows = gpu.parse_smi_csv(SMI_CSV + "\n\n0, short row\n", gpu.SMI_FIELDS)
    assert len(rows) == 2
    t4 = gpu.smi_static(rows[1])
    assert t4 == {
        "index": 1,
        "uuid": "GPU-bbbb",
        "name": "Tesla T4",
        "memory_total_bytes": 15360 * gpu.MIB,
        "compute_capability": "7.5",
        "pci_bus_id": "00000000:02:00.0",
        "power_limit_watts": 70.0,
        "mig_enabled": None,
    }
    m = gpu.smi_metrics(rows[1], 0)
    assert m["memory_used_bytes"] is None
    assert m["utilization_percent"] is None
    assert m["power_watts"] is None
    assert m["fan_percent"] is None
    assert m["temperature_c"] == 40.0
    m0 = gpu.smi_metrics(rows[0], 1)
    assert m0["power_watts"] == 120.4
    assert m0["memory_used_bytes"] == 1024 * gpu.MIB
    assert m0["process_count"] == 1


def test_parse_smi_header():
    assert gpu.parse_smi_header(SMI_HEADER) == ("550.54.15", "12.4")
    assert gpu.parse_smi_header("no header here") == (None, None)


def test_probe_falls_back_to_nvidia_smi():
    runner = fake_runner(
        {
            "--query-gpu": (0, SMI_CSV),
            "--query-compute-apps": (0, "GPU-aaaa\nGPU-aaaa\n"),
            "": (0, SMI_HEADER),
        }
    )
    result = gpu.probe(
        nvml_loader=lambda: make_nvml(init_error=pynvml.NVML_ERROR_UNKNOWN),
        smi_path="nvidia-smi",
        runner=runner,
    )
    assert result.detection["method"] == "nvidia-smi"
    assert result.detection["error"] == "Unknown Error"
    assert result.driver == {
        "driver_version": "550.54.15",
        "cuda_driver_version": "12.4",
        "nvml_version": None,
    }
    assert [g["name"] for g in result.gpus] == ["NVIDIA GeForce RTX 3090", "Tesla T4"]
    metrics = result.sampler.sample()
    assert [m["process_count"] for m in metrics] == [2, 0]


def test_nvidia_smi_without_compute_cap_field():
    calls: list[list[str]] = []
    csv_without_cc = (
        "0, GPU-aaaa, Tesla K80, 11441, 0, 0, 30, 25.00, 149.00, [N/A], 00000000:04:00.0\n"
    )
    runner = fake_runner(
        {
            "compute_cap": (2, 'Field "compute_cap" is not a valid field to query.\n'),
            "--query-gpu": (0, csv_without_cc),
            "": (0, SMI_HEADER),
        },
        calls,
    )
    result = gpu.probe(
        nvml_loader=lambda: make_nvml(init_error=1), smi_path="nvidia-smi", runner=runner
    )
    assert result.gpus[0]["name"] == "Tesla K80"
    assert result.gpus[0]["compute_capability"] is None
    assert result.gpus[0]["memory_total_bytes"] == 11441 * gpu.MIB
    # 之后的指标查询直接使用去掉 compute_cap 的字段
    result.sampler.sample()
    assert "compute_cap" not in calls[-2][1]


def test_nvidia_smi_failure_combines_errors():
    runner = fake_runner(
        {"--query-gpu": (9, "NVIDIA-SMI has failed because it couldn't communicate\n")}
    )
    result = gpu.probe(
        nvml_loader=lambda: make_nvml(init_error=pynvml.NVML_ERROR_DRIVER_NOT_LOADED),
        smi_path="nvidia-smi",
        runner=runner,
    )
    assert result.detection["method"] == "none"
    assert result.detection["error"].startswith(
        "Driver Not Loaded; nvidia-smi: NVIDIA-SMI has failed"
    )


def test_nvidia_smi_not_runnable():
    def runner(cmd, **kwargs):
        raise FileNotFoundError(2, "No such file or directory")

    smi = gpu.NvidiaSmi("/missing/nvidia-smi", runner=runner)
    with pytest.raises(gpu.SmiError, match="cannot run"):
        smi.query()


def test_nvidia_smi_timeout():
    def runner(cmd, **kwargs):
        raise subprocess.TimeoutExpired(cmd, kwargs["timeout"])

    sampler = gpu.SmiSampler(gpu.NvidiaSmi("nvidia-smi", runner=runner))
    assert sampler.sample() == []


def test_find_nvidia_smi_override():
    assert gpu.find_nvidia_smi({"TM_NVIDIA_SMI": "/opt/fake-smi"}) == "/opt/fake-smi"


def test_fake_nvidia_smi_script(tmp_path: Path):
    """用真实的可执行脚本代替 nvidia-smi（与部署脚本测试的方式相同）。"""
    script = tmp_path / "nvidia-smi"
    script.write_text(
        "#!/bin/sh\n"
        'case "$1" in\n'
        f"  --query-gpu=*) cat <<'EOF'\n{SMI_CSV}EOF\n;;\n"
        "  --query-compute-apps=*) ;;\n"
        f"  *) cat <<'EOF'\n{SMI_HEADER}EOF\n;;\n"
        "esac\n"
    )
    script.chmod(0o755)
    result = gpu.probe(nvml_loader=lambda: make_nvml(init_error=1), smi_path=str(script))
    assert len(result.gpus) == 2
    assert result.driver["driver_version"] == "550.54.15"
    assert [m["process_count"] for m in result.sampler.sample()] == [0, 0]


# ---- /proc、/sys、/dev ----


def test_host_gpu_count(tmp_path: Path):
    assert gpu.host_gpu_count(tmp_path) is None
    (tmp_path / "proc/driver/nvidia/gpus").mkdir(parents=True)
    assert gpu.host_gpu_count(tmp_path) == 0
    for bus in ("0000:01:00.0", "0000:02:00.0"):
        write_files(tmp_path, {f"proc/driver/nvidia/gpus/{bus}/information": "Model: x\n"})
    write_files(tmp_path, {"proc/driver/nvidia/gpus/stray-file": ""})
    assert gpu.host_gpu_count(tmp_path) == 2


def test_pci_gpu_count(tmp_path: Path):
    assert gpu.pci_gpu_count(tmp_path) is None
    write_files(
        tmp_path,
        {
            "sys/bus/pci/devices/0000:01:00.0/vendor": "0x10de\n",
            "sys/bus/pci/devices/0000:01:00.0/class": "0x030000\n",
            "sys/bus/pci/devices/0000:01:00.1/vendor": "0x10de\n",
            "sys/bus/pci/devices/0000:01:00.1/class": "0x040300\n",  # 显卡上的 HDMI 音频
            "sys/bus/pci/devices/0000:02:00.0/vendor": "0x10de\n",
            "sys/bus/pci/devices/0000:02:00.0/class": "0x030200\n",
            "sys/bus/pci/devices/0000:03:00.0/vendor": "0x8086\n",
            "sys/bus/pci/devices/0000:03:00.0/class": "0x030000\n",
        },
    )
    (tmp_path / "sys/bus/pci/devices/0000:04:00.0").mkdir()
    assert gpu.pci_gpu_count(tmp_path) == 2


def test_nvidia_device_nodes(tmp_path: Path):
    assert gpu.nvidia_device_nodes(tmp_path) == []
    write_files(
        tmp_path, {"dev/nvidia1": "", "dev/nvidia0": "", "dev/nvidiactl": "", "dev/nvidia-uvm": ""}
    )
    assert gpu.nvidia_device_nodes(tmp_path) == ["nvidia0", "nvidia1"]
