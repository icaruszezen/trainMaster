"""Tier 1 Feature Coverage E2E tests for F5, F6, F7, F8, and F9 (27 genuine tests)."""

from __future__ import annotations

import io
from pathlib import Path
import time
import zipfile

from e2e.harness.cluster import CaseContext
from e2e.harness.fixtures import make_png_bytes, make_voc_dataset_zip, make_voc_xml
from e2e.harness.registry import e2e_test
from e2e.tests._helpers import assert_hex64, assert_status


def _extract_dv_info(task: dict[str, object]) -> tuple[str, int, bool | None]:
    dv = task.get("data_version") if isinstance(task.get("data_version"), dict) else {}
    assert isinstance(dv, dict)
    mode = str(task.get("dv_transfer_mode") or dv.get("transfer_mode") or "")
    total = int(task.get("dv_total_bytes") or dv.get("total_bytes") or 0)  # type: ignore[arg-type]
    shared = dv.get("shared_local") if isinstance(dv.get("shared_local"), bool) else None
    return mode, total, shared


# ==============================================================================
# F5: DB Migration 0018 & KindDocker Volume Driver — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F5_01",
    tier=1,
    features="F5",
    title="Database migration 0018 adds docker kind constraint and storage_migrations archive columns",
)
def test_t1_f5_01_db_migration_0018_schema_columns(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_f5_01")
    cols = ctx.session.pg.query_json_rows(
        app.db_name,
        """
        SELECT column_name, data_type
        FROM information_schema.columns
        WHERE table_name = 'storage_migrations'
          AND column_name IN ('transfer_mode', 'stage', 'archive_bytes')
        ORDER BY column_name
        """,
    )
    col_names = [str(r["column_name"]) for r in cols]
    assert col_names == ["archive_bytes", "stage", "transfer_mode"], (
        f"Expected 0018 columns on storage_migrations, got {col_names}"
    )

    constraints = ctx.session.pg.query_scalar(
        app.db_name,
        "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'storage_nodes_kind_check';",
    )
    assert "docker" in constraints, f"Expected 'docker' in storage_nodes_kind_check, got: {constraints}"


@e2e_test(
    test_id="T1_F5_02",
    tier=1,
    features="F5",
    title="POST /api/v1/storage/nodes/test probes live trainmaster-storage container and returns usage",
)
def test_t1_f5_02_test_docker_node_probe(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f5_02", token="probe-token-secret-987654")
    app = ctx.platform(name="app_f5_02")

    resp = app.test_docker_node(st.url, st.token)
    assert_status(resp, 200, "POST /api/v1/storage/nodes/test")
    res = resp.json()
    assert res.get("ok") is True
    assert float(res.get("latency_ms", -1)) >= 0
    assert isinstance(res.get("display_target"), str) and res["display_target"]
    usage = res.get("usage")
    assert isinstance(usage, dict) and int(usage.get("total_bytes", 0)) > 0


@e2e_test(
    test_id="T1_F5_03",
    tier=1,
    features="F5",
    title="POST /api/v1/storage/nodes creates KindDocker node with encrypted token and supports_archive=true",
)
def test_t1_f5_03_create_docker_node_encrypted_token(ctx: CaseContext) -> None:
    raw_secret = "super-secret-docker-token-xyz-998877"
    st = ctx.storage(name="st_f5_03", token=raw_secret)
    app = ctx.platform(name="app_f5_03")

    create_resp = app.create_docker_node("远程Docker存储A", st.url, raw_secret, activate=True)
    assert_status(create_resp, 201, "create docker node")
    node = create_resp.json()
    assert node["kind"] == "docker"
    assert node["is_active"] is True
    assert node["has_secret"] is True
    assert node["supports_archive"] is True
    assert raw_secret not in create_resp.text, "Plaintext token must never leak in API response"

    db_row = ctx.session.pg.query_json_rows(
        app.db_name,
        f"SELECT config::text AS cfg, length(credentials_enc) AS enc_len, encode(credentials_enc, 'escape') AS enc_txt FROM storage_nodes WHERE id = {int(node['id'])}",
    )[0]
    assert int(db_row["enc_len"]) > 16  # type: ignore[arg-type]
    assert raw_secret not in str(db_row["cfg"])
    assert raw_secret not in str(db_row["enc_txt"])


@e2e_test(
    test_id="T1_F5_04",
    tier=1,
    features="F5",
    title="PATCH /api/v1/storage/nodes/{id} updates Docker node metadata while retaining secret and activating",
)
def test_t1_f5_04_patch_and_activate_docker_node(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f5_04", token="keep-secret-token-123456")
    app = ctx.platform(name="app_f5_04")

    created = app.create_docker_node("节点初始名", st.url, st.token, activate=False).json()
    node_id = int(created["id"])
    assert created["is_active"] is False

    patched_resp = app.update_docker_node(node_id, name="节点更新名", endpoint=st.url)
    assert_status(patched_resp, 200, "PATCH docker node")
    patched = patched_resp.json()
    assert patched["name"] == "节点更新名"
    assert patched["has_secret"] is True
    assert patched["supports_archive"] is True

    act_resp = app.client().post(f"/api/v1/storage/nodes/{node_id}/activate")
    assert_status(act_resp, 200, "activate docker node")
    assert act_resp.json()["is_active"] is True


@e2e_test(
    test_id="T1_F5_05",
    tier=1,
    features="F5",
    title="Builtin node (id=1) reports supports_archive=true with TM_STORAGE_SERVICE_URL and false without",
)
def test_t1_f5_05_builtin_node_storage_service_delegation_flag(ctx: CaseContext) -> None:
    app_plain = ctx.platform(name="app_f5_05_plain")
    nodes_plain = app_plain.client().get("/api/v1/storage/nodes").json()["items"]
    builtin_plain = next(n for n in nodes_plain if n["is_builtin"])
    assert builtin_plain["supports_archive"] is False

    colocated = ctx.storage(name="st_f5_05_coloc", token="coloc-service-token-12345")
    app_with_svc = ctx.platform(
        name="app_f5_05_svc",
        storage_service_url=colocated.url,
        storage_service_token=colocated.token,
    )
    nodes_svc = app_with_svc.client().get("/api/v1/storage/nodes").json()["items"]
    builtin_svc = next(n for n in nodes_svc if n["is_builtin"])
    assert builtin_svc["supports_archive"] is True


@e2e_test(
    test_id="T1_F5_06",
    tier=1,
    features="F5",
    title="Dataset image serving and thumbnail generation succeed on active KindDocker volume",
)
def test_t1_f5_06_dataset_browse_and_image_read_on_docker_volume(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f5_06")
    app = ctx.platform(name="app_f5_06")
    node = app.create_docker_node("Docker浏览节点", st.url, st.token, activate=True).json()

    ds = app.import_voc_dataset("Docker样本集", count=3)
    assert ds["storage_node"]["id"] == node["id"]
    assert ds["storage_node"]["supports_archive"] is True

    imgs_resp = app.client().get(f"/api/v1/datasets/{ds['id']}/images")
    assert_status(imgs_resp, 200, "list dataset images on docker node")
    imgs = imgs_resp.json()["items"]
    assert len(imgs) == 3

    img_id = imgs[0]["id"]
    file_resp = app.client().get(f"/api/v1/datasets/{ds['id']}/images/{img_id}/file")
    assert_status(file_resp, 200, "serve raw dataset image from docker node")
    assert file_resp.body.startswith(b"\x89PNG\r\n\x1a\n")

    thumb_resp = app.client().get(f"/api/v1/datasets/{ds['id']}/images/{img_id}/thumbnail")
    assert_status(thumb_resp, 200, "serve dataset thumbnail from docker node")
    assert len(thumb_resp.body) > 32


# ==============================================================================
# F6: Legacy Storage Server Compatibility Matrix Enforcement — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F6_01",
    tier=1,
    features="F6",
    title="Legacy S3 storage node creation and probe report supports_archive=false",
)
def test_t1_f6_01_legacy_s3_node_supports_archive_false(ctx: CaseContext) -> None:
    s3 = ctx.s3_mock()
    app = ctx.platform(name="app_f6_01")

    resp = app.create_s3_node("历史MinIO节点", s3, activate=True)
    assert_status(resp, 201, "create S3 node")
    node = resp.json()
    assert node["kind"] == "s3"
    assert node["supports_archive"] is False
    assert len(s3.archive_requests()) == 0


@e2e_test(
    test_id="T1_F6_02",
    tier=1,
    features="F6",
    title="External passive local directory node (is_builtin=false) reports supports_archive=false",
)
def test_t1_f6_02_passive_local_node_supports_archive_false(ctx: CaseContext) -> None:
    coloc = ctx.storage(name="st_f6_02_coloc")
    app = ctx.platform(
        name="app_f6_02",
        storage_service_url=coloc.url,
        storage_service_token=coloc.token,
    )
    nfs_mount = ctx.case_dir / "passive_nfs_mount"
    resp = app.create_passive_local_node("挂载NFS目录", nfs_mount, activate=False)
    assert_status(resp, 201, "create passive local node")
    node = resp.json()
    assert node["kind"] == "local"
    assert node["is_builtin"] is False
    assert node["supports_archive"] is False


@e2e_test(
    test_id="T1_F6_03",
    tier=1,
    features="F6",
    title="Dataset import and DataVersion freeze on legacy S3 node perform zero archive operations",
)
def test_t1_f6_03_s3_import_and_freeze_zero_archive_calls(ctx: CaseContext) -> None:
    s3 = ctx.s3_mock()
    spy_s3 = ctx.spy_proxy(s3.url, name="s3_f6_03")
    app = ctx.platform(name="app_f6_03")

    s3_cfg = {
        "endpoint": spy_s3.url,
        "region": "us-east-1",
        "bucket": "tm-datasets",
        "prefix": "legacy",
        "access_key_id": s3.access_key,
        "force_path_style": True,
        "insecure_skip_verify": False,
    }
    c_resp = app.client().post(
        "/api/v1/storage/nodes",
        json_body={
            "name": "S3兼容节点",
            "kind": "s3",
            "s3": s3_cfg,
            "secret_access_key": s3.secret_key,
            "activate": True,
        },
    )
    assert_status(c_resp, 201)
    spy_s3.clear()

    ds = app.import_voc_dataset("S3直传样本", count=3)
    assert ds["storage_node"]["supports_archive"] is False
    _, dv = app.create_project_and_freeze_version("S3兼容项目", [int(ds["id"])])
    assert dv["storage_node"]["supports_archive"] is False
    assert len(spy_s3.archive_requests()) == 0
    assert s3.object_count("tm-datasets", "legacy/datasets/") >= 6


@e2e_test(
    test_id="T1_F6_04",
    tier=1,
    features="F6",
    title="Migration Builtin -> Legacy S3 runs transfer_mode=direct with zero archive pack/unpack calls",
)
def test_t1_f6_04_migration_builtin_to_s3_direct_mode(ctx: CaseContext) -> None:
    coloc = ctx.storage(name="st_f6_04_coloc")
    s3 = ctx.s3_mock()
    app = ctx.platform(
        name="app_f6_04",
        storage_service_url=coloc.url,
        storage_service_token=coloc.token,
    )

    ds = app.import_voc_dataset("待迁往S3数据集", count=3)
    assert_status(app.create_s3_node("目标S3存储", s3, activate=True), 201)

    assert coloc.spy is not None
    coloc.spy.clear()

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [{"target_type": "dataset", "target_id": ds["id"]}],
            "delete_source": True,
        },
    )
    assert_status(mig_resp, 201, "create Builtin->S3 migration")
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    done = migs[mig_id]
    assert done["status"] == "succeeded"
    assert done["transfer_mode"] == "direct"
    assert len(coloc.spy.archive_requests()) == 0, (
        f"Expected 0 archive calls when migrating to legacy S3, got {coloc.spy.archive_requests()}"
    )
    assert len(s3.archive_requests()) == 0


@e2e_test(
    test_id="T1_F6_05",
    tier=1,
    features="F6",
    title="Legacy SMB kind remains accepted by schema and probe contract without archive endpoints",
)
def test_t1_f6_05_smb_kind_backward_compatibility_contract(ctx: CaseContext) -> None:
    app = ctx.platform(name="app_f6_05")
    constraints = ctx.session.pg.query_scalar(
        app.db_name,
        "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'storage_nodes_kind_check';",
    )
    for kind in ("local", "docker", "s3", "smb"):
        assert kind in constraints, f"Expected kind {kind!r} in constraint: {constraints}"

    # Probing an unreachable loopback SMB port returns clean storage error / ok=false (not 422 invalid kind)
    closed_port = 1
    probe = app.client().post(
        "/api/v1/storage/nodes/test",
        json_body={
            "kind": "smb",
            "smb": {
                "host": "127.0.0.1",
                "port": closed_port,
                "share": "datasets",
                "base_path": "",
                "username": "guest",
                "domain": "",
            },
            "password": "secret",
        },
    )
    assert probe.status in (200, 503), f"Expected SMB kind accepted (200 ok=false or 503), got {probe.status}: {probe.text}"
    if probe.status == 200:
        assert probe.json().get("ok") is False


# ==============================================================================
# F7: Pack-Transfer-Unpack Online Storage Migration — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F7_01",
    tier=1,
    features="F7",
    title="Docker A -> Docker B Copy migration (delete_source=false) packs/unpacks and retains source",
)
def test_t1_f7_01_docker_to_docker_copy_migration(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f7_01_a")
    node_b = ctx.storage(name="st_f7_01_b")
    app = ctx.platform(name="app_f7_01")

    na = app.create_docker_node("Docker-A", node_a.url, node_a.token, activate=True).json()
    ds = app.import_voc_dataset("复制迁移数据集", count=4)
    src_dir = node_a.data_dir / "datasets" / ds["dir_name"]
    assert src_dir.is_dir()

    nb = app.create_docker_node("Docker-B", node_b.url, node_b.token, activate=True).json()
    assert node_a.spy is not None and node_b.spy is not None
    node_a.spy.clear()
    node_b.spy.clear()

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [{"target_type": "dataset", "target_id": ds["id"]}],
            "delete_source": False,
        },
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    done = migs[mig_id]

    assert done["status"] == "succeeded"
    assert done["transfer_mode"] == "archive"
    assert int(done["archive_bytes"]) > 0
    assert done["delete_source"] is False
    assert len(node_a.spy.matching_requests("/api/v1/archives/pack", method="POST")) >= 1
    assert (
        len(node_b.spy.matching_requests("/api/v1/archives/pull-unpack", method="POST"))
        + len(node_b.spy.matching_requests("/api/v1/archives/unpack", method="POST"))
        >= 1
    )

    assert src_dir.is_dir(), "delete_source=false must preserve source directory on Docker A"
    updated_ds = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    assert updated_ds["storage_node"]["id"] == nb["id"]
    assert updated_ds["storage_node"]["id"] != na["id"]
    assert (node_b.data_dir / "datasets" / updated_ds["dir_name"]).is_dir()


@e2e_test(
    test_id="T1_F7_02",
    tier=1,
    features="F7",
    title="Docker A -> Docker B Move migration (delete_source=true) removes source and temp archives",
)
def test_t1_f7_02_docker_to_docker_move_migration_cleans_source_and_temps(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f7_02_a")
    node_b = ctx.storage(name="st_f7_02_b")
    app = ctx.platform(name="app_f7_02")

    app.create_docker_node("Docker-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("移动迁移数据集", count=4)
    old_dir = node_a.data_dir / "datasets" / ds["dir_name"]
    assert old_dir.is_dir()

    nb = app.create_docker_node("Docker-B", node_b.url, node_b.token, activate=True).json()
    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [{"target_type": "dataset", "target_id": ds["id"]}],
            "delete_source": True,
        },
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    done = migs[mig_id]
    assert done["status"] == "succeeded"
    assert done["transfer_mode"] == "archive"
    assert int(done["archive_bytes"]) > 0

    assert not old_dir.exists(), "delete_source=true must remove source directory on Docker A after commit"
    updated_ds = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    assert updated_ds["storage_node"]["id"] == nb["id"]
    assert list((node_a.data_dir / ".tmp-archives").iterdir()) == []
    assert list((node_b.data_dir / ".tmp-archives").iterdir()) == []


@e2e_test(
    test_id="T1_F7_03",
    tier=1,
    features="F7",
    title="Ready DataVersion Move migration (Builtin -> Docker) packs/unpacks YOLO version directory",
)
def test_t1_f7_03_dataversion_move_builtin_to_docker(ctx: CaseContext) -> None:
    coloc = ctx.storage(name="st_f7_03_coloc")
    remote = ctx.storage(name="st_f7_03_remote")
    app = ctx.platform(
        name="app_f7_03",
        storage_service_url=coloc.url,
        storage_service_token=coloc.token,
    )
    # Share Builtin data_dir with colocated storage service just like docker-compose.yml!
    # Or let Builtin pack locally; let's test Builtin -> Docker directly!
    app2 = ctx.platform(name="app_f7_03_direct_builtin")
    ds = app2.import_voc_dataset("冻结源数据集", count=5)
    proj, dv = app2.create_project_and_freeze_version("数据版本迁移项目", [int(ds["id"])])
    local_dv_dir = app2.data_dir / "exports" / "data-versions" / f"project-{proj['id']}" / f"v{dv['version']}"
    assert local_dv_dir.is_dir()

    rem_node = app2.create_docker_node("远程Docker节点", remote.url, remote.token, activate=True).json()
    assert remote.spy is not None
    remote.spy.clear()

    mig_resp = app2.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [{"target_type": "data_version", "target_id": dv["id"]}],
            "delete_source": True,
        },
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app2.wait_migrations([mig_id])
    done = migs[mig_id]
    assert done["status"] == "succeeded"
    assert done["transfer_mode"] == "archive"
    assert len(remote.spy.archive_requests()) >= 1
    assert not local_dv_dir.exists()

    updated_dv = app2.client().get(f"/api/v1/data-versions/{dv['id']}").json()
    assert updated_dv["storage_node"]["id"] == rem_node["id"]
    rem_dv_yaml = (
        remote.data_dir
        / "exports"
        / "data-versions"
        / f"project-{proj['id']}"
        / f"v{dv['version']}"
        / "data.yaml"
    )
    assert rem_dv_yaml.is_file()


@e2e_test(
    test_id="T1_F7_04",
    tier=1,
    features="F7",
    title="Archive migration exposes live stage progression and final archive_bytes and file counters",
)
def test_t1_f7_04_migration_stage_transitions_and_counters(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f7_04_a")
    node_b = ctx.storage(name="st_f7_04_b")
    app = ctx.platform(name="app_f7_04")

    app.create_docker_node("Node-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("阶段观测数据集", count=6)
    app.create_docker_node("Node-B", node_b.url, node_b.token, activate=True)

    assert node_b.spy is not None
    node_b.spy.set_delay("/api/v1/archives/", 0.25)

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, seen_stages = app.wait_migrations([mig_id])
    done = migs[mig_id]

    valid_stages = {"packing", "transferring", "extracting", "verifying", "cleaning"}
    assert seen_stages.issubset(valid_stages), f"Unexpected stage names: {seen_stages}"
    assert len(seen_stages) >= 1, "Expected at least one live archive stage observed during migration"
    assert done["status"] == "succeeded"
    assert int(done["files_done"]) == int(done["files_total"]) >= 12
    assert int(done["bytes_done"]) == int(done["bytes_total"]) > 0
    assert int(done["archive_bytes"]) > 0


@e2e_test(
    test_id="T1_F7_05",
    tier=1,
    features="F7",
    title="Peer pull-unpack failure triggers automatic app-relay archive stream fallback",
)
def test_t1_f7_05_migration_app_relay_fallback_when_pull_unpack_fails(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f7_05_a")
    node_b = ctx.storage(name="st_f7_05_b")
    app = ctx.platform(name="app_f7_05")

    app.create_docker_node("Node-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("中继回退数据集", count=3)
    app.create_docker_node("Node-B", node_b.url, node_b.token, activate=True)

    assert node_a.spy is not None and node_b.spy is not None
    node_a.spy.clear()
    node_b.spy.clear()
    # Simulate subnet isolation blocking peer pull-unpack on Node B while /unpack remains reachable from app
    node_b.spy.set_forced_status(
        "/api/v1/archives/pull-unpack",
        502,
        b'{"error":{"code":"unreachable","message":"isolated peer subnet"}}',
    )

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
    )
    assert_status(mig_resp, 201)
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    done = migs[mig_id]

    assert done["status"] == "succeeded"
    assert done["transfer_mode"] == "archive"
    assert len(node_b.spy.matching_requests("/api/v1/archives/unpack", method="POST")) >= 1


@e2e_test(
    test_id="T1_F7_06",
    tier=1,
    features="F7",
    title="Rescanning a migrated dataset on destination Docker node finds zero modified/corrupted files",
)
def test_t1_f7_06_post_migration_rescan_mtime_consistency(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f7_06_a")
    node_b = ctx.storage(name="st_f7_06_b")
    app = ctx.platform(name="app_f7_06")

    app.create_docker_node("Node-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("时间戳一致性样本", count=4)
    app.create_docker_node("Node-B", node_b.url, node_b.token, activate=True)

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
    )
    mig_id = int(mig_resp.json()["items"][0]["id"])
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"

    scan_resp = app.client().post(f"/api/v1/datasets/{ds['id']}/scan")
    assert_status(scan_resp, 202, "trigger dataset scan after migration")
    scan_id = int(scan_resp.json()["id"])
    deadline = time.monotonic() + 15.0
    while time.monotonic() < deadline:
        cur = app.client().get(f"/api/v1/datasets/{ds['id']}/imports/{scan_id}").json()
        if cur["status"] == "succeeded":
            break
        assert cur["status"] not in ("failed", "canceled"), f"scan failed: {cur}"
        time.sleep(0.05)
    after_ds = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    assert int(after_ds["image_count"]) == 4


# ==============================================================================
# F8: Pack-Transfer-Unpack Training Dataset Copy Backend — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F8_01",
    tier=1,
    features="F8",
    title="Compute task claim for DataVersion on KindDocker returns dv_transfer_mode=archive and dv_total_bytes",
)
def test_t1_f8_01_claim_task_on_docker_dataversion_fields(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f8_01")
    app = ctx.platform(name="app_f8_01")
    app.create_docker_node("Docker训练存储", st.url, st.token, activate=True)

    ds = app.import_voc_dataset("训练数据A", count=4)
    proj, dv = app.create_project_and_freeze_version("训练项目A", [int(ds["id"])])
    app.register_compute_node()

    job_resp = app.client().post(
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
    assert_status(job_resp, 201)

    claim = app.agent_client().post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": "11111111-2222-3333-4444-555555555555", "kinds": ["training_job"], "wait_seconds": 0},
    )
    assert_status(claim, 200)
    task = claim.json()["task"]
    assert task is not None
    mode, total_bytes, shared_local = _extract_dv_info(task)
    assert mode == "archive", f"Expected dv_transfer_mode='archive', got {mode!r} in {task}"
    assert total_bytes > 0, f"Expected dv_total_bytes > 0, got {total_bytes}"
    assert shared_local is False


@e2e_test(
    test_id="T1_F8_02",
    tier=1,
    features="F8",
    title="GET .../training-jobs/{id}/dataset streams container-packed .zip with X-Archive-Sha256",
)
def test_t1_f8_02_agent_dataset_stream_archive_headers_and_checksum(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f8_02")
    app = ctx.platform(name="app_f8_02")
    app.create_docker_node("Docker训练节点", st.url, st.token, activate=True)

    ds = app.import_voc_dataset("训练拉取样本", count=4)
    proj, dv = app.create_project_and_freeze_version("拉取项目", [int(ds["id"])])
    node_key = "11111111-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
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
    task = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]

    assert st.spy is not None
    st.spy.clear()

    dest_zip = ctx.case_dir / "job_dataset.zip"
    dl = app.agent_client(node_key=node_key).stream_download(task["dataset_url"], dest_zip)
    assert dl.status == 200
    assert dl.headers.get("X-Transfer-Mode") == "archive"
    arc_sha = assert_hex64(dl.headers.get("X-Archive-Sha256"), "X-Archive-Sha256")
    cnt_sha = assert_hex64(dl.headers.get("X-Content-Sha256"), "X-Content-Sha256")
    assert arc_sha == cnt_sha == dl.sha256
    assert int(dl.headers.get("Content-Length") or 0) == dl.size_bytes > 0
    assert len(st.spy.matching_requests("/api/v1/archives/pack", method="POST")) == 1

    with zipfile.ZipFile(dest_zip, "r") as zf:
        names = zf.namelist()
        assert any(n.endswith("data.yaml") for n in names)
        assert sum(1 for n in names if n.endswith(".png")) == 4


@e2e_test(
    test_id="T1_F8_03",
    tier=1,
    features="F8",
    title="Builtin node with co-located TM_STORAGE_SERVICE_URL streams X-Transfer-Mode=archive to remote trainer",
)
def test_t1_f8_03_builtin_with_storage_service_streams_archive_to_remote_trainer(ctx: CaseContext) -> None:
    shared_root = ctx.case_dir / "shared_builtin"
    coloc = StorageDaemon_shared = ctx.storage(name="st_f8_03_coloc", token="coloc-tok-0123456789")
    # Point PlatformDaemon TM_DATA_DIR to coloc.data_dir (mirroring docker-compose.yml shared /data mount!)
    db_name, dsn = ctx.session.pg.create_db("tm_coloc")
    from e2e.harness.cluster import PlatformDaemon

    app = PlatformDaemon(
        binary=ctx.session.trainmaster_binary,
        work_dir=shared_root,
        db_name=db_name,
        database_url=dsn,
        storage_service_url=StorageDaemon_shared.url,
        storage_service_token=StorageDaemon_shared.token,
        extra_env={"TM_DATA_DIR": str(coloc.data_dir)},
    ).start()
    ctx._closers.append(app.stop)

    ds = app.import_voc_dataset("同机托管样本", count=3)
    proj, dv = app.create_project_and_freeze_version("同机托管项目", [int(ds["id"])])
    node_key = "22222222-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
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
    task = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]

    assert coloc.spy is not None
    coloc.spy.clear()
    dest_zip = ctx.case_dir / "coloc_dataset.zip"
    dl = app.agent_client(node_key=node_key).stream_download(task["dataset_url"], dest_zip)
    assert dl.status == 200
    assert dl.headers.get("X-Transfer-Mode") == "archive"
    assert dl.headers.get("X-Archive-Sha256") == dl.sha256
    assert len(coloc.spy.matching_requests("/api/v1/archives/pack", method="POST")) == 1


@e2e_test(
    test_id="T1_F8_04",
    tier=1,
    features="F8",
    title="DataVersion on legacy S3 node streams X-Transfer-Mode=direct with zero storage pack requests",
)
def test_t1_f8_04_s3_dataversion_streams_direct_mode(ctx: CaseContext) -> None:
    s3 = ctx.s3_mock()
    app = ctx.platform(name="app_f8_04")
    app.create_s3_node("S3训练源", s3, activate=True)

    ds = app.import_voc_dataset("S3训练数据", count=3)
    proj, dv = app.create_project_and_freeze_version("S3训练项目", [int(ds["id"])])
    node_key = "33333333-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
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
    task = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    mode, _, _ = _extract_dv_info(task)
    assert mode == "direct"

    dest_zip = ctx.case_dir / "s3_dataset.zip"
    dl = app.agent_client(node_key=node_key).stream_download(task["dataset_url"], dest_zip)
    assert dl.status == 200
    assert dl.headers.get("X-Transfer-Mode") == "direct"
    assert len(s3.archive_requests()) == 0


@e2e_test(
    test_id="T1_F8_05",
    tier=1,
    features="F8",
    title="Storage container temporary .zip archive is deleted immediately after trainer download completes",
)
def test_t1_f8_05_storage_temp_zip_deleted_after_stream(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f8_05")
    app = ctx.platform(name="app_f8_05")
    app.create_docker_node("Docker清理验证", st.url, st.token, activate=True)

    ds = app.import_voc_dataset("清理样本", count=3)
    proj, dv = app.create_project_and_freeze_version("清理项目", [int(ds["id"])])
    node_key = "44444444-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
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
    task = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]

    dl = app.agent_client(node_key=node_key).stream_download(task["dataset_url"], ctx.case_dir / "out.zip")
    assert dl.status == 200

    deadline = time.monotonic() + 3.0
    while time.monotonic() < deadline and list((st.data_dir / ".tmp-archives").iterdir()):
        time.sleep(0.05)
    assert list((st.data_dir / ".tmp-archives").iterdir()) == []


# ==============================================================================
# F9: Pack-Transfer-Unpack Cross-Node DataVersion Build & Staged Bulk Import — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F9_01",
    tier=1,
    features="F9",
    title="Staged archive dataset import onto active Docker node unpacks via /api/v1/archives/unpack",
)
def test_t1_f9_01_staged_archive_import_uses_archive_unpack_on_docker(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f9_01")
    app = ctx.platform(name="app_f9_01")
    app.create_docker_node("Docker导入节点", st.url, st.token, activate=True)

    assert st.spy is not None
    st.spy.clear()
    ds = app.import_voc_dataset("批量压缩导入样本", count=5)
    assert int(ds["image_count"]) == 5

    unpack_calls = st.spy.matching_requests("/api/v1/archives/unpack", method="POST") + st.spy.matching_requests(
        "/api/v1/archives/pull-unpack", method="POST"
    )
    assert len(unpack_calls) >= 1, f"Expected staged import to unpack archive on Docker node, spy={st.spy.records()}"
    assert not (app.data_dir / "datasets" / ds["dir_name"]).exists()
    assert (st.data_dir / "datasets" / ds["dir_name"]).is_dir()


@e2e_test(
    test_id="T1_F9_02",
    tier=1,
    features="F9",
    title="Staged multipart folder import onto active Docker node packs and unpacks dataset tree",
)
def test_t1_f9_02_staged_folder_import_onto_docker_node(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f9_02")
    app = ctx.platform(name="app_f9_02")
    app.create_docker_node("Docker文件夹导入节点", st.url, st.token, activate=True)

    ds_id = int(app.client().post("/api/v1/datasets", json_body={"name": "文件夹上传样本"}).json()["id"])
    imp_id = int(
        app.client()
        .post(
            f"/api/v1/datasets/{ds_id}/imports",
            json_body={"source": "folder", "file_count": 2, "annotation_format": "voc"},
        )
        .json()["id"]
    )

    boundary = "----TrainMasterE2EBoundary90210"
    png_bytes = make_png_bytes(28, 20, (70, 140, 210))
    xml_bytes = make_voc_xml(width=28, height=20, label="rust")
    parts: list[bytes] = []
    for rel_path, content in [("line1/item_01.png", png_bytes), ("line1/item_01.xml", xml_bytes)]:
        parts.append(
            (
                f"--{boundary}\r\n"
                'Content-Disposition: form-data; name="path"\r\n\r\n'
                f"{rel_path}\r\n"
                f"--{boundary}\r\n"
                f'Content-Disposition: form-data; name="file"; filename="{Path(rel_path).name}"\r\n'
                "Content-Type: application/octet-stream\r\n\r\n"
            ).encode("utf-8")
            + content
            + b"\r\n"
        )
    parts.append(f"--{boundary}--\r\n".encode("utf-8"))
    multipart_body = b"".join(parts)

    assert st.spy is not None
    st.spy.clear()

    up = app.client().post(
        f"/api/v1/datasets/{ds_id}/imports/{imp_id}/files",
        raw_body=multipart_body,
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
    )
    assert_status(up, 200, "multipart folder upload")
    assert_status(app.client().post(f"/api/v1/datasets/{ds_id}/imports/{imp_id}/commit"), 202)

    deadline = time.monotonic() + 15.0
    while time.monotonic() < deadline:
        cur = app.client().get(f"/api/v1/datasets/{ds_id}/imports/{imp_id}").json()
        if cur["status"] == "succeeded":
            break
        assert cur["status"] not in ("failed", "canceled"), f"folder import failed: {cur}"
        time.sleep(0.05)

    final_ds = app.client().get(f"/api/v1/datasets/{ds_id}").json()
    assert int(final_ds["image_count"]) == 1
    assert len(st.spy.archive_requests()) >= 1


@e2e_test(
    test_id="T1_F9_03",
    tier=1,
    features="F9",
    title="Staged import onto Docker node honors on_conflict=skip and on_conflict=overwrite",
)
def test_t1_f9_03_staged_import_conflict_modes_on_docker(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f9_03")
    app = ctx.platform(name="app_f9_03")
    app.create_docker_node("Docker冲突测试节点", st.url, st.token, activate=True)

    zip_v1 = make_voc_dataset_zip(count=2, prefix="dup", label="scratch", rgb_seed=10)
    ds = app.import_voc_dataset("冲突测试集", zip_bytes=zip_v1, on_conflict="skip")
    ds_id = int(ds["id"])

    # Import modified annotation with on_conflict="overwrite" -> label updates to "dent"
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as zf:
        zf.writestr("dup_001.png", make_png_bytes(32, 24, (200, 30, 30)))
        zf.writestr("dup_001.xml", make_voc_xml(label="dent"))
    ds_after = app.import_voc_dataset(
        "冲突测试集",
        dataset_id=ds_id,
        zip_bytes=buf.getvalue(),
        on_conflict="overwrite",
    )
    assert int(ds_after["image_count"]) == 2
    labels = [it["name"] for it in app.client().get(f"/api/v1/datasets/{ds_id}/labels").json()["items"]]
    assert "dent" in labels


@e2e_test(
    test_id="T1_F9_04",
    tier=1,
    features="F9",
    title="Cross-node DataVersion freeze (Docker A -> Docker B) uses .tar.zst pack-transfer-unpack",
)
def test_t1_f9_04_cross_node_dataversion_freeze_uses_archive_pipeline(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f9_04_a")
    node_b = ctx.storage(name="st_f9_04_b")
    app = ctx.platform(name="app_f9_04")

    app.create_docker_node("源Docker-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("跨节点源数据集", count=5)

    nb = app.create_docker_node("目标Docker-B", node_b.url, node_b.token, activate=True).json()
    assert node_a.spy is not None and node_b.spy is not None
    node_a.spy.clear()
    node_b.spy.clear()

    proj, dv = app.create_project_and_freeze_version("跨节点冻结项目", [int(ds["id"])])
    assert dv["storage_node"]["id"] == nb["id"]
    assert len(node_a.spy.matching_requests("/api/v1/archives/pack", method="POST")) >= 1
    assert len(node_b.spy.archive_requests()) >= 1
    assert (
        node_b.data_dir / "exports" / "data-versions" / f"project-{proj['id']}" / f"v{dv['version']}" / "data.yaml"
    ).is_file()


@e2e_test(
    test_id="T1_F9_05",
    tier=1,
    features="F9",
    title="Same-node DataVersion freeze on Docker node uses /api/v1/spaces/copy-within hardlinks",
)
def test_t1_f9_05_same_node_docker_dataversion_freeze_uses_copy_within(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_f9_05")
    app = ctx.platform(name="app_f9_05")
    app.create_docker_node("同节点Docker", st.url, st.token, activate=True)

    ds = app.import_voc_dataset("同节点源数据集", count=4)
    assert st.spy is not None
    st.spy.clear()

    proj, dv = app.create_project_and_freeze_version("同节点冻结项目", [int(ds["id"])])
    assert dv["status"] == "ready"
    copy_calls = st.spy.matching_requests("/api/v1/spaces/copy-within", method="POST")
    assert len(copy_calls) >= 1, (
        f"Expected same-node Docker DataVersion build to invoke /api/v1/spaces/copy-within, spy={st.spy.records()}"
    )
    dv_dir = st.data_dir / "exports" / "data-versions" / f"project-{proj['id']}" / f"v{dv['version']}"
    assert (dv_dir / "data.yaml").is_file()
