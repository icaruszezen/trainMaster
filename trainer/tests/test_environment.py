"""environment.py：子进程中的检测逻辑（假 torch / ultralytics）与父进程对子进程的处理（假子进程脚本）。"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import textwrap
import threading
import time
from pathlib import Path

import pytest
from fakes import TRAINER_DIR, make_torch, make_torchvision, make_ultralytics

from trainmaster_trainer import environment
from trainmaster_trainer.environment import (
    EnvironmentDetector,
    collect,
    failure_raw,
    format_cudnn_version,
    sanitize_raw,
)

TESTS_DIR = Path(__file__).resolve().parent


def install(monkeypatch, torch=None, torchvision=None, ultralytics=None):
    """把假模块放进 sys.modules；参数为 None 时让 import 失败。"""
    monkeypatch.setitem(sys.modules, "torch", torch)
    monkeypatch.setitem(sys.modules, "torchvision", torchvision)
    monkeypatch.setitem(sys.modules, "ultralytics", ultralytics)


# ---- 子进程逻辑 ----


def test_collect_cpu_build(monkeypatch):
    torch = make_torch()
    install(monkeypatch, torch, make_torchvision(), make_ultralytics(torch.calls))
    result = collect()
    assert result["torch_version"] == "2.14.1+cpu"
    assert result["torch_cuda_version"] is None
    assert result["cudnn_version"] is None
    assert result["cuda_available"] is False and result["cuda_device_count"] == 0
    assert result["torch_arch_list"] == []
    assert result["torchvision_version"] == "0.29.1+cpu"
    assert result["ultralytics_version"] == "8.4.198"
    assert result["cuda_error"] is None
    st = result["self_test"]
    assert st["status"] == "passed"
    assert st["device"] == "cpu"
    assert st["message"] == "YOLO11n 前向推理正常（CPU）"
    assert isinstance(st["duration_ms"], int)
    assert "YOLO yolo11n.yaml" in torch.calls
    assert "forward cpu" in torch.calls
    assert "rand cpu" in torch.calls
    assert not any(c.startswith("autocast") for c in torch.calls)


def test_collect_cuda_two_gpus(monkeypatch):
    torch = make_torch(
        version="2.14.1",
        cuda="13.0",
        available=True,
        device_count=2,
        arch_list=["sm_75", "sm_80", "sm_120"],
        cudnn=92400,
    )
    install(monkeypatch, torch, make_torchvision("0.29.1"), make_ultralytics(torch.calls))
    result = collect()
    assert result["cuda_available"] is True
    assert result["cuda_device_count"] == 2
    assert result["torch_arch_list"] == ["sm_75", "sm_80", "sm_120"]
    assert result["cudnn_version"] == "9.24.0"
    st = result["self_test"]
    assert st["status"] == "passed"
    assert st["device"] == "cuda:0"
    assert st["message"] == "YOLO11n 前向推理正常（2 块 GPU，FP16）"
    # 每块卡都做了矩阵乘法并同步，然后在 cuda:0 上 FP32、FP16 各推理一次
    assert [c for c in torch.calls if c.startswith(("randn", "synchronize"))] == [
        "randn cuda:0",
        "synchronize 0",
        "randn cuda:1",
        "synchronize 1",
    ]
    assert torch.calls.count("forward cuda:0") == 2
    assert "enter autocast cuda float16" in torch.calls


def test_collect_fp16_unavailable_still_passes(monkeypatch):
    torch = make_torch(
        version="2.7.1+cu118",
        cuda="11.8",
        available=True,
        device_count=1,
        autocast_error=RuntimeError("Current CUDA Device does not support bfloat16"),
    )
    install(monkeypatch, torch, make_torchvision(), make_ultralytics(torch.calls))
    st = collect()["self_test"]
    assert st["status"] == "passed"
    assert "FP32" in st["message"] and "FP16 不可用" in st["message"]


def test_collect_matmul_nan_fails(monkeypatch):
    torch = make_torch(
        version="2.14.1", cuda="13.0", available=True, device_count=2, matmul_nan_on=1
    )
    install(monkeypatch, torch, make_torchvision(), make_ultralytics(torch.calls))
    st = collect()["self_test"]
    assert st["status"] == "failed"
    assert st["message"].startswith("GPU 1 矩阵乘法失败")
    assert "cuda:1" in st["detail"]


def test_collect_forward_error(monkeypatch):
    torch = make_torch(version="2.14.1+cu126", cuda="12.6", available=True, device_count=1)
    error = RuntimeError("CUDA error: no kernel image is available for execution on the device")
    install(monkeypatch, torch, make_torchvision(), make_ultralytics(torch.calls, error=error))
    st = collect()["self_test"]
    assert st["status"] == "failed"
    assert st["device"] == "cuda:0"
    assert "FP32" in st["message"] and "no kernel image" in st["message"]
    assert "Traceback" in st["detail"] and "no kernel image" in st["detail"]
    assert len(st["detail"]) <= environment.MAX_DETAIL


def test_collect_non_finite_output_fails(monkeypatch):
    torch = make_torch()
    install(monkeypatch, torch, make_torchvision(), make_ultralytics(torch.calls, finite=False))
    st = collect()["self_test"]
    assert st["status"] == "failed"
    assert "NaN" in st["message"]


def test_collect_cuda_build_unavailable_records_reason(monkeypatch):
    torch = make_torch(
        version="2.14.1",
        cuda="13.0",
        available=False,
        availability_warning="CUDA initialization: The NVIDIA driver on your system is too old",
        init_error=RuntimeError(
            "The NVIDIA driver on your system is too old (found version 12040)."
        ),
    )
    install(monkeypatch, torch, make_torchvision(), make_ultralytics(torch.calls))
    result = collect()
    assert result["cuda_available"] is False
    assert "driver on your system is too old" in result["cuda_error"]
    assert "found version 12040" in result["cuda_error"]
    assert result["self_test"]["device"] == "cpu"  # 退回 CPU 自检


def test_collect_without_torch(monkeypatch):
    install(monkeypatch, None, None, None)
    result = collect()
    assert result["torch_version"] is None
    assert result["torch_error"].startswith("ModuleNotFoundError") or result[
        "torch_error"
    ].startswith("ImportError")
    assert result["ultralytics_error"]
    st = result["self_test"]
    assert st["status"] == "skipped"
    assert "PyTorch" in st["message"] and "Ultralytics" in st["message"]


def test_collect_import_raising_os_error(monkeypatch, tmp_path: Path):
    # 缺少系统库时导入抛出的是 OSError（如 OpenCV 找不到 libGL.so.1），也要记录下来而不是崩溃。
    (tmp_path / "ultralytics.py").write_text(
        'raise OSError("libGL.so.1: cannot open shared object file")\n', encoding="utf-8"
    )
    torch = make_torch()
    install(monkeypatch, torch, make_torchvision(), None)
    monkeypatch.delitem(sys.modules, "ultralytics")
    monkeypatch.syspath_prepend(str(tmp_path))
    result = collect()
    assert result["ultralytics_version"] is None
    assert result["ultralytics_error"] == "OSError: libGL.so.1: cannot open shared object file"
    assert result["self_test"]["status"] == "skipped"
    assert "Ultralytics" in result["self_test"]["message"]


def test_collect_self_test_disabled(monkeypatch):
    torch = make_torch()
    install(monkeypatch, torch, make_torchvision(), make_ultralytics(torch.calls))
    st = collect(self_test=False)["self_test"]
    assert st["status"] == "skipped"
    assert "TM_SELF_TEST=false" in st["message"]
    assert "forward cpu" not in torch.calls


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        (91002, "9.10.2"),
        (92400, "9.24.0"),
        (8902, "8.9.2"),
        (8600, "8.6.0"),
        (None, None),
        (0, None),
        (True, None),
    ],
)
def test_format_cudnn_version(value, expected):
    assert format_cudnn_version(value) == expected


_CHILD_WITH_FAKES = textwrap.dedent(
    """
    import os, sys
    sys.path.insert(0, {tests!r})
    import fakes
    torch = fakes.make_torch()
    sys.modules["torch"] = torch
    sys.modules["torchvision"] = fakes.make_torchvision()
    sys.modules["ultralytics"] = fakes.make_ultralytics(torch.calls)
    print("noise printed before the child starts")
    from trainmaster_trainer.environment import child_main
    os.write(1, b"")
    sys.exit(child_main(sys.argv[1:]))
    """
)


def test_child_main_keeps_stdout_clean(tmp_path: Path):
    """ultralytics 打印到 stdout 的内容不能混进结果 JSON。"""
    env = dict(os.environ, PYTHONPATH=str(TRAINER_DIR))
    proc = subprocess.run(
        [sys.executable, "-c", _CHILD_WITH_FAKES.format(tests=str(TESTS_DIR))],
        capture_output=True,
        text=True,
        env=env,
        timeout=60,
        check=True,
    )
    lines = proc.stdout.splitlines()
    assert lines[0] == "noise printed before the child starts"
    data = json.loads(lines[1])
    assert len(lines) == 2
    assert data["self_test"]["status"] == "passed"
    assert "model summary" in proc.stderr


def test_child_main_no_self_test_flag(tmp_path: Path):
    env = dict(os.environ, PYTHONPATH=str(TRAINER_DIR))
    proc = subprocess.run(
        [sys.executable, "-c", _CHILD_WITH_FAKES.format(tests=str(TESTS_DIR)), "--no-self-test"],
        capture_output=True,
        text=True,
        env=env,
        timeout=60,
        check=True,
    )
    assert json.loads(proc.stdout.splitlines()[-1])["self_test"]["status"] == "skipped"


# ---- 父进程 ----

HARDWARE = {
    "os": "Debian",
    "kernel": "6.8",
    "arch": "x86_64",
    "in_container": True,
    "cpu": {},
    "memory": {},
    "shm_bytes": 8 << 30,
    "gpu_driver": None,
    "gpus": [],
    "gpu_detection": {"method": "none", "error": None, "host_gpu_count": None},
}

GOOD_RAW = {
    "python_version": "3.12.7",
    "torch_version": "2.14.1+cpu",
    "torch_cuda_version": None,
    "cudnn_version": None,
    "torch_arch_list": [],
    "torchvision_version": "0.29.1+cpu",
    "ultralytics_version": "8.4.198",
    "cuda_available": False,
    "cuda_device_count": 0,
    "torch_error": None,
    "torchvision_error": None,
    "ultralytics_error": None,
    "cuda_error": None,
    "self_test": {
        "status": "passed",
        "device": "cpu",
        "duration_ms": 900,
        "message": "YOLO11n 前向推理正常（CPU）",
        "detail": None,
    },
}


def script(tmp_path: Path, body: str) -> list[str]:
    path = tmp_path / "child.py"
    path.write_text(textwrap.dedent(body), encoding="utf-8")
    return [sys.executable, str(path)]


def detector(tmp_path: Path, command: list[str], timeout: float = 30, **kw) -> EnvironmentDetector:
    return EnvironmentDetector(
        state_dir=tmp_path / "state", torch_variant="cpu", timeout=timeout, command=command, **kw
    )


def test_detect_success(tmp_path: Path):
    cmd = script(
        tmp_path,
        f"""
        import json, os, sys
        raw = {GOOD_RAW!r}
        raw["self_test"]["message"] = "|".join(
            os.environ.get(k, "") for k in ("YOLO_OFFLINE", "YOLO_CONFIG_DIR", "TM_SELF_TEST")
        )
        print("log line", file=sys.stderr)
        print(json.dumps(raw))
        """,
    )
    env = detector(tmp_path, cmd).detect(HARDWARE)
    assert env["status"] == "cpu_only"
    assert env["torch_variant"] == "cpu"
    assert env["recommended_variant"] == "cpu"
    assert env["self_test"]["status"] == "passed"
    assert env["self_test"]["message"] == f"1|{tmp_path / 'state' / 'ultralytics'}|true"
    assert (tmp_path / "state" / "ultralytics").is_dir()
    assert env["probed_at"].endswith("Z")
    assert [i["code"] for i in env["issues"]] == ["no_gpu"]


def test_detect_self_test_disabled_env(tmp_path: Path):
    cmd = script(
        tmp_path,
        f"""
        import json, os
        raw = {GOOD_RAW!r}
        raw["self_test"] = {{"status": "skipped", "device": None, "duration_ms": None,
                            "message": os.environ["TM_SELF_TEST"], "detail": None}}
        print(json.dumps(raw))
        """,
    )
    env = detector(tmp_path, cmd, self_test=False).detect(HARDWARE)
    assert env["self_test"]["message"] == "false"


def test_detect_garbage_output(tmp_path: Path):
    cmd = script(tmp_path, 'print("this is not json")\n')
    env = detector(tmp_path, cmd).detect(HARDWARE)
    assert env["status"] == "error"
    st = env["self_test"]
    assert st["status"] == "failed"
    assert "输出无法解析" in st["message"]
    assert "this is not json" in st["detail"]
    assert [i["code"] for i in env["issues"]] == ["self_test_failed", "no_gpu"]


@pytest.mark.parametrize(
    "output",
    [
        "",
        "[1, 2]",
        json.dumps({"torch_version": "x"}),
        json.dumps(dict(GOOD_RAW, self_test="passed")),
    ],
)
def test_detect_malformed_json(tmp_path: Path, output):
    cmd = script(tmp_path, f"print({output!r})\n")
    raw = detector(tmp_path, cmd).run_child()
    assert raw["probe_error"] and "无法解析" in raw["probe_error"]


def test_detect_nonzero_exit(tmp_path: Path):
    cmd = script(
        tmp_path,
        """
        import sys
        print("Traceback (most recent call last):\\nImportError: libcudart.so.13", file=sys.stderr)
        sys.exit(3)
        """,
    )
    env = detector(tmp_path, cmd).detect(HARDWARE)
    st = env["self_test"]
    assert st["status"] == "failed"
    assert "退出码 3" in st["message"]
    assert "libcudart.so.13" in st["detail"]
    assert isinstance(st["duration_ms"], int)


def test_detect_killed_by_signal(tmp_path: Path):
    cmd = script(tmp_path, "import os, signal\nos.kill(os.getpid(), signal.SIGKILL)\n")
    raw = detector(tmp_path, cmd).run_child()
    assert "SIGKILL" in raw["self_test"]["message"]


def test_detect_timeout_kills_process_group(tmp_path: Path):
    marker = tmp_path / "grandchild-alive"
    cmd = script(
        tmp_path,
        f"""
        import subprocess, sys, time
        # 孙进程继承了管道：超时后必须连同它一起结束，否则读取输出会一直等待。
        subprocess.Popen([sys.executable, "-c",
            "import time, pathlib; time.sleep(3); pathlib.Path({str(marker)!r}).write_text('x')"])
        print("working", file=sys.stderr, flush=True)
        time.sleep(30)
        """,
    )
    started = time.monotonic()
    env = detector(tmp_path, cmd, timeout=1).detect(HARDWARE)
    assert time.monotonic() - started < 10
    st = env["self_test"]
    assert st["status"] == "failed"
    assert "超时" in st["message"] and "1 秒" in st["message"]
    assert "working" in (st["detail"] or "")
    time.sleep(3.5)
    assert not marker.exists()


def test_cancel_running_detection(tmp_path: Path):
    cmd = script(tmp_path, "import time\ntime.sleep(30)\n")
    det = detector(tmp_path, cmd, timeout=60)
    result = {}
    thread = threading.Thread(target=lambda: result.update(raw=det.run_child()))
    thread.start()
    deadline = time.monotonic() + 10
    while det._proc is None and time.monotonic() < deadline:
        time.sleep(0.02)
    det.cancel()
    thread.join(timeout=10)
    assert not thread.is_alive()
    assert "取消" in result["raw"]["self_test"]["message"]
    # 取消后不再启动新的子进程
    assert "取消" in det.run_child()["self_test"]["message"]


def test_detect_command_not_found(tmp_path: Path):
    raw = detector(tmp_path, [str(tmp_path / "missing-python")]).run_child()
    assert "无法启动" in raw["self_test"]["message"]


def test_real_child_module_without_torch(tmp_path: Path):
    """默认命令（python -m trainmaster_trainer.environment）在没有 torch 的测试环境中也能给出结果。"""
    det = EnvironmentDetector(state_dir=tmp_path, torch_variant=None, timeout=60)
    raw = det.run_child()
    if raw["torch_version"] is not None:
        pytest.skip("torch is installed in the test environment")
    assert raw["probe_error"] is None
    assert raw["torch_error"]
    assert raw["self_test"]["status"] == "skipped"


def test_sanitize_raw_coerces_types():
    raw = sanitize_raw(
        {
            "torch_version": 2,
            "torch_cuda_version": "",
            "torch_arch_list": ["sm_80", 5, None] + ["sm_90"] * 100,
            "cuda_available": "yes",
            "cuda_device_count": -1,
            "self_test": {
                "status": "weird",
                "duration_ms": True,
                "detail": "x" * 5000,
                "message": 3,
            },
        }
    )
    assert raw["torch_version"] is None and raw["torch_cuda_version"] is None
    assert raw["torch_arch_list"][0] == "sm_80" and len(raw["torch_arch_list"]) == 64
    assert raw["cuda_available"] is False
    assert raw["cuda_device_count"] == 0
    assert raw["self_test"]["status"] == "failed"
    assert raw["self_test"]["duration_ms"] is None
    assert raw["self_test"]["message"] is None
    assert len(raw["self_test"]["detail"]) == environment.MAX_DETAIL
    assert raw["probe_error"] is None


def test_failure_raw_shape():
    raw = failure_raw("出错了", "d" * 5000, 12)
    assert raw["probe_error"] == "出错了"
    assert raw["self_test"]["status"] == "failed"
    assert raw["self_test"]["duration_ms"] == 12
    assert len(raw["self_test"]["detail"]) == environment.MAX_DETAIL


def test_child_env_pythonpath_for_source_checkout(tmp_path: Path):
    env = environment.child_env(tmp_path, True, base={"PYTHONPATH": "/x"})
    assert env["PYTHONPATH"].split(os.pathsep) == ["/x", str(TRAINER_DIR)]
    assert env["YOLO_OFFLINE"] == "1"
    assert env["YOLO_CONFIG_DIR"] == str(tmp_path / "ultralytics")
    assert env["YOLO_VERBOSE"] == "False"
    again = environment.child_env(tmp_path, False, base=env)
    assert again["PYTHONPATH"] == env["PYTHONPATH"]
    assert again["TM_SELF_TEST"] == "false"


def test_summarize_exception_keeps_tail():
    try:
        raise ValueError("x" * 5000 + "END")
    except ValueError as exc:
        text = environment.summarize_exception(exc)
    assert len(text) == environment.MAX_DETAIL
    assert text.startswith("…") and text.endswith("END")
