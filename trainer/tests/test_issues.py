"""issues.py：每个 issue code、status / accelerator 判定与 recommended_variant。"""

from __future__ import annotations

from typing import Any

import pytest

from trainmaster_trainer import issues
from trainmaster_trainer.issues import arch_covers, build_environment, format_bytes

CU126_ARCHS = ["sm_50", "sm_60", "sm_70", "sm_75", "sm_80", "sm_86", "sm_90"]
CU130_ARCHS = ["sm_75", "sm_80", "sm_86", "sm_90", "sm_100", "sm_120"]
PROBED_AT = "2026-11-02T07:30:00Z"


def gpu(index: int = 0, name: str = "NVIDIA GeForce RTX 4090", cc: str | None = "8.9", **kw: Any):
    g = {
        "index": index,
        "uuid": f"GPU-{index}",
        "name": name,
        "memory_total_bytes": 25757220864,
        "compute_capability": cc,
        "pci_bus_id": f"00000000:0{index + 1}:00.0",
        "power_limit_watts": 450.0,
        "mig_enabled": False,
    }
    g.update(kw)
    return g


def hw(
    gpus: list[dict[str, Any]] | None = None,
    *,
    driver: str | None = "580.65.06",
    arch: str = "x86_64",
    container: bool = True,
    shm: int | None = 8 << 30,
    method: str | None = None,
    error: str | None = None,
    host_gpu_count: int | None = None,
) -> dict[str, Any]:
    gpus = gpus or []
    return {
        "os": "Debian GNU/Linux 12 (bookworm)",
        "kernel": "6.8.0",
        "arch": arch,
        "in_container": container,
        "cpu": {
            "model": "x",
            "physical_cores": 8,
            "logical_cores": 16,
            "max_mhz": None,
            "limit_cores": None,
        },
        "memory": {"total_bytes": 64 << 30, "limit_bytes": None},
        "shm_bytes": shm,
        "gpu_driver": {
            "driver_version": driver,
            "cuda_driver_version": "13.0",
            "nvml_version": None,
        }
        if driver and (gpus or method == "nvml")
        else None,
        "gpus": gpus,
        "gpu_detection": {
            "method": method or ("nvml" if gpus else "none"),
            "error": error,
            "host_gpu_count": host_gpu_count,
        },
    }


def raw(
    *,
    torch: str | None = "2.14.1",
    cuda: str | None = "13.0",
    archs: list[str] | None = None,
    available: bool = True,
    count: int = 1,
    torchvision: str | None = "0.29.1",
    ultralytics: str | None = "8.4.198",
    self_test: dict[str, Any] | None = None,
    **extra: Any,
) -> dict[str, Any]:
    r = {
        "python_version": "3.12.7",
        "torch_version": torch,
        "torch_cuda_version": cuda,
        "cudnn_version": "9.10.2" if cuda else None,
        "torch_arch_list": (CU130_ARCHS if archs is None else archs) if available else [],
        "torchvision_version": torchvision,
        "ultralytics_version": ultralytics,
        "cuda_available": available,
        "cuda_device_count": count if available else 0,
        "torch_error": None,
        "torchvision_error": None,
        "ultralytics_error": None,
        "cuda_error": None,
        "probe_error": None,
        "self_test": self_test
        or {
            "status": "passed",
            "device": "cuda:0" if available else "cpu",
            "duration_ms": 1830,
            "message": "YOLO11n 前向推理正常",
            "detail": None,
        },
    }
    r.update(extra)
    return r


def cpu_raw(**kw: Any) -> dict[str, Any]:
    return raw(torch="2.14.1+cpu", cuda=None, available=False, **kw)


def env_of(hardware, r, *, torch_variant: str | None = "cu130", **kw: Any) -> dict[str, Any]:
    return build_environment(hardware, r, torch_variant=torch_variant, probed_at=PROBED_AT, **kw)


def codes(env: dict[str, Any]) -> list[str]:
    return [i["code"] for i in env["issues"]]


def only(env: dict[str, Any], code: str) -> dict[str, Any]:
    found = [i for i in env["issues"] if i["code"] == code]
    assert len(found) == 1, env["issues"]
    return found[0]


# ---- 正常情况 ----


def test_ready_gpu_node():
    env = env_of(hw([gpu(0), gpu(1)]), raw(count=2))
    assert env["status"] == "ready"
    assert env["accelerator"] == "cuda"
    assert env["recommended_variant"] == "cu130"
    assert env["issues"] == []
    assert env["cuda_device_count"] == 2
    assert env["torch_variant"] == "cu130"
    assert env["probed_at"] == PROBED_AT
    assert env["self_test"]["status"] == "passed"


def test_environment_has_every_contract_field():
    env = env_of(hw(), cpu_raw(), torch_variant=None)
    assert set(env) == {
        "status",
        "accelerator",
        "python_version",
        "torch_version",
        "torch_cuda_version",
        "cudnn_version",
        "torch_arch_list",
        "torchvision_version",
        "ultralytics_version",
        "torch_variant",
        "recommended_variant",
        "cuda_available",
        "cuda_device_count",
        "self_test",
        "issues",
        "probed_at",
    }
    assert set(env["self_test"]) == {"status", "device", "duration_ms", "message", "detail"}
    assert env["torch_arch_list"] == []
    assert env["torch_variant"] is None


def test_no_gpu_is_cpu_only_with_info():
    env = env_of(hw(), cpu_raw(), torch_variant="cpu")
    assert env["status"] == "cpu_only"
    assert env["accelerator"] == "cpu"
    assert env["recommended_variant"] == "cpu"
    issue = only(env, "no_gpu")
    assert issue["level"] == "info"
    assert "CPU" in issue["message"]


def test_no_gpu_but_pci_devices_without_driver():
    env = env_of(hw(), cpu_raw(), pci_gpu_count=2)
    issue = only(env, "no_gpu")
    assert issue["level"] == "warning"
    assert "2 块" in issue["message"] and "驱动" in issue["message"]
    assert "make setup" in issue["hint"]


def test_cuda_build_without_gpu_is_cpu_only():
    # 镜像默认是 CUDA 版，部署在没有显卡的机器上时仍可用 CPU 训练。
    env = env_of(hw(), raw(available=False))
    assert env["status"] == "cpu_only"
    assert codes(env) == ["no_gpu"]


# ---- 显卡探测 ----


def test_gpu_not_passed_through():
    env = env_of(hw(error="NVML Shared Library Not Found", host_gpu_count=2), raw(available=False))
    issue = only(env, "gpu_not_passed_through")
    assert issue["level"] == "warning"
    assert "2 块" in issue["message"]
    assert "make setup" in issue["hint"] and "docker compose up -d" in issue["hint"]
    assert "no_gpu" not in codes(env)
    assert env["status"] == "cpu_only"


def test_gpu_not_passed_through_without_nvml_error():
    env = env_of(hw(method="nvml", host_gpu_count=1), raw(available=False))
    assert codes(env) == ["gpu_not_passed_through"]


def test_host_gpus_only_matter_in_container():
    env = env_of(hw(host_gpu_count=1, container=False), raw(available=False))
    assert codes(env) == ["no_gpu"]


@pytest.mark.parametrize(
    ("error", "hint_part"),
    [
        ("RM has detected an NVML/RM version mismatch.", "重启机器"),
        ("Driver Not Loaded", "内核模块没有加载"),
        ("Insufficient Permissions", "权限"),
        ("NVML Shared Library Not Found", "make setup"),
        ("Unknown Error", "nvidia-smi"),
    ],
)
def test_nvml_error_with_device_nodes(error, hint_part):
    env = env_of(hw(error=error, host_gpu_count=1), raw(available=False), device_nodes=["nvidia0"])
    issue = only(env, "nvml_error")
    assert issue["level"] == "warning"
    assert error in issue["message"]
    assert hint_part in issue["hint"]


def test_nvml_error_on_bare_metal():
    env = env_of(
        hw(error="NVML Shared Library Not Found", host_gpu_count=1, container=False),
        raw(available=False),
    )
    issue = only(env, "nvml_error")
    assert "安装或重新安装" in issue["hint"]


def test_nvml_error_with_nvidia_smi_fallback():
    env = env_of(hw([gpu()], method="nvidia-smi", error="Unknown Error"), raw())
    issue = only(env, "nvml_error")
    assert "nvidia-smi" in issue["message"]
    assert env["status"] == "ready"


# ---- PyTorch / Ultralytics ----


def test_torch_missing_skips_cuda_checks():
    env = env_of(
        hw([gpu()]),
        raw(
            torch=None,
            cuda=None,
            available=False,
            torchvision=None,
            torch_error="ModuleNotFoundError: torch",
        ),
    )
    issue = only(env, "torch_missing")
    assert issue["level"] == "error"
    assert "ModuleNotFoundError" in issue["message"]
    assert "docker compose build trainer" in issue["hint"]
    assert not {"torch_cpu_build", "cuda_unavailable", "driver_too_old"} & set(codes(env))
    assert env["status"] == "error"


def test_torch_missing_bare_metal_hint():
    env = env_of(
        hw(container=False), raw(torch=None, cuda=None, available=False), torch_variant=None
    )
    assert "install.sh" in only(env, "torch_missing")["hint"]


def test_torchvision_missing():
    env = env_of(hw(), cpu_raw(torchvision=None, torchvision_error="ImportError: libc10.so"))
    issue = only(env, "torch_missing")
    assert "torchvision" in issue["message"] and "libc10.so" in issue["message"]


def test_ultralytics_missing():
    env = env_of(hw(), cpu_raw(ultralytics=None, ultralytics_error="ImportError: libGL.so.1"))
    issue = only(env, "ultralytics_missing")
    assert issue["level"] == "error"
    assert "libGL.so.1" in issue["message"]
    assert env["status"] == "error"


def test_torch_cpu_build_with_gpu():
    env = env_of(hw([gpu()]), cpu_raw(), torch_variant="cpu")
    issue = only(env, "torch_cpu_build")
    assert issue["level"] == "error"
    assert "CPU 版" in issue["message"]
    assert "TM_TORCH_VARIANT=cu130" in issue["hint"]
    assert env["status"] == "error"
    assert env["accelerator"] == "cpu"


def test_torch_cpu_build_bare_metal_hint():
    env = env_of(hw([gpu()], container=False), cpu_raw(), torch_variant="cpu")
    assert "install.sh --variant cu130" in only(env, "torch_cpu_build")["hint"]


def test_cpu_build_when_driver_too_old_for_any_cuda_build():
    env = env_of(hw([gpu(cc="7.5")], driver="440.100"), cpu_raw(), torch_variant="cpu")
    issue = only(env, "driver_too_old")
    assert issue["level"] == "warning"
    assert "440.100" in issue["message"]
    assert "升级 NVIDIA 驱动" in issue["hint"]
    assert env["recommended_variant"] == "cpu"
    assert env["status"] == "cpu_only"


def test_cpu_build_when_gpu_too_old_for_any_build():
    env = env_of(
        hw([gpu(name="Tesla K10", cc="3.0")], driver="470.256.02"), cpu_raw(), torch_variant="cpu"
    )
    issue = only(env, "arch_unsupported")
    assert issue["level"] == "warning"
    assert "3.0" in issue["message"]
    assert env["status"] == "cpu_only"


def test_cpu_build_on_arm_without_cuda_option():
    env = env_of(
        hw([gpu(cc="9.0")], driver="550.54.15", arch="aarch64"), cpu_raw(), torch_variant="cpu"
    )
    issue = only(env, "torch_cpu_build")
    assert issue["level"] == "warning"
    assert "ARM" in issue["message"]


def test_driver_too_old_for_installed_variant():
    env = env_of(hw([gpu()], driver="550.54.15"), raw(available=False), torch_variant="cu130")
    issue = only(env, "driver_too_old")
    assert issue["level"] == "error"
    assert "550.54.15" in issue["message"] and "580" in issue["message"]
    assert "TM_TORCH_VARIANT=cu126" in issue["hint"]
    assert env["recommended_variant"] == "cu126"
    # 驱动过旧时 is_available() 本来就是 False，不再重复报告 cuda_unavailable。
    assert "cuda_unavailable" not in codes(env)
    assert env["status"] == "error"


def test_driver_too_old_when_no_cuda_build_fits():
    env = env_of(
        hw([gpu(cc="7.5")], driver="440.100"),
        raw(cuda="11.8", available=False),
        torch_variant="cu118",
    )
    issue = only(env, "driver_too_old")
    assert "450.80.02" in issue["message"]
    assert "TM_TORCH_VARIANT" not in issue["hint"]


def test_driver_too_old_uses_torch_cuda_when_variant_unknown():
    env = env_of(
        hw([gpu()], driver="550.54.15"),
        raw(torch="2.14.1", cuda="13.0", available=False),
        torch_variant=None,
    )
    assert "driver_too_old" in codes(env)


def test_arch_unsupported_rtx5090_on_cu126():
    env = env_of(
        hw([gpu(name="NVIDIA GeForce RTX 5090", cc="12.0")], driver="580.65.06"),
        raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS),
        torch_variant="cu126",
    )
    issue = only(env, "arch_unsupported")
    assert issue["level"] == "error"
    assert "RTX 5090" in issue["message"] and "12.0" in issue["message"]
    assert "TM_TORCH_VARIANT=cu130" in issue["hint"]
    assert "variant_upgrade_available" not in codes(env)
    assert env["status"] == "error"


def test_arch_unsupported_falls_back_to_table_archs():
    env = env_of(
        hw([gpu(name="NVIDIA GeForce RTX 5090", cc="12.0")], driver="575.57.08"),
        raw(torch="2.14.1+cu126", cuda="12.6", available=False),
        torch_variant="cu126",
    )
    assert "arch_unsupported" in codes(env)
    assert "TM_TORCH_VARIANT=cu128" in only(env, "arch_unsupported")["hint"]


def test_ptx_in_arch_list_covers_newer_gpu():
    env = env_of(
        hw([gpu(cc="12.0")]),
        raw(archs=["sm_80", "sm_90", "compute_90"]),
        torch_variant="cu130",
    )
    assert "arch_unsupported" not in codes(env)


def test_cuda_unavailable():
    env = env_of(
        hw([gpu()]),
        raw(
            available=False,
            cuda_error="CUDA driver initialization failed, you might not have a CUDA gpu.",
        ),
    )
    issue = only(env, "cuda_unavailable")
    assert issue["level"] == "error"
    assert "is_available()" in issue["message"]
    assert "CUDA driver initialization failed" in issue["message"]
    assert "--gpus all" in issue["hint"]
    assert env["accelerator"] == "cpu"
    assert env["status"] == "error"


def test_cuda_unavailable_bare_metal_hint():
    env = env_of(hw([gpu()], container=False), raw(available=False))
    assert "nvidia-smi" in only(env, "cuda_unavailable")["hint"]


def test_self_test_failed():
    st = {
        "status": "failed",
        "device": "cuda:0",
        "duration_ms": 120,
        "message": "YOLO11n 前向推理（cuda:0，FP32）失败：RuntimeError: CUDA error: out of memory",
        "detail": "Traceback ...\nRuntimeError: CUDA error: out of memory",
    }
    env = env_of(hw([gpu()]), raw(self_test=st))
    issue = only(env, "self_test_failed")
    assert issue["level"] == "error"
    assert "out of memory" in issue["message"]
    assert "显存不足" in issue["hint"]
    assert env["status"] == "error"
    assert env["accelerator"] == "cuda"
    assert env["self_test"] == st


def test_self_test_failed_no_kernel_image_hint():
    st = {
        "status": "failed",
        "device": "cuda:0",
        "duration_ms": 10,
        "message": "失败",
        "detail": "CUDA error: no kernel image is available for execution on the device",
    }
    env = env_of(
        hw([gpu()], driver="550.54.15"), raw(cuda="12.6", self_test=st), torch_variant="cu126"
    )
    assert "TM_TORCH_VARIANT=cu126" in only(env, "self_test_failed")["hint"]


def test_self_test_skipped_is_not_an_error():
    st = {
        "status": "skipped",
        "device": None,
        "duration_ms": None,
        "message": "已关闭自检",
        "detail": None,
    }
    env = env_of(hw([gpu()]), raw(self_test=st))
    assert env["status"] == "ready"
    assert env["issues"] == []


def test_child_failure_reports_only_self_test():
    r = {
        "python_version": "3.12.7",
        "torch_arch_list": [],
        "cuda_available": False,
        "cuda_device_count": 0,
        "probe_error": "环境检测超时（300 秒内没有完成）",
        "self_test": {
            "status": "failed",
            "device": None,
            "duration_ms": 300000,
            "message": "环境检测超时（300 秒内没有完成）",
            "detail": None,
        },
    }
    env = env_of(hw([gpu()]), r)
    assert codes(env) == ["self_test_failed"]
    assert "超时" in env["issues"][0]["message"]
    assert env["status"] == "error"


def test_variant_upgrade_available():
    env = env_of(
        hw([gpu()], driver="580.65.06"),
        raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS),
        torch_variant="cu126",
    )
    issue = only(env, "variant_upgrade_available")
    assert issue["level"] == "info"
    assert "cu130" in issue["message"] and "cu126" in issue["message"]
    assert "TM_TORCH_VARIANT=cu130" in issue["hint"]
    assert env["status"] == "ready"


def test_variant_upgrade_uses_inferred_variant():
    env = env_of(
        hw([gpu()]), raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS), torch_variant=None
    )
    assert "variant_upgrade_available" in codes(env)


def test_no_upgrade_suggestion_when_installed_variant_unknown():
    env = env_of(
        hw([gpu()]), raw(torch="2.5.0+cu124", cuda="12.4", archs=CU126_ARCHS), torch_variant=None
    )
    assert codes(env) == []


def test_shm_small_with_gpu():
    env = env_of(hw([gpu()], shm=64 << 20), raw())
    issue = only(env, "shm_small")
    assert issue["level"] == "warning"
    assert "64.0 MiB" in issue["message"]
    assert "TM_TRAINER_SHM_SIZE" in issue["hint"]
    assert env["status"] == "ready"


def test_shm_small_ignored_without_gpu_and_bare_metal_hint():
    assert "shm_small" not in codes(env_of(hw(shm=64 << 20), cpu_raw()))
    env = env_of(hw([gpu()], shm=64 << 20, container=False), raw())
    assert "mount -o remount" in only(env, "shm_small")["hint"]


def test_mig_enabled():
    env = env_of(
        hw([gpu(0, name="NVIDIA A100-SXM4-80GB", cc="8.0", mig_enabled=True), gpu(1)]), raw(count=2)
    )
    issue = only(env, "mig_enabled")
    assert issue["level"] == "info"
    assert "A100" in issue["message"] and "RTX 4090" not in issue["message"]


def test_issues_sorted_by_level():
    env = env_of(
        hw([gpu(mig_enabled=True)], shm=1 << 20),
        raw(available=False),
        device_nodes=["nvidia0"],
    )
    levels = [i["level"] for i in env["issues"]]
    assert levels == sorted(levels, key=issues.LEVEL_ORDER.__getitem__)
    assert codes(env) == ["cuda_unavailable", "shm_small", "mig_enabled"]


def test_long_messages_are_truncated():
    env = env_of(hw([gpu()]), raw(available=False, cuda_error="x" * 2000))
    message = only(env, "cuda_unavailable")["message"]
    assert len(message) == issues.MAX_MESSAGE and message.endswith("…")


def test_unknown_cc_assumed_modern_for_recommendation():
    env = env_of(
        hw([gpu(cc=None)], driver="550.54.15"),
        raw(cuda="12.6", archs=CU126_ARCHS),
        torch_variant="cu126",
    )
    assert env["recommended_variant"] == "cu126"
    assert "arch_unsupported" not in codes(env)


# ---- 辅助函数 ----


@pytest.mark.parametrize(
    ("cc", "archs", "expected"),
    [
        (89, ["sm_86"], True),
        (120, ["sm_90"], False),
        (120, ["sm_90", "compute_90"], True),
        (90, ["sm_90a"], True),
        (100, ["sm_90a"], False),
        (103, ["sm_100f"], True),
        (87, ["sm_80", "sm_86"], False),
        (87, ["sm_87"], True),
        (75, ["garbage", ""], False),
    ],
)
def test_arch_covers(cc, archs, expected):
    assert arch_covers(cc, archs) is expected


@pytest.mark.parametrize(
    ("n", "expected"),
    [
        (512, "512 B"),
        (64 << 20, "64.0 MiB"),
        (1536, "1.5 KiB"),
        (8 << 30, "8.0 GiB"),
        (3 << 40, "3072.0 GiB"),
    ],
)
def test_format_bytes(n, expected):
    assert format_bytes(n) == expected
