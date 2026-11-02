"""平台的节点接口客户端（/api/v1/compute/agent/*），只用标准库 urllib。"""

from __future__ import annotations

import http.client
import json
import math
import urllib.error
import urllib.request
from typing import Any

from . import __version__

DEFAULT_TIMEOUT = 10.0
MAX_ERROR_BODY = 64 * 1024
USER_AGENT = f"trainmaster-trainer/{__version__}"


class ApiError(Exception):
    """服务端返回了 HTTP 错误。code / message 来自错误信封 {"error": {"code", "message"}}（message 为中文）。"""

    def __init__(self, status: int, code: str | None, message: str) -> None:
        super().__init__(f"HTTP {status}" + (f" {code}" if code else "") + f": {message}")
        self.status = status
        self.code = code
        self.message = message


class TransportError(Exception):
    """无法连接服务端、超时或连接中断。"""


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

    def post(self, path: str, payload: dict[str, Any]) -> dict[str, Any]:
        """POST JSON，返回解析后的响应对象。HTTP 错误抛出 ApiError，网络错误抛出 TransportError。"""
        body = json.dumps(json_safe(payload), ensure_ascii=False, allow_nan=False).encode("utf-8")
        headers = {
            "Content-Type": "application/json; charset=utf-8",
            "Accept": "application/json",
            "User-Agent": USER_AGENT,
        }
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        req = urllib.request.Request(
            self.base_url + path, data=body, headers=headers, method="POST"
        )
        try:
            with self._opener.open(req, timeout=self.timeout) as resp:
                status = resp.status
                raw = resp.read()
        except urllib.error.HTTPError as exc:
            try:
                err_body = exc.read(MAX_ERROR_BODY)
            except (OSError, http.client.HTTPException):
                err_body = b""
            finally:
                exc.close()
            raise _parse_error(exc.code, err_body, str(exc.reason or "")) from None
        except urllib.error.URLError as exc:
            raise TransportError(str(exc.reason)) from exc
        except (OSError, http.client.HTTPException) as exc:  # 超时、连接被重置、响应不完整等
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

    def register(self, payload: dict[str, Any]) -> dict[str, Any]:
        return self.post("/api/v1/compute/agent/register", payload)

    def heartbeat(self, payload: dict[str, Any]) -> dict[str, Any]:
        return self.post("/api/v1/compute/agent/heartbeat", payload)
