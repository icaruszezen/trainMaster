"""training.py：YOLO 训练子进程与 TrainingJobTask 集成测试。"""

from __future__ import annotations

import io
import json
import zipfile
from pathlib import Path
from typing import Any

import pytest
from fake_platform import FakePlatform, wait_until
from task_harness import TOKEN, Run, gone, script

from trainmaster_trainer import training
from trainmaster_trainer.model_export import TaskFailed


def _make_dataset_zip(
    *, include_yaml: bool = True, extra_entry: tuple[str, bytes] | None = None
) -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        if include_yaml:
            zf.writestr(
                "data.yaml",
                "path: .\ntrain: train.txt\nval: val.txt\nnames:\n  0: person\n  1: helmet\n",
            )
        zf.writestr("train.txt", "./images/img1.jpg\n")
        zf.writestr("val.txt", "./images/img1.jpg\n")
        zf.writestr("images/img1.jpg", b"\xff\xd8\xff\xd9")
        zf.writestr("labels/img1.txt", "0 0.5 0.5 0.2 0.2\n")
        if extra_entry is not None:
            zf.writestr(extra_entry[0], extra_entry[1])
    return buf.getvalue()


def _write_fake_train_ml(root: Path, **config: Any) -> Path:
    """在 root 下生成供训练子进程导入的假 torch 与 ultralytics 模块。"""
    root.mkdir(parents=True, exist_ok=True)
    calls_file = root / "train-calls.jsonl"
    cfg_path = root / "fake_train_cfg.json"
    cfg_path.write_text(json.dumps({"calls_file": str(calls_file), **config}), encoding="utf-8")

    (root / "torch.py").write_text(
        "import types\n"
        "__version__ = '2.6.0+cu124'\n"
        "cuda = types.SimpleNamespace(\n"
        "    is_available=lambda: True,\n"
        "    memory_reserved=lambda: 2_400_000_000,\n"
        ")\n",
        encoding="utf-8",
    )

    ultralytics_code = f"""\
from __future__ import annotations
import json, os, sys, types
from pathlib import Path

__version__ = "8.4.198"
_CFG = json.loads(Path({str(cfg_path)!r}).read_text(encoding="utf-8"))

def _record(item):
    with open(_CFG["calls_file"], "a", encoding="utf-8") as f:
        f.write(json.dumps(item) + "\\n")

_flag = os.environ.get("ULTRALYTICS_SAFE_LOAD", "").strip().lower()
_safe_load = _flag in ("1", "true", "yes", "on")

nn = types.ModuleType("ultralytics.nn")
nn.__path__ = []
tasks_mod = types.ModuleType("ultralytics.nn.tasks")
tasks_mod.SAFE_LOAD = _safe_load
tasks_mod._SafeLoad = type("_SafeLoad", (), {{"SUPPORTED": True}})
nn.tasks = tasks_mod

cfg_mod = types.ModuleType("ultralytics.cfg")
if _CFG.get("default_cfg") is not None:
    cfg_mod.DEFAULT_CFG_DICT = _CFG["default_cfg"]

sys.modules["ultralytics.nn"] = nn
sys.modules["ultralytics.nn.tasks"] = tasks_mod
sys.modules["ultralytics.cfg"] = cfg_mod

class YOLO:
    def __init__(self, model_arg: str, task: str | None = None) -> None:
        _record({{
            "call": "YOLO",
            "model": model_arg,
            "task": task,
            "safe_load": tasks_mod.SAFE_LOAD,
        }})
        print("ultralytics console stdout line")
        os.write(1, b"C library writes directly to fd 1\\n")
        if _CFG.get("load_error"):
            raise RuntimeError(_CFG["load_error"])
        self.model_arg = model_arg
        self.task = task or "detect"
        self._callbacks: dict[str, list] = {{}}
        self.trainer = None

    def add_callback(self, event: str, fn) -> None:
        self._callbacks.setdefault(event, []).append(fn)

    def _fire(self, event: str, trainer) -> None:
        for cb in self._callbacks.get(event, []):
            cb(trainer)

    def train(self, **kwargs):
        _record({{"call": "train", "kwargs": kwargs}})
        if _CFG.get("train_error"):
            raise RuntimeError(_CFG["train_error"])
        epochs = int(kwargs.get("epochs", 2))
        save_dir = Path(kwargs["project"]) / kwargs["name"]
        wdir = save_dir / "weights"
        wdir.mkdir(parents=True, exist_ok=True)
        trainer = types.SimpleNamespace(
            args=types.SimpleNamespace(**kwargs),
            save_dir=save_dir,
            wdir=wdir,
            epochs=epochs,
            start_epoch=0,
            epoch=0,
            device="cuda:0",
            train_loader=[1, 2, 3, 4],
            tloss=[0.8, 0.5, 1.1],
            label_loss_items=lambda tloss, prefix="train": {{
                f"{{prefix}}/box_loss": float(tloss[0]),
                f"{{prefix}}/cls_loss": float(tloss[1]),
                f"{{prefix}}/dfl_loss": float(tloss[2]),
            }},
            metrics={{}},
            lr={{"lr/pg0": 0.01}},
            fitness=0.0,
        )
        self.trainer = trainer
        self._fire("on_pretrain_routine_start", trainer)
        header = ",".join([
            "epoch", "time", "train/box_loss", "train/cls_loss", "train/dfl_loss",
            "metrics/precision(B)", "metrics/recall(B)", "metrics/mAP50(B)",
            "metrics/mAP50-95(B)", "val/box_loss", "val/cls_loss", "val/dfl_loss", "lr/pg0",
        ])
        csv_lines = [header]
        for ep in range(epochs):
            trainer.epoch = ep
            p0 = f"Epoch {{ep + 1}}/{{epochs}}:   0%|          | 0/4"
            p1 = f"Epoch {{ep + 1}}/{{epochs}}: 100%|##########| 4/4"
            print(p0 + "\\r" + p1)
            self._fire("on_train_epoch_start", trainer)
            for _ in trainer.train_loader:
                self._fire("on_train_batch_end", trainer)
            box = round(0.8 - 0.2 * ep, 4)
            map50 = round(0.55 + 0.15 * ep, 4)
            map95 = round(0.35 + 0.12 * ep, 4)
            trainer.tloss = [box, 0.4, 1.0]
            trainer.metrics = {{
                "val/box_loss": round(box + 0.05, 4),
                "val/cls_loss": 0.42,
                "val/dfl_loss": 1.02,
                "metrics/precision(B)": 0.82,
                "metrics/recall(B)": 0.76,
                "metrics/mAP50(B)": map50,
                "metrics/mAP50-95(B)": map95,
            }}
            trainer.fitness = map95
            self._fire("on_fit_epoch_end", trainer)
            row = [ep + 1, 1.2, box, 0.4, 1.0, 0.82, 0.76, map50, map95, box + 0.05, 0.42, 1.02, 0.01]
            csv_lines.append(",".join(str(x) for x in row))

        (wdir / "last.pt").write_bytes(b"PT_LAST_WEIGHTS")
        if not _CFG.get("only_last_pt"):
            (wdir / "best.pt").write_bytes(b"PT_BEST_WEIGHTS")
        (save_dir / "results.csv").write_text("\\n".join(csv_lines) + "\\n", encoding="utf-8")
        (save_dir / "args.yaml").write_text("epochs: " + str(epochs) + "\\n", encoding="utf-8")
        (save_dir / "results.png").write_bytes(b"\\x89PNG\\r\\n\\x1a\\nRESULTS")
        (save_dir / "confusion_matrix.png").write_bytes(b"\\x89PNG\\r\\n\\x1a\\nCM")
        return trainer.metrics
"""
    (root / "ultralytics.py").write_text(ultralytics_code, encoding="utf-8")
    return calls_file


@pytest.fixture
def platform():
    p = FakePlatform(token=TOKEN)
    p.max_claim_wait = 0.2
    yield p
    p.close()


# ---- 单元测试：参数映射、日志清洗、CSV 解析与安全解压 ----


def test_training_spec_and_kwargs_mapping():
    spec = training.TrainingSpec.from_json(
        {
            "init_mode": "scratch",
            "data_yaml": "/tmp/data.yaml",
            "project_dir": "/tmp/runs",
            "run_name": "train",
            "arch_yaml": "yolo26n.yaml",
            "yolo_version": "yolo26",
            "task": "detect",
            "params": {
                "epochs": 120,
                "batch": -1,
                "imgsz": [640, 480],
                "pretrained": True,
                "resume": True,
                "quantize": "fp16",
                "freeze": [0, 1, 2],
                "project": "/etc/evil",
            },
        }
    )
    # 模拟旧版 Ultralytics 没有 quantize、但有 half 的 DEFAULT_CFG_DICT
    older_cfg = {
        "epochs": 100,
        "batch": 16,
        "imgsz": 640,
        "pretrained": True,
        "resume": False,
        "half": False,
        "freeze": None,
    }
    kw = training._build_train_kwargs(spec, older_cfg)
    assert kw["epochs"] == 120
    assert kw["batch"] == -1
    assert kw["imgsz"] == [640, 480]
    assert kw["freeze"] == [0, 1, 2]
    assert kw["pretrained"] is False  # scratch 模式强制关闭预训练下载
    assert kw["resume"] is False
    assert kw["half"] is True  # quantize="fp16" 兼容旧版映射为 half=True
    assert "quantize" not in kw
    assert kw["project"] == "/tmp/runs"  # 宿主目录始终由节点接管

    resume_spec = training.TrainingSpec.from_json(
        {
            "init_mode": "resume",
            "data_yaml": "/tmp/data.yaml",
            "project_dir": "/tmp/runs",
            "base_weights": "/tmp/last.pt",
            "params": {"epochs": 100, "resume": False},
        }
    )
    rkw = training._build_train_kwargs(resume_spec, None)
    assert rkw["resume"] is True


def test_normalize_log_line_and_parse_results_csv(tmp_path: Path):
    raw = b"Epoch 1/10:  25%|##5       | 1/4\rEpoch 1/10: 100%|##########| 4/4 [00:01<00:00]\r\n"
    assert training.normalize_log_line(raw) == "Epoch 1/10: 100%|##########| 4/4 [00:01<00:00]\n"

    csv_file = tmp_path / "results.csv"
    csv_file.write_text(
        "  epoch , time , train/box_loss , metrics/mAP50(B) , metrics/mAP50-95(B) , lr/pg0 \n"
        "   1 , 2.5 , 1.234 , 0.456 , 0.234 , 0.01 \n"
        "   2 , 2.4 , 0.987 , 0.678 , 0.412 , 0.009 \n",
        encoding="utf-8",
    )
    rows = training.parse_results_csv(csv_file, total_epochs=10)
    assert len(rows) == 2
    assert rows[0]["epoch"] == 1 and rows[0]["total_epochs"] == 10
    assert rows[0]["train_box_loss"] == 1.234
    assert rows[0]["map50"] == 0.456
    assert rows[1]["epoch"] == 2 and rows[1]["map50_95"] == 0.412


def test_safe_extract_zip_blocks_traversal(tmp_path: Path):
    evil_zip = tmp_path / "evil.zip"
    evil_zip.write_bytes(_make_dataset_zip(extra_entry=("../../outside.txt", b"boom")))
    with pytest.raises(TaskFailed, match="越界路径"):
        training.safe_extract_zip(evil_zip, tmp_path / "dest")

    no_yaml_zip = tmp_path / "noyaml.zip"
    no_yaml_zip.write_bytes(_make_dataset_zip(include_yaml=False))
    with pytest.raises(TaskFailed, match="缺少 data.yaml"):
        training.safe_extract_zip(no_yaml_zip, tmp_path / "dest2")


# ---- 子进程与完整节点集成测试 ----


def test_training_job_end_to_end_remote_zip(platform: FakePlatform, tmp_path: Path, monkeypatch):
    ml_dir = tmp_path / "fake-train-ml"
    calls_file = _write_fake_train_ml(ml_dir)
    monkeypatch.setenv("PYTHONPATH", str(ml_dir))

    job_id = platform.add_training_job(
        _make_dataset_zip(),
        init_mode="scratch",
        yolo_version="yolo26",
        model_scale="s",
        arch_yaml="yolo26s.yaml",
        params={"epochs": 3, "batch": 8, "imgsz": 640, "optimizer": "MuSGD", "cos_lr": True},
    )

    with Run(tmp_path, platform, progress_interval=0.2) as run:
        job = wait_until(
            lambda: (
                (j := platform.training_job(job_id))
                and j["status"] in ("succeeded", "failed")
                and j
            ),
            timeout=30,
        )
        assert job["status"] == "succeeded", job
        wait_until(lambda: run.worker.tasks_run == 1)
        assert not (run.state_dir / "work" / f"training-job-{job_id}").exists()

    # 阶段、轮次指标、日志流与上传产物全部完整上报
    stages = [p["stage"] for p in job["progress"]]
    assert "preparing" in stages
    assert "downloading" in stages
    assert "training" in stages
    assert "uploading" in stages

    epochs_by_num = {ep["epoch"]: ep for ep in job["epochs"]}
    assert sorted(epochs_by_num.keys()) == [1, 2, 3]
    assert epochs_by_num[3]["map50"] == 0.85
    assert epochs_by_num[3]["map50_95"] == 0.59

    # 子进程 stdout / C 扩展 fd 1 输出被重定向为训练日志，不污染 JSON 事件通道
    assert "ultralytics console stdout line" in job["logs"]
    assert "C library writes directly to fd 1" in job["logs"]
    assert "Epoch 1/3: 100%|##########| 4/4" in job["logs"]

    assert job["artifacts"]["weights/best.pt"] == b"PT_BEST_WEIGHTS"
    assert job["artifacts"]["weights/last.pt"] == b"PT_LAST_WEIGHTS"
    assert "results.csv" in job["artifacts"]
    assert "args.yaml" in job["artifacts"]
    assert "results.png" in job["artifacts"]
    assert "confusion_matrix.png" in job["artifacts"]
    assert job["result"]["node_name"] == "gpu-01"
    assert job["result"]["ultralytics_version"] == "8.4.198"

    calls = [json.loads(line) for line in calls_file.read_text(encoding="utf-8").splitlines()]
    assert calls[0] == {
        "call": "YOLO",
        "model": "yolo26s.yaml",
        "task": "detect",
        "safe_load": True,
    }
    train_kw = calls[1]["kwargs"]
    assert train_kw["epochs"] == 3
    assert train_kw["batch"] == 8
    assert train_kw["optimizer"] == "MuSGD"
    assert train_kw["cos_lr"] is True
    assert train_kw["pretrained"] is False


def test_training_job_uses_shared_data_dir_and_finetune_weights(
    platform: FakePlatform, tmp_path: Path, monkeypatch
):
    ml_dir = tmp_path / "fake-train-ml"
    calls_file = _write_fake_train_ml(ml_dir, only_last_pt=True)
    monkeypatch.setenv("PYTHONPATH", str(ml_dir))

    # 构造与后端共享的 /data/exports/proj-1/v1 目录
    shared_data = tmp_path / "shared-data"
    shared_dv = shared_data / "exports" / "proj-1" / "v1"
    shared_dv.mkdir(parents=True)
    shared_yaml = shared_dv / "data.yaml"
    shared_yaml.write_text(
        "path: .\ntrain: train.txt\nval: val.txt\nnames:\n  0: car\n", encoding="utf-8"
    )

    base_pt = b"PK\x03\x04FAKE_PRETRAINED_PT"
    job_id = platform.add_training_job(
        b"NOT_A_VALID_ZIP_SHOULD_NOT_BE_DOWNLOADED",
        init_mode="finetune",
        yolo_version="yolo11",
        model_scale="m",
        arch_yaml=None,
        dir_name="proj-1/v1",
        base_model_data=base_pt,
        params={"epochs": 1, "val": False},
    )

    with Run(tmp_path, platform, data_dir=shared_data, progress_interval=0.2):
        job = wait_until(
            lambda: (
                (j := platform.training_job(job_id))
                and j["status"] in ("succeeded", "failed")
                and j
            ),
            timeout=30,
        )
    assert job["status"] == "succeeded", job
    # 当 val=False 只产生 last.pt 时，子进程自动补齐 best.pt 供平台自动注册
    assert job["artifacts"]["weights/best.pt"] == b"PT_LAST_WEIGHTS"
    assert job["artifacts"]["weights/last.pt"] == b"PT_LAST_WEIGHTS"

    # 确认没有请求 dataset 下载接口（零拷贝直读共享目录）
    dataset_gets = [r for r in platform.requests if r["path"].endswith(f"/{job_id}/dataset")]
    assert dataset_gets == []

    calls = [json.loads(line) for line in calls_file.read_text(encoding="utf-8").splitlines()]
    assert calls[0]["safe_load"] is True
    assert Path(calls[0]["model"]).name == "base.pt"
    assert Path(calls[1]["kwargs"]["data"]).resolve() == shared_yaml.resolve()


def test_training_job_translates_oom_error(platform: FakePlatform, tmp_path: Path, monkeypatch):
    ml_dir = tmp_path / "fake-train-ml"
    _write_fake_train_ml(ml_dir, train_error="CUDA out of memory. Tried to allocate 2.00 GiB")
    monkeypatch.setenv("PYTHONPATH", str(ml_dir))

    job_id = platform.add_training_job(_make_dataset_zip(), params={"epochs": 2, "batch": 64})
    with Run(tmp_path, platform, progress_interval=0.2):
        job = wait_until(
            lambda: (j := platform.training_job(job_id)) and j["status"] == "failed" and j,
            timeout=30,
        )
    assert "显存或系统内存不足" in job["error"]
    assert job["fails"][0]["retryable"] is False


def test_platform_cancel_kills_running_training_subprocess(platform: FakePlatform, tmp_path: Path):
    pid_file = tmp_path / "train-child.pid"
    cmd = script(
        tmp_path,
        f"""
        import json, os, pathlib, sys, time
        pathlib.Path({str(pid_file)!r}).write_text(str(os.getpid()), encoding="utf-8")
        print(json.dumps({{"event": "start", "total_epochs": 50}}), flush=True)
        print("training epoch 1...", file=sys.stderr, flush=True)
        time.sleep(60)
        """,
        name="slow_train.py",
    )
    job_id = platform.add_training_job(_make_dataset_zip(), params={"epochs": 50})
    with Run(tmp_path, platform, training_command=cmd, progress_interval=0.2) as run:
        wait_until(lambda: pid_file.is_file())
        child_pid = int(pid_file.read_text(encoding="utf-8").strip())
        # 用户在平台上点击“停止训练”，之后节点的 progress 请求收到 409 Conflict
        platform.cancel_training_job(job_id)
        assert gone(child_pid, timeout=10.0)
        wait_until(lambda: run.worker.tasks_run == 1)

    job = platform.training_job(job_id)
    assert job is not None
    assert job["status"] == "canceled"
    assert job["fails"] == []  # 主动取消的任务不会再上报 /fail


def test_remote_storage_skips_shared_dir_and_honors_dataset_cache(
    platform: FakePlatform, tmp_path: Path, monkeypatch
):
    ml_dir = tmp_path / "fake-train-ml"
    _write_fake_train_ml(ml_dir)
    monkeypatch.setenv("PYTHONPATH", str(ml_dir))

    # 即使本地 data_dir 存在同名目录，当 shared_local=False（数据版本位于远程存储节点）时也必须走平台流式下载
    shared_data = tmp_path / "shared-data"
    stale_dv = shared_data / "exports" / "proj-1" / "v1"
    stale_dv.mkdir(parents=True)
    (stale_dv / "data.yaml").write_text("stale: true\n", encoding="utf-8")

    cache_enabled = {"enabled": True, "max_versions": 2, "ttl_seconds": 600}

    job1_id = platform.add_training_job(
        _make_dataset_zip(),
        dir_name="proj-1/v1",
        params={"epochs": 1},
        task={
            "data_version": {
                "id": 41,
                "version": 1,
                "dir_name": "proj-1/v1",
                "shared_local": False,
                "class_names": ["person", "helmet"],
            },
            "dataset_cache": cache_enabled,
        },
    )

    with Run(tmp_path, platform, data_dir=shared_data, progress_interval=0.2) as run:
        j1 = wait_until(
            lambda: (j := platform.training_job(job1_id)) and j["status"] == "succeeded" and j,
            timeout=30,
        )
        assert j1["status"] == "succeeded"
        wait_until(lambda: run.worker.tasks_run == 1)

        # 第一轮训练走网络下载，工作目录已立即删除，而短期缓存目录保留了 dv-41
        gets_j1 = [r for r in platform.requests if r["path"].endswith(f"/{job1_id}/dataset")]
        assert len(gets_j1) == 1
        assert not (run.state_dir / "work" / f"training-job-{job1_id}").exists()
        assert (run.state_dir / "dataset-cache" / "dv-41" / "data.yaml").is_file()

        # 第二轮使用同一远程数据版本（即使平台 zip 损坏也不会请求网络，直接命中本地短期缓存）
        job2_id = platform.add_training_job(
            b"CORRUPT_SHOULD_HIT_LOCAL_CACHE",
            dir_name="proj-1/v1",
            params={"epochs": 1},
            task={
                "data_version": {
                    "id": 41,
                    "version": 1,
                    "dir_name": "proj-1/v1",
                    "shared_local": False,
                    "class_names": ["person", "helmet"],
                },
                "dataset_cache": cache_enabled,
            },
        )
        j2 = wait_until(
            lambda: (j := platform.training_job(job2_id)) and j["status"] == "succeeded" and j,
            timeout=30,
        )
        assert j2["status"] == "succeeded"
        gets_j2 = [r for r in platform.requests if r["path"].endswith(f"/{job2_id}/dataset")]
        assert gets_j2 == []
        assert "命中训练节点短期数据集缓存" in j2["logs"]

        # 关闭缓存策略后执行新任务，训练结束应立即清空整个 dataset-cache 目录
        job3_id = platform.add_training_job(
            _make_dataset_zip(),
            params={"epochs": 1},
            task={
                "data_version": {
                    "id": 42,
                    "version": 2,
                    "dir_name": "proj-1/v2",
                    "shared_local": False,
                    "class_names": ["person", "helmet"],
                },
                "dataset_cache": {"enabled": False, "max_versions": 2, "ttl_seconds": 600},
            },
        )
        j3 = wait_until(
            lambda: (j := platform.training_job(job3_id)) and j["status"] == "succeeded" and j,
            timeout=30,
        )
        assert j3["status"] == "succeeded"
        wait_until(lambda: run.worker.tasks_run == 3)
        assert not (run.state_dir / "dataset-cache").exists()


def test_dataset_cache_manager_lru_and_ttl(tmp_path: Path):
    now = [1000.0]
    mgr = training.DatasetCacheManager(tmp_path / "state", wall_clock=lambda: now[0])
    src = tmp_path / "src"
    sample_zip = tmp_path / "sample.zip"
    sample_zip.write_bytes(_make_dataset_zip())
    training.safe_extract_zip(sample_zip, src)

    policy = training.DatasetCachePolicy(enabled=True, max_versions=2, ttl_seconds=60)
    mgr.store(1, src, policy)
    now[0] += 10
    mgr.store(2, src, policy)
    now[0] += 10
    # 访问 dv-1 使其成为最近使用，随后写入 dv-3 应淘汰 dv-2
    assert mgr.materialize(1, tmp_path / "mat1", policy) is not None
    now[0] += 5
    mgr.store(3, src, policy)

    assert (mgr.cache_root / "dv-1").is_dir()
    assert not (mgr.cache_root / "dv-2").exists()
    assert (mgr.cache_root / "dv-3").is_dir()

    # 超过 TTL 后自动清理
    now[0] += 120
    mgr.apply_policy(policy)
    assert list(mgr.cache_root.iterdir()) == []


def test_training_task_info_parses_additive_archive_fields_and_defaults():
    base_task = {
        "id": 88,
        "project_id": 2,
        "init_mode": "scratch",
        "yolo_version": "yolo11",
        "model_scale": "n",
        "arch_yaml": "yolo11n.yaml",
        "task": "detect",
        "data_version": {"id": 9, "dir_name": "proj-2/v3", "shared_local": False},
        "params": {"epochs": 5},
        "dataset_url": "/api/v1/compute/agent/training-jobs/88/dataset",
        "progress_url": "/api/v1/compute/agent/training-jobs/88/progress",
        "artifacts_url": "/api/v1/compute/agent/training-jobs/88/artifacts",
        "complete_url": "/api/v1/compute/agent/training-jobs/88/complete",
        "fail_url": "/api/v1/compute/agent/training-jobs/88/fail",
    }
    omitted = training.TrainingJobInfo.parse(base_task)
    assert omitted.dv_total_bytes == 0
    assert omitted.dv_transfer_mode == ""

    top_level = training.TrainingTaskInfo.parse(
        {**base_task, "dv_total_bytes": 12_345_678, "dv_transfer_mode": "archive"}
    )
    assert top_level.dv_total_bytes == 12_345_678
    assert top_level.dv_transfer_mode == "archive"

    nested = training.TrainingTaskInfo.parse(
        {
            **base_task,
            "data_version": {
                "id": 9,
                "dir_name": "proj-2/v3",
                "shared_local": False,
                "total_bytes": 4_096_000,
                "transfer_mode": "direct",
            },
        }
    )
    assert nested.dv_total_bytes == 4_096_000
    assert nested.dv_transfer_mode == "direct"


def test_archive_mode_verifies_sha256_logs_progress_unlinks_zip_and_caches(
    platform: FakePlatform, tmp_path: Path, monkeypatch
):
    import hashlib

    ml_dir = tmp_path / "fake-train-ml"
    _write_fake_train_ml(ml_dir)
    monkeypatch.setenv("PYTHONPATH", str(ml_dir))

    zip_bytes = _make_dataset_zip()
    zip_sha = hashlib.sha256(zip_bytes).hexdigest()
    job_id = platform.add_training_job(
        zip_bytes,
        params={"epochs": 1},
        dataset_headers={
            "X-Archive-Sha256": zip_sha,
            "X-Content-Sha256": zip_sha,
            "X-Transfer-Mode": "archive",
        },
        task={
            "dv_total_bytes": len(zip_bytes),
            "dv_transfer_mode": "archive",
            "data_version": {
                "id": 51,
                "version": 1,
                "dir_name": "proj-1/v51",
                "shared_local": False,
                "class_names": ["person", "helmet"],
            },
            "dataset_cache": {"enabled": True, "max_versions": 2, "ttl_seconds": 600},
        },
    )

    with Run(tmp_path, platform, progress_interval=0.2) as run:
        job = wait_until(
            lambda: (
                (j := platform.training_job(job_id))
                and j["status"] in ("succeeded", "failed")
                and j
            ),
            timeout=30,
        )
        assert job["status"] == "succeeded", job
        wait_until(lambda: run.worker.tasks_run == 1)
        assert not (run.state_dir / "work" / f"training-job-{job_id}").exists()
        assert (run.state_dir / "dataset-cache" / "dv-51" / "data.yaml").is_file()
        assert not list((run.state_dir / "dataset-cache").rglob("*.zip"))

    assert "存储容器已打包数据集压缩包，正在传输" in job["logs"]
    assert "压缩包校验通过" in job["logs"]
    assert zip_sha[:12] in job["logs"]


def test_archive_checksum_mismatch_aborts_before_safe_extract_zip_and_unlinks(
    platform: FakePlatform, tmp_path: Path, monkeypatch
):
    import fake_platform as fp_mod

    monkeypatch.setattr(fp_mod, "MAX_ATTEMPTS", 1)

    # 1. 直接单元测试：不匹配的 X-Archive-Sha256 立即删除压缩包并抛出 ValueError / TaskFailed
    corrupt_zip = tmp_path / "corrupt-dataset.zip"
    corrupt_zip.write_bytes(_make_dataset_zip())
    with pytest.raises((ValueError, TaskFailed), match="SHA-256 mismatch"):
        training.verify_dataset_archive_checksum(
            corrupt_zip,
            {"X-Archive-Sha256": "a" * 64},
        )
    assert not corrupt_zip.exists()

    # 2. 端到端集成测试：校验失败时绝不调用 safe_extract_zip，也不污染短期缓存
    extract_calls: list[Path] = []
    real_extract = training.safe_extract_zip

    def spy_extract(zip_path: Path, dest_dir: Path, abort=None, **kwargs) -> Path:
        extract_calls.append(zip_path)
        return real_extract(zip_path, dest_dir, abort=abort, **kwargs)

    monkeypatch.setattr(training, "safe_extract_zip", spy_extract)

    job_id = platform.add_training_job(
        _make_dataset_zip(),
        params={"epochs": 1},
        dataset_headers={
            "X-Content-Sha256": "b" * 64,
            "X-Transfer-Mode": "archive",
        },
        task={
            "dv_transfer_mode": "archive",
            "data_version": {
                "id": 52,
                "version": 2,
                "dir_name": "proj-1/v52",
                "shared_local": False,
                "class_names": ["person", "helmet"],
            },
            "dataset_cache": {"enabled": True, "max_versions": 2, "ttl_seconds": 600},
        },
    )

    with Run(tmp_path, platform, progress_interval=0.2) as run:
        job = wait_until(
            lambda: (j := platform.training_job(job_id)) and j["status"] == "failed" and j,
            timeout=30,
        )
        wait_until(lambda: run.worker.tasks_run == 1)
        assert extract_calls == []
        assert not (run.state_dir / "dataset-cache" / "dv-52").exists()
        assert not (run.state_dir / "work" / f"training-job-{job_id}").exists()

    assert "SHA-256 mismatch" in job["error"]


def test_legacy_direct_mode_without_checksum_header_falls_back_cleanly(
    platform: FakePlatform, tmp_path: Path, monkeypatch
):
    ml_dir = tmp_path / "fake-train-ml"
    _write_fake_train_ml(ml_dir)
    monkeypatch.setenv("PYTHONPATH", str(ml_dir))

    job_id = platform.add_training_job(
        _make_dataset_zip(),
        params={"epochs": 1},
        dataset_headers={"X-Transfer-Mode": "direct"},
        task={
            "dv_transfer_mode": "direct",
            "data_version": {
                "id": 53,
                "version": 1,
                "dir_name": "proj-1/v53",
                "shared_local": False,
                "class_names": ["person", "helmet"],
            },
        },
    )

    with Run(tmp_path, platform, progress_interval=0.2) as run:
        job = wait_until(
            lambda: (
                (j := platform.training_job(job_id))
                and j["status"] in ("succeeded", "failed")
                and j
            ),
            timeout=30,
        )
        assert job["status"] == "succeeded", job
        wait_until(lambda: run.worker.tasks_run == 1)
        assert not (run.state_dir / "work" / f"training-job-{job_id}").exists()

    assert "兼容存储流式拉取训练数据集，正在解压" in job["logs"]


def test_dv_total_bytes_disk_headroom_evicts_cache_and_rejects_when_insufficient(
    platform: FakePlatform, tmp_path: Path, monkeypatch
):
    import shutil as shutil_mod
    from collections import namedtuple

    import fake_platform as fp_mod

    monkeypatch.setattr(fp_mod, "MAX_ATTEMPTS", 1)

    state_dir = tmp_path / "evict-state"
    mgr = training.DatasetCacheManager(state_dir)
    src = tmp_path / "sample-ds"
    sample_zip = tmp_path / "sample.zip"
    sample_zip.write_bytes(_make_dataset_zip())
    training.safe_extract_zip(sample_zip, src)

    policy = training.DatasetCachePolicy(enabled=True, max_versions=3, ttl_seconds=600)
    mgr.store(10, src, policy)
    mgr.store(11, src, policy)

    Usage = namedtuple("Usage", ["total", "used", "free"])

    def fake_usage(_path):
        # 每存在一个缓存目录占用 400 MiB；总剩余空间随驱逐动态增加
        cached_count = (
            sum(1 for p in mgr.cache_root.iterdir() if p.is_dir()) if mgr.cache_root.is_dir() else 0
        )
        free = (1400 << 20) - cached_count * (400 << 20)
        return Usage(2000 << 20, (2000 << 20) - free, free)

    monkeypatch.setattr(shutil_mod, "disk_usage", fake_usage)

    # 需要 1100 MiB，且排除正在复用的 dv-11：应驱逐最旧的 dv-10（释放后 1000 MiB -> 继续检查时不误删 dv-11）
    mgr.evict_for_space(tmp_path, 950 << 20, exclude_dv_id=11)
    assert not (mgr.cache_root / "dv-10").exists()
    assert (mgr.cache_root / "dv-11").is_dir()

    # 当 dv_total_bytes 远超磁盘剩余空间时，在发起 /dataset 下载前直接拒绝任务
    huge_bytes = 50 << 30
    job_id = platform.add_training_job(
        _make_dataset_zip(),
        params={"epochs": 1},
        task={
            "dv_total_bytes": huge_bytes,
            "dv_transfer_mode": "archive",
            "data_version": {
                "id": 99,
                "version": 1,
                "dir_name": "proj-1/v99",
                "shared_local": False,
                "class_names": ["person"],
            },
        },
    )

    with Run(tmp_path, platform, progress_interval=0.2) as run:
        job = wait_until(
            lambda: (j := platform.training_job(job_id)) and j["status"] == "failed" and j,
            timeout=30,
        )
        wait_until(lambda: run.worker.tasks_run == 1)

    assert "训练节点磁盘空间不足" in job["error"]
    dataset_gets = [r for r in platform.requests if r["path"].endswith(f"/{job_id}/dataset")]
    assert dataset_gets == []


def test_safe_extract_zip_rejects_zip_bomb_symlink_fifo_and_dot_member(tmp_path: Path):
    import io
    import zipfile

    # 1. Zip bomb exceeding max_raw_bytes fails non-retryably and removes dest_dir
    bomb_buf = io.BytesIO()
    with zipfile.ZipFile(bomb_buf, "w", compression=zipfile.ZIP_DEFLATED) as zf:
        zf.writestr("data.yaml", "nc: 1\n")
        zf.writestr("bomb.bin", b"\x00" * (2 << 20))
    bomb_zip = tmp_path / "bomb.zip"
    bomb_zip.write_bytes(bomb_buf.getvalue())
    bomb_dest = tmp_path / "bomb-dest"
    with pytest.raises(TaskFailed) as bomb_exc:
        training.safe_extract_zip(bomb_zip, bomb_dest, max_raw_bytes=1024)
    assert bomb_exc.value.retryable is False
    assert not bomb_dest.exists()

    # 2. Unix symlink external_attr (0o120777 << 16) is rejected non-retryably
    sym_buf = io.BytesIO()
    with zipfile.ZipFile(sym_buf, "w") as zf:
        zf.writestr("data.yaml", "nc: 1\n")
        zi = zipfile.ZipInfo("evil_link.txt")
        zi.external_attr = 0o120777 << 16
        zf.writestr(zi, "/etc/passwd")
    sym_zip = tmp_path / "sym.zip"
    sym_zip.write_bytes(sym_buf.getvalue())
    sym_dest = tmp_path / "sym-dest"
    with pytest.raises(TaskFailed) as sym_exc:
        training.safe_extract_zip(sym_zip, sym_dest)
    assert sym_exc.value.retryable is False
    assert not sym_dest.exists()

    # 3. Unix FIFO external_attr (0o010600 << 16) is rejected non-retryably
    fifo_buf = io.BytesIO()
    with zipfile.ZipFile(fifo_buf, "w") as zf:
        zi = zipfile.ZipInfo("evil.fifo")
        zi.external_attr = 0o010600 << 16
        zf.writestr(zi, b"")
    fifo_zip = tmp_path / "fifo.zip"
    fifo_zip.write_bytes(fifo_buf.getvalue())
    with pytest.raises(TaskFailed) as fifo_exc:
        training.safe_extract_zip(fifo_zip, tmp_path / "fifo-dest")
    assert fifo_exc.value.retryable is False

    # 4. Member named "." is rejected non-retryably (not IsADirectoryError / retryable=True)
    dot_buf = io.BytesIO()
    with zipfile.ZipFile(dot_buf, "w") as zf:
        zf.writestr(".", b"x")
        zf.writestr("data.yaml", "nc: 1\n")
    dot_zip = tmp_path / "dot.zip"
    dot_zip.write_bytes(dot_buf.getvalue())
    with pytest.raises(TaskFailed) as dot_exc:
        training.safe_extract_zip(dot_zip, tmp_path / "dot-dest")
    assert dot_exc.value.retryable is False


def test_archive_mode_rejects_missing_sha256_headers_and_unlinks_zip(
    platform: FakePlatform, tmp_path: Path, monkeypatch
):
    import fake_platform as fp_mod

    monkeypatch.setattr(fp_mod, "MAX_ATTEMPTS", 1)

    # 1. Direct unit check: X-Transfer-Mode: archive without SHA-256 header unlinks zip and raises
    unverified_zip = tmp_path / "unverified.zip"
    unverified_zip.write_bytes(_make_dataset_zip())
    with pytest.raises(training.DatasetArchiveChecksumError) as exc_info:
        training.verify_dataset_archive_checksum(
            unverified_zip,
            {"X-Transfer-Mode": "archive"},
        )
    assert exc_info.value.retryable is True
    assert not unverified_zip.exists()

    # 2. Full job integration check: dv_transfer_mode="archive" with stripped checksum headers aborts pre-extract
    extract_calls: list[Path] = []
    monkeypatch.setattr(
        training,
        "safe_extract_zip",
        lambda zp, dd, **kw: extract_calls.append(zp),
    )

    job_id = platform.add_training_job(
        _make_dataset_zip(),
        params={"epochs": 1},
        dataset_headers={"X-Transfer-Mode": "archive"},
        task={
            "dv_transfer_mode": "archive",
            "data_version": {
                "id": 61,
                "version": 1,
                "dir_name": "proj-1/v61",
                "shared_local": False,
                "class_names": ["person"],
            },
            "dataset_cache": {"enabled": True, "max_versions": 2, "ttl_seconds": 600},
        },
    )

    with Run(tmp_path, platform, progress_interval=0.2) as run:
        job = wait_until(
            lambda: (j := platform.training_job(job_id)) and j["status"] == "failed" and j,
            timeout=30,
        )
        wait_until(lambda: run.worker.tasks_run == 1)
        assert extract_calls == []
        assert not (run.state_dir / "dataset-cache" / "dv-61").exists()
    assert "X-Archive-Sha256" in job["error"]


def test_cache_manager_virtiofs_copy_immutability_stale_tmp_and_toctou_disk_recheck(
    platform: FakePlatform, tmp_path: Path, monkeypatch
):
    import os as os_mod
    import shutil as shutil_mod
    from collections import namedtuple

    import fake_platform as fp_mod

    monkeypatch.setattr(fp_mod, "MAX_ATTEMPTS", 1)

    now = [2000.0]
    state_dir = tmp_path / "cache-hardening-state"
    mgr = training.DatasetCacheManager(state_dir, wall_clock=lambda: now[0])
    src = tmp_path / "extracted-src"
    sample_zip = tmp_path / "sample.zip"
    sample_zip.write_bytes(_make_dataset_zip())
    training.safe_extract_zip(sample_zip, src)
    orig_yaml_text = (src / "data.yaml").read_text(encoding="utf-8")

    # 1. Simulate VirtioFS / SMB where BOTH os.link and shutil.copy2 raise EPERM
    def deny_link(*_a, **_kw):
        raise PermissionError(1, "Operation not permitted")

    def deny_copy2(*_a, **_kw):
        raise PermissionError(1, "Operation not permitted")

    policy = training.DatasetCachePolicy(enabled=True, max_versions=3, ttl_seconds=60)
    with monkeypatch.context() as mp:
        mp.setattr(os_mod, "link", deny_link)
        mp.setattr(shutil_mod, "copy2", deny_copy2)
        mgr.store(71, src, policy)
        mat_dir = tmp_path / "mat-virtiofs"
        mat_yaml = mgr.materialize(71, mat_dir, policy)
        assert mat_yaml is not None and mat_yaml.is_file()

    # 2. Mutating materialized data.yaml in-place must NOT corrupt cached data.yaml
    mat_yaml.write_text("CORRUPTED_BY_JOB\n", encoding="utf-8")
    cached_yaml = mgr.cache_root / "dv-71" / "data.yaml"
    assert cached_yaml.read_text(encoding="utf-8") == orig_yaml_text

    # 3. Stray file in cache_root is unlinked; fresh .tmp-dv-* preserved; stale .tmp-dv-* removed
    stray_file = mgr.cache_root / "stray-note.txt"
    stray_file.write_text("not a dir", encoding="utf-8")
    fresh_tmp = mgr.cache_root / f".tmp-dv-80-123-{int((now[0] - 5) * 1000)}"
    fresh_tmp.mkdir(parents=True)
    stale_tmp = mgr.cache_root / f".tmp-dv-81-123-{int((now[0] - 3600) * 1000)}"
    stale_tmp.mkdir(parents=True)

    mgr.apply_policy(policy)
    assert not stray_file.exists()
    assert fresh_tmp.is_dir()
    assert not stale_tmp.exists()
    shutil_mod.rmtree(fresh_tmp, ignore_errors=True)

    # 4. Unexpected OSError inside store() cleans up .tmp-dv-* staging directory
    with monkeypatch.context() as mp:
        mp.setattr(
            training,
            "_link_or_copy_tree",
            lambda *_a, **_kw: (_ for _ in ()).throw(OSError("disk io error")),
        )
        mgr.store(72, src, policy)
    leaked_tmps = [p for p in mgr.cache_root.iterdir() if p.name.startswith(".tmp-")]
    assert leaked_tmps == []

    # 5. TOCTOU disk re-check: has_cached=True during _execute (600 MiB free > 512 MiB),
    # then materialize() misses while dv_total_bytes=400 MiB requires 512+800=1312 MiB -> aborts pre-download!
    Usage = namedtuple("Usage", ["total", "used", "free"])
    monkeypatch.setattr(
        shutil_mod, "disk_usage", lambda _p: Usage(2000 << 20, 1400 << 20, 600 << 20)
    )
    monkeypatch.setattr(training.DatasetCacheManager, "has_cached", lambda *_a, **_kw: True)
    monkeypatch.setattr(training.DatasetCacheManager, "materialize", lambda *_a, **_kw: None)

    job_id = platform.add_training_job(
        _make_dataset_zip(),
        params={"epochs": 1},
        task={
            "dv_total_bytes": 400 << 20,
            "dv_transfer_mode": "archive",
            "data_version": {
                "id": 73,
                "version": 1,
                "dir_name": "proj-1/v73",
                "shared_local": False,
                "class_names": ["person"],
            },
            "dataset_cache": {"enabled": True, "max_versions": 2, "ttl_seconds": 60},
        },
    )
    with Run(tmp_path / "toctou-run", platform, progress_interval=0.2) as run:
        job = wait_until(
            lambda: (j := platform.training_job(job_id)) and j["status"] == "failed" and j,
            timeout=30,
        )
        wait_until(lambda: run.worker.tasks_run == 1)
    assert "训练节点磁盘空间不足" in job["error"]
    assert [r for r in platform.requests if r["path"].endswith(f"/{job_id}/dataset")] == []
