"""审图任务的模型识别（review_inference）：用模型仓库中的 pt 识别审图任务的每张图片，把检测框上报给平台。

与 model_export.py 一样分两部分：
- 子进程（python -m trainmaster_trainer.review_inference --spec <json>）：导入 torch / ultralytics，用 Ultralytics
  的安全加载读取模型，有 CUDA 时在 cuda:0 上、否则在 CPU 上逐张识别。与父进程之间是 JSON lines 协议：
  启动（加载模型并预热一次）后输出一行 {"ready": true, "device", "names", "versions", …}，失败时输出
  {"ok": false, "error", "detail"} 并退出；之后父进程每张图片写一行 {"id": N, "path": "/abs/file"}，子进程
  回答一行 {"id": N, "ok": true, "width": W, "height": H, "boxes": [[x1, y1, x2, y2, conf, cls], …]}，或
  {"id": N, "ok": false, "error": "无法读取图片（…）"}（只是这张图片失败）；继续识别没有意义的错误（显存
  不足、显卡出错）输出没有 id 的 {"ok": false, "error", "detail"} 后退出；读到空行或 EOF 时退出。
  图片按存储的像素方向读取（忽略 EXIF 方向），坐标与平台显示、标注的图片一致。torch / ultralytics /
  OpenCV 只在这个子进程中导入。pt 来自用户上传：与模型转换一样，子进程只继承运行 Python 需要的环境变量、
  只用安全加载读取模型，但显卡可见。
- 父进程：InferenceProcess 管理子进程（单张图片超时、可从其它线程取消，结束整个进程组）；
  ReviewInferenceTask 在任务线程中执行平台下发的识别任务：工作目录 → 检查磁盘 → 下载并校验 pt → 启动子进程
  → 按页取还没有识别的图片，预先下载几张（小线程池），依次交给子进程，结果攒批上报 → 平台不再列出图片时
  完成；期间定期上报进度（续租）。平台上的任务被删除或已不属于本节点（404 / 409）时结束子进程并放弃，
  不再上报。单张图片下载不到（404）时上报这张图片识别失败，其余照常。
"""

from __future__ import annotations

import argparse
import concurrent.futures
import contextlib
import io
import json
import logging
import math
import os
import queue
import re
import shutil
import signal
import struct
import subprocess
import sys
import threading
import time
import traceback
import unicodedata
from collections import deque
from collections.abc import Callable, Iterator, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import IO, Any, TypeVar

from . import __version__
from .client import ApiError, Client, TransportError, json_safe
from .environment import _kill, summarize_exception
from .model_export import (
    _SHA256,
    FAIL_RETRY_DELAYS,
    MAX_DETAIL,
    MAX_ERROR,
    NMS_TASKS,
    PROGRESS_INTERVAL,
    SAFE_LOAD_ENV,
    STOPPING_FAIL_TIMEOUT,
    TASK_LABELS,
    TRANSFER_TIMEOUT,
    ExportError,
    ExportSpec,
    TaskAbandoned,
    TaskFailed,
    _clean_message,
    _is_out_of_memory,
    _join_detail,
    export_env,
    fail_url_of,
    format_bytes,
    report_failure,
    require_safe_load,
    translate_load_error,
    verify_download,
)

log = logging.getLogger(__name__)

CHILD_MODULE = "trainmaster_trainer.review_inference"
KIND = "review_inference"
WORK_DIR_PREFIX = "review-inference-"
STAGES = ("downloading", "inferring")

PAGE_SIZE = 200  # 每次取的图片数（平台允许 1–500）
PREFETCH_WORKERS = 4  # 同时下载的图片数
PREFETCH_AHEAD = 8  # 最多预先下载（含正在下载）的图片数：工作目录中同时只有这么多张图片
# 交给子进程、还没有回答的图片数：父进程上报结果、等待下载时子进程手上还有一张，不必空等。
PIPELINE_DEPTH = 2
# 结果攒批上报：满 32 张、JSON 满 512 KB 或第一条结果等了 5 秒时上报；一次请求不超过平台的上限
# （请求体 1 MiB，最多 500 条结果），留出余量。
FLUSH_COUNT = 32
FLUSH_BYTES = 512 * 1024
FLUSH_INTERVAL = 5.0
MAX_BODY_BYTES = (1 << 20) - 4096
MAX_BOXES = 1000  # 平台每张图片最多接收的框数
MAX_NAME = 255  # 平台的类别名称上限（字符）
MAX_IMAGE_BYTES = 1 << 30  # 单张图片的上限：更大的文件不下载，上报这张图片识别失败
IMAGE_DISK_RESERVE = 256 << 20  # 工作目录中除模型外为预先下载的图片留出的空间
MAX_PASSES = 3  # 每轮之后平台仍列出未识别的图片时再来一轮；连续这么多轮都有图片时放弃
CLOSE_TIMEOUT = 10.0  # 识别完成后等待子进程退出的时间
# 启动子进程（导入 PyTorch、初始化显卡、加载模型、预热）至少可以用这么久：通常比识别一张图片慢得多
MIN_STARTUP_TIMEOUT = 120.0
MAX_STDERR = 64 * 1024  # 保留子进程 stderr 的最后这么多字节（失败时作为详情）
_T = TypeVar("_T")


class InferenceError(Exception):
    """识别失败，message 为直接展示给用户的中文说明。父进程中 timeout / cancelled 表示子进程超时、被取消。"""

    def __init__(
        self,
        message: str,
        detail: str | None = None,
        *,
        timeout: bool = False,
        cancelled: bool = False,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.detail = detail
        self.timeout = timeout
        self.cancelled = cancelled


def _number(value: Any) -> float | None:
    """有限的数（bool 除外），否则为 None。"""
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
        return None
    return float(value)


def _positive_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and value > 0


def parse_imgsz(value: Any) -> tuple[int, int] | None:
    """输入尺寸：正整数（正方形）或 [高, 宽]；None 表示模型训练时的尺寸。不合法时抛出 ValueError。"""
    if value is None:
        return None
    if _positive_int(value):
        return (value, value)
    if isinstance(value, (list, tuple)) and len(value) == 2 and all(map(_positive_int, value)):
        return (value[0], value[1])
    raise ValueError(f"imgsz 应为正整数或 [高, 宽]：{value!r}")


def size_text(imgsz: Sequence[int] | None) -> str:
    return f"{imgsz[0]}×{imgsz[1]}" if imgsz else "默认"


@dataclass(frozen=True)
class InferenceSpec:
    """子进程的参数（--spec）。imgsz 为 None 时用模型训练时的尺寸；device 为 None 时自动选择
    （有 CUDA 时 cuda:0，否则 cpu）。"""

    weights: str
    task: str | None = None
    yolo_version: str | None = None
    conf: float = 0.25
    iou: float = 0.7
    imgsz: tuple[int, int] | None = None
    max_det: int = 300
    device: str | None = None

    @classmethod
    def from_json(cls, data: Any) -> InferenceSpec:
        """校验 --spec 的内容；不合法时抛出 ValueError（中文）。"""
        if not isinstance(data, dict):
            raise ValueError("应为 JSON 对象")
        weights = data.get("weights")
        if not isinstance(weights, str) or not weights:
            raise ValueError("缺少 weights（pt 文件路径）")

        def opt_str(key: str) -> str | None:
            value = data.get(key)
            if value is None or value == "":
                return None
            if not isinstance(value, str):
                raise ValueError(f"{key} 应为字符串")
            return value

        def fraction(key: str, default: float) -> float:
            value = _number(data.get(key, default))
            if value is None or not 0 < value <= 1:
                raise ValueError(f"{key} 应为大于 0、不超过 1 的数：{data.get(key)!r}")
            return value

        max_det = data.get("max_det", 300)
        if not _positive_int(max_det) or max_det > MAX_BOXES:
            raise ValueError(f"max_det 应为 1–{MAX_BOXES} 的整数：{max_det!r}")
        device = opt_str("device")
        if device is not None and (len(device) > 32 or not device.isprintable()):
            raise ValueError(f"device 不合法：{device!r}")
        return cls(
            weights=weights,
            task=opt_str("task"),
            yolo_version=opt_str("yolo_version"),
            conf=fraction("conf", 0.25),
            iou=fraction("iou", 0.7),
            imgsz=parse_imgsz(data.get("imgsz")),
            max_det=max_det,
            device=device,
        )

    def to_json(self) -> dict[str, Any]:
        return {
            "weights": self.weights,
            "task": self.task,
            "yolo_version": self.yolo_version,
            "conf": self.conf,
            "iou": self.iou,
            "imgsz": list(self.imgsz) if self.imgsz else None,
            "max_det": self.max_det,
            "device": self.device,
        }


# ---------------------------------------------------------------------------
# 子进程


class ImageReadError(Exception):
    """图片无法读取，message 为原因（中文），结果中写为“无法读取图片（原因）”。"""

    def __init__(self, message: str) -> None:
        super().__init__(message)
        self.message = message


# Pillow 原本的 Image.open：Ultralytics 导入时把它换成在 UnidentifiedImageError 时安装并注册 pi-heif 的版本
# （ultralytics.utils.patches），离线时损坏的文件因此报“No module named 'pi_heif'”，允许自动安装时还会
# 运行 pip。Session.load 在导入 ultralytics 之前记下原来的函数，读取图片时用它。
_pillow_open: Callable[..., Any] | None = None


def remember_pillow_open() -> None:
    """在导入 ultralytics 之前调用：记下 Pillow 原本的 Image.open（没有安装 Pillow 时什么也不做）。"""
    global _pillow_open
    if _pillow_open is None:
        with contextlib.suppress(Exception):
            from PIL import Image

            _pillow_open = Image.open


def neutralize_tiff_orientation(data: bytes) -> bytes | None:
    """TIFF（含 BigTIFF）第一个 IFD 中的 Orientation 标签（274）不是 1 时，返回把它改为 1 的副本，否则返回
    None。OpenCV 与 Pillow 读取 TIFF 时都按这个标签旋转 / 翻转像素（IMREAD_IGNORE_ORIENTATION 只管 EXIF，
    Pillow 加载 TIFF 时自动 exif_transpose），而平台按存储的像素显示与标注（Go 的 x/image/tiff 不处理方向），
    所以识别前去掉它。文件结构不完整时返回 None（交给解码器报错）。"""
    if len(data) < 16:
        return None
    endian = {b"II": "<", b"MM": ">"}.get(data[:2])
    if endian is None:
        return None
    magic = struct.unpack_from(endian + "H", data, 2)[0]
    if magic == 42:  # 经典 TIFF：12 字节的条目，值在第 8 个字节起
        ifd = struct.unpack_from(endian + "I", data, 4)[0]
        count_format, count_size, entry_size, value_at = "H", 2, 12, 8
    elif magic == 43:  # BigTIFF：20 字节的条目，值在第 12 个字节起
        ifd = struct.unpack_from(endian + "Q", data, 8)[0]
        count_format, count_size, entry_size, value_at = "Q", 8, 20, 12
    else:
        return None
    if ifd + count_size > len(data):
        return None
    count = struct.unpack_from(endian + count_format, data, ifd)[0]
    for i in range(min(count, 4096)):
        entry = ifd + count_size + i * entry_size
        if entry + entry_size > len(data):
            return None
        tag, kind = struct.unpack_from(endian + "HH", data, entry)
        if tag != 274:
            continue
        if kind != 3:  # SHORT
            return None
        if struct.unpack_from(endian + "H", data, entry + value_at)[0] == 1:
            return None
        patched = bytearray(data)
        struct.pack_into(endian + "H", patched, entry + value_at, 1)
        return bytes(patched)
    return None


def read_image(path: str) -> Any:
    """按存储的像素方向把图片读为 BGR 数组（高 × 宽 × 3，uint8），坐标与平台显示、标注的图片一致：先用
    OpenCV 解码（IMREAD_IGNORE_ORIENTATION：不按 JPEG 等的 EXIF 方向旋转；TIFF 先去掉 Orientation 标签，
    见 neutralize_tiff_orientation）；OpenCV 读不了的格式（GIF、部分 AVIF 等）用 Pillow 读取第一帧（同样
    不做 exif_transpose）。"""
    import cv2
    import numpy as np

    try:
        with open(path, "rb") as f:
            data = f.read()
    except FileNotFoundError:
        raise ImageReadError("文件不存在") from None
    except (OSError, ValueError) as exc:
        raise ImageReadError(f"无法打开文件：{_clean_message(exc)}") from None
    if not data:
        raise ImageReadError("文件是空的")
    data = neutralize_tiff_orientation(data) or data
    try:
        image = cv2.imdecode(
            np.frombuffer(data, dtype=np.uint8), cv2.IMREAD_COLOR | cv2.IMREAD_IGNORE_ORIENTATION
        )
    except Exception as exc:  # cv2.error：尺寸过大等；交给 Pillow 再试
        print(f"OpenCV cannot decode {path}: {_clean_message(exc)}", file=sys.stderr)
        image = None
    if image is not None:
        return image
    return _read_with_pillow(data, cv2, np)


def _read_with_pillow(data: bytes, cv2: Any, np: Any) -> Any:
    try:
        from PIL import Image, UnidentifiedImageError
    except ImportError:
        raise ImageReadError("不是 OpenCV 能读取的格式，训练节点也没有安装 Pillow") from None
    opener = _pillow_open or Image.open
    try:
        with opener(io.BytesIO(data)) as img:
            rgb = img.convert("RGB")  # 第一帧；调色板、灰度、透明通道都转为 RGB
    except UnidentifiedImageError:
        raise ImageReadError("文件已损坏，或不是支持的图片格式") from None
    except Exception as exc:  # 截断的文件、超过 Pillow 的像素上限等
        raise ImageReadError(f"文件已损坏或不完整：{_clean_message(exc)}") from None
    return cv2.cvtColor(np.asarray(rgb), cv2.COLOR_RGB2BGR)


def _clip(value: float, limit: int) -> float:
    return min(max(value, 0.0), float(limit))


def extract_boxes(result: Any, width: int, height: int) -> list[list[float | int]]:
    """一张图片的识别结果 → [[x1, y1, x2, y2, conf, cls], …]（裁剪到图片范围，坐标保留 2 位小数、
    置信度 4 位）。检测 / 分割 / 姿态用 result.boxes；旋转框用 result.obb.xyxy（外接的水平框）。"""
    data = getattr(result, "obb", None)
    if data is None:
        data = getattr(result, "boxes", None)
    if data is None:
        return []
    boxes: list[list[float | int]] = []
    for (x1, y1, x2, y2), conf, cls in zip(
        data.xyxy.tolist(), data.conf.tolist(), data.cls.tolist(), strict=False
    ):
        boxes.append(
            [
                round(_clip(x1, width), 2),
                round(_clip(y1, height), 2),
                round(_clip(x2, width), 2),
                round(_clip(y2, height), 2),
                round(float(conf), 4),
                int(cls),
            ]
        )
    return boxes


def _is_gpu_failure(exc: BaseException) -> bool:
    """显卡出错（CUDA 上下文已损坏，之后的识别都会失败）。"""
    text = str(exc)
    return any(s in text for s in ("CUDA error", "CUBLAS_STATUS", "cuDNN error", "CUDNN_STATUS"))


def translate_predict_error(
    exc: BaseException, device: str, imgsz: Sequence[int] | None, *, warmup: bool = False
) -> tuple[str, bool]:
    """识别时的异常 → (中文说明, 是否继续识别没有意义)。内存不足与显卡出错对每张图片都一样。"""
    gpu = device.startswith("cuda")
    if _is_out_of_memory(exc):
        what = "显存" if gpu else "内存"
        hint = "，或等显卡空闲后再试" if gpu else ""
        return (
            f"训练节点的{what}不足，无法用该模型识别（输入尺寸 {size_text(imgsz)}）。"
            f"请减小输入尺寸后重新识别{hint}",
            True,
        )
    if gpu and _is_gpu_failure(exc):
        return (
            f"显卡出错，无法继续识别（{_clean_message(exc)}）。请在“计算资源”页面检查该训练节点的显卡与驱动",
            True,
        )
    if warmup:
        return f"模型无法在 {device} 上识别：{_clean_message(exc)}", True
    return f"识别失败：{_clean_message(exc)}", False


def pick_device(torch: Any) -> str:
    try:
        return "cuda:0" if torch is not None and torch.cuda.is_available() else "cpu"
    except Exception:  # 驱动问题等：退回 CPU（环境检测会报告显卡的问题）
        return "cpu"


def _version(module: Any) -> str | None:
    return str(getattr(module, "__version__", "")) or None


class Session:
    """子进程中加载好的模型。load() 失败时抛出 InferenceError。"""

    def __init__(
        self,
        *,
        model: Any,
        task: str,
        device: str,
        device_name: str | None,
        names: dict[int, str],
        versions: dict[str, str | None],
        kwargs: dict[str, Any],
        imgsz: tuple[int, int] | None,
        warmup_ms: int,
    ) -> None:
        self.model = model
        self.task = task
        self.device = device
        self.device_name = device_name
        self.names = names
        self.versions = versions
        self.kwargs = kwargs
        self.imgsz = imgsz
        self.warmup_ms = warmup_ms

    @classmethod
    def load(cls, spec: InferenceSpec) -> Session:
        if not Path(spec.weights).is_file():
            raise InferenceError(f"找不到模型文件：{spec.weights}")
        remember_pillow_open()
        try:
            import ultralytics
        except Exception as exc:  # ImportError、缺少 .so 时的 OSError 等
            raise InferenceError(
                f"训练节点无法导入 Ultralytics（{_clean_message(exc)}），"
                "请用 python -m trainmaster_trainer selftest 检查训练节点的环境",
                summarize_exception(exc, MAX_DETAIL),
            ) from exc
        try:
            import cv2
            import numpy as np
        except Exception as exc:
            raise InferenceError(
                f"训练节点无法导入 OpenCV（{_clean_message(exc)}），请升级训练节点"
                "（镜像或 requirements.txt 中的 ultralytics 及其依赖 opencv-python）",
                summarize_exception(exc, MAX_DETAIL),
            ) from exc
        node_version = _version(ultralytics) or "unknown"
        torch = sys.modules.get("torch")
        try:
            require_safe_load(node_version)
        except ExportError as exc:
            raise InferenceError(exc.message, exc.detail) from exc

        try:
            model = ultralytics.YOLO(spec.weights, task=spec.task)
        except Exception as exc:
            load_spec = ExportSpec(weights=spec.weights, yolo_version=spec.yolo_version)
            raise InferenceError(
                translate_load_error(exc, load_spec, node_version),
                summarize_exception(exc, MAX_DETAIL),
            ) from exc
        task = str(getattr(model, "task", "") or spec.task or "")
        if task not in NMS_TASKS:
            label = TASK_LABELS.get(task, task) or "该"
            raise InferenceError(f"{label}模型没有检测框，不能用于审图")

        device = spec.device or pick_device(torch)
        device_name = None
        if device.startswith("cuda"):
            with contextlib.suppress(Exception):
                index = int(device.partition(":")[2] or 0)
                device_name = str(torch.cuda.get_device_name(index))  # type: ignore[union-attr]
        raw_names = getattr(model, "names", None) or {}
        if isinstance(raw_names, (list, tuple)):
            raw_names = dict(enumerate(raw_names))
        names = {}
        for key, value in dict(raw_names).items():
            with contextlib.suppress(TypeError, ValueError):
                names[int(key)] = str(value)
        kwargs: dict[str, Any] = {
            "conf": spec.conf,
            "iou": spec.iou,
            "max_det": spec.max_det,
            "device": device,
            "verbose": False,
        }
        if spec.imgsz:
            kwargs["imgsz"] = list(spec.imgsz)
        print(f"predict: {json.dumps(kwargs)} (task={task})", file=sys.stderr)

        # 预热：在启动时暴露设备、显存与模型本身的问题，而不是在每张图片上失败
        height, width = spec.imgsz or (640, 640)
        started = time.monotonic()
        try:
            model.predict(source=np.zeros((height, width, 3), dtype=np.uint8), **kwargs)
        except Exception as exc:
            message, _ = translate_predict_error(exc, device, spec.imgsz, warmup=True)
            raise InferenceError(message, summarize_exception(exc, MAX_DETAIL)) from exc
        versions = {
            "ultralytics": node_version,
            "torch": _version(torch),
            "opencv": _version(cv2),
            "numpy": _version(np),
        }
        return cls(
            model=model,
            task=task,
            device=device,
            device_name=device_name,
            names=names,
            versions=versions,
            kwargs=kwargs,
            imgsz=spec.imgsz,
            warmup_ms=int((time.monotonic() - started) * 1000),
        )

    def ready(self) -> dict[str, Any]:
        return {
            "ready": True,
            "device": self.device,
            "device_name": self.device_name,
            "task": self.task,
            "names": {str(k): v for k, v in sorted(self.names.items())},
            "versions": self.versions,
            "imgsz": list(self.imgsz) if self.imgsz else None,
            "warmup_ms": self.warmup_ms,
        }

    def infer(self, image_id: int, path: str) -> dict[str, Any]:
        """识别一张图片。只影响这张图片的错误作为 ok: false 的回答；继续识别没有意义的错误抛出 InferenceError。"""
        try:
            image = read_image(path)
        except ImageReadError as exc:
            return {"id": image_id, "ok": False, "error": f"无法读取图片（{exc.message}）"}
        height, width = int(image.shape[0]), int(image.shape[1])
        try:
            results = self.model.predict(source=image, **self.kwargs)
        except Exception as exc:
            message, fatal = translate_predict_error(exc, self.device, self.imgsz)
            if fatal:
                raise InferenceError(message, summarize_exception(exc, MAX_DETAIL)) from exc
            traceback.print_exc(file=sys.stderr)
            return {"id": image_id, "ok": False, "error": message}
        boxes = extract_boxes(results[0], width, height) if results else []
        return {"id": image_id, "ok": True, "width": width, "height": height, "boxes": boxes}


def parse_request(line: bytes | str) -> tuple[int, str]:
    """父进程的一行请求 {"id": N, "path": "/abs/file"}；不合法时抛出 ValueError。"""
    data = json.loads(line)
    if not isinstance(data, dict) or not _positive_int(data.get("id")):
        raise ValueError("缺少 id")
    path = data.get("path")
    if not isinstance(path, str) or not path:
        raise ValueError("缺少 path")
    return data["id"], path


def serve(spec_text: str, stdin: IO[bytes], out: IO[str]) -> int:
    """子进程的主循环（见模块说明中的协议），返回退出码。"""

    def emit(message: Mapping[str, Any]) -> None:
        out.write(json.dumps(json_safe(dict(message)), ensure_ascii=False) + "\n")
        out.flush()

    def failed(error: str, detail: str | None = None) -> int:
        emit({"ok": False, "error": error, "detail": detail})
        return 1

    try:
        spec = InferenceSpec.from_json(json.loads(spec_text))
    except ValueError as exc:
        return failed(f"识别参数不合法：{exc}")
    try:
        session = Session.load(spec)
    except InferenceError as exc:
        return failed(exc.message, exc.detail)
    except Exception as exc:  # 防御：未预料的错误也给出说明
        return failed(f"识别出错：{_clean_message(exc)}", summarize_exception(exc, MAX_DETAIL))
    emit(session.ready())
    while True:
        line = stdin.readline()
        if not line.strip():
            return 0
        try:
            image_id, path = parse_request(line)
        except ValueError as exc:
            return failed(f"识别请求不合法：{exc}")
        try:
            emit(session.infer(image_id, path))
        except InferenceError as exc:
            return failed(exc.message, exc.detail)
        except Exception as exc:
            return failed(f"识别出错：{_clean_message(exc)}", summarize_exception(exc, MAX_DETAIL))


def child_main(argv: Sequence[str] | None = None) -> int:
    """子进程入口：协议的 JSON 写到 stdout，其它输出（ultralytics 的日志、C 扩展的打印）一律转到 stderr。"""
    parser = argparse.ArgumentParser(prog=f"python -m {CHILD_MODULE}")
    parser.add_argument("--spec", required=True, help="识别参数（JSON）")
    args = parser.parse_args(argv)
    os.environ.setdefault("YOLO_OFFLINE", "1")
    os.environ.setdefault("YOLO_AUTOINSTALL", "False")
    os.environ[SAFE_LOAD_ENV] = "1"  # 在导入 ultralytics 之前：它在导入时读取这个开关
    logging.basicConfig(stream=sys.stderr, level=logging.WARNING)

    sys.stdout.flush()
    json_out = os.fdopen(os.dup(1), "w", encoding="utf-8")
    os.dup2(2, 1)  # 文件描述符级别的重定向：C 扩展直接写 fd 1 的内容也不会混入协议
    with contextlib.redirect_stdout(sys.stderr):
        return serve(args.spec, sys.stdin.buffer, json_out)


# ---------------------------------------------------------------------------
# 父进程：运行子进程


def inference_env(state_dir: Path, base: Mapping[str, str] | None = None) -> dict[str, str]:
    """识别子进程的环境变量：与转换子进程相同（export_env：只继承运行 Python 需要的变量，没有 TM_NODE_TOKEN
    等节点配置与密钥；YOLO_OFFLINE、YOLO_AUTOINSTALL=False、ULTRALYTICS_SAFE_LOAD=1），但不隐藏显卡：
    保留节点自己的 CUDA_VISIBLE_DEVICES（裸机节点在 trainer.env 中用它选择显卡），没有时不设置。"""
    source = os.environ if base is None else base
    env = export_env(state_dir, source)
    env.pop("CUDA_VISIBLE_DEVICES", None)
    if "CUDA_VISIBLE_DEVICES" in source:
        env["CUDA_VISIBLE_DEVICES"] = source["CUDA_VISIBLE_DEVICES"]
    return env


class _Tail:
    """子进程 stderr 的最后 limit 个字节（读取线程写入）。"""

    def __init__(self, limit: int = MAX_STDERR) -> None:
        self.limit = limit
        self._data = bytearray()
        self._lock = threading.Lock()

    def add(self, chunk: bytes) -> None:
        with self._lock:
            self._data += chunk
            if len(self._data) > 2 * self.limit:
                del self._data[: -self.limit]

    def text(self, limit: int = MAX_DETAIL) -> str | None:
        with self._lock:
            data = bytes(self._data[-self.limit :])
        text = data.decode("utf-8", "replace").strip()
        if not text:
            return None
        return text if len(text) <= limit else "…" + text[-(limit - 1) :]


class InferenceProcess:
    """一个识别子进程（见模块说明中的 JSON lines 协议）。start() 等到模型加载完成（最多 startup_timeout 秒，
    默认为 image_timeout 与 MIN_STARTUP_TIMEOUT 中较大的）；send() 交给它一张图片，next_reply() 按顺序取
    回答。单张图片超过 image_timeout 没有回答时结束子进程（整个进程组）。cancel() 可从其它线程调用。
    所有失败都抛出 InferenceError（中文说明，stderr 的尾部作为详情）。"""

    def __init__(
        self,
        *,
        state_dir: Path,
        image_timeout: float,
        command: Sequence[str] | None = None,
        startup_timeout: float | None = None,
    ) -> None:
        self.state_dir = state_dir
        self.image_timeout = image_timeout
        self.startup_timeout = (
            startup_timeout
            if startup_timeout is not None
            else max(image_timeout, MIN_STARTUP_TIMEOUT)
        )
        self.command = list(command) if command else [sys.executable, "-m", CHILD_MODULE]
        self.ready: dict[str, Any] | None = None
        self._lock = threading.Lock()
        self._proc: subprocess.Popen[bytes] | None = None
        self._cancelled = False
        self._messages: queue.Queue[tuple[str, Any, float]] = queue.Queue()
        self._pending: deque[tuple[int, float]] = deque()  # (图片 ID, 发出的时间)
        self._last_reply = 0.0
        self._stderr = _Tail()
        self._threads: list[threading.Thread] = []

    @property
    def pending(self) -> int:
        return len(self._pending)

    def stderr_tail(self, limit: int = MAX_DETAIL) -> str | None:
        return self._stderr.text(limit)

    def cancel(self) -> None:
        with self._lock:
            self._cancelled = True
            proc = self._proc
        if proc is not None:
            _kill(proc)

    def start(self, spec: InferenceSpec, *, cwd: Path | None = None) -> dict[str, Any]:
        """启动子进程，返回它的 ready 消息（device、names、versions …）。"""
        try:
            (self.state_dir / "ultralytics").mkdir(parents=True, exist_ok=True)
        except OSError as exc:
            log.debug("cannot create ultralytics config dir: %s", exc)
        with self._lock:
            if self._cancelled:
                raise InferenceError("识别已取消", cancelled=True)
            try:
                self._proc = subprocess.Popen(
                    [*self.command, "--spec", json.dumps(spec.to_json(), ensure_ascii=False)],
                    stdin=subprocess.PIPE,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    env=inference_env(self.state_dir),
                    cwd=cwd,
                    start_new_session=True,  # 超时或停止时连同它启动的进程一起结束
                )
            except OSError as exc:
                raise InferenceError(f"无法启动识别进程：{exc}") from None
            proc = self._proc
        assert proc.stdout is not None and proc.stderr is not None
        for target, stream, name in (
            (self._read_stdout, proc.stdout, "stdout"),
            (self._read_stderr, proc.stderr, "stderr"),
        ):
            thread = threading.Thread(
                target=target, args=(stream,), name=f"inference-{name}", daemon=True
            )
            thread.start()
            self._threads.append(thread)

        deadline = time.monotonic() + self.startup_timeout
        item = self._receive(deadline)
        if item is None:
            raise self._timed_out(
                f"加载模型超时（{self.startup_timeout:g} 秒内没有完成）。"
                "可以在训练节点上用 TM_INFERENCE_IMAGE_TIMEOUT 延长时间"
            )
        message, at = self._message(item)
        if message.get("ready") is not True:
            if message.get("ok") is False:
                raise self._fatal(message)
            self._stop_now()
            raise InferenceError(
                "识别进程的输出无法解析（没有 ready）",
                _join_detail(
                    f"stdout: {json.dumps(message, ensure_ascii=False)[:500]}", self.stderr_tail()
                ),
            )
        self._last_reply = at
        self.ready = message
        return message

    def send(self, image_id: int, path: str) -> None:
        """交给子进程一张图片（不等待回答）。子进程已经退出时不报错：next_reply() 给出原因。"""
        proc = self._proc
        assert proc is not None and proc.stdin is not None
        line = json.dumps({"id": image_id, "path": path}, ensure_ascii=False) + "\n"
        self._pending.append((image_id, time.monotonic()))
        try:
            proc.stdin.write(line.encode("utf-8"))
            proc.stdin.flush()
        except (OSError, ValueError) as exc:  # BrokenPipeError：子进程已经退出（原因在它的输出中）
            log.debug("cannot write to the inference process: %s", exc)
            with contextlib.suppress(OSError, ValueError):
                proc.stdin.close()  # 丢掉写不出去的缓冲（否则回收时还会再报 BrokenPipeError）

    def next_reply(self, wait: float) -> dict[str, Any] | None:
        """最早发出的那张图片的回答；wait 秒内没有回答时返回 None（调用方借机上报结果、检查停止）。
        这张图片从子进程开始处理它（发出时、或上一张的回答到达时）起超过 image_timeout 时结束子进程。"""
        if not self._pending:
            raise RuntimeError("no image is pending")
        image_id, sent = self._pending[0]
        deadline = max(sent, self._last_reply) + self.image_timeout
        item = self._receive(min(deadline, time.monotonic() + max(wait, 0.0)))
        if item is None:
            if time.monotonic() < deadline:
                return None
            raise self._timed_out(
                f"识别超时（单张图片 {self.image_timeout:g} 秒内没有识别完成）。"
                "可以减小输入尺寸，或在训练节点上用 TM_INFERENCE_IMAGE_TIMEOUT 延长时间"
            )
        message, at = self._message(item)
        if "id" not in message:
            raise self._fatal(message)
        if message.get("id") != image_id:
            self._stop_now()
            raise InferenceError(
                f"识别进程的回答与请求不对应（收到 {message.get('id')!r} 的结果，应为 {image_id}）",
                self.stderr_tail(),
            )
        self._pending.popleft()
        self._last_reply = at
        return message

    def close(self, *, kill: bool = False) -> None:
        """结束子进程：kill=False 时写空行让它自行退出（最多等 CLOSE_TIMEOUT 秒），否则立即结束进程组。"""
        proc = self._proc
        if proc is None:
            return
        if not kill and proc.poll() is None:
            try:
                assert proc.stdin is not None
                proc.stdin.write(b"\n")
                proc.stdin.close()
                proc.wait(timeout=CLOSE_TIMEOUT)
            except (OSError, ValueError, subprocess.TimeoutExpired) as exc:
                log.debug("inference process did not exit by itself: %s", exc)
        _kill(proc)
        with contextlib.suppress(OSError, ValueError):
            if proc.stdin is not None:
                proc.stdin.close()
        for thread in self._threads:
            thread.join(timeout=2)

    # ---- 内部 ----

    def _read_stdout(self, stream: IO[bytes]) -> None:
        try:
            for raw in iter(stream.readline, b""):
                text = raw.decode("utf-8", "replace").strip()
                if not text:
                    continue
                try:
                    data = json.loads(text)
                except ValueError:
                    data = None
                if isinstance(data, dict):
                    self._messages.put(("message", data, time.monotonic()))
                else:
                    self._messages.put(("invalid", text[:500], time.monotonic()))
        except (OSError, ValueError):
            pass
        finally:
            self._messages.put(("eof", None, time.monotonic()))

    def _read_stderr(self, stream: IO[bytes]) -> None:
        reader = getattr(stream, "read1", stream.read)
        with contextlib.suppress(OSError, ValueError):
            while chunk := reader(65536):
                self._stderr.add(chunk)

    def _receive(self, until: float) -> tuple[str, Any, float] | None:
        try:
            return self._messages.get(timeout=max(0.0, until - time.monotonic()))
        except queue.Empty:
            return None

    def _message(self, item: tuple[str, Any, float]) -> tuple[dict[str, Any], float]:
        kind, payload, at = item
        if kind == "eof":
            raise self._exited()
        if kind == "invalid":
            self._stop_now()
            raise InferenceError(
                "识别进程的输出无法解析", _join_detail(f"stdout: {payload}", self.stderr_tail())
            )
        return payload, at

    def _wait_exit(self) -> int | None:
        """等子进程退出（最多 5 秒）并读完它的 stderr；返回退出码（仍未退出时结束它，返回 None）。"""
        proc = self._proc
        assert proc is not None
        try:
            code = proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            _kill(proc)
            code = None
        for thread in self._threads:
            if thread.name == "inference-stderr":
                thread.join(timeout=2)
        return code

    def _stop_now(self) -> None:
        if self._proc is not None:
            _kill(self._proc)
        self._wait_exit()

    def _fatal(self, message: Mapping[str, Any]) -> InferenceError:
        """子进程报告了无法继续的错误（随后自行退出）。"""
        self._wait_exit()
        error = message.get("error")
        detail = message.get("detail")
        return InferenceError(
            error if isinstance(error, str) and error.strip() else "识别失败（没有说明）",
            _join_detail(detail if isinstance(detail, str) else None, self.stderr_tail()),
        )

    def _exited(self) -> InferenceError:
        """stdout 已关闭：子进程退出了（被取消、崩溃，或没有说明就退出）。"""
        code = self._wait_exit()
        stderr = self.stderr_tail()
        if self._cancelled:
            return InferenceError("识别已取消", stderr, cancelled=True)
        if code is not None and code < 0:
            try:
                name = signal.Signals(-code).name
            except ValueError:
                name = str(-code)
            return InferenceError(f"识别进程被信号 {name} 终止（可能是内存不足）", stderr)
        return InferenceError(f"识别进程意外退出（退出码 {code}）", stderr)

    def _timed_out(self, message: str) -> InferenceError:
        if self._proc is not None:
            _kill(self._proc)
        self._wait_exit()
        if self._cancelled:
            return InferenceError("识别已取消", self.stderr_tail(), cancelled=True)
        return InferenceError(message, self.stderr_tail(), timeout=True)


# ---------------------------------------------------------------------------
# 结果：子进程的回答 → 上报给平台的结果


def clean_class_name(raw: Any, class_id: int) -> str:
    """类别名称：去掉控制字符与首尾空白，最多 255 个字符（与平台的规则相同）；为空时用类别编号。"""
    text = raw if isinstance(raw, str) else ""
    text = "".join(" " if unicodedata.category(c) == "Cc" else c for c in text).strip()
    return text[:MAX_NAME].strip() or str(class_id)


def class_names(ready: Mapping[str, Any], fallback: Sequence[str] = ()) -> dict[int, str]:
    """类别编号 → 名称：子进程给出的模型类别（ready.names），缺少的用平台记录的 class_names 补上。"""
    names = {i: clean_class_name(name, i) for i, name in enumerate(fallback)}
    raw = ready.get("names")
    if isinstance(raw, dict):
        for key, value in raw.items():
            with contextlib.suppress(TypeError, ValueError):
                index = int(key)
                if index >= 0:
                    names[index] = clean_class_name(value, index)
    return names


def _error_text(value: Any, default: str) -> str:
    text = value.strip() if isinstance(value, str) else ""
    return (text or default)[:MAX_ERROR]


def translate_reply(reply: Mapping[str, Any], names: Mapping[int, str]) -> dict[str, Any]:
    """子进程对一张图片的回答 → 平台 results 接口中的一条结果。框按编号换成类别名称；不合法的框跳过，
    最多 1000 个（平台的上限；max_det 不超过它）。"""
    image_id = reply["id"]
    if reply.get("ok") is not True:
        return {"id": image_id, "ok": False, "error": _error_text(reply.get("error"), "识别失败")}
    width, height = reply.get("width"), reply.get("height")
    if not (_positive_int(width) and _positive_int(height)):
        return {"id": image_id, "ok": False, "error": "识别进程没有给出图片的尺寸"}
    boxes = []
    raw_boxes = reply.get("boxes")
    for raw in raw_boxes if isinstance(raw_boxes, list) else []:
        if len(boxes) == MAX_BOXES:
            break
        if not isinstance(raw, list) or len(raw) < 6:
            continue
        values = [_number(v) for v in raw[:6]]
        if any(v is None for v in values):
            continue
        x1, y1, x2, y2, conf, cls = (float(v) for v in values)  # type: ignore[arg-type]
        if cls < 0 or cls != int(cls) or x2 <= x1 or y2 <= y1:
            continue
        class_id = int(cls)
        boxes.append(
            {
                "name": names.get(class_id) or str(class_id),
                "class_id": class_id,
                "confidence": round(min(max(conf, 0.0), 1.0), 4),
                "xmin": x1,
                "ymin": y1,
                "xmax": x2,
                "ymax": y2,
            }
        )
    return {"id": image_id, "ok": True, "width": width, "height": height, "boxes": boxes}


def encoded_size(value: Any) -> int:
    """value 作为请求体的一部分时的字节数（与 Client.post 的编码相同）。"""
    return len(json.dumps(value, ensure_ascii=False, allow_nan=False).encode("utf-8"))


def fit_result(result: dict[str, Any], limit: int) -> tuple[dict[str, Any], int]:
    """一条结果本身超过 limit 字节（极长的类别名称乘以很多框）时只保留置信度最高的框，返回 (结果, 字节数)。"""
    size = encoded_size(result)
    if size <= limit or not result.get("boxes"):
        return result, size
    boxes = sorted(result["boxes"], key=lambda b: b["confidence"], reverse=True)
    base = encoded_size({**result, "boxes": []})
    kept, total = [], base
    for box in boxes:
        box_size = encoded_size(box) + 1  # 逗号
        if total + box_size > limit:
            break
        kept.append(box)
        total += box_size
    log.warning(
        "image %s: keeping %d of %d boxes to stay under the request limit",
        result["id"],
        len(kept),
        len(boxes),
    )
    trimmed = {**result, "boxes": kept}
    return trimmed, encoded_size(trimmed)


class ResultBatch:
    """攒批上报的结果。满 FLUSH_COUNT 条或 FLUSH_BYTES 字节（full）、或第一条等了 FLUSH_INTERVAL 秒（due）
    时上报；加入前用 fits() 检查请求体不会超过 MAX_BODY_BYTES。"""

    OVERHEAD = len('{"results": []}')

    def __init__(self, clock: Callable[[], float] = time.monotonic) -> None:
        self.clock = clock
        self.items: list[dict[str, Any]] = []
        self.size = self.OVERHEAD
        self.first_at: float | None = None

    def __len__(self) -> int:
        return len(self.items)

    def fits(self, size: int) -> bool:
        return not self.items or self.size + size + 2 <= MAX_BODY_BYTES

    def add(self, result: dict[str, Any], size: int) -> None:
        if not self.items:
            self.first_at = self.clock()
        self.items.append(result)
        self.size += size + 2  # ", "

    @property
    def full(self) -> bool:
        return len(self.items) >= FLUSH_COUNT or self.size >= FLUSH_BYTES

    def seconds_until_due(self) -> float:
        if self.first_at is None:
            return math.inf
        return max(0.0, self.first_at + FLUSH_INTERVAL - self.clock())

    def clear(self) -> None:
        self.items = []
        self.size = self.OVERHEAD
        self.first_at = None


# ---------------------------------------------------------------------------
# 父进程：执行平台的识别任务


def _url(value: Any) -> str | None:
    """平台给出的相对地址（以 / 开头，不能指向其它主机）。"""
    return value if isinstance(value, str) and value.startswith("/") and "//" not in value else None


def _with_query(url: str, **params: Any) -> str:
    query = "&".join(f"{k}={v}" for k, v in params.items())
    return url + ("&" if "?" in url else "?") + query


@dataclass(frozen=True)
class InferenceTaskInfo:
    """平台下发的 review_inference 任务中本节点用到的字段（见 docs/architecture.md 第 9 节）。"""

    id: int
    model_id: int | None
    yolo_version: str | None
    task: str | None
    size_bytes: int
    sha256: str
    class_names: tuple[str, ...]
    conf: float
    iou: float
    imgsz: tuple[int, int]
    max_det: int
    image_count: int | None
    remaining: int | None
    model_url: str
    images_url: str
    progress_url: str
    results_url: str
    complete_url: str
    fail_url: str

    @classmethod
    def parse(cls, task: Mapping[str, Any]) -> InferenceTaskInfo:
        """校验任务的结构；不合法时抛出 ValueError（说明哪个字段不对）。"""

        def url(key: str) -> str:
            value = _url(task.get(key))
            if value is None:
                raise ValueError(key)
            return value

        def opt_str(container: Mapping[str, Any], key: str) -> str | None:
            value = container.get(key)
            return value if isinstance(value, str) and value else None

        def count(key: str) -> int | None:
            value = task.get(key)
            if value is None:
                return None
            if isinstance(value, bool) or not isinstance(value, int) or value < 0:
                raise ValueError(key)
            return value

        if not _positive_int(task.get("id")):
            raise ValueError("id")
        model = task.get("model")
        if not isinstance(model, dict):
            raise ValueError("model")
        if not _positive_int(model.get("size_bytes")):
            raise ValueError("model.size_bytes")
        sha = model.get("sha256")
        if not isinstance(sha, str) or not _SHA256.fullmatch(sha):
            raise ValueError("model.sha256")
        names = model.get("class_names") or []
        if not isinstance(names, list) or not all(isinstance(n, str) for n in names):
            raise ValueError("model.class_names")
        options = task.get("options")
        if not isinstance(options, dict):
            raise ValueError("options")
        conf, iou = _number(options.get("conf")), _number(options.get("iou"))
        if conf is None or not 0 < conf <= 1:
            raise ValueError("options.conf")
        if iou is None or not 0 < iou <= 1:
            raise ValueError("options.iou")
        try:
            imgsz = parse_imgsz(options.get("imgsz"))
        except ValueError:
            imgsz = None
        if imgsz is None:
            raise ValueError("options.imgsz")
        max_det = options.get("max_det")
        if not _positive_int(max_det) or max_det > MAX_BOXES:
            raise ValueError("options.max_det")
        return cls(
            id=task["id"],
            model_id=model.get("id") if _positive_int(model.get("id")) else None,
            yolo_version=opt_str(model, "yolo_version"),
            task=opt_str(model, "task"),
            size_bytes=model["size_bytes"],
            sha256=sha.lower(),
            class_names=tuple(names),
            conf=conf,
            iou=iou,
            imgsz=imgsz,
            max_det=max_det,
            image_count=count("image_count"),
            remaining=count("remaining"),
            model_url=url("model_url"),
            images_url=url("images_url"),
            progress_url=url("progress_url"),
            results_url=url("results_url"),
            complete_url=url("complete_url"),
            fail_url=url("fail_url"),
        )


@dataclass(frozen=True)
class ImageItem:
    """images_url 列出的一张还没有识别的图片。"""

    id: int
    file_name: str
    size_bytes: int | None
    url: str

    @property
    def label(self) -> str:
        return self.file_name or f"#{self.id}"


@dataclass(frozen=True)
class _Fetched:
    """预先下载的一张图片：path（下载成功）或 error（这张图片识别失败的原因）。"""

    item: ImageItem
    path: Path | None
    error: str | None = None


class _ImageGone(Exception):
    """下载图片时平台返回 404（图片文件已不存在、图片已被删除）：只是这张图片失败。"""

    def __init__(self, message: str) -> None:
        super().__init__(message)
        self.message = message


def _suffix(file_name: str) -> str:
    """下载的图片在工作目录中的扩展名（只用于排障时辨认；读取时按内容识别格式）。"""
    ext = os.path.splitext(file_name)[1].lower()
    return ext if re.fullmatch(r"\.[a-z0-9]{1,8}", ext) else ""


class _Prefetcher:
    """按顺序预先下载图片：最多 PREFETCH_AHEAD 张（PREFETCH_WORKERS 个线程同时下载）。next() 在主线程中调用，
    需要时从 items（分页取图片列表的生成器）取下一张。"""

    def __init__(self, task: ReviewInferenceTask, items: Iterator[ImageItem], folder: Path) -> None:
        self._task = task
        self._items = items
        self._folder = folder
        self._queue: deque[tuple[ImageItem, concurrent.futures.Future[_Fetched]]] = deque()
        self._exhausted = False
        self._closed = threading.Event()
        self._pool = concurrent.futures.ThreadPoolExecutor(
            max_workers=PREFETCH_WORKERS, thread_name_prefix="review-inference-download"
        )

    def __enter__(self) -> _Prefetcher:
        return self

    def __exit__(self, exc_type: Any, *_: Any) -> None:
        # 失败或中断时不等正在进行的下载（它们在下一个分块时发现 abort）；工作目录随后整个删除
        if exc_type is not None or self._task._interrupted():
            self._closed.set()
        self._pool.shutdown(wait=not self._closed.is_set(), cancel_futures=True)

    def _abort(self) -> bool:
        return self._closed.is_set() or self._task._interrupted()

    def _fill(self) -> None:
        while not self._exhausted and len(self._queue) < PREFETCH_AHEAD:
            item = next(self._items, None)
            if item is None:
                self._exhausted = True
                return
            future = self._pool.submit(self._task._fetch_image, item, self._folder, self._abort)
            self._queue.append((item, future))

    def next(self) -> _Fetched | None:
        """下一张图片（按列表顺序）；没有更多图片时返回 None。等待下载时检查停止与放弃。"""
        self._fill()
        if not self._queue:
            return None
        _, future = self._queue.popleft()
        while True:
            try:
                fetched = future.result(timeout=0.5)
                break
            except concurrent.futures.TimeoutError:
                self._task._check_interrupted()
        self._fill()  # 子进程识别这张图片时继续下载后面的
        return fetched


class ReviewInferenceTask:
    """执行一个 review_inference 任务。run() 在任务线程中调用；cancel() 可从其它线程调用（节点停止）。"""

    def __init__(
        self,
        task: Mapping[str, Any],
        *,
        client: Client,
        node_key: str,
        state_dir: Path,
        node_name: str,
        image_timeout: float,
        command: Sequence[str] | None = None,
        clock: Callable[[], float] = time.monotonic,
        progress_interval: float = PROGRESS_INTERVAL,
        page_size: int | None = None,
    ) -> None:
        self.raw = task
        self.client = client
        self.node_key = node_key
        self.state_dir = state_dir
        self.node_name = node_name
        self.clock = clock
        self.progress_interval = progress_interval
        self.page_size = page_size or PAGE_SIZE
        self.process = InferenceProcess(
            state_dir=state_dir, image_timeout=image_timeout, command=command
        )
        self.info: InferenceTaskInfo | None = None
        self.work_dir: Path | None = None
        self.stage = "downloading"
        self.ready: dict[str, Any] | None = None
        # 本次运行识别（含失败）的图片 → 是否成功（平台漏记、下一轮重新识别的图片只算一次）
        self._outcomes: dict[int, bool] = {}
        self._names: dict[int, str] = {}
        self._remaining: int | None = None  # 平台上还没有识别的图片数（来自任务与上报结果的响应）
        self._batch = ResultBatch(clock)
        self._headers = {"X-Node-Key": node_key}
        self._stopping = threading.Event()
        self._finished = threading.Event()
        self._abandoned: Exception | None = None
        self._progress_lock = threading.Lock()

    @property
    def processed(self) -> int:
        return len(self._outcomes)

    @property
    def failed(self) -> int:
        return sum(1 for ok in self._outcomes.values() if not ok)

    # ---- 取消 ----

    def cancel(self) -> None:
        """节点停止：结束子进程，中止下载；run() 随后上报已有的结果与可重试的失败（任务重新排队）。"""
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
            raise TaskFailed("训练节点正在停止，识别已中断", retryable=True)

    def _api_error(self, exc: ApiError, what: str) -> Exception:
        """任务调用的 HTTP 错误：404 / 409 放弃；5xx（507 平台磁盘已满除外）与 401 可重试；
        其余（400、413、422、507 …）不可重试。"""
        if exc.status in (404, 409):
            return TaskAbandoned(f"{what}: {exc}")
        retryable = (exc.status >= 500 and exc.status != 507) or exc.status == 401
        return TaskFailed(f"{what}失败：{exc.message}", str(exc), retryable=retryable)

    def _call(
        self,
        fn: Callable[[], _T],
        what: str,
        *,
        image: bool = False,
        abort: Callable[[], bool] | None = None,
    ) -> _T:
        """调用平台的幂等接口（图片列表、下载图片、上报结果、完成）：网络错误与可重试的 HTTP 错误先在
        这里重试几次（长时间的识别不因为一次网络抖动而重新开始），仍失败时按 _api_error 分类。
        image=True 时 404（不是 unknown_node）表示这张图片已不存在，抛出 _ImageGone。"""
        delays = (*FAIL_RETRY_DELAYS, None)
        for delay in delays:
            self._check_interrupted()
            if abort is not None and abort():
                raise TaskFailed(f"{what}已取消")
            try:
                return fn()
            except ApiError as exc:
                if image and exc.status == 404 and exc.code != "unknown_node":
                    raise _ImageGone(exc.message) from None
                failure = self._api_error(exc, what)
                if not isinstance(failure, TaskFailed) or not failure.retryable or delay is None:
                    raise failure from None
            except TransportError as exc:
                self._check_interrupted()
                if delay is None:
                    raise TaskFailed(f"{what}失败：{exc}", retryable=True) from None
                failure = TaskFailed(f"{what}失败：{exc}", retryable=True)
            log.debug("review inference: %s failed, retrying in %gs: %s", what, delay, failure)
            self._stopping.wait(delay)
        raise AssertionError("unreachable")  # pragma: no cover

    # ---- 执行 ----

    def run(self) -> str:
        """执行任务，返回结果：succeeded / failed / abandoned / invalid。不抛出异常。"""
        try:
            self.info = info = InferenceTaskInfo.parse(self.raw)
        except ValueError as exc:
            return self._invalid(str(exc))
        started = self.clock()
        self._remaining = info.remaining
        log.info(
            "review inference %s started: model=%s (%s %s, %d bytes), %s images (%s remaining), "
            "conf=%g iou=%g imgsz=%dx%d max_det=%d",
            info.id,
            info.model_id,
            info.yolo_version,
            info.task,
            info.size_bytes,
            info.image_count,
            info.remaining,
            info.conf,
            info.iou,
            info.imgsz[0],
            info.imgsz[1],
            info.max_det,
        )
        keeper = threading.Thread(
            target=self._keep_lease, name=f"review-inference-{info.id}-lease", daemon=True
        )
        keeper.start()
        try:
            result = self._execute(info, started)
        except TaskAbandoned as exc:
            log.info(
                "review inference %s abandoned after %.1fs: it was deleted or is no longer "
                "assigned to this node (%s)",
                info.id,
                self.clock() - started,
                exc,
            )
            return "abandoned"
        except TaskFailed as failure:
            if self._abandoned is not None:
                log.info("review inference %s abandoned: %s", info.id, self._abandoned)
                return "abandoned"
            log.warning(
                "review inference %s failed after %.1fs (retryable=%s, %d images done): %s",
                info.id,
                self.clock() - started,
                failure.retryable,
                self.processed,
                failure.message,
            )
            self._finished.set()
            self._report_failure(info, failure)
            return "failed"
        except Exception as exc:  # 防御：意外的错误也上报（可重试），任务不必等到租约过期
            log.exception("review inference %s failed unexpectedly", info.id)
            if self._abandoned is not None:
                return "abandoned"
            failure = TaskFailed(
                f"训练节点执行识别时出错：{_clean_message(exc)}",
                summarize_exception(exc, MAX_DETAIL),
                retryable=True,
            )
            self._finished.set()
            self._report_failure(info, failure)
            return "failed"
        finally:
            self._finished.set()
            self.process.close(kill=True)  # 已经正常结束时什么也不做
            # 续租线程可能正在上报进度；停止时不等它（守护线程）
            keeper.join(timeout=2 if self._stopping.is_set() else 15)
            self._cleanup()
        log.info(
            "review inference %s succeeded in %.1fs: %d images (%d failed), %s images/s on %s",
            info.id,
            self.clock() - started,
            self.processed,
            self.failed,
            result.get("images_per_second"),
            result.get("device"),
        )
        return "succeeded"

    def _report_failure(self, info: InferenceTaskInfo, failure: TaskFailed) -> None:
        """先把已经识别的结果交给平台（重试时不必再识别），再上报失败。停止时各只试一次、限时。"""
        stopping = self._stopping.is_set()
        timeout = STOPPING_FAIL_TIMEOUT if stopping else None
        if self._batch.items:
            try:
                self._post_results(timeout=timeout)
            except (ApiError, TransportError) as exc:
                log.debug(
                    "review inference %s: cannot hand over %d results: %s",
                    info.id,
                    len(self._batch),
                    exc,
                )
        report_failure(
            self.client,
            self.node_key,
            info.fail_url,
            failure,
            attempts=1 if stopping else len(FAIL_RETRY_DELAYS) + 1,
            timeout=timeout,
            wait=self._stopping.wait,
        )

    def _invalid(self, field: str) -> str:
        task_id = self.raw.get("id")
        log.warning(
            "review inference task %r is malformed (field %s), not running it", task_id, field
        )
        url = fail_url_of(self.raw)
        if url is not None:
            failure = TaskFailed(
                f"训练节点无法识别平台下发的审图识别任务（字段 {field} 缺失或不合法），"
                f"请确认平台与训练节点（{__version__}）的版本匹配"
            )
            report_failure(self.client, self.node_key, url, failure, attempts=1)
        return "invalid"

    def _execute(self, info: InferenceTaskInfo, started: float) -> dict[str, Any]:
        work = self._prepare_work_dir(info)
        self._check_disk(work, info.size_bytes)

        self._report_stage("downloading")
        weights = work / "model.pt"
        self._download_model(info, weights)

        self._report_stage("inferring")
        spec = InferenceSpec(
            weights=str(weights),
            task=info.task,
            yolo_version=info.yolo_version,
            conf=info.conf,
            iou=info.iou,
            imgsz=info.imgsz,
            max_det=info.max_det,
        )
        try:
            self.ready = ready = self.process.start(spec, cwd=work)
        except InferenceError as exc:
            self._check_interrupted()
            raise TaskFailed(exc.message, exc.detail) from None
        self._names = class_names(ready, info.class_names)
        log.info(
            "review inference %s: model loaded on %s%s (Ultralytics %s, PyTorch %s)",
            info.id,
            ready.get("device"),
            f" ({ready['device_name']})" if ready.get("device_name") else "",
            (ready.get("versions") or {}).get("ultralytics"),
            (ready.get("versions") or {}).get("torch"),
        )
        inferring = self.clock()
        self._infer_all(info, work)
        elapsed = self.clock() - inferring
        self.process.close()
        if self.process.stderr_tail():
            log.debug("review inference stderr:\n%s", self.process.stderr_tail(2000))

        versions = ready.get("versions") if isinstance(ready.get("versions"), dict) else {}
        result = {
            "device": ready.get("device"),
            "device_name": ready.get("device_name"),
            "torch_version": versions.get("torch"),
            "ultralytics_version": versions.get("ultralytics"),
            "opencv_version": versions.get("opencv"),
            "node_name": self.node_name,
            "images_inferred": self.processed,
            "images_failed": self.failed,
            "elapsed_seconds": round(self.clock() - started, 1),
            "images_per_second": round(self.processed / elapsed, 2)
            if self.processed and elapsed > 0
            else None,
        }
        self._call(
            lambda: self.client.post(info.complete_url, {"result": result}, headers=self._headers),
            "完成识别",
        )
        return result

    def _prepare_work_dir(self, info: InferenceTaskInfo) -> Path:
        work = self.state_dir / "work" / f"{WORK_DIR_PREFIX}{info.id}"
        try:
            if work.exists():
                shutil.rmtree(work)  # 上次运行的残留（如进程崩溃）
            (work / "images").mkdir(parents=True)
        except OSError as exc:
            raise TaskFailed(f"无法创建工作目录 {work}：{exc}", retryable=True) from None
        self.work_dir = work
        return work

    def _check_disk(self, work: Path, size: int) -> None:
        need = size + IMAGE_DISK_RESERVE
        try:
            free = shutil.disk_usage(work).free
        except OSError as exc:
            log.debug("disk usage of %s unavailable: %s", work, exc)
            return
        if free < need:
            raise TaskFailed(
                f"训练节点的磁盘空间不足：识别需要约 {format_bytes(need)}，"
                f"{work.parent} 所在的磁盘只剩 {format_bytes(free)}",
                retryable=True,
            )

    def _download_model(self, info: InferenceTaskInfo, dest: Path) -> None:
        try:
            got = self.client.download(
                info.model_url,
                dest,
                headers=self._headers,
                timeout=TRANSFER_TIMEOUT,
                max_bytes=info.size_bytes,
                abort=self._interrupted,
            )
        except ApiError as exc:
            raise self._api_error(exc, "下载模型文件") from None
        except TransportError as exc:
            self._check_interrupted()
            raise TaskFailed(f"下载模型文件失败：{exc}", retryable=True) from None
        except OSError as exc:
            raise TaskFailed(f"无法写入模型文件 {dest}：{exc}", retryable=True) from None
        self._check_interrupted()
        verify_download(got, info.size_bytes, info.sha256)

    # ---- 识别 ----

    def _infer_all(self, info: InferenceTaskInfo, work: Path) -> None:
        """一轮轮识别平台列出的图片，直到某一轮没有图片。平台只列出还没有识别的图片，所以第二轮通常只是
        一次空的列表请求；结果被平台漏掉（如上报时平台重启）的图片在下一轮补上。"""
        busy_passes = 0
        while True:
            processed = self._infer_pass(info, work / "images")
            self._flush()
            if processed == 0:
                return
            busy_passes += 1
            if busy_passes >= MAX_PASSES:
                raise TaskFailed(
                    f"平台在 {MAX_PASSES} 轮识别后仍列出没有识别的图片：上报的识别结果没有被平台记录",
                    retryable=True,
                )

    def _infer_pass(self, info: InferenceTaskInfo, folder: Path) -> int:
        count = 0
        in_flight: deque[_Fetched] = deque()
        with _Prefetcher(self, self._iter_images(info), folder) as prefetch:
            while True:
                while len(in_flight) < PIPELINE_DEPTH:
                    fetched = prefetch.next()
                    if fetched is None:
                        break
                    count += 1
                    if fetched.path is None:
                        self._add_result(
                            {"id": fetched.item.id, "ok": False, "error": fetched.error}
                        )
                        continue
                    self.process.send(fetched.item.id, str(fetched.path))
                    in_flight.append(fetched)
                if not in_flight:
                    return count
                reply = self._next_reply()
                fetched = in_flight.popleft()
                if fetched.path is not None:
                    with contextlib.suppress(OSError):
                        fetched.path.unlink()
                self._add_result(translate_reply(reply, self._names))

    def _next_reply(self) -> dict[str, Any]:
        """等子进程回答最早交给它的图片；等待期间到了上报时间就上报攒下的结果。"""
        while True:
            self._check_interrupted()
            wait = min(self._batch.seconds_until_due(), 1.0)
            try:
                reply = self.process.next_reply(wait)
            except InferenceError as exc:
                self._check_interrupted()
                raise TaskFailed(exc.message, exc.detail) from None
            if reply is not None:
                return reply
            if self._batch.items and self._batch.seconds_until_due() <= 0:
                self._flush()

    def _iter_images(self, info: InferenceTaskInfo) -> Iterator[ImageItem]:
        """按 ID 分页列出平台上还没有识别的图片（after = 上一页最后一张），直到某一页为空。"""
        after = 0
        while True:
            url = _with_query(info.images_url, after=after, limit=self.page_size)
            page = self._call(
                lambda url=url: self.client.get(url, headers=self._headers), "获取图片列表"
            )
            items = self._parse_page(page, after)
            if isinstance(page.get("remaining"), int) and not isinstance(page["remaining"], bool):
                self._remaining = max(0, page["remaining"])
            if not items:
                return
            yield from items
            after = items[-1].id

    def _parse_page(self, page: Mapping[str, Any], after: int) -> list[ImageItem]:
        raw = page.get("items")
        if not isinstance(raw, list):
            raise TaskFailed("平台返回的图片列表不合法（缺少 items）")
        items: list[ImageItem] = []
        last = after
        for entry in raw:
            if not isinstance(entry, dict) or not _positive_int(entry.get("id")):
                raise TaskFailed("平台返回的图片列表不合法（图片缺少 id）")
            if entry["id"] <= last:  # 必须按 ID 递增且在 after 之后，否则分页可能不会结束
                raise TaskFailed(f"平台返回的图片列表不合法（图片 {entry['id']} 不在 {last} 之后）")
            url = _url(entry.get("url"))
            if url is None:
                raise TaskFailed(f"平台返回的图片列表不合法（图片 {entry['id']} 的地址不正确）")
            size = entry.get("size_bytes")
            name = entry.get("file_name")
            items.append(
                ImageItem(
                    id=entry["id"],
                    file_name=name if isinstance(name, str) else "",
                    size_bytes=size if _positive_int(size) else None,
                    url=url,
                )
            )
            last = entry["id"]
        return items

    def _fetch_image(self, item: ImageItem, folder: Path, abort: Callable[[], bool]) -> _Fetched:
        """（下载线程中）下载一张图片。图片不存在（404）或太大时返回这张图片的失败原因；其它失败抛出。
        abort()：任务中断或这一轮已经结束（出错）时为 True，下载随即停止。"""
        if item.size_bytes is not None and item.size_bytes > MAX_IMAGE_BYTES:
            return _Fetched(
                item,
                None,
                f"图片太大（{format_bytes(item.size_bytes)}），训练节点最多识别 "
                f"{format_bytes(MAX_IMAGE_BYTES)} 的图片",
            )
        dest = folder / f"{item.id}{_suffix(item.file_name)}"
        try:
            self._call(
                lambda: self.client.download(
                    item.url,
                    dest,
                    headers=self._headers,
                    timeout=TRANSFER_TIMEOUT,
                    max_bytes=MAX_IMAGE_BYTES,
                    abort=abort,
                ),
                f"下载图片 {item.label}",
                image=True,
                abort=abort,
            )
        except _ImageGone as exc:
            return _Fetched(item, None, _error_text(f"无法下载图片：{exc.message}", "无法下载图片"))
        except OSError as exc:
            raise TaskFailed(f"无法写入图片 {dest}：{exc}", retryable=True) from None
        return _Fetched(item, dest)

    # ---- 结果 ----

    def _add_result(self, result: dict[str, Any]) -> None:
        self._outcomes[result["id"]] = result.get("ok") is True
        result, size = fit_result(result, MAX_BODY_BYTES - ResultBatch.OVERHEAD - 2)
        if not self._batch.fits(size):
            self._flush()
        self._batch.add(result, size)
        if self._batch.full:
            self._flush()

    def _flush(self) -> None:
        if self._batch.items:
            self._call(self._post_results, "上报识别结果")

    def _post_results(self, timeout: float | None = None) -> None:
        """POST results（攒下的结果）；成功后清空。平台忽略已经识别过的图片，重复上报没有关系。"""
        assert self.info is not None
        resp = self.client.post(
            self.info.results_url,
            {"results": self._batch.items},
            headers=self._headers,
            timeout=timeout,
        )
        remaining = resp.get("remaining")
        if isinstance(remaining, int) and not isinstance(remaining, bool):
            self._remaining = max(0, remaining)
        elif self._remaining is not None:
            accepted = resp.get("accepted")
            done = accepted if isinstance(accepted, int) else len(self._batch)
            self._remaining = max(0, self._remaining - done)
        self._batch.clear()

    # ---- 进度与租约 ----

    def progress_message(self) -> str:
        if self.stage != "inferring":
            return "正在下载模型"
        if self.ready is None:
            return "正在加载模型"
        info = self.info
        total = info.image_count if info is not None else None
        if total and self._remaining is not None:
            return f"已识别 {max(0, total - self._remaining)} / {total}"
        return f"已识别 {self.processed} 张"

    def _report_stage(self, stage: str) -> None:
        """进入新阶段：立即上报（续租）。"""
        self._check_interrupted()
        self.stage = stage
        self._progress()
        self._check_interrupted()

    def _progress(self) -> None:
        """POST progress。404 / 409（任务被删除、已不属于本节点）时放弃任务；其它错误只记录（租约有 60 秒，
        之后的上报会续上）。"""
        assert self.info is not None
        payload = {"stage": self.stage, "message": self.progress_message()}
        with self._progress_lock:
            if self._abandoned is not None:
                return
            try:
                self.client.post(self.info.progress_url, payload, headers=self._headers)
            except ApiError as exc:
                if exc.status in (404, 409):
                    self._abandon(exc)
                else:
                    log.debug("review inference %s: progress report failed: %s", self.info.id, exc)
            except TransportError as exc:
                log.debug("review inference %s: progress report failed: %s", self.info.id, exc)

    def _keep_lease(self) -> None:
        while not self._finished.wait(self.progress_interval):
            if self._stopping.is_set():
                return
            self._progress()

    def _cleanup(self) -> None:
        work = self.work_dir
        if work is None:
            return
        try:
            shutil.rmtree(work)
        except FileNotFoundError:
            pass
        except OSError as exc:
            log.warning("cannot remove work directory %s: %s", work, exc)


if __name__ == "__main__":
    sys.exit(child_main())
