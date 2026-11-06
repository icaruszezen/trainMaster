"""模型转换测试用的假模块：ultralytics（YOLO 的加载与 export）、onnx、onnxruntime、numpy、torch。

假的 export 按 ultralytics 8.4.198 实测的规则（docs/architecture.md、设计文档 3.8 节）生成结果，写出的
“onnx 文件”是一个 JSON 描述（输入输出、opset、metadata），假的 onnx / onnxruntime 读取它。
子进程测试通过 PYTHONPATH 上的同名模块文件加载这些假模块，配置放在同一目录的 fake_export.json 中
（不用环境变量：转换子进程只继承运行 Python 需要的环境变量，见 model_export.export_env）。
"""

from __future__ import annotations

import json
import os
import sys
import types
from pathlib import Path
from typing import Any

TESTS_DIR = Path(__file__).resolve().parent
CONFIG_FILE = "fake_export.json"

_MODULE_FILE = """\
import sys
sys.path.insert(0, {tests!r})
import export_fakes
_mod = export_fakes.{factory}(**export_fakes.load_config({root!r}))
sys.modules[__name__] = _mod
sys.modules.update(getattr(_mod, "submodules", {{}}))
"""
SAFE_LOAD_ENV = "ULTRALYTICS_SAFE_LOAD"


def load_config(root: str) -> dict[str, Any]:
    path = Path(root) / CONFIG_FILE
    return json.loads(path.read_text(encoding="utf-8")) if path.is_file() else {}


def write_config(root: Path, **config: Any) -> None:
    """设置假模块的行为（子进程启动时读取）。"""
    (root / CONFIG_FILE).write_text(json.dumps(config), encoding="utf-8")


def write_modules(
    root: Path, *, onnxruntime: bool = True, onnx: bool = True, **config: Any
) -> Path:
    """写出假模块文件与配置（write_config），返回要放到 PYTHONPATH 上的目录。"""
    root.mkdir(parents=True, exist_ok=True)
    write_config(root, **config)
    modules = {"ultralytics": "make_ultralytics", "numpy": "make_numpy", "torch": "make_torch"}
    if onnx:
        modules["onnx"] = "make_onnx"
    else:
        (root / "onnx.py").write_text(
            "raise ImportError('No module named onnx')\n", encoding="utf-8"
        )
    if onnxruntime:
        modules["onnxruntime"] = "make_onnxruntime"
    else:
        (root / "onnxruntime.py").write_text(
            "raise ImportError('No module named onnxruntime')\n", encoding="utf-8"
        )
    for name, factory in modules.items():
        (root / f"{name}.py").write_text(
            _MODULE_FILE.format(tests=str(TESTS_DIR), factory=factory, root=str(root)),
            encoding="utf-8",
        )
    return root


def _anchors(h: int, w: int) -> int:
    return sum((h // s) * (w // s) for s in (8, 16, 32))


def describe(task: str, nc: int, h: int, w: int, *, dynamic: bool, end2end: bool, nms: bool):
    """ultralytics 导出的输入输出（与真实结果相同的形状）。"""
    batch: Any = "batch" if dynamic else 1
    inputs = [
        {"name": "images", "shape": [batch, 3, "height", "width"] if dynamic else [1, 3, h, w]}
    ]
    anchors: Any = "anchors" if dynamic else _anchors(h, w)
    if task == "classify":
        outputs = [{"name": "output0", "shape": [batch, nc]}]
    elif end2end or nms:
        extra = {"detect": 0, "segment": 32, "pose": 51, "obb": 1}.get(task, 0)
        outputs = [{"name": "output0", "shape": [batch, 300, 6 + extra]}]
    else:
        extra = {"detect": 0, "segment": 32, "pose": 51, "obb": 1}.get(task, 0)
        outputs = [{"name": "output0", "shape": [batch, 4 + nc + extra, anchors]}]
    if task == "segment":
        outputs.append(
            {"name": "output1", "shape": [batch, 32, "mask_height", "mask_width"]}
            if dynamic
            else {"name": "output1", "shape": [1, 32, h // 4, w // 4]}
        )
    return inputs, outputs


def make_torch(version: str = "2.14.1+cpu", **_: Any) -> types.ModuleType:
    mod = types.ModuleType("torch")
    mod.__version__ = version  # type: ignore[attr-defined]
    return mod


def make_ultralytics(
    *,
    version: str = "8.4.198",
    task: str = "detect",
    end2end_head: bool = False,
    nc: int = 2,
    train_imgsz: int = 640,
    load_error: list[str] | None = None,
    load_cause: list[str] | None = None,
    export_error: list[str] | None = None,
    ignore_nms: bool = False,
    no_file: bool = False,
    bad_onnx: bool = False,
    nan_output: bool = False,
    ort_error: str | None = None,
    wrong_shape: bool = False,
    no_metadata: bool = False,
    calls_file: str | None = None,
    safe_load: bool | None = None,
    safe_load_class: bool = True,
    safe_load_supported: bool = True,
    **_: Any,
) -> types.ModuleType:
    """一个只实现转换用到的接口的 ultralytics。load_error / export_error：[异常类名, 消息]（可带
    ModuleNotFoundError 的 name 作为第三项），load_cause：load_error 的 __cause__（同样的写法）；
    calls_file：把调用记录追加到这个文件（子进程测试读取）。

    安全加载（ultralytics.nn.tasks，见 submodules）：SAFE_LOAD 与真实的一样在创建（导入）时读取环境变量
    ULTRALYTICS_SAFE_LOAD，safe_load 不为 None 时用它；safe_load_class=False 时没有 _SafeLoad（旧版），
    safe_load_supported 为 _SafeLoad.SUPPORTED（torch 是否支持）。"""
    mod = types.ModuleType("ultralytics")
    mod.__version__ = version  # type: ignore[attr-defined]
    mod.__path__ = []  # type: ignore[attr-defined]  # 包：ultralytics.nn.tasks 是它的子模块
    calls: list[Any] = []
    mod.calls = calls  # type: ignore[attr-defined]
    sys.modules.setdefault("torch", make_torch())

    if safe_load is None:
        flag = os.environ.get(SAFE_LOAD_ENV, "").strip().lower()
        safe_load = flag in ("1", "true", "yes", "on", "y", "t")
    nn = types.ModuleType("ultralytics.nn")
    nn.__path__ = []  # type: ignore[attr-defined]
    tasks = types.ModuleType("ultralytics.nn.tasks")
    tasks.SAFE_LOAD = safe_load  # type: ignore[attr-defined]
    if safe_load_class:
        tasks._SafeLoad = type("_SafeLoad", (), {"SUPPORTED": safe_load_supported})  # type: ignore[attr-defined]
    nn.tasks = tasks  # type: ignore[attr-defined]
    mod.nn = nn  # type: ignore[attr-defined]
    # 测试放进 sys.modules 的子模块（真实的 import 会登记它们）
    mod.submodules = {"ultralytics.nn": nn, "ultralytics.nn.tasks": tasks}  # type: ignore[attr-defined]

    def record(item: Any) -> None:
        calls.append(item)
        if calls_file:
            with open(calls_file, "a", encoding="utf-8") as f:
                f.write(json.dumps(item) + "\n")

    def build_error(spec: list[str]) -> BaseException:
        import builtins
        import pickle

        name, message = spec[0], spec[1]
        cls = getattr(builtins, name, None) or getattr(pickle, name, None) or RuntimeError
        exc = cls(message)
        if isinstance(exc, ModuleNotFoundError) and len(spec) > 2:
            exc.name = spec[2]
        return exc

    class Head:
        def __init__(self) -> None:
            if end2end_head:
                self.one2one_cv2 = object()

    class YOLO:
        def __init__(self, weights: str, task: str | None = None) -> None:
            record({"call": "YOLO", "weights": weights, "task": task, "safe_load": tasks.SAFE_LOAD})
            print("ultralytics prints to stdout while loading")
            if load_error:
                raise build_error(load_error) from (build_error(load_cause) if load_cause else None)
            self.task = task or default_task
            self.model = types.SimpleNamespace(model=[object(), Head()])
            self.weights = weights

        def export(self, **kwargs: Any) -> str | None:
            record({"call": "export", **kwargs})
            print("ultralytics prints its export log to stdout")
            os.write(1, b"C extension writes to fd 1\n")
            if export_error:
                raise build_error(export_error)
            nms = kwargs.get("nms")
            if nms and self.task == "classify":
                raise AssertionError("'nms=True' is not valid for classification models.")
            # 规则同 ultralytics 8.4.198：nms=False 只对带 one2one 头的模型生效（否则静默忽略），
            # nms=True 打开 NMSModel（端到端头随之关闭）。
            end2end = nms is False and end2end_head
            embedded = bool(nms) and not ignore_nms
            imgsz = kwargs.get("imgsz") or [train_imgsz, train_imgsz]
            h, w = imgsz
            dynamic = bool(kwargs.get("dynamic"))
            inputs, outputs = describe(
                self.task, nc, h, w, dynamic=dynamic, end2end=end2end, nms=embedded
            )
            if wrong_shape:
                inputs[0]["shape"] = [1, 3, h + 32, w]
            args = {
                "batch": kwargs.get("batch"),
                "nms": True if embedded else (None if nms or nms is None else False),
                "dynamic": dynamic,
                "simplify": kwargs.get("simplify"),
                "opset": kwargs.get("opset"),
            }
            metadata = (
                {}
                if no_metadata
                else {
                    "task": self.task,
                    "imgsz": str([h, w]),
                    "args": str(args),
                    "end2end": str(end2end),
                }
            )
            if no_file:
                return None
            out = Path(self.weights).with_suffix(".onnx")
            out.write_text(
                json.dumps(
                    {
                        "opset": kwargs.get("opset") or 18,
                        "inputs": inputs,
                        "outputs": outputs,
                        "metadata": metadata,
                        "invalid": bad_onnx,
                        "nan": nan_output,
                        "ort_error": ort_error,
                    }
                ),
                encoding="utf-8",
            )
            return str(out)

    default_task = task
    mod.YOLO = YOLO  # type: ignore[attr-defined]
    return mod


class _Dim:
    def __init__(self, value: Any) -> None:
        self.dim_param = value if isinstance(value, str) else ""
        self.dim_value = value if isinstance(value, int) else 0


def _value_info(item: dict[str, Any]) -> Any:
    tensor_type = types.SimpleNamespace(
        elem_type=item.get("elem_type", 1),
        shape=types.SimpleNamespace(dim=[_Dim(d) for d in item["shape"]]),
    )
    return types.SimpleNamespace(
        name=item["name"], type=types.SimpleNamespace(tensor_type=tensor_type)
    )


def make_onnx(version: str = "1.23.2", **_: Any) -> types.ModuleType:
    mod = types.ModuleType("onnx")
    mod.__version__ = version  # type: ignore[attr-defined]

    class ValidationError(Exception):
        pass

    def read(path: str) -> dict[str, Any]:
        return json.loads(Path(path).read_text(encoding="utf-8"))

    def check_model(path: str) -> None:
        if read(path).get("invalid"):
            raise ValidationError("Field 'shape' of 'type' is required but missing.")

    def load(path: str, load_external_data: bool = True) -> Any:
        data = read(path)
        weight = {"name": "model.0.conv.weight", "shape": [16, 3, 3, 3]}
        return types.SimpleNamespace(
            opset_import=[
                types.SimpleNamespace(domain="ai.onnx.ml", version=3),
                types.SimpleNamespace(domain="", version=data["opset"]),
            ],
            graph=types.SimpleNamespace(
                # 旧版导出把初始值也列在输入中：结果里不应出现
                input=[_value_info(i) for i in data["inputs"]] + [_value_info(weight)],
                output=[_value_info(o) for o in data["outputs"]],
                initializer=[types.SimpleNamespace(name=weight["name"])],
            ),
            metadata_props=[
                types.SimpleNamespace(key=k, value=v) for k, v in data["metadata"].items()
            ],
        )

    mod.load = load  # type: ignore[attr-defined]
    mod.checker = types.SimpleNamespace(  # type: ignore[attr-defined]
        check_model=check_model, ValidationError=ValidationError
    )
    return mod


class FakeArray:
    def __init__(self, shape: list[int], dtype: str = "float32", finite: bool = True) -> None:
        self.shape = shape
        self.dtype = types.SimpleNamespace(
            kind="f" if dtype.startswith("float") else "i", name=dtype
        )
        self.finite = finite


def make_numpy(**_: Any) -> types.ModuleType:
    mod = types.ModuleType("numpy")
    mod.float32, mod.float16, mod.float64 = "float32", "float16", "float64"  # type: ignore[attr-defined]
    mod.zeros = lambda shape, dtype="float64": FakeArray(list(shape), dtype)  # type: ignore[attr-defined]
    mod.asarray = lambda x: x  # type: ignore[attr-defined]
    mod.isfinite = lambda a: types.SimpleNamespace(all=lambda: a.finite)  # type: ignore[attr-defined]
    return mod


def make_onnxruntime(version: str = "1.23.2", **_: Any) -> types.ModuleType:
    mod = types.ModuleType("onnxruntime")
    mod.__version__ = version  # type: ignore[attr-defined]
    runs: list[dict[str, Any]] = []
    mod.runs = runs  # type: ignore[attr-defined]

    class InferenceSession:
        def __init__(self, path: str, providers: list[str] | None = None) -> None:
            self.data = json.loads(Path(path).read_text(encoding="utf-8"))
            self.providers = providers

        def get_inputs(self) -> list[Any]:
            return [
                types.SimpleNamespace(name=i["name"], shape=i["shape"], type="tensor(float)")
                for i in self.data["inputs"]
            ]

        def run(self, names: Any, feed: dict[str, FakeArray]) -> list[FakeArray]:
            runs.append({k: (v.shape, v.dtype.name) for k, v in feed.items()})
            if self.data.get("ort_error"):
                raise RuntimeError(self.data["ort_error"])
            return [FakeArray([1], finite=not self.data.get("nan")) for _ in self.data["outputs"]]

    mod.InferenceSession = InferenceSession  # type: ignore[attr-defined]
    return mod
