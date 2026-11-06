"""review_inference.py：识别子进程（假 ultralytics / torch / numpy / cv2 / Pillow，见 tests/inference_fakes.py）
的图片读取、框的提取与错误翻译，子进程的 JSON lines 协议，父进程对子进程的处理（超时、取消、崩溃、
输出），结果的整理与攒批，以及命令行 infer。任务线程对假平台执行审图识别任务的测试在
test_review_tasks.py 中。"""

from __future__ import annotations

import io
import json
import os
import struct
import subprocess
import sys
import threading
import time
from pathlib import Path
from typing import Any

import inference_fakes
import pytest
from fakes import TRAINER_DIR, posix_only, windows_only
from inference_fakes import FakeBoxes, fake_image
from task_harness import gone, script

from trainmaster_trainer import review_inference as ri
from trainmaster_trainer.review_inference import (
    InferenceError,
    InferenceProcess,
    InferenceSpec,
    InferenceTaskInfo,
    ResultBatch,
    Session,
    class_names,
    clean_class_name,
    extract_boxes,
    fit_result,
    neutralize_tiff_orientation,
    read_image,
    translate_predict_error,
    translate_reply,
)

BUS = [[3.83, 229.36, 796.19, 728.41, 0.94015, 2], [671.02, 394.83, 809.81, 878.71, 0.88822, 0]]


def install(monkeypatch, tmp_path: Path | None = None, **config: Any):
    """把假模块放进 sys.modules（与 child_main 一样先打开安全加载），返回假的 ultralytics（calls 记录调用）。"""
    monkeypatch.setenv(ri.SAFE_LOAD_ENV, "1")
    if tmp_path is not None:
        config.setdefault("calls_file", str(tmp_path / "calls.jsonl"))
    monkeypatch.setitem(sys.modules, "torch", inference_fakes.make_torch(**config))
    ultralytics = inference_fakes.make_ultralytics(**config)
    monkeypatch.setitem(sys.modules, "ultralytics", ultralytics)
    for name, module in ultralytics.submodules.items():
        monkeypatch.setitem(sys.modules, name, module)
    monkeypatch.setitem(sys.modules, "numpy", inference_fakes.make_numpy(**config))
    monkeypatch.setitem(sys.modules, "cv2", inference_fakes.make_cv2(**config))
    pil = inference_fakes.make_pil(**config)
    monkeypatch.setitem(sys.modules, "PIL", pil)
    monkeypatch.setitem(sys.modules, "PIL.Image", pil.Image)
    monkeypatch.setattr(ri, "_pillow_open", None)
    return ultralytics


def calls(tmp_path: Path) -> list[dict[str, Any]]:
    path = tmp_path / "calls.jsonl"
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]


def weights(tmp_path: Path) -> str:
    path = tmp_path / "model.pt"
    path.write_bytes(b"PK\x03\x04 fake checkpoint")
    return str(path)


def image_file(tmp_path: Path, name: str, data: bytes) -> str:
    path = tmp_path / name
    path.write_bytes(data)
    return str(path)


# ---- 参数 ----


def test_spec_round_trip_and_defaults():
    s = InferenceSpec(weights="/w.pt", task="obb", conf=0.5, iou=0.6, imgsz=(480, 640), max_det=10)
    assert InferenceSpec.from_json(json.loads(json.dumps(s.to_json()))) == s
    d = InferenceSpec.from_json({"weights": "/w.pt"})
    assert (d.conf, d.iou, d.imgsz, d.max_det, d.device) == (0.25, 0.7, None, 300, None)
    assert InferenceSpec.from_json({"weights": "/w.pt", "imgsz": 320}).imgsz == (320, 320)
    assert InferenceSpec.from_json({"weights": "/w.pt", "device": "cuda:1"}).device == "cuda:1"


@pytest.mark.parametrize(
    "data",
    [
        [], {}, {"weights": ""}, {"weights": "x", "conf": 0}, {"weights": "x", "conf": 1.5},
        {"weights": "x", "conf": True}, {"weights": "x", "iou": "0.7"}, {"weights": "x", "imgsz": [640]},
        {"weights": "x", "imgsz": [0, 640]}, {"weights": "x", "max_det": 0}, {"weights": "x", "max_det": 1001},
        {"weights": "x", "task": 3}, {"weights": "x", "device": "cuda\n"}, {"weights": "x", "conf": float("nan")},
    ],
)  # fmt: skip
def test_spec_validation(data):
    with pytest.raises(ValueError):
        InferenceSpec.from_json(data)


# ---- 读取图片（按存储的像素方向）----


def test_read_image_with_opencv_ignores_exif_orientation(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path)
    image = read_image(image_file(tmp_path, "a.jpg", fake_image(810, 1080, "bus")))
    assert image.shape == (1080, 810, 3) and image.tag == "bus"
    # IMREAD_COLOR | IMREAD_IGNORE_ORIENTATION：不按 EXIF 方向旋转，与平台标注的坐标一致
    assert calls(tmp_path) == [{"call": "imdecode", "flags": 1 | 128}]


def test_read_image_falls_back_to_pillow(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path)
    image = read_image(image_file(tmp_path, "a.gif", fake_image(320, 240, "anim", fmt="GIF89a")))
    assert image.shape == (240, 320, 3) and image.tag == "anim"
    assert [c["call"] for c in calls(tmp_path)] == ["imdecode", "Image.open", "convert", "cvtColor"]
    assert calls(tmp_path)[2]["mode"] == "RGB" and calls(tmp_path)[3]["code"] == 4  # RGB → BGR


def test_read_image_when_opencv_raises(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path, cv2_raises=True)
    image = read_image(image_file(tmp_path, "a.gif", fake_image(64, 32, fmt="GIF89a")))
    assert image.shape == (32, 64, 3)


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b"garbage", "文件已损坏，或不是支持的图片格式"),
        (
            b"TRUNCATED",
            "文件已损坏或不完整：OSError: image file is truncated (3 bytes not processed)",
        ),
        (b"", "文件是空的"),
        (None, "文件不存在"),
    ],
)
def test_read_image_errors(monkeypatch, tmp_path, data, message):
    install(monkeypatch, tmp_path)
    path = str(tmp_path / "missing.jpg") if data is None else image_file(tmp_path, "x.jpg", data)
    with pytest.raises(ri.ImageReadError) as info:
        read_image(path)
    assert info.value.message == message


def test_read_image_uses_pillow_open_from_before_ultralytics(monkeypatch, tmp_path):
    # Ultralytics 导入时替换 PIL.Image.open（无法识别时安装 pi-heif）；读取图片用原来的函数
    install(monkeypatch, tmp_path)
    original = sys.modules["PIL"].Image.open
    ri.remember_pillow_open()

    def patched(fp: Any) -> Any:
        raise ModuleNotFoundError("No module named 'pi_heif'")

    monkeypatch.setattr(sys.modules["PIL"].Image, "open", patched)
    assert ri._pillow_open is original
    with pytest.raises(ri.ImageReadError) as info:
        read_image(image_file(tmp_path, "x.jpg", b"garbage"))
    assert info.value.message == "文件已损坏，或不是支持的图片格式"


def tiff(orientation: int | None, *, endian: str = "<", big: bool = False, kind: int = 3) -> bytes:
    """一个最小的 TIFF 头与第一个 IFD（宽、高与可选的 Orientation 标签），后面补一些像素字节。"""
    order = b"II" if endian == "<" else b"MM"
    entries = [(256, 3, 1, 810), (257, 3, 1, 1080)]
    if orientation is not None:
        entries.append((274, kind, 1, orientation))
    if big:
        head = order + struct.pack(endian + "HHHQ", 43, 8, 0, 16)
        ifd = struct.pack(endian + "Q", len(entries))
        for tag, typ, count, value in entries:
            ifd += (
                struct.pack(endian + "HHQ", tag, typ, count)
                + struct.pack(endian + "H", value)
                + b"\0" * 6
            )
        ifd += struct.pack(endian + "Q", 0)
    else:
        head = order + struct.pack(endian + "HI", 42, 8)
        ifd = struct.pack(endian + "H", len(entries))
        for tag, typ, count, value in entries:
            ifd += (
                struct.pack(endian + "HHI", tag, typ, count)
                + struct.pack(endian + "H", value)
                + b"\0\0"
            )
        ifd += struct.pack(endian + "I", 0)
    return head + ifd + b"\x7f" * 64


@pytest.mark.parametrize(("endian", "big"), [("<", False), (">", False), ("<", True), (">", True)])
@pytest.mark.parametrize("orientation", [2, 3, 5, 6, 8])
def test_tiff_orientation_is_neutralized(endian, big, orientation):
    # OpenCV 与 Pillow 读取 TIFF 时都按 Orientation 旋转（IMREAD_IGNORE_ORIENTATION 不管 TIFF），
    # 平台按存储的像素标注：识别前把标签改为 1，其余字节不变。
    data = tiff(orientation, endian=endian, big=big)
    patched = neutralize_tiff_orientation(data)
    assert patched is not None and len(patched) == len(data)
    assert patched == tiff(1, endian=endian, big=big)


@pytest.mark.parametrize(
    "data",
    [
        tiff(1),
        tiff(None),
        tiff(6, kind=4),
        tiff(6)[:20],
        b"\xff\xd8\xff\xe0" + b"\0" * 64,
        b"II",
        b"",
    ],
)
def test_tiff_orientation_left_alone(data):
    # 已经是 1、没有标签、类型不对、文件不完整、不是 TIFF：不修改
    assert neutralize_tiff_orientation(data) is None


def test_read_image_decodes_neutralized_tiff(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path)
    seen: list[bytes] = []
    cv2 = sys.modules["cv2"]
    monkeypatch.setattr(cv2, "imdecode", lambda buf, flags: seen.append(buf.data) or None)
    with pytest.raises(ri.ImageReadError):
        read_image(image_file(tmp_path, "a.tif", tiff(6, endian=">")))
    assert seen == [tiff(1, endian=">")]


# ---- 识别结果 ----


def test_extract_boxes_clips_and_rounds():
    result = type("R", (), {"boxes": FakeBoxes([[-3.5, 10.123456, 900.0, 50.0, 0.912345, 2.0]])})()
    assert extract_boxes(result, 810, 1080) == [[0.0, 10.12, 810.0, 50.0, 0.9123, 2]]


def test_extract_boxes_of_obb_models_uses_the_axis_aligned_hull():
    rows = [[100.0, 20.0, 300.0, 1200.0, 0.5, 1]]
    result = type("R", (), {"boxes": None, "obb": FakeBoxes(rows)})()
    assert extract_boxes(result, 810, 1080) == [[100.0, 20.0, 300.0, 1080.0, 0.5, 1]]


def test_extract_boxes_without_boxes():
    assert extract_boxes(type("R", (), {"boxes": None, "obb": None})(), 10, 10) == []


# ---- 子进程中的模型 ----


def test_session_loads_warms_up_and_infers(monkeypatch, tmp_path):
    ultralytics = install(monkeypatch, tmp_path, boxes={"bus": BUS})
    spec = InferenceSpec(weights=weights(tmp_path), task="detect", imgsz=(480, 640), max_det=50)
    session = Session.load(spec)
    assert session.ready() == {
        "ready": True,
        "device": "cpu",
        "device_name": None,
        "task": "detect",
        "names": {"0": "person", "1": "bicycle", "2": "car"},
        "versions": {"ultralytics": "8.4.198", "torch": "2.14.1+cpu", "opencv": "4.12.0", "numpy": "2.3.4"},
        "imgsz": [480, 640],
        "warmup_ms": session.warmup_ms,
    }  # fmt: skip
    assert ultralytics.calls[0] == {
        "call": "YOLO",
        "weights": spec.weights,
        "task": "detect",
        "safe_load": True,  # 用安全加载读取上传的 pt
    }
    kwargs = {
        "conf": 0.25, "iou": 0.7, "max_det": 50, "device": "cpu", "verbose": False, "imgsz": [480, 640],
    }  # fmt: skip
    # 预热：输入尺寸的全零图片
    assert ultralytics.calls[1] == {
        "call": "predict",
        "tag": "warmup",
        "shape": [480, 640, 3],
        **kwargs,
    }
    reply = session.infer(7, image_file(tmp_path, "bus.jpg", fake_image(810, 1080, "bus")))
    assert reply == {
        "id": 7,
        "ok": True,
        "width": 810,
        "height": 1080,
        "boxes": [
            [3.83, 229.36, 796.19, 728.41, 0.9402, 2],
            [671.02, 394.83, 809.81, 878.71, 0.8882, 0],
        ],
    }
    assert ultralytics.calls[2] == {
        "call": "predict",
        "tag": "bus",
        "shape": [1080, 810, 3],
        **kwargs,
    }


def test_session_uses_the_first_gpu(monkeypatch, tmp_path):
    ultralytics = install(monkeypatch, tmp_path, cuda=True, device_name="NVIDIA GeForce RTX 4090")
    session = Session.load(InferenceSpec(weights=weights(tmp_path)))
    assert session.device == "cuda:0"
    assert session.ready()["device_name"] == "NVIDIA GeForce RTX 4090"
    assert ultralytics.calls[1]["device"] == "cuda:0"
    # 没有指定输入尺寸时用模型训练时的尺寸（不传 imgsz），预热用 640×640
    assert "imgsz" not in ultralytics.calls[1] and ultralytics.calls[1]["shape"] == [640, 640, 3]


def test_session_device_can_be_chosen(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path, cuda=True)
    assert Session.load(InferenceSpec(weights=weights(tmp_path), device="cpu")).device == "cpu"


def test_names_of_a_list(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path, names=["划痕", "凹陷"])
    session = Session.load(InferenceSpec(weights=weights(tmp_path)))
    assert session.ready()["names"] == {"0": "划痕", "1": "凹陷"}


def test_obb_model(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path, task="obb", default_boxes=[[-10, 5, 50, 60, 0.7, 1]])
    session = Session.load(InferenceSpec(weights=weights(tmp_path), task="obb"))
    reply = session.infer(1, image_file(tmp_path, "a.jpg", fake_image(40, 100)))
    assert reply["boxes"] == [[0.0, 5.0, 40.0, 60.0, 0.7, 1]]


@pytest.mark.parametrize(
    ("task", "message"),
    [("classify", "分类模型没有检测框，不能用于审图"), ("semantic", "语义分割模型没有检测框，不能用于审图"),
     ("depth", "深度估计模型没有检测框，不能用于审图")],
)  # fmt: skip
def test_models_without_boxes_are_refused(monkeypatch, tmp_path, task, message):
    ultralytics = install(monkeypatch, tmp_path, task=task)
    with pytest.raises(InferenceError) as info:
        Session.load(InferenceSpec(weights=weights(tmp_path)))
    assert info.value.message == message
    assert [c["call"] for c in ultralytics.calls] == ["YOLO"]  # 没有预热


@pytest.mark.parametrize(
    ("config", "message"),
    [
        (
            {"safe_load": False},
            "训练节点的 Ultralytics 8.4.198 没有启用安全加载（ULTRALYTICS_SAFE_LOAD）",
        ),
        ({"safe_load_class": False}, "训练节点的 Ultralytics 8.4.198 不支持安全加载模型文件"),
    ],
)
def test_refuses_to_load_without_safe_load(monkeypatch, tmp_path, config, message):
    ultralytics = install(monkeypatch, tmp_path, **config)
    with pytest.raises(InferenceError) as info:
        Session.load(InferenceSpec(weights=weights(tmp_path)))
    assert info.value.message.startswith(message + "，为了安全不会加载上传的模型文件")
    assert ultralytics.calls == []


@pytest.mark.parametrize(
    ("error", "expected"),
    [
        (["AttributeError", "Can't get attribute 'C3k2' on <module 'ultralytics.nn.modules.block'>"],
         "训练节点的 Ultralytics 8.4.198 无法加载该模型（YOLO11 需要 8.3.0 及以上"),
        (["RuntimeError", "PytorchStreamReader failed reading zip archive"], "模型文件已损坏或不完整"),
        (["MemoryError", ""], "训练节点内存不足，无法加载该模型"),
    ],
)  # fmt: skip
def test_load_errors_are_translated(monkeypatch, tmp_path, error, expected):
    install(monkeypatch, tmp_path, load_error=error)
    with pytest.raises(InferenceError) as info:
        Session.load(InferenceSpec(weights=weights(tmp_path), yolo_version="yolo11"))
    assert info.value.message.startswith(expected), info.value.message
    assert "Traceback" in info.value.detail


@pytest.mark.parametrize(
    ("cuda", "error", "expected"),
    [
        (True, ["OutOfMemoryError", "CUDA out of memory. Tried to allocate 20.00 GiB"],
         "训练节点的显存不足，无法用该模型识别（输入尺寸 1280×1280）。请减小输入尺寸后重新识别，或等显卡空闲后再试"),
        (False, ["RuntimeError", "[enforce fail at alloc_cpu.cpp:121] DefaultCPUAllocator: can't allocate memory"],
         "训练节点的内存不足，无法用该模型识别（输入尺寸 1280×1280）。请减小输入尺寸后重新识别"),
        (True, ["RuntimeError", "CUDA error: no kernel image is available for execution on the device"],
         "显卡出错，无法继续识别（RuntimeError: CUDA error: no kernel image is available"),
        (False, ["ValueError", "shapes do not match"], "模型无法在 cpu 上识别：ValueError: shapes do not match"),
    ],
)  # fmt: skip
def test_warmup_errors(monkeypatch, tmp_path, cuda, error, expected):
    install(monkeypatch, tmp_path, cuda=cuda, warmup_error=error)
    with pytest.raises(InferenceError) as info:
        Session.load(InferenceSpec(weights=weights(tmp_path), imgsz=(1280, 1280)))
    assert info.value.message.startswith(expected), info.value.message


def test_errors_of_one_image(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path)
    session = Session.load(InferenceSpec(weights=weights(tmp_path)))
    assert session.infer(3, image_file(tmp_path, "a.jpg", fake_image(10, 10, "boom"))) == {
        "id": 3,
        "ok": False,
        "error": "识别失败：ValueError: boom",
    }
    assert session.infer(4, image_file(tmp_path, "b.jpg", b"garbage")) == {
        "id": 4,
        "ok": False,
        "error": "无法读取图片（文件已损坏，或不是支持的图片格式）",
    }


@pytest.mark.parametrize(("tag", "expected"), [("oom", "训练节点的显存不足"), ("gpu", "显卡出错")])
def test_gpu_failures_stop_the_inference(monkeypatch, tmp_path, tag, expected):
    # 显存不足、CUDA 上下文损坏：之后的每张图片都会一样失败，不再继续
    install(monkeypatch, tmp_path, cuda=True)
    session = Session.load(InferenceSpec(weights=weights(tmp_path)))
    with pytest.raises(InferenceError) as info:
        session.infer(1, image_file(tmp_path, "a.jpg", fake_image(10, 10, tag)))
    assert info.value.message.startswith(expected)


def test_translate_predict_error():
    assert translate_predict_error(MemoryError(), "cpu", None) == (
        "训练节点的内存不足，无法用该模型识别（输入尺寸 默认）。请减小输入尺寸后重新识别",
        True,
    )
    # CPU 上的“CUDA error”字样不当作显卡出错
    assert translate_predict_error(RuntimeError("CUDA error"), "cpu", None) == (
        "识别失败：RuntimeError: CUDA error",
        False,
    )


def test_missing_weights(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path)
    with pytest.raises(InferenceError) as info:
        Session.load(InferenceSpec(weights=str(tmp_path / "missing.pt")))
    assert info.value.message == f"找不到模型文件：{tmp_path / 'missing.pt'}"


def test_missing_ultralytics(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path)
    monkeypatch.setitem(sys.modules, "ultralytics", None)
    with pytest.raises(InferenceError) as info:
        Session.load(InferenceSpec(weights=weights(tmp_path)))
    assert info.value.message.startswith("训练节点无法导入 Ultralytics")


def test_missing_opencv(monkeypatch, tmp_path):
    install(monkeypatch, tmp_path)
    monkeypatch.setitem(sys.modules, "cv2", None)
    with pytest.raises(InferenceError) as info:
        Session.load(InferenceSpec(weights=weights(tmp_path)))
    assert info.value.message.startswith("训练节点无法导入 OpenCV")


# ---- 子进程入口（python -m trainmaster_trainer.review_inference）----


def fake_ml_dir(tmp_path: Path, **config: Any) -> Path:
    config.setdefault("calls_file", str(tmp_path / "calls.jsonl"))
    return inference_fakes.write_modules(tmp_path / "fake-ml", **config)


def child_env(tmp_path: Path, **config: Any) -> dict[str, str]:
    env = {k: v for k, v in os.environ.items() if not k.startswith(("TM_", "YOLO_"))}
    env.update(PYTHONPATH=os.pathsep.join([str(fake_ml_dir(tmp_path, **config)), str(TRAINER_DIR)]))
    return env


def run_child(
    tmp_path: Path,
    spec_json: str,
    requests: list[str],
    env: dict[str, str] | None = None,
    **config: Any,
) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-m", "trainmaster_trainer.review_inference", "--spec", spec_json],
        input="".join(line + "\n" for line in requests),
        capture_output=True,
        # 协议的 JSON 是 UTF-8（含中文）；不指定时 Windows 上按系统的代码页（如 cp1252）解码
        encoding="utf-8",
        errors="replace",
        env={**child_env(tmp_path, **config), **(env or {})},
        timeout=60,
    )


def request(image_id: int, path: str) -> str:
    return json.dumps({"id": image_id, "path": path})


def test_child_protocol(tmp_path):
    a = image_file(tmp_path, "a.jpg", fake_image(810, 1080, "bus"))
    b = image_file(tmp_path, "b.jpg", b"garbage")
    spec = json.dumps({"weights": weights(tmp_path), "imgsz": [640, 640]})
    proc = run_child(tmp_path, spec, [request(1, a), request(2, b), ""], boxes={"bus": BUS})
    assert proc.returncode == 0, proc.stderr
    lines = [json.loads(line) for line in proc.stdout.splitlines()]
    # 只有协议的 JSON：ultralytics 与 C 扩展打印的内容都转到了 stderr
    assert [line.get("ready", line.get("id")) for line in lines] == [True, 1, 2]
    assert lines[0]["names"] == {"0": "person", "1": "bicycle", "2": "car"}
    assert lines[1]["ok"] is True and len(lines[1]["boxes"]) == 2
    assert lines[2] == {
        "id": 2,
        "ok": False,
        "error": "无法读取图片（文件已损坏，或不是支持的图片格式）",
    }
    assert "C extension writes to fd 1" in proc.stderr
    assert "ultralytics prints to stdout while loading" in proc.stderr


def test_child_exits_at_eof(tmp_path):
    proc = run_child(tmp_path, json.dumps({"weights": weights(tmp_path)}), [])
    assert proc.returncode == 0
    assert json.loads(proc.stdout.splitlines()[0])["ready"] is True


def test_child_turns_on_safe_load(tmp_path):
    # 在导入 ultralytics 之前打开安全加载（它在导入时读取开关），即使环境变量中关闭了它
    proc = run_child(
        tmp_path, json.dumps({"weights": weights(tmp_path)}), [""], {ri.SAFE_LOAD_ENV: "0"}
    )
    assert proc.returncode == 0, proc.stderr
    assert calls(tmp_path)[0]["safe_load"] is True


@pytest.mark.parametrize(
    "spec_json", ["not json", '{"weights": ""}', "[]", '{"weights": "x", "conf": 2}']
)
def test_child_invalid_spec(tmp_path, spec_json):
    proc = run_child(tmp_path, spec_json, [])
    assert proc.returncode == 1
    lines = proc.stdout.splitlines()
    assert len(lines) == 1
    data = json.loads(lines[0])
    assert data["ok"] is False and data["error"].startswith("识别参数不合法：")


def test_child_startup_failure(tmp_path):
    proc = run_child(tmp_path, json.dumps({"weights": weights(tmp_path)}), [], task="classify")
    assert proc.returncode == 1
    assert json.loads(proc.stdout) == {
        "ok": False,
        "error": "分类模型没有检测框，不能用于审图",
        "detail": None,
    }


def test_child_stops_at_a_fatal_error(tmp_path):
    a = image_file(tmp_path, "a.jpg", fake_image(10, 10, "oom"))
    b = image_file(tmp_path, "b.jpg", fake_image(10, 10))
    proc = run_child(
        tmp_path,
        json.dumps({"weights": weights(tmp_path)}),
        [request(1, a), request(2, b)],
        cuda=True,
    )
    assert proc.returncode == 1
    lines = [json.loads(line) for line in proc.stdout.splitlines()]
    assert len(lines) == 2  # ready，然后是没有 id 的错误；不再识别第二张
    assert "id" not in lines[1] and lines[1]["ok"] is False
    assert lines[1]["error"].startswith("训练节点的显存不足，无法用该模型识别")
    assert "OutOfMemoryError" in lines[1]["detail"]


@pytest.mark.parametrize("line", ["{bad", '{"id": 0, "path": "/x"}', '{"id": 1}', "[1]"])
def test_child_invalid_request(tmp_path, line):
    proc = run_child(tmp_path, json.dumps({"weights": weights(tmp_path)}), [line])
    assert proc.returncode == 1
    last = json.loads(proc.stdout.splitlines()[-1])
    assert last["ok"] is False and last["error"].startswith("识别请求不合法：")


# ---- 父进程：InferenceProcess ----


def process(
    tmp_path: Path, command: list[str] | None = None, timeout: float = 60, startup: float = 60
) -> InferenceProcess:
    return InferenceProcess(
        state_dir=tmp_path / "state",
        image_timeout=timeout,
        command=command,
        startup_timeout=startup,
    )


def reply(proc: InferenceProcess, wait: float = 10) -> dict[str, Any] | None:
    deadline = time.monotonic() + wait
    while time.monotonic() < deadline:
        message = proc.next_reply(0.2)
        if message is not None:
            return message
    return None


def test_process_runs_the_real_child_module(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONPATH", str(fake_ml_dir(tmp_path, boxes={"bus": BUS})))
    proc = process(tmp_path)
    ready = proc.start(InferenceSpec(weights=weights(tmp_path), imgsz=(640, 640)), cwd=tmp_path)
    assert ready["ready"] is True and ready["device"] == "cpu"
    assert (tmp_path / "state" / "ultralytics").is_dir()
    # 两张图片同时交给子进程（父进程上报结果时子进程不必空等），按顺序回答
    proc.send(5, image_file(tmp_path, "a.jpg", fake_image(810, 1080, "bus")))
    proc.send(9, image_file(tmp_path, "b.gif", fake_image(32, 16, fmt="GIF89a")))
    assert proc.pending == 2
    first, second = reply(proc), reply(proc)
    assert first is not None and second is not None
    assert (first["id"], first["width"], len(first["boxes"])) == (5, 810, 2)
    assert (second["id"], second["width"], second["height"]) == (9, 32, 16)
    assert proc.pending == 0
    proc.close()
    assert proc._proc is not None and proc._proc.returncode == 0  # 读到空行后自行退出


def ready_child(body: str = "") -> str:
    """脚本子进程：输出 ready，然后按 body 处理请求（默认原样回答一个没有框的结果）。"""
    return (
        "import json, os, sys, time\n"
        'print(json.dumps({"ready": True, "device": "cpu", "names": {"0": "a"}}), flush=True)\n'
        "for line in sys.stdin:\n"
        "    if not line.strip():\n"
        "        break\n"
        "    req = json.loads(line)\n" + (body or ECHO_REPLY)
    )


ECHO_REPLY = (
    '    print(json.dumps({"id": req["id"], "ok": True, "width": 4, "height": 3, "boxes": []}),'
    " flush=True)\n"
)


def test_process_environment(tmp_path, monkeypatch):
    monkeypatch.setenv("CUDA_VISIBLE_DEVICES", "1,2")
    monkeypatch.setenv("TM_NODE_TOKEN", "node-token-secret")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
    monkeypatch.setenv("ULTRALYTICS_SAFE_LOAD", "0")
    cmd = script(
        tmp_path,
        """
        import json, os, sys
        keys = ("CUDA_VISIBLE_DEVICES", "YOLO_OFFLINE", "YOLO_CONFIG_DIR", "YOLO_AUTOINSTALL",
                "ULTRALYTICS_SAFE_LOAD", "TM_NODE_TOKEN", "AWS_SECRET_ACCESS_KEY")
        spec = json.loads(sys.argv[sys.argv.index("--spec") + 1])
        print(json.dumps({"ready": True, "env": {k: os.environ.get(k) for k in keys}, "spec": spec,
                          "cwd": os.getcwd()}))
        """,
    )
    proc = process(tmp_path, cmd)
    ready = proc.start(InferenceSpec(weights="/w.pt", conf=0.4, imgsz=(320, 480)), cwd=tmp_path)
    proc.close()
    assert ready["env"] == {
        "CUDA_VISIBLE_DEVICES": "1,2",  # 显卡可见：节点自己选择的显卡（裸机节点的 trainer.env）
        "YOLO_OFFLINE": "1",
        "YOLO_CONFIG_DIR": str(tmp_path / "state" / "ultralytics"),
        "YOLO_AUTOINSTALL": "False",
        "ULTRALYTICS_SAFE_LOAD": "1",
        # 加载用户上传的 pt 的进程拿不到节点令牌等配置与其它密钥
        "TM_NODE_TOKEN": None,
        "AWS_SECRET_ACCESS_KEY": None,
    }
    assert ready["spec"] == {
        "weights": "/w.pt", "task": None, "yolo_version": None, "conf": 0.4, "iou": 0.7,
        "imgsz": [320, 480], "max_det": 300, "device": None,
    }  # fmt: skip
    assert ready["cwd"] == str(tmp_path)


def test_inference_env_does_not_hide_gpus(tmp_path):
    base = {
        "PATH": "/usr/bin", "HOME": "/var/lib/trainmaster", "CUDA_DEVICE_ORDER": "PCI_BUS_ID",
        "TM_NODE_TOKEN": "secret", "HTTPS_PROXY": "http://user:pw@proxy:3128", "LD_PRELOAD": "/x.so",
    }  # fmt: skip
    env = ri.inference_env(tmp_path, base=base)
    assert "CUDA_VISIBLE_DEVICES" not in env  # 转换子进程设为 ""（只用 CPU）；识别用显卡
    assert not {"TM_NODE_TOKEN", "HTTPS_PROXY", "LD_PRELOAD"} & set(env)
    assert env["CUDA_DEVICE_ORDER"] == "PCI_BUS_ID" and env["PATH"] == "/usr/bin"
    assert env["ULTRALYTICS_SAFE_LOAD"] == "1" and env["YOLO_AUTOINSTALL"] == "False"
    assert env["YOLO_OFFLINE"] == "1" and env["YOLO_CONFIG_DIR"] == str(tmp_path / "ultralytics")
    assert (
        ri.inference_env(tmp_path, base={**base, "CUDA_VISIBLE_DEVICES": ""})[
            "CUDA_VISIBLE_DEVICES"
        ]
        == ""
    )


def test_process_startup_failure_detail_includes_stderr(tmp_path):
    cmd = script(
        tmp_path,
        """
        import json, sys
        print("loading weights...", file=sys.stderr)
        print(json.dumps({"ok": False, "error": "加载模型失败：x", "detail": "Traceback: boom"}))
        sys.exit(1)
        """,
    )
    with pytest.raises(InferenceError) as info:
        process(tmp_path, cmd).start(InferenceSpec(weights="/w.pt"))
    assert info.value.message == "加载模型失败：x"
    assert info.value.detail == "Traceback: boom\n\nloading weights..."


@pytest.mark.parametrize(
    ("body", "expected"),
    [
        ("import sys\nprint('Segmentation fault', file=sys.stderr)\nsys.exit(139)\n", "识别进程意外退出（退出码 139）"),
        # Windows 没有 SIGKILL（也没有被信号结束的负数退出码）：进程崩溃时退出码是 NTSTATUS，这里模拟访问冲突
        # （os._exit 的参数是 C 的 int）
        pytest.param("import os, signal\nos.kill(os.getpid(), signal.SIGKILL)\n",
                     "识别进程被信号 SIGKILL 终止（可能是内存不足）", marks=posix_only),
        pytest.param("import os\nos._exit(0xC0000005 - (1 << 32))\n", "识别进程崩溃（0xC0000005，访问冲突）",
                     marks=windows_only),
        ("print('not json')\n", "识别进程的输出无法解析"),
        ("import json\nprint(json.dumps({'id': 1}))\n", "识别进程的输出无法解析（没有 ready）"),
    ],
)  # fmt: skip
def test_process_startup_problems(tmp_path, body, expected):
    with pytest.raises(InferenceError) as info:
        process(tmp_path, script(tmp_path, body)).start(InferenceSpec(weights="/w.pt"))
    assert info.value.message == expected
    assert info.value.image_id is None  # 不是哪一张图片的问题
    if "Segmentation" in body:
        assert "Segmentation fault" in info.value.detail


def test_process_startup_timeout(tmp_path):
    cmd = script(tmp_path, "import time\ntime.sleep(30)\n")
    started = time.monotonic()
    with pytest.raises(InferenceError) as info:
        process(tmp_path, cmd, timeout=1, startup=1.5).start(InferenceSpec(weights="/w.pt"))
    assert time.monotonic() - started < 10
    assert info.value.timeout is True
    assert info.value.message.startswith("加载模型超时（1.5 秒内没有完成）")
    assert "TM_INFERENCE_IMAGE_TIMEOUT" in info.value.message


def test_process_startup_timeout_defaults():
    # 加载模型至少可以用 MIN_STARTUP_TIMEOUT 秒（即使单张图片的超时更短）
    short = InferenceProcess(state_dir=Path("/state"), image_timeout=5)
    assert short.startup_timeout == ri.MIN_STARTUP_TIMEOUT == 120
    assert InferenceProcess(state_dir=Path("/state"), image_timeout=600).startup_timeout == 600


def test_process_image_timeout_kills_process_group(tmp_path):
    marker = tmp_path / "grandchild-alive"
    grandchild = (
        "import pathlib, sys, time; time.sleep(3); pathlib.Path(sys.argv[1]).write_text('x')"
    )
    body = (
        "    import subprocess\n"
        f"    subprocess.Popen([sys.executable, '-c', {grandchild!r}, {str(marker)!r}])\n"
        "    print('inferring', file=sys.stderr, flush=True)\n"
        "    time.sleep(30)\n"
    )
    proc = process(tmp_path, script(tmp_path, ready_child(body)), timeout=1)
    proc.start(InferenceSpec(weights="/w.pt"))
    proc.send(1, "/img.jpg")
    started = time.monotonic()
    with pytest.raises(InferenceError) as info:
        reply(proc)
    assert time.monotonic() - started < 10
    assert info.value.timeout is True
    assert info.value.image_id == 1  # 超时的图片
    assert info.value.message.startswith("识别超时（单张图片 1 秒内没有识别完成）")
    assert (
        "TM_INFERENCE_IMAGE_TIMEOUT" in info.value.message and "减小输入尺寸" in info.value.message
    )
    assert "inferring" in info.value.detail
    time.sleep(3.5)
    assert not marker.exists()


def test_process_timeout_counts_from_the_previous_reply(tmp_path):
    # 两张图片同时发出：第二张的时间从第一张回答时算起（子进程那时才开始处理它）
    body = "    time.sleep(0.7)\n" + ECHO_REPLY
    # 1.2 秒：每张图片留出 0.5 秒的余量（Windows 的 CI 较慢），从发出算起的 1.4 秒仍然超过它
    proc = process(tmp_path, script(tmp_path, ready_child(body)), timeout=1.2)
    proc.start(InferenceSpec(weights="/w.pt"))
    proc.send(1, "/a.jpg")
    proc.send(2, "/b.jpg")
    assert reply(proc)["id"] == 1
    assert reply(proc)["id"] == 2  # 从发出算起 1.4 秒，但子进程只用了 0.7 秒
    proc.close()


@pytest.mark.parametrize(
    ("body", "expected", "crashed"),
    [
        ('    print(json.dumps({"id": 99, "ok": True}), flush=True)\n',
         "识别进程的回答与请求不对应（收到 99 的结果，应为 1）", False),
        # Windows 上 os.kill 是 TerminateProcess（退出码就是信号的编号），没有被信号结束的负数退出码
        pytest.param("    os.kill(os.getpid(), 9)\n", "识别进程被信号 SIGKILL 终止（可能是内存不足）", True,
                     marks=posix_only),
        # 只有 SIGKILL（系统的 OOM killer）可能是内存不足
        pytest.param("    os.kill(os.getpid(), 11)\n", "识别进程崩溃（信号 SIGSEGV）", True, marks=posix_only),
        pytest.param("    os.kill(os.getpid(), 15)\n", "识别进程被信号 SIGTERM 终止", True, marks=posix_only),
        # Windows 上进程崩溃时退出码是 NTSTATUS（os._exit 的参数是 C 的 int）
        pytest.param("    os._exit(0xC0000005 - (1 << 32))\n", "识别进程崩溃（0xC0000005，访问冲突）", True,
                     marks=windows_only),
        # 子进程报告的无法继续的错误（显存不足、显卡出错）对每张图片都一样：不算这张图片的问题
        ('    print(json.dumps({"ok": False, "error": "显卡出错", "detail": "CUDA"}), flush=True)\n    sys.exit(1)\n',
         "显卡出错", False),
        ("    sys.exit(0)\n", "识别进程意外退出（退出码 0）", True),
        ("    print('garbage', flush=True)\n", "识别进程的输出无法解析", False),
    ],
)  # fmt: skip
def test_process_reply_problems(tmp_path, body, expected, crashed):
    proc = process(tmp_path, script(tmp_path, ready_child(body)))
    proc.start(InferenceSpec(weights="/w.pt"))
    proc.send(1, "/a.jpg")
    proc.send(2, "/b.jpg")
    with pytest.raises(InferenceError) as info:
        reply(proc)
    assert info.value.message == expected
    assert info.value.timeout is False and info.value.cancelled is False
    # 崩溃时正在识别的是最早交给它的那一张（第二张还在排队）
    assert info.value.image_id == (1 if crashed else None)


def test_process_send_after_the_child_died(tmp_path):
    # 子进程已经退出时 send 不报错，next_reply 给出原因
    proc = process(tmp_path, script(tmp_path, ready_child("    sys.exit(3)\n")))
    proc.start(InferenceSpec(weights="/w.pt"))
    proc.send(1, "/a.jpg")
    proc._proc.wait(timeout=10)  # type: ignore[union-attr]
    proc.send(2, "/b.jpg")
    with pytest.raises(InferenceError) as info:
        reply(proc)
    assert info.value.message == "识别进程意外退出（退出码 3）"


def test_process_cancel(tmp_path):
    pid_file = tmp_path / "child.pid"
    body = f"    open({str(pid_file)!r}, 'w').write(str(os.getpid()))\n    time.sleep(30)\n"
    proc = process(tmp_path, script(tmp_path, ready_child(body)))
    proc.start(InferenceSpec(weights="/w.pt"))
    proc.send(1, "/a.jpg")
    deadline = time.monotonic() + 10
    while not (pid_file.exists() and pid_file.read_text()) and time.monotonic() < deadline:
        time.sleep(0.02)
    threading.Timer(0.2, proc.cancel).start()
    with pytest.raises(InferenceError) as info:
        reply(proc)
    assert info.value.cancelled is True and info.value.message == "识别已取消"
    assert info.value.image_id is None
    assert gone(int(pid_file.read_text()))
    # 取消后不再启动新的子进程
    with pytest.raises(InferenceError) as info:
        proc.start(InferenceSpec(weights="/w.pt"))
    assert info.value.cancelled is True


def test_process_close_kills_a_busy_child(tmp_path, monkeypatch):
    monkeypatch.setattr(ri, "CLOSE_TIMEOUT", 0.5)
    proc = process(tmp_path, script(tmp_path, ready_child("    time.sleep(30)\n")))
    proc.start(InferenceSpec(weights="/w.pt"))
    proc.send(1, "/a.jpg")
    started = time.monotonic()
    proc.close()
    assert time.monotonic() - started < 6
    assert proc._proc.poll() is not None  # type: ignore[union-attr]


def test_process_command_not_found(tmp_path):
    with pytest.raises(InferenceError) as info:
        process(tmp_path, [str(tmp_path / "missing-python")]).start(InferenceSpec(weights="/w.pt"))
    assert info.value.message.startswith("无法启动识别进程")


def test_real_child_module_without_ultralytics(tmp_path, monkeypatch):
    """默认命令在没有 ultralytics 的测试环境中也给出中文说明。"""
    monkeypatch.delenv("PYTHONPATH", raising=False)
    try:
        ready = process(tmp_path).start(InferenceSpec(weights=weights(tmp_path)))
    except InferenceError as exc:
        assert exc.message.startswith("训练节点无法导入 Ultralytics"), exc.message
    else:
        pytest.skip(f"ultralytics is installed in the test environment ({ready})")


# ---- 结果的整理与攒批 ----


def test_translate_reply_maps_class_ids_to_names():
    names = {0: "person", 2: "car"}
    reply_ = {
        "id": 4, "ok": True, "width": 810, "height": 1080,
        "boxes": [
            [3.83, 229.36, 796.19, 728.41, 0.9402, 2],
            [1, 2, 3, 4, 1.5, 7],  # 不认识的类别编号：用编号作名称；置信度限制在 0–1
            [5, 5, 5, 9, 0.5, 0],  # 宽为 0
            [1, 2, 3, None, 0.5, 0],  # 非数值（NaN 被 json_safe 换成 null）
            [1, 2, 3, 4, 0.5, 1.5],  # 类别不是整数
            [1, 2, 3, 4, 0.5, -1],
            "junk", [1, 2],
        ],
    }  # fmt: skip
    assert translate_reply(reply_, names) == {
        "id": 4,
        "ok": True,
        "width": 810,
        "height": 1080,
        "boxes": [
            {"name": "car", "class_id": 2, "confidence": 0.9402, "xmin": 3.83, "ymin": 229.36,
             "xmax": 796.19, "ymax": 728.41},
            {"name": "7", "class_id": 7, "confidence": 1.0, "xmin": 1.0, "ymin": 2.0, "xmax": 3.0, "ymax": 4.0},
        ],
    }  # fmt: skip


def test_translate_reply_failures():
    assert translate_reply({"id": 1, "ok": False, "error": "无法读取图片（x）"}, {}) == {
        "id": 1,
        "ok": False,
        "error": "无法读取图片（x）",
    }
    assert translate_reply({"id": 1, "ok": False}, {})["error"] == "识别失败"
    assert len(translate_reply({"id": 1, "ok": False, "error": "长" * 900}, {})["error"]) == 500
    assert translate_reply({"id": 1, "ok": True, "width": 0, "height": 5, "boxes": []}, {}) == {
        "id": 1,
        "ok": False,
        "error": "识别进程没有给出图片的尺寸",
    }


def test_translate_reply_keeps_at_most_1000_boxes():
    boxes = [[i, 0, i + 1, 1, 0.5, 0] for i in range(1200)]
    out = translate_reply({"id": 1, "ok": True, "width": 2000, "height": 2, "boxes": boxes}, {})
    assert len(out["boxes"]) == 1000


@pytest.mark.parametrize(
    ("raw", "expected"),
    [("  person ", "person"), ("划痕\n严重", "划痕 严重"), ("", "3"), ("\t", "3"), (None, "3"),
     ("a" * 300, "a" * 255), ("😀" * 300, "😀" * 255)],
)  # fmt: skip
def test_clean_class_name(raw, expected):
    assert clean_class_name(raw, 3) == expected


def test_class_names_prefer_the_model():
    ready = {"names": {"0": "person", "1": " bike ", "x": "bad", "-1": "neg"}}
    assert class_names(ready, ["人", "自行车", "汽车"]) == {0: "person", 1: "bike", 2: "汽车"}
    assert class_names({}, ["a"]) == {0: "a"}
    assert class_names({"names": None}) == {}


def test_result_batch_flushes_by_count_size_and_age():
    clock = [0.0]
    batch = ResultBatch(lambda: clock[0])
    assert batch.seconds_until_due() == float("inf")
    for i in range(ri.FLUSH_COUNT - 1):
        batch.add({"id": i}, 100)
    assert not batch.full
    batch.add({"id": 99}, 100)
    assert batch.full and len(batch) == ri.FLUSH_COUNT
    batch.clear()
    assert len(batch) == 0 and batch.size == ResultBatch.OVERHEAD
    batch.add({"id": 1}, ri.FLUSH_BYTES)
    assert batch.full
    batch.clear()
    batch.add({"id": 1}, 10)
    clock[0] = 3.0
    assert batch.seconds_until_due() == pytest.approx(2.0)
    clock[0] = 6.0
    assert batch.seconds_until_due() == 0
    # 一次请求不超过平台的上限（1 MiB）：放不下时先上报
    assert batch.fits(ri.MAX_BODY_BYTES // 2)
    assert not batch.fits(ri.MAX_BODY_BYTES)
    assert ResultBatch().fits(ri.MAX_BODY_BYTES * 2)  # 空的批次总能放下一条（fit_result 先裁剪）


def test_fit_result_keeps_the_most_confident_boxes():
    name = "名" * 255
    boxes = [{"name": name, "class_id": 0, "confidence": i / 2000, "xmin": 0.0, "ymin": 0.0, "xmax": 1.0,
              "ymax": 1.0} for i in range(1000)]  # fmt: skip
    result = {"id": 1, "ok": True, "width": 2, "height": 2, "boxes": boxes}
    limit = 200_000
    fitted, size = fit_result(result, limit)
    assert size <= limit and size == ri.encoded_size(fitted)
    kept = [b["confidence"] for b in fitted["boxes"]]
    assert 0 < len(kept) < 1000
    assert kept == sorted(kept, reverse=True) and kept[0] == 999 / 2000
    small = {"id": 2, "ok": True, "width": 2, "height": 2, "boxes": boxes[:2]}
    assert fit_result(small, limit) == (small, ri.encoded_size(small))


# ---- 任务数据 ----


def task_json(**overrides: Any) -> dict[str, Any]:
    base = "/api/v1/compute/agent/review-inferences/12"
    task = {
        "kind": "review_inference", "id": 12, "lease_seconds": 60,
        "model": {"id": 3, "project_id": 1, "version": 3, "yolo_version": "yolo11", "task": "detect",
                  "file_name": "best.pt", "size_bytes": 5400000, "sha256": "A" * 64,
                  "class_names": ["scratch", "dent"]},
        "options": {"conf": 0.25, "iou": 0.7, "imgsz": [640, 640], "max_det": 300},
        "image_count": 1200, "remaining": 1100,
        **{f"{name}_url": f"{base}/{name}" for name in ("model", "images", "progress", "results", "complete", "fail")},
    }  # fmt: skip
    for key, value in overrides.items():
        if "." in key:
            outer, inner = key.split(".")
            task[outer] = {**task[outer], inner: value}
        else:
            task[key] = value
    return task


def test_task_info_parse():
    info = InferenceTaskInfo.parse(task_json())
    assert (info.id, info.model_id, info.yolo_version, info.task) == (12, 3, "yolo11", "detect")
    assert (info.size_bytes, info.sha256, info.class_names) == (
        5400000,
        "a" * 64,
        ("scratch", "dent"),
    )
    assert (info.conf, info.iou, info.imgsz, info.max_det) == (0.25, 0.7, (640, 640), 300)
    assert (info.image_count, info.remaining) == (1200, 1100)
    assert info.images_url == "/api/v1/compute/agent/review-inferences/12/images"
    assert InferenceTaskInfo.parse(task_json(**{"options.imgsz": 320})).imgsz == (320, 320)
    lenient = task_json(
        image_count=None, remaining=None, **{"model.class_names": None, "model.id": None}
    )
    info = InferenceTaskInfo.parse(lenient)
    assert (info.image_count, info.remaining, info.class_names, info.model_id) == (
        None,
        None,
        (),
        None,
    )


@pytest.mark.parametrize(
    ("overrides", "field"),
    [
        ({"id": 0}, "id"), ({"model": None}, "model"), ({"model.size_bytes": 0}, "model.size_bytes"),
        ({"model.sha256": "nope"}, "model.sha256"), ({"model.class_names": [1]}, "model.class_names"),
        ({"options": []}, "options"), ({"options.conf": 0}, "options.conf"), ({"options.iou": "x"}, "options.iou"),
        ({"options.imgsz": [640]}, "options.imgsz"), ({"options.max_det": 5000}, "options.max_det"),
        ({"remaining": -1}, "remaining"), ({"image_count": "many"}, "image_count"),
        ({"results_url": "http://evil/x"}, "results_url"), ({"images_url": "/a//b"}, "images_url"),
        ({"fail_url": None}, "fail_url"),
    ],
)  # fmt: skip
def test_task_info_validation(overrides, field):
    with pytest.raises(ValueError) as info:
        InferenceTaskInfo.parse(task_json(**overrides))
    assert str(info.value) == field


# ---- 命令行 infer ----


def run_cli(tmp_path: Path, *args: str, **config: Any) -> subprocess.CompletedProcess:
    env = child_env(tmp_path, **config)
    env.update(
        HOME=str(tmp_path / "home"),
        TM_STATE_DIR=str(tmp_path / "state"),
        TM_DATA_DIR=str(tmp_path),
        # 命令行的输出用 UTF-8：Windows 上输出到管道时默认使用 ANSI 代码页（如 cp1252），中文无法编码
        PYTHONIOENCODING="utf-8",
    )
    return subprocess.run(
        [sys.executable, "-m", "trainmaster_trainer", "infer", *args],
        capture_output=True,
        encoding="utf-8",
        errors="replace",
        env=env,
        timeout=120,
    )


def test_cli_infer_summary(tmp_path):
    a = image_file(tmp_path, "bus.jpg", fake_image(810, 1080, "bus"))
    proc = run_cli(
        tmp_path,
        "--weights",
        weights(tmp_path),
        "--imgsz",
        "480x640",
        "--conf",
        "0.4",
        a,
        boxes={"bus": BUS},
    )
    assert proc.returncode == 0, proc.stderr
    lines = proc.stdout.splitlines()
    assert lines[0] == "模型已加载：检测模型，cpu（Ultralytics 8.4.198 · PyTorch 2.14.1+cpu）"
    assert lines[1].startswith(f"{a}：810×1080，2 个目标，用时 ")
    assert lines[2:] == ["  car 0.940 [4, 229, 796, 728]", "  person 0.888 [671, 395, 810, 879]"]
    predict = [c for c in calls(tmp_path) if c["call"] == "predict"]
    assert predict[0]["imgsz"] == [480, 640] and predict[0]["conf"] == 0.4


def test_cli_infer_json_with_a_failed_image(tmp_path):
    a = image_file(tmp_path, "a.jpg", fake_image(700, 600, "bus"))
    b = image_file(tmp_path, "b.jpg", b"garbage")
    proc = run_cli(tmp_path, "--weights", weights(tmp_path), "--json", a, b, boxes={"bus": BUS[:1]})
    assert proc.returncode == 1  # 有图片识别失败
    data = json.loads(proc.stdout)
    assert data["ok"] is False and data["device"] == "cpu" and data["task"] == "detect"
    first, second = data["images"]
    assert first["path"] == a and first["ok"] is True and first["boxes"][0]["name"] == "car"
    assert first["boxes"][0]["xmax"] == 700.0  # 裁剪到图片范围
    assert second == {
        "path": b,
        "ok": False,
        "error": "无法读取图片（文件已损坏，或不是支持的图片格式）",
        "elapsed_ms": second["elapsed_ms"],
    }


def test_cli_infer_model_failure(tmp_path):
    a = image_file(tmp_path, "a.jpg", fake_image(4, 4))
    proc = run_cli(tmp_path, "--weights", weights(tmp_path), a, task="classify")
    assert proc.returncode == 1
    assert proc.stdout == ""
    assert proc.stderr.startswith("识别失败：分类模型没有检测框，不能用于审图")
    proc = run_cli(tmp_path, "--weights", weights(tmp_path), "--json", a, task="classify")
    data = json.loads(proc.stdout)
    assert data["ok"] is False and data["error"] == "分类模型没有检测框，不能用于审图"
    # 详情附带子进程的 stderr（加载模型时 ultralytics 的输出被转到 stderr）；按行比较：Windows 上 print 写到
    # stderr 的换行是 \r\n，C 扩展直接写 fd 的是 \n
    assert data["detail"].splitlines() == [
        "ultralytics prints to stdout while loading",
        "C extension writes to fd 1",
    ]


@pytest.mark.parametrize(
    "args",
    [
        ["--conf", "0"],
        ["--conf", "x"],
        ["--iou", "0.99"],
        ["--max-det", "0"],
        ["--imgsz", "500"],
        [],
    ],
)
def test_cli_infer_invalid_arguments(tmp_path, args):
    proc = run_cli(tmp_path, "--weights", weights(tmp_path), *args, *(["a.jpg"] if args else []))
    assert proc.returncode == 2
    assert "usage:" in proc.stderr


def test_format_inference_with_gpu():
    from trainmaster_trainer.__main__ import format_inference

    ready = {"device": "cuda:0", "device_name": "RTX 4090", "task": "obb", "versions": {}}
    text = format_inference(
        ready, [("a.png", {"ok": True, "width": 2, "height": 1, "boxes": []}, 0.5)]
    )
    assert text.splitlines() == [
        "模型已加载：旋转框模型，cuda:0（RTX 4090）",
        "a.png：2×1，0 个目标，用时 0.50 秒",
    ]


def test_pillow_reads_from_bytes(monkeypatch, tmp_path):
    # 读取图片时 Pillow 拿到的是（可能改过 TIFF 方向的）字节，而不是路径
    install(monkeypatch, tmp_path)
    seen: list[Any] = []
    pil = sys.modules["PIL"].Image
    original = pil.open
    monkeypatch.setattr(pil, "open", lambda fp: seen.append(fp) or original(fp))
    read_image(image_file(tmp_path, "a.gif", fake_image(3, 2, fmt="GIF89a")))
    assert len(seen) == 1 and isinstance(seen[0], io.BytesIO)
