"""命令行入口：python -m trainmaster_trainer [run|probe|selftest|healthcheck]

run          注册到平台并持续上报心跳（默认；需要 TM_SERVER_URL）
probe        打印完整的检测报告（硬件、CUDA 环境、实时指标，JSON），不连接平台，便于排障
selftest     检测 CUDA / PyTorch / YOLO 环境并执行自检；--json 输出 Environment
healthcheck  最近一次心跳是否及时（Docker HEALTHCHECK 使用）
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import signal
import sys
from pathlib import Path
from typing import Any

from . import __version__
from . import config as config_mod

log = logging.getLogger("trainmaster_trainer")

_LEVEL_TEXT = {"error": "错误", "warning": "警告", "info": "提示"}
_STATUS_TEXT = {
    "ready": "就绪（GPU 与 CUDA 可用，自检通过）",
    "cpu_only": "CPU 模式（没有可用的 GPU）",
    "error": "异常",
}
_SELF_TEST_TEXT = {"passed": "通过", "failed": "失败", "skipped": "跳过"}


def setup_logging(level: int) -> None:
    logging.basicConfig(
        stream=sys.stderr,
        level=level,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        datefmt="%Y-%m-%dT%H:%M:%S%z",
        force=True,
    )


def _load_config(require_server: bool = False) -> config_mod.Config | None:
    try:
        return config_mod.load(require_server=require_server)
    except config_mod.ConfigError as exc:
        print(f"配置错误：{exc}", file=sys.stderr)
        return None


def _raise_interrupt(signum: int, frame: Any) -> None:
    raise KeyboardInterrupt


def cmd_run() -> int:
    cfg = _load_config(require_server=True)
    if cfg is None:
        return 2
    setup_logging(cfg.log_level)
    from .agent import Agent

    agent = Agent(cfg)

    def handle(signum: int, frame: Any) -> None:
        log.info("received %s, stopping", signal.Signals(signum).name)
        agent.stop()

    signal.signal(signal.SIGTERM, handle)
    signal.signal(signal.SIGINT, handle)
    return agent.run()


def _detect(cfg: config_mod.Config) -> tuple[Any, dict[str, Any]]:
    from . import hardware
    from .environment import EnvironmentDetector

    probe = hardware.probe()
    detector = EnvironmentDetector(
        state_dir=cfg.state_dir,
        torch_variant=cfg.torch_variant,
        self_test=cfg.self_test,
        timeout=cfg.self_test_timeout,
    )
    # Ctrl+C / docker stop 时结束检测子进程（run_child 收到 KeyboardInterrupt 会终止它）。
    signal.signal(signal.SIGTERM, _raise_interrupt)
    env = detector.detect(
        probe.hardware, device_nodes=probe.device_nodes, pci_gpu_count=probe.pci_gpu_count
    )
    return probe, env


def _quiet_level(cfg: config_mod.Config) -> int:
    # probe / selftest 的结果打印在 stdout；日志默认只显示警告，TM_LOG_LEVEL=debug 时显示全部。
    return cfg.log_level if cfg.log_level == logging.DEBUG else logging.WARNING


def cmd_probe() -> int:
    cfg = _load_config()
    if cfg is None:
        return 2
    setup_logging(_quiet_level(cfg))
    from . import hardware, node_key

    metrics = hardware.MetricsCollector(cfg.data_dir)
    metrics.prime()
    try:
        probe, env = _detect(cfg)
    except KeyboardInterrupt:
        return 130
    metrics.sampler = probe.gpu.sampler
    report = {
        "agent_version": __version__,
        "node_key": node_key.read(cfg.state_dir),
        "hardware": probe.hardware,
        "environment": env,
        "metrics": metrics.collect(),
    }
    probe.gpu.sampler.close()
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


def format_environment(env: dict[str, Any]) -> str:
    """selftest 的中文摘要。"""
    lines = [f"CUDA 环境：{_STATUS_TEXT.get(env.get('status'), env.get('status'))}"]
    if env.get("accelerator") == "cuda":
        lines.append(f"  训练设备：GPU（{env.get('cuda_device_count')} 块）")
    else:
        lines.append("  训练设备：CPU")
    versions = [f"Python {env.get('python_version') or '?'}"]
    if env.get("torch_version"):
        torch = f"PyTorch {env['torch_version']}"
        extra = []
        if env.get("torch_cuda_version"):
            extra.append(f"CUDA {env['torch_cuda_version']}")
        if env.get("cudnn_version"):
            extra.append(f"cuDNN {env['cudnn_version']}")
        versions.append(torch + (f"（{'，'.join(extra)}）" if extra else "（CPU 版）"))
    if env.get("torchvision_version"):
        versions.append(f"torchvision {env['torchvision_version']}")
    if env.get("ultralytics_version"):
        versions.append(f"Ultralytics {env['ultralytics_version']}")
    lines.append("  版本：" + " · ".join(versions))
    lines.append(
        f"  PyTorch 变体：已安装 {env.get('torch_variant') or '未知'}，"
        f"按本机驱动与显卡推荐 {env.get('recommended_variant') or '未知'}"
    )
    st = env.get("self_test") or {}
    text = f"  自检：{_SELF_TEST_TEXT.get(st.get('status'), st.get('status') or '未知')}"
    if st.get("message"):
        text += f" — {st['message']}"
    if st.get("duration_ms") is not None:
        text += f"（用时 {st['duration_ms']} ms）"
    lines.append(text)
    if st.get("status") == "failed" and st.get("detail"):
        lines.append("  错误详情：")
        lines.extend("    " + line for line in st["detail"].splitlines())
    items = env.get("issues") or []
    if items:
        lines.append("问题：")
        for item in items:
            lines.append(f"  [{_LEVEL_TEXT.get(item['level'], item['level'])}] {item['message']}")
            if item.get("hint"):
                lines.append(f"      建议：{item['hint']}")
    return "\n".join(lines)


def cmd_selftest(as_json: bool) -> int:
    cfg = _load_config()
    if cfg is None:
        return 2
    setup_logging(_quiet_level(cfg))
    try:
        probe, env = _detect(cfg)
    except KeyboardInterrupt:
        return 130
    probe.gpu.sampler.close()
    if as_json:
        print(json.dumps(env, ensure_ascii=False, indent=2))
    else:
        print(format_environment(env))
    return 1 if env.get("status") == "error" else 0


def cmd_healthcheck() -> int:
    from .agent import check_heartbeat

    configured = Path(os.environ.get("TM_STATE_DIR", "").strip() or config_mod.DEFAULT_STATE_DIR)
    ok, message = check_heartbeat(
        config_mod.state_dir_candidates(configured, os.environ.get("HOME"))
    )
    print(message, file=sys.stdout if ok else sys.stderr)
    return 0 if ok else 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="python -m trainmaster_trainer",
        description="trainMaster 训练节点：自动识别显卡、CPU 与 CUDA 环境，向平台注册并上报心跳。",
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    sub = parser.add_subparsers(dest="command", metavar="命令")
    sub.add_parser("run", help="注册到平台并持续上报心跳（默认）")
    sub.add_parser("probe", help="打印硬件、CUDA 环境与实时指标（JSON），不连接平台")
    selftest = sub.add_parser("selftest", help="检测 CUDA / PyTorch / YOLO 环境并执行自检")
    selftest.add_argument("--json", action="store_true", help="以 JSON 输出检测结果（Environment）")
    sub.add_parser("healthcheck", help="检查最近一次心跳是否及时（Docker HEALTHCHECK 使用）")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    command = args.command or "run"
    if command == "run":
        return cmd_run()
    if command == "probe":
        return cmd_probe()
    if command == "selftest":
        return cmd_selftest(args.json)
    return cmd_healthcheck()


if __name__ == "__main__":
    sys.exit(main())
