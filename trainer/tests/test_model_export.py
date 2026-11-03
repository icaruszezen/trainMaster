"""model_export.py：转换子进程（假 ultralytics / onnx / onnxruntime）的参数映射、结果核对、错误翻译，
父进程对子进程的处理（超时、取消、崩溃、输出），以及命令行 export。"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import textwrap
import threading
import time
from pathlib import Path
from typing import Any

import export_fakes
import pytest
from fakes import TRAINER_DIR

from trainmaster_trainer import model_export
from trainmaster_trainer.model_export import (
    END2END_VERSIONS,
    YOLO_LABELS,
    ExportError,
    ExportProcess,
    ExportSpec,
    check_onnx,
    run_export,
    translate_export_error,
    translate_load_error,
    version_tuple,
)

VERSIONS = list(YOLO_LABELS)


def install(monkeypatch, *, onnxruntime: bool = True, onnx: bool = True, **config: Any):
    """把假模块放进 sys.modules，返回假的 ultralytics（calls 记录调用）与 onnxruntime。"""
    ultralytics = export_fakes.make_ultralytics(**config)
    ort = export_fakes.make_onnxruntime() if onnxruntime else None
    monkeypatch.setitem(sys.modules, "ultralytics", ultralytics)
    monkeypatch.setitem(sys.modules, "onnx", export_fakes.make_onnx() if onnx else None)
    monkeypatch.setitem(sys.modules, "onnxruntime", ort)
    monkeypatch.setitem(sys.modules, "numpy", export_fakes.make_numpy())
    monkeypatch.setitem(sys.modules, "torch", export_fakes.make_torch())
    return ultralytics, ort


def weights(tmp_path: Path) -> str:
    path = tmp_path / "model.pt"
    path.write_bytes(b"PK\x03\x04 fake checkpoint")
    return str(path)


def spec(tmp_path: Path, **kw: Any) -> ExportSpec:
    kw.setdefault("imgsz", (640, 640))
    kw.setdefault("task", "detect")
    return ExportSpec(weights=weights(tmp_path), **kw)


def export_calls(ultralytics) -> list[dict[str, Any]]:
    return [c for c in ultralytics.calls if c["call"] == "export"]


# ---- 每个 YOLO 版本 × 后处理方式 ----

_COMBOS = [
    (version, postprocess)
    for version in VERSIONS
    for postprocess in ("raw", "nms", "end2end")
    if postprocess != "end2end" or version in END2END_VERSIONS
]


@pytest.mark.parametrize(("version", "postprocess"), _COMBOS)
def test_export_arguments_for_every_version(monkeypatch, tmp_path, version, postprocess):
    ultralytics, ort = install(monkeypatch, end2end_head=version in END2END_VERSIONS)
    out = run_export(spec(tmp_path, yolo_version=version, postprocess=postprocess))
    assert out["ok"], out
    assert ultralytics.calls[0] == {
        "call": "YOLO",
        "weights": str(tmp_path / "model.pt"),
        "task": "detect",
    }
    assert export_calls(ultralytics) == [
        {
            "call": "export",
            "format": "onnx",
            "imgsz": [640, 640],
            "dynamic": False,
            "nms": {"raw": None, "nms": True, "end2end": False}[postprocess],
            "opset": None,
            "simplify": True,
            "device": "cpu",
            "batch": 1,
        }
    ]
    result = out["result"]
    assert result["end2end"] is (postprocess == "end2end")
    assert result["nms"] is (postprocess == "nms")
    assert result["inputs"] == [{"name": "images", "shape": [1, 3, 640, 640], "type": "float32"}]
    expected = [1, 300, 6] if postprocess != "raw" else [1, 6, 8400]
    assert result["outputs"] == [{"name": "output0", "shape": expected, "type": "float32"}]
    assert result["opset"] == 18
    assert result["verified"] is True
    assert ort.runs == [{"images": ([1, 3, 640, 640], "float32")}]
    assert out["onnx_path"] == str(tmp_path / "model.onnx")


@pytest.mark.parametrize("version", [v for v in VERSIONS if v not in END2END_VERSIONS])
def test_end2end_rejected_for_models_without_one2one_head(monkeypatch, tmp_path, version):
    # ultralytics 对 YOLO11 等的 nms=False 静默忽略（仍是原始输出），因此在导出前拒绝。
    ultralytics, _ = install(monkeypatch)
    out = run_export(spec(tmp_path, yolo_version=version, postprocess="end2end"))
    assert not out["ok"]
    assert out["error"] == (
        f"该模型（{YOLO_LABELS[version]}）没有端到端（NMS-free）检测头，不能导出 end2end；"
        "请选择 raw 或 nms"
    )
    assert export_calls(ultralytics) == []


def test_end2end_allowed_by_the_head_not_the_version(monkeypatch, tmp_path):
    # 自定义结构：选择的版本是 YOLO11，但检测头带 one2one（如平台识别出 info.end2end）。
    install(monkeypatch, end2end_head=True)
    out = run_export(spec(tmp_path, yolo_version="yolo11", postprocess="end2end"))
    assert out["ok"] and out["result"]["end2end"] is True


@pytest.mark.parametrize(
    ("version", "task", "expected"),
    [("yolo26", "detect", "end2end"), ("yolov10", "detect", "end2end"), ("yolo11", "detect", "raw"),
     ("yolov8", "segment", "raw"), ("yolo26", "classify", "raw")],
)  # fmt: skip
def test_default_postprocess_follows_the_model(monkeypatch, tmp_path, version, task, expected):
    ultralytics, _ = install(
        monkeypatch, task=task, end2end_head=version in END2END_VERSIONS and task != "classify"
    )
    out = run_export(spec(tmp_path, yolo_version=version, task=task, postprocess=None))
    assert out["ok"], out
    nms = export_calls(ultralytics)[0]["nms"]
    assert nms == {"raw": None, "end2end": False}[expected]


@pytest.mark.parametrize("postprocess", ["nms", "end2end"])
def test_classify_only_raw(monkeypatch, tmp_path, postprocess):
    ultralytics, _ = install(monkeypatch, task="classify", end2end_head=True)
    out = run_export(spec(tmp_path, task="classify", postprocess=postprocess))
    assert not out["ok"]
    assert out["error"].startswith("分类模型只能导出原始输出（raw）")
    assert export_calls(ultralytics) == []


def test_classify_raw(monkeypatch, tmp_path):
    install(monkeypatch, task="classify", nc=1000)
    out = run_export(spec(tmp_path, task="classify", imgsz=(224, 320), postprocess="raw"))
    assert out["ok"], out
    assert out["result"]["inputs"][0]["shape"] == [1, 3, 224, 320]
    assert out["result"]["outputs"] == [{"name": "output0", "shape": [1, 1000], "type": "float32"}]


def test_segment_nms_has_two_outputs(monkeypatch, tmp_path):
    install(monkeypatch, task="segment")
    out = run_export(spec(tmp_path, task="segment", imgsz=(320, 320), postprocess="nms"))
    assert out["ok"], out
    assert [o["shape"] for o in out["result"]["outputs"]] == [[1, 300, 38], [1, 32, 80, 80]]


def test_dynamic_input(monkeypatch, tmp_path):
    ultralytics, ort = install(monkeypatch)
    out = run_export(spec(tmp_path, yolo_version="yolo11", dynamic=True, imgsz=(480, 640)))
    assert out["ok"], out
    assert export_calls(ultralytics)[0]["dynamic"] is True
    result = out["result"]
    assert result["dynamic"] is True
    assert result["inputs"][0]["shape"] == ["batch", 3, "height", "width"]
    assert result["outputs"][0]["shape"] == ["batch", 6, "anchors"]
    # 推理验证时动态维度取 batch 1 与请求的尺寸
    assert ort.runs == [{"images": ([1, 3, 480, 640], "float32")}]


def test_opset_and_rectangular_size(monkeypatch, tmp_path):
    ultralytics, _ = install(monkeypatch)
    out = run_export(spec(tmp_path, yolo_version="yolo11", opset=12, imgsz=(480, 640)))
    assert out["ok"], out
    call = export_calls(ultralytics)[0]
    assert call["opset"] == 12 and call["imgsz"] == [480, 640]
    assert out["result"]["opset"] == 12
    assert out["result"]["outputs"][0]["shape"] == [1, 6, 6300]


def test_default_size_comes_from_the_model(monkeypatch, tmp_path):
    ultralytics, ort = install(monkeypatch, train_imgsz=96)
    out = run_export(spec(tmp_path, imgsz=None, postprocess="raw"))
    assert out["ok"], out
    assert "imgsz" not in export_calls(ultralytics)[0]
    assert ort.runs[0]["images"][0] == [1, 3, 96, 96]


def test_output_is_moved(monkeypatch, tmp_path):
    install(monkeypatch)
    target = tmp_path / "out" / "best.onnx"
    out = run_export(spec(tmp_path, postprocess="raw", output=str(target)))
    assert out["ok"], out
    assert out["onnx_path"] == str(target) and target.is_file()
    assert not (tmp_path / "model.onnx").exists()


def test_result_versions(monkeypatch, tmp_path):
    install(monkeypatch, version="8.4.198")
    result = run_export(spec(tmp_path, postprocess="raw"))["result"]
    assert result["ultralytics_version"] == "8.4.198"
    assert result["torch_version"] == "2.14.1+cpu"
    assert result["onnx_version"] == "1.23.2"
    assert result["onnxruntime_version"] == "1.23.2"
    assert isinstance(result["duration_ms"], int) and result["duration_ms"] >= 0
    assert set(result) == {
        "ultralytics_version", "torch_version", "onnx_version", "onnxruntime_version", "opset",
        "end2end", "nms", "dynamic", "inputs", "outputs", "verified", "duration_ms",
    }  # fmt: skip


# ---- 核对导出结果 ----


def test_ignored_nms_is_an_error(monkeypatch, tmp_path):
    install(monkeypatch, ignore_nms=True)
    out = run_export(spec(tmp_path, yolo_version="yolo11", postprocess="nms"))
    assert not out["ok"]
    assert "该模型不支持嵌入 NMS" in out["error"] and "请选择 raw" in out["error"]


def test_ignored_end2end_is_an_error(tmp_path):
    described = {
        "metadata": {"end2end": "False", "args": "{'nms': False}"},
        "inputs": [{"name": "images", "shape": [1, 3, 640, 640], "type": "float32"}],
    }
    with pytest.raises(ExportError) as info:
        check_onnx(described, ExportSpec(weights="x", imgsz=(640, 640)), "end2end", True)
    assert "不支持端到端（end2end）导出" in info.value.message


def test_unexpected_end2end_is_an_error():
    described = {
        "metadata": {"end2end": "True", "args": "{'nms': None}"},
        "inputs": [{"shape": []}],
    }
    with pytest.raises(ExportError) as info:
        check_onnx(described, ExportSpec(weights="x"), "raw", True)
    assert "不一致" in info.value.message


def test_missing_metadata_is_trusted(monkeypatch, tmp_path):
    # 旧版 ultralytics 没有写 end2end / args：无法核对时按请求的方式记录
    install(monkeypatch, no_metadata=True, end2end_head=True)
    out = run_export(spec(tmp_path, postprocess="end2end"))
    assert out["ok"], out
    assert out["result"]["end2end"] is True and out["result"]["nms"] is False


def test_unparsable_args_metadata():
    assert model_export._metadata_flags({"end2end": "True", "args": "{bad"}) == (True, None)
    assert model_export._metadata_flags({"args": "{'nms': True}"}) == (None, True)
    assert model_export._metadata_flags({"end2end": "yes"}) == (None, None)


def test_wrong_input_shape(monkeypatch, tmp_path):
    install(monkeypatch, wrong_shape=True)
    out = run_export(spec(tmp_path, postprocess="raw"))
    assert not out["ok"]
    assert "与请求的输入尺寸 640×640 不一致" in out["error"]


def test_without_onnxruntime_not_verified(monkeypatch, tmp_path):
    install(monkeypatch, onnxruntime=False)
    out = run_export(spec(tmp_path, postprocess="raw"))
    assert out["ok"], out
    assert out["result"]["verified"] is False
    assert out["result"]["onnxruntime_version"] is None


def test_onnxruntime_failure(monkeypatch, tmp_path):
    install(monkeypatch, ort_error="[ONNXRuntimeError] : 10 : INVALID_GRAPH")
    out = run_export(spec(tmp_path, postprocess="raw"))
    assert not out["ok"]
    assert out["error"].startswith(
        "用 ONNX Runtime 验证导出的模型失败：RuntimeError: [ONNXRuntimeError]"
    )
    assert "Traceback" in out["detail"]


def test_non_finite_output(monkeypatch, tmp_path):
    install(monkeypatch, nan_output=True)
    out = run_export(spec(tmp_path, postprocess="raw"))
    assert not out["ok"] and "NaN" in out["error"]


def test_invalid_onnx(monkeypatch, tmp_path):
    install(monkeypatch, bad_onnx=True)
    out = run_export(spec(tmp_path, postprocess="raw"))
    assert not out["ok"]
    assert out["error"].startswith("导出的 ONNX 模型无效：ValidationError")


def test_export_returned_no_file(monkeypatch, tmp_path):
    install(monkeypatch, no_file=True)
    out = run_export(spec(tmp_path, postprocess="raw"))
    assert not out["ok"] and "没有生成 ONNX 文件" in out["error"]


def test_missing_weights(monkeypatch, tmp_path):
    install(monkeypatch)
    out = run_export(ExportSpec(weights=str(tmp_path / "missing.pt")))
    assert out == {
        "ok": False,
        "error": f"找不到模型文件：{tmp_path / 'missing.pt'}",
        "detail": None,
    }


def test_missing_onnx_package(monkeypatch, tmp_path):
    install(monkeypatch, onnx=False)
    out = run_export(spec(tmp_path))
    assert not out["ok"]
    assert out["error"].startswith("训练节点缺少 ONNX 转换需要的 onnx")
    assert "onnxslim、onnxruntime" in out["error"]


def test_missing_ultralytics(monkeypatch, tmp_path):
    install(monkeypatch)
    monkeypatch.setitem(sys.modules, "ultralytics", None)
    out = run_export(spec(tmp_path))
    assert not out["ok"] and out["error"].startswith("训练节点无法导入 Ultralytics")


def test_unexpected_error_is_reported(monkeypatch, tmp_path):
    install(monkeypatch)
    monkeypatch.setattr(model_export, "describe_onnx", lambda *a: 1 / 0)
    out = run_export(spec(tmp_path, postprocess="raw"))
    assert not out["ok"] and "ZeroDivisionError" in out["error"]


# ---- 错误翻译 ----

_TOO_OLD_11 = (
    "训练节点的 Ultralytics 8.2.103 无法加载该模型（YOLO11 需要 8.3.0 及以上、YOLO26 需要 8.4.0 及以上），"
    "请升级训练节点"
)
_TOO_OLD_26 = (
    "训练节点的 Ultralytics 8.2.103 无法加载该模型（YOLO26 需要 8.4.0 及以上、YOLO11 需要 8.3.0 及以上），"
    "请升级训练节点"
)


@pytest.mark.parametrize(
    ("error", "version", "saved", "expected"),
    [
        (["AttributeError", "Can't get attribute 'C3k2' on <module 'ultralytics.nn.modules.block'>"],
         "yolo11", "8.3.40", _TOO_OLD_11 + "；该模型由 Ultralytics 8.3.40 保存，比训练节点的 8.2.103 新"),
        (["ModuleNotFoundError", "No module named 'ultralytics.nn.modules.yolo26'",
          "ultralytics.nn.modules.yolo26"], "yolo26", None, _TOO_OLD_26),
        # 没有列出最低版本的 YOLO12：提示中保持默认顺序；保存的版本不比节点新时没有附加提示
        (["ImportError", "cannot import name 'A2C2f' from 'ultralytics.nn.modules'"], "yolo12", "8.1.0",
         _TOO_OLD_26),
        (["TypeError", "ERROR ❌️ best.pt appears to be an Ultralytics YOLOv5 model originally trained"],
         "yolov8", None, "该模型由原版 YOLOv5 仓库（ultralytics/yolov5）训练"),
        (["TypeError", "ERROR ❌️ best.pt is not a loadable checkpoint — the file is empty"], "yolo11",
         None, "模型文件已损坏或不完整，无法加载（TypeError: best.pt is not a loadable checkpoint"),
        (["RuntimeError", "PytorchStreamReader failed reading zip archive: failed finding central"],
         "yolo11", None, "模型文件已损坏或不完整"),
        (["UnpicklingError", "invalid load key, '\\x00'."], "yolo11", None, "模型文件已损坏或不完整"),
        (["MemoryError", ""], "yolo11", None, "训练节点内存不足，无法加载该模型"),
        (["RuntimeError", "something odd"], "yolo11", "9.0.0",
         "加载模型失败：RuntimeError: something odd；该模型由 Ultralytics 9.0.0 保存，比训练节点的 8.2.103 新"),
        (["RuntimeError", "something odd"], "yolo11", "8.1.0", "加载模型失败：RuntimeError: something odd"),
    ],
)  # fmt: skip
def test_load_errors_are_translated(monkeypatch, tmp_path, error, version, saved, expected):
    install(monkeypatch, version="8.2.103", load_error=error)
    out = run_export(spec(tmp_path, yolo_version=version, saved_ultralytics_version=saved))
    assert not out["ok"]
    assert out["error"].startswith(expected), out["error"]
    if saved != "8.3.40" and saved != "9.0.0":
        assert "保存" not in out["error"]
    assert "Traceback" in out["detail"]


@pytest.mark.parametrize(
    ("error", "expected"),
    [
        (["MemoryError", ""], "训练节点内存不足，无法转换该模型（输入尺寸 640×640）"),
        (["RuntimeError", "[enforce fail at alloc_cpu.cpp:121] DefaultCPUAllocator: can't allocate memory"],
         "训练节点内存不足，无法转换该模型（输入尺寸 640×640）"),
        (["ValueError", "imgsz=[640, 64000] is not a valid image size."], "输入尺寸 640×640 不合法：ValueError"),
        (["RuntimeError", "Unsupported ONNX opset version: 21"],
         "无法按 opset 21 导出：RuntimeError: Unsupported ONNX opset version: 21。请选择其它 opset"),
        (["ModuleNotFoundError", "No module named 'onnxslim'", "onnxslim"],
         "训练节点缺少 ONNX 转换需要的 onnxslim"),
        (["RuntimeError", "tracing failed"], "导出 ONNX 失败：RuntimeError: tracing failed"),
    ],
)  # fmt: skip
def test_export_errors_are_translated(monkeypatch, tmp_path, error, expected):
    install(monkeypatch, export_error=error)
    opset = 21 if "opset" in error[1] else None
    out = run_export(spec(tmp_path, postprocess="raw", opset=opset))
    assert not out["ok"]
    assert out["error"].startswith(expected), out["error"]


def test_translate_with_unknown_versions():
    exc = AttributeError("Can't get attribute 'X' on <module 'ultralytics.nn.tasks'>")
    message = translate_load_error(exc, ExportSpec(weights="x"), "unknown")
    assert message.startswith("训练节点的 Ultralytics unknown 无法加载该模型（YOLO26 需要 8.4.0")
    assert translate_export_error(
        RuntimeError("opset 7"), ExportSpec(weights="x"), None
    ).startswith("无法按 opset 自动 导出")


@pytest.mark.parametrize(
    ("text", "expected"),
    [("8.4.198", (8, 4, 198)), ("8.3.0.dev1", (8, 3, 0)), ("v9", (9,)), ("", ()), (None, ())],
)
def test_version_tuple(text, expected):
    assert version_tuple(text) == expected


# ---- 转换参数 ----


def test_spec_round_trip():
    s = ExportSpec(weights="/w.pt", imgsz=(480, 640), dynamic=True, postprocess="nms", opset=12)
    assert ExportSpec.from_json(json.loads(json.dumps(s.to_json()))) == s
    assert ExportSpec.from_json({"weights": "/w.pt", "imgsz": 320}).imgsz == (320, 320)
    assert ExportSpec.from_json({"weights": "/w.pt", "task": "", "postprocess": None}).task is None


@pytest.mark.parametrize(
    "data",
    [
        [], {}, {"weights": ""}, {"weights": "x", "imgsz": [640]}, {"weights": "x", "imgsz": [0, 640]},
        {"weights": "x", "imgsz": True}, {"weights": "x", "dynamic": "yes"},
        {"weights": "x", "postprocess": "fast"}, {"weights": "x", "opset": "18"},
        {"weights": "x", "opset": 0}, {"weights": "x", "task": 3},
    ],
)  # fmt: skip
def test_spec_validation(data):
    with pytest.raises(ValueError):
        ExportSpec.from_json(data)


# ---- 子进程入口（python -m trainmaster_trainer.model_export）----


def child_env(tmp_path: Path, **config: Any) -> dict[str, str]:
    fakes_dir = export_fakes.write_modules(tmp_path / "fake-ml", onnxruntime=True)
    env = {k: v for k, v in os.environ.items() if not k.startswith(("TM_", "YOLO_"))}
    env.update(
        PYTHONPATH=os.pathsep.join([str(fakes_dir), str(TRAINER_DIR)]),
        FAKE_EXPORT=json.dumps(config),
    )
    return env


def run_child(tmp_path: Path, spec_json: str, **config: Any) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-m", "trainmaster_trainer.model_export", "--spec", spec_json],
        capture_output=True,
        text=True,
        env=child_env(tmp_path, **config),
        timeout=60,
    )


def test_child_main_prints_only_the_result_json(tmp_path):
    s = spec(tmp_path, yolo_version="yolo26", postprocess="end2end")
    proc = run_child(tmp_path, json.dumps(s.to_json()), end2end_head=True)
    assert proc.returncode == 0, proc.stderr
    lines = proc.stdout.splitlines()
    assert len(lines) == 1  # ultralytics 与 C 扩展打印的内容都转到了 stderr
    data = json.loads(lines[0])
    assert data["ok"] is True and data["result"]["end2end"] is True
    assert "C extension writes to fd 1" in proc.stderr
    assert "ultralytics prints its export log" in proc.stderr


def test_child_main_failure_exit_code(tmp_path):
    s = spec(tmp_path, yolo_version="yolo11", postprocess="end2end")
    proc = run_child(tmp_path, json.dumps(s.to_json()))
    assert proc.returncode == 1
    data = json.loads(proc.stdout.splitlines()[-1])
    assert data["ok"] is False and "end2end" in data["error"]


@pytest.mark.parametrize("spec_json", ["not json", '{"weights": ""}', "[]"])
def test_child_main_invalid_spec(tmp_path, spec_json):
    proc = run_child(tmp_path, spec_json)
    assert proc.returncode == 1
    data = json.loads(proc.stdout.splitlines()[-1])
    assert data["error"].startswith("转换参数不合法：")


# ---- 父进程：ExportProcess ----


def script(tmp_path: Path, body: str) -> list[str]:
    path = tmp_path / "child.py"
    path.write_text(textwrap.dedent(body), encoding="utf-8")
    return [sys.executable, str(path)]


def process(tmp_path: Path, command: list[str] | None = None, timeout: float = 60) -> ExportProcess:
    return ExportProcess(state_dir=tmp_path / "state", timeout=timeout, command=command)


def test_process_runs_the_real_child_module(tmp_path, monkeypatch):
    fakes_dir = export_fakes.write_modules(tmp_path / "fake-ml")
    monkeypatch.setenv("PYTHONPATH", str(fakes_dir))
    monkeypatch.setenv("FAKE_EXPORT", json.dumps({"end2end_head": True}))
    out = process(tmp_path).run(spec(tmp_path, yolo_version="yolo26", postprocess="end2end"))
    assert out["ok"], out
    assert out["result"]["outputs"][0]["shape"] == [1, 300, 6]
    assert (tmp_path / "state" / "ultralytics").is_dir()


def test_process_environment(tmp_path, monkeypatch):
    monkeypatch.setenv("CUDA_VISIBLE_DEVICES", "0,1")
    monkeypatch.delenv("YOLO_VERBOSE", raising=False)
    cmd = script(
        tmp_path,
        """
        import json, os, sys
        keys = ("CUDA_VISIBLE_DEVICES", "YOLO_OFFLINE", "YOLO_CONFIG_DIR", "YOLO_AUTOINSTALL",
                "YOLO_VERBOSE", "TM_SELF_TEST")
        env = {k: os.environ.get(k) for k in keys}
        spec = json.loads(sys.argv[sys.argv.index("--spec") + 1])
        print(json.dumps({"ok": True, "onnx_path": spec["weights"], "result": {"env": env, "cwd": os.getcwd()}}))
        """,
    )
    out = process(tmp_path, cmd).run(spec(tmp_path), cwd=tmp_path)
    assert out["ok"], out
    assert out["result"]["env"] == {
        "CUDA_VISIBLE_DEVICES": "",  # 在 CPU 上转换，不占用显存
        "YOLO_OFFLINE": "1",
        "YOLO_CONFIG_DIR": str(tmp_path / "state" / "ultralytics"),
        "YOLO_AUTOINSTALL": "False",
        "YOLO_VERBOSE": "True",
        "TM_SELF_TEST": os.environ.get("TM_SELF_TEST"),
    }
    assert out["result"]["cwd"] == str(tmp_path)


def test_process_failure_detail_includes_stderr(tmp_path):
    cmd = script(
        tmp_path,
        """
        import json, sys
        print("ONNX: starting export with onnx 1.23.2 opset 18...", file=sys.stderr)
        print(json.dumps({"ok": False, "error": "导出 ONNX 失败：x", "detail": "Traceback: boom"}))
        sys.exit(1)
        """,
    )
    out = process(tmp_path, cmd).run(spec(tmp_path))
    assert out == {
        "ok": False,
        "error": "导出 ONNX 失败：x",
        "detail": "Traceback: boom\n\nONNX: starting export with onnx 1.23.2 opset 18...",
        "cancelled": False,
    }


def test_process_nonzero_exit_without_json(tmp_path):
    cmd = script(
        tmp_path, 'import sys\nprint("Segmentation fault", file=sys.stderr)\nsys.exit(139)\n'
    )
    out = process(tmp_path, cmd).run(spec(tmp_path))
    assert out["error"] == "转换进程异常退出（退出码 139）"
    assert "Segmentation fault" in out["detail"]


def test_process_killed_by_signal(tmp_path):
    cmd = script(tmp_path, "import os, signal\nos.kill(os.getpid(), signal.SIGKILL)\n")
    out = process(tmp_path, cmd).run(spec(tmp_path))
    assert out["error"].startswith("转换进程被信号 SIGKILL 终止（可能是内存不足）")


@pytest.mark.parametrize(
    "output", ["not json", json.dumps({"ok": True}), json.dumps([1]), json.dumps({"ok": "yes"})]
)
def test_process_malformed_output(tmp_path, output):
    out = process(tmp_path, script(tmp_path, f"print({output!r})\n")).run(spec(tmp_path))
    assert not out["ok"]
    assert "输出" in out["error"]


def test_process_timeout_kills_process_group(tmp_path):
    marker = tmp_path / "grandchild-alive"
    cmd = script(
        tmp_path,
        f"""
        import subprocess, sys, time
        subprocess.Popen([sys.executable, "-c",
            "import time, pathlib; time.sleep(3); pathlib.Path({str(marker)!r}).write_text('x')"])
        print("exporting", file=sys.stderr, flush=True)
        time.sleep(30)
        """,
    )
    started = time.monotonic()
    out = process(tmp_path, cmd, timeout=1).run(spec(tmp_path))
    assert time.monotonic() - started < 10
    assert out["error"].startswith("转换超时（1 秒内没有完成）")
    assert "TM_EXPORT_TIMEOUT" in out["error"]
    assert "exporting" in out["detail"]
    assert out["cancelled"] is False
    time.sleep(3.5)
    assert not marker.exists()


def test_process_cancel(tmp_path):
    proc = process(tmp_path, script(tmp_path, "import time\ntime.sleep(30)\n"))
    result: dict[str, Any] = {}
    thread = threading.Thread(target=lambda: result.update(out=proc.run(spec(tmp_path))))
    thread.start()
    deadline = time.monotonic() + 10
    while proc._proc is None and time.monotonic() < deadline:
        time.sleep(0.02)
    proc.cancel()
    thread.join(timeout=10)
    assert not thread.is_alive()
    assert result["out"]["cancelled"] is True and result["out"]["error"] == "转换已取消"
    # 取消后不再启动新的子进程
    assert proc.run(spec(tmp_path))["cancelled"] is True


def test_process_command_not_found(tmp_path):
    out = process(tmp_path, [str(tmp_path / "missing-python")]).run(spec(tmp_path))
    assert out["error"].startswith("无法启动转换进程")


def test_real_child_module_without_ultralytics(tmp_path, monkeypatch):
    """默认命令在没有 ultralytics 的测试环境中也给出中文说明。"""
    monkeypatch.delenv("PYTHONPATH", raising=False)
    out = process(tmp_path).run(spec(tmp_path))
    if out["ok"]:
        pytest.skip("ultralytics is installed in the test environment")
    assert out["error"].startswith("训练节点无法导入 Ultralytics")


def test_export_env_keeps_explicit_verbosity(tmp_path):
    env = model_export.export_env(tmp_path, base={"YOLO_VERBOSE": "False", "PATH": "/bin"})
    assert env["YOLO_VERBOSE"] == "False"
    assert env["CUDA_VISIBLE_DEVICES"] == ""
    assert "TM_SELF_TEST" not in env


# ---- 命令行 export ----


def run_cli(tmp_path: Path, *args: str, **config: Any) -> subprocess.CompletedProcess:
    env = child_env(tmp_path, **config)
    env.update(
        HOME=str(tmp_path / "home"),
        TM_STATE_DIR=str(tmp_path / "state"),
        TM_DATA_DIR=str(tmp_path),
    )
    return subprocess.run(
        [sys.executable, "-m", "trainmaster_trainer", "export", *args],
        capture_output=True,
        text=True,
        env=env,
        timeout=120,
    )


def test_cli_export_summary(tmp_path):
    w = weights(tmp_path)
    proc = run_cli(tmp_path, "--weights", w, "--imgsz", "480x640", "--postprocess", "nms")
    assert proc.returncode == 0, proc.stderr
    lines = proc.stdout.splitlines()
    assert lines[0].startswith(f"转换成功：{tmp_path / 'model.onnx'}（")
    assert "  输入：images [1, 3, 480, 640] float32" in lines
    assert "  输出：output0 [1, 300, 6] float32" in lines
    assert "  opset 18 · 嵌入 NMS（nms） · 固定尺寸 · 已用 ONNX Runtime 1.23.2 验证" in lines
    assert "  版本：Ultralytics 8.4.198 · PyTorch 2.14.1+cpu · onnx 1.23.2" in lines


def test_cli_export_json_and_output(tmp_path):
    w = weights(tmp_path)
    target = tmp_path / "exported" / "best.onnx"
    proc = run_cli(
        tmp_path, "--weights", w, "--output", str(target), "--dynamic", "--opset", "12", "--json",
        end2end_head=True,
    )  # fmt: skip
    assert proc.returncode == 0, proc.stderr
    data = json.loads(proc.stdout)
    assert data["ok"] is True and data["onnx_path"] == str(target)
    assert data["result"]["end2end"] is True  # 端到端模型默认 end2end
    assert data["result"]["opset"] == 12 and data["result"]["dynamic"] is True
    assert target.is_file()


def test_cli_export_failure(tmp_path):
    w = weights(tmp_path)
    proc = run_cli(tmp_path, "--weights", w, "--postprocess", "end2end")
    assert proc.returncode == 1
    assert proc.stdout == ""
    assert proc.stderr.startswith("转换失败：该模型没有端到端（NMS-free）检测头")


@pytest.mark.parametrize(
    "args",
    [["--imgsz", "500"], ["--imgsz", "640x"], ["--imgsz", "16"], ["--opset", "9"], ["--opset", "x"],
     ["--postprocess", "fast"], []],
)  # fmt: skip
def test_cli_export_invalid_arguments(tmp_path, args):
    if args:
        args = ["--weights", weights(tmp_path), *args]
    proc = run_cli(tmp_path, *args)
    assert proc.returncode == 2
    assert "usage:" in proc.stderr


def test_parse_imgsz():
    from trainmaster_trainer.__main__ import parse_imgsz

    assert parse_imgsz("640") == (640, 640)
    assert parse_imgsz("480x640") == (480, 640)
    assert parse_imgsz("480,640") == (480, 640)
    assert parse_imgsz("480×640") == (480, 640)
