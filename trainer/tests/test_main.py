"""命令行：probe / selftest / healthcheck / run 的输出与退出码。

probe 与 selftest 以子进程运行完整流程（父进程 → 检测子进程）；torch / ultralytics 用
tests/fakes.py 中的假模块，通过 PYTHONPATH 上的同名模块文件加载。
"""

from __future__ import annotations

import io
import json
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

import pytest
from fakes import TRAINER_DIR, WINDOWS

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
    """写出假的 torch / torchvision / ultralytics 模块文件，返回要加入 PYTHONPATH 的目录。

    Python 按 UTF-8 读取源文件：写入时同样指定 UTF-8（路径中可能有中文，Windows 默认使用 ANSI 代码页）。"""
    root.mkdir(parents=True, exist_ok=True)
    tests = str(TESTS_DIR)
    if torch is not None:
        (root / "torch.py").write_text(
            _FAKE_MODULE.format(
                tests=tests, body=f"sys.modules[__name__] = fakes.make_torch(**{torch!r})"
            ),
            encoding="utf-8",
        )
        (root / "torchvision.py").write_text(
            _FAKE_MODULE.format(
                tests=tests, body="sys.modules[__name__] = fakes.make_torchvision()"
            ),
            encoding="utf-8",
        )
    if ultralytics:
        body = "import torch\nsys.modules[__name__] = fakes.make_ultralytics(torch.calls)"
        (root / "ultralytics.py").write_text(
            _FAKE_MODULE.format(tests=tests, body=body), encoding="utf-8"
        )
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
        # 命令行按 stdout / stderr 的编码打印中文。Windows 上输出到管道时默认使用系统的代码页（runner 上是
        # 不能表示中文的 cp1252），这里与 install.ps1 一样指定 UTF-8。
        PYTHONIOENCODING="utf-8",
    )
    env.update(env_extra)
    return subprocess.run(
        [sys.executable, "-m", "trainmaster_trainer", *args],
        env=env,
        capture_output=True,
        encoding="utf-8",
        errors="replace",
        timeout=120,
    )


def run_healthcheck(tmp_path: Path, **env_extra: str):
    # Windows 上退回的状态目录在 %LOCALAPPDATA% 下（不使用 HOME）：同样放在临时目录中，不查找本机真实的目录。
    env_extra.setdefault("LOCALAPPDATA", str(tmp_path / "home" / "AppData" / "Local"))
    return run_cli(tmp_path, "healthcheck", **env_extra)


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
    (ml / "torch.py").write_text("raise ImportError('No module named torch')\n", encoding="utf-8")
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


def test_detect_passes_the_probe_diagnostics(tmp_path, monkeypatch):
    # probe / selftest 与训练节点一样把只用于诊断的探测结果交给检测（如显卡没有传入容器、宿主机驱动
    # 过旧时提示升级驱动，而不是安装 NVIDIA Container Toolkit）。
    import signal

    from trainmaster_trainer import environment, gpu, hardware
    from trainmaster_trainer.config import Config

    probe = hardware.HardwareProbe(
        hardware={"gpus": []},
        gpu=gpu.GpuProbe(None, [], {"method": "none", "error": None, "host_gpu_count": 1}),
        device_nodes=["nvidiactl"],
        pci_gpu_count=1,
        host_driver_version="440.100",
    )
    seen: dict[str, Any] = {}

    def detect(self, hw, **kw):
        seen.update(kw, hardware=hw)
        return {"status": "cpu_only"}

    monkeypatch.setattr(hardware, "probe", lambda: probe)
    monkeypatch.setattr(environment.EnvironmentDetector, "detect", detect)
    old_handler = signal.getsignal(signal.SIGTERM)
    try:
        assert cli._detect(Config(state_dir=tmp_path)) == (probe, {"status": "cpu_only"})
    finally:
        signal.signal(signal.SIGTERM, old_handler)
    assert seen == {
        "hardware": probe.hardware,
        "device_nodes": ["nvidiactl"],
        "pci_gpu_count": 1,
        "host_driver_version": "440.100",
    }


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
    proc = run_healthcheck(tmp_path)
    assert proc.returncode == 1
    assert "没有心跳记录" in proc.stderr

    state = tmp_path / "state"
    state.mkdir()
    (state / "heartbeat").write_text(json.dumps({"unix": time.time(), "interval_seconds": 10}))
    proc = run_healthcheck(tmp_path)
    assert proc.returncode == 0, proc.stderr
    assert "秒前" in proc.stdout

    (state / "heartbeat").write_text(
        json.dumps({"unix": time.time() - 3600, "interval_seconds": 10})
    )
    assert run_healthcheck(tmp_path).returncode == 1


def test_healthcheck_finds_fallback_state_dir(tmp_path):
    # 状态目录不可写时退回的目录：Linux 为 ~/.local/state/trainmaster，Windows 为 %LOCALAPPDATA%\trainMaster\state
    if WINDOWS:
        fallback = tmp_path / "home" / "AppData" / "Local" / "trainMaster" / "state"
    else:
        fallback = tmp_path / "home" / ".local" / "state" / "trainmaster"
    fallback.mkdir(parents=True)
    (fallback / "heartbeat").write_text(json.dumps({"unix": time.time(), "interval_seconds": 10}))
    proc = run_healthcheck(tmp_path, TM_STATE_DIR=str(tmp_path / "unwritable"))
    assert proc.returncode == 0, proc.stderr


def test_healthcheck_finds_temp_state_dir_of_puid(tmp_path):
    # Docker 以 root 运行 HEALTHCHECK；训练节点以 PUID 运行并退回到了 /tmp/trainmaster-<PUID>。
    tmp = tmp_path / "tmp"
    fallback = tmp / "trainmaster-4321"
    fallback.mkdir(parents=True)
    (fallback / "heartbeat").write_text(json.dumps({"unix": time.time(), "interval_seconds": 10}))
    proc = run_healthcheck(tmp_path, TMPDIR=str(tmp))
    assert proc.returncode == 1
    proc = run_healthcheck(tmp_path, TMPDIR=str(tmp), PUID="4321")
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


# ---- --env-file（Windows 的计划任务用它读取 trainer.env）----


def test_env_file_is_applied_before_loading_config(tmp_path):
    env_file = tmp_path / "trainer.env"
    env_file.write_text("TM_LOG_LEVEL='loud'\n", encoding="utf-8")
    # 配置文件中的值覆盖环境变量：TM_LOG_LEVEL 不合法，报告配置错误
    proc = run_cli(tmp_path, "probe", "--env-file", str(env_file), TM_LOG_LEVEL="info")
    assert proc.returncode == 2
    assert proc.stderr.startswith("配置错误：TM_LOG_LEVEL")
    # 也可以写在命令之前
    proc = run_cli(tmp_path, "--env-file", str(env_file), "probe", TM_LOG_LEVEL="info")
    assert proc.returncode == 2


def test_env_file_errors_exit_2(tmp_path):
    proc = run_cli(tmp_path, "run", "--env-file", str(tmp_path / "missing.env"))
    assert proc.returncode == 2
    assert "配置错误：配置文件" in proc.stderr and "不存在" in proc.stderr
    bad = tmp_path / "bad.env"
    bad.write_text("TM_SERVER_URL='http://x\n", encoding="utf-8")
    proc = run_cli(tmp_path, "run", "--env-file", str(bad))
    assert proc.returncode == 2
    assert "第 1 行（TM_SERVER_URL）：缺少结尾的引号" in proc.stderr


def test_apply_env_file_sets_tm_env_file(tmp_path, monkeypatch):
    env_file = tmp_path / "trainer.env"
    env_file.write_text("TM_NODE_NAME='节点 1'\nCUDA_VISIBLE_DEVICES=1\n", encoding="utf-8")
    monkeypatch.setenv("TM_NODE_NAME", "old")
    # 先 setenv 再 delenv：monkeypatch 记录原来的值，测试结束时还原（apply_env_file 写入的值不留给其它测试）
    for key in ("TM_ENV_FILE", "CUDA_VISIBLE_DEVICES"):
        monkeypatch.setenv(key, "")
        monkeypatch.delenv(key)
    assert cli.apply_env_file(str(env_file))
    assert os.environ["TM_NODE_NAME"] == "节点 1"
    assert os.environ["CUDA_VISIBLE_DEVICES"] == "1"
    assert os.environ["TM_ENV_FILE"] == str(env_file.absolute())
    assert cli.apply_env_file(None)


def test_env_file_reaches_the_fix_commands(tmp_path):
    # TM_ENV_FILE 是 Deployment.env_file（Windows 修复建议中的 -EnvFile 与 selftest --env-file）
    from trainmaster_trainer.environment import deployment_from

    env = {"TM_STATE_DIR": str(tmp_path / "s"), "TM_ENV_FILE": str(tmp_path / "t.env")}
    cfg = config_load(env)
    assert deployment_from(cfg).env_file == str(tmp_path / "t.env")


def config_load(env):
    from trainmaster_trainer import config

    return config.load(env)


# ---- recommend（安装脚本使用）----

SMI_GPUS = (
    "0, GPU-aaaa, NVIDIA GeForce RTX 4090, 24564, 1024, 35, 45, 120.40, 450.00, 30, 8.9, "
    "00000000:01:00.0\n"
)
SMI_HEADER = (
    "| NVIDIA-SMI 581.57                 Driver Version: 581.57         CUDA Version: 13.0     |\n"
)


def test_recommend_json_without_gpu(tmp_path):
    proc = run_cli(tmp_path, "recommend", "--json")
    assert proc.returncode == 0, proc.stderr
    report = json.loads(proc.stdout)
    assert report["recommended"]["variant"] == "cpu"
    assert report["variant"]["name"] == "cpu"
    assert report["variant"]["torch"] and report["variant"]["torchvision"]
    assert report["gpus"] == [] and report["driver"] is None
    assert report["variants"] == ["cu130", "cu126", "cu128", "cu118", "cpu"]
    assert report["platform"] == ("windows" if sys.platform == "win32" else "linux")


def test_recommend_with_nvidia_smi(tmp_path):
    from fakes import write_fake_smi

    smi = write_fake_smi(tmp_path / "smi", SMI_GPUS, SMI_HEADER)
    proc = run_cli(tmp_path, "recommend", "--json", TM_NVIDIA_SMI=str(smi))
    assert proc.returncode == 0, proc.stderr
    report = json.loads(proc.stdout)
    assert report["driver"] == "581.57"
    assert report["gpus"][0]["name"] == "NVIDIA GeForce RTX 4090"
    assert report["gpus"][0]["compute_capability"] == "8.9"
    assert report["recommended"]["variant"] == "cu130"
    assert report["variant"] == {
        "name": "cu130",
        "torch": "2.14.1",
        "torchvision": "0.29.1",
        "source": "pypi",
        "cuda": "13.0",
        "min_driver": "580",
    }
    # 手动指定变体：推荐不变，variant 为指定的变体（Windows 上最低驱动取 Windows 的值）
    proc = run_cli(tmp_path, "recommend", "--json", "--variant", "cu126", TM_NVIDIA_SMI=str(smi))
    report = json.loads(proc.stdout)
    assert report["recommended"]["variant"] == "cu130"
    assert report["variant"]["name"] == "cu126"
    assert report["variant"]["min_driver"] == ("528.33" if sys.platform == "win32" else "525.60.13")
    # 文字摘要
    proc = run_cli(tmp_path, "recommend", "--variant", "cu126", TM_NVIDIA_SMI=str(smi))
    assert proc.returncode == 0, proc.stderr
    assert "[0] NVIDIA GeForce RTX 4090（显存 24 GiB，计算能力 8.9）" in proc.stdout
    assert "驱动：581.57" in proc.stdout
    assert "推荐的 PyTorch 变体：cu130" in proc.stdout
    assert "使用手动指定的变体：cu126" in proc.stdout


def test_recommend_unknown_variant(tmp_path):
    proc = run_cli(tmp_path, "recommend", "--variant", "cu999")
    assert proc.returncode == 2
    assert "未知的变体 cu999（可选：cu130 cu126 cu128 cu118 cpu）" in proc.stderr


def test_format_recommend_without_driver():
    report = {
        "platform": "windows",
        "arch": "AMD64",
        "gpus": [],
        "driver": None,
        "pci_gpu_count": 2,
        "assumed_cc": "7.5",
        "detection": {"error": "NVML Shared Library Not Found"},
        "recommended": {"variant": "cpu", "reason": "未检测到 NVIDIA 显卡，使用 CPU 版 PyTorch"},
        "variant": {"name": "cpu"},
    }
    text = cli.format_recommend(report)
    assert "平台：windows AMD64" in text
    assert "检测到 2 块 NVIDIA 显卡，但没有可用的驱动" in text
    report["pci_gpu_count"] = None
    assert "显卡：未检测到 NVIDIA 显卡\n" in cli.format_recommend(report)
    report["detection"] = {"error": "Unknown Error"}
    assert "未检测到 NVIDIA 显卡（Unknown Error）" in cli.format_recommend(report)


# ---- TM_LOG_FILE ----


def test_setup_logging_writes_log_file(tmp_path):
    import logging

    path = tmp_path / "logs" / "trainer.log"
    try:
        cli.setup_logging(logging.INFO, path)
        logging.getLogger("trainmaster_trainer.test").info("hello 你好")
        for handler in logging.getLogger().handlers:
            handler.flush()
        assert "hello 你好" in path.read_text(encoding="utf-8")
    finally:
        for handler in logging.getLogger().handlers:
            handler.close()
        cli.setup_logging(logging.WARNING)


def test_setup_logging_reports_unwritable_log_file(tmp_path, capsys):
    import logging

    blocker = tmp_path / "file"
    blocker.write_text("x")
    try:
        cli.setup_logging(logging.INFO, blocker / "trainer.log")
        assert "cannot write log file" in capsys.readouterr().err
    finally:
        cli.setup_logging(logging.WARNING)


def test_log_file_rotates(tmp_path, monkeypatch):
    import logging

    monkeypatch.setattr(cli, "LOG_FILE_MAX_BYTES", 200)
    handler = cli.LogFileHandler(tmp_path / "t.log")
    handler.maxBytes = 200
    handler.setFormatter(logging.Formatter("%(message)s"))
    try:
        for i in range(20):
            handler.emit(logging.makeLogRecord({"msg": f"line {i} " + "x" * 40}))
    finally:
        handler.close()
    assert (tmp_path / "t.log.1").is_file()
    assert (tmp_path / "t.log").stat().st_size <= 200


def test_log_file_keeps_writing_when_rotation_fails(tmp_path, monkeypatch):
    # Windows：日志文件被其它程序打开时无法改名。这次不轮转，1 分钟后再试，日志不丢失。
    import logging

    now = [1000.0]
    handler = cli.LogFileHandler(tmp_path / "t.log", clock=lambda: now[0])
    handler.maxBytes = 100
    handler.setFormatter(logging.Formatter("%(message)s"))
    attempts = []

    def failing_rotate(source, dest):
        attempts.append(dest)
        raise PermissionError(32, "另一个程序正在使用此文件，进程无法访问。")

    handler.rotator = failing_rotate
    try:
        for i in range(10):
            handler.emit(logging.makeLogRecord({"msg": f"line {i} " + "y" * 30}))
        assert len(attempts) == 1  # 失败后 60 秒内不再尝试
        now[0] += cli.LOG_ROTATE_RETRY + 1
        handler.emit(logging.makeLogRecord({"msg": "after retry"}))
        assert len(attempts) == 2
    finally:
        handler.close()
    text = (tmp_path / "t.log").read_text(encoding="utf-8")
    assert all(f"line {i} " in text for i in range(10))
    assert "after retry" in text


def test_recommend_runs_without_psutil_and_nvml(tmp_path):
    # 安装脚本在创建虚拟环境之前用系统的 Python 运行 recommend：只有标准库可用。
    code = (
        "import sys\n"
        "sys.modules['psutil'] = None\n"
        "sys.modules['pynvml'] = None\n"
        "from trainmaster_trainer.__main__ import main\n"
        "sys.exit(main(['recommend', '--json']))\n"
    )
    env = {k: v for k, v in os.environ.items() if not k.startswith("TM_")}
    env.update(PYTHONPATH=str(TRAINER_DIR), TM_NVIDIA_SMI=str(tmp_path / "missing"))
    proc = subprocess.run(
        [sys.executable, "-c", code], env=env, capture_output=True, text=True, timeout=120
    )
    assert proc.returncode == 0, proc.stderr
    assert proc.stdout.isascii()
    report = json.loads(proc.stdout)
    assert report["recommended"]["variant"] == "cpu"
    assert "nvidia-ml-py is not installed" in (report["detection"]["error"] or "")


def test_tolerate_unencodable_output(monkeypatch):
    # Windows 上输出重定向时按代码页编码：英文系统的 cp1252 没有中文，不能因此抛出 UnicodeEncodeError
    out = io.TextIOWrapper(io.BytesIO(), encoding="cp1252", newline="\n")
    monkeypatch.setattr(sys, "stdout", out)
    cli.tolerate_unencodable_output(windows=False)
    with pytest.raises(UnicodeEncodeError):
        print("推荐的变体")
    cli.tolerate_unencodable_output(windows=True)
    print("推荐的变体 cu130")
    out.flush()
    assert out.buffer.getvalue() == b"\\u63a8\\u8350\\u7684\\u53d8\\u4f53 cu130\n"
