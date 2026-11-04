"""review_inference.ReviewInferenceTask：任务线程对假平台（tests/fake_platform.py）领取并执行审图识别任务。

识别子进程默认是真实的 python -m trainmaster_trainer.review_inference，ultralytics / torch / numpy / cv2 / PIL
用 tests/inference_fakes.py 中的假模块（通过 PYTHONPATH 加载）；超时、停止等用脚本代替子进程。
"""

from __future__ import annotations

import json
import logging
import shutil
import time
from pathlib import Path
from typing import Any

import inference_fakes
import pytest
from fake_platform import CLAIM, DROP, FakePlatform, wait_until
from inference_fakes import fake_image
from task_harness import NODE_KEY, TOKEN, Run, gone, script

from trainmaster_trainer import model_export
from trainmaster_trainer import review_inference as ri

PT = b"PK\x03\x04 fake checkpoint " + bytes(range(256)) * 4
BUS = [[3.83, 229.36, 796.19, 728.41, 0.94, 2], [671.02, 394.83, 809.81, 878.71, 0.88, 0]]
REVIEWS = "/api/v1/compute/agent/review-inferences/"


@pytest.fixture
def platform():
    p = FakePlatform(token=TOKEN)
    p.max_claim_wait = 0.2  # 没有任务时长轮询很快返回，测试结束时线程尽快退出
    yield p
    p.close()


@pytest.fixture(autouse=True)
def fast_retries(monkeypatch):
    """任务内部重试与上报失败的间隔缩短（真实为 1 秒、3 秒）。"""
    monkeypatch.setattr(ri, "FAIL_RETRY_DELAYS", (0.01, 0.02))
    monkeypatch.setattr(model_export, "FAIL_RETRY_DELAYS", (0.01, 0.02))


@pytest.fixture
def fake_ml(tmp_path, monkeypatch):
    """让识别子进程加载假模块；返回设置假模块行为的函数（写入 fake_inference.json），它返回调用记录的文件。"""
    root = inference_fakes.write_modules(tmp_path / "fake-ml")
    monkeypatch.setenv("PYTHONPATH", str(root))
    calls = tmp_path / "calls.jsonl"

    def configure(**config: Any) -> Path:
        config = {"calls_file": str(calls), "boxes": {"bus": BUS}, **config}
        inference_fakes.write_config(root, **config)
        return calls

    configure()
    return configure


def finished(platform: FakePlatform, review_id: int, *statuses: str) -> dict[str, Any]:
    statuses = statuses or ("ready", "failed")
    return wait_until(
        lambda: (r := platform.review(review_id)) and r["status"] in statuses and r, timeout=30
    )


def review_requests(platform: FakePlatform, review_id: int) -> list[dict[str, Any]]:
    prefix = f"{REVIEWS}{review_id}/"
    with platform.lock:
        return [r for r in platform.requests if r["path"].startswith(prefix)]


def action(request: dict[str, Any]) -> str:
    """请求的接口：model / images?after=..&limit=.. / file N / progress / results / complete / fail。"""
    rest = request["path"].split("/", 7)[7]
    if rest.startswith("images/"):
        return f"file {rest.split('/')[1]}"
    return rest


def recorded(calls: Path) -> list[dict[str, Any]]:
    return [json.loads(line) for line in calls.read_text().splitlines()] if calls.exists() else []


def results_posted(platform: FakePlatform, review_id: int) -> list[list[dict[str, Any]]]:
    return [
        r["body"]["results"] for r in review_requests(platform, review_id) if action(r) == "results"
    ]


def ready_child(tmp_path: Path, body: str) -> list[str]:
    """脚本子进程：输出 ready，然后按 body 处理每个请求 req（body 缩进 4 个空格）。"""
    return script(
        tmp_path,
        "import json, os, sys, time\n"
        'print(json.dumps({"ready": True, "device": "cpu", "names": {"0": "person"}}), flush=True)\n'
        "for line in sys.stdin:\n"
        "    if not line.strip():\n"
        "        break\n"
        "    req = json.loads(line)\n" + body,
    )


ANSWER = (
    '    print(json.dumps({"id": req["id"], "ok": True, "width": 40, "height": 30,'
    ' "boxes": [[1, 2, 30, 20, 0.9, 0]]}), flush=True)\n'
)


def slow_after_first(tmp_path: Path) -> tuple[list[str], Path]:
    """第一张图片立即回答；之后的图片写下 pid 后一直识别（等待被结束）。"""
    pid_file = tmp_path / "child.pid"
    body = (
        '    if req["id"] != 1:\n'
        f"        open({str(pid_file)!r}, 'w').write(str(os.getpid()))\n"
        "        time.sleep(30)\n" + ANSWER
    )
    return ready_child(tmp_path, body), pid_file


# ---- 正常流程 ----


def test_review_inference_end_to_end(platform, tmp_path, fake_ml, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    calls = fake_ml()
    review_id = platform.add_review(
        PT,
        {
            1: fake_image(810, 1080, "bus"),
            2: fake_image(32, 16, fmt="GIF89a"),  # OpenCV 读不了，Pillow 读
            3: None,  # 数据集中的图片文件已被删除：下载时 404
            4: b"garbage",
            5: fake_image(100, 80),  # 没有目标
        },
        file_names={2: "b.gif"},
    )
    with Run(tmp_path, platform) as run:
        review = finished(platform, review_id)
        assert review["status"] == "ready", review
        wait_until(lambda: run.worker.tasks_run == 1)
        assert not (run.state_dir / "work" / f"review-inference-{review_id}").exists()

    assert review["attempts"] == 1
    images = review["images"]
    assert images[1]["result"] == {
        "id": 1,
        "ok": True,
        "width": 810,
        "height": 1080,
        "boxes": [
            {"name": "car", "class_id": 2, "confidence": 0.94,
             "xmin": 3.83, "ymin": 229.36, "xmax": 796.19, "ymax": 728.41},
            {"name": "person", "class_id": 0, "confidence": 0.88,
             "xmin": 671.02, "ymin": 394.83, "xmax": 809.81, "ymax": 878.71},
        ],
    }  # fmt: skip
    assert images[2]["result"] == {"id": 2, "ok": True, "width": 32, "height": 16, "boxes": []}
    assert images[3]["result"] == {
        "id": 3,
        "ok": False,
        "error": "无法下载图片：数据集中的图片文件已不存在：img-3.jpg",
    }
    assert images[4]["result"] == {
        "id": 4,
        "ok": False,
        "error": "无法读取图片（文件已损坏，或不是支持的图片格式）",
    }
    assert images[5]["result"] == {"id": 5, "ok": True, "width": 100, "height": 80, "boxes": []}
    result = review["result"]
    assert {
        k: v for k, v in result.items() if k not in ("elapsed_seconds", "images_per_second")
    } == {
        "device": "cpu",
        "device_name": None,
        "torch_version": "2.14.1+cpu",
        "ultralytics_version": "8.4.198",
        "opencv_version": "4.12.0",
        "node_name": "gpu-01",
        "images_inferred": 5,
        "images_failed": 2,
    }
    assert isinstance(result["elapsed_seconds"], float) and result["elapsed_seconds"] >= 0
    assert result["images_per_second"] is None or result["images_per_second"] > 0
    assert review["progress"] == [("downloading", "正在下载模型"), ("inferring", "正在加载模型")]

    requests = review_requests(platform, review_id)
    assert [action(r) for r in requests if not action(r).startswith("file")] == [
        "progress",
        "model",
        "progress",
        "images?after=0&limit=200",
        "images?after=5&limit=200",  # 第一页不满也再取一页：直到某一页为空
        "results",  # 一次上报这一轮的结果
        "images?after=0&limit=200",  # 第二轮：确认平台已经记录了所有结果
        "complete",
    ]
    assert sorted(action(r) for r in requests if action(r).startswith("file")) == [
        f"file {i}" for i in range(1, 6)
    ]
    assert review["results_posts"] == [5]
    assert all(r["headers"]["X-Node-Key"] == NODE_KEY for r in requests)
    assert all(r["headers"]["Authorization"] == f"Bearer {TOKEN}" for r in requests)
    assert platform.claims[0] == {
        "node_key": NODE_KEY,
        "kinds": ["model_export", "review_inference"],
        "wait_seconds": 25,
    }

    model_calls = recorded(calls)
    assert model_calls[0]["call"] == "YOLO" and model_calls[0]["task"] == "detect"
    assert model_calls[0]["weights"].endswith(f"review-inference-{review_id}/model.pt")
    assert model_calls[0]["safe_load"] is True
    predict = [c for c in model_calls if c["call"] == "predict"]
    assert [c["tag"] for c in predict] == ["warmup", "bus", "", ""]  # 损坏的图片没有交给模型
    assert predict[1] == {
        "call": "predict",
        "tag": "bus",
        "shape": [1080, 810, 3],
        "conf": 0.25,
        "iou": 0.7,
        "max_det": 300,
        "device": "cpu",
        "verbose": False,
        "imgsz": [640, 640],
    }
    messages = [r.getMessage() for r in caplog.records]
    assert any(f"review inference {review_id} started" in m for m in messages)
    assert any(
        f"review inference {review_id} succeeded" in m and "5 images (2 failed)" in m
        for m in messages
    )


def test_options_and_model_are_passed_to_the_child(platform, tmp_path, fake_ml):
    calls = fake_ml(task="obb", boxes={"court": [[0, 1, 50, 40, 0.5, 1]]})
    review_id = platform.add_review(
        PT,
        {7: fake_image(64, 48, "court")},
        model={"task": "obb", "yolo_version": "yolo11", "class_names": ["a", "b"]},
        options={"conf": 0.4, "iou": 0.5, "imgsz": [480, 640], "max_det": 50},
    )
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    model_calls = recorded(calls)
    assert model_calls[0]["task"] == "obb"
    predict = model_calls[-1]
    assert (predict["conf"], predict["iou"], predict["imgsz"], predict["max_det"]) == (
        0.4,
        0.5,
        [480, 640],
        50,
    )
    # 类别名称以模型自带的为准（与平台记录的 class_names 一致时没有区别）
    assert review["images"][7]["result"]["boxes"] == [
        {"name": "bicycle", "class_id": 1, "confidence": 0.5,
         "xmin": 0.0, "ymin": 1.0, "xmax": 50.0, "ymax": 40.0},
    ]  # fmt: skip


def test_pages_and_batches(platform, tmp_path, fake_ml, monkeypatch):
    monkeypatch.setattr(ri, "PAGE_SIZE", 2)
    monkeypatch.setattr(ri, "FLUSH_COUNT", 3)
    review_id = platform.add_review(PT, {i: fake_image(20, 10) for i in range(1, 8)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    assert review["results_posts"] == [3, 3, 1]  # 满 3 条上报一次，一轮结束时上报剩下的
    pages = [action(r) for r in review_requests(platform, review_id) if action(r).startswith("im")]
    assert pages == [
        "images?after=0&limit=2",
        "images?after=2&limit=2",
        "images?after=4&limit=2",
        "images?after=6&limit=2",
        "images?after=7&limit=2",
        "images?after=0&limit=2",
    ]
    assert all(img["inferred"] for img in review["images"].values())
    assert review["result"]["images_inferred"] == 7


def test_results_are_split_to_stay_under_one_mib(platform, tmp_path, fake_ml, monkeypatch):
    # 每张图片 1000 个框（约 110 KB）：每次上报的请求体不超过 1 MiB（平台的上限）
    monkeypatch.setattr(ri, "FLUSH_BYTES", 4 << 20)
    rows = [
        [i % 50, i // 50, i % 50 + 5.123456, i // 50 + 5.654321, 0.5, i % 3] for i in range(1000)
    ]
    fake_ml(boxes={"many": rows})
    review_id = platform.add_review(PT, {i: fake_image(100, 100, "many") for i in range(1, 13)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    assert sum(review["results_posts"]) == 12 and len(review["results_posts"]) >= 2
    for r in review_requests(platform, review_id):
        if action(r) == "results":
            assert int(r["headers"]["Content-Length"]) <= 1 << 20
    assert all(len(img["result"]["boxes"]) == 1000 for img in review["images"].values())


def test_continues_with_the_images_not_inferred(platform, tmp_path, fake_ml):
    # 上次的运行已经识别（并上报）了前两张图片：只下载、识别剩下的
    review_id = platform.add_review(
        PT, {i: fake_image(20, 10) for i in range(1, 5)}, inferred=(1, 2)
    )
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    files = [
        action(r) for r in review_requests(platform, review_id) if action(r).startswith("file ")
    ]
    assert sorted(files) == ["file 3", "file 4"]
    assert review["result"]["images_inferred"] == 2


def test_results_the_platform_lost_are_inferred_again(platform, tmp_path, fake_ml):
    # 平台回答了上报却没有记录（如在提交之前重启）：下一轮重新列出这些图片，再识别一次
    lost: list[int] = []

    def results(review, request):
        if not lost:
            lost.append(len(request["body"]["results"]))
            return 200, {"lease_seconds": 60, "accepted": 0, "remaining": 2}
        return None

    platform.hooks["review:results"] = results
    review_id = platform.add_review(PT, {1: fake_image(20, 10), 2: fake_image(20, 10, "bus")})
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    assert lost == [2] and review["results_posts"] == [2]
    files = [
        action(r) for r in review_requests(platform, review_id) if action(r).startswith("file ")
    ]
    assert sorted(files) == ["file 1", "file 1", "file 2", "file 2"]
    assert review["result"]["images_inferred"] == 2  # 每张图片只算一次


def test_results_that_are_never_recorded_fail_the_attempt(platform, tmp_path, fake_ml):
    platform.hooks["review:results"] = lambda review, request: (
        200,
        {"lease_seconds": 60, "accepted": 0, "remaining": 1},
    )
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    assert review["attempts"] == 3  # 可重试：平台重新排队，直到用完次数
    assert review["fails"][0] == {
        "error": "平台在 3 轮识别后仍列出没有识别的图片：上报的识别结果没有被平台记录",
        "detail": "",
        "retryable": True,
    }
    files = [
        action(r) for r in review_requests(platform, review_id) if action(r).startswith("file ")
    ]
    assert len(files) == 9  # 每次运行 3 轮


def test_keeps_the_lease_with_progress(platform, tmp_path):
    cmd = ready_child(tmp_path, "    time.sleep(0.4)\n" + ANSWER)
    review_id = platform.add_review(PT, {i: fake_image(40, 30) for i in range(1, 5)}, inferred=(1,))
    with Run(tmp_path, platform, inference_command=cmd, progress_interval=0.1):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    progress = review["progress"]
    assert progress[:2] == [("downloading", "正在下载模型"), ("inferring", "正在加载模型")]
    inferring = [message for stage, message in progress[2:] if stage == "inferring"]
    assert len(inferring) >= 5  # 识别时定期续租
    # 进度是平台上已经识别的图片数（含上次运行识别的那一张）；结果攒批上报后才增加
    assert inferring[0] == "已识别 1 / 4"
    assert set(inferring) <= {"已识别 1 / 4", "已识别 2 / 4", "已识别 3 / 4", "已识别 4 / 4"}
    assert all(stage == "inferring" for stage, _ in progress[1:])


def test_results_are_reported_while_waiting(platform, tmp_path, monkeypatch):
    # 识别得慢时也按时上报（FLUSH_INTERVAL），不必等到满一批
    monkeypatch.setattr(ri, "FLUSH_INTERVAL", 0.2)
    cmd = ready_child(tmp_path, '    time.sleep(0.5 if req["id"] == 2 else 0)\n' + ANSWER)
    review_id = platform.add_review(PT, {1: fake_image(40, 30), 2: fake_image(40, 30)})
    with Run(tmp_path, platform, inference_command=cmd):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    assert review["results_posts"] == [1, 1]
    assert review["images"][1]["result"]["boxes"][0]["name"] == "person"


# ---- 放弃、停止、超时 ----


@pytest.mark.parametrize("how", ["conflict", "deleted"])
def test_progress_conflict_abandons_the_task(platform, tmp_path, how, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    cmd, pid_file = slow_after_first(tmp_path)
    review_id = platform.add_review(PT, {1: fake_image(40, 30), 2: fake_image(40, 30)})
    with Run(tmp_path, platform, inference_command=cmd, progress_interval=0.1) as run:
        wait_until(lambda: pid_file.exists() and pid_file.read_text())
        pid = int(pid_file.read_text())
        if how == "conflict":  # 审图任务被转给了其它节点（租约过期后被重新领取）
            platform.hooks["review:progress"] = lambda review, req: (
                409,
                {"error": {"code": "conflict", "message": "审图任务不属于该节点或已经结束"}},
            )
        else:  # 用户删除了审图任务
            platform.delete_review(review_id)
        started = time.monotonic()
        wait_until(lambda: run.worker.tasks_run == 1, timeout=15)
        assert time.monotonic() - started < 10
        assert gone(pid)
        assert not (run.state_dir / "work" / f"review-inference-{review_id}").exists()
    actions = [action(r) for r in review_requests(platform, review_id)]
    assert "fail" not in actions and "complete" not in actions
    assert any(f"review inference {review_id} abandoned" in r.getMessage() for r in caplog.records)


def test_stop_during_inference(platform, tmp_path):
    cmd, pid_file = slow_after_first(tmp_path)
    review_id = platform.add_review(PT, {1: fake_image(40, 30), 2: fake_image(40, 30)})
    run = Run(tmp_path, platform, inference_command=cmd)
    with run:
        wait_until(lambda: pid_file.exists() and pid_file.read_text())
        pid = int(pid_file.read_text())
        task = run.worker._current
        assert task is not None
        wait_until(lambda: task.processed == 1)  # 第一张已经识别（结果还没有上报）
        started = time.monotonic()
        run.worker.stop()
        run.worker.join()
        assert time.monotonic() - started < 10
        assert not run.worker.alive
        assert gone(pid)
    review = platform.review(review_id)
    assert review["status"] == "queued"  # 可重试：立即重新排队
    assert review["fails"] == [
        {"error": "训练节点正在停止，识别已中断", "detail": "", "retryable": True}
    ]
    # 停止前把已经识别的结果交给平台：重新领取后不必再识别
    assert review["images"][1]["inferred"] is True and review["images"][2]["inferred"] is False
    assert [action(r) for r in review_requests(platform, review_id)][-2:] == ["results", "fail"]
    assert not (run.state_dir / "work" / f"review-inference-{review_id}").exists()


def test_task_claimed_while_stopping_is_given_back(platform, tmp_path):
    review_id = platform.add_review(PT, {1: fake_image(40, 30)})
    run = Run(tmp_path, platform)
    resp = run.worker.client.post(
        CLAIM, {"node_key": NODE_KEY, "kinds": ["review_inference"], "wait_seconds": 0}
    )
    run.worker.stop()
    run.worker._execute(resp["task"])
    review = platform.review(review_id)
    assert review["status"] == "queued"
    assert review["fails"][0]["retryable"] is True
    assert [action(r) for r in review_requests(platform, review_id)] == ["fail"]


def test_image_timeout_fails_the_task(platform, tmp_path, fake_ml):
    fake_ml(slow_seconds=30)
    review_id = platform.add_review(
        PT, {1: fake_image(40, 30), 2: fake_image(40, 30, "slow"), 3: fake_image(40, 30)}
    )
    with Run(tmp_path, platform, inference_image_timeout=1):
        review = finished(platform, review_id)
    assert review["status"] == "failed"
    assert review["attempts"] == 1  # 换一个节点也会超时：不重试
    fail = review["fails"][0]
    assert fail["retryable"] is False
    assert fail["error"].startswith("识别超时（单张图片 1 秒内没有识别完成）")
    # 超时之前识别的结果已经交给平台
    assert [img["inferred"] for img in review["images"].values()] == [True, False, False]


def test_out_of_memory_stops_the_task(platform, tmp_path, fake_ml):
    review_id = platform.add_review(
        PT, {1: fake_image(40, 30, "bus"), 2: fake_image(40, 30, "oom"), 3: fake_image(40, 30)}
    )
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "failed"
    fail = review["fails"][0]
    assert fail["retryable"] is False
    assert fail["error"] == (
        "训练节点的内存不足，无法用该模型识别（输入尺寸 640×640）。请减小输入尺寸后重新识别"
    )
    assert "CUDA out of memory" in fail["detail"]
    assert review["images"][1]["inferred"] is True
    assert review["images"][2]["inferred"] is False


def test_an_image_error_does_not_stop_the_task(platform, tmp_path, fake_ml):
    review_id = platform.add_review(PT, {1: fake_image(40, 30, "boom"), 2: fake_image(40, 30)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    assert review["images"][1]["result"] == {
        "id": 1,
        "ok": False,
        "error": "识别失败：ValueError: boom",
    }
    assert review["images"][2]["result"]["ok"] is True


# ---- 平台接口的错误 ----


def test_transient_errors_are_retried_inside_the_task(platform, tmp_path, fake_ml):
    failures = {"review:results": 1, "review:images": 1, "review:file": 1, "review:complete": 1}

    def flaky(name: str):
        def hook(review, request):
            if failures[name]:
                failures[name] -= 1
                if name == "review:file":
                    return DROP  # 下载时连接中断
                return 500, {"error": {"code": "internal", "message": "数据库错误"}}
            return None

        return hook

    for name in failures:
        platform.hooks[name] = flaky(name)
    review_id = platform.add_review(PT, {1: fake_image(20, 10), 2: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    assert review["attempts"] == 1 and review["fails"] == []  # 长时间的识别不因为一次错误重新开始
    assert failures == dict.fromkeys(failures, 0)


def test_image_list_unavailable(platform, tmp_path, fake_ml):
    platform.hooks["review:images"] = lambda review, request: (
        503,
        {"error": {"code": "unavailable", "message": "服务暂不可用"}},
    )
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    assert review["attempts"] == 3
    assert review["fails"][0]["error"] == "获取图片列表失败：服务暂不可用"
    assert review["fails"][0]["retryable"] is True
    pages = [action(r) for r in review_requests(platform, review_id) if action(r).startswith("im")]
    assert len(pages) == 9  # 每次运行试 3 次


@pytest.mark.parametrize(
    ("status", "message", "retryable"),
    [
        (422, "results 必须是最多 500 条的数组", False),
        (507, "平台的磁盘空间不足", False),
        (401, "节点令牌不正确", True),
    ],
)
def test_results_rejected(platform, tmp_path, fake_ml, status, message, retryable):
    platform.hooks["review:results"] = lambda review, request: (
        status,
        {"error": {"code": "x", "message": message}},
    )
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    assert review["fails"][0]["error"] == f"上报识别结果失败：{message}"
    assert review["fails"][0]["retryable"] is retryable
    assert review["attempts"] == (3 if retryable else 1)


def test_complete_conflict_abandons_the_task(platform, tmp_path, fake_ml, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    platform.hooks["review:complete"] = lambda review, request: (
        409,
        {"error": {"code": "conflict", "message": "还有 1 张图片没有识别"}},
    )
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform) as run:
        wait_until(lambda: run.worker.tasks_run == 1)
    review = platform.review(review_id)
    assert review["status"] == "running" and review["fails"] == []  # 留给租约过期处理
    assert any(f"review inference {review_id} abandoned" in r.getMessage() for r in caplog.records)


def test_image_list_must_be_valid(platform, tmp_path, fake_ml):
    platform.hooks["review:images"] = lambda review, request: (
        200,
        {"items": [{"id": 5, "url": f"{REVIEWS}12/images/5/file"}, {"id": 5}], "remaining": 2},
    )
    review_id = platform.add_review(PT, {5: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    assert review["fails"][0]["error"] == "平台返回的图片列表不合法（图片 5 不在 5 之后）"
    assert review["fails"][0]["retryable"] is False


def test_image_too_large(platform, tmp_path, fake_ml, monkeypatch):
    monkeypatch.setattr(ri, "MAX_IMAGE_BYTES", 30)
    review_id = platform.add_review(PT, {1: b"FAKEIMG 20x10 " + b"x" * 40, 2: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id)
    assert review["status"] == "ready", review
    assert review["images"][1]["result"] == {
        "id": 1,
        "ok": False,
        "error": "图片太大（54 B），训练节点最多识别 30 B 的图片",
    }
    files = [
        action(r) for r in review_requests(platform, review_id) if action(r).startswith("file ")
    ]
    assert files == ["file 2"]  # 没有下载


# ---- 模型 ----


@pytest.mark.parametrize(
    ("how", "error"),
    [
        ("sha256", "下载的模型文件校验失败（sha256 不一致），可能是传输出错"),
        ("corrupt", "下载的模型文件校验失败（sha256 不一致），可能是传输出错"),
        ("short", f"下载的模型文件大小不一致（收到 {len(PT) - 10} 字节，应为 {len(PT)} 字节）"),
    ],
)
def test_model_download_is_verified(platform, tmp_path, fake_ml, how, error):
    calls = fake_ml()
    if how == "corrupt":
        platform.hooks["review:model"] = lambda review, request: (200, b"X" * len(PT))
    elif how == "short":
        platform.hooks["review:model"] = lambda review, request: (200, PT[:-10])
    sha = {"sha256": "0" * 64} if how == "sha256" else {}
    review_id = platform.add_review(PT, {1: fake_image(20, 10)}, **sha)
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    assert review["attempts"] == 3
    assert review["fails"][0]["error"] == error
    assert review["fails"][0]["retryable"] is True
    assert recorded(calls) == []  # 校验失败时不加载模型
    assert not any(action(r).startswith("im") for r in review_requests(platform, review_id))


def test_model_deleted_before_download(platform, tmp_path, fake_ml, caplog):
    # 平台发现模型文件已不存在时让审图任务失败并返回 404：节点放弃任务
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    platform.hooks["review:model"] = lambda review, request: (
        404,
        {"error": {"code": "not_found", "message": "模型文件已不存在"}},
    )
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform) as run:
        wait_until(lambda: run.worker.tasks_run == 1)
    assert platform.review(review_id)["fails"] == []
    assert any(f"review inference {review_id} abandoned" in r.getMessage() for r in caplog.records)


def test_model_load_failure_is_not_retried(platform, tmp_path, fake_ml):
    fake_ml(load_error=["RuntimeError", "PytorchStreamReader failed reading zip archive"])
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    assert review["attempts"] == 1
    fail = review["fails"][0]
    assert fail["retryable"] is False
    assert "模型文件" in fail["error"]
    assert "PytorchStreamReader" in fail["detail"]
    assert "ultralytics prints to stdout while loading" in fail["detail"]  # 子进程的输出


def test_classify_model_is_refused(platform, tmp_path, fake_ml):
    fake_ml(task="classify")
    review_id = platform.add_review(PT, {1: fake_image(20, 10)}, model={"task": "classify"})
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    assert review["fails"][0]["error"] == "分类模型没有检测框，不能用于审图"
    assert review["fails"][0]["retryable"] is False


def test_child_crash_while_loading(platform, tmp_path):
    cmd = script(
        tmp_path, "import sys\nprint('Segmentation fault', file=sys.stderr)\nsys.exit(139)\n"
    )
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform, inference_command=cmd):
        review = finished(platform, review_id, "failed")
    fail = review["fails"][0]
    assert fail["error"] == "识别进程意外退出（退出码 139）"
    assert fail["retryable"] is False
    assert "Segmentation fault" in fail["detail"]


# ---- 任务数据与本机环境 ----


@pytest.mark.parametrize(
    ("task", "field"),
    [
        ({"options": {"conf": 0, "iou": 0.7, "imgsz": [640, 640], "max_det": 300}}, "options.conf"),
        ({"options": {"conf": 0.25, "iou": 0.7, "imgsz": "big", "max_det": 300}}, "options.imgsz"),
        ({"model": {"size_bytes": 10, "sha256": "nope"}}, "model.sha256"),
        ({"images_url": "http://evil/x"}, "images_url"),
        ({"results_url": None}, "results_url"),
    ],
)
def test_malformed_task(platform, tmp_path, task, field):
    review_id = platform.add_review(PT, {1: fake_image(20, 10)}, task=task)
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    fail = review["fails"][0]
    assert fail["error"].startswith(
        f"训练节点无法识别平台下发的审图识别任务（字段 {field} 缺失或不合法）"
    )
    assert fail["retryable"] is False
    assert [action(r) for r in review_requests(platform, review_id)] == ["fail"]


def test_disk_space_is_checked(platform, tmp_path, monkeypatch):
    usage = shutil.disk_usage(tmp_path)
    monkeypatch.setattr(ri.shutil, "disk_usage", lambda path: usage._replace(free=1 << 20))
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    assert review["fails"][0]["error"].startswith("训练节点的磁盘空间不足：识别需要约 256.0 MB")
    assert review["fails"][0]["retryable"] is True
    assert review_requests(platform, review_id)[0]["path"].endswith("/fail")  # 没有下载


def test_unexpected_error_is_reported(platform, tmp_path, fake_ml, monkeypatch):
    def broken(self, info, folder):
        raise KeyError("boom")

    monkeypatch.setattr(ri.ReviewInferenceTask, "_infer_pass", broken)
    review_id = platform.add_review(PT, {1: fake_image(20, 10)})
    with Run(tmp_path, platform):
        review = finished(platform, review_id, "failed")
    fail = review["fails"][0]
    assert fail["error"] == "训练节点执行识别时出错：KeyError: 'boom'"
    assert fail["retryable"] is True
    assert "KeyError" in fail["detail"]
