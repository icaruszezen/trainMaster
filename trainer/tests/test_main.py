"""命令行：probe / selftest / healthcheck / run 的输出与退出码。

probe 与 selftest 以子进程运行完整流程（父进程 → 检测子进程）；torch / ultralytics 用
tests/fakes.py 中的假模块，通过 PYTHONPATH 上的同名模块文件加载。
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

import pytest
from fakes import TRAINER_DIR

from trainmaster_trainer import __main__ as cli
from trainmaster_trainer import __version__

TESTS_DIR = Path(__file__).resolve().parent

_FAKE_MODULE = """\
import sys
sys.path.insert(0, {tests!r})
import fakes
{body}
"""


def fake_ml(root: Path, *, torch: dict[str, Any] | None = None, ultralytics: bool = True) -> Path:
    """写出假的 torch / torchvision / ultralytics 模块文件，返回要加入 PYTHONPATH 的目录。"""
    root.mkdir(parents=True, exist_ok=True)
    tests = str(TESTS_DIR)
    if torch is not None:
        (root / "torch.py").write_text(
            _FAKE_MODULE.format(
                tests=tests, body=f"sys.modules[__name__] = fakes.make_torch(**{torch!r})"
            )
        )
        (root / "torchvision.py").write_text(
            _FAKE_MODULE.format(
                tests=tests, body="sys.modules[__name__] = fakes.make_torchvision()"
            )
        )
    if ultralytics:
        body = "import torch\nsys.modules[__name__] = fakes.make_ultralytics(torch.calls)"
        (root / "ultralytics.py").write_text(_FAKE_MODULE.format(tests=tests, body=body))
    return root


def run_cli(tmp_path: Path, *args: str, extra_path: Path | None = None, **env_extra: str):
    paths = [str(p) for p in (extra_path, TRAINER_DIR) if p is not None]
    env = {
        key: value
        for key, value in os.environ.items()
        if not key.startswith("TM_") and key not in ("PYTHONPATH", "TORCH_VARIANT")
    }
    env.update(
        PYTHONPATH=os.pathsep.join(paths),
        HOME=str(tmp_path / "home"),
        TM_STATE_DIR=str(tmp_path / "state"),
        TM_DATA_DIR=str(tmp_path),
        TM_NVIDIA_SMI=str(tmp_path / "no-nvidia-smi"),
        TM_TORCH_VARIANT="cpu",
    )
    env.update(env_extra)
    return subprocess.run(
        [sys.executable, "-m", "trainmaster_trainer", *args],
        env=env,
        capture_output=True,
        text=True,
        timeout=120,
    )


def test_selftest_json_cpu_passes(tmp_path):
    ml = fake_ml(tmp_path / "ml", torch={})
    proc = run_cli(tmp_path, "selftest", "--json", extra_path=ml)
    assert proc.returncode == 0, proc.stderr
    env = json.loads(proc.stdout)
    assert env["status"] == "cpu_only"
    assert env["accelerator"] == "cpu"
    assert env["torch_version"] == "2.14.1+cpu"
    assert env["torch_variant"] == "cpu"
    assert env["ultralytics_version"] == "8.4.198"
    assert env["self_test"]["status"] == "passed"
    assert env["self_test"]["message"] == "YOLO11n 前向推理正常（CPU）"
    codes = {i["code"] for i in env["issues"]}
    assert not {i["level"] for i in env["issues"]} & {"error"}
    assert "torch_missing" not in codes


def test_selftest_text_summary(tmp_path):
    ml = fake_ml(tmp_path / "ml", torch={})
    proc = run_cli(tmp_path, "selftest", extra_path=ml)
    assert proc.returncode == 0, proc.stderr
    assert "CUDA 环境：CPU 模式" in proc.stdout
    assert "训练设备：CPU" in proc.stdout
    assert "PyTorch 2.14.1+cpu（CPU 版）" in proc.stdout
    assert "自检：通过 — YOLO11n 前向推理正常（CPU）" in proc.stdout


def test_selftest_without_torch_exits_1(tmp_path):
    ml = tmp_path / "empty"
    ml.mkdir()
    # 不依赖测试环境是否装了 torch：PYTHONPATH 上放一个导入即失败的同名模块
    (ml / "torch.py").write_text("raise ImportError('No module named torch')\n")
    proc = run_cli(tmp_path, "selftest", extra_path=ml)
    assert proc.returncode == 1
    assert "CUDA 环境：异常" in proc.stdout
    assert "[错误]" in proc.stdout and "建议：" in proc.stdout


def test_selftest_disabled(tmp_path):
    ml = fake_ml(tmp_path / "ml", torch={})
    proc = run_cli(tmp_path, "selftest", "--json", extra_path=ml, TM_SELF_TEST="false")
    assert proc.returncode == 0, proc.stderr
    assert json.loads(proc.stdout)["self_test"]["status"] == "skipped"


def test_probe_prints_full_report(tmp_path):
    ml = fake_ml(tmp_path / "ml", torch={})
    (tmp_path / "state").mkdir()
    (tmp_path / "state" / "node-key").write_text("probe-key-1\n")
    proc = run_cli(tmp_path, "probe", extra_path=ml)
    assert proc.returncode == 0, proc.stderr
    report = json.loads(proc.stdout)
    assert set(report) == {"agent_version", "node_key", "hardware", "environment", "metrics"}
    assert report["agent_version"] == __version__
    assert report["node_key"] == "probe-key-1"
    hw = report["hardware"]
    assert isinstance(hw["gpus"], list)
    assert hw["cpu"]["logical_cores"] >= 1
    assert hw["memory"]["total_bytes"] > 0
    assert hw["gpu_detection"]["method"] in ("nvml", "nvidia-smi", "none")
    assert report["environment"]["status"] == "cpu_only"
    assert report["metrics"]["memory_used_bytes"] > 0
    assert isinstance(report["metrics"]["gpus"], list)


def test_probe_does_not_create_node_key(tmp_path):
    ml = fake_ml(tmp_path / "ml", torch={})
    proc = run_cli(tmp_path, "probe", extra_path=ml)
    assert proc.returncode == 0, proc.stderr
    assert json.loads(proc.stdout)["node_key"] is None
    assert not (tmp_path / "state" / "node-key").exists()


def test_invalid_config_exits_2(tmp_path):
    proc = run_cli(tmp_path, "probe", TM_LOG_LEVEL="loud")
    assert proc.returncode == 2
    assert proc.stderr.startswith("配置错误：TM_LOG_LEVEL")
    assert proc.stdout == ""


def test_run_without_server_url_exits_2(tmp_path):
    proc = run_cli(tmp_path, "run")
    assert proc.returncode == 2
    assert "配置错误：" in proc.stderr and "TM_SERVER_URL" in proc.stderr


def test_default_command_is_run(tmp_path):
    proc = run_cli(tmp_path)
    assert proc.returncode == 2
    assert "TM_SERVER_URL" in proc.stderr


def test_healthcheck_cli(tmp_path):
    proc = run_cli(tmp_path, "healthcheck")
    assert proc.returncode == 1
    assert "没有心跳记录" in proc.stderr

    state = tmp_path / "state"
    state.mkdir()
    (state / "heartbeat").write_text(json.dumps({"unix": time.time(), "interval_seconds": 10}))
    proc = run_cli(tmp_path, "healthcheck")
    assert proc.returncode == 0, proc.stderr
    assert "秒前" in proc.stdout

    (state / "heartbeat").write_text(
        json.dumps({"unix": time.time() - 3600, "interval_seconds": 10})
    )
    assert run_cli(tmp_path, "healthcheck").returncode == 1


def test_healthcheck_finds_fallback_state_dir(tmp_path):
    fallback = tmp_path / "home" / ".local" / "state" / "trainmaster"
    fallback.mkdir(parents=True)
    (fallback / "heartbeat").write_text(json.dumps({"unix": time.time(), "interval_seconds": 10}))
    proc = run_cli(tmp_path, "healthcheck", TM_STATE_DIR=str(tmp_path / "unwritable"))
    assert proc.returncode == 0, proc.stderr


def test_healthcheck_finds_temp_state_dir_of_puid(tmp_path):
    # Docker 以 root 运行 HEALTHCHECK；训练节点以 PUID 运行并退回到了 /tmp/trainmaster-<PUID>。
    tmp = tmp_path / "tmp"
    fallback = tmp / "trainmaster-4321"
    fallback.mkdir(parents=True)
    (fallback / "heartbeat").write_text(json.dumps({"unix": time.time(), "interval_seconds": 10}))
    proc = run_cli(tmp_path, "healthcheck", TMPDIR=str(tmp))
    assert proc.returncode == 1
    proc = run_cli(tmp_path, "healthcheck", TMPDIR=str(tmp), PUID="4321")
    assert proc.returncode == 0, proc.stderr


def test_version_and_help(capsys):
    with pytest.raises(SystemExit) as info:
        cli.main(["--version"])
    assert info.value.code == 0
    assert __version__ in capsys.readouterr().out
    with pytest.raises(SystemExit):
        cli.main(["--help"])
    out = capsys.readouterr().out
    for command in ("run", "probe", "selftest", "healthcheck"):
        assert command in out
    with pytest.raises(SystemExit) as info:
        cli.main(["bogus"])
    assert info.value.code == 2


# ---- format_environment ----


def ready_env(**overrides: Any) -> dict[str, Any]:
    env: dict[str, Any] = {
        "status": "ready",
        "accelerator": "cuda",
        "python_version": "3.12.12",
        "torch_version": "2.14.1+cu130",
        "torch_cuda_version": "13.0",
        "cudnn_version": "9.24.0",
        "torchvision_version": "0.29.1+cu130",
        "ultralytics_version": "8.4.198",
        "cuda_device_count": 2,
        "torch_variant": "cu130",
        "recommended_variant": "cu130",
        "self_test": {
            "status": "passed",
            "device": "cuda",
            "duration_ms": 812,
            "message": "YOLO11n 前向推理正常（2 块 GPU，FP16）",
            "detail": None,
        },
        "issues": [],
    }
    env.update(overrides)
    return env


def test_format_environment_ready():
    text = cli.format_environment(ready_env())
    assert text.splitlines()[0] == "CUDA 环境：就绪（GPU 与 CUDA 可用，自检通过）"
    assert "训练设备：GPU（2 块）" in text
    assert "PyTorch 2.14.1+cu130（CUDA 13.0，cuDNN 9.24.0）" in text
    assert "Ultralytics 8.4.198" in text
    assert "已安装 cu130，按本机驱动与显卡推荐 cu130" in text
    assert "自检：通过 — YOLO11n 前向推理正常（2 块 GPU，FP16）（用时 812 ms）" in text
    assert "问题：" not in text


def test_format_environment_error_with_detail_and_issues():
    env = ready_env(
        status="error",
        accelerator="cpu",
        torch_variant=None,
        self_test={
            "status": "failed",
            "device": "cuda",
            "duration_ms": None,
            "message": "自检失败",
            "detail": "Traceback\nRuntimeError: CUDA error",
        },
        issues=[
            {
                "level": "error",
                "code": "self_test_failed",
                "message": "自检失败",
                "hint": "重启容器",
            },
            {
                "level": "info",
                "code": "variant_upgrade_available",
                "message": "可升级",
                "hint": None,
            },
        ],
    )
    text = cli.format_environment(env)
    assert "CUDA 环境：异常" in text
    assert "训练设备：CPU" in text
    assert "已安装 未知" in text
    assert "    RuntimeError: CUDA error" in text
    assert "  [错误] 自检失败\n      建议：重启容器" in text
    assert "  [提示] 可升级" in text
    assert "用时" not in text


def test_format_environment_minimal():
    text = cli.format_environment({"status": "error", "issues": []})
    assert "  版本：Python ?\n" in text
    assert "已安装 未知，按本机驱动与显卡推荐 未知" in text
    assert text.endswith("  自检：未知")
