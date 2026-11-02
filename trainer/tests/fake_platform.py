"""假的平台服务端（本地 http.server），实现 /api/v1/compute/agent/register 与 /heartbeat。"""

from __future__ import annotations

import http.server
import json
import threading
import time
from collections.abc import Callable
from typing import Any

REGISTER = "/api/v1/compute/agent/register"
HEARTBEAT = "/api/v1/compute/agent/heartbeat"


class FakePlatform:
    """行为与设计文档中的服务端一致：按 node_key 注册、未注册的心跳返回 404 unknown_node、可选令牌。

    mode：up 正常；503 返回服务端错误；drop 读完请求后直接断开连接（模拟网络故障）；
    422 返回校验失败。
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
        platform = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args: Any) -> None:
                pass

            def _send(self, status: int, payload: Any) -> None:
                body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json; charset=utf-8")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self) -> None:  # noqa: N802 - http.server 的接口
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length)
                try:
                    body = json.loads(raw)
                except ValueError:
                    body = None
                with platform.lock:
                    platform.requests.append(
                        {"path": self.path, "body": body, "headers": dict(self.headers)}
                    )
                    mode = platform.mode
                if mode == "drop":
                    self.close_connection = True
                    self.connection.shutdown(2)
                    return
                if mode == "503":
                    self._send(503, {"error": {"code": "unavailable", "message": "服务暂不可用"}})
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
                if not isinstance(body, dict):
                    self._send(400, {"error": {"code": "bad_request", "message": "请求格式不正确"}})
                    return
                if self.path == REGISTER:
                    self._send(200, platform.register(body))
                elif self.path == HEARTBEAT:
                    status, payload = platform.heartbeat(body)
                    self._send(status, payload)
                else:
                    self._send(404, {"error": {"code": "not_found", "message": "接口不存在"}})

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
        self.server.shutdown()
        self.server.server_close()

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


def wait_until(predicate: Callable[[], Any], timeout: float = 10.0, interval: float = 0.01) -> Any:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(interval)
    raise AssertionError("condition not met within timeout")
