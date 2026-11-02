"""client.py：对本地 http.server 发送请求，覆盖成功、错误信封、非 JSON 响应与网络错误。"""

from __future__ import annotations

import http.server
import json
import socket
import threading
from typing import Any

import pytest
from fake_platform import HEARTBEAT, REGISTER, FakePlatform

from trainmaster_trainer import __version__
from trainmaster_trainer.client import ApiError, Client, TransportError, json_safe


@pytest.fixture
def platform():
    p = FakePlatform(token="secret-token-0123456789")
    yield p
    p.close()


def test_register_sends_json_with_token(platform):
    client = Client(platform.url + "/", "secret-token-0123456789")
    resp = client.register({"node_key": "k1", "name": "节点一", "hardware": {"gpus": []}})
    assert resp["node_id"] == 1
    assert resp["heartbeat_interval_seconds"] == 10
    req = platform.requests[-1]
    assert req["path"] == REGISTER
    assert req["body"]["name"] == "节点一"
    assert req["headers"]["Authorization"] == "Bearer secret-token-0123456789"
    assert req["headers"]["Content-Type"] == "application/json; charset=utf-8"
    assert req["headers"]["User-Agent"] == f"trainmaster-trainer/{__version__}"


def test_no_authorization_header_without_token(platform):
    platform.token = None
    Client(platform.url).register({"node_key": "k1", "hardware": {}})
    assert "Authorization" not in platform.requests[-1]["headers"]


def test_error_envelope_is_parsed(platform):
    client = Client(platform.url, "wrong-token-000000000")
    with pytest.raises(ApiError) as info:
        client.heartbeat({"node_key": "k1"})
    assert info.value.status == 401
    assert info.value.code == "unauthorized"
    assert info.value.message == "节点令牌不正确"


def test_unknown_node(platform):
    with pytest.raises(ApiError) as info:
        Client(platform.url, "secret-token-0123456789").heartbeat({"node_key": "nope"})
    assert (info.value.status, info.value.code) == (404, "unknown_node")
    assert platform.requests[-1]["path"] == HEARTBEAT


def test_nan_metrics_are_sent_as_null(platform):
    client = Client(platform.url, "secret-token-0123456789")
    client.register({"node_key": "k1", "hardware": {}, "metrics": {"cpu_percent": float("nan")}})
    assert platform.requests[-1]["body"]["metrics"]["cpu_percent"] is None


def test_json_safe():
    assert json_safe({"a": [1.5, float("inf"), (float("-inf"), True)], "b": None}) == {
        "a": [1.5, None, [None, True]],
        "b": None,
    }


class _RawServer:
    """返回固定响应（状态码、正文）的服务端，用于非 JSON 与异常响应。"""

    def __init__(self, status: int, body: bytes, content_type: str = "text/plain") -> None:
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args: Any) -> None:
                pass

            def do_POST(self) -> None:  # noqa: N802
                self.rfile.read(int(self.headers.get("Content-Length") or 0))
                self.send_response(outer.status)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(outer.body)))
                self.end_headers()
                self.wfile.write(outer.body)

        self.status, self.body = status, body
        self.server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(
            target=self.server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True
        ).start()
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()


@pytest.mark.parametrize(
    ("status", "body", "code", "message"),
    [
        (502, b"<html>Bad Gateway</html>", None, "<html>Bad Gateway</html>"),
        (500, b"", None, "Internal Server Error"),
        (413, json.dumps({"error": {"code": "payload_too_large", "message": "请求体过大"}}).encode(),
         "payload_too_large", "请求体过大"),
        (400, json.dumps({"error": "plain"}).encode(), None, '{"error": "plain"}'),
    ],
)  # fmt: skip
def test_http_errors_without_envelope(status, body, code, message):
    server = _RawServer(status, body)
    try:
        with pytest.raises(ApiError) as info:
            Client(server.url).register({})
    finally:
        server.close()
    assert info.value.status == status
    assert info.value.code == code
    assert info.value.message == message


@pytest.mark.parametrize("body", [b"not json", b"[1, 2]", b"\xff\xfe"])
def test_invalid_success_response(body):
    server = _RawServer(200, body)
    try:
        with pytest.raises(ApiError) as info:
            Client(server.url).heartbeat({})
    finally:
        server.close()
    assert info.value.code == "invalid_response"


def test_connection_refused():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    with pytest.raises(TransportError):
        Client(f"http://127.0.0.1:{port}").register({})


def test_connection_dropped(platform):
    platform.mode = "drop"
    with pytest.raises(TransportError):
        Client(platform.url, "secret-token-0123456789").heartbeat({"node_key": "k"})


def test_timeout():
    listener = socket.socket()
    listener.bind(("127.0.0.1", 0))
    listener.listen(1)  # 接受连接但从不响应
    try:
        client = Client(f"http://127.0.0.1:{listener.getsockname()[1]}", timeout=0.3)
        with pytest.raises(TransportError):
            client.register({})
    finally:
        listener.close()


def test_api_error_str():
    assert (
        str(ApiError(401, "unauthorized", "节点令牌不正确"))
        == "HTTP 401 unauthorized: 节点令牌不正确"
    )
    assert str(ApiError(502, None, "Bad Gateway")) == "HTTP 502: Bad Gateway"
