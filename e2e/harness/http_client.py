"""Proxy-bypassing pure-stdlib HTTP client for loopback E2E verification."""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
from typing import Any, Mapping
import urllib.error
import urllib.parse
import urllib.request


def enforce_no_proxy_env(env: dict[str, str] | None = None) -> dict[str, str]:
    """Ensure 127.0.0.1 and localhost bypass any sandbox HTTP proxy."""
    target = os.environ if env is None else env
    required = ["127.0.0.1", "localhost", "::1"]
    existing = target.get("NO_PROXY") or target.get("no_proxy") or ""
    parts = [p.strip() for p in existing.split(",") if p.strip()]
    for host in required:
        if host not in parts:
            parts.append(host)
    joined = ",".join(parts)
    target["NO_PROXY"] = joined
    target["no_proxy"] = joined
    return target


enforce_no_proxy_env()


class CaseInsensitiveHeaders(dict[str, str]):
    """Case-insensitive HTTP response header mapping."""

    def __init__(self, items: Mapping[str, str] | list[tuple[str, str]] | None = None) -> None:
        super().__init__()
        self._orig: dict[str, str] = {}
        if items:
            iterator = items.items() if isinstance(items, Mapping) else items
            for k, v in iterator:
                self[k] = v

    def __setitem__(self, key: str, value: str) -> None:
        low = key.lower()
        self._orig[low] = key
        super().__setitem__(low, value)

    def __getitem__(self, key: str) -> str:
        return super().__getitem__(key.lower())

    def __contains__(self, key: object) -> bool:
        return isinstance(key, str) and super().__contains__(key.lower())

    def get(self, key: str, default: Any = None) -> Any:
        return super().get(key.lower(), default)


@dataclass
class HttpResponse:
    status: int
    headers: CaseInsensitiveHeaders
    body: bytes

    @property
    def text(self) -> str:
        return self.body.decode("utf-8", errors="replace")

    def json(self) -> Any:
        return json.loads(self.body.decode("utf-8"))


@dataclass
class DownloadResult:
    status: int
    headers: CaseInsensitiveHeaders
    size_bytes: int
    sha256: str
    error_body: bytes = b""


class HttpClient:
    """Stdlib HTTP client using ProxyHandler({}) so loopback traffic never hits proxy."""

    def __init__(
        self,
        base_url: str = "",
        *,
        bearer_token: str | None = None,
        default_headers: Mapping[str, str] | None = None,
        timeout: float = 30.0,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.bearer_token = bearer_token
        self.default_headers = dict(default_headers or {})
        self.timeout = timeout
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def _resolve_url(self, path_or_url: str, query: Mapping[str, Any] | None = None) -> str:
        if path_or_url.startswith(("http://", "https://")):
            url = path_or_url
        else:
            rel = path_or_url if path_or_url.startswith("/") else f"/{path_or_url}"
            url = f"{self.base_url}{rel}"
        if query:
            clean = {k: str(v) for k, v in query.items() if v is not None}
            if clean:
                sep = "&" if "?" in url else "?"
                url = f"{url}{sep}{urllib.parse.urlencode(clean)}"
        return url

    def _build_headers(self, extra: Mapping[str, str] | None = None) -> dict[str, str]:
        hdrs: dict[str, str] = dict(self.default_headers)
        if self.bearer_token is not None:
            hdrs["Authorization"] = f"Bearer {self.bearer_token}"
        if extra:
            hdrs.update(extra)
        return hdrs

    def request(
        self,
        method: str,
        path_or_url: str,
        *,
        query: Mapping[str, Any] | None = None,
        json_body: Any = None,
        raw_body: bytes | None = None,
        headers: Mapping[str, str] | None = None,
        timeout: float | None = None,
    ) -> HttpResponse:
        url = self._resolve_url(path_or_url, query)
        hdrs = self._build_headers(headers)
        payload: bytes | None = raw_body
        if json_body is not None:
            payload = json.dumps(json_body, ensure_ascii=False).encode("utf-8")
            hdrs.setdefault("Content-Type", "application/json; charset=utf-8")
        if payload is not None:
            hdrs.setdefault("Content-Length", str(len(payload)))

        req = urllib.request.Request(url, data=payload, headers=hdrs, method=method.upper())
        eff_timeout = self.timeout if timeout is None else timeout
        try:
            with self._opener.open(req, timeout=eff_timeout) as resp:
                resp_hdrs = CaseInsensitiveHeaders(list(resp.headers.items()))
                data = b"" if method.upper() == "HEAD" else resp.read()
                return HttpResponse(status=resp.status, headers=resp_hdrs, body=data)
        except urllib.error.HTTPError as exc:
            try:
                err_data = b"" if method.upper() == "HEAD" else exc.read()
            finally:
                exc.close()
            resp_hdrs = CaseInsensitiveHeaders(list(exc.headers.items()) if exc.headers else [])
            return HttpResponse(status=exc.code, headers=resp_hdrs, body=err_data)

    def get(self, path: str, **kwargs: Any) -> HttpResponse:
        return self.request("GET", path, **kwargs)

    def head(self, path: str, **kwargs: Any) -> HttpResponse:
        return self.request("HEAD", path, **kwargs)

    def post(self, path: str, **kwargs: Any) -> HttpResponse:
        return self.request("POST", path, **kwargs)

    def put(self, path: str, **kwargs: Any) -> HttpResponse:
        return self.request("PUT", path, **kwargs)

    def patch(self, path: str, **kwargs: Any) -> HttpResponse:
        return self.request("PATCH", path, **kwargs)

    def delete(self, path: str, **kwargs: Any) -> HttpResponse:
        return self.request("DELETE", path, **kwargs)

    def stream_download(
        self,
        path_or_url: str,
        dest_file: Path,
        *,
        headers: Mapping[str, str] | None = None,
        timeout: float | None = None,
    ) -> DownloadResult:
        url = self._resolve_url(path_or_url)
        hdrs = self._build_headers(headers)
        req = urllib.request.Request(url, headers=hdrs, method="GET")
        eff_timeout = self.timeout if timeout is None else timeout
        digest = hashlib.sha256()
        total = 0
        try:
            with self._opener.open(req, timeout=eff_timeout) as resp:
                resp_hdrs = CaseInsensitiveHeaders(list(resp.headers.items()))
                dest_file.parent.mkdir(parents=True, exist_ok=True)
                with open(dest_file, "wb") as fh:
                    while True:
                        chunk = resp.read(1 << 16)
                        if not chunk:
                            break
                        digest.update(chunk)
                        fh.write(chunk)
                        total += len(chunk)
                return DownloadResult(
                    status=resp.status,
                    headers=resp_hdrs,
                    size_bytes=total,
                    sha256=digest.hexdigest(),
                )
        except urllib.error.HTTPError as exc:
            try:
                err_body = exc.read()
            finally:
                exc.close()
            resp_hdrs = CaseInsensitiveHeaders(list(exc.headers.items()) if exc.headers else [])
            return DownloadResult(
                status=exc.code,
                headers=resp_hdrs,
                size_bytes=0,
                sha256="",
                error_body=err_body,
            )
