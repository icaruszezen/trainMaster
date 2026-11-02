"""issues.py：每个 issue code、status / accelerator 判定与 recommended_variant。"""

from __future__ import annotations

from typing import Any

import pytest

from trainmaster_trainer import issues
from trainmaster_trainer.issues import Deployment, arch_covers, build_environment, format_bytes

CU126_ARCHS = ["sm_50", "sm_60", "sm_70", "sm_75", "sm_80", "sm_86", "sm_90"]
CU130_ARCHS = ["sm_75", "sm_80", "sm_86", "sm_90", "sm_100", "sm_120"]
PROBED_AT = "2026-11-02T07:30:00Z"
REMOTE_URL = "http://192.168.1.10:8080"
# 裸机安装（install.sh 的默认目录）
BARE = Deployment(python="/opt/trainmaster-trainer/bin/python", prefix="/opt/trainmaster-trainer")


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
    if not hardware.get("in_container"):
        kw.setdefault("deployment", BARE)
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
    # 装好驱动后还需要 NVIDIA Container Toolkit 才能以 GPU 模式运行
    assert '`make up SETUP_ARGS="--install-toolkit"`' in issue["hint"]


def test_no_gpu_pci_devices_on_bare_metal_reinstalls():
    env = env_of(hw(container=False), cpu_raw(), pci_gpu_count=1)
    hint = only(env, "no_gpu")["hint"]
    assert "安装 NVIDIA 驱动" in hint and "`sudo trainer/scripts/install.sh`" in hint
    assert "make" not in hint and "docker" not in hint


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
    assert "NVIDIA Container Toolkit" in issue["hint"]
    assert issue["hint"].endswith('`make up SETUP_ARGS="--install-toolkit"`')
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
        ("NVML Shared Library Not Found", '`make up SETUP_ARGS="--install-toolkit"`'),
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
    assert issue["hint"] == "重新构建训练节点镜像：`make up`"
    assert not {"torch_cpu_build", "cuda_unavailable", "driver_too_old"} & set(codes(env))
    assert env["status"] == "error"


def test_torch_missing_bare_metal_hint():
    env = env_of(
        hw(container=False), raw(torch=None, cuda=None, available=False), torch_variant=None
    )
    assert only(env, "torch_missing")["hint"].endswith("`sudo trainer/scripts/install.sh`")


def test_torchvision_missing():
    env = env_of(hw(), cpu_raw(torchvision=None, torchvision_error="ImportError: libc10.so"))
    issue = only(env, "torch_missing")
    assert "torchvision" in issue["message"] and "libc10.so" in issue["message"]
    # 重新构建即可，不固定变体（make setup 不接受 --variant cpu）
    assert issue["hint"] == "重新构建训练节点镜像：`make up`"


@pytest.mark.parametrize(
    ("hardware", "deployment", "hint"),
    [
        (
            hw(),
            Deployment(server_url=REMOTE_URL, service="trainer-cpu"),
            "重新构建训练节点镜像：`make setup` 然后 `docker compose up -d --build trainer-cpu`",
        ),
        (
            hw(container=False),
            BARE,
            "重新运行安装脚本（在 trainMaster 仓库目录中运行）："
            "`sudo trainer/scripts/install.sh --variant cpu`",
        ),
    ],
)
def test_torchvision_missing_on_cpu_node_reinstalls_same_variant(hardware, deployment, hint):
    env = env_of(
        hardware,
        cpu_raw(torchvision=None, torchvision_error="ImportError: x"),
        torch_variant="cpu",
        deployment=deployment,
    )
    assert only(env, "torch_missing")["hint"] == hint


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
    assert (
        issue["hint"]
        == '改用 PyTorch 变体 cu130 重新构建镜像：`make up SETUP_ARGS="--variant cu130"`'
    )
    assert env["status"] == "error"
    assert env["accelerator"] == "cpu"


def test_torch_cpu_build_bare_metal_hint():
    env = env_of(hw([gpu()], container=False), cpu_raw(), torch_variant="cpu")
    assert only(env, "torch_cpu_build")["hint"].endswith(
        "`sudo trainer/scripts/install.sh --variant cu130`"
    )


def test_cpu_build_when_driver_too_old_for_any_cuda_build():
    env = env_of(hw([gpu(cc="7.5")], driver="440.100"), cpu_raw(), torch_variant="cpu")
    issue = only(env, "driver_too_old")
    assert issue["level"] == "warning"
    assert "440.100" in issue["message"]
    assert "升级 NVIDIA 驱动" in issue["hint"] and issue["hint"].endswith("`make up`")
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
    assert issue["hint"] == (
        "升级驱动到 580 或更新；或者改用 PyTorch 变体 cu126 重新构建镜像："
        '`make up SETUP_ARGS="--variant cu126"`'
    )
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
    assert "--variant" not in issue["hint"]


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
    assert '`make up SETUP_ARGS="--variant cu130"`' in issue["hint"]
    assert "variant_upgrade_available" not in codes(env)
    assert env["status"] == "error"


def test_arch_unsupported_falls_back_to_table_archs():
    env = env_of(
        hw([gpu(name="NVIDIA GeForce RTX 5090", cc="12.0")], driver="575.57.08"),
        raw(torch="2.14.1+cu126", cuda="12.6", available=False),
        torch_variant="cu126",
    )
    assert "arch_unsupported" in codes(env)
    assert '--variant cu128"`' in only(env, "arch_unsupported")["hint"]


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
        hw([gpu()], driver="580.65.06"), raw(cuda="12.6", self_test=st), torch_variant="cu126"
    )
    assert only(env, "self_test_failed")["hint"].endswith('`make up SETUP_ARGS="--variant cu130"`')
    # 推荐的就是已安装的变体：重新构建同一个变体没有用，给出重现自检的命令
    env = env_of(
        hw([gpu()], driver="550.54.15"), raw(cuda="12.6", self_test=st), torch_variant="cu126"
    )
    hint = only(env, "self_test_failed")["hint"]
    assert "--variant" not in hint
    assert "`docker compose exec trainer python -m trainmaster_trainer selftest`" in hint


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
    assert '`make up SETUP_ARGS="--variant cu130"`' in issue["hint"]
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
    assert "TM_TRAINER_SHM_SIZE" in issue["hint"] and issue["hint"].endswith("`make up`")
    assert env["status"] == "ready"


def test_shm_small_ignored_without_gpu_and_bare_metal_hint():
    assert "shm_small" not in codes(env_of(hw(shm=64 << 20), cpu_raw()))
    env = env_of(hw([gpu()], shm=64 << 20, container=False), raw())
    assert "`sudo mount -o remount,size=8G /dev/shm`" in only(env, "shm_small")["hint"]


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


# ---- 新旧显卡混用 ----

RTX5090 = {"name": "NVIDIA GeForce RTX 5090", "cc": "12.0"}
GTX1080TI = {"name": "NVIDIA GeForce GTX 1080 Ti", "cc": "6.1"}


def test_mixed_gpus_without_common_variant_is_a_warning():
    # cu130 支持 5090 但不支持 1080 Ti，也没有同时支持两者的变体：1080 Ti 不能训练，5090 可以。
    env = env_of(hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)]), raw(count=2), torch_variant="cu130")
    assert env["recommended_variant"] == "cu130"
    issue = only(env, "arch_unsupported")
    assert issue["level"] == "warning"
    assert "1 号 NVIDIA GeForce GTX 1080 Ti" in issue["message"] and "5090" not in issue["message"]
    assert "device_ids" in issue["hint"] and "10.8" in issue["hint"]
    assert issue["hint"].endswith("`make up`")
    assert env["status"] == "ready"


def test_mixed_gpus_bare_metal_hint_uses_cuda_visible_devices():
    env = env_of(
        hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)], container=False),
        raw(count=2),
        torch_variant="cu130",
    )
    hint = only(env, "arch_unsupported")["hint"]
    # 配置文件可能是 install.sh --env-file 指定的其它文件：只说默认路径
    assert "配置文件（默认 /opt/trainmaster-trainer/trainer.env，" in hint and "--env-file" in hint
    assert "`CUDA_VISIBLE_DEVICES=0`" in hint
    assert "`sudo systemctl restart trainmaster-trainer`" in hint
    # 不支持的显卡排在前面时给出其余显卡的序号
    env = env_of(
        hw([gpu(0, **GTX1080TI), gpu(1, **RTX5090), gpu(2, **RTX5090)], container=False),
        raw(count=3),
        torch_variant="cu130",
    )
    assert "`CUDA_VISIBLE_DEVICES=1,2`" in only(env, "arch_unsupported")["hint"]


def bare(cuda_visible_devices: str | None) -> Deployment:
    return Deployment(
        python=BARE.python, prefix=BARE.prefix, cuda_visible_devices=cuda_visible_devices
    )


@pytest.mark.parametrize("visible", ["0", "GPU-0", " 0 ", "0,-1,1", "MIG-GPU-0/1/0"])
def test_bare_metal_cuda_visible_devices_clears_mixed_gpu_warning(visible):
    # 按上一条提示在 trainer.env 中设置了 CUDA_VISIBLE_DEVICES=0：NVML 仍列出两块显卡，
    # 但 1080 Ti 已被排除，不再警告（否则每次检测都重复同样的警告与提示）。
    env = env_of(
        hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)], container=False),
        raw(count=1),
        torch_variant="cu130",
        deployment=bare(visible),
    )
    assert codes(env) == ["arch_unsupported"]
    issue = env["issues"][0]
    assert issue["level"] == "info" and issue["hint"] is None
    assert "1 号 NVIDIA GeForce GTX 1080 Ti" in issue["message"] and "5090" not in issue["message"]
    assert "已通过 CUDA_VISIBLE_DEVICES 排除" in issue["message"]
    assert env["status"] == "ready" and env["recommended_variant"] == "cu130"


def test_bare_metal_cuda_visible_devices_listing_every_gpu_still_warns():
    env = env_of(
        hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)], container=False),
        raw(count=2),
        torch_variant="cu130",
        deployment=bare("0,1"),
    )
    issue = only(env, "arch_unsupported")
    assert issue["level"] == "warning" and "`CUDA_VISIBLE_DEVICES=0`" in issue["hint"]


def test_bare_metal_cuda_visible_devices_hint_lists_only_visible_supported_gpus():
    gpus = [gpu(0, **GTX1080TI), gpu(1, **RTX5090), gpu(2, **RTX5090), gpu(3, **RTX5090)]
    env = env_of(
        hw(gpus, container=False), raw(count=3), torch_variant="cu130", deployment=bare("0,1,3")
    )
    issue = only(env, "arch_unsupported")
    assert issue["level"] == "warning" and "`CUDA_VISIBLE_DEVICES=1,3`" in issue["hint"]


def test_cuda_visible_devices_only_matters_on_bare_metal():
    # 容器中由 device_ids 限制显卡（NVML 也看不到没有分配的显卡），不看 CUDA_VISIBLE_DEVICES。
    env = env_of(
        hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)]),
        raw(count=2),
        torch_variant="cu130",
        deployment=Deployment(cuda_visible_devices="0"),
    )
    assert only(env, "arch_unsupported")["level"] == "warning"


def test_bare_metal_visible_gpus_decide_the_recommendation():
    # 只让 1080 Ti 训练：推荐支持它的 cu126，而不是按两块显卡选出的 cu130。
    hardware = hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)], container=False)
    env = env_of(hardware, raw(count=1), torch_variant="cu130", deployment=bare("1"))
    assert env["recommended_variant"] == "cu126"
    issue = only(env, "arch_unsupported")
    assert issue["level"] == "error" and "1 号 NVIDIA GeForce GTX 1080 Ti" in issue["message"]
    assert "`sudo trainer/scripts/install.sh --variant cu126`" in issue["hint"]
    # 装好 cu126 之后：5090 已被排除，只作提示，也不再建议换回 cu130。
    env = env_of(
        hardware,
        raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS, count=1),
        torch_variant="cu126",
        deployment=bare("1"),
    )
    assert codes(env) == ["arch_unsupported"]
    issue = env["issues"][0]
    assert issue["level"] == "info" and "0 号 NVIDIA GeForce RTX 5090" in issue["message"]
    assert env["status"] == "ready"


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        (None, [0, 1, 2]),
        ("2,0", [0, 2]),  # CUDA 中的顺序不影响哪些显卡可见
        ("GPU-aaaa", [0]),  # UUID 可以只写开头部分
        ("GPU-bbbb-2222", [1]),
        ("1,GPU-cccc", [1, 2]),
        ("MIG-GPU-cccc-3333/1/0", [2]),  # 旧格式的 MIG UUID 包含所在显卡的 UUID
        ("MIG-5f3b9c4e-0000", [2]),  # 新格式：开启了 MIG 的显卡
        ("0,x,1", [0]),  # 与 CUDA 相同，忽略无法识别的项及之后的各项
        ("-1", []),
        ("", []),
    ],
)
def test_visible_gpus(value, expected):
    gpus = [
        gpu(0, uuid="GPU-aaaa-1111"),
        gpu(1, uuid="GPU-bbbb-2222"),
        gpu(2, uuid="GPU-cccc-3333", mig_enabled=True),
    ]
    assert [g["index"] for g in issues.visible_gpus(gpus, value)] == expected


def test_mixed_gpus_on_wrong_variant_is_still_an_error():
    # 装的是 cu126：5090 不能用，而推荐的 cu130 可以让 5090 训练。
    env = env_of(
        hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)]),
        raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS, count=2),
        torch_variant="cu126",
    )
    issue = only(env, "arch_unsupported")
    assert issue["level"] == "error"
    assert '--variant cu130"`' in issue["hint"]
    assert env["status"] == "error"


def test_mixed_gpus_no_kernel_image_hint_does_not_rebuild_same_variant():
    st = {
        "status": "failed",
        "device": "cuda:0",
        "duration_ms": 10,
        "message": "GPU 1 矩阵乘法失败",
        "detail": "CUDA error: no kernel image is available for execution on the device",
    }
    env = env_of(
        hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)]),
        raw(count=2, self_test=st),
        torch_variant="cu130",
    )
    hint = only(env, "self_test_failed")["hint"]
    assert "--variant" not in hint and "selftest" in hint


def test_all_gpus_unsupported_without_alternative_has_no_hint():
    env = env_of(
        hw([gpu(0, name="Tesla K10", cc="3.0")]),
        raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS),
        torch_variant="cu126",
    )
    issue = only(env, "arch_unsupported")
    assert issue["level"] == "error" and issue["hint"] is None


# ---- 部署方式：同一个 compose、其它机器上的 compose、裸机 ----


def remote(service: str = "trainer", **kw: Any) -> Deployment:
    return Deployment(server_url=REMOTE_URL, service=service, **kw)


def test_remote_variant_hint_scopes_to_the_service():
    env = env_of(
        hw([gpu()], driver="550.54.15"),
        raw(available=False),
        torch_variant="cu130",
        deployment=remote(),
    )
    assert only(env, "driver_too_old")["hint"] == (
        "升级驱动到 580 或更新；或者改用 PyTorch 变体 cu126 重新构建镜像："
        '`make setup SETUP_ARGS="--variant cu126"` 然后 `docker compose up -d --build trainer`'
    )


def test_remote_toolkit_hint_on_cpu_service():
    env = env_of(
        hw(host_gpu_count=1),
        cpu_raw(),
        torch_variant="cpu",
        deployment=remote("trainer-cpu", mode="auto"),
    )
    hint = only(env, "gpu_not_passed_through")["hint"]
    assert '`make setup SETUP_ARGS="--install-toolkit"`' in hint
    assert "按它最后输出的服务名启动" in hint and "`docker compose up -d --build trainer`" in hint
    assert "make up" not in hint


def test_remote_shm_and_rebuild_hints():
    env = env_of(hw([gpu()], shm=64 << 20), raw(), deployment=remote())
    assert only(env, "shm_small")["hint"].endswith("`docker compose up -d trainer`")
    env = env_of(
        hw([gpu()]),
        raw(torch=None, cuda=None, available=False, torch_error="ModuleNotFoundError: torch"),
        deployment=remote(),
    )
    assert only(env, "torch_missing")["hint"] == (
        "重新构建训练节点镜像：`make setup` 然后 `docker compose up -d --build trainer`"
    )


def test_server_url_with_app_host_is_the_platform_compose():
    env = env_of(
        hw([gpu()], shm=64 << 20),
        raw(),
        deployment=Deployment(server_url="http://app:8080", service="trainer"),
    )
    assert only(env, "shm_small")["hint"].endswith("`make up`")


@pytest.mark.parametrize(
    ("deployment", "command"),
    [
        (Deployment(), "`docker compose exec trainer python -m trainmaster_trainer selftest`"),
        (
            remote("trainer-cpu"),
            "`docker compose exec trainer-cpu python -m trainmaster_trainer selftest`",
        ),
    ],
)
def test_self_test_hint_names_the_running_service(deployment, command):
    st = {"status": "failed", "device": "cpu", "duration_ms": 5, "message": "失败", "detail": "x"}
    env = env_of(hw(), cpu_raw(self_test=st), torch_variant="cpu", deployment=deployment)
    assert (
        only(env, "self_test_failed")["hint"] == f"查看节点详情中的错误信息，或运行 {command} 重现"
    )


def test_self_test_hint_on_bare_metal_uses_the_venv_python():
    st = {"status": "failed", "device": "cpu", "duration_ms": 5, "message": "失败", "detail": "x"}
    env = env_of(hw(container=False), cpu_raw(self_test=st), torch_variant="cpu")
    assert (
        "`/opt/trainmaster-trainer/bin/python -m trainmaster_trainer selftest`"
        in only(env, "self_test_failed")["hint"]
    )


def test_bare_metal_hints_never_use_docker_or_make():
    custom = Deployment(python="/srv/tm env/bin/python", prefix="/srv/tm env")
    env = env_of(
        hw([gpu(cc="7.5")], driver="440.100", container=False),
        cpu_raw(),
        torch_variant="cpu",
        deployment=custom,
    )
    hint = only(env, "driver_too_old")["hint"]
    assert hint.endswith("`sudo trainer/scripts/install.sh --prefix '/srv/tm env'`")
    assert "make" not in hint and "docker" not in hint
    env = env_of(
        hw([gpu()], driver="550.54.15", container=False),
        raw(available=False),
        torch_variant="cu130",
        deployment=custom,
    )
    assert only(env, "driver_too_old")["hint"].endswith(
        "`sudo trainer/scripts/install.sh --variant cu126 --prefix '/srv/tm env'`"
    )


def test_pinned_variant_suggests_auto():
    hardware = hw([gpu(**RTX5090)], driver="580.65.06")
    cu126 = raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS)
    env = env_of(
        hardware, cu126, torch_variant="cu126", deployment=Deployment(pinned_variant="cu126")
    )
    hint = only(env, "arch_unsupported")["hint"]
    assert "固定为 cu126" in hint and hint.endswith('`make up SETUP_ARGS="--variant auto"`')
    # make setup 不接受的固定值按自动选择处理
    for value in ("cpu", "cu999"):
        env = env_of(
            hardware, cu126, torch_variant="cu126", deployment=Deployment(pinned_variant=value)
        )
        assert '--variant cu130"`' in only(env, "arch_unsupported")["hint"]


def test_pinned_variant_equal_to_recommendation_needs_no_variant_argument():
    # 固定的就是推荐的变体，但镜像是用其它变体构建的（如手动修改了 TM_TORCH_VARIANT）：重新构建即可。
    env = env_of(
        hw([gpu(**RTX5090)]),
        raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS),
        torch_variant="cu126",
        deployment=Deployment(pinned_variant="cu130"),
    )
    assert only(env, "arch_unsupported")["hint"] == "重新构建镜像（PyTorch 变体 cu130）：`make up`"


# ---- 选择了 CPU 模式（TM_TRAINER_MODE=cpu）----


def test_cpu_mode_on_gpu_host_is_info_not_warning():
    cpu_mode = Deployment(service="trainer-cpu", mode="cpu")
    env = env_of(hw(host_gpu_count=2), cpu_raw(), torch_variant="cpu", deployment=cpu_mode)
    assert codes(env) == ["cpu_mode_selected"]
    issue = env["issues"][0]
    assert issue["level"] == "info"
    assert "TM_TRAINER_MODE=cpu" in issue["message"] and "2 块" in issue["message"]
    assert issue["hint"] == '改用 GPU：`make up SETUP_ARGS="--mode auto"`'
    assert env["status"] == "cpu_only"


def test_cpu_mode_remote_hint():
    env = env_of(
        hw(host_gpu_count=1),
        cpu_raw(),
        torch_variant="cpu",
        deployment=remote("trainer-cpu", mode="cpu"),
    )
    hint = only(env, "cpu_mode_selected")["hint"]
    assert hint.startswith('改用 GPU：`make setup SETUP_ARGS="--mode auto"`')
    assert "`docker compose up -d --build trainer`" in hint


def test_cpu_mode_with_gpu_but_no_driver():
    env = env_of(
        hw(), cpu_raw(), torch_variant="cpu", pci_gpu_count=1, deployment=Deployment(mode="cpu")
    )
    issue = only(env, "cpu_mode_selected")
    assert issue["level"] == "info" and "no_gpu" not in codes(env)
    assert "安装 NVIDIA 驱动" in issue["hint"]
    assert issue["hint"].endswith('`make up SETUP_ARGS="--mode auto --install-toolkit"`')


def test_auto_mode_still_warns_when_gpu_not_passed_through():
    env = env_of(
        hw(host_gpu_count=1), cpu_raw(), torch_variant="cpu", deployment=Deployment(mode="auto")
    )
    assert codes(env) == ["gpu_not_passed_through"]


# ---- 显卡没有传入容器、宿主机驱动过旧（make setup 因此选择了 CPU，安装 Toolkit 也没有用）----


def test_old_host_driver_is_driver_too_old_not_gpu_not_passed_through():
    env = env_of(
        hw(host_gpu_count=1),
        cpu_raw(),
        torch_variant="cpu",
        host_driver_version="440.100",
        deployment=Deployment(service="trainer-cpu", mode="auto"),
    )
    assert codes(env) == ["driver_too_old"]
    issue = env["issues"][0]
    assert issue["level"] == "warning"
    assert issue["message"] == (
        "宿主机 NVIDIA 驱动 440.100 过旧，CUDA 版 PyTorch 至少需要 450.80.02，将使用 CPU 训练"
    )
    assert issue["hint"] == (
        "升级 NVIDIA 驱动（推荐 580 或更新）并重启，然后安装 NVIDIA Container Toolkit 并以 GPU 模式"
        '启动（需要 sudo）：`make up SETUP_ARGS="--install-toolkit"`'
    )
    assert env["status"] == "cpu_only"


def test_old_host_driver_remote_hint_starts_only_the_trainer():
    env = env_of(
        hw(host_gpu_count=2),
        cpu_raw(),
        torch_variant="cpu",
        host_driver_version="418.87",
        deployment=remote("trainer-cpu", mode="auto"),
    )
    hint = only(env, "driver_too_old")["hint"]
    assert hint.startswith("升级 NVIDIA 驱动（推荐 580 或更新）并重启，然后")
    assert '`make setup SETUP_ARGS="--install-toolkit"`' in hint
    assert "`docker compose up -d --build trainer`" in hint and "make up" not in hint


def test_cpu_mode_wins_over_old_host_driver():
    env = env_of(
        hw(host_gpu_count=1),
        cpu_raw(),
        torch_variant="cpu",
        host_driver_version="440.100",
        deployment=Deployment(service="trainer-cpu", mode="cpu"),
    )
    assert codes(env) == ["cpu_mode_selected"]


@pytest.mark.parametrize(
    ("arch", "driver", "expected"),
    [
        ("x86_64", "450.80.02", "gpu_not_passed_through"),  # 驱动够用：没有传入容器的原因是 Toolkit
        ("x86_64", "580.65.06", "gpu_not_passed_through"),
        ("x86_64", None, "gpu_not_passed_through"),  # 读不到 /proc/driver/nvidia/version
        ("aarch64", "575.57.08", "driver_too_old"),  # ARM 服务器只有 CUDA 13 版，需要 580
        ("aarch64", "580.65.06", "gpu_not_passed_through"),
        ("ppc64le", "440.100", "gpu_not_passed_through"),  # 没有 CUDA 版 PyTorch，与驱动无关
    ],
)
def test_old_host_driver_threshold_follows_the_platform(arch, driver, expected):
    env = env_of(
        hw(host_gpu_count=1, arch=arch),
        cpu_raw(),
        torch_variant="cpu",
        host_driver_version=driver,
    )
    assert codes(env) == [expected]
    if expected == "driver_too_old":
        assert "至少需要 580" in env["issues"][0]["message"]


def test_host_driver_only_matters_when_gpu_is_not_passed_through():
    # 显卡已传入容器（NVML 正常）：按 NVML 的驱动版本判断，/proc 中的版本不参与。
    env = env_of(hw([gpu()]), raw(), host_driver_version="440.100")
    assert env["issues"] == [] and env["status"] == "ready"
    # 裸机：不存在“没有传入容器”的情况
    env = env_of(
        hw(host_gpu_count=1, container=False), raw(available=False), host_driver_version="440.100"
    )
    assert codes(env) == ["no_gpu"]


# ---- hint 中的命令都用反引号包住（前端据此显示为代码、提供复制）----

# 只会出现在命令中的片段：出现在反引号外面就说明有命令没有包住。
COMMAND_PARTS = (
    "docker compose",
    "docker run",
    "make setup",
    "make up",
    "install.sh",
    "nvidia-smi",
    "python -m",
    "sudo ",
    "systemctl",
    "mount ",
    "SETUP_ARGS",
)


def _all_hints() -> list[tuple[str, str, str]]:
    """尽量覆盖每一种 hint：各种问题 × 三种部署方式（以及 CPU 服务、固定变体、CPU 模式）。"""
    st_failed = {"status": "failed", "device": "cuda:0", "duration_ms": 1, "message": "失败"}
    cases: list[tuple[dict[str, Any], dict[str, Any], dict[str, Any]]] = [
        (hw(), cpu_raw(), {"pci_gpu_count": 2}),
        (hw(error="NVML Shared Library Not Found", host_gpu_count=2), raw(available=False), {}),
        (hw(method="nvml", host_gpu_count=1), cpu_raw(), {"torch_variant": "cpu"}),
        (
            hw(host_gpu_count=1),
            cpu_raw(),
            {"torch_variant": "cpu", "host_driver_version": "440.100"},
        ),
        (hw(error="Driver Not Loaded", host_gpu_count=1), cpu_raw(), {"device_nodes": ["nvidia0"]}),
        (hw(error="Unknown Error", host_gpu_count=1), cpu_raw(), {"device_nodes": ["nvidia0"]}),
        (hw([gpu()], method="nvidia-smi", error="Insufficient Permissions"), raw(), {}),
        (
            hw([gpu()]),
            raw(torch=None, cuda=None, available=False, torch_error="ModuleNotFoundError: torch"),
            {},
        ),
        (hw([gpu()]), raw(torchvision=None, torchvision_error="ImportError: x"), {}),
        (hw([gpu()]), raw(ultralytics=None, ultralytics_error="ImportError: y"), {}),
        (hw([gpu()]), cpu_raw(), {"torch_variant": "cpu"}),
        (hw([gpu(cc="7.5")], driver="440.100"), cpu_raw(), {"torch_variant": "cpu"}),
        (hw([gpu()], driver="550.54.15"), raw(available=False), {}),
        (
            hw([gpu(**RTX5090)]),
            raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS),
            {"torch_variant": "cu126"},
        ),
        (hw([gpu(0, **RTX5090), gpu(1, **GTX1080TI)]), raw(count=2), {}),
        (hw([gpu()]), raw(available=False, cuda_error="init failed"), {}),
        (
            hw([gpu()]),
            raw(torch="2.14.1+cu126", cuda="12.6", archs=CU126_ARCHS),
            {"torch_variant": "cu126"},
        ),
        (hw([gpu()]), raw(self_test={**st_failed, "detail": "CUDA error: out of memory"}), {}),
        (
            hw([gpu()]),
            raw(cuda="12.6", self_test={**st_failed, "detail": "no kernel image is available"}),
            {"torch_variant": "cu126"},
        ),
        (hw([gpu()]), raw(self_test={**st_failed, "detail": "RuntimeError: boom"}), {}),
        (hw([gpu(mig_enabled=True)], shm=1 << 20), raw(), {}),
    ]
    deployments = [  # (标签, 部署方式, 是否在容器中)
        ("local", Deployment(), True),
        ("local", Deployment(mode="cpu", service="trainer-cpu"), True),
        ("local", Deployment(pinned_variant="cu126"), True),
        ("remote", remote(), True),
        ("remote", remote("trainer-cpu", mode="cpu"), True),
        ("bare", BARE, False),
        ("bare", Deployment(python="/srv/tm/bin/python", prefix="/srv/tm"), False),
    ]
    hints = []
    for hardware, r, kw in cases:
        for label, d, container in deployments:
            h = dict(hardware, in_container=container)
            env = env_of(h, r, **{"torch_variant": "cu130", **kw, "deployment": d})
            hints += [(i["code"], label, i["hint"]) for i in env["issues"] if i["hint"]]
    return hints


def test_hint_commands_are_inside_backticks():
    hints = _all_hints()
    codes_seen = {code for code, _, _ in hints}
    assert codes_seen >= {
        "no_gpu",
        "gpu_not_passed_through",
        "cpu_mode_selected",
        "nvml_error",
        "torch_missing",
        "ultralytics_missing",
        "torch_cpu_build",
        "driver_too_old",
        "arch_unsupported",
        "cuda_unavailable",
        "variant_upgrade_available",
        "self_test_failed",
        "shm_small",
        "mig_enabled",
    }
    for code, label, hint in hints:
        assert hint.count("`") % 2 == 0, (code, label, hint)
        outside = "".join(hint.split("`")[0::2])
        for part in COMMAND_PARTS:
            assert part not in outside, (code, label, hint)


def test_remote_hints_never_start_the_platform():
    for code, label, hint in _all_hints():
        if label != "remote":
            continue
        commands = hint.split("`")[1::2]
        for command in commands:
            assert not command.startswith("make up"), (code, hint)
            if command.startswith("docker compose up"):
                assert command.split()[-1] in ("trainer", "trainer-cpu"), (code, hint)


def test_bare_metal_hints_use_the_install_script():
    for code, label, hint in _all_hints():
        if label != "bare":
            continue
        commands = hint.split("`")[1::2]
        assert not any(c.startswith(("make", "docker")) for c in commands), (code, hint)


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
