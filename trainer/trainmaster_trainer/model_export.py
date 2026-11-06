"""模型仓库的 pt → ONNX 转换。

与 environment.py 一样分两部分：
- 子进程（python -m trainmaster_trainer.model_export --spec <json>）：导入 ultralytics / onnx，按 YOLO 版本
  与后处理方式在 CPU 上导出 ONNX，用 onnx 读取并核对结果（opset、输入输出、metadata 中的 end2end 与 nms），
  有 onnxruntime 时用全零输入推理一次，把结果以 JSON 输出到 stdout 的最后一行：
  {"ok": true, "onnx_path": …, "result": {…}} 或 {"ok": false, "error": "中文说明", "detail": "回溯尾部"}。
  torch / ultralytics / onnx 只在这个短命的子进程中导入，常驻进程不导入它们、不占用显存。
  pt 文件来自用户上传：子进程只继承运行 Python 需要的环境变量（没有 TM_NODE_TOKEN 等），并用 Ultralytics
  的安全加载（ULTRALYTICS_SAFE_LOAD，torch.load(weights_only=True)）只还原已知的模型类，不执行文件中的代码。
- 父进程：ExportProcess 限时运行子进程（可从其它线程取消，结束整个进程组）；ModelExportTask 在任务线程中
  执行平台下发的转换任务：工作目录 → 检查磁盘 → 下载并校验 pt → 转换 → 上传 onnx（先带上文件大小上报
  进度，平台确认能接收）→ 完成，期间定期上报进度（续租）。平台上的任务被删除或已不属于本节点（404 / 409）
  时结束子进程并放弃，不再上报。
"""

from __future__ import annotations

import argparse
import ast
import contextlib
import hashlib
import importlib
import json
import logging
import os
import re
import shutil
import subprocess
import sys
import threading
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from . import __version__, procs
from .client import ApiError, Client, Transfer, TransportError, json_safe
from .environment import _kill, _short_error, _tail, child_env, summarize_exception

log = logging.getLogger(__name__)

CHILD_MODULE = "trainmaster_trainer.model_export"
KIND = "model_export"

# YOLO 版本（平台 models.yolo_version 的取值）与显示名；yolov10 / yolo26 带端到端（NMS-free）检测头。
YOLO_LABELS = {
    "yolov8": "YOLOv8",
    "yolov9": "YOLOv9",
    "yolov10": "YOLOv10",
    "yolo11": "YOLO11",
    "yolo12": "YOLO12",
    "yolo26": "YOLO26",
}
END2END_VERSIONS = ("yolov10", "yolo26")
# 后处理方式 → ultralytics export 的 nms 参数：raw 原始输出（nms=None）、nms 导出时嵌入 NMS（nms=True）、
# end2end 端到端头（nms=False；只对带 one2one 头的模型有效，其它模型上 ultralytics 会静默忽略）。
NMS_ARG: dict[str, bool | None] = {"raw": None, "nms": True, "end2end": False}
POSTPROCESS = tuple(NMS_ARG)
POSTPROCESS_LABELS = {
    "raw": "原始输出（raw）",
    "nms": "嵌入 NMS（nms）",
    "end2end": "端到端（end2end）",
}
# 有检测框的任务：只有它们能嵌入 NMS 或带端到端头（与平台的 nmsTasks 相同）。
NMS_TASKS = ("detect", "segment", "pose", "obb")
# 说明中的任务名（与平台的 taskLabels 相同）。
TASK_LABELS = {
    "detect": "检测",
    "segment": "分割",
    "classify": "分类",
    "pose": "姿态",
    "obb": "旋转框",
    "semantic": "语义分割",
    "depth": "深度估计",
}
# 加载各版本的模型至少需要的 Ultralytics 版本（只列出确定的），加载失败时作为升级提示。
MIN_ULTRALYTICS = (("yolo26", "8.4.0"), ("yolo11", "8.3.0"))
# Ultralytics 的安全加载开关：torch.load(weights_only=True)，只允许其清单中的类（ultralytics.nn.tasks._SafeLoad）。
SAFE_LOAD_ENV = "ULTRALYTICS_SAFE_LOAD"
# 安全加载按需允许的模型类所在的模块（与 _SafeLoad.register 相同：这些模块，以及 _SAFE_CLASS_PACKAGES 的
# 直接子模块）：其中不存在的类（大写开头）说明模型由更新的版本保存，而不是夹带了不安全的对象。
_SAFE_CLASS_MODULES = (
    "torch.nn.modules",
    "ultralytics.nn.modules",
    "ultralytics.nn.tasks",
    "ultralytics.nn.distill_model",
    "ultralytics.utils.loss",
    "ultralytics.utils.tal",
)
_SAFE_CLASS_PACKAGES = ("torch.nn.modules", "ultralytics.nn.modules")
# torch 拒绝时的说明中被拒绝的对象：“Unsupported global: GLOBAL os.system …”、“unsupported GLOBAL
# posix.system whose module posix is blocked”，以及 Ultralytics 限制的 getattr；没有对象时的原因
# （“WeightsUnpickler error: Unsupported operand 83”：文件已损坏或不是 torch.save 保存的内容）。
_REFUSED_GLOBAL = re.compile(r"\bGLOBAL\s+([A-Za-z_][\w.]*)")
_REFUSED_GETATTR = re.compile(r"unsafe (getattr\([^)]*\))")
_UNPICKLER_ERROR = re.compile(r"WeightsUnpickler error:\s*([^\n]+)")
# 转换子进程只继承这些环境变量（加上 LC_* 与 YOLO_*）：运行 Python 与查找依赖、语言与时区、临时目录、
# CPU 线程数。pt 文件来自用户上传，不把 TM_NODE_TOKEN 等节点配置与其它密钥交给加载它的进程。
_EXPORT_ENV_NAMES = frozenset(
    {
        "PATH", "HOME", "USER", "LOGNAME", "LANG", "LANGUAGE", "TZ", "TMPDIR", "TEMP", "TMP",
        "PYTHONPATH", "PYTHONHOME", "PYTHONNOUSERSITE", "PYTHONUSERBASE", "PYTHONIOENCODING",
        "PYTHONUTF8", "PYTHONDONTWRITEBYTECODE", "PYTHONPYCACHEPREFIX", "VIRTUAL_ENV",
        "LD_LIBRARY_PATH", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "MPLCONFIGDIR", "CUDA_DEVICE_ORDER",
        "OMP_NUM_THREADS", "MKL_NUM_THREADS", "OPENBLAS_NUM_THREADS", "NUMEXPR_NUM_THREADS",
        # Windows：没有 SYSTEMROOT 时 Python 无法初始化（随机数、socket），其余是系统目录、用户目录与 CPU 信息
        # （os.environ 在 Windows 上的键是大写的）。
        "SYSTEMROOT", "WINDIR", "SYSTEMDRIVE", "COMSPEC", "PATHEXT", "USERPROFILE", "HOMEDRIVE",
        "HOMEPATH", "USERNAME", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES",
        "PROGRAMFILES(X86)", "PROGRAMW6432", "COMMONPROGRAMFILES", "ALLUSERSPROFILE", "PUBLIC",
        "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "PROCESSOR_IDENTIFIER", "PROCESSOR_LEVEL",
        "PROCESSOR_REVISION", "OS",
    }
)  # fmt: skip
_EXPORT_ENV_PREFIXES = ("LC_", "YOLO_")
# ONNX TensorProto.DataType 的编号（ONNX 标准中固定）→ 结果中的类型名。
_ONNX_TYPES = {
    1: "float32",
    2: "uint8",
    3: "int8",
    4: "uint16",
    5: "int16",
    6: "int32",
    7: "int64",
    8: "string",
    9: "bool",
    10: "float16",
    11: "float64",
    12: "uint32",
    13: "uint64",
    16: "bfloat16",
}

# 平台把 error 截断到 500 个字符、error_detail 截断到 8 KiB；这里先截断，保留最有用的部分。
MAX_ERROR = 500
MAX_DETAIL = 8000
MAX_OUTPUT_BYTES = 4 << 30  # 平台接受的 onnx 上限
DISK_FACTOR = 3  # 工作目录需要的空间：pt 大小的 3 倍（pt、onnx 与转换时的临时文件）
PROGRESS_INTERVAL = 15.0
TRANSFER_TIMEOUT = 60.0  # 下载 / 上传时每次读写的超时
STOPPING_FAIL_TIMEOUT = 5.0  # 停止时上报失败的超时：不拖慢 docker stop
FAIL_RETRY_DELAYS = (1.0, 3.0)
_SHA256 = re.compile(r"[0-9a-fA-F]{64}")


class ExportError(Exception):
    """转换失败，message 为直接展示给用户的中文说明。"""

    def __init__(self, message: str, detail: str | None = None) -> None:
        super().__init__(message)
        self.message = message
        self.detail = detail


def yolo_label(version: str | None) -> str:
    return YOLO_LABELS.get(version or "", version or "未知版本")


def version_tuple(text: str | None) -> tuple[int, ...]:
    """'8.4.198' → (8, 4, 198)；只取开头的数字部分（'8.3.0.dev1' → (8, 3, 0)），无法解析时为 ()。"""
    m = re.match(r"\s*v?(\d+(?:\.\d+)*)", text or "")
    return tuple(int(p) for p in m.group(1).split(".")) if m else ()


def _clean_message(exc: BaseException) -> str:
    """异常摘要（类型: 第一行），去掉 ultralytics 消息开头的 “ERROR ❌️”。"""
    text = _short_error(exc)
    return re.sub(r"ERROR\s*❌️?\s*", "", text).strip()


def _is_out_of_memory(exc: BaseException) -> bool:
    if isinstance(exc, MemoryError):
        return True
    text = str(exc).lower()
    return any(
        s in text
        for s in ("out of memory", "can't allocate memory", "defaultcpuallocator", "bad_alloc")
    )


@dataclass(frozen=True)
class ExportSpec:
    """一次转换的参数（子进程的 --spec）。imgsz 为 None 时用模型训练时的尺寸；postprocess 为 None 时
    按模型选择（端到端模型 end2end，其余 raw）。"""

    weights: str
    output: str | None = None
    task: str | None = None
    yolo_version: str | None = None
    imgsz: tuple[int, int] | None = None
    dynamic: bool = False
    postprocess: str | None = None
    opset: int | None = None
    saved_ultralytics_version: str | None = None

    @classmethod
    def from_json(cls, data: Any) -> ExportSpec:
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

        imgsz = data.get("imgsz")
        if imgsz is not None:
            if isinstance(imgsz, int) and not isinstance(imgsz, bool):
                imgsz = [imgsz, imgsz]
            if (
                not isinstance(imgsz, list)
                or len(imgsz) != 2
                or any(isinstance(v, bool) or not isinstance(v, int) or v <= 0 for v in imgsz)
            ):
                raise ValueError(f"imgsz 应为正整数或 [高, 宽]：{imgsz!r}")
            imgsz = (imgsz[0], imgsz[1])
        dynamic = data.get("dynamic", False)
        if not isinstance(dynamic, bool):
            raise ValueError("dynamic 应为 true 或 false")
        postprocess = opt_str("postprocess")
        if postprocess is not None and postprocess not in POSTPROCESS:
            raise ValueError(f"postprocess 应为 {' / '.join(POSTPROCESS)}：{postprocess!r}")
        opset = data.get("opset")
        if opset is not None and (
            isinstance(opset, bool) or not isinstance(opset, int) or not 1 <= opset <= 99
        ):
            raise ValueError(f"opset 应为 1–99 的整数或 null：{opset!r}")
        return cls(
            weights=weights,
            output=opt_str("output"),
            task=opt_str("task"),
            yolo_version=opt_str("yolo_version"),
            imgsz=imgsz,
            dynamic=dynamic,
            postprocess=postprocess,
            opset=opset,
            saved_ultralytics_version=opt_str("saved_ultralytics_version"),
        )

    def to_json(self) -> dict[str, Any]:
        return {
            "weights": self.weights,
            "output": self.output,
            "task": self.task,
            "yolo_version": self.yolo_version,
            "imgsz": list(self.imgsz) if self.imgsz else None,
            "dynamic": self.dynamic,
            "postprocess": self.postprocess,
            "opset": self.opset,
            "saved_ultralytics_version": self.saved_ultralytics_version,
        }


def options_text(
    imgsz: Sequence[int] | None, dynamic: bool, postprocess: str | None, opset: Any
) -> str:
    """日志中的选项（与平台的 options_key 同样的写法）。"""
    size = f"{imgsz[0]}x{imgsz[1]}" if imgsz else "auto"
    return (
        f"imgsz={size};dynamic={int(dynamic)};postprocess={postprocess or 'auto'};"
        f"opset={opset if opset is not None else 'auto'}"
    )


# ---------------------------------------------------------------------------
# 子进程


def _too_old_message(node_version: str, yolo_version: str | None) -> str:
    """模型类不认识：节点的 Ultralytics 太旧。把该模型的版本放在提示的最前面。"""
    hints = sorted(MIN_ULTRALYTICS, key=lambda item: item[0] != yolo_version)
    text = "、".join(f"{YOLO_LABELS[v]} 需要 {minimum} 及以上" for v, minimum in hints)
    return f"训练节点的 Ultralytics {node_version} 无法加载该模型（{text}），请升级训练节点"


def _newer_hint(saved: str | None, node_version: str) -> str:
    if saved and version_tuple(saved) > version_tuple(node_version) and version_tuple(node_version):
        return f"；该模型由 Ultralytics {saved} 保存，比训练节点的 {node_version} 新"
    return ""


def _unknown_class(exc: BaseException) -> bool:
    """加载 pt 时找不到模型中的类：旧版 Ultralytics 加载新版本的模型（如 8.2 加载 YOLO11 的 C3k2）。"""
    text = str(exc)
    if isinstance(exc, AttributeError) and "Can't get attribute" in text:
        return True
    if isinstance(exc, ModuleNotFoundError):
        name = exc.name or ""
        return name.startswith("ultralytics") or "Ultralytics module" in text
    return isinstance(exc, ImportError) and "cannot import name" in text and "ultralytics" in text


def _chain_text(exc: BaseException) -> str:
    """异常及其 cause / context 的消息。"""
    texts: list[str] = []
    current: BaseException | None = exc
    while current is not None and len(texts) < 8:
        texts.append(str(current))
        current = current.__cause__ or current.__context__
    return "\n".join(texts)


def safe_load_refusal(exc: BaseException) -> str | None:
    """安全加载拒绝了文件中的对象时返回它的名字（说明中找不到名字时为空字符串），不是这种错误时返回 None。
    ultralytics 把 torch 的 UnpicklingError（“Weights only load failed …”）包装为 TypeError，名字在 cause 中。"""
    text = _chain_text(exc)
    if (
        "Weights only load failed" not in text
        and "outside the supported Ultralytics checkpoint format" not in text
    ):
        return None
    m = _REFUSED_GLOBAL.search(text) or _REFUSED_GETATTR.search(text)
    return m.group(1).rstrip(".") if m else ""


def _missing_class(name: str) -> bool:
    """被拒绝的对象是安全加载按需允许的模块中的类，但节点上的 Ultralytics / PyTorch 中没有它（模型由更新的
    版本保存）。"""
    module, _, attr = name.rpartition(".")
    if not attr[:1].isupper() or not (
        module in _SAFE_CLASS_MODULES or module.rpartition(".")[0] in _SAFE_CLASS_PACKAGES
    ):
        return False
    try:
        return not hasattr(importlib.import_module(module), attr)
    except ImportError:
        return True
    except Exception:  # 模块存在但导入出错：不是版本问题
        return False


def translate_load_error(exc: BaseException, spec: ExportSpec, node_version: str) -> str:
    saved = spec.saved_ultralytics_version
    if _is_out_of_memory(exc):
        return "训练节点内存不足，无法加载该模型"
    refused = safe_load_refusal(exc)
    if refused and not _missing_class(refused):
        return (
            f"模型文件中引用了不允许加载的对象（{refused}），为了安全训练节点不会加载它。"
            "请上传由 Ultralytics 训练保存的模型文件"
        )
    if refused == "":  # 安全加载拒绝了文件的内容，但不是某个对象：文件已损坏或不是检查点
        m = _UNPICKLER_ERROR.search(_chain_text(exc))
        why = f"（{m.group(1).strip()}）" if m else ""
        return f"模型文件已损坏，或不是 Ultralytics 保存的检查点，无法加载{why}"
    if refused is not None or _unknown_class(exc):
        return _too_old_message(node_version, spec.yolo_version) + _newer_hint(saved, node_version)
    text = str(exc)
    if isinstance(exc, FileNotFoundError):
        return f"找不到模型文件：{spec.weights}"
    if "YOLOv5" in text:
        return (
            "该模型由原版 YOLOv5 仓库（ultralytics/yolov5）训练，无法用 Ultralytics 包转换；"
            "请用 Ultralytics 包（YOLOv8 及以后的版本）训练的模型"
        )
    if "TorchScript" in text:
        return "该文件是 TorchScript 模型，不是 Ultralytics 保存的 PyTorch 检查点（.pt）"
    if (
        "not a loadable checkpoint" in text
        or "PytorchStreamReader" in text
        or isinstance(exc, EOFError)
        or type(exc).__name__ in ("UnpicklingError", "BadZipFile")
    ):
        return f"模型文件已损坏或不完整，无法加载（{_clean_message(exc)}）"
    return f"加载模型失败：{_clean_message(exc)}" + _newer_hint(saved, node_version)


def translate_export_error(
    exc: BaseException, spec: ExportSpec, imgsz: Sequence[int] | None
) -> str:
    size = f"{imgsz[0]}×{imgsz[1]}" if imgsz else "默认"
    text = str(exc)
    if _is_out_of_memory(exc):
        return (
            f"训练节点内存不足，无法转换该模型（输入尺寸 {size}）。"
            "请减小输入尺寸，或在内存更大的训练节点上转换"
        )
    if isinstance(exc, ModuleNotFoundError) and (exc.name or "").split(".")[0] in (
        "onnx",
        "onnxslim",
        "onnxruntime",
    ):
        return (
            f"训练节点缺少 ONNX 转换需要的 {exc.name}，请升级训练节点"
            "（镜像或 requirements.txt 中的 onnx、onnxslim、onnxruntime）"
        )
    if "imgsz" in text:
        return f"输入尺寸 {size} 不合法：{_clean_message(exc)}"
    if "opset" in text.lower():
        opset = spec.opset if spec.opset is not None else "自动"
        return f"无法按 opset {opset} 导出：{_clean_message(exc)}。请选择其它 opset，或选择自动"
    return f"导出 ONNX 失败：{_clean_message(exc)}"


def _head(model: Any) -> Any:
    """已加载模型的最后一层（检测 / 分类头）；结构不是 Ultralytics 的模型时为 None。"""
    layers = getattr(getattr(model, "model", None), "model", None)
    try:
        return layers[-1]  # type: ignore[index]
    except (TypeError, IndexError, KeyError):
        return None


def has_end2end_head(model: Any) -> bool:
    """模型带端到端（one2one）检测头（YOLOv10、YOLO26，或带 one2one 头的自定义结构）。"""
    return getattr(_head(model), "one2one_cv2", None) is not None


def _raw_or_nms(dynamic: bool) -> str:
    """有检测框的模型不能导出 end2end 时建议的其它后处理方式（与平台允许的组合相同）：嵌入 NMS 不能
    与动态尺寸同时使用。没有检测框的任务只能选择 raw（resolve_postprocess 先检查）。"""
    return "raw（关闭动态尺寸后也可以选择 nms）" if dynamic else "raw 或 nms"


def resolve_postprocess(spec: ExportSpec, task: str, end2end_head: bool) -> str:
    """选择后处理方式并检查组合是否可行；不可行时抛出 ExportError（平台已经校验过，这里再防一次）。
    与平台的规则相同：嵌入 NMS 与 end2end 只用于有检测框的任务（NMS_TASKS），end2end 还需要端到端头；
    嵌入 NMS 不能与动态尺寸同时使用。"""
    boxes = task in NMS_TASKS
    end2end_ok = end2end_head and boxes
    postprocess = spec.postprocess
    if postprocess is None:
        postprocess = "end2end" if end2end_ok else "raw"
    label = f"（{yolo_label(spec.yolo_version)}）" if spec.yolo_version else ""
    if task == "classify" and postprocess != "raw":
        raise ExportError(
            f"分类模型只能导出原始输出（raw），不支持{POSTPROCESS_LABELS[postprocess]}"
        )
    # 语义分割、深度估计等没有检测框的任务（YOLO26 的这些模型也没有端到端头）只有原始输出
    if postprocess != "raw" and not boxes:
        what = "嵌入 NMS" if postprocess == "nms" else "导出 end2end"
        raise ExportError(
            f"{TASK_LABELS.get(task, task) or '该'}模型没有检测框，不能{what}，请选择原始输出（raw）"
        )
    if postprocess == "nms" and spec.dynamic:
        # NMSModel 按 batch 逐张处理，导出时循环被展开：动态 batch 的图只处理第一张图片，其余输出全为 0。
        others = "原始输出（raw）/ 端到端（end2end）" if end2end_ok else "原始输出（raw）"
        raise ExportError(
            "嵌入 NMS 的 ONNX 只能固定 batch 为 1，不能与动态尺寸同时使用；"
            f"请关闭动态尺寸，或选择{others}"
        )
    if postprocess == "end2end" and not end2end_head:
        raise ExportError(
            f"该模型{label}没有端到端（NMS-free）检测头，不能导出 end2end；"
            f"请选择 {_raw_or_nms(spec.dynamic)}"
        )
    return postprocess


def _export_kwargs(spec: ExportSpec, postprocess: str) -> dict[str, Any]:
    kwargs: dict[str, Any] = {
        "format": "onnx",
        "dynamic": spec.dynamic,
        "nms": NMS_ARG[postprocess],
        "opset": spec.opset,
        "simplify": True,
        "device": "cpu",
        "batch": 1,
    }
    if spec.imgsz:
        kwargs["imgsz"] = list(spec.imgsz)
    return kwargs


def _dims(value_info: Any) -> list[int | str]:
    dims: list[int | str] = []
    for d in value_info.type.tensor_type.shape.dim:
        if d.dim_param:
            dims.append(str(d.dim_param))
        elif d.dim_value > 0:
            dims.append(int(d.dim_value))
        else:
            dims.append("?")  # 未知的维度
    return dims


def _tensor(value_info: Any) -> dict[str, Any]:
    elem = int(value_info.type.tensor_type.elem_type)
    return {
        "name": str(value_info.name),
        "shape": _dims(value_info),
        "type": _ONNX_TYPES.get(elem, f"type{elem}"),
    }


def describe_onnx(onnx: Any, path: str) -> dict[str, Any]:
    """读取导出的 ONNX：opset、输入（不含初始值）与输出、metadata_props。"""
    model = onnx.load(path, load_external_data=False)
    opset = next((int(o.version) for o in model.opset_import if o.domain in ("", "ai.onnx")), None)
    initializers = {i.name for i in model.graph.initializer}
    metadata = {str(p.key): str(p.value) for p in model.metadata_props}
    return {
        "opset": opset,
        "inputs": [_tensor(v) for v in model.graph.input if v.name not in initializers],
        "outputs": [_tensor(v) for v in model.graph.output],
        "metadata": metadata,
    }


def _metadata_flags(metadata: Mapping[str, str]) -> tuple[bool | None, bool | None]:
    """metadata 中的 (end2end, nms)：ultralytics 把 end2end 写为 "True" / "False"，nms 在 args（dict 的 repr）
    中；没有写（旧版 ultralytics）或无法解析时为 None。"""
    end2end = {"True": True, "False": False}.get(metadata.get("end2end", ""))
    nms: bool | None = None
    raw_args = metadata.get("args")
    if raw_args:
        try:
            args = ast.literal_eval(raw_args)
        except (ValueError, SyntaxError, TypeError, MemoryError, RecursionError):
            args = None
        if isinstance(args, dict) and "nms" in args:
            nms = args["nms"] is True
    return end2end, nms


def check_onnx(
    described: Mapping[str, Any], spec: ExportSpec, postprocess: str, end2end_head: bool
) -> tuple[bool, bool]:
    """核对导出结果与请求是否一致，返回 (end2end, nms)。ultralytics 会静默忽略不适用的选项
    （如 YOLO11 的 nms=False、端到端模型以外的检测头上的部分组合），这里按 metadata 报错。"""
    end2end, nms = _metadata_flags(described["metadata"])
    want_end2end = postprocess == "end2end"
    want_nms = postprocess == "nms"
    if end2end is None:
        end2end = want_end2end
    if nms is None:
        nms = want_nms
    if want_end2end and not end2end:  # 只有有检测框的任务才会请求 end2end（resolve_postprocess）
        raise ExportError(
            "该模型不支持端到端（end2end）导出：Ultralytics 导出的仍是原始输出。"
            f"请选择 {_raw_or_nms(spec.dynamic)}"
        )
    if want_nms and not nms:
        others = "raw 或 end2end" if end2end_head else "raw"
        raise ExportError(
            f"该模型不支持嵌入 NMS：Ultralytics 忽略了 nms=True，导出的是原始输出。请选择 {others}"
        )
    if end2end != want_end2end or nms != want_nms:
        raise ExportError(
            f"导出结果（end2end={end2end}，nms={nms}）与所选的后处理方式"
            f"{POSTPROCESS_LABELS[postprocess]}不一致"
        )
    inputs = described["inputs"]
    if not inputs:
        raise ExportError("导出的 ONNX 模型没有输入")
    if spec.imgsz and not spec.dynamic:
        shape = inputs[0]["shape"]
        if len(shape) != 4 or shape[2:] != list(spec.imgsz):
            raise ExportError(
                f"导出的输入形状 {shape} 与请求的输入尺寸 {spec.imgsz[0]}×{spec.imgsz[1]} 不一致"
            )
    return end2end, nms


def _input_size(spec: ExportSpec, metadata: Mapping[str, str]) -> tuple[int, int]:
    if spec.imgsz:
        return spec.imgsz
    try:
        value = ast.literal_eval(metadata.get("imgsz", ""))
        if isinstance(value, (list, tuple)) and len(value) == 2:
            return int(value[0]), int(value[1])
    except (ValueError, SyntaxError, TypeError):
        pass
    return 640, 640


def verify_onnx(
    path: str, inputs: Sequence[Mapping[str, Any]], size: tuple[int, int]
) -> str | None:
    """用 onnxruntime 以全零输入推理一次，输出必须是有限值。返回 onnxruntime 的版本；没有安装时返回 None。"""
    try:
        import onnxruntime as ort
    except ImportError:
        return None
    import numpy as np

    dtypes = {
        "tensor(float)": np.float32,
        "tensor(float16)": np.float16,
        "tensor(double)": np.float64,
    }
    try:
        session = ort.InferenceSession(path, providers=["CPUExecutionProvider"])
        feed = {}
        for item in session.get_inputs():
            shape = []
            for i, d in enumerate(item.shape):
                if isinstance(d, int) and d > 0:
                    shape.append(d)
                else:  # 动态维度：batch 为 1，高、宽为请求的尺寸
                    shape.append({2: size[0], 3: size[1]}.get(i, 1))
            feed[item.name] = np.zeros(shape, dtype=dtypes.get(item.type, np.float32))
        outputs = session.run(None, feed)
        for out in outputs:
            arr = np.asarray(out)
            if arr.dtype.kind == "f" and not bool(np.isfinite(arr).all()):
                raise RuntimeError("output contains NaN or Inf")
    except Exception as exc:
        raise ExportError(
            f"用 ONNX Runtime 验证导出的模型失败：{_clean_message(exc)}", summarize_exception(exc)
        ) from exc
    return str(getattr(ort, "__version__", "")) or "unknown"


def require_safe_load(node_version: str) -> None:
    """确认 ultralytics 会用安全加载读取模型文件：有 ultralytics.nn.tasks._SafeLoad、torch 支持
    （_SafeLoad.SUPPORTED，需要 PyTorch 2.6 及以上）且 ULTRALYTICS_SAFE_LOAD 已生效。否则 ultralytics 会
    静默退回普通加载（执行文件中的任意代码），所以转换失败（不可重试），提示升级训练节点。"""
    torch_version = str(getattr(sys.modules.get("torch"), "__version__", "")) or "unknown"
    try:
        tasks = importlib.import_module("ultralytics.nn.tasks")
    except Exception as exc:
        raise ExportError(
            f"训练节点无法导入 ultralytics.nn.tasks（{_clean_message(exc)}），"
            "请用 python -m trainmaster_trainer selftest 检查训练节点的环境",
            summarize_exception(exc, MAX_DETAIL),
        ) from exc
    safe = getattr(tasks, "_SafeLoad", None)
    if safe is None:
        reason = f"训练节点的 Ultralytics {node_version} 不支持安全加载模型文件"
    elif getattr(safe, "SUPPORTED", False) is not True:
        reason = (
            f"训练节点的 PyTorch {torch_version} 不支持 Ultralytics 的安全加载（需要 2.6 及以上）"
        )
    elif getattr(tasks, "SAFE_LOAD", False) is not True:
        reason = f"训练节点的 Ultralytics {node_version} 没有启用安全加载（{SAFE_LOAD_ENV}）"
    else:
        return
    raise ExportError(
        f"{reason}，为了安全不会加载上传的模型文件。请升级训练节点"
        "（镜像或 requirements.txt 中的 ultralytics 与 torch）"
    )


def run_export(spec: ExportSpec) -> dict[str, Any]:
    """子进程中执行：加载模型、导出 ONNX、核对并验证。返回 {"ok": …} 结构，不抛出异常。"""
    start = time.monotonic()
    try:
        return _run_export(spec, start)
    except ExportError as exc:
        return {"ok": False, "error": exc.message, "detail": exc.detail}
    except Exception as exc:  # 防御：未预料的错误也给出说明
        return {
            "ok": False,
            "error": f"转换出错：{_clean_message(exc)}",
            "detail": summarize_exception(exc, MAX_DETAIL),
        }


def _run_export(spec: ExportSpec, start: float) -> dict[str, Any]:
    if not Path(spec.weights).is_file():
        raise ExportError(f"找不到模型文件：{spec.weights}")
    try:
        import ultralytics
    except Exception as exc:  # ImportError、缺少 .so 时的 OSError 等
        raise ExportError(
            f"训练节点无法导入 Ultralytics（{_clean_message(exc)}），"
            "请用 python -m trainmaster_trainer selftest 检查训练节点的环境",
            summarize_exception(exc, MAX_DETAIL),
        ) from exc
    try:
        import onnx
    except Exception as exc:
        raise ExportError(
            f"训练节点缺少 ONNX 转换需要的 onnx（{_clean_message(exc)}），请升级训练节点"
            "（镜像或 requirements.txt 中的 onnx、onnxslim、onnxruntime）",
            summarize_exception(exc, MAX_DETAIL),
        ) from exc
    node_version = str(getattr(ultralytics, "__version__", "")) or "unknown"
    torch = sys.modules.get("torch")
    require_safe_load(node_version)

    try:
        model = ultralytics.YOLO(spec.weights, task=spec.task)
    except Exception as exc:
        raise ExportError(
            translate_load_error(exc, spec, node_version), summarize_exception(exc, MAX_DETAIL)
        ) from exc
    task = str(getattr(model, "task", "") or spec.task or "")
    end2end_head = has_end2end_head(model)
    postprocess = resolve_postprocess(spec, task, end2end_head)
    kwargs = _export_kwargs(spec, postprocess)
    print(
        f"export: {json.dumps(kwargs)} (task={task}, end2end_head={end2end_head})", file=sys.stderr
    )

    try:
        exported = model.export(**kwargs)
    except Exception as exc:
        raise ExportError(
            translate_export_error(exc, spec, spec.imgsz), summarize_exception(exc, MAX_DETAIL)
        ) from exc
    if not exported or not Path(str(exported)).is_file():
        raise ExportError(f"Ultralytics 没有生成 ONNX 文件（返回 {exported!r}）")
    path = str(exported)
    if spec.output and Path(spec.output).resolve() != Path(path).resolve():
        Path(spec.output).parent.mkdir(parents=True, exist_ok=True)
        shutil.move(path, spec.output)
        path = spec.output

    try:
        onnx.checker.check_model(path)
        described = describe_onnx(onnx, path)
    except Exception as exc:
        raise ExportError(
            f"导出的 ONNX 模型无效：{_clean_message(exc)}", summarize_exception(exc, MAX_DETAIL)
        ) from exc
    end2end, nms = check_onnx(described, spec, postprocess, end2end_head)
    ort_version = verify_onnx(path, described["inputs"], _input_size(spec, described["metadata"]))
    result = {
        "ultralytics_version": node_version,
        "torch_version": str(getattr(torch, "__version__", "")) or None,
        "onnx_version": str(getattr(onnx, "__version__", "")) or None,
        "onnxruntime_version": ort_version,
        "opset": described["opset"],
        "end2end": end2end,
        "nms": nms,
        "dynamic": spec.dynamic,
        "inputs": described["inputs"],
        "outputs": described["outputs"],
        "verified": ort_version is not None,
        "duration_ms": int((time.monotonic() - start) * 1000),
    }
    return {"ok": True, "onnx_path": path, "result": result}


def child_main(argv: Sequence[str] | None = None) -> int:
    """子进程入口：结果 JSON 写到 stdout，其它输出（ultralytics 的日志、C 扩展的打印）一律转到 stderr。"""
    parser = argparse.ArgumentParser(prog=f"python -m {CHILD_MODULE}")
    parser.add_argument("--spec", required=True, help="转换参数（JSON）")
    args = parser.parse_args(argv)
    os.environ.setdefault("YOLO_OFFLINE", "1")
    os.environ.setdefault("YOLO_AUTOINSTALL", "False")
    os.environ[SAFE_LOAD_ENV] = "1"  # 在导入 ultralytics 之前：它在导入时读取这个开关
    logging.basicConfig(stream=sys.stderr, level=logging.WARNING)

    sys.stdout.flush()
    json_out = os.fdopen(os.dup(1), "w", encoding="utf-8")
    os.dup2(2, 1)  # 文件描述符级别的重定向：C 扩展直接写 fd 1 的内容也不会混入 JSON
    with contextlib.redirect_stdout(sys.stderr):
        try:
            spec = ExportSpec.from_json(json.loads(args.spec))
        except ValueError as exc:
            result: dict[str, Any] = {
                "ok": False,
                "error": f"转换参数不合法：{exc}",
                "detail": None,
            }
        else:
            result = run_export(spec)
    json.dump(json_safe(result), json_out, ensure_ascii=False)
    json_out.write("\n")
    json_out.flush()
    return 0 if result.get("ok") else 1


# ---------------------------------------------------------------------------
# 父进程：运行子进程


def export_env(state_dir: Path, base: Mapping[str, str] | None = None) -> dict[str, str]:
    """转换子进程的环境变量。子进程加载的是用户上传的 pt 文件，所以只继承运行 Python 需要的变量
    （_EXPORT_ENV_NAMES、LC_*、YOLO_*；没有 TM_NODE_TOKEN 等节点配置与密钥），并打开 Ultralytics 的
    安全加载（ULTRALYTICS_SAFE_LOAD=1）。其余与环境检测相同（YOLO_OFFLINE、YOLO_CONFIG_DIR、PYTHONPATH），
    另外只用 CPU（不占用显存、不受正在进行的训练影响），不让 ultralytics 尝试自动安装依赖；默认输出
    ultralytics 的日志（失败时作为详情）。"""
    source = os.environ if base is None else base
    kept = {
        k: v
        for k, v in source.items()
        if k in _EXPORT_ENV_NAMES or k.startswith(_EXPORT_ENV_PREFIXES)
    }
    kept.setdefault("YOLO_VERBOSE", "True")
    env = child_env(state_dir, None, kept)
    env["CUDA_VISIBLE_DEVICES"] = ""
    env["YOLO_AUTOINSTALL"] = "False"
    env[SAFE_LOAD_ENV] = "1"
    return env


def _join_detail(*parts: str | None, limit: int = MAX_DETAIL) -> str | None:
    """把回溯与子进程输出拼成详情；每一部分各占一半上限，保留结尾。"""
    texts = [p.strip() for p in parts if p and p.strip()]
    if not texts:
        return None
    share = limit // len(texts)
    return "\n\n".join(_tail(t, share) or "" for t in texts)


class ExportProcess:
    """限时运行一次转换子进程。可从其它线程调用 cancel()：结束子进程所在的整个进程组。"""

    def __init__(
        self,
        *,
        state_dir: Path,
        timeout: float,
        command: Sequence[str] | None = None,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self.state_dir = state_dir
        self.timeout = timeout
        self.command = list(command) if command else [sys.executable, "-m", CHILD_MODULE]
        self.clock = clock
        self._lock = threading.Lock()
        self._proc: subprocess.Popen[bytes] | None = None
        self._cancelled = False

    def cancel(self) -> None:
        with self._lock:
            self._cancelled = True
            proc = self._proc
        if proc is not None:
            _kill(proc)

    def run(self, spec: ExportSpec, *, cwd: Path | None = None) -> dict[str, Any]:
        """运行子进程，返回 {"ok": True, "onnx_path", "result"} 或 {"ok": False, "error", "detail",
        "cancelled"}；任何失败都转换为中文说明，不抛出异常（Ctrl+C 除外，此时先结束子进程）。"""
        try:
            (self.state_dir / "ultralytics").mkdir(parents=True, exist_ok=True)
        except OSError as exc:
            log.debug("cannot create ultralytics config dir: %s", exc)
        start = self.clock()
        try:
            with self._lock:
                if self._cancelled:
                    return _failed("转换已取消", cancelled=True)
                self._proc = subprocess.Popen(
                    [*self.command, "--spec", json.dumps(spec.to_json(), ensure_ascii=False)],
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    env=export_env(self.state_dir),
                    cwd=cwd,
                    **procs.popen_kwargs(),  # 超时或停止时连同它启动的进程一起结束
                )
                procs.attach(self._proc)
            proc = self._proc
        except OSError as exc:
            return _failed(f"无法启动转换进程：{exc}")

        try:
            stdout, stderr = procs.communicate(proc, self.timeout)  # Windows 上可以被 Ctrl+C 打断
        except BaseException as exc:
            if not isinstance(exc, subprocess.TimeoutExpired):
                _kill(proc)  # Ctrl+C 等：不留下孤儿子进程
                raise
            _kill(proc)
            try:
                _, stderr = proc.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()  # 子进程的子进程仍占着管道时不再等待输出
                stderr = None
            if self._cancelled:
                return _failed("转换已取消", _tail(stderr, MAX_DETAIL), cancelled=True)
            return _failed(
                f"转换超时（{self.timeout:g} 秒内没有完成）。可以减小输入尺寸，"
                "或在训练节点上用 TM_EXPORT_TIMEOUT 延长时间",
                _tail(stderr, MAX_DETAIL),
            )
        finally:
            with self._lock:
                self._proc = None
        elapsed = self.clock() - start
        stderr_tail = _tail(stderr, MAX_DETAIL)

        if self._cancelled:
            return _failed("转换已取消", stderr_tail, cancelled=True)
        name = procs.signal_name(proc.returncode)
        if name is not None:
            return _failed(
                f"转换进程被信号 {name} 终止（可能是内存不足），用时 {elapsed:.0f} 秒", stderr_tail
            )
        crash = procs.windows_crash(proc.returncode)
        if crash is not None:
            return _failed(f"转换进程崩溃（{crash}），用时 {elapsed:.0f} 秒", stderr_tail)
        lines = [line for line in stdout.decode("utf-8", "replace").splitlines() if line.strip()]
        try:
            data = json.loads(lines[-1])
        except (IndexError, ValueError):
            data = None
        if not isinstance(data, dict) or not isinstance(data.get("ok"), bool):
            if proc.returncode != 0:
                return _failed(f"转换进程异常退出（退出码 {proc.returncode}）", stderr_tail)
            detail = _join_detail(f"stdout: {_tail(stdout, 500)}", stderr_tail)
            return _failed("转换进程的输出无法解析", detail)
        if not data["ok"]:
            error = data.get("error") if isinstance(data.get("error"), str) else None
            detail = data.get("detail") if isinstance(data.get("detail"), str) else None
            return _failed(error or "转换失败（没有说明）", _join_detail(detail, stderr_tail))
        if not isinstance(data.get("onnx_path"), str) or not isinstance(data.get("result"), dict):
            return _failed("转换进程的输出不完整（缺少 onnx_path 或 result）", stderr_tail)
        if stderr_tail:
            log.debug("model export stderr:\n%s", _tail(stderr, 2000))
        return {"ok": True, "onnx_path": data["onnx_path"], "result": data["result"]}


def _failed(error: str, detail: str | None = None, *, cancelled: bool = False) -> dict[str, Any]:
    return {"ok": False, "error": error, "detail": detail, "cancelled": cancelled}


# ---------------------------------------------------------------------------
# 父进程：执行平台的转换任务


class TaskAbandoned(Exception):
    """平台上的任务已被删除、不再属于本节点或已经结束（404 / 409）：结束工作，不再上报。"""


class TaskFailed(Exception):
    """任务失败，向平台上报 fail。retryable：重新排队（平台在尝试次数用完之前重试）。"""

    def __init__(self, message: str, detail: str | None = None, *, retryable: bool = False) -> None:
        super().__init__(message)
        self.message = message
        self.detail = detail
        self.retryable = retryable


@dataclass(frozen=True)
class ExportTaskInfo:
    """平台下发的 model_export 任务中本节点用到的字段（见 docs/architecture.md 第 9 节）。"""

    id: int
    model_id: int | None
    yolo_version: str | None
    task: str | None
    size_bytes: int
    sha256: str
    saved_ultralytics_version: str | None
    imgsz: tuple[int, int]
    dynamic: bool
    postprocess: str
    opset: int | None
    input_url: str
    progress_url: str
    output_url: str
    complete_url: str
    fail_url: str

    @classmethod
    def parse(cls, task: Mapping[str, Any]) -> ExportTaskInfo:
        """校验任务的结构；不合法时抛出 ValueError（说明哪个字段不对）。"""

        def positive(value: Any) -> bool:
            return isinstance(value, int) and not isinstance(value, bool) and value > 0

        def url(key: str) -> str:
            value = task.get(key)
            if not isinstance(value, str) or not value.startswith("/") or "//" in value:
                raise ValueError(key)
            return value

        def opt_str(container: Mapping[str, Any], key: str) -> str | None:
            value = container.get(key)
            return value if isinstance(value, str) and value else None

        if not positive(task.get("id")):
            raise ValueError("id")
        model = task.get("model")
        if not isinstance(model, dict):
            raise ValueError("model")
        if not positive(model.get("size_bytes")):
            raise ValueError("model.size_bytes")
        sha = model.get("sha256")
        if not isinstance(sha, str) or not _SHA256.fullmatch(sha):
            raise ValueError("model.sha256")
        info = model.get("info") if isinstance(model.get("info"), dict) else {}
        options = task.get("options")
        if not isinstance(options, dict):
            raise ValueError("options")
        imgsz = options.get("imgsz")
        if not isinstance(imgsz, list) or len(imgsz) != 2 or not all(positive(v) for v in imgsz):
            raise ValueError("options.imgsz")
        dynamic = options.get("dynamic", False)
        if not isinstance(dynamic, bool):
            raise ValueError("options.dynamic")
        postprocess = options.get("postprocess")
        if postprocess not in POSTPROCESS:
            raise ValueError("options.postprocess")
        opset = options.get("opset")
        if opset is not None and not positive(opset):
            raise ValueError("options.opset")
        return cls(
            id=task["id"],
            model_id=model.get("id") if positive(model.get("id")) else None,
            yolo_version=opt_str(model, "yolo_version"),
            task=opt_str(model, "task"),
            size_bytes=model["size_bytes"],
            sha256=sha.lower(),
            saved_ultralytics_version=opt_str(info, "ultralytics_version"),
            imgsz=(imgsz[0], imgsz[1]),
            dynamic=dynamic,
            postprocess=postprocess,
            opset=opset,
            input_url=url("input_url"),
            progress_url=url("progress_url"),
            output_url=url("output_url"),
            complete_url=url("complete_url"),
            fail_url=url("fail_url"),
        )


def format_bytes(n: int | float) -> str:
    value = float(n)
    for unit in ("B", "KB", "MB"):
        if value < 1024:
            return f"{value:.0f} {unit}" if unit == "B" else f"{value:.1f} {unit}"
        value /= 1024
    return f"{value:.1f} GB"


def file_sha256(path: Path, abort: Callable[[], bool] | None = None) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            if abort is not None and abort():
                raise TransportError("aborted")
            digest.update(chunk)
    return digest.hexdigest()


def verify_download(got: Transfer, size_bytes: int, sha256: str) -> None:
    """下载的模型文件与任务中的大小、sha256（小写）核对；不一致（传输出错）时抛出可重试的 TaskFailed。
    模型转换与审图识别共用。"""
    if got.size != size_bytes:
        raise TaskFailed(
            f"下载的模型文件大小不一致（收到 {got.size} 字节，应为 {size_bytes} 字节）",
            retryable=True,
        )
    if got.sha256 != sha256:
        raise TaskFailed(
            "下载的模型文件校验失败（sha256 不一致），可能是传输出错",
            f"expected {sha256}, got {got.sha256}",
            retryable=True,
        )


def fail_url_of(task: Mapping[str, Any]) -> str | None:
    """任务数据不完整时仍尽量找到 fail_url，以便向平台说明原因。"""
    value = task.get("fail_url")
    return value if isinstance(value, str) and value.startswith("/") and "//" not in value else None


def report_failure(
    client: Client,
    node_key: str,
    url: str,
    failure: TaskFailed,
    *,
    attempts: int = len(FAIL_RETRY_DELAYS) + 1,
    timeout: float | None = None,
    wait: Callable[[float], bool] | None = None,
) -> bool:
    """POST fail；网络错误、5xx 时重试几次。返回平台是否收到。任务已不属于本节点（404 / 409）时不再重试。"""
    payload = {
        "error": failure.message[:MAX_ERROR],
        "detail": _tail(failure.detail, MAX_DETAIL) or "",
        "retryable": failure.retryable,
    }
    last: Exception | None = None
    for attempt in range(attempts):
        try:
            client.post(url, payload, headers={"X-Node-Key": node_key}, timeout=timeout)
            return True
        except ApiError as exc:
            if exc.status < 500:
                log.debug("platform rejected the failure report (%s): %s", url, exc)
                return False
            last = exc
        except TransportError as exc:
            last = exc
        if attempt + 1 < attempts:
            delay = FAIL_RETRY_DELAYS[min(attempt, len(FAIL_RETRY_DELAYS) - 1)]
            if wait is not None and wait(delay):
                break
    log.warning(
        "cannot report the failure to the platform (%s): %s; the task is queued again "
        "when its lease expires",
        url,
        last,
    )
    return False


class ModelExportTask:
    """执行一个 model_export 任务。run() 在任务线程中调用；cancel() 可从其它线程调用（节点停止）。"""

    def __init__(
        self,
        task: Mapping[str, Any],
        *,
        client: Client,
        node_key: str,
        state_dir: Path,
        node_name: str,
        timeout: float,
        command: Sequence[str] | None = None,
        clock: Callable[[], float] = time.monotonic,
        progress_interval: float = PROGRESS_INTERVAL,
    ) -> None:
        self.raw = task
        self.client = client
        self.node_key = node_key
        self.state_dir = state_dir
        self.node_name = node_name
        self.clock = clock
        self.progress_interval = progress_interval
        self.process = ExportProcess(state_dir=state_dir, timeout=timeout, command=command)
        self.info: ExportTaskInfo | None = None
        self.work_dir: Path | None = None
        self.stage = "downloading"
        self._headers = {"X-Node-Key": node_key}
        self._stopping = threading.Event()
        self._finished = threading.Event()
        self._abandoned: Exception | None = None
        self._progress_lock = threading.Lock()

    # ---- 取消 ----

    def cancel(self) -> None:
        """节点停止：结束子进程，中止传输；run() 随后向平台上报可重试的失败（任务重新排队）。"""
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
            raise TaskFailed("训练节点正在停止，转换已中断", retryable=True)

    def _api_error(self, exc: ApiError, what: str) -> Exception:
        """任务调用的 HTTP 错误：404 / 409 放弃；5xx（507 平台磁盘已满除外）与 401 可重试；
        其余（400、413、422、507 …）不可重试。"""
        if exc.status in (404, 409):
            return TaskAbandoned(f"{what}: {exc}")
        retryable = (exc.status >= 500 and exc.status != 507) or exc.status == 401
        return TaskFailed(f"{what}失败：{exc.message}", str(exc), retryable=retryable)

    # ---- 执行 ----

    def run(self) -> str:
        """执行任务，返回结果：succeeded / failed / abandoned / invalid。不抛出异常。"""
        try:
            self.info = info = ExportTaskInfo.parse(self.raw)
        except ValueError as exc:
            return self._invalid(str(exc))
        started = self.clock()
        options = options_text(info.imgsz, info.dynamic, info.postprocess, info.opset)
        log.info(
            "model export %s started: model=%s (%s %s, %d bytes) %s",
            info.id,
            info.model_id,
            info.yolo_version,
            info.task,
            info.size_bytes,
            options,
        )
        keeper = threading.Thread(
            target=self._keep_lease, name=f"model-export-{info.id}-lease", daemon=True
        )
        keeper.start()
        try:
            result = self._execute(info)
        except TaskAbandoned as exc:
            log.info(
                "model export %s abandoned after %.1fs: it was deleted or is no longer assigned "
                "to this node (%s)",
                info.id,
                self.clock() - started,
                exc,
            )
            return "abandoned"
        except TaskFailed as failure:
            if self._abandoned is not None:
                log.info("model export %s abandoned: %s", info.id, self._abandoned)
                return "abandoned"
            log.warning(
                "model export %s failed after %.1fs (retryable=%s, %s): %s",
                info.id,
                self.clock() - started,
                failure.retryable,
                options,
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
        except Exception as exc:  # 防御：意外的错误也上报（可重试），任务不必等到租约过期
            log.exception("model export %s failed unexpectedly", info.id)
            if self._abandoned is not None:
                return "abandoned"
            failure = TaskFailed(
                f"训练节点执行转换时出错：{_clean_message(exc)}",
                summarize_exception(exc, MAX_DETAIL),
                retryable=True,
            )
            self._finished.set()
            report_failure(self.client, self.node_key, info.fail_url, failure, attempts=1)
            return "failed"
        finally:
            self._finished.set()
            # 续租线程可能正在上报进度；停止时不等它（守护线程）
            keeper.join(timeout=2 if self._stopping.is_set() else 15)
            self._cleanup()
        log.info(
            "model export %s succeeded in %.1fs: %d bytes, opset %s, %s, verified=%s",
            info.id,
            self.clock() - started,
            result["size"],
            result["opset"],
            options,
            result["verified"],
        )
        return "succeeded"

    def _invalid(self, field: str) -> str:
        task_id = self.raw.get("id")
        log.warning("model export task %r is malformed (field %s), not running it", task_id, field)
        url = fail_url_of(self.raw)
        if url is not None:
            failure = TaskFailed(
                f"训练节点无法识别平台下发的转换任务（字段 {field} 缺失或不合法），"
                f"请确认平台与训练节点（{__version__}）的版本匹配"
            )
            report_failure(self.client, self.node_key, url, failure, attempts=1)
        return "invalid"

    def _execute(self, info: ExportTaskInfo) -> dict[str, Any]:
        work = self._prepare_work_dir(info)
        self._check_disk(work, info.size_bytes)

        self._report_stage("downloading")
        weights = work / "model.pt"
        self._download(info, weights)

        self._report_stage("converting")
        spec = ExportSpec(
            weights=str(weights),
            output=str(work / "model.onnx"),
            task=info.task,
            yolo_version=info.yolo_version,
            imgsz=info.imgsz,
            dynamic=info.dynamic,
            postprocess=info.postprocess,
            opset=info.opset,
            saved_ultralytics_version=info.saved_ultralytics_version,
        )
        outcome = self.process.run(spec, cwd=work)
        self._check_interrupted()
        if not outcome["ok"]:
            raise TaskFailed(outcome["error"], outcome.get("detail"))
        onnx_path = Path(outcome["onnx_path"])
        try:
            onnx_path.resolve().relative_to(work.resolve())
            size = onnx_path.stat().st_size
        except (ValueError, OSError) as exc:
            raise TaskFailed(f"转换进程没有在工作目录中生成 ONNX 文件（{onnx_path}）") from exc
        if size <= 0:
            raise TaskFailed("转换生成的 ONNX 文件是空的")
        if size > MAX_OUTPUT_BYTES:
            raise TaskFailed(
                f"转换生成的 ONNX 文件太大（{format_bytes(size)}），平台最多接受 {format_bytes(MAX_OUTPUT_BYTES)}"
            )

        self._start_upload(size)
        result = dict(outcome["result"], node_name=self.node_name)
        self._upload(info, onnx_path, size)
        try:
            self.client.post(info.complete_url, {"result": result}, headers=self._headers)
        except ApiError as exc:
            raise self._api_error(exc, "完成转换") from None
        except TransportError as exc:
            self._check_interrupted()
            raise TaskFailed(f"无法通知平台转换完成：{exc}", retryable=True) from None
        return {"size": size, "opset": result.get("opset"), "verified": result.get("verified")}

    def _prepare_work_dir(self, info: ExportTaskInfo) -> Path:
        work = self.state_dir / "work" / f"model-export-{info.id}"
        try:
            if work.exists():
                shutil.rmtree(work)  # 上次运行的残留（如进程崩溃）
            work.mkdir(parents=True)
        except OSError as exc:
            raise TaskFailed(f"无法创建工作目录 {work}：{exc}", retryable=True) from None
        self.work_dir = work
        return work

    def _check_disk(self, work: Path, size: int) -> None:
        need = DISK_FACTOR * size
        try:
            free = shutil.disk_usage(work).free
        except OSError as exc:
            log.debug("disk usage of %s unavailable: %s", work, exc)
            return
        if free < need:
            raise TaskFailed(
                f"训练节点的磁盘空间不足：转换需要约 {format_bytes(need)}，"
                f"{work.parent} 所在的磁盘只剩 {format_bytes(free)}",
                retryable=True,
            )

    def _download(self, info: ExportTaskInfo, dest: Path) -> None:
        try:
            got = self.client.download(
                info.input_url,
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

    def _upload(self, info: ExportTaskInfo, path: Path, size: int) -> None:
        try:
            sha = file_sha256(path, self._interrupted)
            resp = self.client.put_file(
                info.output_url,
                path,
                headers={**self._headers, "X-Content-Sha256": sha},
                timeout=TRANSFER_TIMEOUT,
                abort=self._interrupted,
            )
        except ApiError as exc:
            if exc.status == 422:  # 长度或 sha256 不一致：传输出错，可以重试
                raise TaskFailed(
                    f"上传转换结果失败：{exc.message}", str(exc), retryable=True
                ) from None
            raise self._api_error(exc, "上传转换结果") from None
        except TransportError as exc:
            self._check_interrupted()
            self._recheck_upload()
            raise TaskFailed(f"上传转换结果失败：{exc}", retryable=True) from None
        except OSError as exc:
            raise TaskFailed(f"无法读取转换结果 {path}：{exc}") from None
        self._check_interrupted()
        received = resp.get("received_bytes")
        if received != size:
            raise TaskFailed(
                f"平台收到的转换结果大小不一致（{received!r} 字节，应为 {size} 字节）",
                retryable=True,
            )

    # ---- 进度与租约 ----

    def _report_stage(self, stage: str) -> None:
        """进入新阶段：立即上报（续租）。"""
        self._check_interrupted()
        self.stage = stage
        self._progress()
        self._check_interrupted()

    def _start_upload(self, size: int) -> None:
        """进入上传阶段：上报进度时带上文件大小（预检）。平台在读取上传内容之前就会拒绝（任务已被删除或
        不属于本节点、文件太大、平台磁盘空间不足），拒绝后断开连接，正在上传较大文件的节点只能看到连接
        被重置；所以先问一次，平台做与上传相同的检查：404 / 409 放弃，413 / 507 不可重试的失败（平台的
        说明）。不认识 size_bytes 的旧版平台照常续租；其它错误与平时的进度上报一样只记录。"""
        self._check_interrupted()
        self.stage = "uploading"
        self._raise_if_refused(self._progress(size_bytes=size))

    def _recheck_upload(self) -> None:
        """上传时连接中断：可能是平台没有读取内容就拒绝了上传。再上报一次进度（不带大小）来区分：
        404 / 409 放弃，507 不可重试的失败；成功或其它错误时由调用方按网络错误处理（可重试）。"""
        self._raise_if_refused(self._progress())

    def _raise_if_refused(self, error: ApiError | None) -> None:
        """上传阶段的进度上报被拒绝时结束任务：404 / 409 已在 _progress 中放弃，413 / 507 平台不会接收
        这个文件（重试也一样），不可重试的失败。"""
        self._check_interrupted()
        if error is not None and error.status in (413, 507):
            raise self._api_error(error, "上传转换结果")

    def _progress(self, size_bytes: int | None = None) -> ApiError | None:
        """POST progress，返回平台返回的错误（成功或网络错误时为 None）。404 / 409（任务被删除、已不属于
        本节点）时放弃任务；其它错误只记录（租约有 60 秒，之后的上报会续上）。size_bytes 见 _start_upload。"""
        assert self.info is not None
        payload: dict[str, Any] = {"stage": self.stage}
        if size_bytes is not None:
            payload["size_bytes"] = size_bytes
        with self._progress_lock:
            if self._abandoned is not None:
                return None
            try:
                self.client.post(self.info.progress_url, payload, headers=self._headers)
            except ApiError as exc:
                if exc.status in (404, 409):
                    self._abandon(exc)
                else:
                    log.debug("model export %s: progress report failed: %s", self.info.id, exc)
                return exc
            except TransportError as exc:
                log.debug("model export %s: progress report failed: %s", self.info.id, exc)
        return None

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


def clean_work_dirs(state_dir: Path, patterns: Sequence[str] = ("model-export-*",)) -> None:
    """删除之前运行（进程崩溃、被强制结束）留下的任务工作目录：状态目录的 work/ 中与 patterns 匹配的
    条目（默认为转换的工作目录；任务线程启动时传入每类任务的工作目录，见 tasks.WORK_DIR_PATTERNS）。"""
    root = state_dir / "work"
    try:
        entries = [entry for pattern in patterns for entry in root.glob(pattern)]
    except OSError:
        return
    for entry in entries:
        try:
            if entry.is_dir() and not entry.is_symlink():
                shutil.rmtree(entry)
            else:
                entry.unlink()
            log.debug("removed stale work directory %s", entry)
        except OSError as exc:
            log.warning("cannot remove stale work directory %s: %s", entry, exc)


if __name__ == "__main__":
    sys.exit(child_main())
