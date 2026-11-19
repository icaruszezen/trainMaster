"""模型评估任务（evaluation.py）的单元测试：COCO 10-IoU mAP 与曲线计算、spec 校验、
本地零拷贝与 HTTP 下载回退、子进程协议、gzip 结果上传、取消与 TaskWorker 分发。
"""

from __future__ import annotations

import gzip
import hashlib
import json
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Any

from trainmaster_trainer import evaluation, tasks
from trainmaster_trainer.client import Client


class TestEvaluationMath(unittest.TestCase):
    def test_box_iou(self) -> None:
        self.assertAlmostEqual(evaluation.box_iou(0, 0, 10, 10, 0, 0, 10, 10), 1.0, places=6)
        self.assertEqual(evaluation.box_iou(0, 0, 10, 10, 20, 20, 30, 30), 0.0)
        self.assertAlmostEqual(evaluation.box_iou(0, 0, 10, 10, 5, 0, 15, 10), 1.0 / 3.0, places=6)

    def test_compute_curves_exact_coco(self) -> None:
        classes = [{"name": "scratch", "color": "#2563eb"}]
        # 完美框 -> AP50 = AP75 = AP50-95 = 1.0
        perfect = [
            {
                "sample_id": 1,
                "gt_objects": [{"name": "scratch", "xmin": 0, "ymin": 0, "xmax": 100, "ymax": 100}],
                "predictions": [
                    {"name": "scratch", "xmin": 0, "ymin": 0, "xmax": 100, "ymax": 100, "conf": 0.9}
                ],
                "inference_error": "",
            }
        ]
        c1 = evaluation.compute_curves(classes, perfect)
        self.assertAlmostEqual(c1["map50"], 1.0, places=4)
        self.assertAlmostEqual(c1["map75"], 1.0, places=4)
        self.assertAlmostEqual(c1["map50_95"], 1.0, places=4)
        self.assertAlmostEqual(c1["best_f1"], 1.0, places=4)
        self.assertEqual(c1["pr"][0]["name"], "all")
        self.assertEqual(len(c1["pr"][0]["x"]), 101)

        # IoU = 0.60 -> 命中 0.50, 0.55, 0.60（3/10），故 AP50=1.0, AP75=0.0, AP50-95=0.30
        mid = [
            {
                "sample_id": 1,
                "gt_objects": [{"name": "scratch", "xmin": 0, "ymin": 0, "xmax": 100, "ymax": 100}],
                "predictions": [
                    {
                        "name": "scratch",
                        "xmin": 25,
                        "ymin": 0,
                        "xmax": 125,
                        "ymax": 100,
                        "conf": 0.85,
                    }
                ],
                "inference_error": "",
            }
        ]
        c2 = evaluation.compute_curves(classes, mid)
        self.assertAlmostEqual(c2["map50"], 1.0, places=4)
        self.assertAlmostEqual(c2["map75"], 0.0, places=4)
        self.assertAlmostEqual(c2["map50_95"], 0.30, places=4)

    def test_spec_validation(self) -> None:
        with self.assertRaises(ValueError):
            evaluation.EvaluationSpec.from_json({})
        spec = evaluation.EvaluationSpec.from_json(
            {
                "weights": "/tmp/best.pt",
                "manifest_path": "/tmp/m.json",
                "output_path": "/tmp/out.json",
                "classes": [{"name": "a"}],
                "imgsz": [640, 640],
                "batch": 8,
            }
        )
        self.assertEqual(spec.imgsz, (640, 640))
        self.assertEqual(spec.batch, 8)


class _FakeEvalServer:
    """本地假平台 HTTP 服务，用于验证 ModelEvaluationTask 全链路交互。"""

    def __init__(
        self, weights_bytes: bytes, image_bytes: bytes, *, abandon_on_progress: bool = False
    ) -> None:
        self.weights_bytes = weights_bytes
        self.image_bytes = image_bytes
        self.abandon_on_progress = abandon_on_progress
        self.progress_calls: list[dict[str, Any]] = []
        self.results_payload: dict[str, Any] | None = None
        self.complete_payload: dict[str, Any] | None = None
        self.fail_payload: dict[str, Any] | None = None
        self.image_downloads = 0
        self.server = HTTPServer(("127.0.0.1", 0), self._make_handler())
        self.port = self.server.server_address[1]
        self._thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self._thread.start()

    def _make_handler(self) -> type[BaseHTTPRequestHandler]:
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format: str, *args: Any) -> None:
                return

            def _send_json(self, code: int, obj: dict[str, Any]) -> None:
                raw = json.dumps(obj).encode("utf-8")
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self) -> None:
                if self.path == "/eval/1/model":
                    self.send_response(200)
                    self.send_header("Content-Length", str(len(outer.weights_bytes)))
                    self.end_headers()
                    self.wfile.write(outer.weights_bytes)
                elif self.path.startswith("/eval/1/images?"):
                    self._send_json(
                        200,
                        {
                            "items": [
                                {
                                    "id": 101,
                                    "position": 1,
                                    "path": "images/val/a.jpg",
                                    "width": 100,
                                    "height": 100,
                                    "gt_objects": [
                                        {
                                            "name": "scratch",
                                            "xmin": 10,
                                            "ymin": 10,
                                            "xmax": 60,
                                            "ymax": 60,
                                        }
                                    ],
                                    "file_url": "/eval/1/images/101/file",
                                }
                            ],
                            "has_more": False,
                            "next_id": 101,
                        },
                    )
                elif self.path == "/eval/1/images/101/file":
                    outer.image_downloads += 1
                    self.send_response(200)
                    self.send_header("Content-Length", str(len(outer.image_bytes)))
                    self.end_headers()
                    self.wfile.write(outer.image_bytes)
                else:
                    self._send_json(404, {"error": {"code": "not_found", "message": "not found"}})

            def do_POST(self) -> None:
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length)
                if self.headers.get("Content-Encoding", "").lower() == "gzip":
                    raw = gzip.decompress(raw)
                data = json.loads(raw.decode("utf-8")) if raw else {}
                if self.path == "/eval/1/progress":
                    outer.progress_calls.append(data)
                    if outer.abandon_on_progress:
                        self._send_json(409, {"error": {"code": "conflict", "message": "已取消"}})
                        return
                    self._send_json(200, {"lease_seconds": 120})
                elif self.path == "/eval/1/results":
                    outer.results_payload = data
                    self._send_json(200, {"ok": True})
                elif self.path == "/eval/1/complete":
                    outer.complete_payload = data
                    self._send_json(200, {"id": 1, "status": "succeeded"})
                elif self.path == "/eval/1/fail":
                    outer.fail_payload = data
                    self._send_json(200, {"id": 1, "status": "failed"})
                else:
                    self._send_json(404, {"error": {"code": "not_found", "message": "not found"}})

        return Handler

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()


class TestModelEvaluationTask(unittest.TestCase):
    def test_end_to_end_with_fake_worker_and_shared_local(self) -> None:
        weights = b"fake-pt-weights-bytes"
        sha = hashlib.sha256(weights).hexdigest()
        srv = _FakeEvalServer(weights, b"\xff\xd8\xff\xd9")
        try:
            with TemporaryDirectory() as tmp:
                tmp_path = Path(tmp)
                state_dir = tmp_path / "state"
                data_dir = tmp_path / "data"
                local_img = data_dir / "exports" / "dv-1" / "images" / "val" / "a.jpg"
                local_img.parent.mkdir(parents=True, exist_ok=True)
                local_img.write_bytes(b"\xff\xd8\xff\xd9")

                # 模拟评估子进程脚本
                worker_script = tmp_path / "fake_worker.py"
                worker_script.write_text(
                    """
import argparse, json, pathlib
p = argparse.ArgumentParser()
p.add_argument("--spec", required=True)
args = p.parse_args()
spec = json.loads(pathlib.Path(args.spec).read_text())
manifest = json.loads(pathlib.Path(spec["manifest_path"]).read_text())
assert len(manifest) == 1 and manifest[0]["local_path"].endswith("a.jpg")
print(json.dumps({"event": "progress", "stage": "inferring", "done": 1, "total": 1, "eta_seconds": 0}), flush=True)
out = {
    "curves": {
        "map50": 1.0, "map75": 1.0, "map50_95": 0.9,
        "best_f1": 1.0, "best_f1_conf": 0.5,
        "pr": [], "f1": [], "precision": [], "recall": [],
    },
    "predictions": [{
        "sample_id": 101, "width": 100, "height": 100, "inference_error": "",
        "predictions": [{
            "name": "scratch", "xmin": 10, "ymin": 10,
            "xmax": 60, "ymax": 60, "conf": 0.92,
        }],
    }],
    "meta": {"device": "cpu"},
}
pathlib.Path(spec["output_path"]).write_text(json.dumps(out))
print(json.dumps({"event": "done", "ok": True, "meta": out["meta"]}), flush=True)
""",
                    encoding="utf-8",
                )

                task_payload = {
                    "kind": evaluation.KIND,
                    "id": 1,
                    "source_type": "data_version",
                    "data_version": {"dir_name": "dv-1", "shared_local": True},
                    "model": {
                        "size_bytes": len(weights),
                        "sha256": sha,
                        "task": "detect",
                        "yolo_version": "yolo11",
                    },
                    "options": {
                        "conf": 0.25,
                        "iou": 0.5,
                        "nms_iou": 0.7,
                        "batch": 4,
                        "store_min_conf": 0.05,
                    },
                    "classes": [{"name": "scratch", "color": "#2563eb"}],
                    "total_images": 1,
                    "model_url": "/eval/1/model",
                    "images_url": "/eval/1/images",
                    "progress_url": "/eval/1/progress",
                    "results_url": "/eval/1/results",
                    "complete_url": "/eval/1/complete",
                    "fail_url": "/eval/1/fail",
                }
                client = Client(f"http://127.0.0.1:{srv.port}")
                runner = evaluation.ModelEvaluationTask(
                    task_payload,
                    client=client,
                    node_key="nk-1",
                    state_dir=state_dir,
                    node_name="node-1",
                    data_dir=data_dir,
                    command=[sys.executable, str(worker_script)],
                    progress_interval=0.0,
                )
                status = runner.run()
                self.assertEqual(status, "succeeded")
                # 验证零拷贝：未通过 HTTP 下载图片
                self.assertEqual(srv.image_downloads, 0)
                self.assertIsNotNone(srv.results_payload)
                assert srv.results_payload is not None
                self.assertEqual(srv.results_payload["curves"]["map50"], 1.0)
                self.assertEqual(len(srv.results_payload["predictions"]), 1)
                self.assertIsNotNone(srv.complete_payload)
        finally:
            srv.close()

    def test_abandoned_on_409(self) -> None:
        weights = b"fake-pt"
        srv = _FakeEvalServer(weights, b"img", abandon_on_progress=True)
        try:
            with TemporaryDirectory() as tmp:
                client = Client(f"http://127.0.0.1:{srv.port}")
                runner = evaluation.ModelEvaluationTask(
                    {
                        "kind": evaluation.KIND,
                        "id": 1,
                        "model": {},
                        "options": {},
                        "total_images": 1,
                        "model_url": "/eval/1/model",
                        "images_url": "/eval/1/images",
                        "progress_url": "/eval/1/progress",
                        "results_url": "/eval/1/results",
                        "complete_url": "/eval/1/complete",
                        "fail_url": "/eval/1/fail",
                    },
                    client=client,
                    node_key="nk-1",
                    state_dir=Path(tmp),
                    node_name="node-1",
                )
                self.assertEqual(runner.run(), "abandoned")
                self.assertIsNone(srv.fail_payload)
        finally:
            srv.close()

    def test_registered_in_task_worker(self) -> None:
        self.assertIn(evaluation.KIND, tasks.REQUIRED_MODULES)
        self.assertIn(f"{evaluation.WORK_DIR_PREFIX}*", tasks.WORK_DIR_PATTERNS)


if __name__ == "__main__":
    unittest.main()
