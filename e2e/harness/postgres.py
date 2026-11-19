"""Ephemeral PostgreSQL 15 manager running under isolated /tmp/tm_e2e_* scratch dirs."""

from __future__ import annotations

import atexit
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import time
from typing import Any
import urllib.parse

from .http_client import enforce_no_proxy_env

PG_BIN_DIR = Path("/usr/lib/postgresql/15/bin")
INITDB_BIN = PG_BIN_DIR / "initdb"
POSTGRES_BIN = PG_BIN_DIR / "postgres"
PSQL_BIN = Path("/usr/bin/psql")


def allocate_free_port(host: str = "127.0.0.1") -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind((host, 0))
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        return int(sock.getsockname()[1])


class EphemeralPostgres:
    """Manages an isolated PostgreSQL 15 instance as a direct reaped child process."""

    def __init__(self, root_dir: Path, *, superuser: str = "postgres") -> None:
        self.root_dir = root_dir
        self.pgdata = root_dir / "pgdata"
        self.sock_dir = root_dir / "pgsock"
        self.log_file = root_dir / "postgres.log"
        self.superuser = superuser
        self.host = "127.0.0.1"
        self.port = allocate_free_port(self.host)
        self._proc: subprocess.Popen[bytes] | None = None
        self._log_fh: Any = None
        self._db_seq = 0
        atexit.register(self.stop)

    def _clean_env(self) -> dict[str, str]:
        env = enforce_no_proxy_env(dict(os.environ))
        env["LC_ALL"] = "C"
        env["LANG"] = "C"
        return env

    def start(self) -> None:
        if self._proc is not None and self._proc.poll() is None:
            return
        if not INITDB_BIN.is_file() or not POSTGRES_BIN.is_file():
            raise RuntimeError(f"PostgreSQL 15 binaries not found under {PG_BIN_DIR}")

        self.pgdata.mkdir(parents=True, exist_ok=True)
        self.sock_dir.mkdir(parents=True, exist_ok=True)
        os.chmod(self.pgdata, 0o700)
        os.chmod(self.sock_dir, 0o700)

        subprocess.run(
            [
                str(INITDB_BIN),
                "-D",
                str(self.pgdata),
                "-U",
                self.superuser,
                "--auth=trust",
                "--encoding=UTF8",
                "--no-locale",
            ],
            env=self._clean_env(),
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )

        conf_extra = (
            f"\nlisten_addresses = '{self.host}'\n"
            f"port = {self.port}\n"
            f"unix_socket_directories = '{self.sock_dir}'\n"
            "fsync = off\n"
            "synchronous_commit = off\n"
            "full_page_writes = off\n"
            "max_connections = 120\n"
            "shared_buffers = 32MB\n"
        )
        with open(self.pgdata / "postgresql.conf", "a", encoding="utf-8") as fh:
            fh.write(conf_extra)

        self._log_fh = open(self.log_file, "ab")
        self._proc = subprocess.Popen(
            [str(POSTGRES_BIN), "-D", str(self.pgdata)],
            env=self._clean_env(),
            stdout=self._log_fh,
            stderr=subprocess.STDOUT,
        )
        self._wait_ready()

    def _wait_ready(self, timeout: float = 12.0) -> None:
        deadline = time.monotonic() + timeout
        last_err = ""
        while time.monotonic() < deadline:
            if self._proc is not None and self._proc.poll() is not None:
                raise RuntimeError(f"postgres exited prematurely with code {self._proc.returncode}")
            try:
                out = self.query_scalar("postgres", "SELECT 1;")
                if out.strip() == "1":
                    return
            except Exception as exc:  # noqa: BLE001
                last_err = str(exc)
            time.sleep(0.08)
        raise RuntimeError(f"Ephemeral PostgreSQL failed readiness check on {self.host}:{self.port}: {last_err}")

    def dsn(self, dbname: str = "postgres") -> str:
        user = urllib.parse.quote(self.superuser, safe="")
        db = urllib.parse.quote(dbname, safe="")
        return f"postgres://{user}@{self.host}:{self.port}/{db}?sslmode=disable"

    def create_db(self, prefix: str = "tm_e2e") -> tuple[str, str]:
        self._db_seq += 1
        dbname = f"{prefix}_{self._db_seq}_{int(time.time() * 1000) % 100000}"
        self.exec_sql("postgres", f'CREATE DATABASE "{dbname}" ENCODING \'UTF8\';')
        return dbname, self.dsn(dbname)

    def exec_sql(self, dbname: str, sql: str) -> str:
        res = subprocess.run(
            [
                str(PSQL_BIN),
                "--no-psqlrc",
                "-v",
                "ON_ERROR_STOP=1",
                "-h",
                self.host,
                "-p",
                str(self.port),
                "-U",
                self.superuser,
                "-d",
                dbname,
                "-t",
                "-A",
                "-c",
                sql,
            ],
            env=self._clean_env(),
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        return res.stdout

    def query_scalar(self, dbname: str, sql: str) -> str:
        return self.exec_sql(dbname, sql).strip()

    def query_json_rows(self, dbname: str, sql_select: str) -> list[dict[str, object]]:
        import json

        wrapped = f"SELECT COALESCE(json_agg(t), '[]'::json)::text FROM ({sql_select.rstrip(';')}) t;"
        raw = self.query_scalar(dbname, wrapped)
        return json.loads(raw) if raw else []

    def postmaster_pid(self) -> int | None:
        if self._proc is not None:
            return self._proc.pid
        return None

    def stop(self) -> None:
        proc = self._proc
        self._proc = None
        if proc is not None:
            if proc.poll() is None:
                try:
                    # SIGINT triggers PostgreSQL Fast Shutdown; SIGQUIT triggers Immediate Shutdown
                    proc.send_signal(signal.SIGINT)
                    proc.wait(timeout=6.0)
                except subprocess.TimeoutExpired:
                    try:
                        proc.send_signal(signal.SIGQUIT)
                        proc.wait(timeout=3.0)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait(timeout=2.0)
            else:
                proc.wait(timeout=1.0)
        if self._log_fh is not None:
            try:
                self._log_fh.close()
            except Exception:  # noqa: BLE001
                pass
            self._log_fh = None
        shutil.rmtree(self.sock_dir, ignore_errors=True)
