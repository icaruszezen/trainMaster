"""Tier 2 Boundary, Negative, and Security E2E tests for F1, F2, F3, and F4 (23 genuine tests)."""

from __future__ import annotations

import os
import subprocess

from e2e.harness.cluster import CaseContext, REPO_ROOT
from e2e.harness.fixtures import (
    make_devnode_tar_zst,
    make_hardlink_tar_zst,
    make_png_bytes,
    make_rle_bomb_tar_zst,
    make_symlink_tar_zst,
    make_symlink_zip,
    make_tar_zst,
    make_tarslip_tar_zst,
    make_zipslip_zip,
    sha256_bytes,
)
from e2e.harness.registry import e2e_test
from e2e.tests._helpers import (
    assert_hex64,
    assert_status,
    storage_delete_dir,
    storage_get_file,
    storage_mkdir,
    storage_put_file,
)


# ==============================================================================
# F1 Tier 2: Storage Agent Auth, Path Confinement, Range & POSIX Errors — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F1_01",
    tier=2,
    features="F1",
    title="Storage agent rejects missing or wrong Bearer token with 401 and allows empty-token open mode",
)
def test_t2_f1_01_bearer_auth_rejection_and_open_mode(ctx: CaseContext) -> None:
    secured = ctx.storage(name="st_t2_f1_01_sec", token="correct-secret-token-9999")
    for path in ("/api/v1/info", "/api/v1/spaces/datasets/dirs"):
        no_tok = secured.client(token=None).get(path)
        assert_status(no_tok, 401, f"missing token on {path}")
        bad_tok = secured.client(token="wrong-secret-token-9999").get(path)
        assert_status(bad_tok, 401, f"wrong token on {path}")

    bad_pack = secured.client(token="wrong").post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "x", "format": "tar.zst"},
    )
    assert_status(bad_pack, 401, "wrong token on POST /api/v1/archives/pack")

    open_daemon = ctx.storage(name="st_t2_f1_01_open", token="")
    open_resp = open_daemon.client(token=None).get("/api/v1/info")
    assert_status(open_resp, 200, "empty TM_STORAGE_TOKEN allows unauthenticated /api/v1/info")


@e2e_test(
    test_id="T2_F1_02",
    tier=2,
    features="F1",
    title="Storage agent rejects invalid space names outside {datasets, exports}",
)
def test_t2_f1_02_invalid_space_name_rejected(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f1_02")
    for bad_space in ("models", "tmp", ".tmp-archives", "etc"):
        resp = daemon.client().get(f"/api/v1/spaces/{bad_space}/dirs")
        assert_status(resp, (400, 404, 422), f"invalid space {bad_space!r}")


@e2e_test(
    test_id="T2_F1_03",
    tier=2,
    features="F1",
    title="Storage agent rejects ../, leading slash, and backslash traversal on /dirs and /files",
)
def test_t2_f1_03_path_traversal_rejected_on_dirs_and_files(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f1_03")
    outside_marker = daemon.work_dir / "escaped_file.txt"

    for evil_dir in ("../escaped", "../../etc", "/abs/path", "sub\\..\\escape"):
        r_dir = storage_mkdir(daemon, "datasets", evil_dir)
        assert_status(r_dir, (400, 422), f"MkdirDir traversal {evil_dir!r}")

    assert_status(storage_mkdir(daemon, "datasets", "safe_ds"), (200, 201, 204))
    for evil_rel in ("../../escaped_file.txt", "../escaped_file.txt", "/escaped_file.txt", "a\\..\\..\\escaped_file.txt"):
        r_put = storage_put_file(daemon, "datasets", "safe_ds", evil_rel, b"pwned")
        assert_status(r_put, (400, 422), f"PutFile traversal {evil_rel!r}")

    assert not outside_marker.exists(), "Traversal payload must never write outside space root"


@e2e_test(
    test_id="T2_F1_04",
    tier=2,
    features="F1",
    title="Exclusive MkdirDir and CreateExclusive file PUT return 409 Conflict when target exists",
)
def test_t2_f1_04_exclusive_mkdir_and_put_conflict(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f1_04")
    assert_status(storage_mkdir(daemon, "datasets", "dup_dir", exclusive=True), (200, 201, 204))
    dup_mk = storage_mkdir(daemon, "datasets", "dup_dir", exclusive=True)
    assert_status(dup_mk, 409, "duplicate exclusive MkdirDir")

    assert_status(
        storage_put_file(daemon, "datasets", "dup_dir", "once.txt", b"FIRST", exclusive=True),
        (200, 201),
    )
    dup_put = storage_put_file(daemon, "datasets", "dup_dir", "once.txt", b"SECOND", exclusive=True)
    assert_status(dup_put, 409, "duplicate exclusive file PUT")
    assert (daemon.data_dir / "datasets" / "dup_dir" / "once.txt").read_bytes() == b"FIRST"


@e2e_test(
    test_id="T2_F1_05",
    tier=2,
    features="F1",
    title="Unsatisfiable HTTP Range header returns 416 Range Not Satisfiable",
)
def test_t2_f1_05_unsatisfiable_http_range_416(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f1_05")
    assert_status(storage_mkdir(daemon, "datasets", "rng_ds"), (200, 201, 204))
    assert_status(storage_put_file(daemon, "datasets", "rng_ds", "tiny.bin", b"0123456789"), (200, 201))

    resp = storage_get_file(
        daemon,
        "datasets",
        "rng_ds",
        "tiny.bin",
        headers={"Range": "bytes=500-999"},
    )
    assert_status(resp, 416, "out-of-bounds Range request")


@e2e_test(
    test_id="T2_F1_06",
    tier=2,
    features="F1",
    title="Non-recursive RemoveEmptyDir rejects non-empty directory and copy-within rejects missing source",
)
def test_t2_f1_06_non_empty_rmdir_and_missing_copy_within_source(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f1_06")
    assert_status(storage_mkdir(daemon, "datasets", "non_empty"), (200, 201, 204))
    assert_status(storage_put_file(daemon, "datasets", "non_empty", "keep.txt", b"data"), (200, 201))

    rm_non_rec = storage_delete_dir(daemon, "datasets", "non_empty", recursive=False)
    assert_status(rm_non_rec, (400, 409, 422), "RemoveEmptyDir on non-empty dir")
    assert (daemon.data_dir / "datasets" / "non_empty" / "keep.txt").is_file()

    cp_missing = daemon.client().post(
        "/api/v1/spaces/copy-within",
        json_body={
            "items": [
                {
                    "src_space": "datasets",
                    "src_dir": "non_empty",
                    "src_rel": "does_not_exist.png",
                    "dst_space": "exports",
                    "dst_dir": "out",
                    "dst_rel": "does_not_exist.png",
                }
            ]
        },
    )
    assert_status(cp_missing, 404, "copy-within missing source file")


# ==============================================================================
# F2 Tier 2: Archive Engine Empty/0-Byte/Unicode/Deep-Nesting/Missing-ID — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F2_01",
    tier=2,
    features="F2",
    title="Packing and unpacking an empty 0-file directory succeeds with files_count=0 and raw_bytes=0",
)
def test_t2_f2_01_empty_zero_file_directory_pack_and_unpack(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f2_01")
    (daemon.data_dir / "datasets" / "empty_ds" / "empty_sub").mkdir(parents=True, exist_ok=True)

    pack_resp = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "empty_ds", "format": "tar.zst"},
    )
    assert_status(pack_resp, 201, "pack empty 0-file dir")
    pack = pack_resp.json()
    assert int(pack["files_count"]) == 0
    assert int(pack["raw_bytes"]) == 0
    assert_hex64(pack["sha256"])

    raw_arc = daemon.client().get(f"/api/v1/archives/{pack['archive_id']}").body
    up_resp = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "empty_restored", "format": "tar.zst", "mode": "atomic"},
        raw_body=raw_arc,
        headers={"X-Archive-Sha256": pack["sha256"]},
    )
    assert_status(up_resp, 200, "unpack empty 0-file archive")
    assert (daemon.data_dir / "datasets" / "empty_restored").is_dir()


@e2e_test(
    test_id="T2_F2_02",
    tier=2,
    features="F2",
    title="0-byte regular files and Chinese/Unicode filenames with spaces survive tar.zst and zip round-trips",
)
def test_t2_f2_02_zero_byte_and_unicode_filenames_roundtrip(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f2_02")
    src = daemon.data_dir / "datasets" / "unicode_src"
    (src / "产线A 质检组").mkdir(parents=True, exist_ok=True)
    (src / "产线A 质检组" / "缺陷 #1 (左).png").write_bytes(make_png_bytes(16, 16))
    (src / "产线A 质检组" / "空标注_0字节.txt").write_bytes(b"")

    for fmt in ("tar.zst", "zip"):
        pack = daemon.client().post(
            "/api/v1/archives/pack",
            json_body={"space": "datasets", "dir": "unicode_src", "format": fmt},
        ).json()
        raw = daemon.client().get(f"/api/v1/archives/{pack['archive_id']}").body
        dst_name = f"unicode_dst_{fmt.replace('.', '_')}"
        up = daemon.client().post(
            "/api/v1/archives/unpack",
            query={"space": "datasets", "dir": dst_name, "format": fmt, "mode": "atomic"},
            raw_body=raw,
            headers={"X-Archive-Sha256": pack["sha256"]},
        )
        assert_status(up, 200, f"unicode roundtrip {fmt}")
        dst_base = daemon.data_dir / "datasets" / dst_name / "产线A 质检组"
        assert (dst_base / "缺陷 #1 (左).png").is_file()
        zero_f = dst_base / "空标注_0字节.txt"
        assert zero_f.is_file() and zero_f.stat().st_size == 0


@e2e_test(
    test_id="T2_F2_03",
    tier=2,
    features="F2",
    title="Deeply nested 8-level directory structure packs and unpacks with intact hierarchy",
)
def test_t2_f2_03_deeply_nested_hierarchy_pack_unpack(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f2_03")
    deep_rel = "d1/d2/d3/d4/d5/d6/d7/d8/leaf_sample.png"
    leaf = daemon.data_dir / "datasets" / "deep_src" / deep_rel
    leaf.parent.mkdir(parents=True, exist_ok=True)
    img = make_png_bytes(18, 18, (11, 22, 33))
    leaf.write_bytes(img)

    pack = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "deep_src", "format": "tar.zst"},
    ).json()
    raw = daemon.client().get(f"/api/v1/archives/{pack['archive_id']}").body
    up = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "deep_dst", "format": "tar.zst", "mode": "atomic"},
        raw_body=raw,
        headers={"X-Archive-Sha256": pack["sha256"]},
    )
    assert_status(up, 200)
    assert (daemon.data_dir / "datasets" / "deep_dst" / deep_rel).read_bytes() == img


@e2e_test(
    test_id="T2_F2_04",
    tier=2,
    features="F2",
    title="Pack rejects unsupported archive format (400/422) and non-existent source directory (404)",
)
def test_t2_f2_04_pack_invalid_format_and_missing_dir(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f2_04")
    (daemon.data_dir / "datasets" / "real_dir").mkdir(parents=True, exist_ok=True)

    bad_fmt = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "real_dir", "format": "7z"},
    )
    assert_status(bad_fmt, (400, 422), "unsupported archive format 7z")

    missing_dir = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "nonexistent_dataset_dir", "format": "tar.zst"},
    )
    assert_status(missing_dir, 404, "missing source directory")


@e2e_test(
    test_id="T2_F2_05",
    tier=2,
    features="F2",
    title="Unknown archive ID returns 404 on GET and invalid subset paths in pack are rejected",
)
def test_t2_f2_05_unknown_archive_id_and_invalid_subset_paths(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f2_05")
    (daemon.data_dir / "datasets" / "sub_err").mkdir(parents=True, exist_ok=True)
    (daemon.data_dir / "datasets" / "sub_err" / "ok.txt").write_bytes(b"ok")

    not_found = daemon.client().get("/api/v1/archives/pack-does-not-exist-0000")
    assert_status(not_found, 404, "GET unknown archiveID")

    bad_subset = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={
            "space": "datasets",
            "dir": "sub_err",
            "format": "tar.zst",
            "paths": ["../escape.txt"],
        },
    )
    assert_status(bad_subset, (400, 422), "traversal path in subset paths[]")


@e2e_test(
    test_id="T2_F2_06",
    tier=2,
    features="F2",
    title="pull-unpack fails cleanly on wrong source_token or mismatched expected_archive_bytes",
)
def test_t2_f2_06_pull_unpack_wrong_token_and_size_mismatch(ctx: CaseContext) -> None:
    node_a = ctx.storage(name="st_t2_f2_06_a", token="secret-node-a-token-123")
    node_b = ctx.storage(name="st_t2_f2_06_b", token="secret-node-b-token-456")
    (node_a.data_dir / "datasets" / "src").mkdir(parents=True, exist_ok=True)
    (node_a.data_dir / "datasets" / "src" / "f.txt").write_bytes(b"payload-bytes")

    pack = node_a.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "src", "format": "tar.zst"},
    ).json()
    src_url = f"{node_a.url}/api/v1/archives/{pack['archive_id']}"

    bad_tok = node_b.client().post(
        "/api/v1/archives/pull-unpack",
        json_body={
            "source_url": src_url,
            "source_token": "wrong-token",
            "expected_sha256": pack["sha256"],
            "expected_archive_bytes": pack["archive_bytes"],
            "space": "datasets",
            "dir": "dst_bad_tok",
            "format": "tar.zst",
            "mode": "atomic",
        },
    )
    assert bad_tok.status >= 400
    assert not (node_b.data_dir / "datasets" / "dst_bad_tok").exists()

    bad_size = node_b.client().post(
        "/api/v1/archives/pull-unpack",
        json_body={
            "source_url": src_url,
            "source_token": node_a.token,
            "expected_sha256": pack["sha256"],
            "expected_archive_bytes": int(pack["archive_bytes"]) + 999,
            "space": "datasets",
            "dir": "dst_bad_size",
            "format": "tar.zst",
            "mode": "atomic",
        },
    )
    assert bad_size.status >= 400
    assert not (node_b.data_dir / "datasets" / "dst_bad_size").exists()
    assert list((node_b.data_dir / ".tmp-archives").iterdir()) == []


# ==============================================================================
# F3 Tier 2: Security (Tar-Slip, Zip-Slip, Symlinks, Bad SHA, 409, Bomb, 507) — 6 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F3_01",
    tier=2,
    features="F3",
    title="Unpack rejects tar-slip (../) .tar.zst and zip-slip (../../) .zip with zero escaped files",
)
def test_t2_f3_01_tarslip_and_zipslip_rejected(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f3_01")

    evil_tar = make_tarslip_tar_zst("../escaped_tar.txt", b"evil-tar")
    r_tar = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "slip_tar", "format": "tar.zst", "mode": "atomic"},
        raw_body=evil_tar,
        headers={"X-Archive-Sha256": sha256_bytes(evil_tar)},
    )
    assert_status(r_tar, (400, 422), "tar-slip rejection")
    assert not (daemon.data_dir / "datasets" / "escaped_tar.txt").exists()
    assert not (daemon.data_dir / "datasets" / "slip_tar").exists()

    evil_zip = make_zipslip_zip("../../escaped_zip.txt", b"evil-zip")
    r_zip = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "slip_zip", "format": "zip", "mode": "atomic"},
        raw_body=evil_zip,
        headers={"X-Archive-Sha256": sha256_bytes(evil_zip)},
    )
    assert_status(r_zip, (400, 422), "zip-slip rejection")
    assert not (daemon.data_dir / "escaped_zip.txt").exists()
    assert not (daemon.data_dir / "datasets" / "slip_zip").exists()


@e2e_test(
    test_id="T2_F3_02",
    tier=2,
    features="F3",
    title="Pack and unpack reject symlinks, hardlinks, and device nodes inside archives and source dirs",
)
def test_t2_f3_02_symlink_hardlink_and_device_node_rejected(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f3_02")

    # 1) Source directory containing symlink is rejected during pack
    sym_src = daemon.data_dir / "datasets" / "sym_pack_src"
    sym_src.mkdir(parents=True, exist_ok=True)
    (sym_src / "real.txt").write_bytes(b"ok")
    os.symlink("/etc/hosts", sym_src / "bad_link")
    pack_sym = daemon.client().post(
        "/api/v1/archives/pack",
        json_body={"space": "datasets", "dir": "sym_pack_src", "format": "tar.zst"},
    )
    assert_status(pack_sym, (400, 422), "pack must reject symlink in source dir")

    # 2) Unpack rejects tar symlink, hardlink, char device, and zip symlink
    for idx, (fmt, payload) in enumerate(
        [
            ("tar.zst", make_symlink_tar_zst()),
            ("tar.zst", make_hardlink_tar_zst()),
            ("tar.zst", make_devnode_tar_zst()),
            ("zip", make_symlink_zip()),
        ]
    ):
        resp = daemon.client().post(
            "/api/v1/archives/unpack",
            query={"space": "datasets", "dir": f"evil_special_{idx}", "format": fmt, "mode": "atomic"},
            raw_body=payload,
            headers={"X-Archive-Sha256": sha256_bytes(payload)},
        )
        assert_status(resp, (400, 422), f"unpack must reject special entry #{idx} ({fmt})")
        assert not (daemon.data_dir / "datasets" / f"evil_special_{idx}").exists()


@e2e_test(
    test_id="T2_F3_03",
    tier=2,
    features="F3",
    title="Unpack rejects missing or mismatched X-Archive-Sha256 before any extraction occurs",
)
def test_t2_f3_03_missing_or_corrupt_sha256_rejected_pre_extract(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f3_03")
    valid_arc = make_tar_zst([("ok.txt", b"clean-content", None)])

    no_hdr = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "no_sha_dst", "format": "tar.zst", "mode": "atomic"},
        raw_body=valid_arc,
    )
    assert_status(no_hdr, (400, 422), "missing X-Archive-Sha256")
    assert not (daemon.data_dir / "datasets" / "no_sha_dst").exists()

    wrong_sha = "0" * 64
    bad_hdr = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "bad_sha_dst", "format": "tar.zst", "mode": "atomic"},
        raw_body=valid_arc,
        headers={"X-Archive-Sha256": wrong_sha},
    )
    assert_status(bad_hdr, (400, 422), "mismatched X-Archive-Sha256")
    assert not (daemon.data_dir / "datasets" / "bad_sha_dst").exists()
    assert list((daemon.data_dir / ".tmp-archives").iterdir()) == []


@e2e_test(
    test_id="T2_F3_04",
    tier=2,
    features="F3",
    title="Unpack with mode=atomic returns 409 Conflict when destination directory already exists",
)
def test_t2_f3_04_atomic_unpack_conflict_when_target_exists(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f3_04")
    existing = daemon.data_dir / "datasets" / "already_here"
    existing.mkdir(parents=True, exist_ok=True)
    (existing / "precious.txt").write_bytes(b"DO_NOT_CLOBBER")

    incoming = make_tar_zst([("precious.txt", b"OVERWRITTEN", None)])
    resp = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "already_here", "format": "tar.zst", "mode": "atomic"},
        raw_body=incoming,
        headers={"X-Archive-Sha256": sha256_bytes(incoming)},
    )
    assert_status(resp, 409, "atomic unpack onto existing dir must return 409")
    assert (existing / "precious.txt").read_bytes() == b"DO_NOT_CLOBBER"


@e2e_test(
    test_id="T2_F3_05",
    tier=2,
    features="F3",
    title="Decompression bomb exceeding declared X-Archive-Raw-Bytes is aborted and cleaned up",
)
def test_t2_f3_05_decompression_bomb_limit_reader_guard(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f3_05")
    bomb = make_rle_bomb_tar_zst(uncompressed_mb=4)

    resp = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "bomb_dst", "format": "tar.zst", "mode": "atomic"},
        raw_body=bomb,
        headers={
            "X-Archive-Sha256": sha256_bytes(bomb),
            "X-Archive-Raw-Bytes": "256",
        },
    )
    assert_status(resp, (400, 413, 422), "decompression bomb exceeding X-Archive-Raw-Bytes")
    assert not (daemon.data_dir / "datasets" / "bomb_dst").exists()
    unpack_dirs = [p for p in (daemon.data_dir / "datasets").iterdir() if p.name.startswith(".unpack-")]
    assert unpack_dirs == []


@e2e_test(
    test_id="T2_F3_06",
    tier=2,
    features="F3",
    title="Unpack returns HTTP 507 Insufficient Storage when required raw bytes exceed free disk space",
)
def test_t2_f3_06_insufficient_disk_headroom_returns_507(ctx: CaseContext) -> None:
    daemon = ctx.storage(name="st_t2_f3_06")
    info = daemon.client().get("/api/v1/info").json()
    free_bytes = int(info["usage"]["free_bytes"])
    impossible_raw = free_bytes + (100 << 30)  # free_bytes + 100 GiB

    tiny_arc = make_tar_zst([("tiny.txt", b"hi", None)])
    resp = daemon.client().post(
        "/api/v1/archives/unpack",
        query={"space": "datasets", "dir": "disk_full_dst", "format": "tar.zst", "mode": "atomic"},
        raw_body=tiny_arc,
        headers={
            "X-Archive-Sha256": sha256_bytes(tiny_arc),
            "X-Archive-Raw-Bytes": str(impossible_raw),
        },
    )
    assert_status(resp, 507, "insufficient disk space must return 507")
    assert not (daemon.data_dir / "datasets" / "disk_full_dst").exists()


# ==============================================================================
# F4 Tier 2: Docker, Entrypoint & Compose Negative / Security Checks — 5 Tests
# ==============================================================================


@e2e_test(
    test_id="T2_F4_01",
    tier=2,
    features="F4",
    title="storage-entrypoint.sh rejects non-numeric or injected PUID and PGID values",
)
def test_t2_f4_01_entrypoint_rejects_invalid_puid_pgid(ctx: CaseContext) -> None:
    _ = ctx
    ep_path = REPO_ROOT / "deploy" / "docker" / "storage-entrypoint.sh"
    assert ep_path.is_file()

    for bad_env in (
        {"PUID": "notanumber", "PGID": "1000"},
        {"PUID": "1000", "PGID": "1000;id"},
        {"PUID": "-5", "PGID": "1000"},
    ):
        env = dict(os.environ)
        env.update(bad_env)
        res = subprocess.run(
            ["sh", str(ep_path), "true"],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        assert res.returncode != 0, f"Expected non-zero exit for invalid {bad_env}, got rc=0"


@e2e_test(
    test_id="T2_F4_02",
    tier=2,
    features="F4",
    title="Storage shell scripts enforce strict error flags and pass bash -n syntax validation",
)
def test_t2_f4_02_storage_shell_scripts_strict_mode_and_syntax(ctx: CaseContext) -> None:
    _ = ctx
    scripts = [
        REPO_ROOT / "deploy" / "docker" / "storage-entrypoint.sh",
        REPO_ROOT / "deploy" / "scripts" / "tests" / "test-compose.sh",
        REPO_ROOT / "deploy" / "scripts" / "tests" / "test-storage-compose.sh",
    ]
    for sc in scripts:
        assert sc.is_file(), f"Expected script {sc}"
        chk = subprocess.run(["bash", "-n", str(sc)], capture_output=True, text=True, check=False)
        assert chk.returncode == 0, f"Syntax error in {sc}: {chk.stderr}"
        txt = sc.read_text(encoding="utf-8")
        assert "set -eu" in txt or "set -e" in txt, f"Expected strict error exit in {sc}"


@e2e_test(
    test_id="T2_F4_03",
    tier=2,
    features="F4",
    title="Compose manifests never mount docker.sock or enable privileged containers",
)
def test_t2_f4_03_compose_manifests_least_privilege_isolation(ctx: CaseContext) -> None:
    _ = ctx
    for fname in ("docker-compose.yml", "docker-compose.storage.yml"):
        path = REPO_ROOT / fname
        assert path.is_file(), f"Missing {path}"
        text = path.read_text(encoding="utf-8")
        assert "docker.sock" not in text, f"{fname} must not mount /var/run/docker.sock"
        assert "privileged: true" not in text, f"{fname} must not run privileged containers"


@e2e_test(
    test_id="T2_F4_04",
    tier=2,
    features="F4",
    title="Standalone docker-compose.storage.yml isolates storage service from platform DB and secret keys",
)
def test_t2_f4_04_standalone_compose_secret_isolation(ctx: CaseContext) -> None:
    _ = ctx
    text = (REPO_ROOT / "docker-compose.storage.yml").read_text(encoding="utf-8")
    assert "TM_DATABASE_URL" not in text
    assert "TM_DB_PASSWORD" not in text
    assert "TM_SECRET_KEY" not in text


@e2e_test(
    test_id="T2_F4_05",
    tier=2,
    features="F4",
    title="CI workflow builds Dockerfile.storage and runs storage compose verification",
)
def test_t2_f4_05_ci_workflow_covers_storage_image_and_tests(ctx: CaseContext) -> None:
    _ = ctx
    ci_text = (REPO_ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
    assert "Dockerfile.storage" in ci_text or "build-storage" in ci_text
    assert "test-storage-compose.sh" in ci_text or "deploy/scripts/tests/test-*.sh" in ci_text
