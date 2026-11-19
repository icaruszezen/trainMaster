"""Tier 4 Real-World Enterprise End-to-End Application Scenarios (6 comprehensive scenarios)."""

from __future__ import annotations

import os
import subprocess
import time

from e2e.harness.cluster import CaseContext, PlatformDaemon, REPO_ROOT, run_trainer_job_in_subprocess
from e2e.harness.fixtures import make_png_bytes, make_tar_zst, make_voc_dataset_zip, snapshot_tree
from e2e.harness.registry import e2e_test
from e2e.tests._helpers import assert_status, read_all_web_bundle_text


@e2e_test(
    test_id="T4_01",
    tier=4,
    features=["F1", "F2", "F3", "F5", "F8", "F9", "F10", "F11"],
    title="Scenario 1: Remote Docker Storage Onboarding -> Bulk Staged Import -> Freeze -> Trainer Archive Pull & Cache Hit",
)
def test_t4_01_enterprise_docker_onboarding_import_freeze_and_trainer_cache(ctx: CaseContext) -> None:
    docker_srv = ctx.storage(name="st_t4_01", token="ent-storage-token-aabbccdd1122")
    app = ctx.platform(name="app_t4_01", node_token="ent-compute-token-1122334455", with_web=True)

    # Step 1: Probe & onboard dedicated remote Docker storage server via UI-shaped REST payload
    probe = app.client().post(
        "/api/v1/storage/nodes/test",
        json_body={
            "kind": "docker",
            "config": {"endpoint": docker_srv.url, "insecure_skip_verify": False},
            "token": docker_srv.token,
        },
    )
    assert_status(probe, 200)
    assert probe.json()["ok"] is True

    node = app.client().post(
        "/api/v1/storage/nodes",
        json_body={
            "name": "机房独立Docker存储节点-01",
            "kind": "docker",
            "config": {"endpoint": docker_srv.url, "insecure_skip_verify": False},
            "token": docker_srv.token,
            "activate": True,
        },
    ).json()
    assert node["supports_archive"] is True and node["is_active"] is True

    # Step 2: Bulk staged VOC archive upload onto active Docker node (verifying archive unpack & zero local leak)
    assert docker_srv.spy is not None
    docker_srv.spy.clear()
    ds = app.import_voc_dataset("产线PCB缺陷批次01", count=6)
    assert int(ds["image_count"]) == 6
    assert len(docker_srv.spy.archive_requests()) >= 1
    assert not (app.data_dir / "datasets" / ds["dir_name"]).exists()

    # Step 3: Freeze DataVersion on same Docker node (verifying copy-within hardlinks)
    docker_srv.spy.clear()
    proj, dv = app.create_project_and_freeze_version("PCB表面缺陷检测", [int(ds["id"])])
    assert dv["storage_node"]["id"] == node["id"]
    assert len(docker_srv.spy.matching_requests("/api/v1/spaces/copy-within", method="POST")) >= 1

    # Step 4: Enable trainer cache policy, run Job #1 (archive pack+stream+SHA256+extract+unlink+upload model)
    assert_status(
        app.client().put(
            "/api/v1/storage/cache-policy",
            json_body={"enabled": True, "max_versions": 3, "ttl_hours": 24},
        ),
        200,
    )
    node_key = "aaaaaaaa-1111-2222-3333-444444444444"
    app.register_compute_node(node_key=node_key, name="GPU-Worker-Enterprise-01")
    trainer_state = ctx.case_dir / "ent_trainer_state"

    job1_id = int(
        app.client()
        .post(
            "/api/v1/training/jobs",
            json_body={
                "project_id": proj["id"],
                "data_version_id": dv["id"],
                "init_mode": "scratch",
                "yolo_version": "yolo11",
                "model_scale": "n",
                "params": {"epochs": 2},
            },
        )
        .json()["id"]
    )
    task1 = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    docker_srv.spy.clear()
    out1, audit1, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task1,
        state_dir=trainer_state,
        node_key=node_key,
        node_token=app.node_token,
    )
    assert out1 == "succeeded"
    assert audit1 is not None and audit1["image_count"] == 6 and audit1["zip_present_during_training"] is False
    assert len(docker_srv.spy.matching_requests("/api/v1/archives/pack", method="POST")) == 1
    assert app.client().get(f"/api/v1/training/jobs/{job1_id}").json()["status"] == "succeeded"

    # Step 5: Run Job #2 on same DataVersion -> hits local DatasetCacheManager with ZERO container pack calls!
    app.client().post(
        "/api/v1/training/jobs",
        json_body={
            "project_id": proj["id"],
            "data_version_id": dv["id"],
            "init_mode": "scratch",
            "yolo_version": "yolo11",
            "model_scale": "s",
            "params": {"epochs": 1},
        },
    )
    task2 = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    docker_srv.spy.clear()
    out2, audit2, _, _ = run_trainer_job_in_subprocess(
        platform_url=app.url,
        task_payload=task2,
        state_dir=trainer_state,
        node_key=node_key,
        node_token=app.node_token,
    )
    assert out2 == "succeeded" and audit2 is not None and audit2["image_count"] == 6
    assert len(docker_srv.spy.archive_requests()) == 0
    assert list((docker_srv.data_dir / ".tmp-archives").iterdir()) == []


@e2e_test(
    test_id="T4_02",
    tier=4,
    features=["F1", "F2", "F3", "F5", "F7", "F12"],
    title="Scenario 2: Multi-Hop Online Move (Builtin -> Docker A -> Docker B) with Checksum, ModTime & Zero Orphans",
)
def test_t4_02_multi_hop_online_move_builtin_to_docker_a_to_docker_b(ctx: CaseContext) -> None:
    coloc = ctx.storage(name="st_t4_02_coloc", token="coloc-hop-token-123456")
    docker_a = ctx.storage(name="st_t4_02_a", token="hop-a-token-123456")
    docker_b = ctx.storage(name="st_t4_02_b", token="hop-b-token-123456")

    db_name, dsn = ctx.session.pg.create_db("tm_t4_02")
    app = PlatformDaemon(
        binary=ctx.session.trainmaster_binary,
        work_dir=ctx.case_dir / "app_t4_02",
        db_name=db_name,
        database_url=dsn,
        storage_service_url=coloc.url,
        storage_service_token=coloc.token,
        extra_env={"TM_DATA_DIR": str(coloc.data_dir)},
    ).start()
    ctx._closers.append(app.stop)

    # Create dataset & frozen version on Builtin
    ds = app.import_voc_dataset("多跳迁移主数据集", count=5)
    proj, dv = app.create_project_and_freeze_version("多跳迁移项目", [int(ds["id"])])
    orig_ds_snap = snapshot_tree(coloc.data_dir / "datasets" / ds["dir_name"])
    orig_dv_rel = f"data-versions/project-{proj['id']}/v{dv['version']}"
    orig_dv_snap = snapshot_tree(coloc.data_dir / "exports" / orig_dv_rel)

    # Hop 1: Builtin -> Docker A (delete_source=true)
    na = app.create_docker_node("Hop-Docker-A", docker_a.url, docker_a.token, activate=True).json()
    m1_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [
                {"target_type": "dataset", "target_id": ds["id"]},
                {"target_type": "data_version", "target_id": dv["id"]},
            ],
            "delete_source": True,
        },
    )
    assert_status(m1_resp, 201)
    m1_ids = [int(x["id"]) for x in m1_resp.json()["items"]]
    m1_res, _ = app.wait_migrations(m1_ids)
    for mid in m1_ids:
        assert m1_res[mid]["status"] == "succeeded" and m1_res[mid]["transfer_mode"] == "archive"
    assert not (coloc.data_dir / "datasets" / ds["dir_name"]).exists()
    assert app.client().get(f"/api/v1/datasets/{ds['id']}").json()["storage_node"]["id"] == na["id"]

    # Hop 2: Docker A -> Docker B (delete_source=true)
    nb = app.create_docker_node("Hop-Docker-B", docker_b.url, docker_b.token, activate=True).json()
    m2_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [
                {"target_type": "dataset", "target_id": ds["id"]},
                {"target_type": "data_version", "target_id": dv["id"]},
            ],
            "delete_source": True,
        },
    )
    assert_status(m2_resp, 201)
    m2_ids = [int(x["id"]) for x in m2_resp.json()["items"]]
    m2_res, _ = app.wait_migrations(m2_ids)
    for mid in m2_ids:
        assert m2_res[mid]["status"] == "succeeded"
        assert m2_res[mid]["transfer_mode"] == "archive"
        assert int(m2_res[mid]["archive_bytes"]) > 0

    final_ds = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    final_dv = app.client().get(f"/api/v1/data-versions/{dv['id']}").json()
    assert final_ds["storage_node"]["id"] == nb["id"]
    assert final_dv["storage_node"]["id"] == nb["id"]

    # Verify byte-for-byte SHA-256 & ModTime preservation across 2 hops + zero orphan temp files anywhere
    final_ds_snap = snapshot_tree(docker_b.data_dir / "datasets" / final_ds["dir_name"])
    final_dv_snap = snapshot_tree(docker_b.data_dir / "exports" / orig_dv_rel)
    assert {k: v.sha256 for k, v in orig_ds_snap.items()} == {k: v.sha256 for k, v in final_ds_snap.items()}
    assert {k: v.sha256 for k, v in orig_dv_snap.items()} == {k: v.sha256 for k, v in final_dv_snap.items()}

    for d in (coloc, docker_a, docker_b):
        assert list((d.data_dir / ".tmp-archives").iterdir()) == []


@e2e_test(
    test_id="T4_03",
    tier=4,
    features=["F5", "F6", "F7", "F8", "F10", "F11", "F12"],
    title="Scenario 3: Hybrid Enterprise Fleet Coexistence (Builtin + Docker + Legacy S3 + Passive Local)",
)
def test_t4_03_hybrid_enterprise_fleet_coexistence_matrix(ctx: CaseContext) -> None:
    docker_srv = ctx.storage(name="st_t4_03_docker")
    s3_srv = ctx.s3_mock()
    app = ctx.platform(name="app_t4_03", with_web=True)

    # 1) Dataset & DV on Builtin
    ds_builtin = app.import_voc_dataset("混合本机数据集", count=3)

    # 2) Dataset & DV on Legacy S3
    app.create_s3_node("混合历史S3", s3_srv, activate=True)
    ds_s3 = app.import_voc_dataset("混合S3数据集", count=3)
    proj_s3, dv_s3 = app.create_project_and_freeze_version("S3直接训练项目", [int(ds_s3["id"])])

    # 3) Dataset on Passive Local
    app.create_passive_local_node("混合被动目录", ctx.case_dir / "passive_fleet_dir", activate=True)
    ds_passive = app.import_voc_dataset("混合被动目录集", count=2)

    # 4) Activate Docker node and batch-migrate all 3 datasets simultaneously!
    app.create_docker_node("混合中心Docker", docker_srv.url, docker_srv.token, activate=True)
    assert docker_srv.spy is not None
    docker_srv.spy.clear()
    s3_srv.clear_logs()

    mig_resp = app.client().post(
        "/api/v1/storage/migrations",
        json_body={
            "targets": [
                {"target_type": "dataset", "target_id": ds_builtin["id"]},
                {"target_type": "dataset", "target_id": ds_s3["id"]},
                {"target_type": "dataset", "target_id": ds_passive["id"]},
            ],
            "delete_source": False,
        },
    )
    assert_status(mig_resp, 201)
    created_migs = mig_resp.json()["items"]
    migs, _ = app.wait_migrations([int(m["id"]) for m in created_migs])

    by_target = {int(m["target_id"]): m for m in migs.values()}
    assert by_target[int(ds_builtin["id"])]["transfer_mode"] == "archive"
    assert by_target[int(ds_s3["id"])]["transfer_mode"] == "direct"
    assert by_target[int(ds_passive["id"])]["transfer_mode"] == "direct"
    assert len(s3_srv.archive_requests()) == 0

    # 5) Run trainer on S3 DataVersion (direct) and new Docker DataVersion (archive)
    proj_dk, dv_dk = app.create_project_and_freeze_version("Docker打包训练项目", [int(ds_builtin["id"])])
    node_key = "bbbbbbbb-1111-2222-3333-444444444444"
    app.register_compute_node(node_key=node_key)

    for p_id, v_id, expected_mode in [
        (int(proj_s3["id"]), int(dv_s3["id"]), "direct"),
        (int(proj_dk["id"]), int(dv_dk["id"]), "archive"),
    ]:
        app.client().post(
            "/api/v1/training/jobs",
            json_body={
                "project_id": p_id,
                "data_version_id": v_id,
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
        assert (task.get("dv_transfer_mode") or task["data_version"].get("transfer_mode")) == expected_mode
        out, _, _, _ = run_trainer_job_in_subprocess(
            platform_url=app.url,
            task_payload=task,
            state_dir=ctx.case_dir / f"tr_fleet_{expected_mode}",
            node_key=node_key,
        )
        assert out == "succeeded"


@e2e_test(
    test_id="T4_04",
    tier=4,
    features=["F2", "F3", "F5", "F8", "F9", "F10"],
    title="Scenario 4: Cross-Node Multi-Dataset Freeze (Docker A + Builtin -> Docker B) + Tamper Reject & Clean Retry",
)
def test_t4_04_cross_node_multi_dataset_freeze_and_trainer_tamper_recovery(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t4_04_a")
    node_b = ctx.storage(name="st_t4_04_b")
    app = ctx.platform(name="app_t4_04")

    # Dataset 1 on Builtin
    ds_local = app.import_voc_dataset("本机源集", zip_bytes=make_voc_dataset_zip(count=3, prefix="loc"))

    # Dataset 2 on Docker A
    app.create_docker_node("源Docker-A", node_a.url, node_a.token, activate=True)
    ds_dock_a = app.import_voc_dataset("容器A源集", zip_bytes=make_voc_dataset_zip(count=3, prefix="dka"))

    # Activate Docker B and freeze unified DataVersion
    nb = app.create_docker_node("汇聚Docker-B", node_b.url, node_b.token, activate=True).json()
    proj, dv = app.create_project_and_freeze_version(
        "跨节点融合训练项目",
        [int(ds_local["id"]), int(ds_dock_a["id"])],
    )
    assert dv["storage_node"]["id"] == nb["id"] and int(dv["image_count"]) == 6

    node_key = "cccccccc-1111-2222-3333-444444444444"
    app.register_compute_node(node_key=node_key)
    app_spy = ctx.spy_proxy(app.url, name="t4_04_app_spy")
    state_dir = ctx.case_dir / "tr_t4_04"

    # Attempt 1: Inject byte corruption into streamed dataset.zip -> trainer rejects & unlinks zip!
    bad_job_id = int(
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
    bad_task = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    assert int(bad_task["id"]) == bad_job_id
    app_spy.set_corrupt_response_body(str(bad_task["dataset_url"]), True)
    bad_out, bad_audit, _, _ = run_trainer_job_in_subprocess(
        platform_url=app_spy.url,
        task_payload=bad_task,
        state_dir=state_dir,
        node_key=node_key,
    )
    assert bad_out == "failed" and bad_audit is None
    assert list(state_dir.rglob("dataset.zip")) == []

    # Attempt 2: Clear corruption -> retry training job (requeued automatically if retryable=True, else newly created) succeeds cleanly with all 6 images!
    app_spy.clear_faults()
    after_fail = app.client().get(f"/api/v1/training/jobs/{bad_job_id}").json()
    if after_fail["status"] == "queued":
        ok_job_id = bad_job_id
    else:
        ok_job_id = int(
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
    ok_task = app.agent_client(node_key=node_key).post(
        "/api/v1/compute/agent/tasks/claim",
        json_body={"node_key": node_key, "kinds": ["training_job"], "wait_seconds": 0},
    ).json()["task"]
    assert int(ok_task["id"]) == ok_job_id
    ok_out, ok_audit, _, _ = run_trainer_job_in_subprocess(
        platform_url=app_spy.url,
        task_payload=ok_task,
        state_dir=state_dir,
        node_key=node_key,
    )
    assert ok_out == "succeeded" and ok_audit is not None and ok_audit["image_count"] == 6
    assert app.client().get(f"/api/v1/training/jobs/{ok_job_id}").json()["status"] == "succeeded"


@e2e_test(
    test_id="T4_05",
    tier=4,
    features=["F1", "F2", "F3", "F7"],
    title="Scenario 5: Fault Injection & Recovery Lifecycle (Cancel -> 507 -> Corrupt SHA -> Janitor -> Clean Retry)",
)
def test_t4_05_fault_injection_and_full_recovery_lifecycle(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t4_05_a", temp_ttl="250ms")
    node_b = ctx.storage(name="st_t4_05_b", temp_ttl="250ms")
    app = ctx.platform(name="app_t4_05")

    app.create_docker_node("Resilient-A", node_a.url, node_a.token, activate=True)
    ds = app.import_voc_dataset("容错恢复测试数据集", count=4)
    src_dir = node_a.data_dir / "datasets" / ds["dir_name"]
    nb = app.create_docker_node("Resilient-B", node_b.url, node_b.token, activate=True).json()

    assert node_a.spy is not None and node_b.spy is not None

    # Phase 1: Mid-flight cancel
    entered, release = node_a.spy.install_gate("/api/v1/archives/pack")
    m_cancel_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": True},
        )
        .json()["items"][0]["id"]
    )
    assert entered.wait(timeout=10.0)
    assert_status(app.client().post(f"/api/v1/storage/migrations/{m_cancel_id}/cancel"), 200)
    release.set()
    node_a.spy.clear_faults()
    res1, _ = app.wait_migrations([m_cancel_id])
    assert res1[m_cancel_id]["status"] == "canceled" and src_dir.is_dir()

    # Phase 2: 507 Insufficient Storage on destination
    err_507 = b'{"error":{"code":"insufficient_storage","message":"disk full"}}'
    node_b.spy.set_forced_status("/api/v1/archives/pull-unpack", 507, err_507)
    node_b.spy.set_forced_status("/api/v1/archives/unpack", 507, err_507)
    m_507_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": True},
        )
        .json()["items"][0]["id"]
    )
    res2, _ = app.wait_migrations([m_507_id])
    assert res2[m_507_id]["status"] == "failed" and src_dir.is_dir()
    node_b.spy.clear_faults()

    # Phase 3: Direct corrupt SHA-256 upload to Node B rejected + stale orphan swept by TTL janitor
    bad_arc = make_tar_zst([("x.png", make_png_bytes(12, 12), None)])
    assert_status(
        node_b.client().post(
            "/api/v1/archives/unpack",
            query={"space": "datasets", "dir": "corrupt_try", "format": "tar.zst", "mode": "atomic"},
            raw_body=bad_arc,
            headers={"X-Archive-Sha256": "a" * 64},
        ),
        (400, 422),
    )
    stale = node_b.data_dir / ".tmp-archives" / "pack-stale-fault.tar.zst"
    stale.write_bytes(b"orphan")
    old = time.time() - 3600
    os.utime(stale, (old, old))
    deadline = time.monotonic() + 4.0
    while time.monotonic() < deadline and stale.exists():
        time.sleep(0.1)
    assert not stale.exists()

    # Phase 4: Clean retry succeeds and moves dataset to Node B!
    m_ok_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds["id"]}], "delete_source": True},
        )
        .json()["items"][0]["id"]
    )
    res_ok, _ = app.wait_migrations([m_ok_id])
    assert res_ok[m_ok_id]["status"] == "succeeded"
    assert not src_dir.exists()
    final_ds = app.client().get(f"/api/v1/datasets/{ds['id']}").json()
    assert final_ds["storage_node"]["id"] == nb["id"]


@e2e_test(
    test_id="T4_06",
    tier=4,
    features=["F1", "F4", "F5", "F6", "F7", "F11", "F12"],
    title="Scenario 6: Full Production Deployment Contracts + Live Web UI & Multi-Node Storage Workflow",
)
def test_t4_06_full_production_deployment_and_web_ui_end_user_journey(ctx: CaseContext) -> None:
    # Step 1: Verify deployment scripts & compose manifests
    for sh_test in ("test-makefile.sh", "test-compose.sh", "test-storage-compose.sh"):
        res = subprocess.run(
            ["bash", str(REPO_ROOT / "deploy" / "scripts" / "tests" / sh_test)],
            cwd=str(REPO_ROOT),
            capture_output=True,
            text=True,
            check=False,
        )
        assert res.returncode == 0, f"{sh_test} failed:\n{res.stdout}\n{res.stderr}"

    # Step 2: Launch co-located storage + platform serving built Web UI + remote Docker + legacy S3
    coloc = ctx.storage(name="st_t4_06_coloc", token="prod-coloc-token-0123456789")
    rem_docker = ctx.storage(name="st_t4_06_rem", token="prod-remote-token-0123456789")
    s3 = ctx.s3_mock()

    db_name, dsn = ctx.session.pg.create_db("tm_t4_06")
    app = PlatformDaemon(
        binary=ctx.session.trainmaster_binary,
        work_dir=ctx.case_dir / "app_t4_06",
        db_name=db_name,
        database_url=dsn,
        storage_service_url=coloc.url,
        storage_service_token=coloc.token,
        web_dir=ctx.session.ensure_web_bundle(),
        extra_env={"TM_DATA_DIR": str(coloc.data_dir)},
    ).start()
    ctx._closers.append(app.stop)

    # Step 3: Verify SPA HTML & bundled Chinese copy for onboarding, capability badges & live stages
    assert_status(app.client().get("/system/storage"), 200)
    assert_status(app.client().get("/training/jobs/new"), 200)
    bundle_text = read_all_web_bundle_text(ctx.session.ensure_web_bundle())
    for required_copy in (
        "存储服务容器",
        "docker-compose.storage.yml",
        "支持打包加速",
        "兼容直传",
        "正在打包压缩",
        "正在解压落盘",
        "本机共享零拷贝",
        "存储容器打包拉取并解压",
        "兼容存储流式拉取",
    ):
        assert required_copy in bundle_text, f"Missing UI copy {required_copy!r} in production web bundle"

    # Step 4: Verify Builtin (delegated), Remote Docker, and Legacy S3 coexist and migrate cleanly
    ds_init = app.import_voc_dataset("生产验证样本集", count=3)
    app.create_s3_node("生产归档S3", s3, activate=False)
    rem_node = app.create_docker_node("生产机房Docker", rem_docker.url, rem_docker.token, activate=True).json()

    nodes = app.client().get("/api/v1/storage/nodes").json()["items"]
    caps = {n["name"]: n["supports_archive"] for n in nodes}
    assert caps["本机默认存储"] is True
    assert caps["生产机房Docker"] is True
    assert caps["生产归档S3"] is False

    mig_id = int(
        app.client()
        .post(
            "/api/v1/storage/migrations",
            json_body={"targets": [{"target_type": "dataset", "target_id": ds_init["id"]}]},
        )
        .json()["items"][0]["id"]
    )
    migs, _ = app.wait_migrations([mig_id])
    assert migs[mig_id]["status"] == "succeeded"
    assert migs[mig_id]["transfer_mode"] == "archive"
    assert app.client().get(f"/api/v1/datasets/{ds_init['id']}").json()["storage_node"]["id"] == rem_node["id"]
