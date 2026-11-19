"""In-process pure-stdlib S3-compatible HTTP mock server for legacy S3 E2E verification."""

from __future__ import annotations

import atexit
from dataclasses import dataclass
from datetime import datetime, timezone
from email.utils import format_datetime
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading
import urllib.parse
import xml.etree.ElementTree as ET
from xml.sax.saxutils import escape as xml_escape

from .postgres import allocate_free_port


@dataclass
class _StoredObject:
    data: bytes
    etag: str
    mod_time: datetime


class _MultipartUpload:
    def __init__(self, bucket: str, key: str) -> None:
        self.bucket = bucket
        self.key = key
        self.parts: dict[int, bytes] = {}


@dataclass(frozen=True)
class S3RequestLog:
    method: str
    path: str
    query: str
    status: int


class _QuietThreadingHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


class S3MockServer:
    """Path-style S3 HTTP mock server supporting SigV4 check, ListV2, Range GET, PUT, Copy, Multipart & Delete."""

    def __init__(
        self,
        buckets: list[str] | tuple[str, ...] = ("tm-datasets", "tm-secondary"),
        *,
        access_key: str = "minioadmin",
        secret_key: str = "minioadmin-secret-123",
    ) -> None:
        self.access_key = access_key
        self.secret_key = secret_key
        self.host = "127.0.0.1"
        self.port = allocate_free_port(self.host)

        self._lock = threading.RLock()
        self._buckets: dict[str, dict[str, _StoredObject]] = {b: {} for b in buckets}
        self._uploads: dict[str, _MultipartUpload] = {}
        self._upload_seq = 0
        self._requests: list[S3RequestLog] = []

        self._server: _QuietThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        atexit.register(self.stop)

    @property
    def url(self) -> str:
        return f"http://{self.host}:{self.port}"

    def start(self) -> S3MockServer:
        if self._server is not None:
            return self
        s3 = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                return

            def _dispatch(self) -> None:
                s3._handle(self)

            do_GET = _dispatch
            do_HEAD = _dispatch
            do_PUT = _dispatch
            do_POST = _dispatch
            do_DELETE = _dispatch

        self._server = _QuietThreadingHTTPServer((self.host, self.port), Handler)
        self._thread = threading.Thread(
            target=self._server.serve_forever,
            name=f"e2e-s3-mock-{self.port}",
            daemon=True,
        )
        self._thread.start()
        return self

    def stop(self) -> None:
        srv = self._server
        self._server = None
        if srv is not None:
            srv.shutdown()
            srv.server_close()
        if self._thread is not None:
            self._thread.join(timeout=3.0)
            self._thread = None

    # ---- Inspection & seed helpers ----

    def ensure_bucket(self, bucket: str) -> None:
        with self._lock:
            self._buckets.setdefault(bucket, {})

    def put_raw(self, bucket: str, key: str, data: bytes) -> None:
        digest = hashlib.md5(data).hexdigest()
        with self._lock:
            self._buckets.setdefault(bucket, {})[key] = _StoredObject(
                data=bytes(data),
                etag=f'"{digest}"',
                mod_time=datetime.now(timezone.utc).replace(microsecond=0),
            )

    def get_raw(self, bucket: str, key: str) -> bytes | None:
        with self._lock:
            obj = self._buckets.get(bucket, {}).get(key)
            return obj.data if obj is not None else None

    def object_count(self, bucket: str, prefix: str = "") -> int:
        with self._lock:
            b = self._buckets.get(bucket, {})
            return sum(1 for k in b if k.startswith(prefix))

    def list_keys(self, bucket: str, prefix: str = "") -> list[str]:
        with self._lock:
            b = self._buckets.get(bucket, {})
            return sorted(k for k in b if k.startswith(prefix))

    def request_logs(self) -> list[S3RequestLog]:
        with self._lock:
            return list(self._requests)

    def clear_logs(self) -> None:
        with self._lock:
            self._requests.clear()

    def archive_requests(self) -> list[S3RequestLog]:
        return [r for r in self.request_logs() if "/archives" in r.path or "/api/v1/archives" in r.path]

    def sigv4_auth_header(self, region: str = "us-east-1") -> str:
        return (
            f"AWS4-HMAC-SHA256 Credential={self.access_key}/20261118/{region}/s3/aws4_request, "
            "SignedHeaders=host;x-amz-date, Signature=0000000000000000000000000000000000000000000000000000000000000000"
        )

    # ---- HTTP Wire Implementation ----

    def _read_body(self, handler: BaseHTTPRequestHandler) -> bytes:
        cl = handler.headers.get("Content-Length")
        if cl and cl.strip().isdigit():
            n = int(cl.strip())
            return handler.rfile.read(n) if n > 0 else b""
        return b""

    def _send_bytes(
        self,
        handler: BaseHTTPRequestHandler,
        status: int,
        body: bytes,
        *,
        content_type: str = "application/xml",
        extra_headers: dict[str, str] | None = None,
        head_only: bool = False,
        path: str = "",
        query: str = "",
    ) -> None:
        with self._lock:
            self._requests.append(S3RequestLog(method=handler.command, path=path, query=query, status=status))
        handler.send_response(status)
        if content_type:
            handler.send_header("Content-Type", content_type)
        if extra_headers:
            for k, v in extra_headers.items():
                handler.send_header(k, v)
        if "Content-Length" not in (extra_headers or {}):
            handler.send_header("Content-Length", str(len(body)))
        handler.send_header("Connection", "close")
        handler.end_headers()
        if not head_only and body:
            handler.wfile.write(body)
        handler.close_connection = True

    def _send_error(
        self,
        handler: BaseHTTPRequestHandler,
        status: int,
        code: str,
        message: str,
        *,
        path: str,
        query: str,
    ) -> None:
        xml_body = (
            '<?xml version="1.0" encoding="UTF-8"?>'
            f"<Error><Code>{xml_escape(code)}</Code><Message>{xml_escape(message)}</Message></Error>"
        ).encode("utf-8")
        self._send_bytes(
            handler,
            status,
            xml_body,
            head_only=(handler.command == "HEAD"),
            path=path,
            query=query,
        )

    def _handle(self, handler: BaseHTTPRequestHandler) -> None:
        split = urllib.parse.urlsplit(handler.path)
        raw_path = urllib.parse.unquote(split.path or "/")
        query_str = split.query or ""
        q = urllib.parse.parse_qs(query_str, keep_blank_values=True)
        body = self._read_body(handler)

        auth = handler.headers.get("Authorization") or ""
        expected_prefix = f"AWS4-HMAC-SHA256 Credential={self.access_key}/"
        if not auth.startswith(expected_prefix):
            self._send_error(handler, 403, "AccessDenied", "Invalid Access Key Id", path=raw_path, query=query_str)
            return

        trimmed = raw_path.lstrip("/")
        if "/" in trimmed:
            bucket, key = trimmed.split("/", 1)
        else:
            bucket, key = trimmed, ""

        if not bucket:
            self._send_error(handler, 400, "InvalidBucketName", "Missing bucket", path=raw_path, query=query_str)
            return

        with self._lock:
            bucket_exists = bucket in self._buckets
        if not bucket_exists:
            if handler.command == "PUT" and not key:
                self.ensure_bucket(bucket)
                self._send_bytes(handler, 200, b"", content_type="", path=raw_path, query=query_str)
                return
            self._send_error(
                handler,
                404,
                "NoSuchBucket",
                "The specified bucket does not exist",
                path=raw_path,
                query=query_str,
            )
            return

        if not key:
            if handler.command == "GET" and q.get("list-type", [""])[0] == "2":
                self._handle_list_v2(handler, bucket, q, raw_path, query_str)
                return
            if handler.command == "POST" and "delete" in q:
                self._handle_batch_delete(handler, bucket, body, raw_path, query_str)
                return
            if handler.command == "HEAD":
                self._send_bytes(handler, 200, b"", content_type="", head_only=True, path=raw_path, query=query_str)
                return
            self._send_error(handler, 405, "MethodNotAllowed", handler.command, path=raw_path, query=query_str)
            return

        method = handler.command
        if method == "HEAD":
            self._handle_get_or_head(handler, bucket, key, True, raw_path, query_str)
        elif method == "GET":
            self._handle_get_or_head(handler, bucket, key, False, raw_path, query_str)
        elif method == "PUT":
            upload_id = q.get("uploadId", [""])[0]
            if upload_id:
                self._handle_upload_part(handler, upload_id, q.get("partNumber", [""])[0], body, raw_path, query_str)
                return
            copy_src = handler.headers.get("X-Amz-Copy-Source") or ""
            if copy_src:
                self._handle_copy_object(handler, bucket, key, copy_src, raw_path, query_str)
                return
            self._handle_put_object(handler, bucket, key, body, raw_path, query_str)
        elif method == "POST":
            if "uploads" in q:
                self._handle_init_multipart(handler, bucket, key, raw_path, query_str)
                return
            upload_id = q.get("uploadId", [""])[0]
            if upload_id:
                self._handle_complete_multipart(handler, upload_id, body, raw_path, query_str)
                return
            self._send_error(handler, 400, "InvalidRequest", "Unsupported POST", path=raw_path, query=query_str)
        elif method == "DELETE":
            upload_id = q.get("uploadId", [""])[0]
            with self._lock:
                if upload_id:
                    self._uploads.pop(upload_id, None)
                else:
                    self._buckets[bucket].pop(key, None)
            self._send_bytes(handler, 204, b"", content_type="", path=raw_path, query=query_str)
        else:
            self._send_error(handler, 405, "MethodNotAllowed", method, path=raw_path, query=query_str)

    def _handle_list_v2(
        self,
        handler: BaseHTTPRequestHandler,
        bucket: str,
        q: dict[str, list[str]],
        raw_path: str,
        query_str: str,
    ) -> None:
        prefix = q.get("prefix", [""])[0]
        delimiter = q.get("delimiter", [""])[0]
        token_str = q.get("continuation-token", ["0"])[0]
        start_idx = int(token_str) if token_str.isdigit() else 0
        mk_str = q.get("max-keys", ["1000"])[0]
        max_keys = int(mk_str) if mk_str.isdigit() and int(mk_str) > 0 else 1000

        with self._lock:
            b = self._buckets[bucket]
            matching = sorted(k for k in b if k.startswith(prefix))
            seen_prefixes: set[str] = set()
            entries: list[tuple[bool, str, _StoredObject | None]] = []
            for k in matching:
                if delimiter:
                    rest = k[len(prefix) :]
                    idx = rest.find(delimiter)
                    if idx >= 0:
                        cp = prefix + rest[: idx + len(delimiter)]
                        if cp not in seen_prefixes:
                            seen_prefixes.add(cp)
                            entries.append((True, cp, None))
                        continue
                entries.append((False, k, b[k]))

        start_idx = min(start_idx, len(entries))
        end_idx = start_idx + max_keys
        truncated = end_idx < len(entries)
        next_token = str(end_idx) if truncated else ""
        page = entries[start_idx : min(end_idx, len(entries))]

        parts = [
            '<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>',
            f"<Name>{xml_escape(bucket)}</Name>",
            f"<Prefix>{xml_escape(prefix)}</Prefix>",
            f"<KeyCount>{len(page)}</KeyCount>",
            f"<MaxKeys>{max_keys}</MaxKeys>",
            f"<IsTruncated>{'true' if truncated else 'false'}</IsTruncated>",
        ]
        if next_token:
            parts.append(f"<NextContinuationToken>{xml_escape(next_token)}</NextContinuationToken>")
        for is_prefix, k, obj in page:
            if is_prefix:
                parts.append(f"<CommonPrefixes><Prefix>{xml_escape(k)}</Prefix></CommonPrefixes>")
            else:
                assert obj is not None
                ts = obj.mod_time.strftime("%Y-%m-%dT%H:%M:%SZ")
                parts.append(
                    f"<Contents><Key>{xml_escape(k)}</Key><LastModified>{ts}</LastModified>"
                    f"<ETag>{xml_escape(obj.etag)}</ETag><Size>{len(obj.data)}</Size></Contents>"
                )
        parts.append("</ListBucketResult>")
        self._send_bytes(handler, 200, "".join(parts).encode("utf-8"), path=raw_path, query=query_str)

    def _handle_get_or_head(
        self,
        handler: BaseHTTPRequestHandler,
        bucket: str,
        key: str,
        head_only: bool,
        raw_path: str,
        query_str: str,
    ) -> None:
        with self._lock:
            obj = self._buckets[bucket].get(key)
        if obj is None:
            self._send_error(
                handler,
                404,
                "NoSuchKey",
                "The specified key does not exist.",
                path=raw_path,
                query=query_str,
            )
            return

        base_hdrs = {
            "ETag": obj.etag,
            "Last-Modified": format_datetime(obj.mod_time, usegmt=True),
        }
        range_hdr = handler.headers.get("Range") or ""
        if not head_only and range_hdr.startswith("bytes=") and len(obj.data) > 0:
            spec = range_hdr[len("bytes=") :]
            start_s, _, end_s = spec.partition("-")
            if not start_s.isdigit():
                base_hdrs["Content-Range"] = f"bytes */{len(obj.data)}"
                self._send_bytes(handler, 416, b"", content_type="", extra_headers=base_hdrs, path=raw_path, query=query_str)
                return
            start = int(start_s)
            if start < 0 or start >= len(obj.data):
                base_hdrs["Content-Range"] = f"bytes */{len(obj.data)}"
                self._send_bytes(handler, 416, b"", content_type="", extra_headers=base_hdrs, path=raw_path, query=query_str)
                return
            end = len(obj.data) - 1
            if end_s.strip().isdigit():
                end = min(end, int(end_s.strip()))
            if end < start:
                self._send_bytes(handler, 416, b"", content_type="", extra_headers=base_hdrs, path=raw_path, query=query_str)
                return
            chunk = obj.data[start : end + 1]
            base_hdrs["Content-Range"] = f"bytes {start}-{end}/{len(obj.data)}"
            base_hdrs["Content-Length"] = str(len(chunk))
            self._send_bytes(
                handler,
                206,
                chunk,
                content_type="application/octet-stream",
                extra_headers=base_hdrs,
                path=raw_path,
                query=query_str,
            )
            return

        base_hdrs["Content-Length"] = str(len(obj.data))
        self._send_bytes(
            handler,
            200,
            obj.data,
            content_type="application/octet-stream",
            extra_headers=base_hdrs,
            head_only=head_only,
            path=raw_path,
            query=query_str,
        )

    def _handle_put_object(
        self,
        handler: BaseHTTPRequestHandler,
        bucket: str,
        key: str,
        body: bytes,
        raw_path: str,
        query_str: str,
    ) -> None:
        with self._lock:
            if handler.headers.get("If-None-Match") == "*" and key in self._buckets[bucket]:
                self._send_error(handler, 412, "PreconditionFailed", "Key already exists", path=raw_path, query=query_str)
                return
            etag = f'"{hashlib.md5(body).hexdigest()}"'
            self._buckets[bucket][key] = _StoredObject(
                data=body,
                etag=etag,
                mod_time=datetime.now(timezone.utc).replace(microsecond=0),
            )
        self._send_bytes(
            handler,
            200,
            b"",
            content_type="",
            extra_headers={"ETag": etag, "Content-Length": "0"},
            path=raw_path,
            query=query_str,
        )

    def _handle_copy_object(
        self,
        handler: BaseHTTPRequestHandler,
        dst_bucket: str,
        dst_key: str,
        raw_source: str,
        raw_path: str,
        query_str: str,
    ) -> None:
        unesc = urllib.parse.unquote(raw_source.lstrip("/"))
        if "/" not in unesc:
            self._send_error(handler, 400, "InvalidArgument", "Invalid copy source", path=raw_path, query=query_str)
            return
        src_bucket, src_key = unesc.split("/", 1)
        with self._lock:
            src_obj = self._buckets.get(src_bucket, {}).get(src_key)
            if src_obj is None:
                self._send_error(handler, 404, "NoSuchKey", "Source key does not exist", path=raw_path, query=query_str)
                return
            now = datetime.now(timezone.utc).replace(microsecond=0)
            cp = _StoredObject(data=bytes(src_obj.data), etag=src_obj.etag, mod_time=now)
            self._buckets[dst_bucket][dst_key] = cp
        xml_body = (
            '<?xml version="1.0" encoding="UTF-8"?>'
            f"<CopyObjectResult><LastModified>{now.strftime('%Y-%m-%dT%H:%M:%SZ')}</LastModified>"
            f"<ETag>{xml_escape(cp.etag)}</ETag></CopyObjectResult>"
        ).encode("utf-8")
        self._send_bytes(handler, 200, xml_body, path=raw_path, query=query_str)

    def _handle_init_multipart(
        self,
        handler: BaseHTTPRequestHandler,
        bucket: str,
        key: str,
        raw_path: str,
        query_str: str,
    ) -> None:
        with self._lock:
            self._upload_seq += 1
            upload_id = f"up-{self._upload_seq}"
            self._uploads[upload_id] = _MultipartUpload(bucket=bucket, key=key)
        xml_body = (
            '<?xml version="1.0" encoding="UTF-8"?>'
            f"<InitiateMultipartUploadResult><Bucket>{xml_escape(bucket)}</Bucket>"
            f"<Key>{xml_escape(key)}</Key><UploadId>{upload_id}</UploadId></InitiateMultipartUploadResult>"
        ).encode("utf-8")
        self._send_bytes(handler, 200, xml_body, path=raw_path, query=query_str)

    def _handle_upload_part(
        self,
        handler: BaseHTTPRequestHandler,
        upload_id: str,
        part_num_s: str,
        body: bytes,
        raw_path: str,
        query_str: str,
    ) -> None:
        if not part_num_s.isdigit() or int(part_num_s) < 1:
            self._send_error(handler, 400, "InvalidPart", "Bad part number", path=raw_path, query=query_str)
            return
        part_num = int(part_num_s)
        etag = f'"{hashlib.md5(body).hexdigest()}"'
        with self._lock:
            up = self._uploads.get(upload_id)
            if up is None:
                self._send_error(handler, 404, "NoSuchUpload", "Upload not found", path=raw_path, query=query_str)
                return
            up.parts[part_num] = body
        self._send_bytes(
            handler,
            200,
            b"",
            content_type="",
            extra_headers={"ETag": etag, "Content-Length": "0"},
            path=raw_path,
            query=query_str,
        )

    def _handle_complete_multipart(
        self,
        handler: BaseHTTPRequestHandler,
        upload_id: str,
        body: bytes,
        raw_path: str,
        query_str: str,
    ) -> None:
        try:
            root = ET.fromstring(body.decode("utf-8"))
        except Exception as exc:  # noqa: BLE001
            self._send_error(handler, 400, "MalformedXML", str(exc), path=raw_path, query=query_str)
            return
        part_nums: list[int] = []
        for elem in root.iter():
            if elem.tag.endswith("PartNumber") and elem.text and elem.text.strip().isdigit():
                part_nums.append(int(elem.text.strip()))

        with self._lock:
            up = self._uploads.get(upload_id)
            if up is None:
                self._send_error(handler, 404, "NoSuchUpload", "Upload not found", path=raw_path, query=query_str)
                return
            chunks: list[bytes] = []
            for pn in part_nums:
                if pn not in up.parts:
                    self._send_error(handler, 400, "InvalidPart", "Missing part", path=raw_path, query=query_str)
                    return
                chunks.append(up.parts[pn])
            merged = b"".join(chunks)
            etag = f'"{hashlib.md5(merged).hexdigest()}"'
            self._buckets[up.bucket][up.key] = _StoredObject(
                data=merged,
                etag=etag,
                mod_time=datetime.now(timezone.utc).replace(microsecond=0),
            )
            self._uploads.pop(upload_id, None)

        xml_body = (
            '<?xml version="1.0" encoding="UTF-8"?>'
            f"<CompleteMultipartUploadResult><ETag>{xml_escape(etag)}</ETag></CompleteMultipartUploadResult>"
        ).encode("utf-8")
        self._send_bytes(handler, 200, xml_body, path=raw_path, query=query_str)

    def _handle_batch_delete(
        self,
        handler: BaseHTTPRequestHandler,
        bucket: str,
        body: bytes,
        raw_path: str,
        query_str: str,
    ) -> None:
        try:
            root = ET.fromstring(body.decode("utf-8"))
        except Exception as exc:  # noqa: BLE001
            self._send_error(handler, 400, "MalformedXML", str(exc), path=raw_path, query=query_str)
            return
        keys: list[str] = []
        for elem in root.iter():
            if elem.tag.endswith("Key") and elem.text is not None:
                keys.append(elem.text)
        with self._lock:
            for k in keys:
                self._buckets[bucket].pop(k, None)
        xml_body = b'<?xml version="1.0" encoding="UTF-8"?><DeleteResult></DeleteResult>'
        self._send_bytes(handler, 200, xml_body, path=raw_path, query=query_str)
