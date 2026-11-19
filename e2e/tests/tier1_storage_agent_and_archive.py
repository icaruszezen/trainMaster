"""Tier 1 Feature Coverage E2E tests for F1, F2, F3, and F4 (23 genuine tests)."""

from __future__ import annotations

import io
import os
import subprocess
import time
import zipfile

from e2e.harness.cluster import CaseContext, REPO_ROOT
from e2e.harness.fixtures import (
    make_png_bytes,
    make_tar_zst,
    make_yolo_dataset_zip,
    populate_yolo_dir,
    sha256_bytes,
    snapshot_tree,
)
from e2e.harness.registry import e2e_test
from e2e.tests._helpers import (
    assert_hex64,
    assert_status,
    storage_delete_dir,
    storage_delete_file,
    storage_get_file,
    storage_head_file,
    storage_mkdir,
    storage_put_file,
    storage_rename_dir,
)


# ==============================================================================
# F1: Standalone Storage Container HTTP Agent (trainmaster-storage) — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F1_01",
    tier=1,
    features="F1",
    title="trainmaster-storage /healthz responds 200 without auth and initializes data spaces",
)
def test_t1_f1_01_healthz_and_auto_dirs(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f1_01", token="secret-token-alpha-123456")
    unauth = daemon.client(token=None).get("/healthz")
    assert_status(unauth, 200, "unauthenticated GET /healthz")
    body = unauth.json()
    assert body.get("ok") is True
    assert body.get("service") == "trainmaster-storage"

    for subdir in ("datasets", "exports", ".tmp-archives"):
        assert (daemon.data_dir / subdir).is_dir(), f"Expected auto-created directory {subdir}"


@e2e_test(
    test_id="T1_F1_02",
    tier=1,
    features="F1",
    title="GET /api/v1/info returns supports_archive=true, hostname, display_path, and statfs usage",
)
def test_t1_f1_02_info_endpoint_usage_and_capability(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f1_02", token="secret-token-beta-123456")
    resp = daemon.client().get("/api/v1/info")
    assert_status(resp, 200, "GET /api/v1/info")
    info = resp.json()
    assert info.get("ok") is True
    assert info.get("supports_archive") is True
    assert isinstance(info.get("hostname"), str) and len(info["hostname"]) > 0
    assert isinstance(info.get("display_path"), str) and len(info["display_path"]) > 0
    usage = info.get("usage")
    assert isinstance(usage, dict), f"Expected usage object, got {usage!r}"
    assert int(usage.get("total_bytes", 0)) > 0
    assert int(usage.get("free_bytes", 0)) > 0


@e2e_test(
    test_id="T1_F1_03",
    tier=1,
    features="F1",
    title="Spaces directory lifecycle: MkdirDir, ListTopDirs, RenameDir, and RemoveDirAll",
)
def test_t1_f1_03_spaces_dirs_crud(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f1_03")
    mk = storage_mkdir(daemon, "datasets", "line_alpha", exclusive=True)
    assert_status(mk, (200, 201, 204), "MkdirDir datasets/line_alpha")
    assert (daemon.data_dir / "datasets" / "line_alpha").is_dir()

    put = storage_put_file(daemon, "datasets", "line_alpha", "sub/img01.png", make_png_bytes(16, 16))
    assert_status(put, (200, 201), "PutFile inside line_alpha")

    ls_resp = daemon.client().get("/api/v1/spaces/datasets/dirs")
    assert_status(ls_resp, 200, "ListTopDirs")
    assert "line_alpha" in ls_resp.text

    ren = storage_rename_dir(daemon, "datasets", "line_alpha", "line_beta")
    assert_status(ren, (200, 204), "RenameDir line_alpha -> line_beta")
    assert not (daemon.data_dir / "datasets" / "line_alpha").exists()
    assert (daemon.data_dir / "datasets" / "line_beta" / "sub" / "img01.png").is_file()

    rm = storage_delete_dir(daemon, "datasets", "line_beta", recursive=True)
    assert_status(rm, (200, 204), "RemoveDirAll line_beta")
    assert not (daemon.data_dir / "datasets" / "line_beta").exists()


@e2e_test(
    test_id="T1_F1_04",
    tier=1,
    features="F1",
    title="Spaces files CRUD with HEAD metadata and RFC 7233 HTTP Range partial reads",
)
def test_t1_f1_04_spaces_files_crud_and_range(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f1_04")
    payload = make_png_bytes(40, 30, (12, 140, 230))
    assert_status(storage_mkdir(daemon, "datasets", "ds_files"), (200, 201, 204))

    put_resp = storage_put_file(daemon, "datasets", "ds_files", "nested/part_01.png", payload)
    assert_status(put_resp, (200, 201), "PUT file")

    head_resp = storage_head_file(daemon, "datasets", "ds_files", "nested/part_01.png")
    assert_status(head_resp, 200, "HEAD file")

    full_resp = storage_get_file(daemon, "datasets", "ds_files", "nested/part_01.png")
    assert_status(full_resp, 200, "GET full file")
    assert full_resp.body == payload

    range_resp = storage_get_file(
        daemon,
        "datasets",
        "ds_files",
        "nested/part_01.png",
        headers={"Range": "bytes=0-15"},
    )
    assert_status(range_resp, 206, "GET byte range 0-15")
    assert range_resp.body == payload[:16]
    assert "bytes 0-15/" in (range_resp.headers.get("Content-Range") or "")

    del_resp = storage_delete_file(daemon, "datasets", "ds_files", "nested/part_01.png")
    assert_status(del_resp, (200, 204), "DELETE file")
    assert not (daemon.data_dir / "datasets" / "ds_files" / "nested" / "part_01.png").exists()


@e2e_test(
    test_id="T1_F1_05",
    tier=1,
    features="F1",
    title="GET /api/v1/spaces/datasets/walk returns lexical regular-file metadata entries",
)
def test_t1_f1_05_spaces_walk_lexical_order(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f1_05")
    ds_root = daemon.data_dir / "datasets" / "walk_ds"
    (ds_root / "b_dir").mkdir(parents=True, exist_ok=True)
    (ds_root / "a_dir").mkdir(parents=True, exist_ok=True)
    (ds_root / "b_dir" / "02.txt").write_bytes(b"two")
    (ds_root / "a_dir" / "01.txt").write_bytes(b"one-1")

    resp = daemon.client().get("/api/v1/spaces/datasets/walk", query={"dir": "walk_ds"})
    assert_status(resp, 200, "GET /walk")
    data = resp.json()
    items = data["items"] if isinstance(data, dict) and "items" in data else data
    assert isinstance(items, list)
    file_entries = [e for e in items if not e.get("is_dir")]
    rels = [e["rel"] for e in file_entries]
    assert rels == sorted(rels), f"Expected lexical walk order, got {rels}"
    assert "a_dir/01.txt" in rels and "b_dir/02.txt" in rels
    sizes = {e["rel"]: int(e["size"]) for e in file_entries}
    assert sizes["a_dir/01.txt"] == 5 and sizes["b_dir/02.txt"] == 3


@e2e_test(
    test_id="T1_F1_06",
    tier=1,
    features="F1",
    title="POST /api/v1/spaces/copy-within batch hardlinks files across datasets and exports spaces",
)
def test_t1_f1_06_copy_within_batch_hardlink(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f1_06")
    src_dir = daemon.data_dir / "datasets" / "src_ds"
    src_dir.mkdir(parents=True, exist_ok=True)
    img_a = make_png_bytes(24, 24, (80, 90, 100))
    img_b = make_png_bytes(24, 24, (110, 120, 130))
    (src_dir / "a.png").write_bytes(img_a)
    (src_dir / "sub").mkdir()
    (src_dir / "sub" / "b.png").write_bytes(img_b)

    resp = daemon.client().post(
        "/api/v1/spaces/copy-within",
        json_body={
            "items": [
                {
                    "src_space": "datasets",
                    "src_dir": "src_ds",
                    "src_rel": "a.png",
                    "dst_space": "exports",
                    "dst_dir": "data-versions/project-1/v1",
                    "dst_rel": "images/train/a.png",
                },
                {
                    "src_space": "datasets",
                    "src_dir": "src_ds",
                    "src_rel": "sub/b.png",
                    "dst_space": "exports",
                    "dst_dir": "data-versions/project-1/v1",
                    "dst_rel": "images/val/b.png",
                },
            ]
        },
    )
    assert_status(resp, (200, 201, 204), "POST /api/v1/spaces/copy-within")
    dst_a = daemon.data_dir / "exports" / "data-versions" / "project-1" / "v1" / "images" / "train" / "a.png"
    dst_b = daemon.data_dir / "exports" / "data-versions" / "project-1" / "v1" / "images" / "val" / "b.png"
    assert dst_a.read_bytes() == img_a
    assert dst_b.read_bytes() == img_b
    assert dst_a.stat().st_ino == (src_dir / "a.png").stat().st_ino


# ==============================================================================
# F2: Dual-Format Archive Engine (.tar.zst & YOLO .zip) — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F2_01",
    tier=1,
    features="F2",
    title="POST /api/v1/archives/pack (tar.zst) returns accurate sha256, files_count, and raw_bytes",
)
def test_t1_f2_01_pack_tar_zst_metrics(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f2_01")
    ds_dir = daemon.data_dir / "datasets" / "pack_ds"
    (ds_dir / "images").mkdir(parents=True, exist_ok=True)
    b1 = make_png_bytes(32, 32, (20, 40, 60))
    b2 = b"<annotation><object><name>bolt</name></object></annotation>" * 8
    (ds_dir / "images" / "01.png").write_bytes(b1)
    (ds_dir / "images" / "01.xml").write_bytes(b2)

    resp = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "pack_ds", "format": "tar.zst"},
    )
    assert_status(resp, 201, "POST /api/v1/archives/pack tar.zst")
    meta = resp.json()
    assert meta["format"] == "tar.zst"
    assert int(meta["files_count"]) == 2
    assert int(meta["raw_bytes"]) == len(b1) + len(b2)
    assert int(meta["archive_bytes"]) > 0
    assert_hex64(meta["sha256"])
    assert isinstance(meta.get("archive_id"), str) and meta["archive_id"]


@e2e_test(
    test_id="T1_F2_02",
    tier=1,
    features="F2",
    title="GET /api/v1/archives/{id} streams checksummed archive and DELETE removes staged file",
)
def test_t1_f2_02_stream_and_delete_archive(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f2_02")
    ds_dir = daemon.data_dir / "datasets" / "stream_ds"
    ds_dir.mkdir(parents=True, exist_ok=True)
    (ds_dir / "sample.png").write_bytes(make_png_bytes(20, 20))
    (ds_dir / "sample.xml").write_bytes(b"<annotation/>")

    pack = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "stream_ds", "format": "tar.zst"},
    ).json()
    arc_id = pack["archive_id"]

    dest_file = ctx.case_dir / "downloaded.tar.zst"
    dl = daemon.client().stream_download(f"/api/v1/archives/{arc_id}", dest_file)
    assert dl.status == 200
    assert dl.size_bytes == int(pack["archive_bytes"])
    assert dl.sha256 == pack["sha256"]
    assert dl.headers.get("X-Archive-Sha256") == pack["sha256"]
    assert int(dl.headers.get("X-Archive-Files") or 0) == 2
    assert dest_file.read_bytes()[:4] == b"\x28\xb5\x2f\xfd"

    del_resp = daemon.client().delete(f"/api/v1/archives/{arc_id}")
    assert_status(del_resp, 204, "DELETE /api/v1/archives/{id}")
    after_get = daemon.client().get(f"/api/v1/archives/{arc_id}")
    assert_status(after_get, 404, "GET deleted archive")


@e2e_test(
    test_id="T1_F2_03",
    tier=1,
    features="F2",
    title="Pack and atomic unpack of tar.zst preserves exact file bytes and microsecond ModTime",
)
def test_t1_f2_03_tar_zst_unpack_preserves_microsecond_mtime(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f2_03_a")
    node_b = ctx.storage(name="st_f2_03_b")

    src_dir = node_a.data_dir / "datasets" / "mtime_src"
    (src_dir / "sub").mkdir(parents=True, exist_ok=True)
    f1 = src_dir / "sub" / "frame_01.png"
    f2 = src_dir / "sub" / "frame_01.xml"
    f1.write_bytes(make_png_bytes(28, 28, (50, 100, 150)))
    f2.write_bytes(b"<annotation><size><width>28</width></size></annotation>")
    # Set deterministic microsecond timestamps
    t1_ns = 1_710_000_123_456_000_000
    t2_ns = 1_715_222_987_654_000_000
    os.utime(f1, ns=(t1_ns, t1_ns))
    os.utime(f2, ns=(t2_ns, t2_ns))

    pack = node_a.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "mtime_src", "format": "tar.zst"},
    ).json()
    arc_bytes = node_a.client().get(f"/api/v1/archives/{pack['archive_id']}").body

    unpack = node_b.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "mtime_dst", "format": "tar.zst", "mode": "atomic"},
        raw_body=arc_bytes,
        headers={
            "Content-Type": "application/octet-stream",
            "X-Archive-Sha256": pack["sha256"],
            "X-Archive-Raw-Bytes": str(pack["raw_bytes"]),
        },
    )
    assert_status(unpack, 200, "POST /api/v1/archives/unpack")
    res = unpack.json()
    assert int(res["files_count"]) == 2
    assert int(res["raw_bytes"]) == int(pack["raw_bytes"])

    snap_src = snapshot_tree(src_dir)
    snap_dst = snapshot_tree(node_b.data_dir / "datasets" / "mtime_dst")
    assert set(snap_src) == set(snap_dst)
    for rel, s_info in snap_src.items():
        d_info = snap_dst[rel]
        assert d_info.sha256 == s_info.sha256
        assert abs(d_info.mtime_ns - s_info.mtime_ns) < 10_000, (
            f"ModTime drift > 10us for {rel}: src={s_info.mtime_ns} dst={d_info.mtime_ns}"
        )


@e2e_test(
    test_id="T1_F2_04",
    tier=1,
    features="F2",
    title="YOLO .zip pack stores compressed images with ZIP_STORED and deflates labels/data.yaml",
)
def test_t1_f2_04_yolo_zip_compression_methods(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f2_04")
    dv_dir = daemon.data_dir / "exports" / "data-versions" / "project-1" / "v1"
    populate_yolo_dir(dv_dir, count=4, class_name="dent")

    pack_resp = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={
            "space": "exports",
            "dir": "data-versions/project-1/v1",
            "format": "zip",
        },
    )
    assert_status(pack_resp, 201, "pack YOLO zip")
    pack = pack_resp.json()
    zip_raw = daemon.client().get(f"/api/v1/archives/{pack['archive_id']}").body
    assert sha256_bytes(zip_raw) == pack["sha256"]

    with zipfile.ZipFile(io.BytesIO(zip_raw), "r") as zf:
        infos = {zi.filename: zi for zi in zf.infolist() if not zi.is_dir()}
        assert "data.yaml" in infos
        assert infos["data.yaml"].compress_type == zipfile.ZIP_DEFLATED
        png_entries = [zi for name, zi in infos.items() if name.endswith(".png")]
        txt_entries = [zi for name, zi in infos.items() if name.endswith(".txt")]
        assert len(png_entries) == 4
        assert all(zi.compress_type == zipfile.ZIP_STORED for zi in png_entries)
        assert all(zi.compress_type == zipfile.ZIP_DEFLATED for zi in txt_entries)


@e2e_test(
    test_id="T1_F2_05",
    tier=1,
    features="F2",
    title="POST /api/v1/archives/pack with subset paths packs only selected relative files",
)
def test_t1_f2_05_pack_subset_paths_filtering(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f2_05_a")
    ds_dir = node_a.data_dir / "datasets" / "subset_ds"
    ds_dir.mkdir(parents=True, exist_ok=True)
    for idx in range(1, 5):
        (ds_dir / f"img_{idx}.png").write_bytes(make_png_bytes(16, 16, (idx * 30, 80, 120)))

    pack_resp = node_a.client().post(
        "/api/v1/archives/pack",
        json_body={
            "space": "datasets",
            "dir": "subset_ds",
            "format": "tar.zst",
            "paths": ["img_1.png", "img_3.png"],
        },
    )
    assert_status(pack_resp, 201, "subset pack")
    pack = pack_resp.json()
    assert int(pack["files_count"]) == 2

    arc_raw = node_a.client().get(f"/api/v1/archives/{pack['archive_id']}").body
    unpack_resp = node_a.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "subset_out", "format": "tar.zst", "mode": "atomic"},
        raw_body=arc_raw,
        headers={"X-Archive-Sha256": pack["sha256"]},
    )
    assert_status(unpack_resp, 200, "subset unpack")
    extracted = sorted(snapshot_tree(node_a.data_dir / "datasets" / "subset_out"))
    assert extracted == ["img_1.png", "img_3.png"]


@e2e_test(
    test_id="T1_F2_06",
    tier=1,
    features="F2",
    title="POST /api/v1/archives/pull-unpack streams peer archive directly from Node A to Node B",
)
def test_t1_f2_06_peer_pull_unpack_between_storage_containers(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_f2_06_a", token="token-node-a-1234567890")
    node_b = ctx.storage(name="st_f2_06_b", token="token-node-b-1234567890")

    src_dir = node_a.data_dir / "datasets" / "peer_src"
    (src_dir / "cam1").mkdir(parents=True, exist_ok=True)
    (src_dir / "cam1" / "shot1.png").write_bytes(make_png_bytes(36, 24, (200, 100, 50)))
    (src_dir / "cam1" / "shot1.xml").write_bytes(b"<annotation><object><name>crack</name></object></annotation>")

    pack = node_a.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "peer_src", "format": "tar.zst"},
    ).json()
    source_url = f"{node_a.url}/api/v1/archives/{pack['archive_id']}"

    pull_resp = node_b.client().post(
        "/api/v1/archives/pull-unpack",
        json_body={
            "source_url": source_url,
            "source_token": node_a.token,
            "expected_sha256": pack["sha256"],
            "expected_archive_bytes": pack["archive_bytes"],
            "space": "datasets",
            "dir": "peer_dst",
            "format": "tar.zst",
            "mode": "atomic",
        },
    )
    assert_status(pull_resp, 200, "POST /api/v1/archives/pull-unpack")
    assert int(pull_resp.json()["files_count"]) == 2
    assert node_a.spy is not None
    assert len(node_a.spy.matching_requests(f"/api/v1/archives/{pack['archive_id']}", method="GET")) == 1

    snap_a = snapshot_tree(src_dir)
    snap_b = snapshot_tree(node_b.data_dir / "datasets" / "peer_dst")
    assert {k: v.sha256 for k, v in snap_a.items()} == {k: v.sha256 for k, v in snap_b.items()}


# ==============================================================================
# F3: Archive Security, Atomic Extraction & TTL Janitor — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F3_01",
    tier=1,
    features="F3",
    title="Unpack commits atomically via sibling staging dir and leaves zero residual temp files",
)
def test_t1_f3_01_atomic_unpack_leaves_zero_temp_files(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f3_01")
    archive = make_tar_zst(
        [
            ("batch/img_a.png", make_png_bytes(20, 20), 1_700_100_000.5),
            ("batch/img_a.xml", b"<annotation/>", 1_700_100_001.25),
        ]
    )
    digest = sha256_bytes(archive)

    resp = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "clean_atomic", "format": "tar.zst", "mode": "atomic"},
        raw_body=archive,
        headers={"X-Archive-Sha256": digest},
    )
    assert_status(resp, 200, "atomic unpack")
    assert (daemon.data_dir / "datasets" / "clean_atomic" / "batch" / "img_a.png").is_file()

    tmp_leftovers = list((daemon.data_dir / ".tmp-archives").iterdir())
    unpack_leftovers = [p for p in (daemon.data_dir / "datasets").iterdir() if p.name.startswith(".unpack-")]
    assert tmp_leftovers == [], f"Expected empty .tmp-archives after unpack, found {tmp_leftovers}"
    assert unpack_leftovers == [], f"Expected zero .unpack-* staging dirs, found {unpack_leftovers}"


@e2e_test(
    test_id="T1_F3_02",
    tier=1,
    features="F3",
    title="Unpack with mode=merge_skip retains existing conflicting files and adds new files",
)
def test_t1_f3_02_unpack_merge_skip_preserves_existing(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f3_02")
    target = daemon.data_dir / "datasets" / "merge_skip_ds"
    target.mkdir(parents=True, exist_ok=True)
    orig_bytes = b"ORIGINAL_PROTECTED_CONTENT"
    (target / "existing.txt").write_bytes(orig_bytes)

    archive = make_tar_zst(
        [
            ("existing.txt", b"NEW_CONFLICTING_CONTENT", 1_720_000_000.0),
            ("added.txt", b"BRAND_NEW_FILE", 1_720_000_001.0),
        ]
    )
    resp = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "merge_skip_ds", "format": "tar.zst", "mode": "merge_skip"},
        raw_body=archive,
        headers={"X-Archive-Sha256": sha256_bytes(archive)},
    )
    assert_status(resp, 200, "merge_skip unpack")
    assert (target / "existing.txt").read_bytes() == orig_bytes
    assert (target / "added.txt").read_bytes() == b"BRAND_NEW_FILE"


@e2e_test(
    test_id="T1_F3_03",
    tier=1,
    features="F3",
    title="Unpack with mode=merge_overwrite replaces conflicting files while keeping untouched files",
)
def test_t1_f3_03_unpack_merge_overwrite_replaces_conflicts(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f3_03")
    target = daemon.data_dir / "datasets" / "merge_over_ds"
    target.mkdir(parents=True, exist_ok=True)
    (target / "conflict.txt").write_bytes(b"OLD_VERSION")
    (target / "untouched.txt").write_bytes(b"KEEP_ME")

    archive = make_tar_zst(
        [
            ("conflict.txt", b"UPDATED_VERSION_V2", 1_725_000_000.0),
            ("fresh.txt", b"FRESH_CONTENT", 1_725_000_002.0),
        ]
    )
    resp = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "merge_over_ds", "format": "tar.zst", "mode": "merge_overwrite"},
        raw_body=archive,
        headers={"X-Archive-Sha256": sha256_bytes(archive)},
    )
    assert_status(resp, 200, "merge_overwrite unpack")
    assert (target / "conflict.txt").read_bytes() == b"UPDATED_VERSION_V2"
    assert (target / "untouched.txt").read_bytes() == b"KEEP_ME"
    assert (target / "fresh.txt").read_bytes() == b"FRESH_CONTENT"


@e2e_test(
    test_id="T1_F3_04",
    tier=1,
    features="F3",
    title="TTL janitor sweeps stale .tmp-archives and .unpack-* directories without touching datasets",
)
def test_t1_f3_04_ttl_janitor_sweeps_stale_artifacts(ctx: CaseContext) -> None:
    pre_dir = ctx.case_dir / "st_janitor" / "data"
    (pre_dir / ".tmp-archives").mkdir(parents=True, exist_ok=True)
    (pre_dir / "datasets" / ".unpack-orphan-123").mkdir(parents=True, exist_ok=True)
    (pre_dir / "datasets" / "keep_real_dataset").mkdir(parents=True, exist_ok=True)

    stale_arc = pre_dir / ".tmp-archives" / "pack-orphan.tar.zst"
    stale_unpack_file = pre_dir / "datasets" / ".unpack-orphan-123" / "partial.bin"
    keep_file = pre_dir / "datasets" / "keep_real_dataset" / "img.png"

    stale_arc.write_bytes(b"stale-archive")
    stale_unpack_file.write_bytes(b"stale-unpack")
    keep_file.write_bytes(make_png_bytes(12, 12))

    old_ts = time.time() - 7200
    os.utime(stale_arc, (old_ts, old_ts))
    os.utime(stale_unpack_file, (old_ts, old_ts))
    os.utime(stale_unpack_file.parent, (old_ts, old_ts))

    daemon = ctx.storage(name="st_janitor", temp_ttl="200ms")
    deadline = time.monotonic() + 4.0
    while time.monotonic() < deadline:
        if not stale_arc.exists() and not stale_unpack_file.parent.exists():
            break
        time.sleep(0.1)

    assert not stale_arc.exists(), "Expected TTL janitor to remove stale .tmp-archives/pack-orphan.tar.zst"
    assert not stale_unpack_file.parent.exists(), "Expected TTL janitor to remove stale .unpack-orphan-123"
    assert (daemon.data_dir / "datasets" / "keep_real_dataset" / "img.png").is_file()


@e2e_test(
    test_id="T1_F3_05",
    tier=1,
    features="F3",
    title="Unpack validates matching X-Archive-Sha256 and X-Archive-Raw-Bytes for zip format",
)
def test_t1_f3_05_zip_unpack_with_sha256_and_raw_bytes_guard(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_f3_05")
    zip_payload = make_yolo_dataset_zip(count=3, class_name="pinhole")
    with zipfile.ZipFile(io.BytesIO(zip_payload), "r") as zf:
        raw_total = sum(zi.file_size for zi in zf.infolist() if not zi.is_dir())

    resp = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "exports", "dir": "data-versions/p1/v1", "format": "zip", "mode": "atomic"},
        raw_body=zip_payload,
        headers={
            "X-Archive-Sha256": sha256_bytes(zip_payload),
            "X-Archive-Raw-Bytes": str(raw_total),
        },
    )
    assert_status(resp, 200, "zip unpack with exact X-Archive-Sha256 & X-Archive-Raw-Bytes")
    body = resp.json()
    assert int(body["raw_bytes"]) == raw_total
    assert (daemon.data_dir / "exports" / "data-versions" / "p1" / "v1" / "data.yaml").is_file()


# ==============================================================================
# F4: Storage Docker Image, Compose Topology & Shell Scripts — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T1_F4_01",
    tier=1,
    features="F4",
    title="deploy/docker/Dockerfile.storage defines CGO_ENABLED=0 build, entrypoint, and /healthz check",
)
def test_t1_f4_01_dockerfile_storage_contract(ctx: CaseContext) -> None:
    _ = ctx
    df_path = REPO_ROOT / "deploy" / "docker" / "Dockerfile.storage"
    assert df_path.is_file(), f"Missing {df_path}"
    content = df_path.read_text(encoding="utf-8")
    assert "CGO_ENABLED=0" in content
    assert "./cmd/trainmaster-storage" in content
    assert "storage-entrypoint.sh" in content
    assert "/healthz" in content
    assert "8090" in content


@e2e_test(
    test_id="T1_F4_02",
    tier=1,
    features="F4",
    title="deploy/docker/storage-entrypoint.sh passes bash -n, creates spaces, and avoids recursive chown",
)
def test_t1_f4_02_storage_entrypoint_script_contract(ctx: CaseContext) -> None:
    _ = ctx
    ep_path = REPO_ROOT / "deploy" / "docker" / "storage-entrypoint.sh"
    assert ep_path.is_file(), f"Missing {ep_path}"
    res = subprocess.run(["bash", "-n", str(ep_path)], capture_output=True, text=True, check=False)
    assert res.returncode == 0, f"bash -n failed on storage-entrypoint.sh: {res.stderr}"

    text = ep_path.read_text(encoding="utf-8")
    assert "PUID" in text and "PGID" in text
    assert "datasets" in text and "exports" in text and ".tmp-archives" in text
    assert "chown -R" not in text, "storage-entrypoint.sh must NOT run slow recursive chown -R"


@e2e_test(
    test_id="T1_F4_03",
    tier=1,
    features="F4",
    title="docker-compose.storage.yml defines standalone storage container service and env contract",
)
def test_t1_f4_03_standalone_storage_compose_manifest(ctx: CaseContext) -> None:
    _ = ctx
    compose_path = REPO_ROOT / "docker-compose.storage.yml"
    assert compose_path.is_file(), f"Missing {compose_path}"
    text = compose_path.read_text(encoding="utf-8")
    assert "Dockerfile.storage" in text
    assert "8090" in text
    assert "TM_STORAGE_TOKEN" in text
    assert ":/data" in text


@e2e_test(
    test_id="T1_F4_04",
    tier=1,
    features="F4",
    title="Root docker-compose.yml and .env.example wire co-located storage service and TM_STORAGE_SERVICE_URL",
)
def test_t1_f4_04_root_compose_and_env_example_contract(ctx: CaseContext) -> None:
    _ = ctx
    root_compose = (REPO_ROOT / "docker-compose.yml").read_text(encoding="utf-8")
    env_example = (REPO_ROOT / ".env.example").read_text(encoding="utf-8")
    assert "storage:" in root_compose
    assert "TM_STORAGE_SERVICE_URL" in root_compose
    assert "TM_STORAGE_TOKEN" in env_example or "TM_STORAGE_SERVICE_URL" in env_example


@e2e_test(
    test_id="T1_F4_05",
    tier=1,
    features="F4",
    title="Makefile exposes build-storage target and passes test-makefile.sh regression suite",
)
def test_t1_f4_05_makefile_build_storage_and_regression_script(ctx: CaseContext) -> None:
    _ = ctx
    makefile_text = (REPO_ROOT / "Makefile").read_text(encoding="utf-8")
    assert "build-storage" in makefile_text
    assert "trainmaster-storage" in makefile_text

    res = subprocess.run(
        ["bash", str(REPO_ROOT / "deploy" / "scripts" / "tests" / "test-makefile.sh")],
        cwd=str(REPO_ROOT),
        capture_output=True,
        text=True,
        check=False,
    )
    assert res.returncode == 0, f"test-makefile.sh failed:\n{res.stdout}\n{res.stderr}"


@e2e_test(
    test_id="T1_F4_06",
    tier=1,
    features="F4",
    title="Deployment compose test scripts test-compose.sh and test-storage-compose.sh pass",
)
def test_t1_f4_06_compose_shell_test_suites_pass(ctx: CaseContext) -> None:
    _ = ctx
    for script_name in ("test-compose.sh", "test-storage-compose.sh"):
        script_path = REPO_ROOT / "deploy" / "scripts" / "tests" / script_name
        assert script_path.is_file(), f"Missing deployment test script {script_path}"
        res = subprocess.run(
            ["bash", str(script_path)],
            cwd=str(REPO_ROOT),
            capture_output=True,
            text=True,
            check=False,
        )
        assert res.returncode == 0, f"{script_name} failed (rc={res.returncode}):\n{res.stdout}\n{res.stderr}"
