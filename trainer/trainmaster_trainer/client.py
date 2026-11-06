"""平台的节点接口客户端（/api/v1/compute/agent/*），只用标准库 urllib。

除 JSON 请求（POST，以及读取任务数据的 GET）外还支持流式下载（边写文件边计算 sha256）与流式上传文件
（PUT 原始字节），供任务（如模型转换、审图识别）传输大文件；内存中只保留一个分块。
"""

from __future__ import annotations

import contextlib
import hashlib
import http.client
import json
import math
import os
import urllib.error
import urllib.request
from collections.abc import Callable, Iterator, Mapping
from pathlib import Path
from typing import Any, NamedTuple

from . import __version__

DEFAULT_TIMEOUT = 10.0
MAX_ERROR_BODY = 64 * 1024
CHUNK_SIZE = 1 << 20
USER_AGENT = f"trainmaster-trainer/{__version__}"


class ApiError(Exception):
    """服务端返回了 HTTP 错误。code / message 来自错误信封 {"error": {"code", "message"}}（message 为中文）。"""

    def __init__(self, status: int, code: str | None, message: str) -> None:
        super().__init__(f"HTTP {status}" + (f" {code}" if code else "") + f": {message}")
        self.status = status
        self.code = code
        self.message = message


class TransportError(Exception):
    """无法连接服务端、超时或连接中断（也包括调用方通过 abort 中止的传输）。"""


class Transfer(NamedTuple):
    """下载的结果：字节数与 sha256（小写十六进制）。"""

    size: int
    sha256: str


class _Aborted(Exception):
    """上传途中 abort() 返回 True：从请求体的生成器中抛出，结束这次请求。"""


def json_safe(value: Any) -> Any:
    """把 NaN / Inf 换成 null（JSON 不支持它们；偶尔异常的指标不应让心跳失败）。"""
    if isinstance(value, float):
        return value if math.isfinite(value) else None
    if isinstance(value, dict):
        return {k: json_safe(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [json_safe(v) for v in value]
    return value


def _parse_error(status: int, body: bytes, reason: str) -> ApiError:
    text = body.decode("utf-8", "replace").strip()
    try:
        data = json.loads(text)
    except ValueError:
        data = None
    if isinstance(data, dict) and isinstance(data.get("error"), dict):
        err = data["error"]
        code = err.get("code") if isinstance(err.get("code"), str) else None
        message = err.get("message") if isinstance(err.get("message"), str) else ""
        return ApiError(status, code, message or reason or f"HTTP {status}")
    snippet = " ".join(text.split())[:200]
    return ApiError(status, None, snippet or reason or f"HTTP {status}")


class Client:
    def __init__(
        self,
        base_url: str,
        token: str | None = None,
        *,
        timeout: float = DEFAULT_TIMEOUT,
        opener: urllib.request.OpenerDirector | None = None,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout
        self._opener = opener or urllib.request.build_opener()

    def _headers(self, extra: Mapping[str, str] | None, **base: str) -> dict[str, str]:
        headers = {"User-Agent": USER_AGENT, **base}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        if extra:
            headers.update(extra)
        return headers

    def _open(self, req: urllib.request.Request, timeout: float | None) -> Any:
        """发送请求并返回响应（调用方负责关闭）。HTTP 错误抛出 ApiError，网络错误抛出 TransportError。"""
        try:
            return self._opener.open(req, timeout=self.timeout if timeout is None else timeout)
        except urllib.error.HTTPError as exc:
            try:
                err_body = exc.read(MAX_ERROR_BODY)
            except (OSError, http.client.HTTPException):
                err_body = b""
            finally:
                exc.close()
            raise _parse_error(exc.code, err_body, str(exc.reason or "")) from None
        except urllib.error.URLError as exc:
            if isinstance(exc.reason, _Aborted):
                raise TransportError("upload aborted") from None
            raise TransportError(str(exc.reason)) from exc
        except _Aborted:
            raise TransportError("upload aborted") from None
        except (OSError, http.client.HTTPException) as exc:  # 超时、连接被重置、响应不完整等
            raise TransportError(f"{type(exc).__name__}: {exc}") from exc

    def _json_response(self, req: urllib.request.Request, timeout: float | None) -> dict[str, Any]:
        resp = self._open(req, timeout)
        try:
            with resp:
                status = resp.status
                raw = resp.read()
        except (OSError, http.client.HTTPException) as exc:
            raise TransportError(f"{type(exc).__name__}: {exc}") from exc

        try:
            data = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, ValueError) as exc:
            raise ApiError(
                status, "invalid_response", f"response is not valid JSON: {exc}"
            ) from exc
        if not isinstance(data, dict):
            raise ApiError(status, "invalid_response", "response is not a JSON object")
        return data

    def post(
        self,
        path: str,
        payload: dict[str, Any],
        *,
        timeout: float | None = None,
        headers: Mapping[str, str] | None = None,
    ) -> dict[str, Any]:
        """POST JSON，返回解析后的响应对象。HTTP 错误抛出 ApiError，网络错误抛出 TransportError。

        timeout 默认为客户端的超时（长轮询时调用方给出更长的值）；headers 为附加的请求头（如 X-Node-Key）。
        """
        body = json.dumps(json_safe(payload), ensure_ascii=False, allow_nan=False).encode("utf-8")
        req = urllib.request.Request(
            self.base_url + path,
            data=body,
            headers=self._headers(
                headers,
                **{"Content-Type": "application/json; charset=utf-8", "Accept": "application/json"},
            ),
            method="POST",
        )
        return self._json_response(req, timeout)

    def get(
        self,
        path: str,
        *,
        timeout: float | None = None,
        headers: Mapping[str, str] | None = None,
    ) -> dict[str, Any]:
        """GET，返回解析后的 JSON 对象（如审图识别任务的图片列表）。错误与 post 相同。"""
        req = urllib.request.Request(
            self.base_url + path,
            headers=self._headers(headers, Accept="application/json"),
            method="GET",
        )
        return self._json_response(req, timeout)

    def download(
        self,
        path: str,
        dest: Path,
        *,
        headers: Mapping[str, str] | None = None,
        timeout: float | None = None,
        max_bytes: int | None = None,
        abort: Callable[[], bool] | None = None,
    ) -> Transfer:
        """GET path，把响应体流式写入 dest，同时计算 sha256。

        timeout 是每次读写的超时（不是整个下载的时间上限）。响应比 max_bytes 大、比 Content-Length 短、
        或 abort() 返回 True 时抛出 TransportError；写文件失败抛出 OSError。失败时删除写了一半的 dest。
        """
        req = urllib.request.Request(
            self.base_url + path,
            headers=self._headers(headers, Accept="*/*"),
            method="GET",
        )
        resp = self._open(req, timeout)
        digest = hashlib.sha256()
        size = 0
        try:
            with resp, open(dest, "wb") as f:
                declared = resp.headers.get("Content-Length")
                expected = int(declared) if declared and declared.strip().isdigit() else None
                if max_bytes is not None and expected is not None and expected > max_bytes:
                    raise TransportError(
                        f"response has {expected} bytes, more than the expected {max_bytes}"
                    )
                while True:
                    if abort is not None and abort():
                        raise TransportError("download aborted")
                    try:
                        chunk = resp.read(CHUNK_SIZE)
                    except (OSError, http.client.HTTPException) as exc:
                        raise TransportError(f"{type(exc).__name__}: {exc}") from exc
                    if not chunk:
                        break
                    size += len(chunk)
                    if max_bytes is not None and size > max_bytes:
                        raise TransportError(
                            f"response is larger than the expected {max_bytes} bytes"
                        )
                    digest.update(chunk)
                    f.write(chunk)
                if expected is not None and size != expected:
                    # http.client 在连接提前关闭时只返回空字节，不报错
                    raise TransportError(f"connection closed after {size} of {expected} bytes")
        except BaseException:
            with contextlib.suppress(OSError):
                dest.unlink()
            raise
        return Transfer(size, digest.hexdigest())

    def put_file(
        self,
        path: str,
        file: Path,
        *,
        headers: Mapping[str, str] | None = None,
        timeout: float | None = None,
        abort: Callable[[], bool] | None = None,
    ) -> dict[str, Any]:
        """PUT 文件的原始字节（带 Content-Length，分块读取，不整体读入内存），返回解析后的 JSON 响应。

        abort() 在每个分块之前检查，返回 True 时中止上传并抛出 TransportError。打开文件失败抛出 OSError。
        """
        size = os.stat(file).st_size

        def body() -> Iterator[bytes]:
            with open(file, "rb") as f:
                while True:
                    if abort is not None and abort():
                        raise _Aborted
                    chunk = f.read(CHUNK_SIZE)
                    if not chunk:
                        return
                    yield chunk

        chunks = body()
        req = urllib.request.Request(
            self.base_url + path,
            data=chunks,
            headers=self._headers(
                headers,
                **{
                    "Content-Type": "application/octet-stream",
                    "Content-Length": str(size),
                    "Accept": "application/json",
                },
            ),
            method="PUT",
        )
        try:
            return self._json_response(req, timeout)
        finally:
            # 发送中途出错时生成器停在 yield 处，文件要等它被回收才关闭；Windows 上之后删除工作目录会失败
            chunks.close()

    def register(self, payload: dict[str, Any]) -> dict[str, Any]:
        return self.post("/api/v1/compute/agent/register", payload)

    def heartbeat(self, payload: dict[str, Any]) -> dict[str, Any]:
        return self.post("/api/v1/compute/agent/heartbeat", payload)
