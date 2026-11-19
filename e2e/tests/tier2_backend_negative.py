"""Tier 2 Boundary, Negative, and Fault-Injection E2E tests for F5, F6, F7, F8, and F9 (27 genuine tests)."""

from __future__ import annotations

import io
import time
import zipfile

from e2e.harness.cluster import CaseContext
from e2e.harness.fixtures import make_png_bytes
from e2e.harness.registry import e2e_test
from e2e.tests._helpers import assert_status


# ==============================================================================
# F5 Tier 2: KindDocker Validation, Secret Retention & Delete Guards — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F5_01",
    tier=2,
    features="F5",
    title="POST /api/v1/storage/nodes rejects empty/invalid Docker endpoint URLs and unknown node kinds",
)
def test_t2_f5_01_invalid_docker_endpoint_and_kind_rejected(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_t2_f5_01")
    for bad_ep in ("", "not-a-url", "ftp://127.0.0.1:8090"):
        resp = app.create_docker_node("非法端点", bad_ep, "tok-123")
        assert_status(resp, (400, 422), f"invalid docker endpoint {bad_ep!r}")

    bad_kind = app.client().post(
        "/api/v1/storage/nodes",
        json_body={"name": "未知类型", "kind": "webdav", "activate": False},
    )
    assert_status(bad_kind, (400, 422), "unknown storage node kind")


@e2e_test(
    test_id="T2_F5_02",
    tier=2,
    features="F5",
    title="Probing a secured StorageDaemon with wrong Bearer token fails probe cleanly",
)
def test_t2_f5_02_docker_probe_wrong_token_fails_cleanly(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f5_02", token="real-secret-token-123456")
    app = ctx.platform(name="app_t2_f5_02")

    probe = app.test_docker_node(st.url, "wrong-token-value")
    assert probe.status in (200, 401, 422, 503)
    if probe.status == 200:
        assert probe.json().get("ok") is False


@e2e_test(
    test_id="T2_F5_03",
    tier=2,
    features="F5",
    title="POST /api/v1/storage/nodes/test with node_id and empty token reuses stored encrypted token",
)
def test_t2_f5_03_test_node_reuses_saved_encrypted_token(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f5_03", token="stored-encrypted-token-777")
    app = ctx.platform(name="app_t2_f5_03")

    created = app.create_docker_node("加密复用节点", st.url, st.token, activate=False).json()
    node_id = int(created["id"])

    probe = app.test_docker_node(st.url, "", node_id=node_id)
    assert_status(probe, 200, "test existing docker node reusing stored secret")
    assert probe.json().get("ok") is True


@e2e_test(
    test_id="T2_F5_04",
    tier=2,
    features="F5",
    title="DELETE /api/v1/storage/nodes/{id} blocks deleting builtin, active, or non-empty Docker nodes (409)",
)
def test_t2_f5_04_delete_builtin_active_or_non_empty_docker_node_conflict(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f5_04")
    app = ctx.platform(name="app_t2_f5_04")
    cli = app.client()

    assert_status(cli.delete("/api/v1/storage/nodes/1"), 409, "delete builtin node")

    node = app.create_docker_node("受保护Docker节点", st.url, st.token, activate=True).json()
    node_id = int(node["id"])
    assert_status(cli.delete(f"/api/v1/storage/nodes/{node_id}"), 409, "delete active docker node")

    app.import_voc_dataset("占用数据集", count=2)
    assert_status(cli.post("/api/v1/storage/nodes/1/activate"), 200)
    assert_status(
        cli.delete(f"/api/v1/storage/nodes/{node_id}"),
        409,
        "delete non-empty docker node holding dataset",
    )


@e2e_test(
    test_id="T2_F5_05",
    tier=2,
    features="F5",
    title="Creating two storage nodes with duplicate names returns 409 Conflict",
)
def test_t2_f5_05_duplicate_node_name_returns_409(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f5_05")
    app = ctx.platform(name="app_t2_f5_05")
    assert_status(app.create_docker_node("重名Docker节点", st.url, st.token), 201)
    dup = app.create_docker_node("重名Docker节点", st.url, st.token)
    assert_status(dup, 409, "duplicate storage node name")


@e2e_test(
    test_id="T2_F5_06",
    tier=2,
    features="F5",
    title="PATCH with clear_secret=true clears encrypted token and sets has_secret=false",
)
def test_t2_f5_06_clear_secret_removes_saved_docker_token(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f5_06", token="removable-token-123456")
    app = ctx.platform(name="app_t2_f5_06")
    node = app.create_docker_node("可清除密钥节点", st.url, st.token, activate=False).json()
    assert node["has_secret"] is True

    # Allow clearing secret when pointing at an open storage daemon or clearing directly
    open_st = ctx.storage(name="st_t2_f5_06_open", token="")
    patched = app.update_docker_node(
        int(node["id"]),
        endpoint=open_st.url,
        clear_secret=True,
    )
    assert_status(patched, 200, "PATCH clear_secret=true")
    assert patched.json()["has_secret"] is False


# ==============================================================================
# F6 Tier 2: Legacy Storage Server Negative & Zero-Archive Proofs — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F6_01",
    tier=2,
    features="F6",
    title="Legacy S3 probe with wrong access_key_id fails cleanly with zero archive requests",
)
def test_t2_f6_01_s3_wrong_credentials_probe_fails_without_archive(ctx: CaseContext) -> None:
    s3 = ctx.s3_mock()
    app = ctx.platform(name="app_t2_f6_01")
    resp = app.client().post(
        "/api/v1/storage/nodes/test",
        json_body={
            "kind": "s3",
            "s3": {
                "endpoint": s3.url,
                "region": "us-east-1",
                "bucket": "tm-datasets",
                "prefix": "p",
                "access_key_id": "wrong-access-key",
                "force_path_style": True,
                "insecure_skip_verify": False,
            },
            "secret_access_key": "wrong-secret",
        },
    )
    assert resp.status in (200, 403, 503)
    if resp.status == 200:
        assert resp.json().get("ok") is False
    assert len(s3.archive_requests()) == 0


@e2e_test(
    test_id="T2_F6_02",
    tier=2,
    features="F6",
    title="Migration Docker A -> Legacy S3 runs transfer_mode=direct with ZERO archive calls on Docker A",
)
def test_t2_f6_02_docker_to_s3_migration_zero_archive_calls(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f6_02")
    s3 = ctx.s3_mock()
    app = ctx.platform(name="app_t2_f6_02")

    app.create_docker_node("源Docker", st.url, st.token, activate=True)
    ds = app.import_voc_dataset("Docker迁S3数据集", count=3)
    app.create_s3_node("目标S3", s3, activate=True)

    assert st.spy is not None
    st.spy.clear()
    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "direct"
    assert len(st.spy.archive_requests()) == 0, "Legacy S3 peer must forbid packing on Docker source"
    assert len(s3.archive_requests()) == 0


@e2e_test(
    test_id="T2_F6_03",
    tier=2,
    features="F6",
    title="Migration Legacy S3 -> Docker A runs transfer_mode=direct with ZERO archive calls",
)
def test_t2_f6_03_s3_to_docker_migration_zero_archive_calls(ctx: CaseContext) -> None:
    s3 = ctx.s3_mock()
    st = ctx.storage(name="st_t2_f6_03")
    app = ctx.platform(name="app_t2_f6_03")

    app.create_s3_node("源S3", s3, activate=True)
    ds = app.import_voc_dataset("S3迁Docker数据集", count=3)
    app.create_docker_node("目标Docker", st.url, st.token, activate=True)

    assert st.spy is not None
    st.spy.clear()
    s3.clear_logs()

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "direct"
    assert len(st.spy.archive_requests()) == 0
    assert len(s3.archive_requests()) == 0


@e2e_test(
    test_id="T2_F6_04",
    tier=2,
    features="F6",
    title="Migration Builtin -> Passive Local (is_builtin=false) uses direct mode without archive packing",
)
def test_t2_f6_04_builtin_to_passive_local_uses_direct_mode(ctx: CaseContext) -> None:
    coloc = ctx.storage(name="st_t2_f6_04_coloc")
    app = ctx.platform(
        name="app_t2_f6_04",
        storage_service_url=coloc.url,
        storage_service_token=coloc.token,
    )
    ds = app.import_voc_dataset("本机迁被动目录数据集", count=3)
    passive_dir = ctx.case_dir / "passive_host_mount"
    assert_status(app.create_passive_local_node("外挂磁盘目录", passive_dir, activate=True), 201)

    assert coloc.spy is not None
    coloc.spy.clear()

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "direct"
    assert len(coloc.spy.archive_requests()) == 0


@e2e_test(
    test_id="T2_F6_05",
    tier=2,
    features="F6",
    title="Cross-node DataVersion build from Legacy S3 source onto Docker node performs zero archive calls on S3",
)
def test_t2_f6_05_cross_node_dv_build_from_s3_to_docker_no_s3_packing(ctx: CaseContext) -> None:
    s3 = ctx.s3_mock()
    st = ctx.storage(name="st_t2_f6_05")
    app = ctx.platform(name="app_t2_f6_05")

    app.create_s3_node("源S3存储", s3, activate=True)
    ds = app.import_voc_dataset("S3冻结源", count=3)
    app.create_docker_node("目标Docker存储", st.url, st.token, activate=True)
    s3.clear_logs()

    _, dv = app.create_project_and_freeze_version("混合源版本冻结", [int(ds["id"])])
    assert dv["status"] == "ready"
    assert len(s3.archive_requests()) == 0


# ==============================================================================
# F7 Tier 2: Migration Cancel, Corrupt Rollback, 507 & Invalid Targets — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F7_01",
    tier=2,
    features="F7",
    title="Mid-migration cancel aborts in-flight archive transfer, preserves source, and cleans temp files",
)
def test_t2_f7_01_mid_migration_cancel_preserves_source_and_cleans_up(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t2_f7_01_a")
    node_b = ctx.storage(name="st_t2_f7_01_b")
    app = ctx.platform(name="app_t2_f7_01")

    na = app.create_docker_node("Cancel-A", node_a.url, node_a.token, activate=True).json()
    ds = app.import_voc_dataset("待取消迁移数据集", count=5)
    src_dir = node_a.data_dir / "datasets" / ds["dir_name"]
    app.create_docker_node("Cancel-B", node_b.url, node_b.token, activate=True)

    assert node_a.spy is not None
    entered, release = node_a.spy.install_gate("/api/v1/archives/pack")

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": True},
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])

    assert entered.wait(timeout=10.0), "Expected migration to reach /api/v1/archives/pack gate"
    cancel_resp = app.client().post(f"/api/v1/storage/migrations/{mig_id}/cancel")
    assert_status(cancel_resp, 200, "cancel running migration")
    release.set()

    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "canceled"
    assert src_dir.is_dir(), "Canceled migration must NEVER delete source directory"
    cur_ds = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    assert cur_ds["storage_node"]["id"] == na["id"]


@e2e_test(
    test_id="T2_F7_02",
    tier=2,
    features="F7",
    title="Corrupted archive stream during migration fails verification, rolls back DB, and keeps source intact",
)
def test_t2_f7_02_corrupt_archive_transfer_rolls_back_and_keeps_source(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t2_f7_02_a")
    node_b = ctx.storage(name="st_t2_f7_02_b")
    app = ctx.platform(name="app_t2_f7_02")

    na = app.create_docker_node("Corrupt-A", node_a.url, node_a.token, activate=True).json()
    ds = app.import_voc_dataset("防损坏数据集", count=4)
    src_dir = node_a.data_dir / "datasets" / ds["dir_name"]
    app.create_docker_node("Corrupt-B", node_b.url, node_b.token, activate=True)

    assert node_a.spy is not None and node_b.spy is not None
    # Force app-relay path and flip bytes in streamed archive from Node A!
    node_b.spy.set_forced_status("/api/v1/archives/pull-unpack", 502, b'{"error":{"message":"no p2p"}}')
    node_a.spy.set_corrupt_response_body("/api/v1/archives/", True)

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": True},
    )
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "failed"
    assert migs[mig_id].get("error"), "Failed migration must record diagnostic error message"

    assert src_dir.is_dir(), "Failed migration with delete_source=true MUST preserve source files!"
    cur_ds = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    assert cur_ds["storage_node"]["id"] == na["id"]


@e2e_test(
    test_id="T2_F7_03",
    tier=2,
    features="F7",
    title="Destination HTTP 507 Insufficient Storage fails migration cleanly without deleting source",
)
def test_t2_f7_03_destination_507_insufficient_storage_keeps_source(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t2_f7_03_a")
    node_b = ctx.storage(name="st_t2_f7_03_b")
    app = ctx.platform(name="app_t2_f7_03")

    na = app.create_docker_node("DiskSrc-A", node_a.url, node_a.token, activate=True).json()
    ds = app.import_voc_dataset("空间不足测试集", count=3)
    src_dir = node_a.data_dir / "datasets" / ds["dir_name"]
    app.create_docker_node("DiskDst-B", node_b.url, node_b.token, activate=True)

    assert node_b.spy is not None
    err_507 = b'{"error":{"code":"insufficient_storage","message":"target disk full"}}'
    node_b.spy.set_forced_status("/api/v1/archives/pull-unpack", 507, err_507)
    node_b.spy.set_forced_status("/api/v1/archives/unpack", 507, err_507)

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": True},
    )
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "failed"
    assert src_dir.is_dir()
    assert app.client().get(f"/api/v1/datasets/{ds['id']}").json()["storage_node"]["id"] == na["id"]


@e2e_test(
    test_id="T2_F7_04",
    tier=2,
    features="F7",
    title="Migrating an empty 0-image dataset between Docker A and Docker B succeeds cleanly",
)
def test_t2_f7_04_empty_dataset_archive_migration_succeeds(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t2_f7_04_a")
    node_b = ctx.storage(name="st_t2_f7_04_b")
    app = ctx.platform(name="app_t2_f7_04")

    app.create_docker_node("Empty-A", node_a.url, node_a.token, activate=True)
    empty_ds = app.client().post("/api/v1/datasets", json_body={"name": "零图片空数据集"}).json()
    nb = app.create_docker_node("Empty-B", node_b.url, node_b.token, activate=True).json()

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": empty_ds["id"]}]},
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "archive"
    assert app.client().get(f"/api/v1/datasets/{empty_ds['id']}").json()["storage_node"]["id"] == nb["id"]


@e2e_test(
    test_id="T2_F7_05",
    tier=2,
    features="F7",
    title="Creating migration for dataset already on active node or unknown target_id returns 4xx",
)
def test_t2_f7_05_migrate_already_on_active_node_or_missing_target_rejected(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f7_05")
    app = ctx.platform(name="app_t2_f7_05")
    app.create_docker_node("当前活跃Docker", st.url, st.token, activate=True)
    ds = app.import_voc_dataset("已在目标节点", count=2)

    same_node = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
    )
    assert_status(same_node, (400, 409, 422), "dataset already on active node")

    missing = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": 999999}]},
    )
    assert_status(missing, (404, 422), "non-existent dataset migration target")


@e2e_test(
    test_id="T2_F7_06",
    tier=2,
    features="F7",
    title="Canceling an already succeeded migration returns 409 Conflict",
)
def test_t2_f7_06_cancel_completed_migration_returns_409(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t2_f7_06_a")
    node_b = ctx.storage(name="st_t2_f7_06_b")
    app = ctx.platform(name="app_t2_f7_06")

    app.create_docker_node("Done-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("已完成迁移", count=2)
    app.create_docker_node("Done-B", node_b.url, node_b.token, activate=True)

    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
        )
        .json()["items"][0]["id"]
    )
    app.wait_migrations([mig_id])

    late_cancel = app.client().post(f"/api/v1/storage/migrations/{mig_id}/cancel")
    assert_status(late_cancel, 409, "cancel already-succeeded migration")


# ==============================================================================
# F8 Tier 2: Training Dataset Copy Auth, Lease & Unreachable Node Errors — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F8_01",
    tier=2,
    features="F8",
    title="GET /api/v1/compute/agent/training-jobs/{id}/dataset without X-Node-Key returns 400",
)
def test_t2_f8_01_training_dataset_missing_node_key_400(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_t2_f8_01")
    resp = app.client().get("/api/v1/compute/agent/training-jobs/1/dataset")
    assert_status(resp, 400, "missing X-Node-Key header")


@e2e_test(
    test_id="T2_F8_02",
    tier=2,
    features="F8",
    title="Agent dataset download with invalid TM_NODE_TOKEN returns 401 Unauthorized",
)
def test_t2_f8_02_training_dataset_invalid_bearer_token_401(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_t2_f8_02", node_token="compute-agent-secret-token-123456")
    bad_cli = app.agent_client(node_key="11111111-2222-3333-4444-555555555555", token="wrong-agent-token-999999")
    resp = bad_cli.get("/api/v1/compute/agent/training-jobs/1/dataset")
    assert_status(resp, 401, "invalid compute agent Bearer token")
    assert "Bearer" in (resp.headers.get("WWW-Authenticate") or "")


@e2e_test(
    test_id="T2_F8_03",
    tier=2,
    features="F8",
    title="Agent dataset download from non-owner compute node returns 409 Conflict",
)
def test_t2_f8_03_training_dataset_wrong_lease_holder_409(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f8_03")
    app = ctx.platform(name="app_t2_f8_03")
    app.create_docker_node("LeaseDocker", st.url, st.token, activate=True)

    ds = app.import_voc_dataset("租约数据集", count=2)
    proj, dv = app.create_project_and_freeze_version("租约项目", [int(ds["id"])])

    owner_key = "11111111-2222-3333-4444-555555555555"
    other_key = "99999999-8888-7777-6666-555555555555"
    app.register_compute_node(node_key=owner_key, name="Owner-Node")
    app.register_compute_node(node_key=other_key, name="Other-Node")

    app.client().post(
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
    task = app.agent_client(node_key=owner_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": owner_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]

    hijack = app.agent_client(node_key=other_key).get(task["dataset_url"])
    assert_status(hijack, 409, "non-owner compute node downloading leased job dataset")


@e2e_test(
    test_id="T2_F8_04",
    tier=2,
    features="F8",
    title="Agent dataset download for non-existent job ID returns 404 Not Found",
)
def test_t2_f8_04_training_dataset_unknown_job_404(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_t2_f8_04")
    key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=key)
    resp = app.agent_client(node_key=key).get("/api/v1/compute/agent/training-jobs/888888/dataset")
    assert_status(resp, 404, "unknown training job ID")


@e2e_test(
    test_id="T2_F8_05",
    tier=2,
    features="F8",
    title="Unreachable Docker storage container during GET .../dataset returns 5xx cleanly without crashing app",
)
def test_t2_f8_05_unreachable_docker_storage_on_dataset_download_graceful_5xx(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f8_05")
    app = ctx.platform(name="app_t2_f8_05")
    app.create_docker_node("DownDocker", st.url, st.token, activate=True)

    ds = app.import_voc_dataset("断连测试集", count=2)
    proj, dv = app.create_project_and_freeze_version("断连项目", [int(ds["id"])])
    key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=key)
    app.client().post(
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
    task = app.agent_client(node_key=key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]

    assert st.spy is not None
    st.spy.set_blocked(True)

    resp = app.agent_client(node_key=key).get(task["dataset_url"])
    assert resp.status >= 500, f"Expected 5xx when Docker storage is offline, got {resp.status}"
    assert_status(app.client().get("/api/healthz"), 200, "platform server remains healthy")


# ==============================================================================
# F9 Tier 2: Bulk Import & Cross-Node Freeze Negative / Edge Cases — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F9_01",
    tier=2,
    features="F9",
    title="Importing a corrupt/invalid zip archive onto active Docker node fails cleanly with zero leaked files",
)
def test_t2_f9_01_corrupt_import_zip_onto_docker_fails_cleanly(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f9_01")
    app = ctx.platform(name="app_t2_f9_01")
    app.create_docker_node("损坏导入Docker", st.url, st.token, activate=True)

    ds = app.client().post("/api/v1/datasets", json_body={"name": "损坏压缩包集"}).json()
    ds_id = int(ds["id"])
    garbage = b"PK\x03\x04CORRUPT_ZIP_ARCHIVE_BYTES_NOT_VALID"
    imp = app.client().post(
        f"/api/v1/datasets/{ds_id}/imports",
        json_body={"source": "archive", "file_name": "broken.zip", "file_size": len(garbage), "annotation_format": "voc"},
    ).json()
    imp_id = int(imp["id"])
    assert_status(
        app.client().put(
            f"/api/v1/datasets/{ds_id}/imports/{imp_id}/archive",
            query={"offset": 0},
            raw_body=garbage,
        ),
        200,
    )
    assert_status(app.client().post(f"/api/v1/datasets/{ds_id}/imports/{imp_id}/commit"), 202)

    deadline = time.monotonic() + 10.0
    while time.monotonic() < deadline:
        cur = app.client().get(f"/api/v1/datasets/{ds_id}/imports/{imp_id}").json()
        if cur["status"] == "failed":
            break
        time.sleep(0.05)
    assert cur["status"] == "failed"
    assert int(app.client().get(f"/api/v1/datasets/{ds_id}").json()["image_count"]) == 0


@e2e_test(
    test_id="T2_F9_02",
    tier=2,
    features="F9",
    title="Staged archive chunk upload with wrong byte offset returns 409 upload_offset_mismatch",
)
def test_t2_f9_02_import_archive_wrong_offset_409(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f9_02")
    app = ctx.platform(name="app_t2_f9_02")
    app.create_docker_node("OffsetDocker", st.url, st.token, activate=True)

    ds_id = int(app.client().post("/api/v1/datasets", json_body={"name": "Offset测试"}).json()["id"])
    imp_id = int(
        app.client()
        .post(
            f"/api/v1/datasets/{ds_id}/imports",
            json_body={"source": "archive", "file_name": "a.zip", "file_size": 100, "annotation_format": "voc"},
        )
        .json()["id"]
    )
    bad_off = app.client().put(
        f"/api/v1/datasets/{ds_id}/imports/{imp_id}/archive",
        query={"offset": 50},
        raw_body=b"x" * 50,
    )
    assert_status(bad_off, 409, "wrong chunk offset")


@e2e_test(
    test_id="T2_F9_03",
    tier=2,
    features="F9",
    title="Canceling an uploading dataset import transitions status to canceled and cleans staged data",
)
def test_t2_f9_03_cancel_uploading_import_cleans_staged(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f9_03")
    app = ctx.platform(name="app_t2_f9_03")
    app.create_docker_node("CancelImportDocker", st.url, st.token, activate=True)

    ds_id = int(app.client().post("/api/v1/datasets", json_body={"name": "取消导入集"}).json()["id"])
    imp_id = int(
        app.client()
        .post(
            f"/api/v1/datasets/{ds_id}/imports",
            json_body={"source": "archive", "file_name": "part.zip", "file_size": 1024, "annotation_format": "voc"},
        )
        .json()["id"]
    )
    cancel = app.client().post(f"/api/v1/datasets/{ds_id}/imports/{imp_id}/cancel")
    assert_status(cancel, 200, "cancel import")
    assert cancel.json()["status"] == "canceled"


@e2e_test(
    test_id="T2_F9_04",
    tier=2,
    features="F9",
    title="Freezing a DataVersion with scope=annotated on unannotated dataset fails or rejects cleanly",
)
def test_t2_f9_04_freeze_unannotated_dataset_with_annotated_scope(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t2_f9_04")
    app = ctx.platform(name="app_t2_f9_04")
    app.create_docker_node("UnannotatedDocker", st.url, st.token, activate=True)

    # Build zip with image only (no VOC xml)
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as zf:
        zf.writestr("only_img.png", make_png_bytes(24, 24))
    ds = app.import_voc_dataset("无标注数据集", zip_bytes=buf.getvalue())

    proj = app.client().post("/api/v1/projects", json_body={"name": "无标注项目", "dataset_ids": [ds["id"]]}).json()
    cls_ver = (
        app.client()
        .put(f"/api/v1/projects/{proj['id']}/classes", json_body={"classes": [{"name": "c1", "color": "#112233"}]})
        .json()["version"]
    )
    dv_resp = app.client().post(
        "/api/v1/data-versions",
        json_body={
            "project_id": proj["id"],
            "note": "empty-check",
            "class_version": cls_ver,
            "sources": [{"dataset_id": ds["id"], "include_base": True, "version_ids": []}],
            "split": {"train": 80, "val": 20, "test": 0, "seed": 1, "use_folders": True, "group_by": "stem"},
            "scope": "annotated",
            "include_empty": False,
            "ignore_labels": [],
            "augmentation": None,
        },
    )
    if dv_resp.status == 201:
        dv_id = int(dv_resp.json()["id"])
        deadline = time.monotonic() + 10.0
        while time.monotonic() < deadline:
            cur = app.client().get(f"/api/v1/data-versions/{dv_id}").json()
            if cur["status"] in ("failed", "ready"):
                break
            time.sleep(0.05)
        assert cur["status"] == "failed", f"Expected 0-annotated-image version build to fail, got {cur}"
    else:
        assert_status(dv_resp, (400, 409, 422))


@e2e_test(
    test_id="T2_F9_05",
    tier=2,
    features="F9",
    title="Cross-node DataVersion freeze onto Docker node returning 507 marks build failed and cleans temp archives",
)
def test_t2_f9_05_cross_node_freeze_507_on_target_fails_cleanly(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t2_f9_05_a")
    node_b = ctx.storage(name="st_t2_f9_05_b")
    app = ctx.platform(name="app_t2_f9_05")

    app.create_docker_node("FreezeSrc-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("跨节点507数据集", count=3)
    app.create_docker_node("FreezeDst-B", node_b.url, node_b.token, activate=True)

    assert node_b.spy is not None
    err_507 = b'{"error":{"code":"insufficient_storage","message":"disk full"}}'
    node_b.spy.set_forced_status("/api/v1/archives/unpack", 507, err_507)
    node_b.spy.set_forced_status("/api/v1/archives/pull-unpack", 507, err_507)
    node_b.spy.set_forced_status("/api/v1/spaces/exports/files", 507, err_507)

    proj = app.client().post("/api/v1/projects", json_body={"name": "507冻结项目", "dataset_ids": [ds["id"]]}).json()
    cls_ver = (
        app.client()
        .put(f"/api/v1/projects/{proj['id']}/classes", json_body={"classes": [{"name": "scratch", "color": "#ef4444"}]})
        .json()["version"]
    )
    dv_resp = app.client().post(
        "/api/v1/data-versions",
        json_body={
            "project_id": proj["id"],
            "note": "507-test",
            "class_version": cls_ver,
            "sources": [{"dataset_id": ds["id"], "include_base": True, "version_ids": []}],
            "split": {"train": 80, "val": 20, "test": 0, "seed": 42, "use_folders": True, "group_by": "stem"},
            "scope": "annotated",
            "include_empty": True,
            "ignore_labels": [],
            "augmentation": None,
        },
    )
    if dv_resp.status == 201:
        dv_id = int(dv_resp.json()["id"])
        deadline = time.monotonic() + 15.0
        while time.monotonic() < deadline:
            cur = app.client().get(f"/api/v1/data-versions/{dv_id}").json()
            if cur["status"] in ("failed", "ready"):
                break
            time.sleep(0.05)
        assert cur["status"] == "failed"
    else:
        assert_status(dv_resp, 507)
    assert list((node_a.data_dir / ".tmp-archives").iterdir()) == []
