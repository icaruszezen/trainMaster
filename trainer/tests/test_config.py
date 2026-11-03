"""config.py：TM_* 环境变量的解析、校验与状态目录的退回。"""

from __future__ import annotations

import logging
import os
import socket
from pathlib import Path

import pytest

from trainmaster_trainer import config
from trainmaster_trainer.config import Config, ConfigError


def base_env(tmp_path: Path, **extra: str) -> dict[str, str]:
    env = {
        "TM_STATE_DIR": str(tmp_path / "state"),
        "TM_DATA_DIR": str(tmp_path),
        "HOME": str(tmp_path),
    }
    env.update(extra)
    return env


def test_defaults(tmp_path):
    cfg = config.load(base_env(tmp_path))
    assert cfg.server_url is None and cfg.node_token is None
    assert cfg.state_dir == tmp_path / "state" and cfg.state_dir.is_dir()
    assert cfg.data_dir == tmp_path
    assert cfg.self_test is True
    assert cfg.self_test_timeout == 300.0
    assert cfg.export_timeout == 900.0
    assert cfg.log_level == logging.INFO
    assert cfg.torch_variant is None
    assert cfg.state_dir_warning is None
    assert cfg.node_name == (socket.gethostname() or "trainer")[:64]
    assert cfg.ultralytics_config_dir == tmp_path / "state" / "ultralytics"


def test_full_configuration(tmp_path):
    cfg = config.load(
        base_env(
            tmp_path,
            TM_SERVER_URL=" https://tm.example.com:8443/base/ ",
            TM_NODE_TOKEN="tok-0123456789abcdef\r\n",
            TM_NODE_NAME="  机房A-4090 \t",
            TM_TORCH_VARIANT="cu128",
            TM_SELF_TEST="off",
            TM_SELF_TEST_TIMEOUT="5m",
            TM_EXPORT_TIMEOUT=" 20m ",
            TM_LOG_LEVEL="DEBUG",
        ),
        require_server=True,
    )
    assert cfg.server_url == "https://tm.example.com:8443/base"
    assert cfg.node_token == "tok-0123456789abcdef"
    assert cfg.node_name == "机房A-4090"
    assert cfg.torch_variant == "cu128"
    assert cfg.self_test is False
    assert cfg.self_test_timeout == 300.0
    assert cfg.export_timeout == 1200.0
    assert cfg.log_level == logging.DEBUG


def test_compose_deployment_settings(tmp_path):
    cfg = config.load(base_env(tmp_path))
    assert cfg.trainer_service is None and cfg.trainer_mode is None
    assert cfg.trainer_variant is None
    cfg = config.load(
        base_env(
            tmp_path,
            TM_TRAINER_SERVICE=" trainer-cpu ",
            TM_TRAINER_MODE=" cpu ",
            TM_TRAINER_VARIANT=" cu126 ",
        )
    )
    assert cfg.trainer_service == "trainer-cpu"
    assert cfg.trainer_mode == "cpu"
    assert cfg.trainer_variant == "cu126"
    # auto 表示没有固定变体
    assert config.load(base_env(tmp_path, TM_TRAINER_VARIANT="auto")).trainer_variant is None


def test_invalid_compose_deployment_settings(tmp_path):
    with pytest.raises(ConfigError) as info:
        config.load(base_env(tmp_path, TM_TRAINER_SERVICE="train er"))
    message = str(info.value)
    assert "TM_TRAINER_SERVICE" in message and "compose 服务名" in message


@pytest.mark.parametrize("value", ["fast", "CPU", "Auto"])
def test_invalid_trainer_mode_is_auto_like_make_setup(tmp_path, value):
    # make setup 只认 auto / gpu / cpu / off（区分大小写），其它值警告后按 auto 处理：
    # 训练节点同样按 auto 处理，不因为只用于修复建议的设置而无法启动。
    assert config.load(base_env(tmp_path, TM_TRAINER_MODE=value)).trainer_mode is None
    assert config.load(base_env(tmp_path, TM_TRAINER_MODE="off")).trainer_mode == "off"


def test_server_url_required_for_run(tmp_path):
    with pytest.raises(ConfigError) as info:
        config.load(base_env(tmp_path), require_server=True)
    assert "TM_SERVER_URL" in str(info.value) and "必须设置" in str(info.value)


def test_all_problems_reported_together(tmp_path):
    env = base_env(
        tmp_path,
        TM_SERVER_URL="ftp://host",
        TM_NODE_TOKEN="有 中文",
        TM_SELF_TEST="maybe",
        TM_SELF_TEST_TIMEOUT="soon",
        TM_EXPORT_TIMEOUT="25h",
        TM_LOG_LEVEL="loud",
    )
    with pytest.raises(ConfigError) as info:
        config.load(env)
    text = str(info.value)
    for key in (
        "TM_SERVER_URL",
        "TM_NODE_TOKEN",
        "TM_SELF_TEST：",
        "TM_SELF_TEST_TIMEOUT",
        "TM_EXPORT_TIMEOUT",
        "TM_LOG_LEVEL",
    ):
        assert key in text
    assert text.count("；") == 5


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        ("http://192.168.1.10:8080", "http://192.168.1.10:8080"),
        ("http://192.168.1.10:8080/", "http://192.168.1.10:8080"),
        ("https://tm.local/", "https://tm.local"),
        ("http://[::1]:8080", "http://[::1]:8080"),
        ("http://host/prefix//", "http://host/prefix"),
    ],
)
def test_parse_server_url_valid(raw, expected):
    assert config.parse_server_url(raw) == expected


@pytest.mark.parametrize(
    "raw",
    [
        "192.168.1.10:8080",
        "ftp://host",
        "http://",
        "http://host:99999",
        "http://host:port",
        "http://user:pass@host",
        "http://host/?a=1",
        "http://host/#x",
    ],
)
def test_parse_server_url_invalid(raw):
    with pytest.raises(ValueError):
        config.parse_server_url(raw)


def test_parse_token():
    assert config.parse_token("") is None
    assert config.parse_token("   ") is None
    assert config.parse_token(" abc~!@#$%^&*()_+ \n") == "abc~!@#$%^&*()_+"
    assert config.parse_token("x" * 256) == "x" * 256
    for bad in ("x" * 257, "with space", "令牌", "tab\tinside"):
        with pytest.raises(ValueError):
            config.parse_token(bad)


def test_clean_node_name():
    assert config.clean_node_name("a\x00b\x1bc​d", "host") == "abcd"
    assert config.clean_node_name("   ", "host-1") == "host-1"
    assert config.clean_node_name("", "") == "trainer"
    assert config.clean_node_name("名" * 100, "h") == "名" * 64


@pytest.mark.parametrize(
    ("raw", "value"),
    [
        ("1", True), ("TRUE", True), (" yes ", True), ("on", True),
        ("0", False), ("False", False), ("no", False), ("off", False),
    ],
)  # fmt: skip
def test_parse_bool(raw, value):
    assert config.parse_bool(raw) is value


def test_parse_bool_invalid():
    with pytest.raises(ValueError):
        config.parse_bool("2")


@pytest.mark.parametrize(
    ("raw", "seconds"),
    [("300", 300.0), ("300s", 300.0), ("5m", 300.0), ("1.5m", 90.0), (" 10 S ", 10.0), ("86400", 86400.0)],
)  # fmt: skip
def test_parse_timeout(raw, seconds):
    assert config.parse_timeout(raw) == seconds


@pytest.mark.parametrize("raw", ["0", "0m", "-1", "86401", "1h", "abc", "", "5 minutes"])
def test_parse_timeout_invalid(raw):
    with pytest.raises(ValueError):
        config.parse_timeout(raw)


@pytest.mark.parametrize(("raw", "seconds"), [("900", 900.0), ("30m", 1800.0), ("1.5", 1.5)])
def test_export_timeout(tmp_path, raw, seconds):
    # 与 TM_SELF_TEST_TIMEOUT 的规则相同：秒数，可带 s / m 后缀，不超过 24 小时
    assert config.load(base_env(tmp_path, TM_EXPORT_TIMEOUT=raw)).export_timeout == seconds


@pytest.mark.parametrize("raw", ["0", "86401", "1h", "never"])
def test_export_timeout_invalid(tmp_path, raw):
    with pytest.raises(ConfigError) as info:
        config.load(base_env(tmp_path, TM_EXPORT_TIMEOUT=raw))
    assert str(info.value).startswith("TM_EXPORT_TIMEOUT：")


def test_parse_log_level():
    assert config.parse_log_level("warn") == logging.WARNING
    assert config.parse_log_level("Warning") == logging.WARNING
    assert config.parse_log_level("error") == logging.ERROR
    with pytest.raises(ValueError):
        config.parse_log_level("trace")


def test_missing_data_dir_falls_back_to_state_dir(tmp_path):
    cfg = config.load(base_env(tmp_path, TM_DATA_DIR=str(tmp_path / "missing")))
    assert cfg.data_dir == cfg.state_dir


def test_state_dir_not_created_when_asked(tmp_path):
    cfg = config.load(base_env(tmp_path), create_state_dir=False)
    assert cfg.state_dir == tmp_path / "state"
    assert not cfg.state_dir.exists()


def test_state_dir_candidates(tmp_path):
    configured = tmp_path / "configured"
    candidates = config.state_dir_candidates(configured, home=str(tmp_path / "home"))
    assert candidates[0] == configured
    assert candidates[1] == tmp_path / "home" / ".local" / "state" / "trainmaster"
    assert candidates[2].name == f"trainmaster-{os.getuid()}"
    assert len(candidates) == 3
    # 配置值与退回目录相同时不重复
    assert len(config.state_dir_candidates(candidates[1], home=str(tmp_path / "home"))) == 2


def test_healthcheck_dirs_include_puid_temp_dir(tmp_path, monkeypatch):
    # HEALTHCHECK 以 root 运行，训练节点以 PUID 运行：也查找 PUID 的临时目录。
    monkeypatch.setattr(config.tempfile, "gettempdir", lambda: str(tmp_path / "tmp"))
    configured = tmp_path / "configured"
    dirs = config.healthcheck_dirs(configured, {"HOME": str(tmp_path / "home"), "PUID": "1000"})
    assert dirs[:2] == config.state_dir_candidates(configured, home=str(tmp_path / "home"))[:2]
    assert dirs[-1] == tmp_path / "tmp" / "trainmaster-1000"
    assert tmp_path / "tmp" / f"trainmaster-{os.getuid()}" in dirs
    # 没有 PUID、PUID 不是数字或与当前用户相同时不重复添加
    for env in ({}, {"PUID": "abc"}, {"PUID": str(os.getuid())}):
        assert config.healthcheck_dirs(configured, env) == config.state_dir_candidates(configured)


def test_unwritable_state_dir_falls_back_to_home(tmp_path):
    blocker = tmp_path / "file"
    blocker.write_text("not a directory")
    path, warning = config.resolve_state_dir(blocker / "state", home=str(tmp_path / "home"))
    assert path == tmp_path / "home" / ".local" / "state" / "trainmaster"
    assert path.is_dir()
    assert warning is not None and "not writable" in warning


def test_no_writable_state_dir(tmp_path, monkeypatch):
    monkeypatch.setattr(config, "_writable_dir", lambda path: False)
    path, warning = config.resolve_state_dir(tmp_path / "s", home=str(tmp_path))
    assert path == tmp_path / "s"
    assert warning is not None and "no writable state directory" in warning


def test_load_reports_state_dir_fallback(tmp_path):
    blocker = tmp_path / "file"
    blocker.write_text("x")
    cfg = config.load(base_env(tmp_path, TM_STATE_DIR=str(blocker)))
    assert cfg.state_dir != blocker
    assert cfg.state_dir_warning


def test_config_is_frozen(tmp_path):
    cfg = Config(state_dir=tmp_path)
    with pytest.raises(AttributeError):
        cfg.node_name = "x"  # type: ignore[misc]
