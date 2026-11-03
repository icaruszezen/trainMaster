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


# ---- 自定义超时与请求头、流式下载与上传 ----


def test_post_with_headers_and_timeout(platform, monkeypatch):
    client = Client(platform.url, "secret-token-0123456789")
    seen: dict[str, Any] = {}
    original = client._opener.open

    def spy(req, timeout=None):
        seen["timeout"] = timeout
        return original(req, timeout=timeout)

    monkeypatch.setattr(client._opener, "open", spy)
    client.post(
        REGISTER, {"node_key": "k1", "hardware": {}}, timeout=40, headers={"X-Node-Key": "k1"}
    )
    assert seen["timeout"] == 40
    headers = platform.requests[-1]["headers"]
    assert headers["X-Node-Key"] == "k1"
    assert headers["Authorization"] == "Bearer secret-token-0123456789"
    client.register({"node_key": "k1", "hardware": {}})
    assert seen["timeout"] == client.timeout == 10.0


class _FileServer:
    """GET 返回固定内容（可声明错误的 Content-Length 并提前断开），PUT 记录收到的内容并返回 JSON。"""

    def __init__(
        self, body: bytes = b"", *, declared: int | None = None, status: int = 200
    ) -> None:
        outer = self
        self.body, self.declared, self.status = body, declared, status
        self.puts: list[dict[str, Any]] = []

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args: Any) -> None:
                pass

            def do_GET(self) -> None:  # noqa: N802
                outer.get_headers = dict(self.headers)
                if outer.status != 200:
                    payload = json.dumps(
                        {"error": {"code": "not_found", "message": "转换任务不存在"}}
                    )
                    self.send_response(outer.status)
                    self.send_header("Content-Length", str(len(payload.encode())))
                    self.end_headers()
                    self.wfile.write(payload.encode())
                    return
                self.send_response(200)
                length = outer.declared if outer.declared is not None else len(outer.body)
                self.send_header("Content-Length", str(length))
                self.end_headers()
                self.wfile.write(outer.body)
                self.close_connection = True

            def do_PUT(self) -> None:  # noqa: N802
                length = int(self.headers["Content-Length"])
                data = self.rfile.read(length)
                outer.puts.append({"headers": dict(self.headers), "body": data})
                status = outer.status
                payload = json.dumps(
                    {"received_bytes": len(data)}
                    if status == 200
                    else {"error": {"code": "validation_failed", "message": "sha256 不一致"}}
                ).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        threading.Thread(
            target=self.server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True
        ).start()
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def files():
    servers: list[_FileServer] = []

    def make(*args: Any, **kw: Any) -> _FileServer:
        servers.append(_FileServer(*args, **kw))
        return servers[-1]

    yield make
    for s in servers:
        s.close()


def test_download_streams_and_hashes(files, tmp_path, monkeypatch):
    import hashlib

    from trainmaster_trainer import client as client_mod

    monkeypatch.setattr(client_mod, "CHUNK_SIZE", 1000)  # 分多块读取
    body = bytes(range(256)) * 50
    server = files(body)
    dest = tmp_path / "model.pt"
    got = Client(server.url, "tok-0123456789abcdef").download(
        "/input", dest, headers={"X-Node-Key": "k1"}, max_bytes=len(body)
    )
    assert got.size == len(body)
    assert got.sha256 == hashlib.sha256(body).hexdigest()
    assert dest.read_bytes() == body
    assert server.get_headers["X-Node-Key"] == "k1"
    assert server.get_headers["Authorization"] == "Bearer tok-0123456789abcdef"


def test_download_larger_than_expected(files, tmp_path):
    server = files(b"x" * 100)
    dest = tmp_path / "model.pt"
    with pytest.raises(TransportError) as info:
        Client(server.url).download("/input", dest, max_bytes=99)
    assert "more than the expected 99" in str(info.value)
    assert not dest.exists()


def test_download_truncated(files, tmp_path):
    server = files(b"x" * 100, declared=200)  # 声明 200 字节，只发送 100 字节就断开
    dest = tmp_path / "model.pt"
    with pytest.raises(TransportError) as info:
        Client(server.url).download("/input", dest)
    assert "100 of 200" in str(info.value)
    assert not dest.exists()  # 写了一半的文件被删除


def test_download_abort(files, tmp_path):
    server = files(b"x" * 100)
    dest = tmp_path / "model.pt"
    with pytest.raises(TransportError):
        Client(server.url).download("/input", dest, abort=lambda: True)
    assert not dest.exists()


def test_download_http_error(files, tmp_path):
    server = files(status=404)
    dest = tmp_path / "model.pt"
    with pytest.raises(ApiError) as info:
        Client(server.url).download("/input", dest)
    assert (info.value.status, info.value.code) == (404, "not_found")
    assert not dest.exists()


def test_download_write_error_is_os_error(files, tmp_path):
    server = files(b"data")
    with pytest.raises(OSError):
        Client(server.url).download("/input", tmp_path / "missing-dir" / "model.pt")


def test_put_file_streams_with_headers(files, tmp_path, monkeypatch):
    from trainmaster_trainer import client as client_mod

    monkeypatch.setattr(client_mod, "CHUNK_SIZE", 7)
    server = files()
    path = tmp_path / "model.onnx"
    path.write_bytes(b"onnx-bytes" * 10)
    resp = Client(server.url, "tok-0123456789abcdef").put_file(
        "/output", path, headers={"X-Content-Sha256": "ab" * 32}
    )
    assert resp == {"received_bytes": 100}
    put = server.puts[0]
    assert put["body"] == b"onnx-bytes" * 10
    assert put["headers"]["Content-Length"] == "100"
    assert put["headers"]["Content-Type"] == "application/octet-stream"
    assert put["headers"]["X-Content-Sha256"] == "ab" * 32
    assert "Transfer-Encoding" not in put["headers"]


def test_put_file_error_and_abort(files, tmp_path):
    path = tmp_path / "model.onnx"
    path.write_bytes(b"x" * 10)
    server = files(status=422)
    with pytest.raises(ApiError) as info:
        Client(server.url).put_file("/output", path)
    assert (info.value.status, info.value.message) == (422, "sha256 不一致")
    with pytest.raises(TransportError) as info2:
        Client(server.url).put_file("/output", path, abort=lambda: True)
    assert "aborted" in str(info2.value)
    with pytest.raises(OSError):
        Client(server.url).put_file("/output", tmp_path / "missing.onnx")
