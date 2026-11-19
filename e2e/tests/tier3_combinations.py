"""Tier 3 Cross-Feature Pairwise and Multi-Feature Combination E2E tests (14 genuine tests)."""

from __future__ import annotations

import io
import time
import zipfile

from e2e.harness.cluster import REPO_ROOT, CaseContext, PlatformDaemon, run_trainer_job_in_subprocess
from e2e.harness.fixtures import make_png_bytes, make_voc_dataset_zip, make_voc_xml, snapshot_tree
from e2e.harness.registry import e2e_test
from e2e.tests._helpers import assert_status, read_all_web_bundle_text


@e2e_test(
    test_id="T3_01",
    tier=3,
    features=["F1", "F2", "F5", "F7"],
    title="Docker A -> Docker B Dataset Copy (delete_source=false) via peer pull-unpack preserves both copies",
)
def test_t3_01_docker_to_docker_copy_peer_pull_unpack(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t3_01_a", token="tok-peer-a-0123456789")
    node_b = ctx.storage(name="st_t3_01_b", token="tok-peer-b-0123456789")
    app = ctx.platform(name="app_t3_01")

    app.create_docker_node("Peer-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("双容器复制集", count=4)
    snap_before = snapshot_tree(node_a.data_dir / "datasets" / ds["dir_name"])

    nb = app.create_docker_node("Peer-B", node_b.url, node_b.token, activate=True).json()
    assert node_a.spy is not None and node_b.spy is not None
    node_a.spy.clear()
    node_b.spy.clear()

    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": False},
        )
        .json()["items"][0]["id"]
    )
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "archive"
    assert len(node_b.spy.matching_requests("/api/v1/archives/pull-unpack", method="POST")) == 1

    updated = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    assert updated["storage_node"]["id"] == nb["id"]
    snap_a_after = snapshot_tree(node_a.data_dir / "datasets" / ds["dir_name"])
    snap_b_after = snapshot_tree(node_b.data_dir / "datasets" / updated["dir_name"])
    assert {k: v.sha256 for k, v in snap_before.items()} == {k: v.sha256 for k, v in snap_a_after.items()}
    assert {k: v.sha256 for k, v in snap_before.items()} == {k: v.sha256 for k, v in snap_b_after.items()}


@e2e_test(
    test_id="T3_02",
    tier=3,
    features=["F1", "F3", "F5", "F7"],
    title="Docker A -> Docker B Dataset Move (delete_source=true) via app-relay fallback deletes source post-commit",
)
def test_t3_02_docker_to_docker_move_app_relay_fallback(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t3_02_a")
    node_b = ctx.storage(name="st_t3_02_b")
    app = ctx.platform(name="app_t3_02")

    app.create_docker_node("Relay-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("中继移动集", count=4)
    old_dir = node_a.data_dir / "datasets" / ds["dir_name"]

    app.create_docker_node("Relay-B", node_b.url, node_b.token, activate=True)
    assert node_b.spy is not None
    node_b.spy.clear()
    node_b.spy.set_forced_status("/api/v1/archives/pull-unpack", 502, b'{"error":{"message":"subnet blocked"}}')

    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": True},
        )
        .json()["items"][0]["id"]
    )
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert len(node_b.spy.matching_requests("/api/v1/archives/unpack", method="POST")) == 1
    assert not old_dir.exists()


@e2e_test(
    test_id="T3_03",
    tier=3,
    features=["F1", "F4", "F5", "F7"],
    title="Co-located Builtin (with TM_STORAGE_SERVICE_URL) -> Docker A Move delegates packing to storage container",
)
def test_t3_03_colocated_builtin_to_docker_move_delegates_packing(ctx: CaseContext) -> None:
    assert "TM_STORAGE_SERVICE_URL" in (REPO_ROOT / "docker-compose.yml").read_text(encoding="utf-8")
    coloc = ctx.storage(name="st_t3_03_coloc", token="coloc-secret-token-123456")
    remote = ctx.storage(name="st_t3_03_remote", token="remote-secret-token-123456")

    db_name, dsn = ctx.session.pg.create_db("tm_t3_03")
    app = PlatformDaemon(
        binary=ctx.session.trainmaster_binary,
        work_dir=ctx.case_dir / "app_t3_03",
        db_name=db_name,
        database_url=dsn,
        storage_service_url=coloc.url,
        storage_service_token=coloc.token,
        extra_env={"TM_DATA_DIR": str(coloc.data_dir)},
    ).start()
    ctx._closers.append(app.stop)

    ds = app.import_voc_dataset("本机托管迁远端集", count=4)
    _, dv = app.create_project_and_freeze_version("本机托管迁远端项目", [int(ds["id"])])

    app.create_docker_node("远端Docker", remote.url, remote.token, activate=True)
    assert coloc.spy is not None and remote.spy is not None
    coloc.spy.clear()
    remote.spy.clear()

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [
                {"target_type": "dataset", "target_id": ds["id"]},
                {"target_type": "data_version", "target_id": dv["id"]},
            ],
            "delete_source": True,
        },
    )
    assert_status(mig_resp, 201)
    ids = [int(m["id"]) for m in mig_resp.json()["items"]]
    migs, _ = app.wait_migrations(ids)
    for mid in ids:
        assert migs[mid]["status"] == "succeeded"
        assert migs[mid]["transfer_mode"] == "archive"
    assert len(coloc.spy.matching_requests("/api/v1/archives/pack", method="POST")) >= 2
    assert len(remote.spy.archive_requests()) >= 2


@e2e_test(
    test_id="T3_04",
    tier=3,
    features=["F2", "F3", "F5", "F7"],
    title="Docker A -> Builtin (without TM_STORAGE_SERVICE_URL) Dataset Copy uses archive pack and local unpack",
)
def test_t3_04_docker_to_plain_builtin_archive_copy(ctx: CaseContext) -> None:
    remote = ctx.storage(name="st_t3_04_remote")
    app = ctx.platform(name="app_t3_04")  # No TM_STORAGE_SERVICE_URL

    app.create_docker_node("远端源Docker", remote.url, remote.token, activate=True)
    ds = app.import_voc_dataset("迁回本机数据集", count=4)

    assert_status(app.client().post("/api/v1/storage/nodes/1/activate"), 200)
    assert remote.spy is not None
    remote.spy.clear()

    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": False},
        )
        .json()["items"][0]["id"]
    )
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "archive"
    assert len(remote.spy.matching_requests("/api/v1/archives/pack", method="POST")) == 1

    updated = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    assert updated["storage_node"]["is_builtin"] is True
    assert (app.data_dir / "datasets" / updated["dir_name"]).is_dir()


@e2e_test(
    test_id="T3_05",
    tier=3,
    features=["F5", "F6", "F7"],
    title="Docker A -> Legacy S3 Dataset Move runs transfer_mode=direct with zero archive calls and deletes source",
)
def test_t3_05_docker_to_s3_move_direct_mode(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t3_05")
    s3 = ctx.s3_mock()
    app = ctx.platform(name="app_t3_05")

    app.create_docker_node("Docker-T3-05", st.url, st.token, activate=True)
    ds = app.import_voc_dataset("Docker迁S3移动集", count=3)
    src_dir = st.data_dir / "datasets" / ds["dir_name"]
    app.create_s3_node("S3-T3-05", s3, activate=True)

    assert st.spy is not None
    st.spy.clear()
    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": True},
        )
        .json()["items"][0]["id"]
    )
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "direct"
    assert len(st.spy.archive_requests()) == 0
    assert not src_dir.exists()
    assert s3.object_count("tm-datasets", "prod/datasets/") >= 6


@e2e_test(
    test_id="T3_06",
    tier=3,
    features=["F5", "F6", "F7"],
    title="Legacy S3 -> Docker A Dataset Copy runs transfer_mode=direct with zero archive calls and keeps S3 objects",
)
def test_t3_06_s3_to_docker_copy_direct_mode(ctx: CaseContext) -> None:
    s3 = ctx.s3_mock()
    st = ctx.storage(name="st_t3_06")
    app = ctx.platform(name="app_t3_06")

    app.create_s3_node("S3-T3-06", s3, activate=True)
    ds = app.import_voc_dataset("S3迁Docker复制集", count=3)
    before_s3_count = s3.object_count("tm-datasets", "prod/datasets/")
    app.create_docker_node("Docker-T3-06", st.url, st.token, activate=True)

    assert st.spy is not None
    st.spy.clear()
    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": False},
        )
        .json()["items"][0]["id"]
    )
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "direct"
    assert len(st.spy.archive_requests()) == 0
    assert s3.object_count("tm-datasets", "prod/datasets/") == before_s3_count


@e2e_test(
    test_id="T3_07",
    tier=3,
    features=["F6", "F7"],
    title="Legacy S3 A -> Legacy S3 B Dataset and DataVersion Move completes in direct mode with zero archive calls",
)
def test_t3_07_s3_to_s3_dataset_and_dataversion_move(ctx: CaseContext) -> None:
    s3_a = ctx.s3_mock(buckets=("bucket-a",))
    s3_b = ctx.s3_mock(buckets=("bucket-b",))
    app = ctx.platform(name="app_t3_07")

    app.create_s3_node("S3-Bucket-A", s3_a, bucket="bucket-a", prefix="pa", activate=True)
    ds = app.import_voc_dataset("S3间迁移数据集", count=3)
    _, dv = app.create_project_and_freeze_version("S3间迁移项目", [int(ds["id"])])

    app.create_s3_node("S3-Bucket-B", s3_b, bucket="bucket-b", prefix="pb", activate=True)
    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [
                {"target_type": "dataset", "target_id": ds["id"]},
                {"target_type": "data_version", "target_id": dv["id"]},
            ],
            "delete_source": True,
        },
    )
    assert_status(mig_resp, 201)
    ids = [int(m["id"]) for m in mig_resp.json()["items"]]
    migs, _ = app.wait_migrations(ids)
    for mid in ids:
        assert migs[mid]["status"] == "succeeded"
        assert migs[mid]["transfer_mode"] == "direct"
    assert len(s3_a.archive_requests()) == 0 and len(s3_b.archive_requests()) == 0
    assert s3_b.object_count("bucket-b", "pb/") > 0


@e2e_test(
    test_id="T3_08",
    tier=3,
    features=["F5", "F6", "F7", "F11"],
    title="Passive Local (is_builtin=false) <-> Docker migration respects legacy non-archive matrix",
)
def test_t3_08_passive_local_to_docker_respects_legacy_direct_matrix(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t3_08")
    app = ctx.platform(name="app_t3_08", with_web=True)

    passive_root = ctx.case_dir / "passive_nas"
    p_node = app.create_passive_local_node("被动NAS挂载目录", passive_root, activate=True).json()
    assert p_node["supports_archive"] is False

    ds = app.import_voc_dataset("被动目录样本", count=3)
    d_node = app.create_docker_node("主动Docker容器", st.url, st.token, activate=True).json()
    assert d_node["supports_archive"] is True

    assert st.spy is not None
    st.spy.clear()
    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
        )
        .json()["items"][0]["id"]
    )
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "direct"
    assert len(st.spy.archive_requests()) == 0


@e2e_test(
    test_id="T3_09",
    tier=3,
    features=["F2", "F3", "F5", "F9"],
    title="Sequential merge_skip and merge_overwrite staged imports onto Docker followed by cross-node freeze",
)
def test_t3_09_sequential_conflict_imports_and_cross_node_freeze(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t3_09_a")
    node_b = ctx.storage(name="st_t3_09_b")
    app = ctx.platform(name="app_t3_09")

    app.create_docker_node("Import-A", node_a.url, node_a.token, activate=True)
    base_zip = make_voc_dataset_zip(count=3, prefix="item", label="scratch")
    ds = app.import_voc_dataset("增量合并集", zip_bytes=base_zip, on_conflict="skip")
    ds_id = int(ds["id"])

    assert node_a.spy is not None
    node_a.spy.clear()

    # Second import with on_conflict="skip": item_001 kept as "scratch", item_004 added as "crack"
    buf_skip = io.BytesIO()
    with zipfile.ZipFile(buf_skip, "w") as zf:
        zf.writestr("item_001.png", make_png_bytes(32, 24, (250, 10, 10)))
        zf.writestr("item_001.xml", make_voc_xml(label="ignored_label"))
        zf.writestr("item_004.png", make_png_bytes(32, 24, (10, 250, 10)))
        zf.writestr("item_004.xml", make_voc_xml(label="crack"))
    ds2 = app.import_voc_dataset("增量合并集", dataset_id=ds_id, zip_bytes=buf_skip.getvalue(), on_conflict="skip")
    assert int(ds2["image_count"]) == 4
    labels_after_skip = {it["name"] for it in app.client().get(f"/api/v1/datasets/{ds_id}/labels").json()["items"]}
    assert "ignored_label" not in labels_after_skip and "crack" in labels_after_skip
    skip_unpacks = node_a.spy.matching_requests("/api/v1/archives/unpack", method="POST")
    assert any("mode=merge_skip" in r.full_path for r in skip_unpacks), (
        f"Expected mode=merge_skip in {[r.full_path for r in skip_unpacks]}"
    )

    # Third import with on_conflict="overwrite": item_002 overwritten from "scratch" to "dent"
    node_a.spy.clear()
    buf_overwrite = io.BytesIO()
    with zipfile.ZipFile(buf_overwrite, "w") as zf:
        zf.writestr("item_002.png", make_png_bytes(32, 24, (10, 10, 250)))
        zf.writestr("item_002.xml", make_voc_xml(label="dent"))
    ds3 = app.import_voc_dataset(
        "增量合并集",
        dataset_id=ds_id,
        zip_bytes=buf_overwrite.getvalue(),
        on_conflict="overwrite",
    )
    assert int(ds3["image_count"]) == 4
    labels_after_overwrite = {it["name"] for it in app.client().get(f"/api/v1/datasets/{ds_id}/labels").json()["items"]}
    assert {"scratch", "crack", "dent"}.issubset(labels_after_overwrite)
    overwrite_unpacks = node_a.spy.matching_requests("/api/v1/archives/unpack", method="POST")
    assert any("mode=merge_overwrite" in r.full_path for r in overwrite_unpacks), (
        f"Expected mode=merge_overwrite in {[r.full_path for r in overwrite_unpacks]}"
    )

    # Activate Node B and freeze multi-class DataVersion across Docker A -> Docker B
    app.create_docker_node("Freeze-B", node_b.url, node_b.token, activate=True)
    proj = app.client().post("/api/v1/projects", json_body={"name": "增量跨节点项目", "dataset_ids": [ds_id]}).json()
    cls_ver = (
        app.client()
        .put(
            f"/api/v1/projects/{proj['id']}/classes",
            json_body={
                "classes": [
                    {"name": "scratch", "color": "#ef4444"},
                    {"name": "crack", "color": "#22c55e"},
                    {"name": "dent", "color": "#3b82f6"},
                ]
            },
        )
        .json()["version"]
    )
    dv_resp = app.client().post(
        "/api/v1/data-versions",
        json_body={
            "project_id": proj["id"],
            "note": "merged-v1",
            "class_version": cls_ver,
            "sources": [{"dataset_id": ds_id, "include_base": True, "version_ids": []}],
            "split": {"train": 75, "val": 25, "test": 0, "seed": 7, "use_folders": True, "group_by": "stem"},
            "scope": "annotated",
            "include_empty": True,
            "ignore_labels": [],
            "augmentation": None,
        },
    )
    assert_status(dv_resp, 201)
    dv_id = int(dv_resp.json()["id"])

    deadline = time.monotonic() + 25.0
    dv_ready = None
    while time.monotonic() < deadline:
        cur_resp = app.client().get(f"/api/v1/data-versions/{dv_id}")
        assert_status(cur_resp, 200)
        cur = cur_resp.json()
        if cur["status"] == "ready":
            dv_ready = cur
            break
        if cur["status"] in ("failed", "canceled"):
            raise AssertionError(f"multi-class data_version {dv_id} {cur['status']}: {cur.get('error')}")
        time.sleep(0.05)
    assert dv_ready is not None and dv_ready["status"] == "ready"
    assert int(dv_ready["image_count"]) == 4


@e2e_test(
    test_id="T3_10",
    tier=3,
    features=["F1", "F2", "F5", "F9"],
    title="Multi-dataset DataVersion freeze combines cross-node archive pull (Docker A) and same-node copy-within (Docker B)",
)
def test_t3_10_hybrid_same_and_cross_node_dataversion_freeze(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t3_10_a")
    node_b = ctx.storage(name="st_t3_10_b")
    app = ctx.platform(name="app_t3_10")

    app.create_docker_node("Hybrid-A", node_a.url, node_a.token, activate=True)
    ds_a = app.import_voc_dataset("节点A数据集", zip_bytes=make_voc_dataset_zip(count=3, prefix="a_img"))

    app.create_docker_node("Hybrid-B", node_b.url, node_b.token, activate=True)
    ds_b = app.import_voc_dataset("节点B数据集", zip_bytes=make_voc_dataset_zip(count=3, prefix="b_img"))

    assert node_a.spy is not None and node_b.spy is not None
    node_a.spy.clear()
    node_b.spy.clear()

    _, dv = app.create_project_and_freeze_version(
        "双节点联合冻结项目",
        [int(ds_a["id"]), int(ds_b["id"])],
    )
    assert dv["status"] == "ready"
    assert int(dv["image_count"]) == 6
    assert len(node_a.spy.matching_requests("/api/v1/archives/pack", method="POST")) >= 1
    assert len(node_b.spy.matching_requests("/api/v1/spaces/copy-within", method="POST")) >= 1


@e2e_test(
    test_id="T3_11",
    tier=3,
    features=["F2", "F8", "F10"],
    title="DataVersion on Docker -> Trainer 1st job archive verify+cache -> 2nd job cache hit without HTTP pull",
)
def test_t3_11_docker_dataversion_trainer_archive_then_cache_hit(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t3_11")
    app = ctx.platform(name="app_t3_11")
    app.create_docker_node("CacheDocker", st.url, st.token, activate=True)
    app.client().put(
        "/api/v1/storage/cache-policy",
        json_body={"enabled": True, "max_versions": 2, "ttl_hours": 12},
    )

    ds = app.import_voc_dataset("训练缓存集", count=4)
    proj, dv = app.create_project_and_freeze_version("训练缓存项目", [int(ds["id"])])
    node_key = "55555555-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)
    state_dir = ctx.case_dir / "tr_t3_11"

    assert st.spy is not None
    for run_idx in (1, 2):
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
        st.spy.clear()
        out, audit, _, _ = run_trainer_job_in_subprocess(
            platform_url=app.url,
            task_payload=task,
            state_dir=state_dir,
            node_key=node_key,
        )
        assert out == "succeeded"
        assert audit is not None and audit["image_count"] == 4
        if run_idx == 1:
            assert len(st.spy.matching_requests("/api/v1/archives/pack", method="POST")) == 1
        else:
            assert len(st.spy.archive_requests()) == 0


@e2e_test(
    test_id="T3_12",
    tier=3,
    features=["F4", "F8", "F10"],
    title="Builtin DataVersion with co-located storage service supports both shared zero-copy and remote archive pull",
)
def test_t3_12_colocated_builtin_shared_zero_copy_vs_remote_archive_trainer(ctx: CaseContext) -> None:
    coloc = ctx.storage(name="st_t3_12_coloc", token="coloc-tok-987654321")
    db_name, dsn = ctx.session.pg.create_db("tm_t3_12")
    app = PlatformDaemon(
        binary=ctx.session.trainmaster_binary,
        work_dir=ctx.case_dir / "app_t3_12",
        db_name=db_name,
        database_url=dsn,
        storage_service_url=coloc.url,
        storage_service_token=coloc.token,
        extra_env={"TM_DATA_DIR": str(coloc.data_dir)},
    ).start()
    ctx._closers.append(app.stop)

    ds = app.import_voc_dataset("双模训练数据集", count=3)
    proj, dv = app.create_project_and_freeze_version("双模训练项目", [int(ds["id"])])
    node_key = "66666666-2222-3333-4444-555555555555"
    app.register_compute_node(node_key=node_key)

    assert coloc.spy is not None

    # 1) Co-located trainer sharing coloc.data_dir -> zero archive pack calls!
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
    t_local = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    coloc.spy.clear()
    out_loc, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=t_local,
        state_dir=ctx.case_dir / "tr_coloc_local",
        node_key=node_key,
        data_dir=coloc.data_dir,
    )
    assert out_loc == "succeeded"
    assert len(coloc.spy.archive_requests()) == 0

    # 2) Remote trainer without shared data_dir -> storage container packs .zip via archive mode!
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
    t_rem = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    coloc.spy.clear()
    out_rem, _, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=t_rem,
        state_dir=ctx.case_dir / "tr_coloc_remote",
        node_key=node_key,
        data_dir=None,
    )
    assert out_rem == "succeeded"
    assert len(coloc.spy.matching_requests("/api/v1/archives/pack", method="POST")) == 1


@e2e_test(
    test_id="T3_13",
    tier=3,
    features=["F1", "F5", "F9"],
    title="Recycle bin soft-delete and restore of Dataset on Docker node updates node stats and preserves images",
)
def test_t3_13_recycle_bin_soft_delete_and_restore_on_docker_node(ctx: CaseContext) -> None:
    st = ctx.storage(name="st_t3_13")
    app = ctx.platform(name="app_t3_13")
    d_node = app.create_docker_node("回收站Docker", st.url, st.token, activate=True).json()

    ds = app.import_voc_dataset("可回收Docker数据集", count=3)
    ds_id = int(ds["id"])

    del_resp = app.client().delete(f"/api/v1/datasets/{ds_id}?delete_files=true")
    assert_status(del_resp, 204, "soft-delete dataset on Docker node")

    rb = app.client().get("/api/v1/recycle-bin").json()
    trashed_ids = [int(d["id"]) for d in rb["datasets"]]
    assert ds_id in trashed_ids

    nodes_mid = app.client().get("/api/v1/storage/nodes").json()["items"]
    docker_mid = next(n for n in nodes_mid if int(n["id"]) == int(d_node["id"]))
    assert int(docker_mid["stats"]["trashed_dataset_count"]) == 1

    rest_resp = app.client().post(f"/api/v1/recycle-bin/datasets/{ds_id}/restore", json_body={})
    assert_status(rest_resp, 200, "restore dataset on Docker node")
    restored = rest_resp.json()
    assert int(restored["image_count"]) == 3
    assert restored["storage_node"]["id"] == d_node["id"]


@e2e_test(
    test_id="T3_14",
    tier=3,
    features=["F5", "F7", "F11", "F12"],
    title="End-to-end Web UI + Storage API alignment across onboarding, capability badges, and multi-stage migration",
)
def test_t3_14_web_ui_and_storage_api_alignment_across_fleet(ctx: CaseContext) -> None:
    st_a = ctx.storage(name="st_t3_14_a")
    st_b = ctx.storage(name="st_t3_14_b")
    app = ctx.platform(name="app_t3_14", with_web=True)

    # Verify UI SPA & bundle contract
    assert_status(app.client().get("/system/storage"), 200)
    bundle_text = read_all_web_bundle_text(ctx.session.ensure_web_bundle())
    assert "支持打包加速" in bundle_text
    assert "正在打包压缩" in bundle_text
    assert "存储容器打包拉取并解压" in bundle_text

    # Onboard Docker A via UI payload shape, import dataset, onboard Docker B, migrate & verify JSON fields
    n_a = app.client().post(
        "/api/v1/storage/nodes",
        json_body={
            "name": "UI-Docker-A",
            "kind": "docker",
            "config": {"endpoint": st_a.url, "insecure_skip_verify": False},
            "token": st_a.token,
            "activate": True,
        },
    ).json()
    assert n_a["supports_archive"] is True
    ds = app.import_voc_dataset("UI迁移验证集", count=3)

    n_b = app.client().post(
        "/api/v1/storage/nodes",
        json_body={
            "name": "UI-Docker-B",
            "kind": "docker",
            "config": {"endpoint": st_b.url, "insecure_skip_verify": False},
            "token": st_b.token,
            "activate": True,
        },
    ).json()
    assert n_b["supports_archive"] is True

    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}]},
        )
        .json()["items"][0]["id"]
    )
    migs, _ = app.wait_migrations([mig_id])
    row = migs[mig_id]
    assert row["status"] == "succeeded"
    assert row["transfer_mode"] == "archive"
    assert int(row["archive_bytes"]) > 0
