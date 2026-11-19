"""Tier 1 Feature Coverage E2E tests for F10, F11, and F12 (15 genuine tests)."""

from __future__ import annotations

import json
import re
import time

from e2e.harness.cluster import CaseContext, run_trainer_job_in_subprocess
from e2e.harness.fixtures import populate_yolo_dir
from e2e.harness.registry import e2e_test
from e2e.tests._helpers import assert_status, read_all_web_bundle_text


def _create_and_claim_job(
    app,
    proj_id: int,
    dv_id: int,
    node_key: str = "11111111-2222-3333-4444-555555555555",
) -> tuple[int, dict[str, object]]:
    job_resp = app.client().post(
        "/api/v1/training/jobs",
        json_body={
            "project_id": proj_id,
            "data_version_id": dv_id,
            "init_mode": "scratch",
            "yolo_version": "yolo11",
            "model_scale": "n",
            "params": {"epochs": 1},
        },
    )
    assert_status(job_resp, 201, "create training job")
    job_id = int(job_resp.json()["id"])

    claim_resp = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    )
    assert_status(claim_resp, 200, "claim training job")
    task = claim_resp.json()["task"]
    assert isinstance(task, dict) and int(task["id"]) == job_id
    return job_id, task


def _get_job_logs(app, job_id: int) -> str:
    resp = app.client().get(f"/api/v1/training/jobs/{job_id}/logs", query={"offset": 0, "limit": 131072})
    assert_status(resp, 200, "fetch training job logs")
    data = resp.json()
    if isinstance(data, dict):
        return str(data.get("content") or data.get("data") or data.get("logs") or resp.text)
    return resp.text


# ==============================================================================
# F10: Python Trainer Checksummed Archive Unpack & Disk Headroom — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F10_01",
    tier=1,
    features="F10",
    title="Python trainer verifies X-Archive-Sha256, extracts dataset.zip, unlinks temp zip, and completes job",
)
def test_t1_f10_01_trainer_archive_verify_unpack_unlink_and_complete(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f10_01")
    app = ctx.platform(name="app_f10_01")
    app.create_docker_node("Docker训练节点", st.url, st.token, activate=True)

    ds = app.import_voc_dataset("Trainer打包样本", count=4)
    proj, dv = app.create_project_and_freeze_version("Trainer打包项目", [int(ds["id"])])
    node_key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
    job_id, task = _create_and_claim_job(app, int(proj["id"]), int(dv["id"]), node_key=node_key)

    trainer_state = ctx.case_dir / "trainer_state_01"
    outcome, audit, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task,
        state_dir=trainer_state,
        node_key=node_key,
    )
    assert outcome == "succeeded", f"Expected trainer outcome 'succeeded', got {outcome!r}"
    assert audit is not None
    assert audit["image_count"] == 4
    assert audit["zip_present_during_training"] is False, "Temporary dataset.zip must be unlinked before training"
    assert list(trainer_state.rglob("dataset.zip")) == []

    job_final = app.client().get(f"/api/v1/training/jobs/{job_id}").json()
    assert job_final["status"] == "succeeded"


@e2e_test(
    test_id="T1_F10_02",
    tier=1,
    features="F10",
    title="Trainer emits distinct [trainMaster] log lines for archive mode vs legacy direct mode",
)
def test_t1_f10_02_trainer_logs_archive_vs_direct_transfer_mode(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f10_02")
    s3 = ctx.s3_mock()
    app = ctx.platform(name="app_f10_02")
    node_key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)

    # 1) Archive mode on Docker node
    app.create_docker_node("Docker日志节点", st.url, st.token, activate=True)
    ds_arc = app.import_voc_dataset("Docker日志集", count=3)
    proj_arc, dv_arc = app.create_project_and_freeze_version("Docker日志项目", [int(ds_arc["id"])])
    job_arc_id, task_arc = _create_and_claim_job(app, int(proj_arc["id"]), int(dv_arc["id"]), node_key=node_key)
    out_arc, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task_arc,
        state_dir=ctx.case_dir / "tr_arc",
        node_key=node_key,
    )
    assert out_arc == "succeeded"
    logs_arc = _get_job_logs(app, job_arc_id)
    assert "[trainMaster]" in logs_arc
    assert any(kw in logs_arc for kw in ("打包", "压缩包", "SHA-256", "解压")), (
        f"Expected archive pack/verify/unpack message in job logs, got:\n{logs_arc}"
    )

    # 2) Legacy direct mode on S3 node
    app.create_s3_node("S3日志节点", s3, activate=True)
    ds_dir = app.import_voc_dataset("S3日志集", count=3)
    proj_dir, dv_dir = app.create_project_and_freeze_version("S3日志项目", [int(ds_dir["id"])])
    job_dir_id, task_dir = _create_and_claim_job(app, int(proj_dir["id"]), int(dv_dir["id"]), node_key=node_key)
    out_dir, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task_dir,
        state_dir=ctx.case_dir / "tr_dir",
        node_key=node_key,
    )
    assert out_dir == "succeeded"
    logs_dir = _get_job_logs(app, job_dir_id)
    assert "[trainMaster]" in logs_dir
    assert any(kw in logs_dir for kw in ("兼容", "流式", "直传")), (
        f"Expected legacy compatible direct pull message in job logs, got:\n{logs_dir}"
    )


@e2e_test(
    test_id="T1_F10_03",
    tier=1,
    features="F10",
    title="Trainer populates DatasetCacheManager on 1st archive pull and skips network download on 2nd job",
)
def test_t1_f10_03_trainer_dataset_cache_store_and_second_run_hit(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f10_03")
    app = ctx.platform(name="app_f10_03")
    app.create_docker_node("Docker缓存测试", st.url, st.token, activate=True)
    assert_status(
        app.client().put(
            "/api/v1/storage/cache-policy",
            json_body={"enabled": True, "max_versions": 3, "ttl_hours": 24},
        ),
        200,
    )

    ds = app.import_voc_dataset("缓存复用样本", count=4)
    proj, dv = app.create_project_and_freeze_version("缓存复用项目", [int(ds["id"])])
    node_key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
    shared_state = ctx.case_dir / "trainer_cache_state"

    # Run 1: downloads from storage container & populates cache
    _, task1 = _create_and_claim_job(app, int(proj["id"]), int(dv["id"]), node_key=node_key)
    assert st.spy is not None
    st.spy.clear()
    out1, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task1,
        state_dir=shared_state,
        node_key=node_key,
    )
    assert out1 == "succeeded"
    assert (shared_state / "dataset-cache" / f"dv-{dv['id']}" / "data.yaml").is_file()
    assert len(st.spy.matching_requests("/api/v1/archives/pack", method="POST")) == 1

    # Run 2: hits local DatasetCacheManager -> ZERO archive pack calls on StorageDaemon!
    job2_id, task2 = _create_and_claim_job(app, int(proj["id"]), int(dv["id"]), node_key=node_key)
    st.spy.clear()
    out2, audit2, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task2,
        state_dir=shared_state,
        node_key=node_key,
    )
    assert out2 == "succeeded"
    assert audit2 is not None and audit2["image_count"] == 4
    assert len(st.spy.archive_requests()) == 0, "Cache hit must trigger 0 remote archive requests"
    assert "命中" in _get_job_logs(app, job2_id)


@e2e_test(
    test_id="T1_F10_04",
    tier=1,
    features="F10",
    title="Co-located trainer with shared Builtin /data mount uses zero-copy dataset directory",
)
def test_t1_f10_04_shared_local_builtin_zero_copy_fast_path(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_f10_04")
    app_spy = ctx.spy_proxy(app.url, name="app_spy_f10_04")

    ds = app.import_voc_dataset("同机零拷贝样本", count=3)
    proj, dv = app.create_project_and_freeze_version("同机零拷贝项目", [int(ds["id"])])
    node_key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
    _, task = _create_and_claim_job(app, int(proj["id"]), int(dv["id"]), node_key=node_key)

    app_spy.clear()
    out, audit, _, _ = run_trainer_job_in_subprocess(
        platform_url=app_spy.url,
        task_payload=task,
        state_dir=ctx.case_dir / "tr_shared",
        node_key=node_key,
        data_dir=app.data_dir,
    )
    assert out == "succeeded"
    assert audit is not None and str(app.data_dir) in str(audit["data_yaml"])
    dataset_downloads = app_spy.matching_requests(str(task["dataset_url"]), method="GET")
    assert len(dataset_downloads) == 0, "Shared local mount must bypass HTTP /dataset download"


@e2e_test(
    test_id="T1_F10_05",
    tier=1,
    features="F10",
    title="Trainer pre-job disk headroom check accounts for dv_total_bytes and evicts stale cached versions",
)
def test_t1_f10_05_trainer_dv_total_bytes_cache_eviction_headroom(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f10_05")
    app = ctx.platform(name="app_f10_05")
    app.create_docker_node("Docker空间检查节点", st.url, st.token, activate=True)
    app.client().put(
        "/api/v1/storage/cache-policy",
        json_body={"enabled": True, "max_versions": 5, "ttl_hours": 24},
    )

    ds = app.import_voc_dataset("空间检查样本", count=3)
    proj, dv = app.create_project_and_freeze_version("空间检查项目", [int(ds["id"])])
    node_key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
    _, task = _create_and_claim_job(app, int(proj["id"]), int(dv["id"]), node_key=node_key)
    dv_bytes = int(task["dv_total_bytes"])
    assert dv_bytes > 0

    # 1. Seed cached version dv-9999 and run with free disk = 512 MiB + (dv_total_bytes - 1),
    # which passes a bare 512 MiB check but fails 512 MiB + 2 * dv_total_bytes headroom after evicting dv-9999.
    state_dir = ctx.case_dir / "tr_evict_state"
    stale_cache = state_dir / "dataset-cache" / "dv-9999"
    populate_yolo_dir(stale_cache, count=2)
    (stale_cache / ".cache-meta.json").write_text(
        json.dumps({"dv_id": 9999, "cached_at": time.time() - 3600, "last_used_at": time.time() - 3600, "size_bytes": 4096}),
        encoding="utf-8",
    )

    out_tight, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task,
        state_dir=state_dir,
        node_key=node_key,
        fake_free_disk_bytes=(512 << 20) + max(0, dv_bytes - 1),
    )
    assert out_tight == "failed"
    assert not stale_cache.exists(), "evict_for_space must evict cached dv-9999 when dv_total_bytes headroom is tight"

    # 2. Seed TTL-expired dv-9998 (> 24h), re-claim retryable job, and verify TTL eviction + successful run
    ttl_expired_cache = state_dir / "dataset-cache" / "dv-9998"
    populate_yolo_dir(ttl_expired_cache, count=2)
    (ttl_expired_cache / ".cache-meta.json").write_text(
        json.dumps({"dv_id": 9998, "cached_at": time.time() - 90000, "last_used_at": time.time() - 90000, "size_bytes": 4096}),
        encoding="utf-8",
    )
    task_retry = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    assert task_retry is not None

    out, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task_retry,
        state_dir=state_dir,
        node_key=node_key,
    )
    assert out == "succeeded"
    assert not ttl_expired_cache.exists(), "TTL-expired dv-9998 must be evicted"
    assert (state_dir / "dataset-cache" / f"dv-{dv['id']}" / "data.yaml").is_file()


# ==============================================================================
# F11: Frontend Storage Docker Container Onboarding & Capability Badges — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F11_01",
    tier=1,
    features="F11",
    title="Built frontend bundle includes Storage Docker Container kind option and label",
)
def test_t1_f11_01_web_bundle_contains_docker_storage_kind(ctx: CaseContext) -> None:
    bundle_dir = ctx.session.ensure_web_bundle()
    bundle_text = read_all_web_bundle_text(bundle_dir)
    assert "存储服务容器" in bundle_text
    assert "docker" in bundle_text


@e2e_test(
    test_id="T1_F11_02",
    tier=1,
    features="F11",
    title="Built frontend bundle includes Docker onboarding fields (endpoint, token, insecure_skip_verify, 测试连接)",
)
def test_t1_f11_02_web_bundle_contains_docker_onboarding_form_fields(ctx: CaseContext) -> None:
    bundle_dir = ctx.session.ensure_web_bundle()
    bundle_text = read_all_web_bundle_text(bundle_dir)
    for token_str in ("endpoint", "insecure_skip_verify", "测试连接", "留空保持不变"):
        assert token_str in bundle_text, f"Missing {token_str!r} in built web bundle"


@e2e_test(
    test_id="T1_F11_03",
    tier=1,
    features="F11",
    title="Built frontend bundle embeds copyable docker-compose.storage.yml and docker run command helpers",
)
def test_t1_f11_03_web_bundle_contains_docker_deployment_commands(ctx: CaseContext) -> None:
    bundle_dir = ctx.session.ensure_web_bundle()
    bundle_text = read_all_web_bundle_text(bundle_dir)
    assert "docker-compose.storage.yml" in bundle_text
    assert "docker run" in bundle_text
    assert "TM_STORAGE_TOKEN" in bundle_text


@e2e_test(
    test_id="T1_F11_04",
    tier=1,
    features="F11",
    title="Built frontend bundle includes supports_archive capability badges matching live /api/v1/storage/nodes",
)
def test_t1_f11_04_web_bundle_capability_badges_and_live_api_alignment(ctx: CaseContext) -> None:
    bundle_dir = ctx.session.ensure_web_bundle()
    bundle_text = read_all_web_bundle_text(bundle_dir)
    assert "支持打包加速" in bundle_text
    assert "兼容直传" in bundle_text and "不打包" in bundle_text

    st = ctx.storage(name="st_f11_04")
    s3 = ctx.s3_mock()
    app = ctx.platform(name="app_f11_04", with_web=True)
    app.create_docker_node("UI容器存储", st.url, st.token, activate=True)
    app.create_s3_node("UI历史S3", s3, activate=False)

    nodes = app.client().get("/api/v1/storage/nodes").json()["items"]
    by_kind = {n["kind"]: n for n in nodes if not n["is_builtin"]}
    assert by_kind["docker"]["supports_archive"] is True
    assert by_kind["s3"]["supports_archive"] is False


@e2e_test(
    test_id="T1_F11_05",
    tier=1,
    features="F11",
    title="Frontend payload contract for create/test/patch Docker node succeeds end-to-end against trainmaster",
)
def test_t1_f11_05_frontend_docker_node_payload_contract_roundtrip(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f11_05", token="ui-form-token-abcdef123456")
    app = ctx.platform(name="app_f11_05", with_web=True)
    cli = app.client()

    # Exact payload shape sent by frontend StorageNodeDialog (`config: { endpoint, insecure_skip_verify }` + `token`)
    test_resp = cli.post(
        "/api/v1/storage/nodes/test",
        json_body={
            "kind": "docker",
            "config": {"endpoint": st.url, "insecure_skip_verify": False},
            "token": st.token,
        },
    )
    assert_status(test_resp, 200, "UI-shaped POST /api/v1/storage/nodes/test")
    assert test_resp.json()["ok"] is True

    create_resp = cli.post(
        "/api/v1/storage/nodes",
        json_body={
            "name": "前端弹窗创建的Docker节点",
            "kind": "docker",
            "config": {"endpoint": st.url, "insecure_skip_verify": False},
            "token": st.token,
            "activate": True,
        },
    )
    assert_status(create_resp, 201, "UI-shaped POST /api/v1/storage/nodes")
    created = create_resp.json()
    assert created["has_secret"] is True
    assert created["supports_archive"] is True


# ==============================================================================
# F12: Frontend Multi-Stage Migration, DataVersion & Training Status UI — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F12_01",
    tier=1,
    features="F12",
    title="Built frontend bundle includes pre-migration archive vs legacy direct transfer mode indicators",
)
def test_t1_f12_01_web_bundle_pre_migration_transfer_mode_banners(ctx: CaseContext) -> None:
    bundle_dir = ctx.session.ensure_web_bundle()
    bundle_text = read_all_web_bundle_text(bundle_dir)
    assert "压缩包整包迁移" in bundle_text
    assert "兼容逐文件直传" in bundle_text


@e2e_test(
    test_id="T1_F12_02",
    tier=1,
    features="F12",
    title="Built frontend bundle includes all 5 multi-stage archive migration Chinese progress labels",
)
def test_t1_f12_02_web_bundle_five_migration_stage_labels(ctx: CaseContext) -> None:
    bundle_dir = ctx.session.ensure_web_bundle()
    bundle_text = read_all_web_bundle_text(bundle_dir)
    for stage_label in ("正在打包压缩", "正在传输压缩包", "正在解压落盘", "正在校验", "正在清理临时包"):
        assert stage_label in bundle_text, f"Missing live migration stage label {stage_label!r} in web bundle"


@e2e_test(
    test_id="T1_F12_03",
    tier=1,
    features="F12",
    title="Built frontend bundle surfaces archive_bytes and transfer_mode badges matching migration JSON",
)
def test_t1_f12_03_web_bundle_archive_bytes_and_transfer_mode_badges(ctx: CaseContext) -> None:
    bundle_dir = ctx.session.ensure_web_bundle()
    bundle_text = read_all_web_bundle_text(bundle_dir)
    assert "archive_bytes" in bundle_text
    assert "transfer_mode" in bundle_text
    assert "压缩包传输" in bundle_text
    assert "逐文件直传" in bundle_text


@e2e_test(
    test_id="T1_F12_04",
    tier=1,
    features="F12",
    title="Built frontend bundle includes NewTrainingJobPage data copy mode hints across node types",
)
def test_t1_f12_04_web_bundle_training_data_copy_mode_hints(ctx: CaseContext) -> None:
    bundle_dir = ctx.session.ensure_web_bundle()
    bundle_text = read_all_web_bundle_text(bundle_dir)
    assert "本机共享零拷贝" in bundle_text
    assert "存储容器打包拉取并解压" in bundle_text
    assert "兼容存储流式拉取" in bundle_text


@e2e_test(
    test_id="T1_F12_05",
    tier=1,
    features="F12",
    title="Live trainmaster serves SPA routes (/system/storage, /training/jobs/new) and hashed JS/CSS assets",
)
def test_t1_f12_05_live_spa_routes_and_static_assets_http_200(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_f12_05", with_web=True)
    cli = app.client()

    for spa_route in ("/system/storage", "/datasets/versions/new", "/training/jobs/new"):
        page = cli.get(spa_route)
        assert_status(page, 200, f"GET {spa_route}")
        assert "<!doctype html>" in page.text.lower() or "<div id=\"root\">" in page.text.lower()

    html = cli.get("/system/storage").text
    asset_paths = re.findall(r'(?:src|href)="(/assets/[^"]+)"', html)
    assert len(asset_paths) >= 1, f"Expected hashed /assets/* references in index.html, got:\n{html}"
    for asset in asset_paths:
        a_resp = cli.get(asset)
        assert_status(a_resp, 200, f"GET static asset {asset}")
        assert len(a_resp.body) > 100
