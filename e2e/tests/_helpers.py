"""Shared opaque-box E2E assertion and storageagent request helpers."""

from __future__ import annotations

from pathlib import Path
import re
from typing import Any

from e2e.harness.cluster import DEPLOY_DIR, FRONTEND_DIR, REPO_ROOT, StorageDaemon
from e2e.harness.http_client import HttpResponse

HEX64_RE = re.compile(r"^[0-9a-f]{64}$")


def assert_hex64(value: Any, label: str = "sha256") -> str:
    assert isinstance(value, str) and HEX64_RE.fullmatch(value), (
        f"Expected lowercase 64-hex {label}, got {value!r}"
    )
    return value


def assert_status(resp: HttpResponse, expected: int | tuple[int, ...], context: str = "") -> None:
    allowed = (expected,) if isinstance(expected, int) else expected
    assert resp.status in allowed, (
        f"{context + ': ' if context else ''}expected HTTP {allowed}, got {resp.status}: {resp.text[:500]}"
    )


def storage_put_file(
    daemon: StorageDaemon,
    space: str,
    dir_name: str,
    rel_path: str,
    content: bytes,
    *,
    exclusive: bool = False,
) -> HttpResponse:
    cli = daemon.client()
    q1: dict[str, Any] = {"dir": dir_name, "path": rel_path}
    if exclusive:
        q1["exclusive"] = "true"
    return cli.put(f"/api/v1/spaces/{space}/files", query=q1, raw_body=content)


def storage_get_file(
    daemon: StorageDaemon,
    space: str,
    dir_name: str,
    rel_path: str,
    *,
    headers: dict[str, str] | None = None,
) -> HttpResponse:
    cli = daemon.client()
    return cli.get(
        f"/api/v1/spaces/{space}/files",
        query={"dir": dir_name, "path": rel_path},
        headers=headers,
    )


def storage_head_file(
    daemon: StorageDaemon,
    space: str,
    dir_name: str,
    rel_path: str,
    *,
    lstat: bool = False,
) -> HttpResponse:
    cli = daemon.client()
    q: dict[str, Any] = {"dir": dir_name, "path": rel_path}
    if lstat:
        q["lstat"] = "true"
    return cli.head(f"/api/v1/spaces/{space}/files", query=q)


def storage_delete_file(daemon: StorageDaemon, space: str, dir_name: str, rel_path: str) -> HttpResponse:
    cli = daemon.client()
    return cli.delete(f"/api/v1/spaces/{space}/files", query={"dir": dir_name, "path": rel_path})


def storage_mkdir(
    daemon: StorageDaemon,
    space: str,
    dir_name: str,
    *,
    exclusive: bool = True,
) -> HttpResponse:
    cli = daemon.client()
    return cli.post(
        f"/api/v1/spaces/{space}/dirs",
        query={"dir": dir_name, "exclusive": "true" if exclusive else "false"},
        json_body={"dir": dir_name, "path": dir_name, "exclusive": exclusive},
    )


def storage_rename_dir(daemon: StorageDaemon, space: str, src_dir: str, dst_dir: str) -> HttpResponse:
    cli = daemon.client()
    return cli.patch(
        f"/api/v1/spaces/{space}/dirs",
        query={"from": src_dir, "to": dst_dir},
        json_body={"from": src_dir, "to": dst_dir, "src": src_dir, "dst": dst_dir},
    )


def storage_delete_dir(
    daemon: StorageDaemon,
    space: str,
    dir_name: str,
    *,
    recursive: bool = True,
) -> HttpResponse:
    cli = daemon.client()
    return cli.delete(
        f"/api/v1/spaces/{space}/dirs",
        query={"dir": dir_name, "recursive": "true" if recursive else "false"},
    )


def read_all_web_bundle_text(bundle_dir: Path) -> str:
    """Concatenate index.html and all JS/CSS assets from built frontend bundle for UI contract inspection."""
    parts: list[str] = []
    for p in sorted(bundle_dir.rglob("*")):
        if p.is_file() and p.suffix.lower() in (".html", ".js", ".mjs", ".css"):
            parts.append(p.read_text(encoding="utf-8", errors="replace"))
    return "\n".join(parts)


__all__ = [
    "DEPLOY_DIR",
    "FRONTEND_DIR",
    "HEX64_RE",
    "REPO_ROOT",
    "assert_hex64",
    "assert_status",
    "read_all_web_bundle_text",
    "storage_delete_dir",
    "storage_delete_file",
    "storage_get_file",
    "storage_head_file",
    "storage_mkdir",
    "storage_put_file",
    "storage_rename_dir",
]
