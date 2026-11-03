# Test fixtures

Ultralytics checkpoints for `checkpoint_test.go` and `fuzz_test.go`. The parser only reads `<root>/data.pkl`,
so every fixture keeps just `<root>/data.pkl` and `<root>/version` of the file torch wrote (the tensor data
in `<root>/data/N` is dropped) and the whole set stays around 1.3 MB.

| File | Content | What it covers |
|---|---|---|
| `yolov8n.pt`, `yolov9t.pt`, `yolov10n.pt`, `yolo11n.pt`, `yolo12n.pt`, `yolo26n.pt` | `YOLO("<name>.yaml").save(...)`: untrained detection models; `yolov8n.pt` is stored uncompressed like the files torch writes, the others are deflated | Version and scale from the yaml name, parameter counts, heads, `End2End` (yolov10, yolo26), `ImageSize` 640 from `model.args` |
| `yolo11n-seg.pt`, `yolo11n-cls.pt`, `yolo11n-pose.pt`, `yolo11n-obb.pt`, `yolo26n-seg.pt`, `yoloe-11n-seg.pt`, `yolov8n-worldv2.pt` | The same for the other tasks and heads | Task, head and model class of each task |
| `line-a-detector.pt`, `my-model.pt` | `yolo26.yaml` and `yolov8.yaml` copied under custom names | Version from the architecture, scale from the first `scales` key |
| `parts-cls.pt` | `yolo26-cls.yaml` copied under a custom name | No version from the architecture: `yolo11-cls.yaml` is the same network |
| `yolo11n-best.pt`, `yolo26n-best.pt` | `best.pt` of 2 epochs of training (imgsz 96, CPU) on a synthetic dataset with the classes `scratch` and `划痕-凹坑` | Class names, `ImageSize` 96, epochs, epoch -1, best fitness, metrics, `TrainData`, `BaseModel` |
| `yolo11n-last.pt` | `last.pt` after the first epoch, copied in `on_model_save` before Ultralytics strips the optimizer | `TrainingCheckpoint`, epoch 0 |
| `yolo11n-variants.pt` | `yolo11n-best.pt` re-saved with numpy scalars (as older Ultralytics versions save), a `PosixPath`, class names as a list, imgsz `[96, 128]`, a NaN, a string and an int in the metrics | numpy and pathlib reducers, value cleaning |
| `yolo26n-best-protocol0.pt`, `-protocol1.pt`, `-protocol4.pt`, `-protocol5.pt` | `yolo26n-best.pt` re-saved with pickle protocols 0, 1, 4 and 5 (torch uses 2) | The pickle interpreter (torch itself cannot load protocol 0). Protocols 0 and 1 pickle objects with `copyreg._reconstructor`, which safe loading refuses, so `Inspect` rejects them (`unsafe`) and the test checks their extraction directly |
| `rtdetr-l.pt` | `RTDETR("rtdetr-l.yaml")` | `unsupported_model` |
| `yolov5-repo.pt` | A tiny model with the class layout of the original YOLOv5 repository (`models.yolo.DetectionModel`, `models.common.Conv`) | `yolov5_repo` |
| `state-dict.pt`, `state-dict-wrapped.pt` | `model.state_dict()`, and `{"model": OrderedDict of tensors}` | `not_ultralytics` (weights only) |
| `torchscript.pt` | `torch.jit.script(...).save(...)` | `not_ultralytics` (TorchScript) |
| `legacy-format.pt` | `torch.save(..., _use_new_zipfile_serialization=False)` | `legacy_format` |
| `fuzz/FuzzInspect/*` | Inputs found by `FuzzInspect`, kept as regression tests (`1c3698c82bf33d19`: a global with an invalid UTF-8 name, which ended up in an error message) | |

Inputs that are not files here are built by the tests: truncated and damaged zips, a zip without `data.pkl`,
a zip64 archive larger than 4 GiB (a sparse virtual file), oversized zip directories and `data.pkl` entries,
top-level values that are not checkpoints (`TestInspectRejected`), and hostile pickles: deep nesting,
huge LONG4 / BINUNICODE lengths, memo abuse, amplification through memo references, cycles
(`TestUnpickleErrors`, `TestUnpickleCraftedDefaults`, the seeds of `FuzzInspect`), a few-KB pickle that makes
an opcode or the extraction copy or scan one large memoized string many times (`TestCraftedAmplification`),
and `yolo11n-best.pt` with calls of `os.system` and the like added (`TestInspectUnsafe`).

`.gitattributes` marks the `.pt` files as binary.

## Regenerating the checkpoints

Generated with Python 3.13.18, torch 2.14.1 (run on the CPU), ultralytics 8.4.198, numpy 2.5.4 and
opencv-python 5.0.0. Save `gen_fixtures.py` (below) outside the repository, for example in `/var/tmp/ckpt-scripts/`,
and run it from the repository root:

```sh
python3.13 -m venv /var/tmp/ckpt-venv
/var/tmp/ckpt-venv/bin/pip install ultralytics==8.4.198 torch==2.14.1 numpy==2.5.4 opencv-python==5.0.0
YOLO_OFFLINE=1 YOLO_CONFIG_DIR=/var/tmp/ckpt-ycfg CUDA_VISIBLE_DEVICES= \
  /var/tmp/ckpt-venv/bin/python -I /var/tmp/ckpt-scripts/gen_fixtures.py /var/tmp/work-checkpoint/gen backend/internal/model/checkpoint/testdata
```

The first argument is a scratch directory; the trained checkpoints record the dataset path in it
(`train_args.data`), which the tests expect to be `/var/tmp/work-checkpoint/gen/tinyds/data.yaml`. The dates,
the metrics and the best fitness change with every run, so after regenerating, update the expected values in
`checkpoint_test.go` (`yolo11Best`, `yolo26Best`, `yolo11Metrics` and the dates in `TestInspectFixtures`) from
the output of `expect.py` below. Parameter counts only change when Ultralytics changes an architecture.

`gen_fixtures.py`:

```python
"""Generates the checkpoint fixtures in testdata/ (see README.md)."""

import argparse
import collections
import datetime
import os
import pathlib
import random
import shutil
import sys
import zipfile

os.environ["YOLO_OFFLINE"] = "1"

import cv2  # noqa: E402
import numpy as np  # noqa: E402
import torch  # noqa: E402
from ultralytics import RTDETR, YOLO  # noqa: E402

parser = argparse.ArgumentParser()
parser.add_argument("work", type=pathlib.Path, help="scratch directory (created)")
parser.add_argument("testdata", type=pathlib.Path, help="backend/internal/model/checkpoint/testdata")
args = parser.parse_args()
work, testdata = args.work.resolve(), args.testdata.resolve()
pt = work / "pt"  # full checkpoints, repacked into testdata at the end
pt.mkdir(parents=True, exist_ok=True)
models_cfg = pathlib.Path(sys.modules["ultralytics"].__file__).parent / "cfg" / "models"

# 1. Models built from the Ultralytics yaml files and saved untrained.
for name in ["yolov8n", "yolov9t", "yolov10n", "yolo11n", "yolo12n", "yolo26n", "yolo11n-seg", "yolo11n-cls",
             "yolo11n-pose", "yolo11n-obb", "yolo26n-seg", "yoloe-11n-seg", "yolov8n-worldv2"]:
    YOLO(name + ".yaml").save(pt / f"{name}.pt")
RTDETR("rtdetr-l.yaml").save(pt / "rtdetr-l.pt")

# 2. Custom yaml names (copies of the Ultralytics files): the version is recognized from the architecture.
for src, dst in [("26/yolo26.yaml", "line-a-detector.yaml"), ("v8/yolov8.yaml", "my-model.yaml"),
                 ("26/yolo26-cls.yaml", "parts-cls.yaml")]:
    shutil.copy(models_cfg / src, work / dst)
    YOLO(str(work / dst)).save(pt / dst.replace(".yaml", ".pt"))

# 3. Two epochs of training on a tiny synthetic dataset (96x96, classes "scratch" and "划痕-凹坑").
random.seed(0)
ds = work / "tinyds"
for split in ("train", "val"):
    (ds / "images" / split).mkdir(parents=True, exist_ok=True)
    (ds / "labels" / split).mkdir(parents=True, exist_ok=True)
    for i in range(8):
        img = np.full((96, 96, 3), 200, np.uint8)
        x, y, w, h = random.randint(5, 50), random.randint(5, 50), random.randint(15, 35), random.randint(15, 35)
        c = i % 2
        cv2.rectangle(img, (x, y), (x + w, y + h), (0, 0, 255) if c else (255, 0, 0), -1)
        cv2.imwrite(str(ds / "images" / split / f"{i}.jpg"), img)
        (ds / "labels" / split / f"{i}.txt").write_text(
            f"{c} {(x + w / 2) / 96:.4f} {(y + h / 2) / 96:.4f} {w / 96:.4f} {h / 96:.4f}\n")
(ds / "data.yaml").write_text(f"path: {ds}\ntrain: images/train\nval: images/val\nnames:\n  0: scratch\n  1: 划痕-凹坑\n",
                              encoding="utf-8")
train = dict(data=str(ds / "data.yaml"), epochs=2, imgsz=96, batch=4, workers=0, device="cpu", plots=False,
             project=str(work / "runs"), exist_ok=True, verbose=False, amp=False)
for arch in ("yolo11n", "yolo26n"):
    model = YOLO(arch + ".yaml")
    if arch == "yolo11n":
        def grab(trainer):  # last.pt as saved after the first epoch, before strip_optimizer
            if trainer.last.exists() and not (pt / "yolo11n-last.pt").exists():
                shutil.copy(trainer.last, pt / "yolo11n-last.pt")
        model.add_callback("on_model_save", grab)
    model.train(name=arch, **train)
    shutil.copy(work / "runs" / arch / "weights" / "best.pt", pt / f"{arch}-best.pt")

# 4. Variants of the trained yolo11n best.pt: numpy scalars (older Ultralytics), pathlib paths, names as a list,
# imgsz as [h, w]; and the same checkpoint pickled with every protocol.
ckpt = torch.load(pt / "yolo11n-best.pt", map_location="cpu", weights_only=False)
ckpt["best_fitness"] = np.float64(0.375)
ckpt["epoch"] = np.int64(41)
ckpt["train_metrics"] = {"metrics/mAP50(B)": np.float64(0.91), "metrics/mAP50-95(B)": np.float32(0.625),
                         "fitness": np.float64(float("nan")), "note": "text", "val/box_loss": 1}
ckpt["train_args"]["data"] = pathlib.PosixPath("/data/exports/line-a/data.yaml")  # PurePosixPath is not a safe global
ckpt["train_args"]["imgsz"] = [96, 128]
ckpt["model"].names = ["scratch", "划痕-凹坑"]
torch.save(ckpt, pt / "yolo11n-variants.pt")
ckpt = torch.load(pt / "yolo26n-best.pt", map_location="cpu", weights_only=False)
for protocol in (0, 1, 4, 5):
    torch.save(ckpt, pt / f"yolo26n-best-protocol{protocol}.pt", pickle_protocol=protocol)

# 5. Files the platform rejects.
v5 = work / "yolov5src" / "models"  # the class layout of the original YOLOv5 repository (models.yolo, models.common)
v5.mkdir(parents=True, exist_ok=True)
(v5 / "__init__.py").write_text("")
(v5 / "common.py").write_text(
    "import torch.nn as nn\n\n\n"
    "class Conv(nn.Module):\n"
    "    def __init__(self, c1, c2, k=1, s=1):\n"
    "        super().__init__()\n"
    "        self.conv = nn.Conv2d(c1, c2, k, s, k // 2, bias=False)\n"
    "        self.bn = nn.BatchNorm2d(c2)\n"
    "        self.act = nn.SiLU()\n")
(v5 / "yolo.py").write_text(
    "import torch\nimport torch.nn as nn\n\nfrom models.common import Conv\n\n\n"
    "class Detect(nn.Module):\n"
    "    def __init__(self, nc=2, ch=(16,)):\n"
    "        super().__init__()\n"
    "        self.nc, self.no = nc, nc + 5\n"
    "        self.m = nn.ModuleList(nn.Conv2d(x, self.no * 3, 1) for x in ch)\n\n\n"
    "class DetectionModel(nn.Module):\n"
    "    def __init__(self):\n"
    "        super().__init__()\n"
    "        self.yaml = {'nc': 2, 'depth_multiple': 0.33, 'width_multiple': 0.25,\n"
    "                     'backbone': [[-1, 1, 'Conv', [64, 6, 2, 2]]], 'head': [[[17], 1, 'Detect', ['nc']]]}\n"
    "        self.model = nn.Sequential(Conv(3, 16, 3, 2), Detect())\n"
    "        self.names = ['scratch', 'dent']\n"
    "        self.stride = torch.tensor([8.0, 16.0, 32.0])\n")
sys.path.insert(0, str(v5.parent))
from models.yolo import DetectionModel  # noqa: E402

torch.save({"epoch": -1, "best_fitness": None, "model": DetectionModel().half(), "ema": None, "updates": None,
            "optimizer": None, "opt": {"weights": "yolov5s.pt", "imgsz": 640, "epochs": 100},
            "git": {"remote": "https://github.com/ultralytics/yolov5"}, "date": datetime.datetime(2024, 5, 1).isoformat()},
           pt / "yolov5-repo.pt")
torch.save(YOLO("yolo11n.yaml").model.state_dict(), pt / "state-dict.pt")
torch.save({"model": collections.OrderedDict(conv=torch.zeros(4, 3, 3, 3)), "epoch": 3}, pt / "state-dict-wrapped.pt")
torch.jit.script(torch.nn.Sequential(torch.nn.Conv2d(3, 4, 3), torch.nn.SiLU())).save(str(pt / "torchscript.pt"))
torch.save({"epoch": 1, "weight": torch.zeros(2, 2)}, testdata / "legacy-format.pt", _use_new_zipfile_serialization=False)


# 6. Keep only <root>/data.pkl and <root>/version: the parser never reads the tensor data. yolov8n.pt stays
# uncompressed like the files torch writes; the others are deflated to keep the repository small.
def repack(src: pathlib.Path, dst: pathlib.Path, method: int) -> None:
    with zipfile.ZipFile(src) as z:
        names = z.namelist()
        root = names[0].split("/", 1)[0]
        version = f"{root}/version" if f"{root}/version" in names else f"{root}/.data/version"
        entries = [(f"{root}/data.pkl", z.read(f"{root}/data.pkl")), (f"{root}/version", z.read(version))]
    with zipfile.ZipFile(dst, "w") as z:
        for name, data in entries:
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = method
            z.writestr(info, data, compresslevel=9 if method == zipfile.ZIP_DEFLATED else None)


for src in sorted(pt.glob("*.pt")):
    repack(src, testdata / src.name, zipfile.ZIP_STORED if src.name == "yolov8n.pt" else zipfile.ZIP_DEFLATED)
    print(f"{src.name}: {(testdata / src.name).stat().st_size} bytes")
```

## Expected values

The values in `checkpoint_test.go` are what PyTorch and Ultralytics report for the full checkpoints (in the
`pt/` folder of the scratch directory): `sum(p.numel() for p in model.parameters())` of `ckpt["ema"] or
ckpt["model"]`, its yaml, head, names and the training fields.

```sh
/var/tmp/ckpt-venv/bin/python -I /var/tmp/ckpt-scripts/expect.py /var/tmp/work-checkpoint/gen/pt/*.pt
```

`expect.py`:

```python
import sys, json, torch, warnings, math
warnings.filterwarnings("ignore")
for p in sys.argv[1:]:
    try:
        ck = torch.load(p, map_location="cpu", weights_only=False)
    except Exception as e:
        print(json.dumps({"file": p.split("/")[-1], "error": str(e)[:200]})); continue
    if not isinstance(ck, dict) or not (ck.get("ema") or ck.get("model")) or isinstance(ck.get("model"), dict):
        print(json.dumps({"file": p.split("/")[-1], "type": type(ck).__name__})); continue
    m = ck.get("ema") or ck.get("model")
    if not hasattr(m, "yaml"):
        print(json.dumps({"file": p.split("/")[-1], "cls": type(m).__module__ + "." + type(m).__name__})); continue
    y = m.yaml
    head = m.model[-1]
    ta = ck.get("train_args") or {}
    a = m.args if isinstance(m.args, dict) else vars(m.args)
    names = m.names
    nl = [names[k] for k in sorted(names)] if isinstance(names, dict) else list(names)
    tm = ck.get("train_metrics")
    out = dict(
        file=p.split("/")[-1], cls=type(m).__name__,
        yaml_file=y.get("yaml_file"), scale=y.get("scale"), end2end=y.get("end2end"), channels=y.get("channels"),
        names=nl if len(nl) <= 3 else [len(nl), nl[0], nl[-1]],
        head=type(head).__name__, one2one=hasattr(head, "one2one_cv2"),
        params=sum(p.numel() for p in m.parameters()),
        task_attr=getattr(m, "task", None), args_task=a.get("task"), args_imgsz=a.get("imgsz"),
        version=ck.get("version"), date=ck.get("date"), epoch=None if ck.get("epoch") is None else int(ck.get("epoch")),
        best_fitness=None if ck.get("best_fitness") is None else float(ck.get("best_fitness")),
        train_args={k: str(ta.get(k)) if ta.get(k) is not None else None for k in ("task", "imgsz", "epochs", "data", "model")},
        train_metrics={k: (float(v) if not isinstance(v, str) else v) for k, v in tm.items()} if tm else tm,
        optimizer=ck.get("optimizer") is not None,
    )
    print(json.dumps(out, ensure_ascii=False, default=str))
```

## Pickles in `pickle_test.go`

`pythonPickles` in `pickle_test.go` holds the output of this script (Python 3.13, numpy 2.5.4): the same
structures pickled with protocols 0 to 5.

```python
"""Prints the pickles used by pickle_test.go as Go string literals."""
import collections, copyreg, pathlib, pickle, sys, types
import numpy as np

mod = types.ModuleType("fakemod")
sys.modules["fakemod"] = mod


class Thing:
    pass


class Pair:
    __slots__ = ("a", "b")

    def __init__(self, a, b):
        self.a, self.b = a, b

    def __getstate__(self):
        return (None, {"a": self.a, "b": self.b})


class Keyword:
    def __new__(cls, *args, **kwargs):
        o = object.__new__(cls)
        o.args, o.kwargs = args, kwargs
        return o

    def __getnewargs_ex__(self):
        return self.args, self.kwargs


for c in (Thing, Pair, Keyword):
    c.__module__ = "fakemod"
    setattr(mod, c.__name__, c)


def go(b: bytes) -> str:
    out = []
    for c in b:
        ch = chr(c)
        if 0x20 <= c < 0x7F and ch not in '"\\':
            out.append(ch)
        elif ch == "\n":
            out.append("\\n")
        else:
            out.append("\\x%02x" % c)
    return '"' + "".join(out) + '"'


scalars = {"a": 1, "big": 2**70, "neg": -5, "s": "x'y\"z\\\n", "u": "划痕-凹坑", "f": 1.5, "t": (1, 2), "l": [True, False, None],
           "b": b"\x00\xff", "i2": 40000, "i4": -70000, "i8": 2**40}
t = Thing()
t.x = 1
t.me = t
objects = {"thing": t, "pair": Pair(1, "two"), "kw": Keyword(1, k=2), "od": collections.OrderedDict([("k", 1)]),
           "dd": collections.defaultdict(list, {"z": [1]}), "set": {3}, "fs": frozenset([4]), "ba": bytearray(b"ab"),
           "np": [np.float64(0.25), np.float32(1.5), np.int64(-3), np.uint8(200), np.bool_(True), np.float16(0.5)],
           "path": pathlib.PurePosixPath("/data/a.yaml"), "wpath": pathlib.PureWindowsPath("C:\\data\\a.yaml")}
for proto in range(6):
    print(f"scalars protocol {proto}: {go(pickle.dumps(scalars, protocol=proto))}")
for proto in range(6):
    print(f"objects protocol {proto}: {go(pickle.dumps(objects, protocol=proto))}")
```

## Architectures in `version_test.go`

`ultralyticsYAMLs` in `version_test.go` lists the modules of every model yaml of Ultralytics 8.4.198 as printed by
this script; the expected versions were added by hand.

```python
# Prints the module set, head and end2end flag of every Ultralytics model yaml.
import pathlib, sys
import ultralytics
from ultralytics.utils import YAML
root = pathlib.Path(ultralytics.__file__).parent / "cfg" / "models"
for p in sorted(root.rglob("*.yaml")):
    y = YAML.load(p)
    mods, last = set(), ""
    for sec in ("backbone", "head"):
        for layer in y.get(sec) or []:
            if len(layer) >= 3 and isinstance(layer[2], str):
                last = layer[2].rsplit(".", 1)[-1]
                mods.add(last)
    e2e = bool(y.get("end2end")) or last == "v10Detect"
    print(f'{{"{p.relative_to(root)}", "{" ".join(sorted(mods))}", "{last}", {str(e2e).lower()}}},')
```

## Safe globals

`safe.go` mirrors what Ultralytics' safe loading (`ULTRALYTICS_SAFE_LOAD=1`, `_SafeLoad` in
`ultralytics/nn/tasks.py`) allows with torch 2.14.1 and ultralytics 8.4.198; `safeGlobalCases` in `safe_test.go`
and `TestFixtureGlobals` hold the verdicts. To check them against Python (after upgrading torch or Ultralytics),
save `safe_globals.py` (below) next to the other scripts and run it with the module and name of each case, and
with the fixtures and some real checkpoints:

```sh
export YOLO_OFFLINE=1 YOLO_CONFIG_DIR=/var/tmp/ckpt-ycfg CUDA_VISIBLE_DEVICES=
/var/tmp/ckpt-venv/bin/python -I /var/tmp/ckpt-scripts/safe_globals.py lists
/var/tmp/ckpt-venv/bin/python -I /var/tmp/ckpt-scripts/safe_globals.py names torch._utils _rebuild_tensor_v2 posix system
/var/tmp/ckpt-venv/bin/python -I /var/tmp/ckpt-scripts/safe_globals.py files backend/internal/model/checkpoint/testdata/*.pt
```

`lists` prints `torchSafeGlobals` and `ultralyticsGlobals`. With torch 2.14.1 and ultralytics 8.4.198 every
verdict of `names` matches `safeGlobalCases`, except `ultralytics.nn.text_model.CLIP`, which needs the `clip`
package (`safe.go` allows it, as Ultralytics does once `clip` is installed). `files` refuses nothing in any
fixture or real checkpoint except `torchscript.pt` and `yolov5-repo.pt` (as `TestFixtureGlobals` expects);
torch's safe unpickler cannot read the protocol 0, 1, 4 and 5 files at all (it only knows the opcodes of
protocol 2, which `torch.save` uses), so `Inspect` refuses the globals of the first two and accepts the
others.

`safe_globals.py`:

```python
"""Prints what Ultralytics' safe loading (ULTRALYTICS_SAFE_LOAD=1) refuses, to compare with safe.go.

safe_globals.py lists                 the allow-lists: torchSafeGlobals and ultralyticsGlobals
safe_globals.py names MODULE NAME ... one line per GLOBAL (module and name as pickled): allowed or refused
safe_globals.py files FILE.pt ...     one line per checkpoint: the globals it references that are refused
"""

import io
import sys
import warnings

import torch.serialization as ts
from torch import _weights_only_unpickler as w
from ultralytics.nn.tasks import _SafeLoad

warnings.filterwarnings("ignore")
scan = ts.get_unsafe_globals_in_checkpoint


def register(weight, needed=None):
    """Runs _SafeLoad.register in a fresh state, for a file or for a checkpoint referencing `needed`."""
    ts.clear_safe_globals()
    _SafeLoad._registry = None
    if needed is not None:
        ts.get_unsafe_globals_in_checkpoint = lambda _: needed
    try:
        _SafeLoad.register(weight)
    finally:
        ts.get_unsafe_globals_in_checkpoint = scan


def safe():
    return set(w._get_allowed_globals()) | set(w._get_user_allowed_globals())


mode, args = sys.argv[1], sys.argv[2:]
if mode == "lists":
    print("torch:", " ".join(sorted(w._get_allowed_globals())))
    print("ultralytics:", " ".join(sorted(_SafeLoad._build())))
elif mode == "names":
    for module, name in zip(args[::2], args[1::2]):
        m, n = w._read_global_instruction(io.BytesIO(f"{module}\n{name}\n".encode()).readline)  # torch's renaming
        try:
            register("unused.pt", [f"{m}.{n}"])
            verdict = "allowed" if f"{m}.{n}" in safe() else "refused"
        except Exception as e:  # e.g. ModuleNotFoundError: clip is not installed
            verdict = f"error {type(e).__name__}: {e}"
        print(f"{module}.{name}\t{verdict}")
elif mode == "files":
    for path in args:
        try:
            register(path)
            print(f"{path}\trefused {sorted(scan(path))}")
        except Exception as e:  # pickle opcodes torch's safe unpickler does not read (protocols 0, 1, 4, 5), TorchScript
            print(f"{path}\terror {type(e).__name__}: {str(e)[:100]}")
```

`allowedClass` only checks that a name in the modules whose classes `_SafeLoad` allows looks like a class (an
identifier with an upper-case letter, so that `v10Detect`, `v8DetectionLoss` and `_ConvNd` pass): without
importing anything the platform cannot check that it is an `nn.Module`. This accepts no fewer names than
Ultralytics (checked over every attribute of those modules), and the few more it accepts (typing names,
constants such as `ultralytics.nn.tasks.LOGGER`) are refused by the trainer, which loads with the real rule.
