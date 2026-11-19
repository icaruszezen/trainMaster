"""Transparent multi-threaded HTTP reverse spy proxy with fault-injection hooks."""

from __future__ import annotations

import atexit
from dataclasses import dataclass
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading
import time
import urllib.parse

from .http_client import CaseInsensitiveHeaders
from .postgres import allocate_free_port

HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}


@dataclass(frozen=True)
class SpyRecord:
    method: str
    path: str
    query: str
    req_headers: dict[str, str]
    status: int
    resp_headers: dict[str, str]
    req_bytes: int
    resp_bytes: int
    timestamp: float

    @property
    def full_path(self) -> str:
        return f"{self.path}?{self.query}" if self.query else self.path


class _QuietThreadingHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


class HttpSpyProxy:
    """Transparent HTTP/1.1 spy proxy recording requests, headers, and status codes."""

    def __init__(self, upstream_url: str, *, name: str = "spy") -> None:
        parsed = urllib.parse.urlsplit(upstream_url)
        if not parsed.hostname or not parsed.port:
            raise ValueError(f"Invalid upstream_url {upstream_url!r}; expected http://host:port")
        self.name = name
        self.upstream_url = upstream_url.rstrip("/")
        self.upstream_host = parsed.hostname
        self.upstream_port = parsed.port
        self.host = "127.0.0.1"
        self.port = allocate_free_port(self.host)

        self._lock = threading.Lock()
        self._records: list[SpyRecord] = []
        self._blocked = False
        self._corrupt_prefixes: set[str] = set()
        self._override_headers: dict[str, dict[str, str]] = {}
        self._force_status: dict[str, tuple[int, bytes]] = {}
        self._gates: dict[str, tuple[threading.Event, threading.Event]] = {}
        self._delay_seconds: dict[str, float] = {}

        self._server: _QuietThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        atexit.register(self.stop)

    @property
    def url(self) -> str:
        return f"http://{self.host}:{self.port}"

    def start(self) -> HttpSpyProxy:
        if self._server is not None:
            return self
        proxy = self

        class ProxyHandler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                return

            def _handle_any(self) -> None:
                proxy._forward(self)

            do_GET = _handle_any
            do_HEAD = _handle_any
            do_POST = _handle_any
            do_PUT = _handle_any
            do_PATCH = _handle_any
            do_DELETE = _handle_any

        self._server = _QuietThreadingHTTPServer((self.host, self.port), ProxyHandler)
        self._thread = threading.Thread(
            target=self._server.serve_forever,
            name=f"e2e-spy-{self.name}-{self.port}",
            daemon=True,
        )
        self._thread.start()
        return self

    def stop(self) -> None:
        srv = self._server
        self._server = None
        with self._lock:
            for _, release_ev in self._gates.values():
                release_ev.set()
        if srv is not None:
            srv.shutdown()
            srv.server_close()
        if self._thread is not None:
            self._thread.join(timeout=3.0)
            self._thread = None

    # ---- Observation helpers ----

    def records(self) -> list[SpyRecord]:
        with self._lock:
            return list(self._records)

    def clear(self) -> None:
        with self._lock:
            self._records.clear()

    def archive_requests(self, method: str | None = None) -> list[SpyRecord]:
        return self.matching_requests("/api/v1/archives", method=method)

    def space_requests(self, method: str | None = None) -> list[SpyRecord]:
        return self.matching_requests("/api/v1/spaces", method=method)

    def matching_requests(self, path_prefix: str, *, method: str | None = None) -> list[SpyRecord]:
        want_method = method.upper() if method else None
        out: list[SpyRecord] = []
        for rec in self.records():
            if rec.path.startswith(path_prefix):
                if want_method is None or rec.method == want_method:
                    out.append(rec)
        return out

    # ---- Fault-injection & synchronization controls ----

    def set_blocked(self, blocked: bool) -> None:
        """Simulate unreachable peer subnet (returns 502 Bad Gateway immediately)."""
        with self._lock:
            self._blocked = blocked

    def set_corrupt_response_body(self, path_prefix: str, enabled: bool = True) -> None:
        """Flip bytes in streamed response body while preserving X-Archive-Sha256 header."""
        with self._lock:
            if enabled:
                self._corrupt_prefixes.add(path_prefix)
            else:
                self._corrupt_prefixes.discard(path_prefix)

    def set_response_header_override(self, path_prefix: str, headers: dict[str, str] | None) -> None:
        with self._lock:
            if headers is None:
                self._override_headers.pop(path_prefix, None)
            else:
                self._override_headers[path_prefix] = dict(headers)

    def set_forced_status(self, path_prefix: str, status: int | None, body: bytes = b"") -> None:
        with self._lock:
            if status is None:
                self._force_status.pop(path_prefix, None)
            else:
                self._force_status[path_prefix] = (status, body)

    def set_delay(self, path_prefix: str, seconds: float) -> None:
        with self._lock:
            if seconds <= 0:
                self._delay_seconds.pop(path_prefix, None)
            else:
                self._delay_seconds[path_prefix] = seconds

    def install_gate(self, path_prefix: str) -> tuple[threading.Event, threading.Event]:
        """Pause matching requests: returns (entered_event, release_event)."""
        entered = threading.Event()
        release = threading.Event()
        with self._lock:
            self._gates[path_prefix] = (entered, release)
        return entered, release

    def clear_faults(self) -> None:
        with self._lock:
            self._blocked = False
            self._corrupt_prefixes.clear()
            self._override_headers.clear()
            self._force_status.clear()
            self._delay_seconds.clear()
            for _, rel in self._gates.values():
                rel.set()
            self._gates.clear()

    def _read_request_body(self, handler: BaseHTTPRequestHandler) -> bytes:
        te = (handler.headers.get("Transfer-Encoding") or "").lower()
        if "chunked" in te:
            chunks: list[bytes] = []
            while True:
                line = handler.rfile.readline()
                if not line:
                    break
                size_str = line.split(b";", 1)[0].strip()
                size = int(size_str, 16)
                if size == 0:
                    handler.rfile.readline()
                    break
                chunks.append(handler.rfile.read(size))
                handler.rfile.read(2)  # CRLF
            return b"".join(chunks)
        cl = handler.headers.get("Content-Length")
        if cl and cl.strip().isdigit():
            n = int(cl.strip())
            return handler.rfile.read(n) if n > 0 else b""
        return b""

    def _forward(self, handler: BaseHTTPRequestHandler) -> None:
        split = urllib.parse.urlsplit(handler.path)
        path = split.path or "/"
        query = split.query or ""
        req_hdrs = {k.lower(): v for k, v in handler.headers.items()}
        req_body = self._read_request_body(handler)

        with self._lock:
            blocked = self._blocked
            forced = next((v for p, v in self._force_status.items() if path.startswith(p)), None)
            gate = next((v for p, v in self._gates.items() if path.startswith(p)), None)
            delay = next((v for p, v in self._delay_seconds.items() if path.startswith(p)), 0.0)
            corrupt = any(path.startswith(p) for p in self._corrupt_prefixes)
            hdr_override = next((dict(v) for p, v in self._override_headers.items() if path.startswith(p)), {})

        if gate is not None:
            entered_ev, release_ev = gate
            entered_ev.set()
            release_ev.wait(timeout=15.0)

        if delay > 0:
            time.sleep(delay)

        if blocked:
            msg = b'{"error":{"code":"peer_unreachable","message":"simulated subnet isolation"}}'
            self._record(handler.command, path, query, req_hdrs, 502, {"content-type": "application/json"}, len(req_body), len(msg))
            handler.send_response(502)
            handler.send_header("Content-Type", "application/json")
            handler.send_header("Content-Length", str(len(msg)))
            handler.send_header("Connection", "close")
            handler.end_headers()
            if handler.command != "HEAD":
                handler.wfile.write(msg)
            handler.close_connection = True
            return

        if forced is not None:
            status_code, forced_body = forced
            self._record(handler.command, path, query, req_hdrs, status_code, {"content-type": "application/json"}, len(req_body), len(forced_body))
            handler.send_response(status_code)
            handler.send_header("Content-Type", "application/json")
            handler.send_header("Content-Length", str(len(forced_body)))
            handler.send_header("Connection", "close")
            handler.end_headers()
            if handler.command != "HEAD" and forced_body:
                handler.wfile.write(forced_body)
            handler.close_connection = True
            return

        conn = http.client.HTTPConnection(self.upstream_host, self.upstream_port, timeout=60.0)
        try:
            fwd_headers: dict[str, str] = {}
            for k, v in handler.headers.items():
                if k.lower() in HOP_BY_HOP_HEADERS or k.lower() in ("host", "content-length"):
                    continue
                fwd_headers[k] = v
            fwd_headers["Host"] = f"{self.upstream_host}:{self.upstream_port}"
            fwd_headers["Content-Length"] = str(len(req_body))

            target = f"{path}?{query}" if query else path
            conn.request(handler.command, target, body=req_body, headers=fwd_headers)
            up_resp = conn.getresponse()
            resp_body = b"" if handler.command == "HEAD" else up_resp.read()
            if corrupt and resp_body:
                mut = bytearray(resp_body)
                mut[len(mut) // 2] ^= 0xFF
                resp_body = bytes(mut)

            resp_hdr_map = CaseInsensitiveHeaders(list(up_resp.getheaders()))
            for ok_k, ok_v in hdr_override.items():
                resp_hdr_map[ok_k] = ok_v

            handler.send_response(up_resp.status)
            sent_cl = False
            for k, v in up_resp.getheaders():
                low = k.lower()
                if low in HOP_BY_HOP_HEADERS:
                    continue
                if low in {hk.lower() for hk in hdr_override}:
                    continue
                if low == "content-length":
                    sent_cl = True
                    if handler.command != "HEAD":
                        handler.send_header("Content-Length", str(len(resp_body)))
                    else:
                        handler.send_header("Content-Length", v)
                    continue
                handler.send_header(k, v)
            for ok_k, ok_v in hdr_override.items():
                handler.send_header(ok_k, ok_v)
            if not sent_cl and handler.command != "HEAD":
                handler.send_header("Content-Length", str(len(resp_body)))
            handler.send_header("Connection", "close")
            handler.end_headers()
            if handler.command != "HEAD" and resp_body:
                handler.wfile.write(resp_body)
            handler.close_connection = True

            self._record(
                handler.command,
                path,
                query,
                req_hdrs,
                up_resp.status,
                dict(resp_hdr_map),
                len(req_body),
                len(resp_body),
            )
        finally:
            conn.close()

    def _record(
        self,
        method: str,
        path: str,
        query: str,
        req_headers: dict[str, str],
        status: int,
        resp_headers: dict[str, str],
        req_bytes: int,
        resp_bytes: int,
    ) -> None:
        rec = SpyRecord(
            method=method.upper(),
            path=path,
            query=query,
            req_headers=req_headers,
            status=status,
            resp_headers={k.lower(): v for k, v in resp_headers.items()},
            req_bytes=req_bytes,
            resp_bytes=resp_bytes,
            timestamp=time.time(),
        )
        with self._lock:
            self._records.append(rec)
