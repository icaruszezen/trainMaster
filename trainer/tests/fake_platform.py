"""假的平台服务端（本地 http.server）：节点接口 /api/v1/compute/agent/register、/heartbeat，
以及任务接口：领取，模型转换（下载输入、进度、上传结果、完成、失败）与审图识别（下载模型、图片列表、
下载图片、进度、上报结果、完成、失败）。"""

from __future__ import annotations

import hashlib
import http.server
import json
import re
import threading
import time
import urllib.parse
from collections.abc import Callable
from typing import Any

REGISTER = "/api/v1/compute/agent/register"
HEARTBEAT = "/api/v1/compute/agent/heartbeat"
CLAIM = "/api/v1/compute/agent/tasks/claim"
EXPORTS = "/api/v1/compute/agent/model-exports/"
REVIEWS = "/api/v1/compute/agent/review-inferences/"
LEASE_SECONDS = 60
MAX_ATTEMPTS = 3
STAGES = ("downloading", "converting", "uploading")
MAX_OUTPUT_BYTES = 4 << 30
# hooks 返回 DROP 时不响应、直接断开连接：真实的服务端在读取上传内容之前拒绝较大的上传时，
# 正在上传的节点看到的就是连接被重置（读不到响应）。
DROP = "drop"
_EXPORT_PATH = re.compile(
    r"/api/v1/compute/agent/model-exports/(\d+)/(input|progress|output|complete|fail)"
)
_REVIEW_PATH = re.compile(
    r"/api/v1/compute/agent/review-inferences/(\d+)/"
    r"(model|images|images/(\d+)/file|progress|results|complete|fail)"
)
REVIEW_STAGES = ("downloading", "inferring")
MAX_RESULTS_BODY = 1 << 20
MAX_RESULTS = 500

# 一个合法的 model_export 任务中模型与选项的默认值（与设计文档中的示例相同）。
DEFAULT_MODEL = {
    "id": 5,
    "project_id": 2,
    "version": 3,
    "yolo_version": "yolo26",
    "task": "detect",
    "file_name": "best.pt",
    "info": {"task": "detect", "ultralytics_version": "8.4.198", "image_size": 640},
}
DEFAULT_OPTIONS = {"imgsz": [640, 640], "dynamic": False, "postprocess": "end2end", "opset": None}
# 审图识别任务的模型与选项的默认值（与实现规格中的示例相同）。
DEFAULT_REVIEW_MODEL = {
    "id": 3,
    "project_id": 1,
    "version": 3,
    "yolo_version": "yolo11",
    "task": "detect",
    "file_name": "best.pt",
    "class_names": ["person", "bicycle", "car"],
}
DEFAULT_REVIEW_OPTIONS = {"conf": 0.25, "iou": 0.7, "imgsz": [640, 640], "max_det": 300}


def _error(status: int, code: str, message: str) -> tuple[int, dict[str, Any]]:
    return status, {"error": {"code": code, "message": message}}


class FakePlatform:
    """行为与设计文档中的服务端一致：按 node_key 注册、未注册的心跳返回 404 unknown_node、可选令牌。

    mode：up 正常；503 返回服务端错误；drop 读完请求后直接断开连接（模拟网络故障）；
    422 返回校验失败（这三种对所有接口生效）。tasks_supported=False 模拟没有任务接口的旧版平台
    （任务接口返回 404 not_found）。hooks[接口名] 可以替换某个任务接口的响应（见 task_request；审图识别
    的接口名带前缀 review:，如 review:results）。
    free_bytes：平台磁盘的剩余空间（None 为不限），上传结果与带 size_bytes 的上传阶段进度上报
    与真实的服务端一样检查它（507）。
    """

    def __init__(self, token: str | None = None, interval: float = 10) -> None:
        self.token = token
        self.interval: Any = interval
        self.mode = "up"
        self.clear_probe_on_environment = True
        # 注册时没有带检测结果（节点刚启动、检测还在进行）：保留原来的结果并标记为“正在检测”。
        self.request_probe_on_register = True
        self.nodes: dict[str, dict[str, Any]] = {}
        self.requests: list[dict[str, Any]] = []
        self.lock = threading.Lock()
        self._next_id = 1

        # 任务（model_export）
        self.tasks_supported = True
        self.max_claim_wait = 30.0  # 测试可以缩短长轮询
        self.exports: dict[int, dict[str, Any]] = {}
        self.claims: list[dict[str, Any]] = []
        # hooks["claim" | "input" | "progress" | "output" | "complete" | "fail"](export 或 None, 请求)
        # 返回 (status, payload) 时替换响应，返回 None 时照常处理；payload 为 bytes 时原样发送；
        # 返回 DROP 时断开连接。
        self.hooks: dict[str, Callable[[dict[str, Any] | None, dict[str, Any]], Any]] = {}
        self.free_bytes: int | None = None
        # 不需要令牌的下载（GET 路径 → (内容, Content-Type)），如 Windows 的安装脚本与安装包
        self.files: dict[str, tuple[bytes, str]] = {}
        # 审图识别任务（review_inference）
        self.reviews: dict[int, dict[str, Any]] = {}
        self._next_review = 12
        self.task_changed = threading.Condition(self.lock)
        self.closed = False
        self._wakeups = 0
        self._next_export = 17
        platform = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args: Any) -> None:
                pass

            def _send(self, status: int, payload: Any, content_type: str | None = None) -> None:
                body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", content_type or "application/json; charset=utf-8")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def _handle(self, method: str) -> None:
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                body: Any = None
                if method == "POST":
                    try:
                        body = json.loads(raw)
                    except ValueError:
                        body = None
                request = {
                    "method": method,
                    "path": self.path,
                    "body": body,
                    "headers": dict(self.headers),
                    "raw": raw if method == "PUT" else None,
                }
                with platform.lock:
                    platform.requests.append(request)
                    mode = platform.mode
                if mode == "drop":
                    self.close_connection = True
                    self.connection.shutdown(2)
                    return
                if mode == "503":
                    self._send(503, {"error": {"code": "unavailable", "message": "服务暂不可用"}})
                    return
                if method == "GET" and self.path in platform.files:
                    data, content_type = platform.files[self.path]
                    self._send(200, data, content_type)
                    return
                if mode == "422":
                    self._send(
                        422, {"error": {"code": "validation_failed", "message": "节点名称不合法"}}
                    )
                    return
                expected = f"Bearer {platform.token}"
                if platform.token is not None and self.headers.get("Authorization") != expected:
                    self._send(
                        401, {"error": {"code": "unauthorized", "message": "节点令牌不正确"}}
                    )
                    return
                if method == "POST" and self.path in (REGISTER, HEARTBEAT):
                    if not isinstance(body, dict):
                        self._send(
                            400, {"error": {"code": "bad_request", "message": "请求格式不正确"}}
                        )
                    elif self.path == REGISTER:
                        self._send(200, platform.register(body))
                    else:
                        self._send(*platform.heartbeat(body))
                    return
                if platform.tasks_supported and (
                    self.path == CLAIM
                    or self.path.startswith(EXPORTS)
                    or self.path.startswith(REVIEWS)
                ):
                    response = platform.task_request(method, request)
                    if response == DROP:
                        self.close_connection = True
                        self.connection.shutdown(2)
                        return
                    status, payload = response
                    content_type = (
                        "application/octet-stream" if isinstance(payload, bytes) else None
                    )
                    self._send(status, payload, content_type)
                    return
                self._send(404, {"error": {"code": "not_found", "message": "接口不存在"}})

            def do_POST(self) -> None:  # noqa: N802 - http.server 的接口
                self._handle("POST")

            def do_GET(self) -> None:  # noqa: N802
                self._handle("GET")

            def do_PUT(self) -> None:  # noqa: N802
                self._handle("PUT")

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(
            target=self.server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True
        )
        self.thread.start()

    @property
    def url(self) -> str:
        host, port = self.server.server_address[:2]
        return f"http://{host}:{port}"

    def close(self) -> None:
        with self.lock:
            self.closed = True  # 正在长轮询的领取立即返回
            self.task_changed.notify_all()
        self.server.shutdown()
        self.server.server_close()

    def release_claims(self) -> None:
        """让正在长轮询的领取立即返回空结果（如服务端关闭时）。"""
        with self.lock:
            self._wakeups += 1
            self.task_changed.notify_all()

    def register(self, body: dict[str, Any]) -> dict[str, Any]:
        with self.lock:
            key = body["node_key"]
            node = self.nodes.get(key)
            if node is None:
                node = {"node_id": self._next_id, "environment": None, "probe_requested": False}
                self._next_id += 1
                self.nodes[key] = node
            node["registrations"] = node.get("registrations", 0) + 1
            node["hardware"] = body.get("hardware")
            node["name"] = body.get("name")
            if body.get("environment") is not None:
                node["environment"] = body["environment"]
            elif self.request_probe_on_register:
                node["probe_requested"] = True
            if body.get("metrics") is not None:
                node["metrics"] = body["metrics"]
            return {
                "node_id": node["node_id"],
                "heartbeat_interval_seconds": self.interval,
                "server_time": "2026-11-02T07:30:00Z",
            }

    def heartbeat(self, body: dict[str, Any]) -> tuple[int, dict[str, Any]]:
        with self.lock:
            node = self.nodes.get(body.get("node_key"))
            if node is None:
                return 404, {"error": {"code": "unknown_node", "message": "节点未注册，请重新注册"}}
            node["heartbeats"] = node.get("heartbeats", 0) + 1
            if body.get("metrics") is not None:
                node["metrics"] = body["metrics"]
            if body.get("hardware") is not None:
                node["hardware"] = body["hardware"]
                node["hardware_updates"] = node.get("hardware_updates", 0) + 1
            if body.get("environment") is not None:
                node["environment"] = body["environment"]
                node["environment_updates"] = node.get("environment_updates", 0) + 1
                if self.clear_probe_on_environment:
                    node["probe_requested"] = False
            return 200, {
                "node_id": node["node_id"],
                "heartbeat_interval_seconds": self.interval,
                "probe_requested": node["probe_requested"],
            }

    def node(self) -> dict[str, Any] | None:
        with self.lock:
            return next(iter(self.nodes.values()), None)

    def count(self, path: str) -> int:
        with self.lock:
            return sum(1 for r in self.requests if r["path"] == path)

    # ---- 任务 ----

    def add_export(
        self,
        data: bytes,
        *,
        model: dict[str, Any] | None = None,
        options: dict[str, Any] | None = None,
        sha256: str | None = None,
        size_bytes: int | None = None,
        serve: bytes | None = None,
        task: dict[str, Any] | None = None,
    ) -> int:
        """加入一个排队中的 model_export 任务，返回其 ID。serve：input 接口实际返回的内容（模拟损坏）；
        task：覆盖下发的任务中的字段（模拟不合法的任务）。"""
        with self.lock:
            export_id = self._next_export
            self._next_export += 1
            self.exports[export_id] = {
                "id": export_id,
                "status": "queued",
                "stage": "",
                "attempts": 0,
                "node_key": None,
                "input": data if serve is None else serve,
                "model": {
                    **DEFAULT_MODEL,
                    **(model or {}),
                    "size_bytes": len(data) if size_bytes is None else size_bytes,
                    "sha256": sha256 or hashlib.sha256(data).hexdigest(),
                },
                "options": {**DEFAULT_OPTIONS, **(options or {})},
                "task_overrides": task or {},
                "progress": [],
                "output": None,
                "output_sha256": None,
                "result": None,
                "fails": [],
            }
            self.task_changed.notify_all()
            return export_id

    def delete_export(self, export_id: int) -> None:
        with self.lock:
            self.exports.pop(export_id, None)

    def export(self, export_id: int) -> dict[str, Any] | None:
        with self.lock:
            return self.exports.get(export_id)

    def _task_json(self, exp: dict[str, Any]) -> dict[str, Any]:
        base = f"{EXPORTS}{exp['id']}"
        task = {
            "kind": "model_export",
            "id": exp["id"],
            "lease_seconds": LEASE_SECONDS,
            "model": dict(exp["model"]),
            "options": dict(exp["options"]),
            "input_url": f"{base}/input",
            "progress_url": f"{base}/progress",
            "output_url": f"{base}/output",
            "complete_url": f"{base}/complete",
            "fail_url": f"{base}/fail",
        }
        task.update(exp["task_overrides"])
        return task

    def _view(self, exp: dict[str, Any]) -> dict[str, Any]:
        return {
            "id": exp["id"],
            "status": exp["status"],
            "stage": exp["stage"],
            "attempts": exp["attempts"],
            "error": exp.get("error", ""),
        }

    def task_request(self, method: str, request: dict[str, Any]) -> tuple[int, Any] | str:
        """返回 (status, payload)，或 DROP（断开连接，只有 hooks 会返回）。"""
        path = request["path"]
        if path == CLAIM and method == "POST":
            return self._claim(request)
        if path.startswith(REVIEWS):
            return self.review_request(method, request)
        m = _EXPORT_PATH.fullmatch(path)
        if m is None:
            return _error(404, "not_found", "接口不存在")
        export_id, action = int(m.group(1)), m.group(2)
        allowed = {"input": "GET", "output": "PUT"}.get(action, "POST")
        if method != allowed:
            return _error(405, "method_not_allowed", "不支持该请求方法")
        with self.lock:
            exp = self.exports.get(export_id)
            hook = self.hooks.get(action)
        if hook is not None:
            override = hook(exp, request)
            if override is not None:
                return override
        key = request["headers"].get("X-Node-Key")
        with self.lock:
            if key not in self.nodes:
                return _error(404, "unknown_node", "节点未注册，请重新注册")
            exp = self.exports.get(export_id)
            if exp is None:
                return _error(404, "not_found", "转换任务不存在")
            if exp["status"] != "running" or exp["node_key"] != key:
                return _error(409, "conflict", "转换任务不属于该节点或已经结束")
            return getattr(self, f"_{action}")(exp, request)

    def _claim(self, request: dict[str, Any]) -> tuple[int, Any]:
        body = request["body"]
        if not isinstance(body, dict):
            return _error(400, "bad_request", "请求格式不正确")
        hook = self.hooks.get("claim")
        if hook is not None:
            override = hook(None, request)
            if override is not None:
                return override
        key = body.get("node_key")
        wait = body.get("wait_seconds", 0)
        if not isinstance(wait, (int, float)) or not 0 <= wait <= 30:
            return _error(422, "validation_failed", "wait_seconds 必须在 0–30 之间")
        deadline = time.monotonic() + min(float(wait), self.max_claim_wait)
        with self.task_changed:
            if key not in self.nodes:
                return _error(404, "unknown_node", "节点未注册，请重新注册")
            self.claims.append(body)
            wakeups = self._wakeups
            while True:
                if "model_export" in (body.get("kinds") or []):
                    for exp in self.exports.values():
                        if exp["status"] == "queued":
                            exp.update(status="running", node_key=key, stage="downloading")
                            exp["attempts"] += 1
                            return 200, {"task": self._task_json(exp)}
                if "review_inference" in (body.get("kinds") or []):
                    for review in self.reviews.values():
                        if review["status"] == "queued":
                            review.update(status="running", node_key=key, stage="downloading")
                            review["attempts"] += 1
                            return 200, {"task": self._review_task_json(review)}
                remaining = deadline - time.monotonic()
                if remaining <= 0 or self.closed or self._wakeups != wakeups:
                    return 200, {"task": None}
                self.task_changed.wait(remaining)

    def _input(self, exp: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        return 200, exp["input"]

    def _progress(self, exp: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        body = request["body"]
        stage = body.get("stage") if isinstance(body, dict) else None
        if stage not in STAGES:
            return _error(422, "validation_failed", "stage 不合法")
        size = body.get("size_bytes")
        if size is not None and (not isinstance(size, int) or size < 0):
            return _error(422, "validation_failed", "size_bytes 不能为负数")
        exp["stage"] = stage
        exp["progress"].append(stage)
        if stage == "uploading" and size:  # 预检：与上传时相同的检查
            if size > MAX_OUTPUT_BYTES:
                return _error(413, "payload_too_large", "转换结果不能超过 4 GiB")
            refused = self._check_free_space(size)
            if refused is not None:
                return refused
        return 200, {"lease_seconds": LEASE_SECONDS}

    def _check_free_space(self, size: int) -> tuple[int, Any] | None:
        if self.free_bytes is not None and size > self.free_bytes:
            return _error(
                507,
                "insufficient_storage",
                f"磁盘剩余空间不足：可用 {self.free_bytes} B，至少需要 {size} B",
            )
        return None

    def _output(self, exp: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        data = request["raw"] or b""
        length = request["headers"].get("Content-Length")
        sha = request["headers"].get("X-Content-Sha256") or ""
        if length is None or int(length) != len(data):
            return _error(422, "validation_failed", "上传的字节数与 Content-Length 不一致")
        if sha.lower() != hashlib.sha256(data).hexdigest():
            return _error(422, "validation_failed", "上传的文件与 X-Content-Sha256 不一致")
        refused = self._check_free_space(len(data))
        if refused is not None:
            return refused
        exp["output"] = data
        exp["output_sha256"] = sha.lower()
        return 200, {"received_bytes": len(data)}

    def _complete(self, exp: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        if exp["output"] is None:
            return _error(409, "conflict", "还没有上传转换结果")
        body = request["body"]
        exp.update(status="succeeded", stage="", result=(body or {}).get("result"))
        return 200, self._view(exp)

    def _fail(self, exp: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        body = request["body"] if isinstance(request["body"], dict) else {}
        exp["fails"].append(body)
        exp["error"] = body.get("error", "")
        if body.get("retryable") is True and exp["attempts"] < MAX_ATTEMPTS:
            exp.update(status="queued", stage="", node_key=None)
            self.task_changed.notify_all()
        else:
            exp.update(status="failed", stage="")
        return 200, self._view(exp)

    # ---- 审图识别（review_inference）----

    def add_review(
        self,
        model_data: bytes,
        images: dict[int, bytes | None],
        *,
        model: dict[str, Any] | None = None,
        options: dict[str, Any] | None = None,
        sha256: str | None = None,
        inferred: tuple[int, ...] = (),
        task: dict[str, Any] | None = None,
        file_names: dict[int, str] | None = None,
    ) -> int:
        """加入一个排队中的审图识别任务，返回其 ID。images：审图图片 ID → 图片文件的内容（None 表示
        数据集中的文件已不存在，下载时 404）；inferred：已经识别过的图片（继续上次的识别）；task：覆盖
        下发的任务中的字段（模拟不合法的任务）。"""
        with self.lock:
            review_id = self._next_review
            self._next_review += 1
            names = file_names or {}
            self.reviews[review_id] = {
                "id": review_id,
                "status": "queued",
                "stage": "",
                "attempts": 0,
                "node_key": None,
                "model_data": model_data,
                "model": {
                    **DEFAULT_REVIEW_MODEL,
                    **(model or {}),
                    "size_bytes": len(model_data),
                    "sha256": sha256 or hashlib.sha256(model_data).hexdigest(),
                },
                "options": {**DEFAULT_REVIEW_OPTIONS, **(options or {})},
                "task_overrides": task or {},
                "images": {
                    image_id: {
                        "data": data,
                        "file_name": names.get(image_id, f"img-{image_id}.jpg"),
                        "inferred": image_id in inferred,
                        "result": None,
                    }
                    for image_id, data in sorted(images.items())
                },
                "progress": [],
                "results_posts": [],
                "result": None,
                "fails": [],
            }
            self.task_changed.notify_all()
            return review_id

    def review(self, review_id: int) -> dict[str, Any] | None:
        with self.lock:
            return self.reviews.get(review_id)

    def delete_review(self, review_id: int) -> None:
        with self.lock:
            self.reviews.pop(review_id, None)

    @staticmethod
    def _review_remaining(review: dict[str, Any]) -> int:
        return sum(1 for img in review["images"].values() if not img["inferred"])

    def _review_task_json(self, review: dict[str, Any]) -> dict[str, Any]:
        base = f"{REVIEWS}{review['id']}"
        task = {
            "kind": "review_inference",
            "id": review["id"],
            "lease_seconds": LEASE_SECONDS,
            "model": dict(review["model"]),
            "options": dict(review["options"]),
            "image_count": len(review["images"]),
            "remaining": self._review_remaining(review),
            "model_url": f"{base}/model",
            "images_url": f"{base}/images",
            "progress_url": f"{base}/progress",
            "results_url": f"{base}/results",
            "complete_url": f"{base}/complete",
            "fail_url": f"{base}/fail",
        }
        task.update(review["task_overrides"])
        return task

    def _review_view(self, review: dict[str, Any]) -> dict[str, Any]:
        return {
            "id": review["id"],
            "status": review["status"],
            "stage": review["stage"],
            "attempts": review["attempts"],
            "error": review.get("error", ""),
        }

    def review_request(self, method: str, request: dict[str, Any]) -> tuple[int, Any] | str:
        """审图识别的接口（规则同实现规格第 5.2 节）。返回 (status, payload)，或 DROP。"""
        parts = urllib.parse.urlsplit(request["path"])
        m = _REVIEW_PATH.fullmatch(parts.path)
        if m is None:
            return _error(404, "not_found", "接口不存在")
        review_id, action = int(m.group(1)), m.group(2)
        image_id = int(m.group(3)) if m.group(3) else None
        if image_id is not None:
            action = "file"
        allowed = "GET" if action in ("model", "images", "file") else "POST"
        if method != allowed:
            return _error(405, "method_not_allowed", "不支持该请求方法")
        with self.lock:
            review = self.reviews.get(review_id)
            hook = self.hooks.get(f"review:{action}")
        if hook is not None:
            override = hook(review, request)
            if override is not None:
                return override
        key = request["headers"].get("X-Node-Key")
        with self.lock:
            if key not in self.nodes:
                return _error(404, "unknown_node", "节点未注册，请重新注册")
            review = self.reviews.get(review_id)
            if review is None:
                return _error(404, "not_found", "审图任务不存在")
            if review["status"] != "running" or review["node_key"] != key:
                return _error(409, "conflict", "审图任务不属于该节点或已经结束")
            if action == "file":
                return self._review_file(review, image_id)
            if action == "images":
                return self._review_images(review, dict(urllib.parse.parse_qsl(parts.query)))
            return getattr(self, f"_review_{action}")(review, request)

    def _review_model(self, review: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        return 200, review["model_data"]

    def _review_images(self, review: dict[str, Any], query: dict[str, str]) -> tuple[int, Any]:
        try:
            after = int(query.get("after", "0"))
            limit = int(query.get("limit", "200"))
        except ValueError:
            return _error(400, "bad_request", "after / limit 必须是整数")
        if not 1 <= limit <= 500:
            return _error(422, "validation_failed", "limit 必须在 1–500 之间")
        base = f"{REVIEWS}{review['id']}"
        items = [
            {
                "id": image_id,
                "file_name": img["file_name"],
                "width": None,
                "height": None,
                "size_bytes": len(img["data"] or b""),
                "url": f"{base}/images/{image_id}/file",
            }
            for image_id, img in sorted(review["images"].items())
            if not img["inferred"] and image_id > after
        ][:limit]
        return 200, {"items": items, "remaining": self._review_remaining(review)}

    def _review_file(self, review: dict[str, Any], image_id: int | None) -> tuple[int, Any]:
        img = review["images"].get(image_id)
        if img is None:
            return _error(404, "not_found", "审图图片不存在")
        if img["data"] is None:
            return _error(404, "not_found", f"数据集中的图片文件已不存在：{img['file_name']}")
        return 200, img["data"]

    def _review_progress(self, review: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        body = request["body"]
        stage = body.get("stage") if isinstance(body, dict) else None
        if stage not in REVIEW_STAGES:
            return _error(422, "validation_failed", "stage 不合法")
        review["stage"] = stage
        review["progress"].append((stage, body.get("message")))
        return 200, {"lease_seconds": LEASE_SECONDS}

    def _review_results(self, review: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        body = request["body"]
        if int(request["headers"].get("Content-Length") or 0) > MAX_RESULTS_BODY:
            return _error(413, "payload_too_large", "请求体不能超过 1 MiB")
        results = body.get("results") if isinstance(body, dict) else None
        if not isinstance(results, list) or len(results) > MAX_RESULTS:
            return _error(422, "validation_failed", "results 必须是最多 500 条的数组")
        review["results_posts"].append(len(results))
        accepted = 0
        for result in results:
            img = review["images"].get(result.get("id"))
            if img is None or img["inferred"]:
                continue  # 不在这个审图任务中、或已经识别过：忽略（重复上报没有关系）
            img.update(inferred=True, result=result)
            accepted += 1
        return 200, {
            "lease_seconds": LEASE_SECONDS,
            "accepted": accepted,
            "remaining": self._review_remaining(review),
        }

    def _review_complete(self, review: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        remaining = self._review_remaining(review)
        if remaining:
            return _error(409, "conflict", f"还有 {remaining} 张图片没有识别")
        body = request["body"]
        review.update(status="ready", stage="", result=(body or {}).get("result"))
        return 200, self._review_view(review)

    def _review_fail(self, review: dict[str, Any], request: dict[str, Any]) -> tuple[int, Any]:
        body = request["body"] if isinstance(request["body"], dict) else {}
        review["fails"].append(body)
        review["error"] = body.get("error", "")
        if body.get("retryable") is True and review["attempts"] < MAX_ATTEMPTS:
            review.update(status="queued", stage="", node_key=None)
            self.task_changed.notify_all()
        else:
            review.update(status="failed", stage="")
        return 200, self._review_view(review)


def wait_until(predicate: Callable[[], Any], timeout: float = 10.0, interval: float = 0.01) -> Any:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(interval)
    raise AssertionError("condition not met within timeout")
