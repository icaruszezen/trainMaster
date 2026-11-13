"""YOLO 模型训练任务（training_job）：领取平台下发的训练任务并在子进程中调用 Ultralytics 执行训练。

与 model_export.py、review_inference.py 一样分两部分：
- 子进程（python -m trainmaster_trainer.training --spec <json>）：导入 torch 与 ultralytics，在需要加载
  基础模型时启用 Ultralytics 安全加载（ULTRALYTICS_SAFE_LOAD=1），注册训练回调向原 stdout 输出结构化
  JSON 事件行（start / batch / epoch / done），而 Ultralytics 的控制台日志与 C 扩展输出全部重定向到
  stderr 供父进程实时采集为 train.log。常驻主进程不导入 torch / ultralytics。
- 父进程：TrainingProcess 管理训练子进程与进程组（支持随时取消，连同 DataLoader / DDP 子进程一并终止）；
  TrainingJobTask 准备数据集（共享 /data 目录就地读取 data.yaml，或流式下载并安全解压数据集 zip）与可选
  基础模型权重，运行子进程并实时上报阶段、轮次/批次进度、每轮损失与 mAP 指标及训练日志，训练完成后逐个上传
  weights/best.pt、weights/last.pt、results.csv、args.yaml 与评估图表，最后调用 complete 接口。若平台
  返回 404 / 409（用户停止任务或删除任务），立即终止子进程组并退出。
"""

from __future__ import annotations

import argparse
import contextlib
import csv
import json
import logging
import math
import os
import shutil
import subprocess
import sys
import threading
import time
import urllib.parse
import zipfile
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import IO, Any

from . import __version__, procs
from .client import ApiError, Client, TransportError, json_safe
from .environment import _kill, summarize_exception
from .model_export import (
    _SHA256,
    FAIL_RETRY_DELAYS,
    MAX_DETAIL,
    MAX_OUTPUT_BYTES,
    PROGRESS_INTERVAL,
    SAFE_LOAD_ENV,
    STOPPING_FAIL_TIMEOUT,
    TRANSFER_TIMEOUT,
    ExportError,
    ExportSpec,
    TaskAbandoned,
    TaskFailed,
    _clean_message,
    _is_out_of_memory,
    _join_detail,
    fail_url_of,
    file_sha256,
    format_bytes,
    report_failure,
    require_safe_load,
    translate_load_error,
    verify_download,
)
from .review_inference import _Tail, inference_env

log = logging.getLogger(__name__)

CHILD_MODULE = "trainmaster_trainer.training"
KIND = "training_job"
WORK_DIR_PREFIX = "training-job-"

INIT_MODES = ("scratch", "finetune", "resume")
STAGES = ("preparing", "downloading", "training", "uploading")

# 训练日志与实时进度刷新周期（秒）；无新日志时仍按 progress_interval（15s）续租。
LOG_FLUSH_INTERVAL = 2.0
BATCH_EVENT_INTERVAL = 1.0
MAX_LOG_CHUNK_BYTES = 64 * 1024
MAX_DATASET_ZIP_BYTES = 64 << 30  # 远程节点下载数据集 zip 上限（64 GiB）
MIN_FREE_DISK_BYTES = 512 << 20  # 工作目录至少保留 512 MiB 可用空间

# 允许上传到平台 runs/job-<id>/ 的产物扩展名（与后端 cleanArtifactRel 一致）。
ARTIFACT_PLOT_EXTS = frozenset({".png", ".jpg", ".jpeg"})
ARTIFACT_META_EXTS = frozenset({".csv", ".yaml", ".yml"})


class TrainingError(Exception):
    """子进程训练失败，message 为面向用户的中文错误摘要。"""

    def __init__(self, message: str, detail: str | None = None) -> None:
        super().__init__(message)
        self.message = message
        self.detail = detail


def _finite(value: Any) -> float | None:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    f = float(value)
    return round(f, 6) if math.isfinite(f) else None


def _positive_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and value > 0


@dataclass(frozen=True)
class TrainingSpec:
    """训练子进程参数（--spec JSON）。"""

    init_mode: str  # scratch | finetune | resume
    data_yaml: str
    project_dir: str
    run_name: str = "train"
    arch_yaml: str | None = None
    base_weights: str | None = None
    yolo_version: str | None = None
    task: str = "detect"
    params: Mapping[str, Any] = None  # type: ignore[assignment]

    @classmethod
    def from_json(cls, data: Any) -> TrainingSpec:
        if not isinstance(data, dict):
            raise ValueError("应为 JSON 对象")
        init_mode = data.get("init_mode")
        if init_mode not in INIT_MODES:
            raise ValueError(f"init_mode 应为 {' / '.join(INIT_MODES)}：{init_mode!r}")
        data_yaml = data.get("data_yaml")
        if not isinstance(data_yaml, str) or not data_yaml:
            raise ValueError("缺少 data_yaml（数据集配置文件路径）")
        project_dir = data.get("project_dir")
        if not isinstance(project_dir, str) or not project_dir:
            raise ValueError("缺少 project_dir（训练输出目录）")
        run_name = data.get("run_name", "train")
        if not isinstance(run_name, str) or not run_name or "/" in run_name or "\\" in run_name:
            raise ValueError(f"run_name 不合法：{run_name!r}")

        arch_yaml = data.get("arch_yaml")
        if arch_yaml is not None and (not isinstance(arch_yaml, str) or not arch_yaml):
            raise ValueError("arch_yaml 应为非空字符串")
        base_weights = data.get("base_weights")
        if base_weights is not None and (not isinstance(base_weights, str) or not base_weights):
            raise ValueError("base_weights 应为非空字符串")

        if init_mode == "scratch" and not arch_yaml:
            raise ValueError("scratch 模式缺少 arch_yaml")
        if init_mode in ("finetune", "resume") and not base_weights:
            raise ValueError(f"{init_mode} 模式缺少 base_weights")

        yolo_version = data.get("yolo_version")
        if yolo_version is not None and not isinstance(yolo_version, str):
            raise ValueError("yolo_version 应为字符串")
        task = data.get("task") or "detect"
        if not isinstance(task, str):
            raise ValueError("task 应为字符串")
        params = data.get("params") or {}
        if not isinstance(params, dict):
            raise ValueError("params 应为 JSON 对象")

        return cls(
            init_mode=init_mode,
            data_yaml=data_yaml,
            project_dir=project_dir,
            run_name=run_name,
            arch_yaml=arch_yaml,
            base_weights=base_weights,
            yolo_version=yolo_version,
            task=task,
            params=dict(params),
        )

    def to_json(self) -> dict[str, Any]:
        return {
            "init_mode": self.init_mode,
            "data_yaml": self.data_yaml,
            "project_dir": self.project_dir,
            "run_name": self.run_name,
            "arch_yaml": self.arch_yaml,
            "base_weights": self.base_weights,
            "yolo_version": self.yolo_version,
            "task": self.task,
            "params": dict(self.params or {}),
        }


# ---------------------------------------------------------------------------
# 子进程：执行 Ultralytics 训练并输出实时事件


def translate_train_error(exc: BaseException, spec: TrainingSpec) -> str:
    text = str(exc)
    lower = text.lower()
    if (
        _is_out_of_memory(exc)
        or "cuda out of memory" in lower
        or "cublas_status_alloc_failed" in lower
    ):
        return (
            "显存或系统内存不足，训练中断。建议调小批次大小（batch，或设为 -1 自动计算）、"
            "减小输入图像尺寸（imgsz），或将图像缓存（cache）设为 false"
        )
    if "nothing to resume" in lower or ("resume" in lower and "finished" in lower):
        return (
            "该检查点已完成全部轮数的训练，无法继续断点续训；"
            "如需在此权重基础上继续训练更多轮数，请改选“预训练微调”模式"
        )
    if "no labels found" in lower or "no images found" in lower:
        return f"数据集缺少可用的标注或图片，无法开始训练（{_clean_message(exc)}）"
    if "nc" in lower and "names" in lower:
        return f"数据集类别定义不合法（{_clean_message(exc)}）"
    if "cuda" in lower and (
        "device" in lower or "invalid device" in lower or "no kernel image" in lower
    ):
        return f"训练设备（device={spec.params.get('device')!r}）不可用或与当前显卡不兼容：{_clean_message(exc)}"
    return f"训练执行失败：{_clean_message(exc)}"


def _build_train_kwargs(
    spec: TrainingSpec, default_cfg: Mapping[str, Any] | None
) -> dict[str, Any]:
    """把平台校验过的全量 YOLO 参数整理成传给 model.train(**kwargs) 的字典，并兼容不同小版本的 Ultralytics。"""
    kwargs: dict[str, Any] = {}
    allowed = set(default_cfg.keys()) if isinstance(default_cfg, Mapping) and default_cfg else None

    for k, v in (spec.params or {}).items():
        if k in (
            "mode",
            "task",
            "model",
            "data",
            "project",
            "name",
            "exist_ok",
            "cfg",
            "source",
            "distill_model",
        ):
            continue
        if allowed is not None and k not in allowed:
            # 兼容较旧版本 Ultralytics：quantize="fp16" 映射为旧版 half=True；其余未修改的新增参数静默跳过。
            if k == "quantize" and v in ("fp16", 16, True) and "half" in allowed:
                kwargs["half"] = True
            continue
        kwargs[k] = v

    # 从头构建网络（.yaml）时没有预训练权重文件，强制 pretrained=False 避免任何联网下载尝试。
    if spec.init_mode == "scratch":
        kwargs["pretrained"] = False
        kwargs["resume"] = False
    elif spec.init_mode == "finetune":
        kwargs["resume"] = False
    elif spec.init_mode == "resume":
        kwargs["resume"] = True

    # Windows 或受限容器环境中适度限制默认 workers，用户显式改小后尊重用户值。
    kwargs["data"] = spec.data_yaml
    kwargs["project"] = spec.project_dir
    kwargs["name"] = spec.run_name
    kwargs["exist_ok"] = True
    return kwargs


def _first_metric(mapping: Mapping[str, Any], *keys: str) -> float | None:
    for k in keys:
        if k in mapping:
            val = _finite(mapping[k])
            if val is not None:
                return val
    return None


def _extract_epoch_metrics(
    trainer: Any,
    *,
    epoch_num: int,
    total_epochs: int,
    duration_sec: float | None,
    gpu_mem_gb: float | None,
) -> dict[str, Any]:
    """从 Ultralytics trainer 实例提取一轮训练+验证的核心指标。"""
    losses: dict[str, Any] = {}
    tloss = getattr(trainer, "tloss", None)
    if tloss is not None and hasattr(trainer, "label_loss_items"):
        with contextlib.suppress(Exception):
            raw = trainer.label_loss_items(tloss, prefix="train")
            if isinstance(raw, Mapping):
                losses = dict(raw)

    metrics_raw = getattr(trainer, "metrics", None)
    metrics: Mapping[str, Any] = metrics_raw if isinstance(metrics_raw, Mapping) else {}

    lr_map = getattr(trainer, "lr", None)
    lr_val: float | None = None
    if isinstance(lr_map, Mapping):
        lr_val = _first_metric(lr_map, "lr/pg0", "lr/pg1", "lr/pg2")
        if lr_val is None and lr_map:
            lr_val = _finite(next(iter(lr_map.values())))

    out: dict[str, Any] = {
        "epoch": epoch_num,
        "total_epochs": total_epochs,
        "train_box_loss": _first_metric(losses, "train/box_loss"),
        "train_cls_loss": _first_metric(losses, "train/cls_loss"),
        "train_dfl_loss": _first_metric(losses, "train/dfl_loss"),
        "val_box_loss": _first_metric(metrics, "val/box_loss"),
        "val_cls_loss": _first_metric(metrics, "val/cls_loss"),
        "val_dfl_loss": _first_metric(metrics, "val/dfl_loss"),
        "precision": _first_metric(
            metrics,
            "metrics/precision(B)",
            "metrics/precision(M)",
            "metrics/precision(P)",
            "metrics/precision",
        ),
        "recall": _first_metric(
            metrics, "metrics/recall(B)", "metrics/recall(M)", "metrics/recall(P)", "metrics/recall"
        ),
        "map50": _first_metric(
            metrics,
            "metrics/mAP50(B)",
            "metrics/mAP50(M)",
            "metrics/mAP50(P)",
            "metrics/mAP50",
            "metrics/accuracy_top1",
        ),
        "map50_95": _first_metric(
            metrics,
            "metrics/mAP50-95(B)",
            "metrics/mAP50-95(M)",
            "metrics/mAP50-95(P)",
            "metrics/mAP50-95",
            "metrics/accuracy_top5",
        ),
        "fitness": _finite(getattr(trainer, "fitness", None)),
        "lr": lr_val,
        "gpu_mem_gb": _finite(gpu_mem_gb),
        "duration_sec": _finite(duration_sec),
    }
    return {k: v for k, v in out.items() if v is not None or k in ("epoch", "total_epochs")}


def parse_results_csv(csv_path: Path, total_epochs: int = 0) -> list[dict[str, Any]]:
    """兜底解析 Ultralytics 生成的 results.csv，确保每一轮指标完整无缺。"""
    if not csv_path.is_file():
        return []
    out: list[dict[str, Any]] = []
    try:
        with open(csv_path, encoding="utf-8", errors="replace", newline="") as f:
            reader = csv.DictReader(f)
            for row in reader:
                clean = {
                    str(k).strip(): (v.strip() if isinstance(v, str) else v)
                    for k, v in row.items()
                    if k
                }
                try:
                    ep = int(float(clean.get("epoch", "0")))
                except ValueError:
                    continue
                if ep < 1:
                    continue
                nums: dict[str, float] = {}
                for rk, rv in clean.items():
                    if rk == "epoch" or rv in (None, ""):
                        continue
                    with contextlib.suppress(ValueError):
                        fv = float(rv)
                        if math.isfinite(fv):
                            nums[rk] = round(fv, 6)
                item: dict[str, Any] = {
                    "epoch": ep,
                    "total_epochs": max(total_epochs, ep),
                    "train_box_loss": _first_metric(nums, "train/box_loss"),
                    "train_cls_loss": _first_metric(nums, "train/cls_loss"),
                    "train_dfl_loss": _first_metric(nums, "train/dfl_loss"),
                    "val_box_loss": _first_metric(nums, "val/box_loss"),
                    "val_cls_loss": _first_metric(nums, "val/cls_loss"),
                    "val_dfl_loss": _first_metric(nums, "val/dfl_loss"),
                    "precision": _first_metric(
                        nums, "metrics/precision(B)", "metrics/precision(M)", "metrics/precision"
                    ),
                    "recall": _first_metric(
                        nums, "metrics/recall(B)", "metrics/recall(M)", "metrics/recall"
                    ),
                    "map50": _first_metric(
                        nums,
                        "metrics/mAP50(B)",
                        "metrics/mAP50(M)",
                        "metrics/mAP50",
                        "metrics/accuracy_top1",
                    ),
                    "map50_95": _first_metric(
                        nums,
                        "metrics/mAP50-95(B)",
                        "metrics/mAP50-95(M)",
                        "metrics/mAP50-95",
                        "metrics/accuracy_top5",
                    ),
                    "lr": _first_metric(nums, "lr/pg0", "lr/pg1"),
                    "duration_sec": _first_metric(nums, "time"),
                }
                out.append(
                    {
                        k: v
                        for k, v in item.items()
                        if v is not None or k in ("epoch", "total_epochs")
                    }
                )
    except OSError:
        return []
    return out


def run_training(spec: TrainingSpec, emit: Callable[[Mapping[str, Any]], None]) -> dict[str, Any]:
    """在子进程中执行 Ultralytics 训练，通过 emit 推送实时事件，并返回最终结果字典（不抛出异常）。"""
    started = time.monotonic()
    try:
        return _run_training(spec, emit, started)
    except TrainingError as exc:
        return {"event": "done", "ok": False, "error": exc.message, "detail": exc.detail}
    except ExportError as exc:
        return {"event": "done", "ok": False, "error": exc.message, "detail": exc.detail}
    except Exception as exc:
        return {
            "event": "done",
            "ok": False,
            "error": translate_train_error(exc, spec),
            "detail": summarize_exception(exc, MAX_DETAIL),
        }


def _run_training(
    spec: TrainingSpec, emit: Callable[[Mapping[str, Any]], None], started: float
) -> dict[str, Any]:
    if not Path(spec.data_yaml).is_file():
        raise TrainingError(f"找不到训练数据集配置文件：{spec.data_yaml}")
    if spec.init_mode in ("finetune", "resume"):
        assert spec.base_weights is not None
        if not Path(spec.base_weights).is_file():
            raise TrainingError(f"找不到基础模型权重文件：{spec.base_weights}")

    try:
        import torch
        import ultralytics
    except Exception as exc:
        raise TrainingError(
            f"训练节点无法导入 PyTorch / Ultralytics（{_clean_message(exc)}），"
            "请运行 python -m trainmaster_trainer selftest 检查节点环境",
            summarize_exception(exc, MAX_DETAIL),
        ) from exc

    node_version = str(getattr(ultralytics, "__version__", "")) or "unknown"
    torch_version = str(getattr(torch, "__version__", "")) or "unknown"

    default_cfg: Mapping[str, Any] | None = None
    with contextlib.suppress(Exception):
        from ultralytics.cfg import DEFAULT_CFG_DICT

        if isinstance(DEFAULT_CFG_DICT, Mapping):
            default_cfg = DEFAULT_CFG_DICT

    if spec.init_mode in ("finetune", "resume"):
        assert spec.base_weights is not None
        require_safe_load(node_version)
        load_spec = ExportSpec(
            weights=spec.base_weights, task=spec.task, yolo_version=spec.yolo_version
        )
        try:
            model = ultralytics.YOLO(spec.base_weights, task=spec.task)
        except Exception as exc:
            raise TrainingError(
                translate_load_error(exc, load_spec, node_version),
                summarize_exception(exc, MAX_DETAIL),
            ) from exc
    else:
        assert spec.arch_yaml is not None
        try:
            model = ultralytics.YOLO(spec.arch_yaml, task=spec.task)
        except Exception as exc:
            raise TrainingError(
                f"无法构建模型网络结构 {spec.arch_yaml}：{_clean_message(exc)}",
                summarize_exception(exc, MAX_DETAIL),
            ) from exc

    kwargs = _build_train_kwargs(spec, default_cfg)
    state: dict[str, Any] = {
        "train_started": time.monotonic(),
        "epoch_started": time.monotonic(),
        "last_batch_emit": 0.0,
        "batch_idx": 0,
        "emitted_epochs": set(),
    }

    def gpu_mem() -> float | None:
        with contextlib.suppress(Exception):
            if torch.cuda.is_available():
                mem = float(torch.cuda.memory_reserved()) / 1e9
                return round(mem, 3) if mem > 0 else None
        return None

    def on_pretrain_routine_start(trainer: Any) -> None:
        # 断点续训时覆盖旧检查点中可能已失效的数据集路径与输出目录
        if hasattr(trainer, "args"):
            trainer.args.data = spec.data_yaml
            trainer.args.project = spec.project_dir
            trainer.args.name = spec.run_name
            trainer.args.exist_ok = True
        save_dir = Path(spec.project_dir) / spec.run_name
        if getattr(trainer, "save_dir", None) != save_dir:
            with contextlib.suppress(Exception):
                save_dir.mkdir(parents=True, exist_ok=True)
                trainer.save_dir = save_dir
                trainer.wdir = save_dir / "weights"
                trainer.wdir.mkdir(parents=True, exist_ok=True)
                trainer.last = trainer.wdir / "last.pt"
                trainer.best = trainer.wdir / "best.pt"
                trainer.csv = save_dir / "results.csv"
        state["train_started"] = time.monotonic()
        total_ep = int(getattr(trainer, "epochs", kwargs.get("epochs", 100)) or 100)
        emit(
            {
                "event": "start",
                "total_epochs": total_ep,
                "device": str(getattr(trainer, "device", "")),
            }
        )

    def on_train_epoch_start(trainer: Any) -> None:
        state["epoch_started"] = time.monotonic()
        state["batch_idx"] = 0
        cur_ep = int(getattr(trainer, "epoch", 0)) + 1
        total_ep = int(getattr(trainer, "epochs", kwargs.get("epochs", 100)) or 100)
        loader = getattr(trainer, "train_loader", None)
        batch_total = len(loader) if loader is not None and hasattr(loader, "__len__") else 0
        emit(
            {
                "event": "batch",
                "current_epoch": cur_ep,
                "total_epochs": total_ep,
                "batch_done": 0,
                "batch_total": batch_total,
                "gpu_mem_gb": gpu_mem(),
            }
        )

    def on_train_batch_end(trainer: Any) -> None:
        state["batch_idx"] = int(state["batch_idx"]) + 1
        now = time.monotonic()
        loader = getattr(trainer, "train_loader", None)
        batch_total = len(loader) if loader is not None and hasattr(loader, "__len__") else 0
        batch_done = int(state["batch_idx"])
        if batch_total > 0 and batch_done > batch_total:
            batch_done = batch_total
        if (
            now - float(state["last_batch_emit"]) < BATCH_EVENT_INTERVAL
            and batch_done != batch_total
        ):
            return
        state["last_batch_emit"] = now

        cur_ep = int(getattr(trainer, "epoch", 0)) + 1
        total_ep = int(getattr(trainer, "epochs", kwargs.get("epochs", 100)) or 100)
        start_ep = int(getattr(trainer, "start_epoch", 0))
        eta_seconds: int | None = None
        if batch_total > 0 and total_ep > start_ep:
            done_steps = max(0, (cur_ep - 1 - start_ep) * batch_total + batch_done)
            total_steps = max(1, (total_ep - start_ep) * batch_total)
            elapsed = max(0.001, now - float(state["train_started"]))
            if done_steps > 0 and total_steps >= done_steps:
                eta_seconds = max(0, int(round(elapsed * (total_steps - done_steps) / done_steps)))

        emit(
            {
                "event": "batch",
                "current_epoch": cur_ep,
                "total_epochs": total_ep,
                "batch_done": batch_done,
                "batch_total": batch_total,
                "eta_seconds": eta_seconds,
                "gpu_mem_gb": gpu_mem(),
            }
        )

    def on_fit_epoch_end(trainer: Any) -> None:
        cur_ep = int(getattr(trainer, "epoch", 0)) + 1
        total_ep = int(getattr(trainer, "epochs", kwargs.get("epochs", 100)) or 100)
        dur = max(0.0, time.monotonic() - float(state["epoch_started"]))
        ep_metrics = _extract_epoch_metrics(
            trainer,
            epoch_num=cur_ep,
            total_epochs=total_ep,
            duration_sec=dur,
            gpu_mem_gb=gpu_mem(),
        )
        state["emitted_epochs"].add(cur_ep)
        emit({"event": "epoch", "epoch": ep_metrics})

    if hasattr(model, "add_callback"):
        model.add_callback("on_pretrain_routine_start", on_pretrain_routine_start)
        model.add_callback("on_train_epoch_start", on_train_epoch_start)
        model.add_callback("on_train_batch_end", on_train_batch_end)
        model.add_callback("on_fit_epoch_end", on_fit_epoch_end)

    print(
        f"[trainMaster] 开始训练: init_mode={spec.init_mode} model={spec.arch_yaml or spec.base_weights} "
        f"epochs={kwargs.get('epochs')} batch={kwargs.get('batch')} imgsz={kwargs.get('imgsz')} "
        f"optimizer={kwargs.get('optimizer')}",
        file=sys.stderr,
        flush=True,
    )

    try:
        model.train(**kwargs)
    except Exception as exc:
        raise TrainingError(
            translate_train_error(exc, spec),
            summarize_exception(exc, MAX_DETAIL),
        ) from exc

    trainer_obj = getattr(model, "trainer", None)
    save_dir = Path(
        getattr(trainer_obj, "save_dir", None) or (Path(spec.project_dir) / spec.run_name)
    )
    total_ep = int(getattr(trainer_obj, "epochs", kwargs.get("epochs", 100)) or 100)

    # 补充 results.csv 中尚未通过回调上报的轮次指标（例如自定义或假模块直接落盘 results.csv）
    for csv_ep in parse_results_csv(save_dir / "results.csv", total_ep):
        if csv_ep["epoch"] not in state["emitted_epochs"]:
            emit({"event": "epoch", "epoch": csv_ep})

    best_pt = save_dir / "weights" / "best.pt"
    last_pt = save_dir / "weights" / "last.pt"
    if not best_pt.is_file() and not last_pt.is_file():
        raise TrainingError(f"训练结束但未生成模型权重文件（{save_dir / 'weights'}）")
    # 当某些训练配置（如 val=False）只生成了 last.pt 时，复制一份作为 best.pt 以便自动注册
    if not best_pt.is_file() and last_pt.is_file():
        shutil.copy2(last_pt, best_pt)

    device_str = str(getattr(trainer_obj, "device", "") or kwargs.get("device") or "auto")
    result = {
        "ultralytics_version": node_version,
        "torch_version": torch_version,
        "cuda_available": bool(torch.cuda.is_available()),
        "device": device_str,
        "duration_ms": int((time.monotonic() - started) * 1000),
    }
    return {
        "event": "done",
        "ok": True,
        "save_dir": str(save_dir),
        "result": result,
    }


def child_main(argv: Sequence[str] | None = None) -> int:
    """训练子进程入口：原 stdout 仅输出 JSON lines 事件，所有控制台日志重定向至 stderr。"""
    parser = argparse.ArgumentParser(prog=f"python -m {CHILD_MODULE}")
    parser.add_argument("--spec", required=True, help="训练参数（JSON）")
    args = parser.parse_args(argv)

    os.environ.setdefault("YOLO_OFFLINE", "1")
    os.environ.setdefault("YOLO_AUTOINSTALL", "False")
    os.environ[SAFE_LOAD_ENV] = "1"
    logging.basicConfig(stream=sys.stderr, level=logging.INFO)

    sys.stdout.flush()
    json_out = os.fdopen(os.dup(1), "w", encoding="utf-8")
    os.dup2(2, 1)
    lock = threading.Lock()

    def emit(payload: Mapping[str, Any]) -> None:
        with lock:
            json_out.write(json.dumps(json_safe(dict(payload)), ensure_ascii=False) + "\n")
            json_out.flush()

    with contextlib.redirect_stdout(sys.stderr):
        try:
            spec = TrainingSpec.from_json(json.loads(args.spec))
        except ValueError as exc:
            outcome: dict[str, Any] = {
                "event": "done",
                "ok": False,
                "error": f"训练参数不合法：{exc}",
                "detail": None,
            }
        else:
            outcome = run_training(spec, emit)

    emit(outcome)
    return 0 if outcome.get("ok") else 1


# ---------------------------------------------------------------------------
# 父进程：管理训练子进程并实时采集事件与日志


def training_env(state_dir: Path, base: Mapping[str, str] | None = None) -> dict[str, str]:
    """训练子进程的受限环境变量：可见显卡，启用安全加载与行缓冲输出，隐藏节点令牌。"""
    env = inference_env(state_dir, base)
    env["PYTHONUNBUFFERED"] = "1"
    return env


def normalize_log_line(raw: bytes) -> str:
    """规范化子进程输出的一行日志：处理 tqdm 回车符 \\r，只保留该行最后一次刷新的文本。"""
    text = raw.decode("utf-8", "replace")
    has_nl = text.endswith("\n")
    stripped = text.rstrip("\r\n")
    if "\r" in stripped:
        parts = [p for p in stripped.split("\r") if p.strip()]
        stripped = parts[-1] if parts else ""
    if not stripped and not has_nl:
        return ""
    return stripped + "\n"


class TrainingProcess:
    """运行训练子进程并在后台线程中实时收集结构化事件与训练日志。支持从任意线程调用 cancel()。"""

    def __init__(
        self,
        *,
        state_dir: Path,
        command: Sequence[str] | None = None,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self.state_dir = state_dir
        self.command = list(command) if command else [sys.executable, "-m", CHILD_MODULE]
        self.clock = clock

        self._lock = threading.Lock()
        self._wakeup = threading.Event()
        self._proc: subprocess.Popen[bytes] | None = None
        self._cancelled = False

        self._current_epoch: int | None = None
        self._total_epochs: int | None = None
        self._batch_done: int | None = None
        self._batch_total: int | None = None
        self._eta_seconds: int | None = None
        self._pending_epochs: list[dict[str, Any]] = []
        self._log_chunks: list[str] = []
        self._log_bytes = 0
        self._done_event: dict[str, Any] | None = None
        self._stderr_tail = _Tail()

    def cancel(self) -> None:
        with self._lock:
            self._cancelled = True
            proc = self._proc
        self._wakeup.set()
        if proc is not None:
            _kill(proc)

    def drain_progress(self) -> dict[str, Any]:
        """原子取出自上次上报以来累积的轮次指标、最新进度与日志片段。"""
        with self._lock:
            epochs = list(self._pending_epochs)
            self._pending_epochs.clear()

            log_chunk = "".join(self._log_chunks)
            self._log_chunks.clear()
            self._log_bytes = 0
            if len(log_chunk.encode("utf-8")) > MAX_LOG_CHUNK_BYTES:
                encoded = log_chunk.encode("utf-8")[-MAX_LOG_CHUNK_BYTES:]
                log_chunk = encoded.decode("utf-8", "ignore")

            return {
                "current_epoch": self._current_epoch,
                "total_epochs": self._total_epochs,
                "batch_done": self._batch_done,
                "batch_total": self._batch_total,
                "eta_seconds": self._eta_seconds,
                "epochs": epochs,
                "log_chunk": log_chunk,
            }

    def run(
        self,
        spec: TrainingSpec,
        *,
        cwd: Path | None = None,
        on_tick: Callable[[bool], None] | None = None,
        tick_interval: float = LOG_FLUSH_INTERVAL,
    ) -> dict[str, Any]:
        """启动训练子进程，周期性回调 on_tick(force) 上报进度，直到子进程结束。"""
        with contextlib.suppress(OSError):
            (self.state_dir / "ultralytics").mkdir(parents=True, exist_ok=True)
        started = self.clock()
        try:
            with self._lock:
                if self._cancelled:
                    return {"ok": False, "error": "训练已取消", "detail": None, "cancelled": True}
                self._proc = subprocess.Popen(
                    [*self.command, "--spec", json.dumps(spec.to_json(), ensure_ascii=False)],
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    env=training_env(self.state_dir),
                    cwd=cwd,
                    **procs.popen_kwargs(),
                )
                procs.attach(self._proc)
            proc = self._proc
        except OSError as exc:
            return {
                "ok": False,
                "error": f"无法启动训练子进程：{exc}",
                "detail": None,
                "cancelled": False,
            }

        assert proc.stdout is not None and proc.stderr is not None
        out_thread = threading.Thread(
            target=self._read_events, args=(proc.stdout,), name="train-events", daemon=True
        )
        err_thread = threading.Thread(
            target=self._read_logs, args=(proc.stderr,), name="train-logs", daemon=True
        )
        out_thread.start()
        err_thread.start()

        try:
            while proc.poll() is None:
                triggered = self._wakeup.wait(timeout=tick_interval)
                self._wakeup.clear()
                if on_tick is not None:
                    on_tick(triggered)
            out_thread.join(timeout=5.0)
            err_thread.join(timeout=5.0)
            if on_tick is not None:
                on_tick(True)
        except BaseException:
            _kill(proc)
            raise
        finally:
            with self._lock:
                self._proc = None

        elapsed = self.clock() - started
        stderr_tail = self._stderr_tail.text(MAX_DETAIL)
        if self._cancelled:
            return {"ok": False, "error": "训练已取消", "detail": stderr_tail, "cancelled": True}

        sig_name = procs.signal_name(proc.returncode)
        if sig_name is not None:
            return {
                "ok": False,
                "error": f"训练进程被信号 {sig_name} 终止（可能是显存或内存不足触发系统回收），用时 {elapsed:.0f} 秒",
                "detail": stderr_tail,
                "cancelled": False,
            }
        crash = procs.windows_crash(proc.returncode)
        if crash is not None:
            return {
                "ok": False,
                "error": f"训练进程异常崩溃（{crash}），用时 {elapsed:.0f} 秒",
                "detail": stderr_tail,
                "cancelled": False,
            }

        done = self._done_event
        if not isinstance(done, dict) or not isinstance(done.get("ok"), bool):
            if proc.returncode != 0:
                return {
                    "ok": False,
                    "error": f"训练进程异常退出（退出码 {proc.returncode}）",
                    "detail": stderr_tail,
                    "cancelled": False,
                }
            return {
                "ok": False,
                "error": "训练进程未返回结果事件",
                "detail": stderr_tail,
                "cancelled": False,
            }
        if not done["ok"]:
            err = done.get("error") if isinstance(done.get("error"), str) else "训练失败"
            det = done.get("detail") if isinstance(done.get("detail"), str) else None
            return {
                "ok": False,
                "error": err,
                "detail": _join_detail(det, stderr_tail),
                "cancelled": False,
            }
        save_dir = done.get("save_dir")
        result = done.get("result")
        if not isinstance(save_dir, str) or not isinstance(result, dict):
            return {
                "ok": False,
                "error": "训练进程返回的结果不完整（缺少 save_dir 或 result）",
                "detail": stderr_tail,
                "cancelled": False,
            }
        return {"ok": True, "save_dir": save_dir, "result": result}

    def _read_events(self, pipe: IO[bytes]) -> None:
        try:
            for raw in pipe:
                line = raw.decode("utf-8", "replace").strip()
                if not line:
                    continue
                try:
                    ev = json.loads(line)
                except ValueError:
                    continue
                if not isinstance(ev, dict):
                    continue
                kind = ev.get("event")
                urgent = False
                with self._lock:
                    if kind == "start":
                        if _positive_int(ev.get("total_epochs")):
                            self._total_epochs = ev["total_epochs"]
                        urgent = True
                    elif kind == "batch":
                        if isinstance(ev.get("current_epoch"), int) and ev["current_epoch"] >= 0:
                            self._current_epoch = ev["current_epoch"]
                        if _positive_int(ev.get("total_epochs")):
                            self._total_epochs = ev["total_epochs"]
                        if isinstance(ev.get("batch_done"), int) and ev["batch_done"] >= 0:
                            self._batch_done = ev["batch_done"]
                        if isinstance(ev.get("batch_total"), int) and ev["batch_total"] >= 0:
                            self._batch_total = ev["batch_total"]
                        if isinstance(ev.get("eta_seconds"), int) and ev["eta_seconds"] >= 0:
                            self._eta_seconds = ev["eta_seconds"]
                    elif kind == "epoch":
                        ep = ev.get("epoch")
                        if isinstance(ep, dict) and _positive_int(ep.get("epoch")):
                            self._pending_epochs.append(ep)
                            self._current_epoch = ep["epoch"]
                            if _positive_int(ep.get("total_epochs")):
                                self._total_epochs = ep["total_epochs"]
                            urgent = True
                    elif kind == "done":
                        self._done_event = ev
                        urgent = True
                if urgent:
                    self._wakeup.set()
        finally:
            with contextlib.suppress(OSError):
                pipe.close()

    def _read_logs(self, pipe: IO[bytes]) -> None:
        try:
            while True:
                raw = pipe.readline()
                if not raw:
                    break
                self._stderr_tail.add(raw)
                norm = normalize_log_line(raw)
                if not norm:
                    continue
                encoded_len = len(norm.encode("utf-8"))
                flush_now = False
                with self._lock:
                    self._log_chunks.append(norm)
                    self._log_bytes += encoded_len
                    if self._log_bytes >= MAX_LOG_CHUNK_BYTES // 2:
                        flush_now = True
                if flush_now:
                    self._wakeup.set()
        finally:
            with contextlib.suppress(OSError):
                pipe.close()


# ---------------------------------------------------------------------------
# 父进程：执行平台下发的 training_job 任务


@dataclass(frozen=True)
class TrainingTaskInfo:
    """平台下发的 training_job 任务结构。"""

    id: int
    project_id: int
    init_mode: str
    yolo_version: str
    model_scale: str
    arch_yaml: str | None
    task: str
    dv_dir_name: str | None
    base_size_bytes: int | None
    base_sha256: str | None
    params: dict[str, Any]
    dataset_url: str
    base_model_url: str | None
    progress_url: str
    artifacts_url: str
    complete_url: str
    fail_url: str

    @classmethod
    def parse(cls, task: Mapping[str, Any]) -> TrainingTaskInfo:
        def url(key: str) -> str:
            v = task.get(key)
            if not isinstance(v, str) or not v.startswith("/") or "//" in v:
                raise ValueError(key)
            return v

        if not _positive_int(task.get("id")):
            raise ValueError("id")
        project_id = task.get("project_id")
        if not _positive_int(project_id):
            raise ValueError("project_id")
        init_mode = task.get("init_mode")
        if init_mode not in INIT_MODES:
            raise ValueError("init_mode")
        yolo_version = task.get("yolo_version")
        if not isinstance(yolo_version, str) or not yolo_version:
            raise ValueError("yolo_version")
        model_scale = task.get("model_scale")
        if not isinstance(model_scale, str):
            model_scale = ""
        task_type = task.get("task")
        if not isinstance(task_type, str) or not task_type:
            task_type = "detect"

        arch_yaml = task.get("arch_yaml")
        if init_mode == "scratch":
            if not isinstance(arch_yaml, str) or not arch_yaml:
                raise ValueError("arch_yaml")
        else:
            arch_yaml = arch_yaml if isinstance(arch_yaml, str) and arch_yaml else None

        dv = task.get("data_version")
        if not isinstance(dv, dict):
            raise ValueError("data_version")
        dv_dir = dv.get("dir_name")
        dv_dir_name = dv_dir if isinstance(dv_dir, str) and dv_dir else None

        base_size: int | None = None
        base_sha: str | None = None
        base_url: str | None = None
        if init_mode in ("finetune", "resume"):
            bm = task.get("base_model")
            if not isinstance(bm, dict):
                raise ValueError("base_model")
            if not _positive_int(bm.get("size_bytes")):
                raise ValueError("base_model.size_bytes")
            sha = bm.get("sha256")
            if not isinstance(sha, str) or not _SHA256.fullmatch(sha):
                raise ValueError("base_model.sha256")
            base_size = bm["size_bytes"]
            base_sha = sha.lower()
            base_url = url("base_model_url")

        params = task.get("params")
        if not isinstance(params, dict):
            raise ValueError("params")

        return cls(
            id=task["id"],
            project_id=project_id,
            init_mode=init_mode,
            yolo_version=yolo_version,
            model_scale=model_scale,
            arch_yaml=arch_yaml,
            task=task_type,
            dv_dir_name=dv_dir_name,
            base_size_bytes=base_size,
            base_sha256=base_sha,
            params=dict(params),
            dataset_url=url("dataset_url"),
            base_model_url=base_url,
            progress_url=url("progress_url"),
            artifacts_url=url("artifacts_url"),
            complete_url=url("complete_url"),
            fail_url=url("fail_url"),
        )


def safe_extract_zip(
    zip_path: Path, dest_dir: Path, abort: Callable[[], bool] | None = None
) -> Path:
    """安全解压数据集 zip 到 dest_dir（防范路径穿越），并返回其中的 data.yaml 路径。"""
    dest_resolved = dest_dir.resolve()
    data_yaml: Path | None = None
    with zipfile.ZipFile(zip_path, "r") as zf:
        for member in zf.infolist():
            if abort is not None and abort():
                raise TransportError("extract aborted")
            name = member.filename.replace("\\", "/")
            if not name or name.startswith("/"):
                raise TaskFailed(f"数据集压缩包包含非法路径：{member.filename!r}")
            target = (dest_dir / name).resolve()
            try:
                target.relative_to(dest_resolved)
            except ValueError as exc:
                raise TaskFailed(f"数据集压缩包包含越界路径：{member.filename!r}") from exc
            if member.is_dir() or name.endswith("/"):
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            with zf.open(member, "r") as src, open(target, "wb") as dst:
                shutil.copyfileobj(src, dst, length=1 << 20)
            if target.name == "data.yaml" and (
                data_yaml is None or len(target.parts) < len(data_yaml.parts)
            ):
                data_yaml = target
    if data_yaml is None or not data_yaml.is_file():
        raise TaskFailed("下载的数据集压缩包中缺少 data.yaml 配置文件")
    return data_yaml


def collect_artifacts(save_dir: Path) -> list[tuple[str, Path]]:
    """收集训练输出目录中需要上传给平台的产物（权重、曲线表、参数配置与评估图表）。"""
    items: list[tuple[str, Path]] = []
    weights_dir = save_dir / "weights"
    for wname in ("best.pt", "last.pt"):
        wp = weights_dir / wname
        if wp.is_file() and not wp.is_symlink():
            items.append((f"weights/{wname}", wp))
    if save_dir.is_dir():
        for entry in sorted(save_dir.iterdir(), key=lambda p: p.name):
            if not entry.is_file() or entry.is_symlink():
                continue
            ext = entry.suffix.lower()
            if ext in ARTIFACT_META_EXTS or ext in ARTIFACT_PLOT_EXTS:
                items.append((entry.name, entry))
    return items


class TrainingJobTask:
    """执行一个 training_job 任务。实现 tasks.Runner 协议（run / cancel）。"""

    def __init__(
        self,
        task: Mapping[str, Any],
        *,
        client: Client,
        node_key: str,
        state_dir: Path,
        node_name: str,
        data_dir: Path | None = None,
        command: Sequence[str] | None = None,
        clock: Callable[[], float] = time.monotonic,
        progress_interval: float = PROGRESS_INTERVAL,
    ) -> None:
        self.raw = task
        self.client = client
        self.node_key = node_key
        self.state_dir = state_dir
        self.node_name = node_name
        self.data_dir = data_dir
        self.clock = clock
        self.progress_interval = progress_interval
        self.process = TrainingProcess(state_dir=state_dir, command=command, clock=clock)

        self.info: TrainingTaskInfo | None = None
        self.work_dir: Path | None = None
        self.stage = "preparing"
        self._headers = {"X-Node-Key": node_key}
        self._stopping = threading.Event()
        self._finished = threading.Event()
        self._abandoned: Exception | None = None
        self._progress_lock = threading.Lock()
        self._last_progress_at = 0.0

    def cancel(self) -> None:
        self._stopping.set()
        self.process.cancel()

    def _abandon(self, reason: Exception) -> None:
        if self._abandoned is None:
            self._abandoned = reason
        self.process.cancel()

    def _interrupted(self) -> bool:
        return self._stopping.is_set() or self._abandoned is not None

    def _check_interrupted(self) -> None:
        if self._abandoned is not None:
            raise TaskAbandoned(str(self._abandoned))
        if self._stopping.is_set():
            raise TaskFailed("训练节点正在停止，训练已中断", retryable=True)

    def _api_error(self, exc: ApiError, what: str) -> Exception:
        if exc.status in (404, 409):
            return TaskAbandoned(f"{what}: {exc}")
        retryable = (exc.status >= 500 and exc.status != 507) or exc.status == 401
        return TaskFailed(f"{what}失败：{exc.message}", str(exc), retryable=retryable)

    def run(self) -> str:
        try:
            self.info = info = TrainingTaskInfo.parse(self.raw)
        except ValueError as exc:
            return self._invalid(str(exc))

        started = self.clock()
        log.info(
            "training job %s started: init_mode=%s yolo=%s%s epochs=%s",
            info.id,
            info.init_mode,
            info.yolo_version,
            info.model_scale,
            info.params.get("epochs"),
        )
        keeper = threading.Thread(
            target=self._keep_lease, name=f"training-job-{info.id}-lease", daemon=True
        )
        keeper.start()
        try:
            summary = self._execute(info)
        except TaskAbandoned as exc:
            log.info(
                "training job %s abandoned after %.1fs: canceled or deleted on platform (%s)",
                info.id,
                self.clock() - started,
                exc,
            )
            return "abandoned"
        except TaskFailed as failure:
            if self._abandoned is not None:
                log.info("training job %s abandoned: %s", info.id, self._abandoned)
                return "abandoned"
            log.warning(
                "training job %s failed after %.1fs (retryable=%s): %s",
                info.id,
                self.clock() - started,
                failure.retryable,
                failure.message,
            )
            self._finished.set()
            stopping = self._stopping.is_set()
            report_failure(
                self.client,
                self.node_key,
                info.fail_url,
                failure,
                attempts=1 if stopping else len(FAIL_RETRY_DELAYS) + 1,
                timeout=STOPPING_FAIL_TIMEOUT if stopping else None,
                wait=self._stopping.wait,
            )
            return "failed"
        except Exception as exc:
            log.exception("training job %s failed unexpectedly", info.id)
            if self._abandoned is not None:
                return "abandoned"
            failure = TaskFailed(
                f"训练节点执行训练时发生意外错误：{_clean_message(exc)}",
                summarize_exception(exc, MAX_DETAIL),
                retryable=True,
            )
            self._finished.set()
            report_failure(self.client, self.node_key, info.fail_url, failure, attempts=1)
            return "failed"
        finally:
            self._finished.set()
            keeper.join(timeout=2 if self._stopping.is_set() else 15)
            self._cleanup()

        log.info(
            "training job %s succeeded in %.1fs: uploaded %d artifacts",
            info.id,
            self.clock() - started,
            summary["uploaded"],
        )
        return "succeeded"

    def _invalid(self, field: str) -> str:
        task_id = self.raw.get("id")
        log.warning("training job task %r is malformed (field %s)", task_id, field)
        url = fail_url_of(self.raw)
        if url is not None:
            failure = TaskFailed(
                f"训练节点无法识别平台下发的训练任务（字段 {field} 缺失或不合法），"
                f"请确认平台与训练节点（{__version__}）版本一致"
            )
            report_failure(self.client, self.node_key, url, failure, attempts=1)
        return "invalid"

    def _execute(self, info: TrainingTaskInfo) -> dict[str, Any]:
        work = self._prepare_work_dir(info)
        self._check_disk(work, (info.base_size_bytes or 0) + MIN_FREE_DISK_BYTES)

        self._set_stage(
            "preparing",
            log_chunk=f"[trainMaster] 训练节点 {self.node_name} 已接管训练任务 #{info.id}\n",
        )
        data_yaml = self._prepare_dataset(info, work)

        base_weights: Path | None = None
        if info.init_mode in ("finetune", "resume"):
            self._set_stage("downloading", log_chunk="[trainMaster] 正在下载基础模型权重...\n")
            base_weights = work / "base.pt"
            self._download_base_model(info, base_weights)

        self._set_stage("training", log_chunk=f"[trainMaster] 数据集就绪：{data_yaml}\n")
        runs_dir = work / "runs"
        runs_dir.mkdir(parents=True, exist_ok=True)
        spec = TrainingSpec(
            init_mode=info.init_mode,
            data_yaml=str(data_yaml),
            project_dir=str(runs_dir),
            run_name="train",
            arch_yaml=info.arch_yaml,
            base_weights=str(base_weights) if base_weights is not None else None,
            yolo_version=info.yolo_version,
            task=info.task,
            params=info.params,
        )

        def on_tick(urgent: bool) -> None:
            drained = self.process.drain_progress()
            has_updates = bool(drained["epochs"] or drained["log_chunk"])
            due = (self.clock() - self._last_progress_at) >= self.progress_interval
            if urgent or has_updates or due or drained["current_epoch"] is not None:
                self._send_progress(drained)

        outcome = self.process.run(
            spec,
            cwd=work,
            on_tick=on_tick,
            tick_interval=min(LOG_FLUSH_INTERVAL, self.progress_interval),
        )
        # 确保剩余日志和最后一轮指标全部送达平台
        final_drain = self.process.drain_progress()
        if final_drain["epochs"] or final_drain["log_chunk"]:
            self._send_progress(final_drain)

        self._check_interrupted()
        if not outcome["ok"]:
            raise TaskFailed(outcome["error"], outcome.get("detail"))

        save_dir = Path(outcome["save_dir"])
        artifacts = collect_artifacts(save_dir)
        if not any(rel == "weights/best.pt" for rel, _ in artifacts):
            raise TaskFailed("训练已完成但未找到最佳模型权重 weights/best.pt")

        self._set_stage(
            "uploading", log_chunk=f"[trainMaster] 正在上传 {len(artifacts)} 个训练产物...\n"
        )
        for rel_path, file_path in artifacts:
            self._check_interrupted()
            self._upload_artifact(info, rel_path, file_path)

        result = dict(outcome["result"], node_name=self.node_name)
        try:
            self.client.post(info.complete_url, {"result": result}, headers=self._headers)
        except ApiError as exc:
            raise self._api_error(exc, "完成训练任务") from None
        except TransportError as exc:
            self._check_interrupted()
            raise TaskFailed(f"无法通知平台训练完成：{exc}", retryable=True) from None

        return {"uploaded": len(artifacts)}

    def _prepare_work_dir(self, info: TrainingTaskInfo) -> Path:
        work = self.state_dir / "work" / f"{WORK_DIR_PREFIX}{info.id}"
        try:
            if work.exists():
                shutil.rmtree(work)
            work.mkdir(parents=True)
        except OSError as exc:
            raise TaskFailed(f"无法创建工作目录 {work}：{exc}", retryable=True) from None
        self.work_dir = work
        return work

    def _check_disk(self, work: Path, need: int) -> None:
        try:
            free = shutil.disk_usage(work).free
        except OSError:
            return
        if free < need:
            raise TaskFailed(
                f"训练节点磁盘空间不足：至少需要 {format_bytes(need)}，当前仅剩 {format_bytes(free)}",
                retryable=True,
            )

    def _prepare_dataset(self, info: TrainingTaskInfo, work: Path) -> Path:
        """优先使用与服务端共享且可写的 /data/exports/<dir_name>/data.yaml；否则从平台流式下载 zip 解压。"""
        if self.data_dir is not None and info.dv_dir_name:
            rel = Path(*info.dv_dir_name.strip("/").split("/"))
            candidate_dir = self.data_dir / "exports" / rel
            candidate_yaml = candidate_dir / "data.yaml"
            if (
                not any(part == ".." for part in rel.parts)
                and candidate_yaml.is_file()
                and not (candidate_dir / ".pending").exists()
                and os.access(candidate_dir, os.W_OK)
            ):
                log.info(
                    "training job %s using shared dataset directory %s", info.id, candidate_dir
                )
                return candidate_yaml

        self._set_stage("downloading", log_chunk="[trainMaster] 正在从平台下载训练数据集归档...\n")
        zip_path = work / "dataset.zip"
        try:
            self.client.download(
                info.dataset_url,
                zip_path,
                headers=self._headers,
                timeout=TRANSFER_TIMEOUT,
                max_bytes=MAX_DATASET_ZIP_BYTES,
                abort=self._interrupted,
            )
        except ApiError as exc:
            raise self._api_error(exc, "下载训练数据集") from None
        except TransportError as exc:
            self._check_interrupted()
            raise TaskFailed(f"下载训练数据集失败：{exc}", retryable=True) from None
        except OSError as exc:
            raise TaskFailed(f"写入训练数据集压缩包失败：{exc}", retryable=True) from None

        self._check_interrupted()
        dataset_dir = work / "dataset"
        dataset_dir.mkdir(parents=True, exist_ok=True)
        try:
            data_yaml = safe_extract_zip(zip_path, dataset_dir, abort=self._interrupted)
        except zipfile.BadZipFile as exc:
            raise TaskFailed(f"下载的数据集压缩包已损坏：{exc}", retryable=True) from None
        except TransportError:
            self._check_interrupted()
            raise TaskFailed("解压训练数据集被中断", retryable=True) from None
        except OSError as exc:
            raise TaskFailed(f"解压训练数据集失败：{exc}", retryable=True) from None
        finally:
            with contextlib.suppress(OSError):
                zip_path.unlink()
        return data_yaml

    def _download_base_model(self, info: TrainingTaskInfo, dest: Path) -> None:
        assert info.base_model_url is not None
        assert info.base_size_bytes is not None and info.base_sha256 is not None
        try:
            got = self.client.download(
                info.base_model_url,
                dest,
                headers=self._headers,
                timeout=TRANSFER_TIMEOUT,
                max_bytes=info.base_size_bytes,
                abort=self._interrupted,
            )
        except ApiError as exc:
            raise self._api_error(exc, "下载基础模型") from None
        except TransportError as exc:
            self._check_interrupted()
            raise TaskFailed(f"下载基础模型失败：{exc}", retryable=True) from None
        except OSError as exc:
            raise TaskFailed(f"写入基础模型失败：{exc}", retryable=True) from None
        self._check_interrupted()
        verify_download(got, info.base_size_bytes, info.base_sha256)

    def _upload_artifact(self, info: TrainingTaskInfo, rel_path: str, file_path: Path) -> None:
        try:
            size = file_path.stat().st_size
        except OSError as exc:
            raise TaskFailed(f"无法读取训练产物 {rel_path}：{exc}") from None
        if size > MAX_OUTPUT_BYTES:
            raise TaskFailed(
                f"训练产物 {rel_path} 过大（{format_bytes(size)}），超过上限 {format_bytes(MAX_OUTPUT_BYTES)}"
            )
        query = urllib.parse.urlencode({"path": rel_path})
        target_url = f"{info.artifacts_url}?{query}"
        try:
            sha = file_sha256(file_path, self._interrupted)
            self.client.put_file(
                target_url,
                file_path,
                headers={**self._headers, "X-Content-Sha256": sha},
                timeout=TRANSFER_TIMEOUT,
                abort=self._interrupted,
            )
        except ApiError as exc:
            if exc.status == 422 and exc.message and "Sha256" in exc.message:
                raise TaskFailed(
                    f"上传训练产物 {rel_path} 失败：{exc.message}", str(exc), retryable=True
                ) from None
            raise self._api_error(exc, f"上传训练产物 {rel_path}") from None
        except TransportError as exc:
            self._check_interrupted()
            raise TaskFailed(f"上传训练产物 {rel_path} 失败：{exc}", retryable=True) from None
        except OSError as exc:
            raise TaskFailed(f"读取训练产物 {rel_path} 失败：{exc}") from None

    def _set_stage(self, stage: str, *, log_chunk: str = "") -> None:
        self._check_interrupted()
        self.stage = stage
        self._send_progress({"log_chunk": log_chunk} if log_chunk else {})
        self._check_interrupted()

    def _send_progress(self, updates: Mapping[str, Any] | None = None) -> ApiError | None:
        assert self.info is not None
        payload: dict[str, Any] = {"stage": self.stage}
        if updates:
            for key in (
                "current_epoch",
                "total_epochs",
                "batch_done",
                "batch_total",
                "eta_seconds",
            ):
                if updates.get(key) is not None:
                    payload[key] = updates[key]
            if updates.get("epochs"):
                payload["epochs"] = updates["epochs"]
            if updates.get("log_chunk"):
                payload["log_chunk"] = updates["log_chunk"]

        with self._progress_lock:
            if self._abandoned is not None:
                return None
            self._last_progress_at = self.clock()
            try:
                self.client.post(self.info.progress_url, payload, headers=self._headers)
            except ApiError as exc:
                if exc.status in (404, 409):
                    self._abandon(exc)
                else:
                    log.debug("training job %s: progress report failed: %s", self.info.id, exc)
                return exc
            except TransportError as exc:
                log.debug("training job %s: progress report failed: %s", self.info.id, exc)
        return None

    def _keep_lease(self) -> None:
        while not self._finished.wait(self.progress_interval):
            if self._stopping.is_set():
                return
            if self.clock() - self._last_progress_at >= self.progress_interval * 0.8:
                self._send_progress()

    def _cleanup(self) -> None:
        work = self.work_dir
        if work is None:
            return
        try:
            shutil.rmtree(work)
        except FileNotFoundError:
            pass
        except OSError as exc:
            log.warning("cannot remove training work directory %s: %s", work, exc)


if __name__ == "__main__":
    sys.exit(child_main())
