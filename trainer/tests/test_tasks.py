"""tasks.py 与 model_export.ModelExportTask：任务线程对假平台（tests/fake_platform.py）领取并执行转换任务。

转换子进程默认是真实的 python -m trainmaster_trainer.model_export，ultralytics / onnx / onnxruntime 用
tests/export_fakes.py 中的假模块（通过 PYTHONPATH 加载）；超时、停止等用脚本代替子进程。
"""

from __future__ import annotations

import hashlib
import json
import logging
import sys
import threading
import time
from pathlib import Path
from typing import Any

import export_fakes
import pytest
from fake_platform import CLAIM, DROP, FakePlatform, wait_until
from fakes import WINDOWS
from task_harness import NODE_KEY, TOKEN, FakeClock, Run, Waits, gone, script

from trainmaster_trainer import model_export, tasks
from trainmaster_trainer.tasks import TaskWorker, missing_modules

PT = b"PK\x03\x04" + bytes(range(256)) * 40


@pytest.fixture
def platform():
    p = FakePlatform(token=TOKEN)
    p.max_claim_wait = 0.2  # 没有任务时长轮询很快返回，测试结束时线程尽快退出
    yield p
    p.close()


@pytest.fixture
def fake_ml(tmp_path, monkeypatch):
    """让转换子进程加载假模块；返回设置假模块行为的函数（写入 fake_export.json）。"""
    root = export_fakes.write_modules(tmp_path / "fake-ml")
    monkeypatch.setenv("PYTHONPATH", str(root))
    calls = tmp_path / "ultralytics-calls.jsonl"

    def configure(**config: Any) -> Path:
        config = {"calls_file": str(calls), "end2end_head": True, **config}  # 默认按 YOLO26
        export_fakes.write_config(root, **config)
        return calls

    configure()
    return configure


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
    # 按路径的各部分比较（Windows 上的分隔符是 \）
    weights = Path(ultralytics_calls[0]["weights"])
    assert weights.parts[-2:] == (f"model-export-{export_id}", "model.pt")
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
    # 上传之前带上文件大小上报进度（预检），平台确认能接收后才上传
    assert [r["body"] for r in requests if r["path"].endswith("/progress")] == [
        {"stage": "downloading"},
        {"stage": "converting"},
        {"stage": "uploading", "size_bytes": len(exp["output"])},
    ]
    assert all(r["headers"]["X-Node-Key"] == NODE_KEY for r in requests)
    assert all(r["headers"]["Authorization"] == f"Bearer {TOKEN}" for r in requests)
    claim = platform.claims[0]
    kinds = ["model_export", "review_inference", "training_job"]
    assert claim == {"node_key": NODE_KEY, "kinds": kinds, "wait_seconds": 25}
    messages = [r.getMessage() for r in caplog.records]
    assert any(f"model export {export_id} started" in m for m in messages)
    assert any(f"model export {export_id} succeeded" in m for m in messages)


@pytest.mark.parametrize(
    ("dynamic", "postprocess", "nms"), [(True, "raw", None), (False, "nms", True)]
)
def test_options_are_passed_to_the_child(platform, tmp_path, fake_ml, dynamic, postprocess, nms):
    calls = fake_ml(end2end_head=False)
    options = {"imgsz": [480, 640], "dynamic": dynamic, "postprocess": postprocess, "opset": 12}
    export_id = platform.add_export(PT, model={"yolo_version": "yolo11"}, options=options)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id)
    assert exp["status"] == "succeeded", exp
    load_call, export_call = (json.loads(line) for line in calls.read_text().splitlines())
    assert load_call["safe_load"] is True  # 子进程用安全加载读取上传的 pt
    assert export_call["imgsz"] == [480, 640]
    assert export_call["dynamic"] is dynamic and export_call["nms"] is nms
    assert export_call["opset"] == 12
    batch = "batch" if dynamic else 1
    assert exp["result"]["inputs"][0]["shape"][0] == batch


@pytest.mark.parametrize(
    ("end2end_head", "others"),
    [(False, "原始输出（raw）"), (True, "原始输出（raw）/ 端到端（end2end）")],
)
def test_dynamic_nms_is_refused(platform, tmp_path, fake_ml, end2end_head, others):
    # 平台不会下发这个组合（嵌入的 NMS 只处理 batch 中的第一张图片）；节点再防一次，不可重试。
    calls = fake_ml(end2end_head=end2end_head)
    options = {"dynamic": True, "postprocess": "nms"}
    export_id = platform.add_export(PT, model={"yolo_version": "yolo11"}, options=options)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id)
    assert exp["status"] == "failed" and exp["attempts"] == 1
    assert exp["fails"][0]["retryable"] is False
    assert exp["fails"][0]["error"] == (
        "嵌入 NMS 的 ONNX 只能固定 batch 为 1，不能与动态尺寸同时使用；"
        f"请关闭动态尺寸，或选择{others}"
    )
    assert [json.loads(line)["call"] for line in calls.read_text().splitlines()] == ["YOLO"]
    assert exp["output"] is None


@pytest.mark.parametrize(("task", "label"), [("semantic", "语义分割"), ("depth", "深度估计")])
def test_yolo26_models_without_boxes_only_export_raw(platform, tmp_path, fake_ml, task, label):
    # YOLO26 的语义分割、深度估计模型没有端到端头；提示只建议 raw（平台对它们拒绝 nms）。
    fake_ml(task=task, end2end_head=False)
    export_id = platform.add_export(PT, model={"task": task}, options={"postprocess": "end2end"})
    with Run(tmp_path, platform):
        exp = finished(platform, export_id)
    assert exp["fails"] == [
        {
            "error": f"{label}模型没有检测框，不能导出 end2end，请选择原始输出（raw）",
            "detail": exp["fails"][0]["detail"],
            "retryable": False,
        }
    ]
    assert "nms" not in exp["fails"][0]["error"]


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
        "kinds": ["model_export", "review_inference", "training_job"],
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
    if WINDOWS:
        # Windows 没有 SIGKILL：进程崩溃时退出码是 NTSTATUS，这里模拟访问冲突（os._exit 的参数是 C 的 int）。
        body = "import os\nos._exit(0xC0000005 - (1 << 32))\n"
        error = "转换进程崩溃（0xC0000005，访问冲突）"
    else:
        body = "import os, signal\nos.kill(os.getpid(), signal.SIGKILL)\n"
        error = "转换进程被信号 SIGKILL 终止"
    cmd = script(tmp_path, body)
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform, export_command=cmd):
        exp = finished(platform, export_id)
    assert exp["fails"][0]["error"].startswith(error)


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


@pytest.mark.parametrize(
    ("refuse", "status", "message"),
    [("disk", 507, "磁盘剩余空间不足：可用 100 B，至少需要"),
     ("too_large", 413, "转换结果不能超过 4 GiB")],
)  # fmt: skip
def test_upload_preflight_refused(platform, tmp_path, fake_ml, refuse, status, message):
    # 平台在读取上传内容之前就拒绝（较大的文件只能看到连接被重置）：先带上大小问一次，不上传、不重试。
    if refuse == "disk":
        platform.free_bytes = 100
    else:
        platform.hooks["progress"] = lambda exp, req: (
            (413, {"error": {"code": "payload_too_large", "message": "转换结果不能超过 4 GiB"}})
            if req["body"].get("size_bytes")
            else None
        )
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform):
        exp = finished(platform, export_id, "failed")
    assert exp["attempts"] == 1
    fail = exp["fails"][0]
    assert fail["retryable"] is False
    assert fail["error"].startswith(f"上传转换结果失败：{message}")
    assert fail["detail"].startswith(f"HTTP {status} ")
    methods = [r["method"] for r in task_requests(platform, export_id)]
    assert "PUT" not in methods  # 没有上传


@pytest.mark.parametrize("how", ["conflict", "deleted"])
def test_upload_preflight_conflict_abandons(platform, tmp_path, fake_ml, how):
    def progress(exp, req):
        if not req["body"].get("size_bytes"):
            return None
        if how == "deleted":
            platform.delete_export(exp["id"])
            return None  # 照常处理：404
        return 409, {"error": {"code": "conflict", "message": "转换任务不属于该节点"}}

    platform.hooks["progress"] = progress
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform) as run:
        wait_until(lambda: run.worker.tasks_run == 1)
    actions = [r["path"].rsplit("/", 1)[1] for r in task_requests(platform, export_id)]
    assert actions == ["progress", "input", "progress", "progress"]  # 不上传，也不上报失败


@pytest.mark.parametrize(
    ("recheck", "outcome"),
    [(None, "retried"), (409, "abandoned"), (404, "abandoned"), (507, "failed")],
)
def test_upload_connection_reset_is_classified(platform, tmp_path, fake_ml, recheck, outcome):
    # 上传时连接被重置（平台没有读取内容就拒绝了，或网络故障）：再上报一次进度（不带大小）来区分。
    puts: list[bool] = []

    def output(exp, req):
        puts.append(True)
        return DROP

    def progress(exp, req):
        if not puts or req["body"] != {"stage": "uploading"} or recheck is None:
            return None
        if recheck == 404:
            platform.delete_export(exp["id"])
            return None
        if recheck == 409:
            return 409, {"error": {"code": "conflict", "message": "转换任务不属于该节点"}}
        return 507, {"error": {"code": "insufficient_storage", "message": "磁盘剩余空间不足"}}

    platform.hooks["output"] = output
    platform.hooks["progress"] = progress
    export_id = platform.add_export(PT)
    with Run(tmp_path, platform) as run:
        if outcome == "abandoned":
            wait_until(lambda: run.worker.tasks_run == 1)
        else:
            exp = finished(platform, export_id, "failed")
    requests = task_requests(platform, export_id)
    if outcome == "abandoned":
        actions = [r["path"].rsplit("/", 1)[1] for r in requests]
        assert actions == ["progress", "input", "progress", "progress", "output", "progress"]
        return
    bodies = [r["body"] for r in requests if r["path"].endswith("/progress")]
    assert bodies[2]["size_bytes"] > 0 and bodies[3] == {"stage": "uploading"}
    if outcome == "retried":  # 网络故障：可重试，3 次后失败
        assert exp["attempts"] == 3
        assert [f["retryable"] for f in exp["fails"]] == [True, True, True]
        assert exp["fails"][0]["error"].startswith("上传转换结果失败：")
    else:  # 平台磁盘空间不足：不可重试，带上平台的说明
        assert exp["attempts"] == 1
        assert exp["fails"] == [
            {
                "error": "上传转换结果失败：磁盘剩余空间不足",
                "detail": "HTTP 507 insufficient_storage: 磁盘剩余空间不足",
                "retryable": False,
            }
        ]


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
        assert gone(pid)
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
        assert gone(pid)
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
    work = tmp_path / "state" / "work"
    stale = [work / "model-export-99", work / "review-inference-12"]
    for path in stale:
        (path / "images").mkdir(parents=True)
        (path / "model.pt").write_bytes(b"left over")
    keep = work / "other"
    keep.mkdir()
    with Run(tmp_path, platform):
        wait_until(lambda: not any(path.exists() for path in stale))
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
    installed = {"torch"}
    with Run(
        tmp_path,
        platform,
        waits=Waits(clock),
        clock=clock,
        find_spec=lambda name: object() if name in installed else None,
    ) as run:
        wait_until(lambda: run.waits.calls.count(300.0) >= 3)
        assert platform.claims == []  # 缺少 ultralytics：三类任务都不领取
        installed.add("ultralytics")  # 之后安装了依赖（裸机节点）：可以识别和训练，还不能转换
        wait_until(lambda: platform.claims)
        assert platform.claims[0]["kinds"] == ["review_inference", "training_job"]
        installed.add("onnx")
        clock.now += 300  # 下一次检查时 model_export 也可以领取
        wait_until(
            lambda: (
                platform.claims[-1]["kinds"] == ["model_export", "review_inference", "training_job"]
            )
        )
    messages = [r.getMessage() for r in caplog.records]
    assert (
        sum("not claiming model_export tasks: cannot find ultralytics, onnx" in m for m in messages)
        == 1
    )
    assert (
        sum("not claiming review_inference tasks: cannot find ultralytics" in m for m in messages)
        == 1
    )
    assert (
        sum("not claiming training_job tasks: cannot find ultralytics" in m for m in messages) == 1
    )
    assert any("review_inference tasks enabled" in m for m in messages)
    assert any("training_job tasks enabled" in m for m in messages)
    assert any("model_export tasks enabled" in m for m in messages)


def test_review_inference_needs_torch_and_ultralytics_only():
    assert tasks.REQUIRED_MODULES == {
        "model_export": ("torch", "ultralytics", "onnx"),
        "review_inference": ("torch", "ultralytics"),
        "training_job": ("torch", "ultralytics"),
    }


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
        "import sys, trainmaster_trainer.agent, trainmaster_trainer.tasks, trainmaster_trainer.model_export,"
        " trainmaster_trainer.review_inference, trainmaster_trainer.training;"
        "print(sorted(m for m in ('torch', 'ultralytics', 'onnx', 'onnxruntime', 'numpy', 'cv2', 'PIL')"
        " if m in sys.modules))"
    )
    out = subprocess.run(
        [sys.executable, "-c", code], capture_output=True, text=True, check=True, timeout=60,
        cwd=str(Path(__file__).resolve().parent.parent),
    )  # fmt: skip
    assert out.stdout.strip() == "[]"
