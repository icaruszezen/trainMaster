"""node_key.py：生成、沿用、修复与只读读取节点标识。"""

from __future__ import annotations

import logging
import os
import stat
import uuid

import pytest

from trainmaster_trainer import node_key


def test_generated_once_and_reused(tmp_path, caplog):
    caplog.set_level(logging.INFO)
    state = tmp_path / "state"
    key = node_key.load_or_create(state)
    uuid.UUID(key)
    path = state / "node-key"
    assert path.read_text() == key + "\n"
    assert stat.S_IMODE(path.stat().st_mode) == 0o644
    assert node_key.load_or_create(state) == key
    assert node_key.read(state) == key
    assert sum("generated a new node key" in r.getMessage() for r in caplog.records) == 1
    assert not [p for p in state.iterdir() if p.name.startswith(".")]  # 没有残留的临时文件


def test_existing_key_with_whitespace_is_kept(tmp_path):
    (tmp_path / "node-key").write_bytes(b"  legacy-key_01.A \r\n")
    assert node_key.load_or_create(tmp_path) == "legacy-key_01.A"
    assert (tmp_path / "node-key").read_bytes() == b"  legacy-key_01.A \r\n"  # 不改写已有文件


@pytest.mark.parametrize("content", ["", "bad key", "x" * 65, "键", "a/b"])
def test_invalid_key_is_replaced(tmp_path, caplog, content):
    (tmp_path / "node-key").write_text(content)
    assert node_key.read(tmp_path) is None
    key = node_key.load_or_create(tmp_path)
    assert node_key.is_valid(key) and key != content.strip()
    assert (tmp_path / "node-key").read_text().strip() == key
    if content.strip():
        assert any("is invalid" in r.getMessage() for r in caplog.records)


def test_unwritable_state_dir_still_returns_key(tmp_path, caplog):
    blocker = tmp_path / "file"
    blocker.write_text("x")
    key = node_key.load_or_create(blocker / "state")
    assert node_key.is_valid(key)
    assert any("cannot save node key" in r.getMessage() for r in caplog.records)


def test_unreadable_key_file(tmp_path, caplog):
    (tmp_path / "node-key").mkdir()  # 读取时报 IsADirectoryError
    key = node_key.load_or_create(tmp_path)
    assert node_key.is_valid(key)
    assert any("cannot read node key file" in r.getMessage() for r in caplog.records)
    assert node_key.read(tmp_path) is None


def test_read_missing(tmp_path):
    assert node_key.read(tmp_path) is None
    assert not (tmp_path / "node-key").exists()


def test_is_valid():
    assert node_key.is_valid("a")
    assert node_key.is_valid("A-z_0.9" * 9 + "x")  # 64 个字符
    assert not node_key.is_valid("")
    assert not node_key.is_valid("x" * 65)
    assert not node_key.is_valid("a b")
    assert not node_key.is_valid("a\n")


def test_atomic_write_cleans_up_on_failure(tmp_path, monkeypatch):
    target = tmp_path / "file"
    target.write_text("old")

    def boom(src, dst):
        raise OSError("disk full")

    monkeypatch.setattr(os, "replace", boom)
    with pytest.raises(OSError):
        node_key.atomic_write_text(target, "new")
    assert target.read_text() == "old"
    assert sorted(p.name for p in tmp_path.iterdir()) == ["file"]


def test_atomic_write_mode(tmp_path):
    target = tmp_path / "secret"
    node_key.atomic_write_text(target, "x", mode=0o600)
    assert stat.S_IMODE(target.stat().st_mode) == 0o600
