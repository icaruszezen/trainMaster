"""tasks.py 与 model_export.ModelExportTask：任务线程对假平台（tests/fake_platform.py）领取并执行转换任务。

转换子进程默认是真实的 python -m trainmaster_trainer.model_export，ultralytics / onnx / onnxruntime 用
tests/export_fakes.py 中的假模块（通过 PYTHONPATH 加载）；超时、停止等用脚本代替子进程。
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import random
import sys
import textwrap
import threading
import time
from pathlib import Path
from typing import Any

import export_fakes
import pytest
from fake_platform import CLAIM, FakePlatform, wait_until

from trainmaster_trainer import model_export, tasks
from trainmaster_trainer.client import Client
from trainmaster_trainer.tasks import TaskWorker, missing_modules

TOKEN = "right-token-0123456789"
NODE_KEY = "node-key-1"
PT = b"PK\x03\x04" + bytes(range(256)) * 40


class FakeRegistration:
    """主循环的替身：一直处于已注册状态；registration_lost 时在假平台上重新注册（epoch 加一）。"""

    def __init__(self, platform: FakePlatform) -> None:
        self.platform = platform
        self.epoch = 1
        self.lost: list[int] = []
        platform.register({"node_key": NODE_KEY, "name": "gpu-01"})

    def wait_registered(self, timeout: float) -> int | None:
        return self.epoch

    def registration_lost(self, epoch: int) -> None:
        self.lost.append(epoch)
        if epoch == self.epoch:
            self.platform.register({"node_key": NODE_KEY, "name": "gpu-01"})
            self.epoch += 1


class FakeClock:
    def __init__(self) -> None:
        self.now = 1000.0

    def __call__(self) -> float:
        return self.now


class Waits:
    """TaskWorker 的 wait 替身：记录等待时间并推进假时钟，只真实等待一小会。"""

    def __init__(self, clock: FakeClock | None = None) -> None:
        self.clock = clock
        self.calls: list[float] = []

    def __call__(self, seconds: float) -> bool:
        self.calls.append(seconds)
        if self.clock is not None:
            self.clock.now += seconds
        time.sleep(0.005)
        return False


@pytest.fixture
def platform():
    p = FakePlatform(token=TOKEN)
    p.max_claim_wait = 0.2  # 没有任务时长轮询很快返回，测试结束时线程尽快退出
    yield p
    p.close()


@pytest.fixture
def fake_ml(tmp_path, monkeypatch):
    """让转换子进程加载假模块；返回设置假模块行为的函数（写入 FAKE_EXPORT）。"""
    root = export_fakes.write_modules(tmp_path / "fake-ml")
    monkeypatch.setenv("PYTHONPATH", str(root))
    calls = tmp_path / "ultralytics-calls.jsonl"

    def configure(**config: Any) -> Path:
        config = {"calls_file": str(calls), "end2end_head": True, **config}  # 默认按 YOLO26
        monkeypatch.setenv(export_fakes.ENV, json.dumps(config))
        return calls

    configure()
    return configure


def script(tmp_path: Path, body: str) -> list[str]:
    path = tmp_path / "child.py"
    path.write_text(textwrap.dedent(body), encoding="utf-8")
    return [sys.executable, str(path)]


class Run:
    def __init__(self, tmp_path: Path, platform: FakePlatform, **kw: Any) -> None:
        self.platform = platform
        self.registration = FakeRegistration(platform)
        self.waits: Waits = kw.pop("waits", None) or Waits()
        self.state_dir = tmp_path / "state"
        kw.setdefault("export_timeout", 60)
        kw.setdefault("find_spec", lambda name: object())
        self.worker = TaskWorker(
            client=Client(platform.url, TOKEN),
            node_key=NODE_KEY,
            registration=self.registration,
            state_dir=self.state_dir,
            node_name="gpu-01",
            wait=self.waits,
            rng=random.Random(1),
            **kw,
        )

    def __enter__(self) -> Run:
        self.worker.start()
        return self

    def __exit__(self, *exc: Any) -> None:
        self.worker.stop()
        deadline = time.monotonic() + 15
        while self.worker.alive and time.monotonic() < deadline:
            # 正在进行的长轮询立即返回（停止前刚发出的领取请求可能还没到达服务端，所以重复几次）
            self.platform.release_claims()
            self.worker._thread.join(timeout=0.1)  # type: ignore[union-attr]
        assert not self.worker.alive


def finished(platform: FakePlatform, export_id: int, *statuses: str) -> dict[str, Any]:
    statuses = statuses or ("succeeded", "failed")
    return wait_until(
        lambda: (e := platform.export(export_id)) and e["status"] in statuses and e, timeout=30
    )


def task_requests(platform: FakePlatform, export_id: int) -> list[dict[str, Any]]:
    prefix = f"/api/v1/compute/agent/model-exports/{export_id}/"
    with platform.lock:
        return [r for r in platform.requests if r["path"].startswith(prefix)]


# ---- 正常流程 ----


def test_export_end_to_end(platform, tmp_path, fake_ml, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    calls = fake_ml(end2end_head=True)
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform) as run:
        exp = finished(platform, export_id)
        assert exp["status"] == "succeeded", exp
        assert exp["attempts"] == 1
        assert exp["progress"] == ["downloading", "converting", "uploading"]
        result = exp["result"]
        assert result["node_name"] == "gpu-01"
        assert result["end2end"] is True and result["nms"] is False
        assert result["outputs"] == [{"name": "output0", "shape": [1, 300, 6], "type": "float32"}]
        assert result["verified"] is True
        assert exp["output_sha256"] == hashlib.sha256(exp["output"]).hexdigest()
        assert json.loads(exp["output"])["metadata"]["end2end"] == "True"  # 上传的就是导出的文件
        wait_until(lambda: run.worker.tasks_run == 1)
        assert not (run.state_dir / "work" / f"model-export-{export_id}").exists()

    ultralytics_calls = [json.loads(line) for line in calls.read_text().splitlines()]
    assert ultralytics_calls[0]["task"] == "detect"
    assert ultralytics_calls[0]["weights"].endswith(f"model-export-{export_id}/model.pt")
    assert ultralytics_calls[1] == {
        "call": "export",
        "format": "onnx",
        "imgsz": [640, 640],
        "dynamic": False,
        "nms": False,
        "opset": None,
        "simplify": True,
        "device": "cpu",
        "batch": 1,
    }
    requests = task_requests(platform, export_id)
    assert [(r["method"], r["path"].rsplit("/", 1)[1]) for r in requests] == [
        ("POST", "progress"),
        ("GET", "input"),
        ("POST", "progress"),
        ("POST", "progress"),
        ("PUT", "output"),
        ("POST", "complete"),
    ]
    assert all(r["headers"]["X-Node-Key"] == NODE_KEY for r in requests)
    assert all(r["headers"]["Authorization"] == f"Bearer {TOKEN}" for r in requests)
    claim = platform.claims[0]
    assert claim == {"node_key": NODE_KEY, "kinds": ["model_export"], "wait_seconds": 25}
    messages = [r.getMessage() for r in caplog.records]
    assert any(f"model export {export_id} started" in m for m in messages)
    assert any(f"model export {export_id} succeeded" in m for m in messages)


def test_options_are_passed_to_the_child(platform, tmp_path, fake_ml):
    calls = fake_ml(end2end_head=False)
    options = {"imgsz": [480, 640], "dynamic": True, "postprocess": "nms", "opset": 12}
    export_id = platform.add_export(PT, model={"yolo_version": "yolo11"}, options=options)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id)
    assert exp["status"] == "succeeded", exp
    export_call = json.loads(calls.read_text().splitlines()[1])
    assert export_call["imgsz"] == [480, 640]
    assert export_call["dynamic"] is True and export_call["nms"] is True
    assert export_call["opset"] == 12
    assert exp["result"]["inputs"][0]["shape"] == ["batch", 3, "height", "width"]


def test_long_poll_picks_up_a_new_task(platform, tmp_path, fake_ml):
    platform.max_claim_wait = 30
    with Run(tmp_path, platform):
        wait_until(lambda: len(platform.claims) >= 1)
        time.sleep(0.2)  # 领取请求正在服务端等待
        started = time.monotonic()
        export_id = platform.add_export(PT)
        exp = finished(platform, export_id)
        assert exp["status"] == "succeeded"
        assert time.monotonic() - started < 15
        assert len(platform.claims) <= 2  # 等待中的领取被新任务唤醒，而不是反复轮询


def test_claim_uses_long_timeout():
    seen: list[dict[str, Any]] = []
    stop = threading.Event()

    class Scripted:
        def post(self, path, payload, *, timeout=None, headers=None):
            seen.append({"path": path, "payload": payload, "timeout": timeout})
            if len(seen) >= 3:
                stop.set()
            return {"task": None}

    clock = FakeClock()
    worker = TaskWorker(
        client=Scripted(),  # type: ignore[arg-type]
        node_key=NODE_KEY,
        registration=type("R", (), {"wait_registered": lambda self, t: 1})(),  # type: ignore[arg-type]
        state_dir=Path("/nonexistent"),
        node_name="n",
        export_timeout=60,
        find_spec=lambda name: object(),
        wait=Waits(clock),
        clock=clock,
    )
    while not stop.is_set():
        worker._step()
    assert [s["path"] for s in seen] == [CLAIM] * 3
    assert all(s["timeout"] == 25 + 15 for s in seen)
    assert seen[0]["payload"] == {
        "node_key": NODE_KEY,
        "kinds": ["model_export"],
        "wait_seconds": 25,
    }
    # 空结果立即返回（假时钟没有走）时稍等再领，避免空转
    assert worker._wait_fn.calls == [1.0, 1.0, 1.0]  # type: ignore[union-attr]


def test_retryable_failure_is_retried(platform, tmp_path, fake_ml):
    failed_once: list[bool] = []

    def complete(exp, request):
        if not failed_once:
            failed_once.append(True)
            return 500, {"error": {"code": "internal", "message": "数据库错误"}}
        return None

    platform.hooks["complete"] = complete
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id, "succeeded")
    assert exp["attempts"] == 2
    assert exp["fails"][0]["retryable"] is True
    assert exp["fails"][0]["error"] == "完成转换失败：数据库错误"


# ---- 下载校验 ----


@pytest.mark.parametrize(
    ("overrides", "error"),
    [
        ({"sha256": "0" * 64}, "下载的模型文件校验失败（sha256 不一致），可能是传输出错"),
        ({"serve": b"X" * len(PT)}, "下载的模型文件校验失败（sha256 不一致），可能是传输出错"),
        ({"size_bytes": len(PT) + 10}, f"下载的模型文件大小不一致（收到 {len(PT)} 字节，应为 {len(PT) + 10} 字节）"),
        ({"size_bytes": len(PT) - 10}, "下载模型文件失败：response has"),
    ],
)  # fmt: skip
def test_download_verification(platform, tmp_path, fake_ml, overrides, error):
    calls = fake_ml()
    export_id = platform.add_export(PT, **overrides)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id, "failed")
    # 传输出错可以重试：平台重新排队，3 次后失败
    assert exp["attempts"] == 3
    assert [f["retryable"] for f in exp["fails"]] == [True, True, True]
    assert exp["fails"][0]["error"].startswith(error)
    assert not calls.exists()  # 没有启动转换
    assert exp["output"] is None


def test_input_deleted_before_download(platform, tmp_path, fake_ml):
    def input_gone(exp, request):
        platform.delete_export(exp["id"])  # 下载之前被删除：照常处理请求，返回 404

    platform.hooks["input"] = input_gone
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform) as run:
        wait_until(lambda: run.worker.tasks_run == 1)
    paths = [r["path"].rsplit("/", 1)[1] for r in task_requests(platform, export_id)]
    assert paths == ["progress", "input"]  # 404 后放弃，不再上报失败


# ---- 转换失败 ----


def test_export_failure_is_reported(platform, tmp_path, fake_ml):
    fake_ml(export_error=["RuntimeError", "tracing failed"])
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id)
    assert exp["status"] == "failed" and exp["attempts"] == 1
    fail = exp["fails"][0]
    assert fail["retryable"] is False
    assert fail["error"] == "导出 ONNX 失败：RuntimeError: tracing failed"
    assert "Traceback" in fail["detail"]
    assert "ultralytics prints its export log" in fail["detail"]  # 子进程的输出附在详情中
    assert exp["output"] is None


def test_end2end_for_a_model_without_one2one_head(platform, tmp_path, fake_ml):
    fake_ml(end2end_head=False)
    export_id = platform.add_export(PT, model={"yolo_version": "yolo11"})
    with Run(tmp_path, platform):
        exp = finished(platform, export_id)
    assert exp["fails"][0]["error"].startswith("该模型（YOLO11）没有端到端（NMS-free）检测头")
    assert exp["fails"][0]["retryable"] is False


def test_too_old_ultralytics(platform, tmp_path, fake_ml):
    fake_ml(version="8.3.100", load_error=["AttributeError", "Can't get attribute 'C3k2s'"])
    export_id = platform.add_export(
        PT, model={"info": {"ultralytics_version": "8.4.198", "task": "detect"}}
    )
    with Run(tmp_path, platform):
        exp = finished(platform, export_id)
    assert exp["fails"][0]["error"] == (
        "训练节点的 Ultralytics 8.3.100 无法加载该模型（YOLO26 需要 8.4.0 及以上、YOLO11 需要 8.3.0 及以上），"
        "请升级训练节点；该模型由 Ultralytics 8.4.198 保存，比训练节点的 8.3.100 新"
    )


def test_export_timeout(platform, tmp_path):
    cmd = script(tmp_path, "import time\ntime.sleep(30)\n")
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform, export_command=cmd, export_timeout=1):
        exp = finished(platform, export_id)
    assert exp["fails"][0]["error"].startswith("转换超时（1 秒内没有完成）")
    assert exp["fails"][0]["retryable"] is False


def test_child_crash(platform, tmp_path):
    cmd = script(tmp_path, "import os, signal\nos.kill(os.getpid(), signal.SIGKILL)\n")
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform, export_command=cmd):
        exp = finished(platform, export_id)
    assert exp["fails"][0]["error"].startswith("转换进程被信号 SIGKILL 终止")


def test_onnx_outside_work_dir_is_rejected(platform, tmp_path):
    cmd = script(
        tmp_path,
        f"""
        import json
        open({str(tmp_path / "elsewhere.onnx")!r}, "w").write("x")
        print(json.dumps({{"ok": True, "onnx_path": {str(tmp_path / "elsewhere.onnx")!r}, "result": {{}}}}))
        """,
    )
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform, export_command=cmd):
        exp = finished(platform, export_id)
    assert exp["fails"][0]["error"].startswith("转换进程没有在工作目录中生成 ONNX 文件")


# ---- 上传 ----


@pytest.mark.parametrize(
    ("status", "message", "retryable", "attempts"),
    [(422, "上传的文件与 X-Content-Sha256 不一致", True, 3), (507, "平台磁盘空间不足", False, 1),
     (413, "转换结果太大", False, 1)],
)  # fmt: skip
def test_upload_rejected(platform, tmp_path, fake_ml, status, message, retryable, attempts):
    platform.hooks["output"] = lambda exp, req: (
        status,
        {"error": {"code": "x", "message": message}},
    )
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id, "failed")
    assert exp["attempts"] == attempts
    assert exp["fails"][0] == {
        "error": f"上传转换结果失败：{message}",
        "detail": f"HTTP {status} x: {message}",
        "retryable": retryable,
    }


# ---- 放弃与停止 ----


def _slow_child(tmp_path: Path) -> tuple[list[str], Path]:
    pid_file = tmp_path / "child.pid"
    cmd = script(
        tmp_path,
        f"""
        import os, pathlib, time
        pathlib.Path({str(pid_file)!r}).write_text(str(os.getpid()))
        time.sleep(30)
        """,
    )
    return cmd, pid_file


def _gone(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return True
    return False


@pytest.mark.parametrize("how", ["conflict", "deleted"])
def test_progress_conflict_abandons_the_task(platform, tmp_path, how, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    cmd, pid_file = _slow_child(tmp_path)
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform, export_command=cmd, progress_interval=0.1) as run:
        wait_until(lambda: pid_file.exists() and pid_file.read_text())
        pid = int(pid_file.read_text())
        if how == "conflict":  # 任务被转给了其它节点（租约过期后被重新领取）
            platform.hooks["progress"] = lambda exp, req: (
                409,
                {"error": {"code": "conflict", "message": "转换任务不属于该节点"}},
            )
        else:  # 用户在页面上删除了转换
            platform.delete_export(export_id)
        started = time.monotonic()
        wait_until(lambda: run.worker.tasks_run == 1, timeout=15)
        assert time.monotonic() - started < 10
        assert _gone(pid)
        assert not (run.state_dir / "work" / f"model-export-{export_id}").exists()
    actions = [r["path"].rsplit("/", 1)[1] for r in task_requests(platform, export_id)]
    assert "fail" not in actions and "complete" not in actions and "output" not in actions
    assert any(f"model export {export_id} abandoned" in r.getMessage() for r in caplog.records)


def test_keeps_the_lease_while_converting(platform, tmp_path, fake_ml):
    cmd = script(
        tmp_path,
        """
        import json, sys, time
        spec = json.loads(sys.argv[sys.argv.index("--spec") + 1])
        time.sleep(1)
        open(spec["output"], "w").write("onnx")
        print(json.dumps({"ok": True, "onnx_path": spec["output"], "result": {"opset": 18}}))
        """,
    )
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform, export_command=cmd, progress_interval=0.1):
        exp = finished(platform, export_id)
    assert exp["status"] == "succeeded", exp
    assert exp["progress"].count("converting") >= 4  # 阶段开始时一次，之后定期续租
    assert exp["output"] == b"onnx"


def test_stop_during_a_task(platform, tmp_path):
    cmd, pid_file = _slow_child(tmp_path)
    export_id = platform.add_export(PT)
    run = Run(tmp_path, platform, export_command=cmd)
    with run:
        wait_until(lambda: pid_file.exists() and pid_file.read_text())
        pid = int(pid_file.read_text())
        assert run.worker.busy
        started = time.monotonic()
        run.worker.stop()
        run.worker.join()
        assert time.monotonic() - started < 10
        assert not run.worker.alive
        assert _gone(pid)
    exp = platform.export(export_id)
    assert exp["status"] == "queued"  # 可重试的失败：立即重新排队，由其它节点或重启后的节点执行
    assert exp["fails"] == [
        {"error": "训练节点正在停止，转换已中断", "detail": "", "retryable": True}
    ]
    assert not (run.state_dir / "work" / f"model-export-{export_id}").exists()


def test_task_claimed_while_stopping_is_given_back(platform, tmp_path):
    export_id = platform.add_export(PT)
    run = Run(tmp_path, platform)
    resp = run.worker.client.post(
        CLAIM, {"node_key": NODE_KEY, "kinds": ["model_export"], "wait_seconds": 0}
    )
    run.worker.stop()
    run.worker._execute(resp["task"])
    exp = platform.export(export_id)
    assert exp["status"] == "queued"
    assert exp["fails"][0]["retryable"] is True


def test_join_does_not_wait_for_an_idle_long_poll(platform, tmp_path):
    platform.max_claim_wait = 30
    run = Run(tmp_path, platform)
    with run:
        wait_until(lambda: len(platform.claims) >= 1)
        started = time.monotonic()
        run.worker.stop()
        run.worker.join()
        assert time.monotonic() - started < 2
        assert run.worker.alive  # 仍在长轮询中（守护线程，进程退出时随之结束）


def test_unexpected_error_is_reported(platform, tmp_path, monkeypatch):
    def broken(self, info):
        raise KeyError("bug")

    monkeypatch.setattr(model_export.ModelExportTask, "_prepare_work_dir", broken)
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform) as run:
        exp = finished(platform, export_id, "failed")
        assert run.worker.alive  # 任务线程继续领取
    assert exp["fails"][0]["error"] == "训练节点执行转换时出错：KeyError: 'bug'"
    assert exp["fails"][0]["retryable"] is True
    assert "Traceback" in exp["fails"][0]["detail"]


# ---- 任务数据与本机环境 ----


@pytest.mark.parametrize(
    ("overrides", "error"),
    [
        ({"task": {"options": {"imgsz": "big"}}}, "字段 options.imgsz"),
        ({"sha256": "nope"}, "字段 model.sha256"),
        ({"task": {"progress_url": "http://evil/x"}}, "字段 progress_url"),
        ({"task": {"kind": "training"}}, "不支持该类任务：'training'"),
    ],
)
def test_malformed_task(platform, tmp_path, overrides, error):
    export_id = platform.add_export(PT, **overrides)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id)
    assert exp["status"] == "failed"
    assert error in exp["fails"][0]["error"]
    assert exp["fails"][0]["retryable"] is False


def test_disk_space_is_checked(platform, tmp_path, monkeypatch):
    import shutil

    usage = shutil.disk_usage(tmp_path)
    monkeypatch.setattr(
        model_export.shutil, "disk_usage", lambda path: usage._replace(free=2 * len(PT))
    )
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id, "failed")
    assert exp["fails"][0]["error"].startswith("训练节点的磁盘空间不足：转换需要约 30.0 KB")
    assert exp["fails"][0]["retryable"] is True
    assert task_requests(platform, export_id)[0]["path"].endswith("/fail")  # 没有下载


def test_stale_work_dirs_are_removed(platform, tmp_path):
    stale = tmp_path / "state" / "work" / "model-export-99"
    stale.mkdir(parents=True)
    (stale / "model.pt").write_bytes(b"left over")
    keep = tmp_path / "state" / "work" / "other"
    keep.mkdir()
    with Run(tmp_path, platform):
        wait_until(lambda: not stale.exists())
    assert keep.exists()


# ---- 领取的错误处理 ----


def test_old_platform_is_checked_every_five_minutes(platform, tmp_path, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    platform.tasks_supported = False
    with Run(tmp_path, platform) as run:
        wait_until(lambda: run.waits.calls.count(300.0) >= 3)
        platform.tasks_supported = True
        export_id = platform.add_export(PT)
        wait_until(lambda: platform.export(export_id)["attempts"] == 1)
    messages = [r.getMessage() for r in caplog.records]
    assert sum("does not support task claims" in m for m in messages) == 1  # 只记录一次
    assert any("supports tasks now" in m for m in messages)


def test_unknown_node_waits_for_registration(platform, tmp_path):
    with Run(tmp_path, platform) as run:
        wait_until(lambda: len(platform.claims) >= 1)
        with platform.lock:
            platform.nodes.clear()  # 节点在平台上被删除
        wait_until(lambda: run.registration.lost)
        assert run.registration.lost == [1]
        claims = len(platform.claims)
        wait_until(lambda: len(platform.claims) > claims)  # 重新注册后继续领取
        assert 0.75 <= run.waits.calls[0] <= 1.25  # 退避一次，不连续重试


@pytest.mark.parametrize("mode", ["503", "drop", "422"])
def test_claim_errors_back_off(platform, tmp_path, caplog, mode):
    caplog.set_level(logging.DEBUG, logger="trainmaster_trainer")
    platform.mode = mode
    with Run(tmp_path, platform) as run:
        wait_until(lambda: len(run.waits.calls) >= 8)
        delays = run.waits.calls[:8]
        for i, delay in enumerate(delays):
            base = min(60.0, 2.0**i)
            assert 0.75 * base <= delay <= min(60.0, 1.25 * base), delays
        platform.mode = "up"
        export_id = platform.add_export(PT)
        wait_until(lambda: platform.export(export_id)["attempts"] == 1)
        wait_until(lambda: run.worker._backoff.attempt == 0)
    warnings = [
        r for r in caplog.records if r.levelno >= logging.WARNING and "claim" in r.getMessage()
    ]
    # HTTP 错误记录一次 WARNING；网络错误只记 DEBUG（主循环的心跳已经报告了连接状态）
    assert len(warnings) == (0 if mode == "drop" else 1)
    assert any("claiming tasks again" in r.getMessage() for r in caplog.records)


def test_missing_capabilities_are_rechecked(platform, tmp_path, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    clock = FakeClock()
    installed = {"torch", "ultralytics"}
    with Run(
        tmp_path,
        platform,
        waits=Waits(clock),
        clock=clock,
        find_spec=lambda name: object() if name in installed else None,
    ) as run:
        wait_until(lambda: run.waits.calls.count(300.0) >= 3)
        assert platform.claims == []  # 缺少 onnx：不领取 model_export
        installed.add("onnx")  # 之后安装了依赖（裸机节点）
        wait_until(lambda: platform.claims)
    messages = [r.getMessage() for r in caplog.records]
    assert sum("not claiming model_export tasks: cannot find onnx" in m for m in messages) == 1
    assert any("model_export tasks enabled" in m for m in messages)


def test_missing_modules_does_not_import(monkeypatch):
    assert missing_modules(["json", "trainmaster_no_such_module"]) == ["trainmaster_no_such_module"]

    def broken(name):
        raise ValueError("__spec__ is None")

    assert missing_modules(["x"], find_spec=broken) == ["x"]
    # 常驻进程不导入 torch / ultralytics / onnx
    monkeypatch.delitem(sys.modules, "onnx", raising=False)
    missing_modules(tasks.REQUIRED_MODULES["model_export"])
    assert "onnx" not in sys.modules


def test_resident_modules_do_not_import_ml_packages():
    import subprocess

    code = (
        "import sys, trainmaster_trainer.agent, trainmaster_trainer.tasks, trainmaster_trainer.model_export;"
        "print(sorted(m for m in ('torch', 'ultralytics', 'onnx', 'onnxruntime', 'numpy') if m in sys.modules))"
    )
    out = subprocess.run(
        [sys.executable, "-c", code], capture_output=True, text=True, check=True, timeout=60,
        cwd=str(Path(__file__).resolve().parent.parent),
    )  # fmt: skip
    assert out.stdout.strip() == "[]"
