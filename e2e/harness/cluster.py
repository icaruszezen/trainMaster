"""Multi-daemon runtime cluster manager for trainmaster, trainmaster-storage, and trainer."""

from __future__ import annotations

import atexit
from dataclasses import dataclass
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from typing import Any, Mapping

from .fixtures import make_voc_dataset_zip
from .http_client import HttpClient, HttpResponse, enforce_no_proxy_env
from .postgres import EphemeralPostgres, allocate_free_port
from .s3_mock import S3MockServer
from .spy_proxy import HttpSpyProxy

REPO_ROOT = Path("/home/antigravity/icaruszezen/trainMaster")
BACKEND_DIR = REPO_ROOT / "backend"
FRONTEND_DIR = REPO_ROOT / "frontend"
TRAINER_DIR = REPO_ROOT / "trainer"
DEPLOY_DIR = REPO_ROOT / "deploy"
FAKE_YOLO_WORKER = Path(__file__).resolve().parent / "fake_yolo_worker.py"


def _terminate_proc(proc: subprocess.Popen[Any] | None) -> None:
    if proc is None or proc.poll() is not None:
        return
    try:
        proc.send_signal(signal.SIGTERM)
        proc.wait(timeout=5.0)
    except Exception:  # noqa: BLE001
        try:
            proc.kill()
            proc.wait(timeout=3.0)
        except Exception:  # noqa: BLE001
            pass


class StorageDaemon:
    """Real trainmaster-storage OS process optionally fronted by HttpSpyProxy."""

    def __init__(
        self,
        *,
        binary: Path,
        work_dir: Path,
        name: str = "storage",
        token: str = "tm-storage-secret-token-0123456789",
        temp_ttl: str = "2h",
        with_spy: bool = True,
        extra_env: Mapping[str, str] | None = None,
    ) -> None:
        self.binary = binary
        self.work_dir = work_dir
        self.data_dir = work_dir / "data"
        self.log_path = work_dir / f"{name}.log"
        self.name = name
        self.token = token
        self.temp_ttl = temp_ttl
        self.host = "127.0.0.1"
        self.port = allocate_free_port(self.host)
        self.direct_url = f"http://{self.host}:{self.port}"
        self.spy: HttpSpyProxy | None = HttpSpyProxy(self.direct_url, name=name) if with_spy else None
        self.extra_env = dict(extra_env or {})
        self._proc: subprocess.Popen[bytes] | None = None
        self._log_fh: Any = None
        atexit.register(self.stop)

    @property
    def url(self) -> str:
        return self.spy.url if self.spy is not None else self.direct_url

    def client(self, *, token: str | None = "DEFAULT") -> HttpClient:
        tok = (self.token or None) if token == "DEFAULT" else token
        return HttpClient(self.url, bearer_token=tok)

    def direct_client(self, *, token: str | None = "DEFAULT") -> HttpClient:
        tok = (self.token or None) if token == "DEFAULT" else token
        return HttpClient(self.direct_url, bearer_token=tok)

    def start(self) -> StorageDaemon:
        if self._proc is not None and self._proc.poll() is None:
            return self
        self.work_dir.mkdir(parents=True, exist_ok=True)
        self.data_dir.mkdir(parents=True, exist_ok=True)
        if self.spy is not None:
            self.spy.start()

        env = enforce_no_proxy_env(dict(os.environ))
        env["TM_STORAGE_ADDR"] = f"{self.host}:{self.port}"
        env["TM_STORAGE_DATA_DIR"] = str(self.data_dir)
        if self.token:
            env["TM_STORAGE_TOKEN"] = self.token
        else:
            env.pop("TM_STORAGE_TOKEN", None)
        env["TM_STORAGE_TEMP_TTL"] = self.temp_ttl
        env.update(self.extra_env)

        self._log_fh = open(self.log_path, "ab")
        self._proc = subprocess.Popen(
            [str(self.binary)],
            cwd=str(self.work_dir),
            env=env,
            stdout=self._log_fh,
            stderr=subprocess.STDOUT,
        )
        self._wait_healthy()
        return self

    def _wait_healthy(self, timeout: float = 12.0) -> None:
        cli = HttpClient(self.direct_url, timeout=2.0)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self._proc is not None and self._proc.poll() is not None:
                logs = self.read_logs()
                raise RuntimeError(
                    f"trainmaster-storage ({self.name}) exited early (rc={self._proc.returncode}):\n{logs}"
                )
            try:
                resp = cli.get("/healthz")
                if resp.status == 200:
                    return
            except Exception:  # noqa: BLE001
                pass
            time.sleep(0.08)
        raise RuntimeError(f"trainmaster-storage ({self.name}) timed out on /healthz:\n{self.read_logs()}")

    def read_logs(self) -> str:
        if not self.log_path.is_file():
            return ""
        return self.log_path.read_text(encoding="utf-8", errors="replace")

    def stop(self) -> None:
        _terminate_proc(self._proc)
        self._proc = None
        if self._log_fh is not None:
            try:
                self._log_fh.close()
            except Exception:  # noqa: BLE001
                pass
            self._log_fh = None
        if self.spy is not None:
            self.spy.stop()


class PlatformDaemon:
    """Real trainmaster platform server backed by an isolated PostgreSQL database."""

    def __init__(
        self,
        *,
        binary: Path,
        work_dir: Path,
        db_name: str,
        database_url: str,
        name: str = "app",
        secret_key: str = "tm-e2e-master-secret-key-0123456789",
        node_token: str = "",
        storage_service_url: str = "",
        storage_service_token: str = "",
        web_dir: Path | None = None,
        extra_env: Mapping[str, str] | None = None,
    ) -> None:
        self.binary = binary
        self.work_dir = work_dir
        self.data_dir = work_dir / "data"
        self.log_path = work_dir / f"{name}.log"
        self.db_name = db_name
        self.database_url = database_url
        self.name = name
        self.secret_key = secret_key
        self.node_token = node_token
        self.storage_service_url = storage_service_url
        self.storage_service_token = storage_service_token
        self.web_dir = web_dir
        self.extra_env = dict(extra_env or {})
        self.host = "127.0.0.1"
        self.port = allocate_free_port(self.host)
        self.url = f"http://{self.host}:{self.port}"
        self._proc: subprocess.Popen[bytes] | None = None
        self._log_fh: Any = None
        atexit.register(self.stop)

    def client(self, *, bearer_token: str | None = None) -> HttpClient:
        return HttpClient(self.url, bearer_token=bearer_token)

    def agent_client(self, *, node_key: str | None = None, token: str | None = "DEFAULT") -> HttpClient:
        eff_tok = (self.node_token or None) if token == "DEFAULT" else token
        hdrs = {"X-Node-Key": node_key} if node_key else {}
        return HttpClient(self.url, bearer_token=eff_tok, default_headers=hdrs)

    def start(self) -> PlatformDaemon:
        if self._proc is not None and self._proc.poll() is None:
            return self
        self.work_dir.mkdir(parents=True, exist_ok=True)
        self.data_dir.mkdir(parents=True, exist_ok=True)

        env = enforce_no_proxy_env(dict(os.environ))
        env["TM_HTTP_ADDR"] = f"{self.host}:{self.port}"
        env["TM_DATA_DIR"] = str(self.data_dir)
        env["TM_DATABASE_URL"] = self.database_url
        env["TM_DB_AUTO_MIGRATE"] = "true"
        env["TM_SECRET_KEY"] = self.secret_key
        env["TM_LOG_LEVEL"] = "info"
        if self.node_token:
            env["TM_NODE_TOKEN"] = self.node_token
        else:
            env.pop("TM_NODE_TOKEN", None)
        if self.storage_service_url:
            env["TM_STORAGE_SERVICE_URL"] = self.storage_service_url
        else:
            env.pop("TM_STORAGE_SERVICE_URL", None)
        if self.storage_service_token:
            env["TM_STORAGE_SERVICE_TOKEN"] = self.storage_service_token
        else:
            env.pop("TM_STORAGE_SERVICE_TOKEN", None)
        if self.web_dir is not None and self.web_dir.is_dir():
            env["TM_WEB_DIR"] = str(self.web_dir)
        else:
            env.pop("TM_WEB_DIR", None)
        env.update(self.extra_env)

        self._log_fh = open(self.log_path, "ab")
        self._proc = subprocess.Popen(
            [str(self.binary)],
            cwd=str(self.work_dir),
            env=env,
            stdout=self._log_fh,
            stderr=subprocess.STDOUT,
        )
        self._wait_ready()
        return self

    def _wait_ready(self, timeout: float = 15.0) -> None:
        cli = HttpClient(self.url, timeout=2.0)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self._proc is not None and self._proc.poll() is not None:
                raise RuntimeError(
                    f"trainmaster ({self.name}) exited early (rc={self._proc.returncode}):\n{self.read_logs()}"
                )
            try:
                resp = cli.get("/api/readyz")
                if resp.status == 200:
                    return
            except Exception:  # noqa: BLE001
                pass
            time.sleep(0.1)
        raise RuntimeError(f"trainmaster ({self.name}) timed out on /api/readyz:\n{self.read_logs()}")

    def read_logs(self) -> str:
        if not self.log_path.is_file():
            return ""
        return self.log_path.read_text(encoding="utf-8", errors="replace")

    def stop(self) -> None:
        _terminate_proc(self._proc)
        self._proc = None
        if self._log_fh is not None:
            try:
                self._log_fh.close()
            except Exception:  # noqa: BLE001
                pass
            self._log_fh = None

    # ---- High-level REST workflow helpers ----

    def test_docker_node(
        self,
        endpoint: str,
        token: str = "",
        *,
        insecure_skip_verify: bool = False,
        node_id: int | None = None,
    ) -> HttpResponse:
        cfg = {"endpoint": endpoint, "insecure_skip_verify": insecure_skip_verify}
        body: dict[str, Any] = {
            "kind": "docker",
            "docker": cfg,
            "config": cfg,
            "token": token,
        }
        if node_id is not None:
            body["node_id"] = node_id
        return self.client().post("/api/v1/storage/nodes/test", json_body=body)

    def create_docker_node(
        self,
        name: str,
        endpoint: str,
        token: str = "",
        *,
        insecure_skip_verify: bool = False,
        activate: bool = False,
    ) -> HttpResponse:
        cfg = {"endpoint": endpoint, "insecure_skip_verify": insecure_skip_verify}
        body: dict[str, Any] = {
            "name": name,
            "kind": "docker",
            "docker": cfg,
            "config": cfg,
            "token": token,
            "activate": activate,
        }
        return self.client().post("/api/v1/storage/nodes", json_body=body)

    def update_docker_node(
        self,
        node_id: int,
        *,
        name: str | None = None,
        endpoint: str | None = None,
        token: str | None = None,
        insecure_skip_verify: bool | None = None,
        clear_secret: bool = False,
    ) -> HttpResponse:
        body: dict[str, Any] = {}
        if name is not None:
            body["name"] = name
        if endpoint is not None or insecure_skip_verify is not None:
            cfg: dict[str, Any] = {}
            if endpoint is not None:
                cfg["endpoint"] = endpoint
            if insecure_skip_verify is not None:
                cfg["insecure_skip_verify"] = insecure_skip_verify
            body["docker"] = cfg
            body["config"] = cfg
        if token is not None:
            body["token"] = token
        if clear_secret:
            body["clear_secret"] = True
        return self.client().patch(f"/api/v1/storage/nodes/{node_id}", json_body=body)

    def create_s3_node(
        self,
        name: str,
        s3_srv: S3MockServer,
        *,
        bucket: str = "tm-datasets",
        prefix: str = "prod",
        activate: bool = False,
    ) -> HttpResponse:
        s3_cfg = {
            "endpoint": s3_srv.url,
            "region": "us-east-1",
            "bucket": bucket,
            "prefix": prefix,
            "access_key_id": s3_srv.access_key,
            "force_path_style": True,
            "insecure_skip_verify": False,
        }
        return self.client().post(
            "/api/v1/storage/nodes",
            json_body={
                "name": name,
                "kind": "s3",
                "s3": s3_cfg,
                "secret_access_key": s3_srv.secret_key,
                "activate": activate,
            },
        )

    def create_passive_local_node(
        self,
        name: str,
        local_path: Path,
        *,
        activate: bool = False,
    ) -> HttpResponse:
        local_path.mkdir(parents=True, exist_ok=True)
        return self.client().post(
            "/api/v1/storage/nodes",
            json_body={
                "name": name,
                "kind": "local",
                "local": {"path": str(local_path)},
                "activate": activate,
            },
        )

    def import_voc_dataset(
        self,
        name: str,
        *,
        count: int = 4,
        zip_bytes: bytes | None = None,
        on_conflict: str = "skip",
        dataset_id: int | None = None,
        timeout: float = 20.0,
    ) -> dict[str, Any]:
        cli = self.client()
        if dataset_id is None:
            ds_resp = cli.post("/api/v1/datasets", json_body={"name": name})
            assert ds_resp.status == 201, f"create dataset failed ({ds_resp.status}): {ds_resp.text}"
            ds_id = int(ds_resp.json()["id"])
        else:
            ds_id = dataset_id

        archive = make_voc_dataset_zip(count=count) if zip_bytes is None else zip_bytes
        imp_resp = cli.post(
            f"/api/v1/datasets/{ds_id}/imports",
            json_body={
                "source": "archive",
                "file_name": "batch.zip",
                "file_size": len(archive),
                "on_conflict": on_conflict,
                "annotation_format": "voc",
            },
        )
        assert imp_resp.status == 201, f"create import failed ({imp_resp.status}): {imp_resp.text}"
        imp_id = int(imp_resp.json()["id"])

        up_resp = cli.put(
            f"/api/v1/datasets/{ds_id}/imports/{imp_id}/archive",
            query={"offset": 0},
            raw_body=archive,
            headers={"Content-Type": "application/octet-stream"},
        )
        assert up_resp.status == 200, f"upload archive failed ({up_resp.status}): {up_resp.text}"

        com_resp = cli.post(f"/api/v1/datasets/{ds_id}/imports/{imp_id}/commit")
        assert com_resp.status == 202, f"commit import failed ({com_resp.status}): {com_resp.text}"

        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            st_resp = cli.get(f"/api/v1/datasets/{ds_id}/imports/{imp_id}")
            assert st_resp.status == 200
            cur = st_resp.json()
            if cur["status"] == "succeeded":
                break
            if cur["status"] in ("failed", "canceled"):
                raise AssertionError(f"dataset import {imp_id} {cur['status']}: {cur.get('error')}")
            time.sleep(0.05)
        else:
            raise TimeoutError(f"dataset import {imp_id} timed out")

        final_ds = cli.get(f"/api/v1/datasets/{ds_id}")
        assert final_ds.status == 200
        return final_ds.json()

    def create_project_and_freeze_version(
        self,
        project_name: str,
        dataset_ids: list[int],
        *,
        class_name: str = "scratch",
        note: str = "e2e-freeze",
        timeout: float = 25.0,
    ) -> tuple[dict[str, Any], dict[str, Any]]:
        cli = self.client()
        p_resp = cli.post(
            "/api/v1/projects",
            json_body={"name": project_name, "dataset_ids": dataset_ids},
        )
        assert p_resp.status == 201, f"create project failed ({p_resp.status}): {p_resp.text}"
        proj = p_resp.json()
        proj_id = int(proj["id"])

        c_resp = cli.put(
            f"/api/v1/projects/{proj_id}/classes",
            json_body={"classes": [{"name": class_name, "color": "#ef4444"}]},
        )
        assert c_resp.status == 200, f"set classes failed ({c_resp.status}): {c_resp.text}"
        cls_ver = c_resp.json()["version"]

        dv_resp = cli.post(
            "/api/v1/data-versions",
            json_body={
                "project_id": proj_id,
                "note": note,
                "class_version": cls_ver,
                "sources": [
                    {"dataset_id": d_id, "include_base": True, "version_ids": []}
                    for d_id in dataset_ids
                ],
                "split": {
                    "train": 80,
                    "val": 20,
                    "test": 0,
                    "seed": 42,
                    "use_folders": True,
                    "group_by": "stem",
                },
                "scope": "annotated",
                "include_empty": True,
                "ignore_labels": [],
                "augmentation": None,
            },
        )
        assert dv_resp.status == 201, f"create data version failed ({dv_resp.status}): {dv_resp.text}"
        dv = dv_resp.json()
        dv_id = int(dv["id"])

        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            cur_resp = cli.get(f"/api/v1/data-versions/{dv_id}")
            assert cur_resp.status == 200
            dv = cur_resp.json()
            if dv["status"] == "ready":
                return proj, dv
            if dv["status"] in ("failed", "canceled"):
                raise AssertionError(f"data_version {dv_id} {dv['status']}: {dv.get('error')}")
            time.sleep(0.05)
        raise TimeoutError(f"data_version {dv_id} did not reach ready in {timeout}s")

    def wait_migrations(
        self,
        migration_ids: list[int],
        *,
        timeout: float = 25.0,
    ) -> tuple[dict[int, dict[str, Any]], set[str]]:
        """Poll /api/v1/storage/migrations until all given IDs reach a terminal state."""
        cli = self.client()
        wanted = set(migration_ids)
        seen_stages: set[str] = set()
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            resp = cli.get("/api/v1/storage/migrations")
            assert resp.status == 200
            items = {int(m["id"]): m for m in resp.json().get("items", []) if int(m["id"]) in wanted}
            for m in items.values():
                stg = m.get("stage")
                if isinstance(stg, str) and stg:
                    seen_stages.add(stg)
            if len(items) == len(wanted) and all(
                m["status"] in ("succeeded", "failed", "canceled") for m in items.values()
            ):
                return items, seen_stages
            time.sleep(0.03)
        raise TimeoutError(f"migrations {migration_ids} timed out after {timeout}s")

    def register_compute_node(
        self,
        node_key: str = "11111111-2222-3333-4444-555555555555",
        name: str = "E2E-Trainer-01",
    ) -> dict[str, Any]:
        ac = self.agent_client(node_key=node_key)
        resp = ac.post(
            "/api/v1/compute/agent/register",
            json_body={
                "node_key": node_key,
                "name": name,
                "hostname": "e2e-trainer-host",
                "agent_version": "0.6.0",
                "hardware": {
                    "in_container": False,
                    "cpu": {"logical_cores": 8},
                    "memory": {"total_bytes": 16 << 30},
                    "gpus": [],
                    "gpu_detection": {"method": "none"},
                },
            },
        )
        assert resp.status == 200, f"register compute node failed ({resp.status}): {resp.text}"
        return resp.json()


@dataclass
class TrainerRunResult:
    outcome: str
    claimed_task: dict[str, Any]
    audit: dict[str, Any] | None
    job_detail: dict[str, Any]
    job_logs: str
    stdout: str
    stderr: str


def run_trainer_job_in_subprocess(
    *,
    platform_url: str,
    task_payload: dict[str, Any],
    state_dir: Path,
    node_key: str = "11111111-2222-3333-4444-555555555555",
    node_token: str = "",
    data_dir: Path | None = None,
    fake_free_disk_bytes: int | None = None,
) -> tuple[str, dict[str, Any] | None, str, str]:
    """Invoke the real trainmaster_trainer TrainingJobTask in an isolated Python subprocess."""
    state_dir.mkdir(parents=True, exist_ok=True)
    task_json_file = state_dir / "claimed_task.json"
    audit_file = state_dir / "trainer_audit.json"
    outcome_file = state_dir / "trainer_outcome.txt"
    task_json_file.write_text(json.dumps(task_payload, ensure_ascii=False), encoding="utf-8")
    if audit_file.exists():
        audit_file.unlink()

    driver_code = """
import json, os, shutil, sys, urllib.request
from pathlib import Path
from collections import namedtuple
from trainmaster_trainer.client import Client
from trainmaster_trainer.training import DatasetCacheManager, TrainingJobTask

task_data = json.loads(Path(os.environ["TM_E2E_TASK_FILE"]).read_text(encoding="utf-8"))
state_dir = Path(os.environ["TM_E2E_STATE_DIR"])
data_dir_raw = os.environ.get("TM_E2E_DATA_DIR") or ""
data_dir = Path(data_dir_raw) if data_dir_raw else None
fake_free = os.environ.get("TM_E2E_FAKE_FREE_BYTES")
if fake_free:
    orig_usage = shutil.disk_usage
    FakeUsage = namedtuple("usage", ["total", "used", "free"])
    val = int(fake_free)
    shutil.disk_usage = lambda p: FakeUsage(max(val * 2, 10 << 30), 1 << 30, val)

opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
client = Client(
    os.environ["TM_E2E_PLATFORM_URL"],
    token=os.environ.get("TM_E2E_NODE_TOKEN") or None,
    opener=opener,
)
cache = DatasetCacheManager(state_dir)
runner = TrainingJobTask(
    task_data,
    client=client,
    node_key=os.environ["TM_E2E_NODE_KEY"],
    state_dir=state_dir,
    node_name="E2E-Trainer-01",
    data_dir=data_dir,
    dataset_cache=cache,
    command=[
        sys.executable,
        os.environ["TM_E2E_FAKE_YOLO"],
        "--audit-file",
        os.environ["TM_E2E_AUDIT_FILE"],
    ],
    progress_interval=0.2,
)
res = runner.run()
Path(os.environ["TM_E2E_OUTCOME_FILE"]).write_text(res, encoding="utf-8")
"""
    env = enforce_no_proxy_env(dict(os.environ))
    py_path = str(TRAINER_DIR)
    if env.get("PYTHONPATH"):
        py_path = f"{py_path}:{env['PYTHONPATH']}"
    env["PYTHONPATH"] = py_path
    env["TM_E2E_TASK_FILE"] = str(task_json_file)
    env["TM_E2E_STATE_DIR"] = str(state_dir)
    env["TM_E2E_OUTCOME_FILE"] = str(outcome_file)
    env["TM_E2E_AUDIT_FILE"] = str(audit_file)
    env["TM_E2E_TRAINER_AUDIT_FILE"] = str(audit_file)
    env["TM_E2E_PLATFORM_URL"] = platform_url
    env["TM_E2E_NODE_KEY"] = node_key
    env["TM_E2E_NODE_TOKEN"] = node_token
    env["TM_E2E_DATA_DIR"] = str(data_dir) if data_dir is not None else ""
    env["TM_E2E_FAKE_YOLO"] = str(FAKE_YOLO_WORKER)
    if fake_free_disk_bytes is not None:
        env["TM_E2E_FAKE_FREE_BYTES"] = str(fake_free_disk_bytes)
    else:
        env.pop("TM_E2E_FAKE_FREE_BYTES", None)

    proc = subprocess.run(
        [sys.executable, "-c", driver_code],
        cwd=str(state_dir),
        env=env,
        capture_output=True,
        text=True,
        timeout=45.0,
        check=False,
    )
    if proc.returncode != 0 and not outcome_file.is_file():
        raise RuntimeError(
            f"Trainer subprocess crashed (rc={proc.returncode}):\nSTDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
        )
    outcome = outcome_file.read_text(encoding="utf-8").strip()
    audit = json.loads(audit_file.read_text(encoding="utf-8")) if audit_file.is_file() else None
    return outcome, audit, proc.stdout, proc.stderr


class E2ESession:
    """Shared session context managing compiled binaries, PG15 cluster, and per-test clean dirs."""

    def __init__(self) -> None:
        self.root_dir = Path(tempfile.mkdtemp(prefix="tm_e2e_session_", dir="/tmp"))
        self.bin_dir = self.root_dir / "bin"
        self.cases_dir = self.root_dir / "cases"
        self.bin_dir.mkdir(parents=True, exist_ok=True)
        self.cases_dir.mkdir(parents=True, exist_ok=True)

        self._pg: EphemeralPostgres | None = None
        self._trainmaster_bin: Path | None = None
        self._storage_bin: Path | None = None
        self._web_bundle_dir: Path | None = None
        self._closed = False
        atexit.register(self.close)

    @property
    def pg(self) -> EphemeralPostgres:
        if self._pg is None:
            self._pg = EphemeralPostgres(self.root_dir / "pg15")
            self._pg.start()
        return self._pg

    def new_case_dir(self, test_id: str) -> Path:
        safe = "".join(c if c.isalnum() or c in ("-", "_") else "_" for c in test_id)
        p = self.cases_dir / f"{safe}_{int(time.time() * 1000) % 100000}"
        p.mkdir(parents=True, exist_ok=True)
        return p

    def _go_build(self, pkg: str, out_bin: Path) -> Path:
        if out_bin.is_file():
            return out_bin
        cmd_dir = BACKEND_DIR / pkg.lstrip("./")
        if not cmd_dir.is_dir():
            raise FileNotFoundError(
                f"Backend entrypoint directory {cmd_dir} does not exist yet (M1/M2 implementation required)"
            )
        attempts = [
            {},
            {"GOPROXY": "https://goproxy.cn,https://goproxy.io,direct", "GOSUMDB": "off"},
            {"GOPROXY": "direct", "GOSUMDB": "off"},
        ]
        last_err = ""
        for overrides in attempts:
            env = enforce_no_proxy_env(dict(os.environ))
            env["CGO_ENABLED"] = "0"
            env.update(overrides)
            res = subprocess.run(
                ["go", "build", "-o", str(out_bin), pkg],
                cwd=str(BACKEND_DIR),
                env=env,
                capture_output=True,
                text=True,
            )
            if res.returncode == 0 and out_bin.is_file():
                return out_bin
            last_err = res.stderr or res.stdout
            if "proxy.golang.org" not in last_err and "403 Forbidden" not in last_err:
                break
        raise RuntimeError(f"Failed building {pkg} -> {out_bin}:\n{last_err}")

    @property
    def storage_binary(self) -> Path:
        if self._storage_bin is None:
            self._storage_bin = self._go_build("./cmd/trainmaster-storage", self.bin_dir / "trainmaster-storage")
        return self._storage_bin

    @property
    def trainmaster_binary(self) -> Path:
        if self._trainmaster_bin is None:
            self._trainmaster_bin = self._go_build("./cmd/trainmaster", self.bin_dir / "trainmaster")
        return self._trainmaster_bin

    def ensure_web_bundle(self) -> Path:
        """Return built frontend bundle directory without polluting frontend/ workspace."""
        if self._web_bundle_dir is not None and (self._web_bundle_dir / "index.html").is_file():
            return self._web_bundle_dir
        existing_dist = FRONTEND_DIR / "dist"
        if (existing_dist / "index.html").is_file():
            self._web_bundle_dir = existing_dist
            return existing_dist

        out_dir = self.root_dir / "web_dist"
        node_modules = FRONTEND_DIR / "node_modules"
        if not node_modules.is_dir():
            raise RuntimeError(
                f"Frontend bundle not found at {existing_dist} and {node_modules} is not installed"
            )
        vite_bin = node_modules / ".bin" / "vite"
        res = subprocess.run(
            [str(vite_bin), "build", "--outDir", str(out_dir), "--emptyOutDir"],
            cwd=str(FRONTEND_DIR),
            env=enforce_no_proxy_env(dict(os.environ)),
            capture_output=True,
            text=True,
        )
        if res.returncode != 0 or not (out_dir / "index.html").is_file():
            raise RuntimeError(f"vite build into {out_dir} failed:\n{res.stderr}\n{res.stdout}")
        self._web_bundle_dir = out_dir
        return out_dir

    def spawn_storage(
        self,
        case_dir: Path,
        *,
        name: str = "storage_a",
        token: str = "tm-storage-secret-token-0123456789",
        temp_ttl: str = "2h",
        with_spy: bool = True,
        extra_env: Mapping[str, str] | None = None,
    ) -> StorageDaemon:
        d = StorageDaemon(
            binary=self.storage_binary,
            work_dir=case_dir / name,
            name=name,
            token=token,
            temp_ttl=temp_ttl,
            with_spy=with_spy,
            extra_env=extra_env,
        )
        return d.start()

    def spawn_platform(
        self,
        case_dir: Path,
        *,
        name: str = "app",
        node_token: str = "",
        storage_service_url: str = "",
        storage_service_token: str = "",
        with_web: bool = False,
        extra_env: Mapping[str, str] | None = None,
    ) -> PlatformDaemon:
        db_name, dsn = self.pg.create_db(prefix="tm_case")
        web_dir = self.ensure_web_bundle() if with_web else None
        p = PlatformDaemon(
            binary=self.trainmaster_binary,
            work_dir=case_dir / name,
            db_name=db_name,
            database_url=dsn,
            name=name,
            node_token=node_token,
            storage_service_url=storage_service_url,
            storage_service_token=storage_service_token,
            web_dir=web_dir,
            extra_env=extra_env,
        )
        return p.start()

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        if self._pg is not None:
            self._pg.stop()
            self._pg = None
        shutil.rmtree(self.root_dir, ignore_errors=True)


class CaseContext:
    """Per-test RAII context ensuring all spawned daemons/mocks/proxies stop cleanly."""

    def __init__(self, session: E2ESession, test_id: str) -> None:
        self.session = session
        self.test_id = test_id
        self.case_dir = session.new_case_dir(test_id)
        self._closers: list[Any] = []

    def storage(self, **kwargs: Any) -> StorageDaemon:
        d = self.session.spawn_storage(self.case_dir, **kwargs)
        self._closers.append(d.stop)
        return d

    def platform(self, **kwargs: Any) -> PlatformDaemon:
        p = self.session.spawn_platform(self.case_dir, **kwargs)
        self._closers.append(p.stop)
        return p

    def s3_mock(self, **kwargs: Any) -> S3MockServer:
        s3 = S3MockServer(**kwargs).start()
        self._closers.append(s3.stop)
        return s3

    def spy_proxy(self, upstream_url: str, *, name: str = "spy") -> HttpSpyProxy:
        spy = HttpSpyProxy(upstream_url, name=name).start()
        self._closers.append(spy.stop)
        return spy

    def close(self) -> None:
        for closer in reversed(self._closers):
            try:
                closer()
            except Exception:  # noqa: BLE001
                pass
        self._closers.clear()
        shutil.rmtree(self.case_dir, ignore_errors=True)
