"""命令行入口：python -m trainmaster_trainer [run|probe|selftest|export|infer|healthcheck]

run          注册到平台并持续上报心跳、执行平台的任务（默认；需要 TM_SERVER_URL）
probe        打印完整的检测报告（硬件、CUDA 环境、实时指标，JSON），不连接平台，便于排障
selftest     检测 CUDA / PyTorch / YOLO 环境并执行自检；--json 输出 Environment
export       在本机把 pt 转换为 ONNX（与平台的转换任务使用同一个子进程），不连接平台，便于排障
infer        在本机用 pt 识别图片（与平台的审图识别任务使用同一个子进程），不连接平台，便于排障
healthcheck  最近一次心跳是否及时（Docker HEALTHCHECK 使用）
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import signal
import sys
import time
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
    from .environment import EnvironmentDetector, deployment_from

    probe = hardware.probe()
    detector = EnvironmentDetector(
        state_dir=cfg.state_dir,
        torch_variant=cfg.torch_variant,
        self_test=cfg.self_test,
        timeout=cfg.self_test_timeout,
        deployment=deployment_from(cfg),
    )
    # Ctrl+C / docker stop 时结束检测子进程（run_child 收到 KeyboardInterrupt 会终止它）。
    signal.signal(signal.SIGTERM, _raise_interrupt)
    env = detector.detect(
        probe.hardware,
        device_nodes=probe.device_nodes,
        pci_gpu_count=probe.pci_gpu_count,
        host_driver_version=probe.host_driver_version,
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


_POSTPROCESS_TEXT = {
    "raw": "原始输出（raw）",
    "nms": "嵌入 NMS（nms）",
    "end2end": "端到端（end2end）",
}


def parse_imgsz(raw: str) -> tuple[int, int]:
    """--imgsz：640（正方形）或 高x宽 / 高,宽（如 480x640）；每边 32–4096 且是 32 的倍数（与平台相同）。"""
    parts = [p.strip() for p in raw.lower().replace(",", "x").replace("×", "x").split("x")]
    if len(parts) not in (1, 2) or not all(p.isdigit() for p in parts):
        raise argparse.ArgumentTypeError(f"{raw!r} 不是合法的输入尺寸（如 640 或 480x640）")
    values = [int(p) for p in parts]
    if any(not 32 <= v <= 4096 or v % 32 for v in values):
        raise argparse.ArgumentTypeError(f"{raw!r}：每边应在 32–4096 之间且是 32 的倍数")
    return (values[0], values[-1])


def parse_opset(raw: str) -> int:
    try:
        value = int(raw)
    except ValueError:
        value = 0
    if not 11 <= value <= 21:
        raise argparse.ArgumentTypeError(f"{raw!r}：opset 应为 11–21 的整数（不指定时自动选择）")
    return value


def _shape(shape: Any) -> str:
    return "[" + ", ".join(str(d) for d in shape) + "]" if isinstance(shape, list) else str(shape)


def format_export(outcome: dict[str, Any]) -> str:
    """export 的中文摘要。"""
    if not outcome.get("ok"):
        lines = [f"转换失败：{outcome.get('error')}"]
        if outcome.get("detail"):
            lines.append("  错误详情：")
            lines.extend("    " + line for line in str(outcome["detail"]).splitlines())
        return "\n".join(lines)
    result = outcome.get("result") or {}
    path = outcome.get("onnx_path")
    try:
        size = f"{Path(str(path)).stat().st_size / (1 << 20):.1f} MB，"
    except OSError:
        size = ""
    duration = result.get("duration_ms")
    took = f"用时 {duration / 1000:.1f} 秒" if isinstance(duration, (int, float)) else "完成"
    lines = [f"转换成功：{path}（{size}{took}）"]
    for label, key in (("输入", "inputs"), ("输出", "outputs")):
        for item in result.get(key) or []:
            lines.append(
                f"  {label}：{item.get('name')} {_shape(item.get('shape'))} {item.get('type')}"
            )
    if result.get("end2end"):
        postprocess = "end2end"
    elif result.get("nms"):
        postprocess = "nms"
    else:
        postprocess = "raw"
    facts = [f"opset {result.get('opset')}", _POSTPROCESS_TEXT[postprocess]]
    facts.append("动态尺寸" if result.get("dynamic") else "固定尺寸")
    if result.get("verified"):
        facts.append(f"已用 ONNX Runtime {result.get('onnxruntime_version')} 验证")
    else:
        facts.append("未验证（没有安装 onnxruntime）")
    lines.append("  " + " · ".join(facts))
    versions = [
        f"{name} {result[key]}"
        for name, key in (
            ("Ultralytics", "ultralytics_version"),
            ("PyTorch", "torch_version"),
            ("onnx", "onnx_version"),
        )
        if result.get(key)
    ]
    if versions:
        lines.append("  版本：" + " · ".join(versions))
    return "\n".join(lines)


def cmd_export(args: argparse.Namespace) -> int:
    cfg = _load_config()
    if cfg is None:
        return 2
    setup_logging(_quiet_level(cfg))
    from .model_export import ExportProcess, ExportSpec

    spec = ExportSpec(
        weights=str(Path(args.weights).resolve()),
        output=str(Path(args.output).resolve()) if args.output else None,
        task=args.task,
        imgsz=args.imgsz,
        dynamic=args.dynamic,
        postprocess=args.postprocess,
        opset=args.opset,
    )
    # Ctrl+C / docker stop 时结束转换子进程（run 收到 KeyboardInterrupt 会终止它）。
    signal.signal(signal.SIGTERM, _raise_interrupt)
    try:
        outcome = ExportProcess(state_dir=cfg.state_dir, timeout=cfg.export_timeout).run(spec)
    except KeyboardInterrupt:
        return 130
    if args.json:
        outcome.pop("cancelled", None)
        print(json.dumps(outcome, ensure_ascii=False, indent=2))
    else:
        print(format_export(outcome), file=sys.stdout if outcome["ok"] else sys.stderr)
    return 0 if outcome["ok"] else 1


def parse_fraction(low: float, high: float) -> Any:
    """--conf / --iou：low–high 之间的数（与平台创建审图任务时的范围相同）。"""

    def parse(raw: str) -> float:
        try:
            value = float(raw)
        except ValueError:
            value = float("nan")
        if not low <= value <= high:
            raise argparse.ArgumentTypeError(f"{raw!r}：应为 {low:g}–{high:g} 之间的数")
        return value

    return parse


def parse_max_det(raw: str) -> int:
    try:
        value = int(raw)
    except ValueError:
        value = 0
    if not 1 <= value <= 1000:
        raise argparse.ArgumentTypeError(f"{raw!r}：应为 1–1000 的整数")
    return value


def format_inference(
    ready: dict[str, Any], outcomes: list[tuple[str, dict[str, Any], float]]
) -> str:
    """infer 的中文摘要：模型与设备，每张图片的尺寸、目标（类别 置信度 [x1, y1, x2, y2]）与用时。"""
    from .model_export import TASK_LABELS

    versions = ready.get("versions") or {}
    facts = [
        f"{name} {versions[key]}"
        for name, key in (("Ultralytics", "ultralytics"), ("PyTorch", "torch"))
        if versions.get(key)
    ]
    device = str(ready.get("device"))
    if ready.get("device_name"):
        device += f"（{ready['device_name']}）"
    task = TASK_LABELS.get(str(ready.get("task")), str(ready.get("task")))
    lines = [f"模型已加载：{task}模型，{device}" + (f"（{' · '.join(facts)}）" if facts else "")]
    for path, result, elapsed in outcomes:
        if not result.get("ok"):
            lines.append(f"{path}：{result.get('error')}")
            continue
        boxes = result.get("boxes") or []
        lines.append(
            f"{path}：{result['width']}×{result['height']}，{len(boxes)} 个目标，"
            f"用时 {elapsed:.2f} 秒"
        )
        for box in boxes:
            coords = ", ".join(str(round(box[k])) for k in ("xmin", "ymin", "xmax", "ymax"))
            lines.append(f"  {box['name']} {box['confidence']:.3f} [{coords}]")
    return "\n".join(lines)


def cmd_infer(args: argparse.Namespace) -> int:
    cfg = _load_config()
    if cfg is None:
        return 2
    setup_logging(_quiet_level(cfg))
    from .review_inference import (
        InferenceError,
        InferenceProcess,
        InferenceSpec,
        class_names,
        translate_reply,
    )

    spec = InferenceSpec(
        weights=str(Path(args.weights).resolve()),
        task=args.task,
        conf=args.conf,
        iou=args.iou,
        imgsz=args.imgsz,
        max_det=args.max_det,
        device=args.device,
    )
    process = InferenceProcess(state_dir=cfg.state_dir, image_timeout=cfg.inference_image_timeout)
    outcomes: list[tuple[str, dict[str, Any], float]] = []
    failure: InferenceError | None = None
    finished = False
    # Ctrl+C / docker stop 时结束识别子进程（finally 中结束整个进程组）。
    signal.signal(signal.SIGTERM, _raise_interrupt)
    try:
        ready = process.start(spec)
        names = class_names(ready)
        for index, image in enumerate(args.images, 1):
            started = time.monotonic()
            process.send(index, str(Path(image).resolve()))
            reply = None
            while reply is None:
                reply = process.next_reply(1.0)
            outcomes.append((image, translate_reply(reply, names), time.monotonic() - started))
        finished = True
    except InferenceError as exc:
        failure = exc
    except KeyboardInterrupt:
        return 130
    finally:
        process.close(kill=not finished)  # 识别完成时让子进程自行退出，否则立即结束
    if failure is not None:
        if args.json:
            report = {"ok": False, "error": failure.message, "detail": failure.detail}
            print(json.dumps(report, ensure_ascii=False, indent=2))
        else:
            lines = [f"识别失败：{failure.message}"]
            if failure.detail:
                lines.append("  错误详情：")
                lines.extend("    " + line for line in failure.detail.splitlines())
            print("\n".join(lines), file=sys.stderr)
        return 1
    ok = all(result.get("ok") for _, result, _ in outcomes)
    if args.json:
        images = []
        for path, result, elapsed in outcomes:
            entry = {"path": path, **result, "elapsed_ms": int(elapsed * 1000)}
            entry.pop("id")
            images.append(entry)
        keys = ("device", "device_name", "task", "versions")
        report = {"ok": ok, **{k: ready.get(k) for k in keys}, "images": images}
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        print(format_inference(ready, outcomes))
    return 0 if ok else 1


def cmd_healthcheck() -> int:
    from .agent import check_heartbeat

    configured = Path(os.environ.get("TM_STATE_DIR", "").strip() or config_mod.DEFAULT_STATE_DIR)
    ok, message = check_heartbeat(config_mod.healthcheck_dirs(configured, os.environ))
    print(message, file=sys.stdout if ok else sys.stderr)
    return 0 if ok else 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="python -m trainmaster_trainer",
        description="trainMaster 训练节点：自动识别显卡、CPU 与 CUDA 环境，向平台注册并上报心跳，"
        "执行平台的任务（模型的 ONNX 转换、审图识别）。",
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    sub = parser.add_subparsers(dest="command", metavar="命令")
    sub.add_parser("run", help="注册到平台并持续上报心跳、执行平台的任务（默认）")
    sub.add_parser("probe", help="打印硬件、CUDA 环境与实时指标（JSON），不连接平台")
    selftest = sub.add_parser("selftest", help="检测 CUDA / PyTorch / YOLO 环境并执行自检")
    selftest.add_argument("--json", action="store_true", help="以 JSON 输出检测结果（Environment）")
    export = sub.add_parser(
        "export",
        help="在本机把 pt 转换为 ONNX（与平台的转换任务相同），不连接平台",
        description="在本机把 Ultralytics 的 pt 模型转换为 ONNX（在 CPU 上，离线），打印转换结果。"
        "与平台的转换任务使用同一个子进程，便于排查转换失败的原因。",
    )
    export.add_argument("--weights", required=True, metavar="PT", help="要转换的 pt 文件")
    export.add_argument(
        "--imgsz",
        type=parse_imgsz,
        metavar="尺寸",
        help="输入尺寸：640 或 高x宽（如 480x640）；默认为模型训练时的尺寸",
    )
    export.add_argument(
        "--postprocess",
        choices=("raw", "nms", "end2end"),
        help="后处理方式：raw 原始输出、nms 嵌入 NMS、end2end 端到端（YOLOv10 / YOLO26）；"
        "默认端到端模型为 end2end，其余为 raw",
    )
    export.add_argument("--dynamic", action="store_true", help="动态输入尺寸（batch、高、宽）")
    export.add_argument(
        "--opset", type=parse_opset, metavar="N", help="ONNX opset（11–21），默认自动"
    )
    export.add_argument("--task", help="模型任务（detect、segment 等），默认从模型文件读取")
    export.add_argument(
        "--output", metavar="ONNX", help="输出文件，默认与 pt 文件同目录、同名的 .onnx"
    )
    export.add_argument("--json", action="store_true", help="以 JSON 输出转换结果")
    infer = sub.add_parser(
        "infer",
        help="在本机用 pt 识别图片（与平台的审图识别相同），不连接平台",
        description="在本机用 Ultralytics 的 pt 模型识别图片（有 CUDA 时在 cuda:0 上，离线），打印检测框。"
        "与平台的审图识别任务使用同一个子进程（安全加载模型、按存储的像素方向读取图片），"
        "便于排查识别失败或结果不对的原因。",
    )
    infer.add_argument("--weights", required=True, metavar="PT", help="模型文件（pt）")
    infer.add_argument("images", nargs="+", metavar="图片", help="要识别的图片文件")
    infer.add_argument(
        "--conf",
        type=parse_fraction(0.01, 1),
        default=0.25,
        metavar="置信度",
        help="置信度阈值（0.01–1），默认 0.25",
    )
    infer.add_argument(
        "--iou",
        type=parse_fraction(0.1, 0.95),
        default=0.7,
        metavar="IoU",
        help="NMS 的 IoU 阈值（0.1–0.95），默认 0.7",
    )
    infer.add_argument(
        "--imgsz",
        type=parse_imgsz,
        metavar="尺寸",
        help="输入尺寸：640 或 高x宽（如 480x640）；默认为模型训练时的尺寸",
    )
    infer.add_argument(
        "--max-det",
        type=parse_max_det,
        default=300,
        metavar="N",
        help="每张图片最多的检测框数（1–1000），默认 300",
    )
    infer.add_argument("--device", help="设备（cpu、cuda:0 …），默认有 CUDA 时为 cuda:0，否则 cpu")
    infer.add_argument("--task", help="模型任务（detect、segment 等），默认从模型文件读取")
    infer.add_argument("--json", action="store_true", help="以 JSON 输出识别结果")
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
    if command == "export":
        return cmd_export(args)
    if command == "infer":
        return cmd_infer(args)
    return cmd_healthcheck()


if __name__ == "__main__":
    sys.exit(main())
