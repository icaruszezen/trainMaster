"""审图识别测试用的假模块：ultralytics（YOLO 的加载与 predict）、torch、numpy、cv2、PIL（Pillow）。

假图片是一个小的文本文件：
- “FAKEIMG <宽>x<高> [标签]”：OpenCV 能读（假 cv2.imdecode 返回带形状与标签的 FakeImage）；
- “GIF89a <宽>x<高> [标签]”：OpenCV 读不了，Pillow 能读（与真实的 opencv-python 读不了 GIF 一样）；
- “TRUNCATED”：两者都读不了，Pillow 报“文件不完整”；其它内容两者都不认识（损坏的文件）。
假的 predict 按图片的标签返回配置中的框（boxes[标签]，没有时为 default_boxes）；标签 boom / oom / gpu /
slow 分别让 predict 抛出普通错误、显存不足、CUDA 错误或先等待 slow_seconds 秒。

子进程测试通过 PYTHONPATH 上的同名模块文件加载这些假模块，配置放在同一目录的 fake_inference.json 中
（识别子进程只继承运行 Python 需要的环境变量，见 review_inference.inference_env）。
"""

from __future__ import annotations

import json
import os
import sys
import time
import types
from pathlib import Path
from typing import Any

TESTS_DIR = Path(__file__).resolve().parent
CONFIG_FILE = "fake_inference.json"
SAFE_LOAD_ENV = "ULTRALYTICS_SAFE_LOAD"
COCO_NAMES = ["person", "bicycle", "car"]

_MODULE_FILE = """\
import sys
sys.path.insert(0, {tests!r})
import inference_fakes
_mod = inference_fakes.{factory}(**inference_fakes.load_config({root!r}))
sys.modules[__name__] = _mod
sys.modules.update(getattr(_mod, "submodules", {{}}))
"""
_FACTORIES = {
    "ultralytics": "make_ultralytics",
    "torch": "make_torch",
    "numpy": "make_numpy",
    "cv2": "make_cv2",
    "PIL": "make_pil",
}


def load_config(root: str) -> dict[str, Any]:
    path = Path(root) / CONFIG_FILE
    return json.loads(path.read_text(encoding="utf-8")) if path.is_file() else {}


def write_config(root: Path, **config: Any) -> None:
    """设置假模块的行为（子进程启动时读取）。"""
    (root / CONFIG_FILE).write_text(json.dumps(config), encoding="utf-8")


def write_modules(root: Path, *, without: tuple[str, ...] = (), **config: Any) -> Path:
    """写出假模块文件与配置（write_config），返回要放到 PYTHONPATH 上的目录。without 中的模块写成导入时
    抛出 ImportError 的文件（模拟没有安装）。"""
    root.mkdir(parents=True, exist_ok=True)
    write_config(root, **config)
    for name, factory in _FACTORIES.items():
        if name in without:
            text = f"raise ImportError('No module named {name}')\n"
        else:
            text = _MODULE_FILE.format(tests=str(TESTS_DIR), factory=factory, root=str(root))
        (root / f"{name}.py").write_text(text, encoding="utf-8")
    return root


def fake_image(width: int, height: int, tag: str = "", fmt: str = "FAKEIMG") -> bytes:
    return f"{fmt} {width}x{height} {tag}".strip().encode()


def _record(calls_file: str | None, item: dict[str, Any]) -> None:
    if calls_file:
        with open(calls_file, "a", encoding="utf-8") as f:
            f.write(json.dumps(item) + "\n")


class FakeImage:
    """假的图片数组：shape 与标签（predict 按标签决定结果）。"""

    def __init__(self, shape: tuple[int, ...], tag: str = "", dtype: str = "uint8") -> None:
        self.shape = shape
        self.tag = tag
        self.dtype = dtype


class FakeBuffer:
    def __init__(self, data: bytes) -> None:
        self.data = data
        self.size = len(data)


def _parse(data: bytes, fmt: str) -> tuple[int, int, str] | None:
    parts = data.decode("utf-8", "replace").split()
    if len(parts) < 2 or parts[0] != fmt or "x" not in parts[1]:
        return None
    w, _, h = parts[1].partition("x")
    return int(w), int(h), (parts[2] if len(parts) > 2 else "")


def make_numpy(numpy_version: str = "2.3.4", **_: Any) -> types.ModuleType:
    mod = types.ModuleType("numpy")
    mod.__version__ = numpy_version  # type: ignore[attr-defined]
    mod.uint8 = "uint8"  # type: ignore[attr-defined]

    def frombuffer(data: bytes, dtype: Any = None) -> FakeBuffer:
        return FakeBuffer(bytes(data))

    def zeros(shape: Any, dtype: Any = None) -> FakeImage:
        return FakeImage(tuple(shape), "warmup")

    def asarray(value: Any) -> FakeImage:
        return FakeImage((value.size[1], value.size[0], 3), value.tag)

    mod.frombuffer = frombuffer  # type: ignore[attr-defined]
    mod.zeros = zeros  # type: ignore[attr-defined]
    mod.asarray = asarray  # type: ignore[attr-defined]
    return mod


def make_cv2(
    cv2_version: str = "4.12.0", calls_file: str | None = None, cv2_raises: bool = False, **_: Any
) -> types.ModuleType:
    mod = types.ModuleType("cv2")
    mod.__version__ = cv2_version  # type: ignore[attr-defined]
    mod.IMREAD_COLOR = 1  # type: ignore[attr-defined]
    mod.IMREAD_IGNORE_ORIENTATION = 128  # type: ignore[attr-defined]
    mod.COLOR_RGB2BGR = 4  # type: ignore[attr-defined]

    def imdecode(buf: FakeBuffer, flags: int) -> FakeImage | None:
        _record(calls_file, {"call": "imdecode", "flags": flags})
        if cv2_raises:
            raise RuntimeError("OpenCV(4.12.0) imdecode_: image is too big")
        parsed = _parse(buf.data, "FAKEIMG")
        if parsed is None:
            return None
        w, h, tag = parsed
        return FakeImage((h, w, 3), tag)

    def cvtColor(image: FakeImage, code: int) -> FakeImage:  # noqa: N802 - OpenCV 的接口
        _record(calls_file, {"call": "cvtColor", "code": code})
        return FakeImage(image.shape, image.tag)

    mod.imdecode = imdecode  # type: ignore[attr-defined]
    mod.cvtColor = cvtColor  # type: ignore[attr-defined]
    return mod


def make_pil(calls_file: str | None = None, **_: Any) -> types.ModuleType:
    pil = types.ModuleType("PIL")
    pil.__path__ = []  # type: ignore[attr-defined]
    image_mod = types.ModuleType("PIL.Image")

    class UnidentifiedImageError(OSError):
        pass

    class FakePILImage:
        def __init__(self, size: tuple[int, int], tag: str, mode: str = "P") -> None:
            self.size = size
            self.tag = tag
            self.mode = mode

        def __enter__(self) -> FakePILImage:
            return self

        def __exit__(self, *exc: Any) -> None:
            pass

        def convert(self, mode: str) -> FakePILImage:
            _record(calls_file, {"call": "convert", "mode": mode})
            return FakePILImage(self.size, self.tag, mode)

    def open_image(fp: Any) -> FakePILImage:
        data = fp.read() if hasattr(fp, "read") else Path(fp).read_bytes()
        _record(calls_file, {"call": "Image.open"})
        if data.startswith(b"TRUNCATED"):
            raise OSError("image file is truncated (3 bytes not processed)")
        parsed = _parse(data, "GIF89a")
        if parsed is None:
            raise UnidentifiedImageError(f"cannot identify image file {fp!r}")
        w, h, tag = parsed
        return FakePILImage((w, h), tag)

    image_mod.open = open_image  # type: ignore[attr-defined]
    image_mod.UnidentifiedImageError = UnidentifiedImageError  # type: ignore[attr-defined]
    pil.Image = image_mod  # type: ignore[attr-defined]
    pil.UnidentifiedImageError = UnidentifiedImageError  # type: ignore[attr-defined]
    pil.submodules = {"PIL.Image": image_mod}  # type: ignore[attr-defined]
    return pil


def make_torch(
    torch_version: str = "2.14.1+cpu",
    cuda: bool = False,
    device_name: str = "NVIDIA GeForce RTX 4090",
    **_: Any,
) -> types.ModuleType:
    mod = types.ModuleType("torch")
    mod.__version__ = torch_version  # type: ignore[attr-defined]

    class OutOfMemoryError(RuntimeError):
        pass

    mod.cuda = types.SimpleNamespace(  # type: ignore[attr-defined]
        is_available=lambda: cuda,
        get_device_name=lambda index=0: device_name,
        OutOfMemoryError=OutOfMemoryError,
    )
    return mod


class FakeTensor:
    def __init__(self, values: list[Any]) -> None:
        self.values = values

    def tolist(self) -> list[Any]:
        return list(self.values)


class FakeBoxes:
    def __init__(self, rows: list[list[float]]) -> None:
        self.xyxy = FakeTensor([r[:4] for r in rows])
        self.conf = FakeTensor([r[4] for r in rows])
        self.cls = FakeTensor([float(r[5]) for r in rows])


def make_ultralytics(
    *,
    version: str = "8.4.198",
    task: str = "detect",
    names: list[str] | None = None,
    boxes: dict[str, list[list[float]]] | None = None,
    default_boxes: list[list[float]] | None = None,
    load_error: list[str] | None = None,
    warmup_error: list[str] | None = None,
    slow_seconds: float = 30.0,
    calls_file: str | None = None,
    safe_load: bool | None = None,
    safe_load_class: bool = True,
    **_: Any,
) -> types.ModuleType:
    """一个只实现识别用到的接口的 ultralytics。load_error / warmup_error：[异常类名, 消息]；calls_file：
    把调用记录追加到这个文件（子进程测试读取）。安全加载与 export_fakes 相同：SAFE_LOAD 在创建（导入）时
    读取环境变量 ULTRALYTICS_SAFE_LOAD，safe_load 不为 None 时用它。"""
    import builtins

    mod = types.ModuleType("ultralytics")
    mod.__version__ = version  # type: ignore[attr-defined]
    mod.__path__ = []  # type: ignore[attr-defined]
    try:
        import torch  # 子进程中是 PYTHONPATH 上的假 torch；进程内的测试先把假 torch 放进 sys.modules
    except ImportError:
        torch = make_torch()
        sys.modules["torch"] = torch

    if safe_load is None:
        safe_load = os.environ.get(SAFE_LOAD_ENV, "").strip().lower() in ("1", "true", "yes", "on")
    nn = types.ModuleType("ultralytics.nn")
    nn.__path__ = []  # type: ignore[attr-defined]
    tasks = types.ModuleType("ultralytics.nn.tasks")
    tasks.SAFE_LOAD = safe_load  # type: ignore[attr-defined]
    if safe_load_class:
        tasks._SafeLoad = type("_SafeLoad", (), {"SUPPORTED": True})  # type: ignore[attr-defined]
    nn.tasks = tasks  # type: ignore[attr-defined]
    mod.nn = nn  # type: ignore[attr-defined]
    mod.submodules = {"ultralytics.nn": nn, "ultralytics.nn.tasks": tasks}  # type: ignore[attr-defined]
    calls: list[Any] = []
    mod.calls = calls  # type: ignore[attr-defined]

    def record(item: dict[str, Any]) -> None:
        calls.append(item)
        _record(calls_file, item)

    def error(spec: list[str]) -> BaseException:
        cls = getattr(builtins, spec[0], None) or RuntimeError
        if spec[0] == "OutOfMemoryError":
            cls = torch.cuda.OutOfMemoryError
        return cls(spec[1])

    default_task = task

    class Result:
        def __init__(self, rows: list[list[float]], obb: bool) -> None:
            self.boxes = None if obb else FakeBoxes(rows)
            self.obb = FakeBoxes(rows) if obb else None

    class YOLO:
        def __init__(self, weights: str, task: str | None = None) -> None:
            record({"call": "YOLO", "weights": weights, "task": task, "safe_load": tasks.SAFE_LOAD})
            print("ultralytics prints to stdout while loading")
            os.write(1, b"C extension writes to fd 1\n")
            if load_error:
                raise error(load_error)
            self.task = task or default_task
            self.names = dict(enumerate(names if names is not None else COCO_NAMES))

        def predict(self, source: FakeImage, **kwargs: Any) -> list[Result]:
            record({"call": "predict", "tag": source.tag, "shape": list(source.shape), **kwargs})
            tag = source.tag
            if tag == "warmup":
                if warmup_error:
                    raise error(warmup_error)
                return [Result([], self.task == "obb")]
            if tag == "boom":
                raise ValueError("boom")
            if tag == "oom":
                raise torch.cuda.OutOfMemoryError("CUDA out of memory. Tried to allocate 2.00 GiB")
            if tag == "gpu":
                raise RuntimeError("CUDA error: an illegal memory access was encountered")
            if tag == "slow":
                time.sleep(slow_seconds)
            rows = (boxes or {}).get(tag, default_boxes or [])
            return [Result(rows, self.task == "obb")]

    mod.YOLO = YOLO  # type: ignore[attr-defined]
    return mod
