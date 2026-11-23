"""模型评估任务（model_evaluation）：在训练节点上加载 .pt 模型对数据版本或数据集样本做低置信度
批量推理（conf=0.001），按类别名称对齐计算 Ultralytics/COCO 口径的 mAP50 / mAP75 / mAP50-95
与 4 条完整曲线（PR / F1-Conf / Precision-Conf / Recall-Conf），并将紧凑预测框（conf >= store_min_conf）
与曲线数据 gzip 上传给平台，支持前端免 GPU 实时拖动阈值分析。

与 review_inference.py / training.py 一样分两部分：
- 子进程（python -m trainmaster_trainer.evaluation --spec <json>）：在隔离环境中导入 torch / ultralytics，
  开启安全加载（ULTRALYTICS_SAFE_LOAD=1），批量推理并向 stdout 输出 JSON lines 进度事件；
- 父进程（ModelEvaluationTask）：下载并校验权重、按页获取样本清单、优先零拷贝读取本地共享数据版本文件
  （不可用时自动并发 HTTP 下载回退）、监控子进程并续租、上传 gzip 结果并完成任务。
"""

from __future__ import annotations

import argparse
import bisect
import concurrent.futures
import contextlib
import json
import logging
import math
import secrets
import shutil
import subprocess
import sys
import threading
import time
import traceback
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from . import __version__, procs
from .client import ApiError, Client, TransportError, json_safe
from .model_export import (
    MAX_DETAIL,
    MAX_ERROR,
    PROGRESS_INTERVAL,
    SAFE_LOAD_ENV,
    TRANSFER_TIMEOUT,
    ExportSpec,
    TaskAbandoned,
    TaskFailed,
    _clean_message,
    _is_out_of_memory,
    _join_detail,
    export_env,
    fail_url_of,
    report_failure,
    require_safe_load,
    should_use_end2end_inference,
    translate_load_error,
    verify_download,
)

log = logging.getLogger(__name__)

KIND = "model_evaluation"
WORK_DIR_PREFIX = "model-evaluation-"
CHILD_MODULE = "trainmaster_trainer.evaluation"

LOW_CONF = 0.001
PAGE_SIZE = 200
PREFETCH_WORKERS = 4
MAX_IMAGE_BYTES = 1 << 30
MAX_STDERR = 64 * 1024
IOU_THRESHOLDS = tuple(round(0.50 + 0.05 * i, 2) for i in range(10))


def _round4(v: float) -> float:
    if not math.isfinite(v):
        return 0.0
    return round(v, 4)


def box_iou(
    ax1: float,
    ay1: float,
    ax2: float,
    ay2: float,
    bx1: float,
    by1: float,
    bx2: float,
    by2: float,
) -> float:
    """计算两个轴对齐边界框 [xmin, ymin, xmax, ymax] 的交并比 IoU。"""
    iw = min(ax2, bx2) - max(ax1, bx1)
    ih = min(ay2, by2) - max(ay1, by1)
    if iw <= 0.0 or ih <= 0.0:
        return 0.0
    inter = iw * ih
    area_a = max(0.0, ax2 - ax1) * max(0.0, ay2 - ay1)
    area_b = max(0.0, bx2 - bx1) * max(0.0, by2 - by1)
    union = area_a + area_b - inter
    if union <= 0.0:
        return 0.0
    return inter / union


def _f1(p: float, r: float) -> float:
    return (2.0 * p * r / (p + r)) if (p + r) > 0.0 else 0.0


def compute_ap101(rec: Sequence[float], prec: Sequence[float]) -> float:
    """Ultralytics / COCO 101 点精度包络积分。"""
    if not rec:
        return 0.0
    mrec = [0.0, *rec, 1.0]
    mpre = [0.0, *prec, 0.0]
    for i in range(len(mpre) - 2, -1, -1):
        if mpre[i] < mpre[i + 1]:
            mpre[i] = mpre[i + 1]
    total = 0.0
    n = len(mrec)
    idx = 0
    for k in range(101):
        r = k / 100.0
        while idx < n and mrec[idx] < r:
            idx += 1
        if idx < n:
            total += mpre[idx]
    return total / 101.0


def _sample_pr_curve(
    rec: Sequence[float], prec: Sequence[float], x_grid: Sequence[float]
) -> list[float]:
    if not rec:
        return [0.0] * len(x_grid)
    env = list(prec)
    for i in range(len(env) - 2, -1, -1):
        if env[i] < env[i + 1]:
            env[i] = env[i + 1]
    out: list[float] = []
    j = 0
    n = len(rec)
    for r in x_grid:
        while j < n and rec[j] < r:
            j += 1
        out.append(_round4(env[j]) if j < n else 0.0)
    return out


def _sample_vs_conf(
    confs_desc: Sequence[float],
    vals: Sequence[float],
    x_grid: Sequence[float],
    high_end_val: float,
) -> list[float]:
    """将按置信度降序记录的曲线采样到升序 x_grid（0.00..1.00）。"""
    if not confs_desc:
        return [0.0] * len(x_grid)
    # 转为升序以便用 bisect_left 查找 >= c 的最小位置（即原降序数组中的最大索引）。
    n = len(confs_desc)
    confs_asc = list(reversed(confs_desc))
    out: list[float] = []
    for c in x_grid:
        pos = bisect.bisect_left(confs_asc, c)
        if pos >= n:
            out.append(_round4(high_end_val))
        else:
            orig_idx = n - 1 - pos
            out.append(_round4(vals[orig_idx]))
    return out


def compute_curves(
    classes: Sequence[Mapping[str, Any]],
    samples: Sequence[Mapping[str, Any]],
    speed: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """在全低置信度（conf >= 0.001）检测结果上计算精确 COCO 10-IoU mAP 与 4 条 101 点曲线。

    按类别名称精确对齐（与模型类别索引顺序无关），与后端 Go ComputeCurvesFromSamples 口径完全一致。
    """
    gt_by_class: dict[str, int] = {}
    dets_by_class: dict[str, list[tuple[float, list[bool]]]] = {}
    color_by_class = {str(c.get("name", "")): str(c.get("color", "")) for c in classes}

    for smp in samples:
        if smp.get("inference_error"):
            continue
        gt_per_cls: dict[str, list[Mapping[str, Any]]] = {}
        for g in smp.get("gt_objects") or ():
            name = str(g.get("name", ""))
            gt_per_cls.setdefault(name, []).append(g)
            gt_by_class[name] = gt_by_class.get(name, 0) + 1

        pred_per_cls: dict[str, list[Mapping[str, Any]]] = {}
        for p in smp.get("predictions") or ():
            name = str(p.get("name", ""))
            pred_per_cls.setdefault(name, []).append(p)

        for cls_name, preds in pred_per_cls.items():
            preds_sorted = sorted(preds, key=lambda x: -float(x.get("conf", 0.0)))
            gts = gt_per_cls.get(cls_name) or []
            bucket = dets_by_class.setdefault(cls_name, [])
            if not gts:
                for p in preds_sorted:
                    bucket.append((float(p["conf"]), [False] * 10))
                continue

            ious = [
                [
                    box_iou(
                        float(p["xmin"]),
                        float(p["ymin"]),
                        float(p["xmax"]),
                        float(p["ymax"]),
                        float(g["xmin"]),
                        float(g["ymin"]),
                        float(g["xmax"]),
                        float(g["ymax"]),
                    )
                    for g in gts
                ]
                for p in preds_sorted
            ]
            tp_mats = [[False] * 10 for _ in preds_sorted]
            for ti, thr in enumerate(IOU_THRESHOLDS):
                used = [False] * len(gts)
                for pi in range(len(preds_sorted)):
                    best_gi = -1
                    best_v = thr
                    row = ious[pi]
                    for gi, v in enumerate(row):
                        if not used[gi] and v >= best_v and (best_gi < 0 or v > best_v):
                            best_v = v
                            best_gi = gi
                    if best_gi >= 0:
                        used[best_gi] = True
                        tp_mats[pi][ti] = True
            for pi, p in enumerate(preds_sorted):
                bucket.append((float(p["conf"]), tp_mats[pi]))

    grid_n = 101
    x_grid = [round(i / (grid_n - 1), 4) for i in range(grid_n)]

    pr_series: list[dict[str, Any]] = []
    f1_series: list[dict[str, Any]] = []
    p_series: list[dict[str, Any]] = []
    r_series: list[dict[str, Any]] = []
    class_ap50: dict[str, float] = {}
    class_ap75: dict[str, float] = {}
    class_ap50_95: dict[str, float] = {}

    sum_ap50 = 0.0
    sum_ap75 = 0.0
    sum_ap50_95 = 0.0
    gt_cls_count = 0
    mean_pr = [0.0] * grid_n
    mean_p = [0.0] * grid_n
    mean_r = [0.0] * grid_n
    mean_f1 = [0.0] * grid_n

    for c in classes:
        cls_name = str(c.get("name", ""))
        n_gt = gt_by_class.get(cls_name, 0)
        if n_gt == 0:
            continue
        gt_cls_count += 1
        recs = sorted(dets_by_class.get(cls_name, ()), key=lambda x: -x[0])

        ap10 = [0.0] * 10
        confs: list[float] = []
        p_curve50: list[float] = []
        r_curve50: list[float] = []
        if recs:
            confs = [r[0] for r in recs]
            for ti in range(10):
                tp_sum = 0.0
                fp_sum = 0.0
                rec_arr: list[float] = []
                prec_arr: list[float] = []
                for _, tp_mat in recs:
                    if tp_mat[ti]:
                        tp_sum += 1.0
                    else:
                        fp_sum += 1.0
                    rec_arr.append(tp_sum / n_gt)
                    prec_arr.append(tp_sum / (tp_sum + fp_sum))
                ap10[ti] = compute_ap101(rec_arr, prec_arr)
                if ti == 0:
                    r_curve50 = rec_arr
                    p_curve50 = prec_arr

        ap_mean = sum(ap10) / 10.0
        class_ap50[cls_name] = _round4(ap10[0])
        class_ap75[cls_name] = _round4(ap10[5])
        class_ap50_95[cls_name] = _round4(ap_mean)
        sum_ap50 += ap10[0]
        sum_ap75 += ap10[5]
        sum_ap50_95 += ap_mean

        pr_y = _sample_pr_curve(r_curve50, p_curve50, x_grid)
        p_y = _sample_vs_conf(confs, p_curve50, x_grid, 1.0)
        r_y = _sample_vs_conf(confs, r_curve50, x_grid, 0.0)
        f1_y = [_round4(_f1(p_y[i], r_y[i])) for i in range(grid_n)]
        for i in range(grid_n):
            mean_pr[i] += pr_y[i]
            mean_p[i] += p_y[i]
            mean_r[i] += r_y[i]
            mean_f1[i] += f1_y[i]

        color = color_by_class.get(cls_name, "")
        pr_series.append({"name": cls_name, "color": color, "x": x_grid, "y": pr_y})
        p_series.append({"name": cls_name, "color": color, "x": x_grid, "y": p_y})
        r_series.append({"name": cls_name, "color": color, "x": x_grid, "y": r_y})
        f1_series.append({"name": cls_name, "color": color, "x": x_grid, "y": f1_y})

    map50 = 0.0
    map75 = 0.0
    map50_95 = 0.0
    best_f1 = 0.0
    best_f1_conf = 0.25

    if gt_cls_count > 0:
        map50 = _round4(sum_ap50 / gt_cls_count)
        map75 = _round4(sum_ap75 / gt_cls_count)
        map50_95 = _round4(sum_ap50_95 / gt_cls_count)
        best_val = -1.0
        for i in range(grid_n):
            mean_pr[i] = _round4(mean_pr[i] / gt_cls_count)
            mean_p[i] = _round4(mean_p[i] / gt_cls_count)
            mean_r[i] = _round4(mean_r[i] / gt_cls_count)
            mean_f1[i] = _round4(mean_f1[i] / gt_cls_count)
            if mean_f1[i] > best_val:
                best_val = mean_f1[i]
                best_f1_conf = x_grid[i]
        best_f1 = _round4(max(0.0, best_val))
        pr_series.insert(0, {"name": "all", "color": "#2563eb", "x": x_grid, "y": mean_pr})
        p_series.insert(0, {"name": "all", "color": "#f59e0b", "x": x_grid, "y": mean_p})
        r_series.insert(0, {"name": "all", "color": "#8b5cf6", "x": x_grid, "y": mean_r})
        f1_series.insert(0, {"name": "all", "color": "#10b981", "x": x_grid, "y": mean_f1})

    result: dict[str, Any] = {
        "pr": pr_series,
        "f1": f1_series,
        "precision": p_series,
        "recall": r_series,
        "class_ap50": class_ap50,
        "class_ap75": class_ap75,
        "class_ap50_95": class_ap50_95,
        "map50": map50,
        "map75": map75,
        "map50_95": map50_95,
        "best_f1": best_f1,
        "best_f1_conf": _round4(best_f1_conf),
    }
    if speed is not None:
        result["speed"] = dict(speed)
    return result


# ---- 子进程配置与执行 ----


@dataclass(frozen=True)
class EvaluationSpec:
    weights: str
    manifest_path: str
    output_path: str
    classes: tuple[dict[str, Any], ...]
    task: str | None = None
    yolo_version: str | None = None
    nms_iou: float = 0.70
    imgsz: tuple[int, int] | None = None
    batch: int = 16
    max_det: int = 300
    half: bool = False
    device: str | None = None
    low_conf: float = LOW_CONF
    store_min_conf: float = 0.05

    @classmethod
    def from_json(cls, data: Any) -> EvaluationSpec:
        if not isinstance(data, dict):
            raise ValueError("spec 应为 JSON 对象")
        weights = data.get("weights")
        manifest_path = data.get("manifest_path")
        output_path = data.get("output_path")
        if not isinstance(weights, str) or not weights:
            raise ValueError("缺少 weights")
        if not isinstance(manifest_path, str) or not manifest_path:
            raise ValueError("缺少 manifest_path")
        if not isinstance(output_path, str) or not output_path:
            raise ValueError("缺少 output_path")

        raw_imgsz = data.get("imgsz")
        imgsz: tuple[int, int] | None = None
        if isinstance(raw_imgsz, list) and len(raw_imgsz) == 2:
            imgsz = (int(raw_imgsz[0]), int(raw_imgsz[1]))
        elif isinstance(raw_imgsz, int) and raw_imgsz > 0:
            imgsz = (raw_imgsz, raw_imgsz)

        raw_classes = data.get("classes") or []
        if not isinstance(raw_classes, list):
            raise ValueError("classes 应为数组")

        return cls(
            weights=weights,
            manifest_path=manifest_path,
            output_path=output_path,
            classes=tuple(c for c in raw_classes if isinstance(c, dict)),
            task=data.get("task") or None,
            yolo_version=data.get("yolo_version") or None,
            nms_iou=float(data.get("nms_iou", 0.70)),
            imgsz=imgsz,
            batch=max(1, int(data.get("batch", 16))),
            max_det=max(1, int(data.get("max_det", 300))),
            half=bool(data.get("half", False)),
            device=(str(data.get("device")).strip() or None)
            if data.get("device") is not None
            else None,
            low_conf=float(data.get("low_conf", LOW_CONF)),
            store_min_conf=float(data.get("store_min_conf", 0.05)),
        )


def _emit(event: dict[str, Any]) -> None:
    sys.stdout.write(json.dumps(json_safe(event), ensure_ascii=False) + "\n")
    sys.stdout.flush()


def _extract_predictions(result: Any, names_map: Mapping[int, str]) -> list[dict[str, Any]]:
    """从 Ultralytics Result 中统一提取轴对齐检测框（兼容 detect/segment/pose/obb）。"""
    preds: list[dict[str, Any]] = []
    boxes = getattr(result, "boxes", None)
    if boxes is not None and len(boxes) > 0:
        xyxy = boxes.xyxy.detach().cpu().tolist()
        confs = boxes.conf.detach().cpu().tolist()
        clss = boxes.cls.detach().cpu().tolist()
        for coords, conf, cls_val in zip(xyxy, confs, clss, strict=True):
            cid = int(cls_val)
            preds.append(
                {
                    "name": str(names_map.get(cid, str(cid))),
                    "class_idx": cid,
                    "xmin": round(float(coords[0]), 2),
                    "ymin": round(float(coords[1]), 2),
                    "xmax": round(float(coords[2]), 2),
                    "ymax": round(float(coords[3]), 2),
                    "conf": _round4(float(conf)),
                }
            )
        return preds

    obb = getattr(result, "obb", None)
    if obb is not None and len(obb) > 0:
        confs = obb.conf.detach().cpu().tolist()
        clss = obb.cls.detach().cpu().tolist()
        if hasattr(obb, "xyxyxyxy"):
            polys = obb.xyxyxyxy.detach().cpu().tolist()
            for poly, conf, cls_val in zip(polys, confs, clss, strict=True):
                xs = [float(pt[0]) for pt in poly]
                ys = [float(pt[1]) for pt in poly]
                cid = int(cls_val)
                preds.append(
                    {
                        "name": str(names_map.get(cid, str(cid))),
                        "class_idx": cid,
                        "xmin": round(min(xs), 2),
                        "ymin": round(min(ys), 2),
                        "xmax": round(max(xs), 2),
                        "ymax": round(max(ys), 2),
                        "conf": _round4(float(conf)),
                    }
                )
        elif hasattr(obb, "xyxy"):
            xyxy = obb.xyxy.detach().cpu().tolist()
            for coords, conf, cls_val in zip(xyxy, confs, clss, strict=True):
                cid = int(cls_val)
                preds.append(
                    {
                        "name": str(names_map.get(cid, str(cid))),
                        "class_idx": cid,
                        "xmin": round(float(coords[0]), 2),
                        "ymin": round(float(coords[1]), 2),
                        "xmax": round(float(coords[2]), 2),
                        "ymax": round(float(coords[3]), 2),
                        "conf": _round4(float(conf)),
                    }
                )
    return preds


def run_evaluation_subprocess(spec: EvaluationSpec) -> dict[str, Any]:
    """在子进程内加载模型并执行低置信度全量批量推理 + COCO 指标计算。"""
    import torch
    import ultralytics
    from ultralytics import YOLO

    node_version = str(getattr(ultralytics, "__version__", "")) or "unknown"
    require_safe_load(node_version)

    device = spec.device or ("cuda:0" if torch.cuda.is_available() else "cpu")
    use_half = spec.half and device != "cpu" and ("cpu" not in device.lower())

    try:
        model = YOLO(spec.weights, task=spec.task)
    except Exception as exc:
        load_spec = ExportSpec(weights=spec.weights, task=spec.task, yolo_version=spec.yolo_version)
        msg = translate_load_error(exc, load_spec, node_version)
        raise RuntimeError(msg) from exc

    raw_names = getattr(model, "names", None) or {}
    if isinstance(raw_names, list):
        names_map = {i: str(v) for i, v in enumerate(raw_names)}
    elif isinstance(raw_names, dict):
        names_map = {int(k): str(v) for k, v in raw_names.items()}
    else:
        names_map = {}

    manifest = json.loads(Path(spec.manifest_path).read_text(encoding="utf-8"))
    total = len(manifest)

    use_end2end = should_use_end2end_inference(model)
    predict_kwargs: dict[str, Any] = {
        "conf": spec.low_conf,
        "iou": spec.nms_iou,
        "max_det": spec.max_det,
        "half": use_half,
        "device": device,
        "verbose": False,
    }
    if use_end2end:
        predict_kwargs["nms"] = False
    if spec.imgsz is not None:
        predict_kwargs["imgsz"] = list(spec.imgsz)

    effective_imgsz = list(spec.imgsz) if spec.imgsz else [640, 640]
    if spec.imgsz is None:
        with contextlib.suppress(Exception):
            arg_sz = getattr(getattr(model, "overrides", None), "get", lambda *_: None)("imgsz")
            if isinstance(arg_sz, int) and arg_sz > 0:
                effective_imgsz = [arg_sz, arg_sz]
            elif isinstance(arg_sz, (list, tuple)) and len(arg_sz) == 2:
                effective_imgsz = [int(arg_sz[0]), int(arg_sz[1])]

    _emit({"event": "progress", "stage": "inferring", "done": 0, "total": total})

    all_records: list[dict[str, Any]] = []
    stored_predictions: list[dict[str, Any]] = []
    sum_pre = 0.0
    sum_inf = 0.0
    sum_post = 0.0
    timed_images = 0
    done = 0
    started = time.monotonic()

    # 按 batch 切分样本；已标记下载失败的样本直接记录错误。
    i = 0
    while i < total:
        chunk = manifest[i : i + spec.batch]
        i += spec.batch

        valid_entries: list[dict[str, Any]] = []
        for entry in chunk:
            if entry.get("inference_error") or not entry.get("local_path"):
                err_msg = str(entry.get("inference_error") or "源图片不可用")
                rec = {
                    "sample_id": int(entry["sample_id"]),
                    "width": int(entry.get("width") or 0),
                    "height": int(entry.get("height") or 0),
                    "gt_objects": entry.get("gt_objects") or [],
                    "predictions": [],
                    "inference_error": err_msg,
                }
                all_records.append(rec)
                stored_predictions.append(
                    {
                        "sample_id": rec["sample_id"],
                        "width": rec["width"],
                        "height": rec["height"],
                        "inference_error": err_msg,
                        "predictions": [],
                    }
                )
                done += 1
            else:
                valid_entries.append(entry)

        if valid_entries:
            paths = [str(e["local_path"]) for e in valid_entries]
            results: list[Any] | None = None
            try:
                results = list(model.predict(source=paths, **predict_kwargs))
            except Exception as batch_exc:
                if _is_out_of_memory(batch_exc):
                    raise
                # 批处理中个别损坏图片导致失败时，回退到逐张推理以隔离损坏样本。
                results = None

            if results is not None and len(results) == len(valid_entries):
                pairs = list(zip(valid_entries, results, strict=True))
            else:
                pairs = []
                for entry in valid_entries:
                    try:
                        single = list(
                            model.predict(source=[str(entry["local_path"])], **predict_kwargs)
                        )
                        pairs.append((entry, single[0] if single else None))
                    except Exception as single_exc:
                        if _is_out_of_memory(single_exc):
                            raise
                        pairs.append((entry, single_exc))

            for entry, res in pairs:
                sid = int(entry["sample_id"])
                w = int(entry.get("width") or 0)
                h = int(entry.get("height") or 0)
                if isinstance(res, Exception) or res is None:
                    err_text = (
                        _clean_message(str(res)) if isinstance(res, Exception) else "推理无返回结果"
                    )
                    all_records.append(
                        {
                            "sample_id": sid,
                            "width": w,
                            "height": h,
                            "gt_objects": entry.get("gt_objects") or [],
                            "predictions": [],
                            "inference_error": err_text,
                        }
                    )
                    stored_predictions.append(
                        {
                            "sample_id": sid,
                            "width": w,
                            "height": h,
                            "inference_error": err_text,
                            "predictions": [],
                        }
                    )
                else:
                    orig_shape = getattr(res, "orig_shape", None)
                    if orig_shape and len(orig_shape) >= 2:
                        h, w = int(orig_shape[0]), int(orig_shape[1])
                    spd = getattr(res, "speed", None) or {}
                    sum_pre += float(spd.get("preprocess") or 0.0)
                    sum_inf += float(spd.get("inference") or 0.0)
                    sum_post += float(spd.get("postprocess") or 0.0)
                    timed_images += 1

                    raw_preds = _extract_predictions(res, names_map)
                    all_records.append(
                        {
                            "sample_id": sid,
                            "width": w,
                            "height": h,
                            "gt_objects": entry.get("gt_objects") or [],
                            "predictions": raw_preds,
                            "inference_error": "",
                        }
                    )
                    kept = [p for p in raw_preds if p["conf"] >= spec.store_min_conf]
                    kept.sort(key=lambda x: (-x["conf"], x["name"]))
                    stored_predictions.append(
                        {
                            "sample_id": sid,
                            "width": w,
                            "height": h,
                            "inference_error": "",
                            "predictions": kept,
                        }
                    )
                done += 1

        elapsed = max(0.001, time.monotonic() - started)
        eta = int(round((total - done) * (elapsed / done))) if done > 0 and done < total else 0
        _emit(
            {
                "event": "progress",
                "stage": "inferring",
                "done": done,
                "total": total,
                "eta_seconds": eta,
            }
        )

    _emit(
        {"event": "progress", "stage": "computing", "done": total, "total": total, "eta_seconds": 0}
    )

    speed_info: dict[str, Any] | None = None
    if timed_images > 0:
        avg_pre = sum_pre / timed_images
        avg_inf = sum_inf / timed_images
        avg_post = sum_post / timed_images
        avg_total = avg_pre + avg_inf + avg_post
        speed_info = {
            "preprocess_ms": round(avg_pre, 2),
            "inference_ms": round(avg_inf, 2),
            "postprocess_ms": round(avg_post, 2),
            "total_ms": round(avg_total, 2),
            "fps": round(1000.0 / avg_total, 1) if avg_total > 0 else 0.0,
            "device": device,
            "batch_size": spec.batch,
            "imgsz": effective_imgsz,
        }

    curves = compute_curves(spec.classes, all_records, speed=speed_info)
    meta = {
        "trainer_version": __version__,
        "device": device,
        "half": use_half,
        "end2end": use_end2end,
        "total_images": total,
        "timed_images": timed_images,
        "model_names": names_map,
    }
    output = {"curves": curves, "predictions": stored_predictions, "meta": meta}
    Path(spec.output_path).write_text(
        json.dumps(json_safe(output), ensure_ascii=False, allow_nan=False), encoding="utf-8"
    )
    return meta


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="trainMaster model evaluation worker")
    parser.add_argument("--spec", required=True, help="JSON spec file path")
    args = parser.parse_args(argv)
    try:
        spec_data = json.loads(Path(args.spec).read_text(encoding="utf-8"))
        spec = EvaluationSpec.from_json(spec_data)
        meta = run_evaluation_subprocess(spec)
        _emit({"event": "done", "ok": True, "meta": meta})
        return 0
    except Exception as exc:
        msg = _clean_message(str(exc)) or type(exc).__name__
        _emit(
            {
                "event": "done",
                "ok": False,
                "error": msg,
                "detail": traceback.format_exc()[-MAX_DETAIL:],
            }
        )
        return 1


# ---- 父进程任务执行器（TaskWorker 调用） ----


class ModelEvaluationTask:
    """执行平台下发的 model_evaluation 任务。"""

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
        progress_interval: float = PROGRESS_INTERVAL,
    ) -> None:
        self.task = dict(task)
        self.client = client
        self.node_key = node_key
        self.state_dir = state_dir
        self.node_name = node_name
        self.data_dir = data_dir
        self.command = list(command) if command else [sys.executable, "-m", CHILD_MODULE]
        self.progress_interval = progress_interval

        self._cancel = threading.Event()
        self._lock = threading.Lock()
        self._proc: subprocess.Popen[str] | None = None
        self._last_progress_at = 0.0

    def cancel(self) -> None:
        self._cancel.set()
        with self._lock:
            proc = self._proc
        if proc is not None and proc.poll() is None:
            procs.kill(proc)

    def _headers(self) -> dict[str, str]:
        return {"X-Node-Key": self.node_key}

    def _report_progress(
        self,
        stage: str,
        *,
        done: int | None = None,
        total: int | None = None,
        eta_seconds: int | None = None,
        force: bool = False,
    ) -> None:
        now = time.monotonic()
        if not force and now - self._last_progress_at < self.progress_interval:
            return
        url = self.task.get("progress_url")
        if not isinstance(url, str) or not url:
            return
        payload: dict[str, Any] = {"stage": stage}
        if done is not None:
            payload["progress_done"] = done
        if total is not None:
            payload["progress_total"] = total
        if eta_seconds is not None:
            payload["eta_seconds"] = eta_seconds
        try:
            self.client.post(url, payload, headers=self._headers())
            self._last_progress_at = now
        except ApiError as exc:
            if exc.status in (404, 409):
                raise TaskAbandoned(exc.message) from None
            log.warning("evaluation %s progress report failed: %s", self.task.get("id"), exc)
        except TransportError as exc:
            log.debug("evaluation %s progress transport error: %s", self.task.get("id"), exc)

    def _resolve_shared_local_dir(self) -> Path | None:
        if self.data_dir is None or self.task.get("source_type") != "data_version":
            return None
        dv = self.task.get("data_version")
        if not isinstance(dv, dict) or not dv.get("shared_local"):
            return None
        dir_name = dv.get("dir_name")
        if not isinstance(dir_name, str) or not dir_name:
            return None
        candidate = (self.data_dir / "exports" / Path(dir_name)).resolve()
        exports_root = (self.data_dir / "exports").resolve()
        try:
            candidate.relative_to(exports_root)
        except ValueError:
            return None
        return candidate if candidate.is_dir() else None

    def _fetch_all_images(self) -> list[dict[str, Any]]:
        images_url = str(self.task["images_url"])
        items: list[dict[str, Any]] = []
        after = 0
        while True:
            if self._cancel.is_set():
                raise TaskFailed("节点正在停止，评估任务将重新排队", retryable=True)
            sep = "&" if "?" in images_url else "?"
            page = self.client.get(
                f"{images_url}{sep}after={after}&limit={PAGE_SIZE}",
                headers=self._headers(),
                timeout=TRANSFER_TIMEOUT,
            )
            batch = page.get("items") or []
            items.extend(batch)
            if not page.get("has_more") or not batch:
                break
            after = int(page.get("next_id") or batch[-1]["id"])
        return items

    def _prepare_images(self, samples: list[dict[str, Any]], img_dir: Path) -> list[dict[str, Any]]:
        img_dir.mkdir(parents=True, exist_ok=True)
        shared_dir = self._resolve_shared_local_dir()
        manifest: list[dict[str, Any]] = []
        to_download: list[tuple[int, dict[str, Any]]] = []

        for idx, smp in enumerate(samples):
            entry: dict[str, Any] = {
                "sample_id": int(smp["id"]),
                "width": int(smp.get("width") or 0),
                "height": int(smp.get("height") or 0),
                "gt_objects": smp.get("gt_objects") or [],
                "local_path": "",
                "inference_error": "",
            }
            if shared_dir is not None and isinstance(smp.get("path"), str):
                candidate = (shared_dir / Path(smp["path"])).resolve()
                try:
                    candidate.relative_to(shared_dir)
                    if candidate.is_file():
                        entry["local_path"] = str(candidate)
                except ValueError:
                    pass
            manifest.append(entry)
            if not entry["local_path"]:
                to_download.append((idx, smp))

        if not to_download:
            return manifest

        def download_one(pair: tuple[int, dict[str, Any]]) -> tuple[int, str, str]:
            idx, smp = pair
            if self._cancel.is_set():
                return idx, "", "任务已中止"
            ext = Path(str(smp.get("path") or "img.jpg")).suffix or ".jpg"
            dest = img_dir / f"{smp['id']}{ext}"
            try:
                self.client.download(
                    str(smp["file_url"]),
                    dest,
                    headers=self._headers(),
                    timeout=TRANSFER_TIMEOUT,
                    max_bytes=MAX_IMAGE_BYTES,
                    abort=self._cancel.is_set,
                )
                return idx, str(dest), ""
            except ApiError as exc:
                if exc.status == 409:
                    raise TaskAbandoned(exc.message) from None
                return idx, "", f"下载图片失败（HTTP {exc.status}）：{exc.message}"
            except (TransportError, OSError) as exc:
                return idx, "", f"下载图片失败：{exc}"

        done_dl = len(samples) - len(to_download)
        with concurrent.futures.ThreadPoolExecutor(max_workers=PREFETCH_WORKERS) as pool:
            futures = [pool.submit(download_one, pair) for pair in to_download]
            for fut in concurrent.futures.as_completed(futures):
                if self._cancel.is_set():
                    raise TaskFailed("节点正在停止，评估任务将重新排队", retryable=True)
                idx, local_path, err_msg = fut.result()
                manifest[idx]["local_path"] = local_path
                manifest[idx]["inference_error"] = err_msg
                done_dl += 1
                self._report_progress("downloading", done=done_dl, total=len(samples))

        return manifest

    def run(self) -> str:
        eval_id = self.task.get("id")
        work_dir = self.state_dir / "work" / f"{WORK_DIR_PREFIX}{eval_id}-{secrets.token_hex(4)}"
        work_dir.mkdir(parents=True, exist_ok=True)
        try:
            return self._execute(work_dir)
        except TaskAbandoned as exc:
            log.info("evaluation %s abandoned: %s", eval_id, exc)
            return "abandoned"
        except TaskFailed as exc:
            log.warning(
                "evaluation %s failed (retryable=%s): %s", eval_id, exc.retryable, exc.message
            )
            url = fail_url_of(self.task)
            if url:
                report_failure(self.client, self.node_key, url, exc)
            return "failed"
        except ApiError as exc:
            if exc.status in (404, 409):
                log.info("evaluation %s abandoned (HTTP %s): %s", eval_id, exc.status, exc.message)
                return "abandoned"
            err = TaskFailed(
                f"平台接口错误（HTTP {exc.status}）：{exc.message}", retryable=exc.status >= 500
            )
            url = fail_url_of(self.task)
            if url:
                report_failure(self.client, self.node_key, url, err)
            return "failed"
        except TransportError as exc:
            err = TaskFailed(f"与平台通信失败：{exc}", retryable=True)
            url = fail_url_of(self.task)
            if url:
                report_failure(self.client, self.node_key, url, err)
            return "failed"
        except Exception as exc:
            err = TaskFailed(
                _clean_message(str(exc)) or f"评估任务异常：{type(exc).__name__}",
                detail=traceback.format_exc()[-MAX_DETAIL:],
                retryable=False,
            )
            url = fail_url_of(self.task)
            if url:
                report_failure(self.client, self.node_key, url, err)
            return "failed"
        finally:
            shutil.rmtree(work_dir, ignore_errors=True)

    def _execute(self, work_dir: Path) -> str:
        model_info = self.task.get("model") or {}
        options = self.task.get("options") or {}
        total_images = int(self.task.get("total_images") or 0)

        self._report_progress("downloading", done=0, total=total_images, force=True)

        # 1. 下载并校验 .pt 权重文件
        weights_path = work_dir / "model.pt"
        transfer = self.client.download(
            str(self.task["model_url"]),
            weights_path,
            headers=self._headers(),
            timeout=TRANSFER_TIMEOUT,
            abort=self._cancel.is_set,
        )
        expected_sha = str(model_info.get("sha256") or "")
        expected_size = int(model_info.get("size_bytes") or 0)
        if expected_sha and expected_size > 0:
            verify_download(transfer, expected_size, expected_sha)

        # 2. 拉取全部样本与 GT 清单，并解析本地共享文件或 HTTP 下载
        samples = self._fetch_all_images()
        manifest = self._prepare_images(samples, work_dir / "images")
        manifest_path = work_dir / "manifest.json"
        output_path = work_dir / "output.json"
        spec_path = work_dir / "spec.json"

        manifest_path.write_text(json.dumps(manifest, ensure_ascii=False), encoding="utf-8")
        spec_payload = {
            "weights": str(weights_path),
            "manifest_path": str(manifest_path),
            "output_path": str(output_path),
            "classes": self.task.get("classes") or [],
            "task": model_info.get("task"),
            "yolo_version": model_info.get("yolo_version"),
            "nms_iou": options.get("nms_iou", 0.70),
            "imgsz": options.get("imgsz"),
            "batch": options.get("batch", 16),
            "max_det": options.get("max_det", 300),
            "half": options.get("half", False),
            "device": options.get("device") or None,
            "low_conf": LOW_CONF,
            "store_min_conf": options.get("store_min_conf", 0.05),
        }
        spec_path.write_text(json.dumps(spec_payload, ensure_ascii=False), encoding="utf-8")

        # 3. 启动隔离子进程执行推理与曲线计算
        self._report_progress("inferring", done=0, total=len(samples), force=True)
        env = export_env(self.state_dir)
        env[SAFE_LOAD_ENV] = "1"
        cmd = [*self.command, "--spec", str(spec_path)]
        proc = subprocess.Popen(
            cmd,
            cwd=str(work_dir),
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
            **procs.popen_kwargs(),
        )
        procs.attach(proc)
        with self._lock:
            self._proc = proc

        stderr_chunks: list[str] = []

        def drain_stderr() -> None:
            assert proc.stderr is not None
            total_len = 0
            for line in proc.stderr:
                if total_len < MAX_STDERR:
                    stderr_chunks.append(line)
                    total_len += len(line)

        err_thread = threading.Thread(target=drain_stderr, daemon=True)
        err_thread.start()

        done_event: dict[str, Any] | None = None
        try:
            assert proc.stdout is not None
            for raw_line in proc.stdout:
                line = raw_line.strip()
                if not line:
                    continue
                try:
                    ev = json.loads(line)
                except ValueError:
                    continue
                kind = ev.get("event")
                if kind == "progress":
                    self._report_progress(
                        str(ev.get("stage") or "inferring"),
                        done=ev.get("done"),
                        total=ev.get("total"),
                        eta_seconds=ev.get("eta_seconds"),
                    )
                elif kind == "done":
                    done_event = ev
        finally:
            if self._cancel.is_set() and proc.poll() is None:
                procs.kill(proc)
            rc = proc.wait()
            err_thread.join(timeout=5.0)
            if proc.stdout is not None:
                proc.stdout.close()
            if proc.stderr is not None:
                proc.stderr.close()
            with self._lock:
                self._proc = None

        if self._cancel.is_set():
            raise TaskFailed("节点正在停止，评估任务将重新排队", retryable=True)

        stderr_text = "".join(stderr_chunks)[-MAX_STDERR:]
        if rc != 0 or not done_event or not done_event.get("ok"):
            err_msg = (
                str(done_event.get("error"))
                if done_event and done_event.get("error")
                else f"评估子进程异常退出（退出码 {rc}）"
            )
            detail = _join_detail(
                done_event.get("detail") if done_event else None,
                stderr_text or None,
            )
            raise TaskFailed(err_msg[:MAX_ERROR], detail=detail, retryable=False)

        # 4. 读取输出并 gzip 上传给平台，再触发完成
        if not output_path.is_file():
            raise TaskFailed("评估子进程未生成结果文件", detail=stderr_text)
        output = json.loads(output_path.read_text(encoding="utf-8"))

        self._report_progress("uploading", done=len(samples), total=len(samples), force=True)
        self.client.post_gzip(
            str(self.task["results_url"]),
            {
                "curves": output.get("curves") or {},
                "predictions": output.get("predictions") or [],
            },
            headers=self._headers(),
            timeout=max(TRANSFER_TIMEOUT, 120.0),
        )
        self.client.post(
            str(self.task["complete_url"]),
            {"result": output.get("meta") or {}},
            headers=self._headers(),
            timeout=max(TRANSFER_TIMEOUT, 120.0),
        )
        return "succeeded"


if __name__ == "__main__":
    raise SystemExit(main())
