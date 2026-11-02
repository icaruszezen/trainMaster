"""节点标识（node_key）：首次启动时生成 uuid4 并保存在 STATE_DIR/node-key，之后一直沿用。

平台按 node_key 识别节点：容器重建、改名、换 IP 后仍是同一个节点。删除该文件后节点会以新节点注册。
"""

from __future__ import annotations

import contextlib
import logging
import os
import re
import tempfile
import uuid
from pathlib import Path

log = logging.getLogger(__name__)

FILENAME = "node-key"
# 与服务端的校验规则相同：1–64 个字符，只能是 [A-Za-z0-9._-]。
_VALID = re.compile(r"[A-Za-z0-9._-]{1,64}")


def is_valid(key: str) -> bool:
    return bool(_VALID.fullmatch(key))


def atomic_write_text(path: Path, text: str, mode: int = 0o644) -> None:
    """写入临时文件后 rename，读取方不会看到写了一半的文件；断电后也不会留下空文件。"""
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp)
        raise


def load_or_create(state_dir: Path) -> str:
    """读取 STATE_DIR/node-key；不存在或内容不合法时生成新的并保存。

    保存失败时仍返回新生成的标识（本次运行可用），并记录 WARNING：重启后会以新节点注册。
    """
    path = state_dir / FILENAME
    try:
        existing = path.read_text(encoding="utf-8").strip()
    except FileNotFoundError:
        existing = None
    except OSError as exc:
        log.warning("cannot read node key file %s: %s", path, exc)
        existing = None
    if existing is not None:
        if is_valid(existing):
            return existing
        log.warning("node key file %s is invalid, generating a new key", path)

    key = str(uuid.uuid4())
    try:
        state_dir.mkdir(parents=True, exist_ok=True)
        atomic_write_text(path, key + "\n")
    except OSError as exc:
        log.warning(
            "cannot save node key to %s (%s); the node will register as a new node after a restart",
            path,
            exc,
        )
    else:
        log.info("generated a new node key in %s", path)
    return key


def read(state_dir: Path) -> str | None:
    """只读取已有的标识（probe 子命令用，不生成新文件）。"""
    try:
        key = (state_dir / FILENAME).read_text(encoding="utf-8").strip()
    except OSError:
        return None
    return key if is_valid(key) else None
