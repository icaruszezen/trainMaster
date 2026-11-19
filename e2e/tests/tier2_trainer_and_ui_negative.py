"""Tier 2 Boundary, Negative, and Security E2E tests for F10, F11, and F12 (16 genuine tests)."""

from __future__ import annotations

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import subprocess
import threading

from e2e.harness.cluster import FRONTEND_DIR, CaseContext, run_trainer_job_in_subprocess
from e2e.harness.fixtures import make_zipslip_zip, populate_yolo_dir, sha256_bytes
from e2e.harness.http_client import enforce_no_proxy_env
from e2e.harness.postgres import allocate_free_port
from e2e.harness.registry import e2e_test
from e2e.tests._helpers import assert_status, read_all_web_bundle_text


def _setup_docker_training_task(ctx: CaseContext, suffix: str) -> tuple[object, object, int, dict[str, object], str]:
    st = ctx.storage(name=f"st_{suffix}")
    app = ctx.platform(name=f"app_{suffix}")
    app.create_docker_node(f"Docker-{suffix}", st.url, st.token, activate=True)
    app.client().put(
        "/api/v1/storage/cache-policy",
        json_body={"enabled": True, "max_versions": 3, "ttl_hours": 24},
    )
    ds = app.import_voc_dataset(f"DS-{suffix}", count=3)
    proj, dv = app.create_project_and_freeze_version(f"Proj-{suffix}", [int(ds["id"])])
    node_key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)

    job_id = int(
        app.client()
        .post(
            "/api/v1/training/jobs",
            json_body={
                "project_id": proj["id"],
                "data_version_id": dv["id"],
                "init_mode": "scratch",
                "yolo_version": "yolo11",
                "model_scale": "n",
                "params": {"epochs": 1},
            },
        )
        .json()["id"]
    )
    task = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    return st, app, job_id, task, node_key


# ==============================================================================
# F10 Tier 2: Trainer SHA-256 Tamper, Bad Zip, Zip-Slip, Disk Space, Cache Purge — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F10_01",
    tier=2,
    features="F10",
    title="Tampered dataset.zip stream fails pre-extract SHA-256 check, unlinks zip, and skips extraction",
)
def test_t2_f10_01_trainer_tampered_zip_bytes_rejected_pre_extract(ctx: CaseContext) -> None:
    _, app, job_id, task, node_key = _setup_docker_training_task(ctx, "t2_f10_01")
    app_spy = ctx.spy_proxy(app.url, name="app_tamper_spy")
    app_spy.set_corrupt_response_body(str(task["dataset_url"]), True)

    state_dir = ctx.case_dir / "tr_tamper_state"
    outcome, audit, _, _ = run_trainer_job_in_subprocess(
        platform_url=app_spy.url,
        task_payload=task,
        state_dir=state_dir,
        node_key=node_key,
    )
    assert outcome == "failed"
    assert audit is None, "Training worker must NEVER run when pre-extract SHA-256 verification fails"
    assert list(state_dir.rglob("dataset.zip")) == [], "Corrupt dataset.zip must be deleted immediately"
    cache_entries = list((state_dir / "dataset-cache").glob("dv-*")) if (state_dir / "dataset-cache").exists() else []
    assert cache_entries == [], "Corrupt download must never populate DatasetCacheManager"

    job = app.client().get(f"/api/v1/training/jobs/{job_id}").json()
    assert job["status"] in ("failed", "queued")


@e2e_test(
    test_id="T2_F10_02",
    tier=2,
    features="F10",
    title="Forged X-Archive-Sha256 header on valid dataset.zip is rejected before extraction",
)
def test_t2_f10_02_trainer_forged_sha256_header_rejected(ctx: CaseContext) -> None:
    _, app, _, task, node_key = _setup_docker_training_task(ctx, "t2_f10_02")
    app_spy = ctx.spy_proxy(app.url, name="app_forged_hdr")
    forged = "f" * 64
    app_spy.set_response_header_override(
        str(task["dataset_url"]),
        {"X-Archive-Sha256": forged, "X-Content-Sha256": forged},
    )

    state_dir = ctx.case_dir / "tr_forged_state"
    outcome, audit, _, _ = run_trainer_job_in_subprocess(
        platform_url=app_spy.url,
        task_payload=task,
        state_dir=state_dir,
        node_key=node_key,
    )
    assert outcome == "failed"
    assert audit is None
    assert list(state_dir.rglob("dataset.zip")) == []


@e2e_test(
    test_id="T2_F10_03",
    tier=2,
    features="F10",
    title="Trainer rejects zip-slip archive containing ../../ traversal without writing outside work dir",
)
def test_t2_f10_03_trainer_zipslip_archive_rejected(ctx: CaseContext) -> None:
    evil_zip = make_zipslip_zip("../../pwned_by_zip.txt", b"zipslip")
    evil_sha = sha256_bytes(evil_zip)
    port = allocate_free_port("127.0.0.1")
    failures_reported: list[dict[str, object]] = []

    class FakePlatformHandler(BaseHTTPRequestHandler):
        def log_message(self, format: str, *args: object) -> None:  # noqa: A002
            return

        def do_GET(self) -> None:
            self.send_response(200)
            self.send_header("Content-Type", "application/zip")
            self.send_header("Content-Length", str(len(evil_zip)))
            self.send_header("X-Transfer-Mode", "archive")
            self.send_header("X-Archive-Sha256", evil_sha)
            self.end_headers()
            self.wfile.write(evil_zip)

        def do_POST(self) -> None:
            ln = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(ln) if ln > 0 else b"{}"
            if self.path.endswith("/fail"):
                failures_reported.append(json.loads(body.decode("utf-8")))
            resp = b'{"lease_seconds": 60}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)

    srv = ThreadingHTTPServer(("127.0.0.1", port), FakePlatformHandler)
    srv.daemon_threads = True
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    ctx._closers.append(lambda: (srv.shutdown(), srv.server_close()))

    task_payload = {
        "id": 77,
        "project_id": 1,
        "init_mode": "scratch",
        "yolo_version": "yolo11",
        "model_scale": "n",
        "arch_yaml": "nc: 1\nscales:\n  n: [0.5, 0.25, 1024]\nbackbone: []\nhead: []\n",
        "task": "detect",
        "data_version": {"id": 10, "dir_name": "data-versions/project-1/v1", "shared_local": False},
        "dv_total_bytes": len(evil_zip),
        "dv_transfer_mode": "archive",
        "dataset_cache": {"enabled": True, "max_versions": 2, "ttl_seconds": 3600},
        "params": {"epochs": 1},
        "dataset_url": "/api/v1/compute/agent/training-jobs/77/dataset",
        "progress_url": "/api/v1/compute/agent/training-jobs/77/progress",
        "artifacts_url": "/api/v1/compute/agent/training-jobs/77/artifacts",
        "complete_url": "/api/v1/compute/agent/training-jobs/77/complete",
        "fail_url": "/api/v1/compute/agent/training-jobs/77/fail",
    }
    state_dir = ctx.case_dir / "tr_zipslip"
    outcome, audit, _, _ = run_trainer_job_in_subprocess(
        platform_url=f"http://127.0.0.1:{port}",
        task_payload=task_payload,
        state_dir=state_dir,
    )
    assert outcome == "failed"
    assert audit is None
    assert not (ctx.case_dir / "pwned_by_zip.txt").exists()
    assert not (state_dir / "pwned_by_zip.txt").exists()
    assert len(failures_reported) == 1


@e2e_test(
    test_id="T2_F10_04",
    tier=2,
    features="F10",
    title="Trainer handles corrupt non-zip payload with matching SHA-256 by unlinking temp file and failing cleanly",
)
def test_t2_f10_04_trainer_corrupt_badzipfile_unlinks_and_reports_failure(ctx: CaseContext) -> None:
    not_a_zip = b"NOT_A_ZIP_FILE_AT_ALL_1234567890"
    sha = sha256_bytes(not_a_zip)
    port = allocate_free_port("127.0.0.1")

    class BadZipHandler(BaseHTTPRequestHandler):
        def log_message(self, format: str, *args: object) -> None:  # noqa: A002
            return

        def do_GET(self) -> None:
            self.send_response(200)
            self.send_header("Content-Type", "application/zip")
            self.send_header("Content-Length", str(len(not_a_zip)))
            self.send_header("X-Transfer-Mode", "archive")
            self.send_header("X-Archive-Sha256", sha)
            self.end_headers()
            self.wfile.write(not_a_zip)

        def do_POST(self) -> None:
            ln = int(self.headers.get("Content-Length") or 0)
            if ln > 0:
                self.rfile.read(ln)
            resp = b'{"lease_seconds": 60}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)

    srv = ThreadingHTTPServer(("127.0.0.1", port), BadZipHandler)
    srv.daemon_threads = True
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    ctx._closers.append(lambda: (srv.shutdown(), srv.server_close()))

    task_payload = {
        "id": 78,
        "project_id": 1,
        "init_mode": "scratch",
        "yolo_version": "yolo11",
        "model_scale": "n",
        "arch_yaml": "nc: 1\n",
        "task": "detect",
        "data_version": {"id": 11, "dir_name": "data-versions/project-1/v1", "shared_local": False},
        "dv_total_bytes": 1024,
        "dv_transfer_mode": "archive",
        "dataset_cache": {"enabled": True, "max_versions": 2, "ttl_seconds": 3600},
        "params": {"epochs": 1},
        "dataset_url": "/api/v1/compute/agent/training-jobs/78/dataset",
        "progress_url": "/api/v1/compute/agent/training-jobs/78/progress",
        "artifacts_url": "/api/v1/compute/agent/training-jobs/78/artifacts",
        "complete_url": "/api/v1/compute/agent/training-jobs/78/complete",
        "fail_url": "/api/v1/compute/agent/training-jobs/78/fail",
    }
    state_dir = ctx.case_dir / "tr_badzip"
    outcome, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=f"http://127.0.0.1:{port}",
        task_payload=task_payload,
        state_dir=state_dir,
    )
    assert outcome == "failed"
    assert list(state_dir.rglob("dataset.zip")) == []


@e2e_test(
    test_id="T2_F10_05",
    tier=2,
    features="F10",
    title="Trainer aborts before downloading dataset.zip when free disk is below dv_total_bytes headroom",
)
def test_t2_f10_05_trainer_insufficient_disk_for_dv_total_bytes_aborts_early(ctx: CaseContext) -> None:
    _, app, _, task, node_key = _setup_docker_training_task(ctx, "t2_f10_05")
    app_spy = ctx.spy_proxy(app.url, name="app_disk_spy")

    # Inflate dv_total_bytes to 8 GiB while simulated free disk is 2 GiB (above 512 MiB base, below 8 GiB dataset!)
    inflated_task = dict(task)
    inflated_task["dv_total_bytes"] = 8 << 30

    app_spy.clear()
    outcome, audit, _, _ = run_trainer_job_in_subprocess(
        platform_url=app_spy.url,
        task_payload=inflated_task,
        state_dir=ctx.case_dir / "tr_low_disk",
        node_key=node_key,
        fake_free_disk_bytes=2 << 30,
    )
    assert outcome == "failed", "Expected trainer to fail pre-download disk check when free < dv_total_bytes"
    assert audit is None
    assert len(app_spy.matching_requests(str(task["dataset_url"]), method="GET")) == 0


@e2e_test(
    test_id="T2_F10_06",
    tier=2,
    features="F10",
    title="Disabling trainer dataset cache policy purges existing cached versions on next job",
)
def test_t2_f10_06_disabling_cache_policy_purges_cached_versions(ctx: CaseContext) -> None:
    _, app, _, task, node_key = _setup_docker_training_task(ctx, "t2_f10_06")
    state_dir = ctx.case_dir / "tr_purge_cache"
    old_cache = state_dir / "dataset-cache" / "dv-1234"
    populate_yolo_dir(old_cache, count=2)
    (old_cache / ".cache-meta.json").write_text(
        '{"dv_id": 1234, "cached_at": 1700000000, "last_used_at": 1700000000, "size_bytes": 100}\n',
        encoding="utf-8",
    )

    disabled_task = dict(task)
    disabled_task["dataset_cache"] = {"enabled": False, "max_versions": 2, "ttl_seconds": 3600}
    out, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=disabled_task,
        state_dir=state_dir,
        node_key=node_key,
    )
    assert out == "succeeded"
    assert not (state_dir / "dataset-cache").exists(), "Disabled cache policy must purge dataset-cache directory"


# ==============================================================================
# F11 Tier 2: Frontend Validation, Secret Redaction & Vitest Suite — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F11_01",
    tier=2,
    features="F11",
    title="Frontend Vitest suite passes all storage onboarding, command generator, and badge tests",
)
def test_t2_f11_01_frontend_vitest_suite_passes(ctx: CaseContext) -> None:
    _ = ctx
    vitest_bin = FRONTEND_DIR / "node_modules" / ".bin" / "vitest"
    assert vitest_bin.is_file(), f"Missing {vitest_bin}"
    res = subprocess.run(
        [str(vitest_bin), "run"],
        cwd=str(FRONTEND_DIR),
        env=enforce_no_proxy_env(),
        capture_output=True,
        text=True,
        check=False,
    )
    assert res.returncode == 0, f"Frontend vitest run failed:\n{res.stdout}\n{res.stderr}"


@e2e_test(
    test_id="T2_F11_02",
    tier=2,
    features="F11",
    title="GET /api/v1/storage/nodes redacts plaintext secrets across docker and s3 nodes",
)
def test_t2_f11_02_storage_nodes_list_never_exposes_secrets(ctx: CaseContext) -> None:
    docker_tok = "ultra-sensitive-docker-secret-token-888"
    st = ctx.storage(name="st_t2_f11_02", token=docker_tok)
    s3 = ctx.s3_mock(secret_key="ultra-sensitive-s3-secret-key-999")
    app = ctx.platform(name="app_t2_f11_02", with_web=True)

    app.create_docker_node("安全Docker", st.url, docker_tok, activate=True)
    app.create_s3_node("安全S3", s3, activate=False)

    raw_resp = app.client().get("/api/v1/storage/nodes")
    assert_status(raw_resp, 200)
    assert docker_tok not in raw_resp.text
    assert s3.secret_key not in raw_resp.text
    for item in raw_resp.json()["items"]:
        if not item["is_builtin"]:
            assert item["has_secret"] is True


@e2e_test(
    test_id="T2_F11_03",
    tier=2,
    features="F11",
    title="PATCH and DELETE on non-existent storage node ID return structured JSON 404 envelope",
)
def test_t2_f11_03_missing_storage_node_returns_json_404_envelope(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_t2_f11_03", with_web=True)
    for resp in (
        app.client().patch("/api/v1/storage/nodes/999999", json_body={"name": "ghost"}),
        app.client().delete("/api/v1/storage/nodes/999999"),
    ):
        assert_status(resp, 404)
        err = resp.json().get("error", {})
        assert err.get("code") == "not_found"


@e2e_test(
    test_id="T2_F11_04",
    tier=2,
    features="F11",
    title="Built frontend bundle includes legacy storage compatibility notice that packing is not used",
)
def test_t2_f11_04_web_bundle_legacy_storage_no_packing_notice(ctx: CaseContext) -> None:
    bundle_text = read_all_web_bundle_text(ctx.session.ensure_web_bundle())
    assert "不提供打包" in bundle_text or "不打包" in bundle_text


@e2e_test(
    test_id="T2_F11_05",
    tier=2,
    features="F11",
    title="Built frontend bundle includes token placeholder fallback (<访问令牌>) for unentered Docker tokens",
)
def test_t2_f11_05_web_bundle_docker_command_token_placeholder_fallback(ctx: CaseContext) -> None:
    bundle_text = read_all_web_bundle_text(ctx.session.ensure_web_bundle())
    assert "访问令牌" in bundle_text


# ==============================================================================
# F12 Tier 2: Frontend Defensive Fallbacks, Error States & Mixed Modes — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F12_01",
    tier=2,
    features="F12",
    title="Frontend TypeScript strict typecheck passes with zero errors across storage, training, and data-versions",
)
def test_t2_f12_01_frontend_strict_typecheck_passes(ctx: CaseContext) -> None:
    _ = ctx
    tsc_bin = FRONTEND_DIR / "node_modules" / ".bin" / "tsc"
    assert tsc_bin.is_file(), f"Missing {tsc_bin}"
    res = subprocess.run(
        [str(tsc_bin), "--noEmit", "-p", "tsconfig.app.json"],
        cwd=str(FRONTEND_DIR),
        env=enforce_no_proxy_env(),
        capture_output=True,
        text=True,
        check=False,
    )
    assert res.returncode == 0, f"tsc --noEmit failed:\n{res.stdout}\n{res.stderr}"


@e2e_test(
    test_id="T2_F12_02",
    tier=2,
    features="F12",
    title="Failed migration record on GET /api/v1/storage/migrations preserves valid JSON schema for UI rendering",
)
def test_t2_f12_02_failed_migration_api_shape_for_ui_error_row(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t2_f12_02_a")
    node_b = ctx.storage(name="st_t2_f12_02_b")
    app = ctx.platform(name="app_t2_f12_02", with_web=True)

    app.create_docker_node("UI-Err-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("UI报错数据集", count=2)
    app.create_docker_node("UI-Err-B", node_b.url, node_b.token, activate=True)

    assert node_b.spy is not None
    node_b.spy.set_forced_status(
        "/api/v1/archives",
        502,
        b'{"error":{"code":"bad_gateway","message":"simulated archive unpack failure"}}',
    )

    create_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
    )
    assert_status(create_resp, (200, 201, 202), "enqueue migration before archive failure")
    mig_id = int(create_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    row = migs[mig_id]
    assert row["status"] == "failed"
    assert isinstance(row.get("error"), str) and len(row["error"]) > 0
    assert row.get("transfer_mode") == "archive"
    assert isinstance(row.get("archive_bytes", 0), int)


@e2e_test(
    test_id="T2_F12_03",
    tier=2,
    features="F12",
    title="GET /api/v1/storage/migratable exposes mixed Builtin and Legacy S3 node refs when Docker is active",
)
def test_t2_f12_03_migratable_items_mixed_fleet_metadata(ctx: CaseContext) -> None:
    s3 = ctx.s3_mock()
    st = ctx.storage(name="st_t2_f12_03")
    app = ctx.platform(name="app_t2_f12_03", with_web=True)

    ds_local = app.import_voc_dataset("本机可迁移项", count=2)
    app.create_s3_node("S3历史节点", s3, activate=True)
    ds_s3 = app.import_voc_dataset("S3可迁移项", count=2)
    app.create_docker_node("新激活Docker", st.url, st.token, activate=True)

    migratable = app.client().get("/api/v1/storage/migratable").json()["items"]
    by_id = {int(it["target_id"]): it for it in migratable if it["target_type"] == "dataset"}
    assert int(ds_local["id"]) in by_id and int(ds_s3["id"]) in by_id
    assert by_id[int(ds_local["id"])]["storage_node"]["is_builtin"] is True
    assert by_id[int(ds_s3["id"])]["storage_node"]["kind"] == "s3"
    assert by_id[int(ds_s3["id"])]["storage_node"]["supports_archive"] is False


@e2e_test(
    test_id="T2_F12_04",
    tier=2,
    features="F12",
    title="Invalid cache-policy bounds are rejected or normalized without corrupting GET /api/v1/storage/cache-policy",
)
def test_t2_f12_04_cache_policy_invalid_bounds_handling(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_t2_f12_04", with_web=True)
    put_resp = app.client().put(
        "/api/v1/storage/cache-policy",
        json_body={"enabled": True, "max_versions": -5, "ttl_hours": 0},
    )
    assert put_resp.status in (200, 400, 422)
    cur = app.client().get("/api/v1/storage/cache-policy")
    assert_status(cur, 200)
    policy = cur.json()
    assert int(policy["max_versions"]) >= 1
    assert int(policy["ttl_hours"]) >= 1


@e2e_test(
    test_id="T2_F12_05",
    tier=2,
    features="F12",
    title="Unknown /api/v1/storage/* route returns JSON 404 no-store while SPA deep links serve HTML",
)
def test_t2_f12_05_api_404_vs_spa_fallback_separation(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_t2_f12_05", with_web=True)
    api_404 = app.client().get("/api/v1/storage/does-not-exist")
    assert_status(api_404, 404)
    assert "no-store" in (api_404.headers.get("Cache-Control") or "")
    assert api_404.json()["error"]["code"] == "not_found"

    spa_ok = app.client().get("/system/storage")
    assert_status(spa_ok, 200)
    assert "text/html" in (spa_ok.headers.get("Content-Type") or "")
