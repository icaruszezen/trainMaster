"""训练节点配置：全部来自 TM_* 环境变量。"""

from __future__ import annotations

import logging
import os
import re
import socket
import tempfile
import unicodedata
from collections.abc import Mapping
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import urlsplit

log = logging.getLogger(__name__)

DEFAULT_STATE_DIR = Path("/var/lib/trainmaster")
DEFAULT_DATA_DIR = Path("/data")
DEFAULT_SELF_TEST_TIMEOUT = 300.0
MAX_SELF_TEST_TIMEOUT = 86400.0
MAX_NODE_NAME = 64
MAX_TOKEN = 256

_LOG_LEVELS = {
    "debug": logging.DEBUG,
    "info": logging.INFO,
    "warn": logging.WARNING,
    "warning": logging.WARNING,
    "error": logging.ERROR,
}
_TRUE = {"1", "true", "yes", "on"}
_FALSE = {"0", "false", "no", "off"}
_DURATION = re.compile(r"(\d+(?:\.\d+)?)\s*(s|m)?")
# make setup 的训练节点模式（deploy/scripts/setup-compute.sh --mode）。
TRAINER_MODES = ("auto", "gpu", "cpu", "off")
# compose 服务名（TM_TRAINER_SERVICE）：字母、数字开头，之后可以有 . _ -。
_SERVICE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")


class ConfigError(ValueError):
    """环境变量不合法。消息（中文）列出所有问题，直接展示给用户。"""


@dataclass(frozen=True)
class Config:
    server_url: str | None = None
    node_token: str | None = None
    node_name: str = "trainer"
    state_dir: Path = DEFAULT_STATE_DIR
    data_dir: Path = DEFAULT_DATA_DIR
    torch_variant: str | None = None
    self_test: bool = True
    self_test_timeout: float = DEFAULT_SELF_TEST_TIMEOUT
    log_level: int = logging.INFO
    # 以下三项由 docker-compose.yml 传入，只用于生成修复建议中的命令：
    # 所在的 compose 服务（trainer / trainer-cpu）、make setup 保存的模式（TM_TRAINER_MODE）
    # 与固定的 PyTorch 变体（TM_TRAINER_VARIANT，没有固定时为 None）。
    trainer_service: str | None = None
    trainer_mode: str | None = None
    trainer_variant: str | None = None
    # 状态目录不可写、退回到其它目录时的说明（启动时记录 WARNING）。
    state_dir_warning: str | None = field(default=None, compare=False)

    @property
    def ultralytics_config_dir(self) -> Path:
        return self.state_dir / "ultralytics"


def _clean(env: Mapping[str, str], key: str) -> str:
    return env.get(key, "").strip()


def parse_server_url(raw: str) -> str:
    """校验平台地址并去掉末尾的 /：只接受 http(s)://主机[:端口][/路径]。"""
    try:
        parts = urlsplit(raw)
        _ = parts.port  # 端口不是数字或超出范围时抛出 ValueError
    except ValueError as exc:
        raise ValueError(f"地址 {raw!r} 不合法：{exc}") from exc
    if parts.scheme not in ("http", "https") or not parts.hostname:
        raise ValueError(f"地址 {raw!r} 不合法，应为 http://主机:端口")
    if parts.username or parts.password:
        raise ValueError("地址中不能包含用户名或密码（令牌请用 TM_NODE_TOKEN 设置）")
    if parts.query or parts.fragment:
        raise ValueError(f"地址中不能包含查询参数或 #：{raw!r}")
    return raw.rstrip("/")


def parse_token(raw: str) -> str | None:
    """节点令牌：只能是可见 ASCII（与服务端 TM_NODE_TOKEN 的规则相同），去掉首尾空白（如 Windows 换行）。"""
    token = raw.strip()
    if not token:
        return None
    if len(token) > MAX_TOKEN:
        raise ValueError(f"太长（{len(token)} 个字符，最多 {MAX_TOKEN} 个）")
    if any(not 0x21 <= ord(c) <= 0x7E for c in token):
        raise ValueError("只能包含可见的 ASCII 字符（不能有空格或中文）")
    return token


def clean_node_name(raw: str, fallback: str) -> str:
    """节点名称：去掉控制字符与首尾空白，最多 64 个字符；为空时使用 fallback（主机名）。"""
    name = "".join(c for c in raw if unicodedata.category(c)[0] != "C").strip()
    if not name:
        name = "".join(c for c in fallback if unicodedata.category(c)[0] != "C").strip()
    return (name or "trainer")[:MAX_NODE_NAME]


def parse_bool(raw: str) -> bool:
    value = raw.strip().lower()
    if value in _TRUE:
        return True
    if value in _FALSE:
        return False
    raise ValueError(f"{raw!r} 不是布尔值（请用 true 或 false）")


def parse_timeout(raw: str) -> float:
    """自检超时：秒数，可带 s / m 后缀（300、300s、5m）。"""
    m = _DURATION.fullmatch(raw.strip().lower())
    if not m:
        raise ValueError(f"{raw!r} 不是合法的时长（秒数，例如 300 或 5m）")
    seconds = float(m.group(1)) * (60 if m.group(2) == "m" else 1)
    if not 0 < seconds <= MAX_SELF_TEST_TIMEOUT:
        raise ValueError(f"{raw!r} 超出范围（应大于 0 且不超过 24 小时）")
    return seconds


def parse_service(raw: str) -> str:
    name = raw.strip()
    if not _SERVICE.fullmatch(name):
        raise ValueError(f"{raw!r} 不是合法的 compose 服务名（如 trainer、trainer-cpu）")
    return name


def parse_mode(raw: str) -> str:
    mode = raw.strip().lower()
    if mode not in TRAINER_MODES:
        raise ValueError(f"未知的模式 {raw!r}（{'、'.join(TRAINER_MODES)}）")
    return mode


def parse_log_level(raw: str) -> int:
    level = _LOG_LEVELS.get(raw.strip().lower())
    if level is None:
        raise ValueError(f"未知的日志级别 {raw!r}（debug、info、warn 或 error）")
    return level


def _writable_dir(path: Path) -> bool:
    """目录存在（必要时创建）且当前用户能在其中创建文件。"""
    try:
        path.mkdir(parents=True, exist_ok=True)
        with tempfile.NamedTemporaryFile(dir=path, prefix=".write-test-"):
            pass
    except OSError:
        return False
    return True


def state_dir_candidates(configured: Path, home: str | None = None) -> list[Path]:
    """状态目录的候选：配置值 → ~/.local/state/trainmaster → 临时目录。healthcheck 也按这个顺序查找心跳文件。"""
    candidates = [configured]
    home = home or os.path.expanduser("~")
    if home and home != "~":
        candidates.append(Path(home) / ".local" / "state" / "trainmaster")
    uid = os.getuid() if hasattr(os, "getuid") else "user"
    candidates.append(Path(tempfile.gettempdir()) / f"trainmaster-{uid}")
    out: list[Path] = []
    for c in candidates:
        if c not in out:
            out.append(c)
    return out


def resolve_state_dir(
    configured: Path, *, home: str | None = None, create: bool = True
) -> tuple[Path, str | None]:
    """选择第一个可写的状态目录，返回 (目录, 退回时的说明)。create=False 时只返回配置值。"""
    if not create:
        return configured, None
    candidates = state_dir_candidates(configured, home)
    for path in candidates:
        if _writable_dir(path):
            if path == configured:
                return path, None
            return path, (
                f"state directory {configured} is not writable, using {path} instead; "
                "the node key will not survive a container rebuild"
            )
    # 都不可写时仍返回配置值：node-key 等写入失败会各自记录日志，节点仍能运行。
    return configured, f"no writable state directory (tried {', '.join(map(str, candidates))})"


def load(
    env: Mapping[str, str] | None = None,
    *,
    require_server: bool = False,
    create_state_dir: bool = True,
) -> Config:
    """从环境变量读取配置。require_server：run 子命令要求 TM_SERVER_URL。"""
    env = os.environ if env is None else env
    problems: list[str] = []

    def fail(key: str, err: object) -> None:
        problems.append(f"{key}：{err}")

    server_url: str | None = None
    raw_url = _clean(env, "TM_SERVER_URL")
    if raw_url:
        try:
            server_url = parse_server_url(raw_url)
        except ValueError as exc:
            fail("TM_SERVER_URL", exc)
    elif require_server:
        fail("TM_SERVER_URL", "必须设置（平台地址，例如 http://192.168.1.10:8080）")

    token: str | None = None
    try:
        token = parse_token(env.get("TM_NODE_TOKEN", ""))
    except ValueError as exc:
        fail("TM_NODE_TOKEN", exc)

    try:
        hostname = socket.gethostname()
    except OSError:
        hostname = ""
    node_name = clean_node_name(env.get("TM_NODE_NAME", ""), hostname)

    self_test = True
    if _clean(env, "TM_SELF_TEST"):
        try:
            self_test = parse_bool(env["TM_SELF_TEST"])
        except ValueError as exc:
            fail("TM_SELF_TEST", exc)

    timeout = DEFAULT_SELF_TEST_TIMEOUT
    if _clean(env, "TM_SELF_TEST_TIMEOUT"):
        try:
            timeout = parse_timeout(env["TM_SELF_TEST_TIMEOUT"])
        except ValueError as exc:
            fail("TM_SELF_TEST_TIMEOUT", exc)

    service: str | None = None
    if _clean(env, "TM_TRAINER_SERVICE"):
        try:
            service = parse_service(env["TM_TRAINER_SERVICE"])
        except ValueError as exc:
            fail("TM_TRAINER_SERVICE", exc)

    mode: str | None = None
    if _clean(env, "TM_TRAINER_MODE"):
        try:
            mode = parse_mode(env["TM_TRAINER_MODE"])
        except ValueError as exc:
            fail("TM_TRAINER_MODE", exc)

    # make setup 把无效的值按自动选择处理，这里同样不报错；auto 表示没有固定。
    pinned = _clean(env, "TM_TRAINER_VARIANT").lower()

    level = logging.INFO
    if _clean(env, "TM_LOG_LEVEL"):
        try:
            level = parse_log_level(env["TM_LOG_LEVEL"])
        except ValueError as exc:
            fail("TM_LOG_LEVEL", exc)

    if problems:
        raise ConfigError("；".join(problems))

    configured_state = Path(_clean(env, "TM_STATE_DIR") or DEFAULT_STATE_DIR)
    state_dir, warning = resolve_state_dir(
        configured_state, home=env.get("HOME"), create=create_state_dir
    )

    data_dir = Path(_clean(env, "TM_DATA_DIR") or DEFAULT_DATA_DIR)
    if not data_dir.is_dir():
        data_dir = state_dir

    return Config(
        server_url=server_url,
        node_token=token,
        node_name=node_name,
        state_dir=state_dir,
        data_dir=data_dir,
        torch_variant=_clean(env, "TM_TORCH_VARIANT") or None,
        self_test=self_test,
        self_test_timeout=timeout,
        log_level=level,
        trainer_service=service,
        trainer_mode=mode,
        trainer_variant=pinned if pinned and pinned != "auto" else None,
        state_dir_warning=warning,
    )
