"""Self-validation routine for --validate-harness (PostgreSQL 15, S3 mock, Spy proxy, Fixtures, Cleanup)."""

from __future__ import annotations

import glob
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import os
from pathlib import Path
import shutil
import tempfile
import threading
import time
import zipfile

from .fixtures import (
    make_png_bytes,
    make_rle_bomb_tar_zst,
    make_symlink_tar_zst,
    make_tar_zst,
    make_tarslip_tar_zst,
    make_voc_dataset_zip,
    make_yolo_dataset_zip,
    make_zipslip_zip,
    sha256_bytes,
)
from .http_client import HttpClient, enforce_no_proxy_env
from .postgres import EphemeralPostgres, allocate_free_port
from .s3_mock import S3MockServer
from .spy_proxy import HttpSpyProxy


def _pid_alive(pid: int | None) -> bool:
    if pid is None or pid <= 0:
        return False
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def run_harness_validation() -> dict[str, object]:
    """Boot real ephemeral PG15, S3 mock, HTTP spy proxy, test round-trips & faults, verify zero leaks."""
    enforce_no_proxy_env()
    before_dirs = set(glob.glob("/tmp/tm_e2e_validate_*"))
    val_root = Path(tempfile.mkdtemp(prefix="tm_e2e_validate_", dir="/tmp"))
    steps: list[dict[str, object]] = []

    pg: EphemeralPostgres | None = None
    s3: S3MockServer | None = None
    spy_s3: HttpSpyProxy | None = None
    echo_srv: ThreadingHTTPServer | None = None
    spy_echo: HttpSpyProxy | None = None
    pg_pid: int | None = None

    try:
        # 1. Ephemeral PostgreSQL 15 lifecycle & UTF-8 SQL round-trip
        t0 = time.monotonic()
        pg = EphemeralPostgres(val_root / "pg15")
        pg.start()
        pg_pid = pg.postmaster_pid()
        if not _pid_alive(pg_pid):
            raise AssertionError(f"Expected live postgres postmaster PID, got {pg_pid}")
        dbname, dsn = pg.create_db("harness_check")
        pg.exec_sql(
            dbname,
            """
            CREATE TABLE storage_check (
                id serial PRIMARY KEY,
                name text NOT NULL,
                kind text NOT NULL,
                supports_archive boolean NOT NULL
            );
            INSERT INTO storage_check (name, kind, supports_archive) VALUES
                ('机房存储容器A', 'docker', true),
                ('历史S3节点', 's3', false);
            """,
        )
        rows = pg.query_json_rows(dbname, "SELECT name, kind, supports_archive FROM storage_check ORDER BY id")
        assert len(rows) == 2, f"Expected 2 rows, got {rows}"
        assert rows[0]["name"] == "机房存储容器A" and rows[0]["supports_archive"] is True
        assert rows[1]["kind"] == "s3" and rows[1]["supports_archive"] is False
        steps.append(
            {
                "component": "postgresql_15",
                "ok": True,
                "port": pg.port,
                "dsn": dsn,
                "pid": pg_pid,
                "duration_ms": round((time.monotonic() - t0) * 1000, 1),
            }
        )

        # 2. S3MockServer fronted by HttpSpyProxy (SigV4, PUT, If-None-Match, Range GET, ListV2, Copy, Multipart, Delete)
        t1 = time.monotonic()
        s3 = S3MockServer(buckets=("tm-datasets",)).start()
        spy_s3 = HttpSpyProxy(s3.url, name="s3-spy").start()
        cli = HttpClient(spy_s3.url)

        unauth = cli.head("/tm-datasets")
        assert unauth.status == 403, f"Expected 403 without SigV4 header, got {unauth.status}"

        auth_hdr = {"Authorization": s3.sigv4_auth_header()}
        assert cli.head("/tm-datasets", headers=auth_hdr).status == 200

        png = make_png_bytes(16, 16, (10, 200, 90))
        put1 = cli.put(
            "/tm-datasets/prod/datasets/ds1/sample_01.png",
            raw_body=png,
            headers={**auth_hdr, "If-None-Match": "*"},
        )
        assert put1.status == 200
        put_dup = cli.put(
            "/tm-datasets/prod/datasets/ds1/sample_01.png",
            raw_body=png,
            headers={**auth_hdr, "If-None-Match": "*"},
        )
        assert put_dup.status == 412, f"Expected 412 PreconditionFailed, got {put_dup.status}"

        rng = cli.get(
            "/tm-datasets/prod/datasets/ds1/sample_01.png",
            headers={**auth_hdr, "Range": "bytes=0-7"},
        )
        assert rng.status == 206 and rng.body == b"\x89PNG\r\n\x1a\n"

        lst = cli.get(
            "/tm-datasets",
            query={"list-type": "2", "prefix": "prod/datasets/", "delimiter": "/"},
            headers=auth_hdr,
        )
        assert lst.status == 200 and "<Prefix>prod/datasets/ds1/</Prefix>" in lst.text

        cp = cli.put(
            "/tm-datasets/prod/datasets/ds1/copied_01.png",
            headers={**auth_hdr, "X-Amz-Copy-Source": "/tm-datasets/prod/datasets/ds1/sample_01.png"},
        )
        assert cp.status == 200 and s3.object_count("tm-datasets", "prod/datasets/ds1/") == 2

        init_mp = cli.post("/tm-datasets/prod/datasets/ds1/multi.bin?uploads", headers=auth_hdr)
        assert init_mp.status == 200 and "<UploadId>up-1</UploadId>" in init_mp.text
        assert (
            cli.put(
                "/tm-datasets/prod/datasets/ds1/multi.bin",
                query={"uploadId": "up-1", "partNumber": "1"},
                raw_body=b"partA-",
                headers=auth_hdr,
            ).status
            == 200
        )
        assert (
            cli.put(
                "/tm-datasets/prod/datasets/ds1/multi.bin",
                query={"uploadId": "up-1", "partNumber": "2"},
                raw_body=b"partB",
                headers=auth_hdr,
            ).status
            == 200
        )
        comp_xml = b"<CompleteMultipartUpload><Part><PartNumber>1</PartNumber></Part><Part><PartNumber>2</PartNumber></Part></CompleteMultipartUpload>"
        assert (
            cli.post(
                "/tm-datasets/prod/datasets/ds1/multi.bin",
                query={"uploadId": "up-1"},
                raw_body=comp_xml,
                headers=auth_hdr,
            ).status
            == 200
        )
        assert s3.get_raw("tm-datasets", "prod/datasets/ds1/multi.bin") == b"partA-partB"

        del_xml = b"<Delete><Object><Key>prod/datasets/ds1/copied_01.png</Key></Object></Delete>"
        assert cli.post("/tm-datasets?delete", raw_body=del_xml, headers=auth_hdr).status == 200
        assert s3.object_count("tm-datasets", "prod/datasets/ds1/") == 2
        assert len(s3.archive_requests()) == 0
        assert len(spy_s3.archive_requests()) == 0
        steps.append(
            {
                "component": "s3_mock_and_spy_proxy",
                "ok": True,
                "s3_url": s3.url,
                "spy_url": spy_s3.url,
                "recorded_requests": len(spy_s3.records()),
                "duration_ms": round((time.monotonic() - t1) * 1000, 1),
            }
        )

        # 3. HttpSpyProxy header inspection & fault injection (X-Archive-Sha256, X-Transfer-Mode, corrupt stream, subnet block)
        t2 = time.monotonic()
        echo_port = allocate_free_port("127.0.0.1")
        sample_zip = make_yolo_dataset_zip(count=3)
        sample_sha = sha256_bytes(sample_zip)

        class ArchiveEchoHandler(BaseHTTPRequestHandler):
            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                return

            def do_GET(self) -> None:
                self.send_response(200)
                self.send_header("Content-Type", "application/zip")
                self.send_header("Content-Length", str(len(sample_zip)))
                self.send_header("X-Archive-Sha256", sample_sha)
                self.send_header("X-Transfer-Mode", "archive")
                self.end_headers()
                self.wfile.write(sample_zip)

        echo_srv = ThreadingHTTPServer(("127.0.0.1", echo_port), ArchiveEchoHandler)
        echo_srv.daemon_threads = True
        threading.Thread(target=echo_srv.serve_forever, daemon=True).start()

        spy_echo = HttpSpyProxy(f"http://127.0.0.1:{echo_port}", name="echo-spy").start()
        echo_cli = HttpClient(spy_echo.url)
        dl_dest = val_root / "clean_download.zip"
        dl = echo_cli.stream_download("/api/v1/archives/arc-1", dl_dest)
        assert dl.status == 200
        assert dl.sha256 == sample_sha
        assert dl.headers.get("X-Archive-Sha256") == sample_sha
        assert dl.headers.get("X-Transfer-Mode") == "archive"
        assert len(spy_echo.archive_requests("GET")) == 1

        # Verify corrupt body fault-injection flips bytes while keeping X-Archive-Sha256 header
        spy_echo.set_corrupt_response_body("/api/v1/archives/", True)
        corrupt_dest = val_root / "corrupt_download.zip"
        dl_bad = echo_cli.stream_download("/api/v1/archives/arc-1", corrupt_dest)
        assert dl_bad.status == 200
        assert dl_bad.headers.get("X-Archive-Sha256") == sample_sha
        assert dl_bad.sha256 != sample_sha, "Expected tampered SHA-256 mismatch under fault injection"

        # Verify subnet isolation block returns 502
        spy_echo.set_blocked(True)
        assert echo_cli.get("/api/v1/archives/arc-1").status == 502
        steps.append(
            {
                "component": "spy_header_and_fault_injection",
                "ok": True,
                "duration_ms": round((time.monotonic() - t2) * 1000, 1),
            }
        )

        # 4. Pure-Python VOC/YOLO/Zstd/Adversarial Archive Fixtures
        t3 = time.monotonic()
        voc_zip = make_voc_dataset_zip(count=2, subdir="车间A")
        with zipfile.ZipFile(io.BytesIO(voc_zip), "r") as zf:
            names = zf.namelist()
            assert "车间A/sample_001.png" in names and "车间A/sample_001.xml" in names
        zst_valid = make_tar_zst([("a/hello.txt", b"hello", 1700000000.123456)])
        assert zst_valid.startswith(b"\x28\xb5\x2f\xfd")
        assert make_tarslip_tar_zst().startswith(b"\x28\xb5\x2f\xfd")
        assert make_symlink_tar_zst().startswith(b"\x28\xb5\x2f\xfd")
        bomb = make_rle_bomb_tar_zst(uncompressed_mb=4)
        assert bomb.startswith(b"\x28\xb5\x2f\xfd") and len(bomb) < 100_000
        assert len(make_zipslip_zip()) > 50
        steps.append(
            {
                "component": "archive_and_dataset_fixtures",
                "ok": True,
                "rle_bomb_compressed_bytes": len(bomb),
                "duration_ms": round((time.monotonic() - t3) * 1000, 1),
            }
        )
    finally:
        if spy_echo is not None:
            spy_echo.stop()
        if echo_srv is not None:
            echo_srv.shutdown()
            echo_srv.server_close()
        if spy_s3 is not None:
            spy_s3.stop()
        if s3 is not None:
            s3.stop()
        if pg is not None:
            pg.stop()
        shutil.rmtree(val_root, ignore_errors=True)

    # 5. Post-teardown zero-leak verification
    deadline = time.monotonic() + 3.0
    while time.monotonic() < deadline and _pid_alive(pg_pid):
        time.sleep(0.05)
    if _pid_alive(pg_pid):
        raise AssertionError(f"PostgreSQL postmaster process {pg_pid} leaked after teardown!")
    after_dirs = set(glob.glob("/tmp/tm_e2e_validate_*"))
    leaked_dirs = sorted(after_dirs - before_dirs)
    if leaked_dirs or val_root.exists():
        raise AssertionError(f"Leaked /tmp scratch directories after teardown: {leaked_dirs}")

    steps.append({"component": "zero_leak_teardown", "ok": True, "leaked_dirs": 0, "pg_pid_terminated": True})
    return {"ok": True, "steps": steps}
