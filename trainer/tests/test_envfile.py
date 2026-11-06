"""envfile.py：trainer.env 的解析（与 install.sh、install.ps1 写入的格式相同）。"""

from __future__ import annotations

import shutil
import subprocess
from pathlib import Path

import pytest
from fakes import TRAINER_DIR, posix_only

from trainmaster_trainer import envfile
from trainmaster_trainer.envfile import EnvFileError


def test_parse_basic_forms():
    text = (
        "\ufeff# trainMaster 训练节点配置\r\n"
        "\r\n"
        "TM_SERVER_URL='http://192.168.1.10:8080'\r\n"
        'export TM_NODE_NAME="显卡机 1"\r\n'
        "TM_STATE_DIR=C:\\ProgramData\\trainMaster\\state\r\n"
        "  TM_DATA_DIR = 'D:\\data'   # 训练数据\r\n"
        "EMPTY=\r\n"
        "QUOTED_EMPTY=''\r\n"
        "CUDA_VISIBLE_DEVICES=0,1\r\n"
        "TM_SERVER_URL='http://10.0.0.2:8080'\r\n"
    )
    assert envfile.parse(text) == {
        "TM_SERVER_URL": "http://10.0.0.2:8080",  # 最后一次为准
        "TM_NODE_NAME": "显卡机 1",
        "TM_STATE_DIR": "C:\\ProgramData\\trainMaster\\state",
        "TM_DATA_DIR": "D:\\data",
        "EMPTY": "",
        "QUOTED_EMPTY": "",
        "CUDA_VISIBLE_DEVICES": "0,1",
    }


@pytest.mark.parametrize(
    ("line", "value"),
    [
        ('A="a\\"b"', 'a"b'),
        ('A="a\\\\b"', "a\\b"),
        ('A="\\$HOME and \\`x\\`"', "$HOME and `x`"),
        ('A="C:\\ProgramData\\x"', "C:\\ProgramData\\x"),  # 其它反斜杠原样保留
        ("A='it''s'", None),  # 单引号中不能有单引号
        ("A='a\"b'", 'a"b'),
        ("A='a\\b'", "a\\b"),
        ('A="x" # 注释', "x"),
        ("A=x # 不加引号时 # 是值的一部分", "x # 不加引号时 # 是值的一部分"),
        ("A=a=b", "a=b"),
        ("export\tA=1", "1"),
    ],
)
def test_parse_values(line, value):
    if value is None:
        with pytest.raises(EnvFileError):
            envfile.parse(line)
    else:
        assert envfile.parse(line) == {"A": value}


@pytest.mark.parametrize(
    ("text", "part"),
    [
        ("NOT A LINE", "第 1 行不是 KEY=VALUE 的格式"),
        ("1A=x", "第 1 行不是 KEY=VALUE 的格式"),
        ("A-B=x", "第 1 行不是 KEY=VALUE 的格式"),
        ("A='x", "第 1 行（A）：缺少结尾的引号"),
        ('A="x" y', "第 1 行（A）：引号后面还有内容"),
        ("# ok\nA=1\nB='\nC", "第 3 行（B）：缺少结尾的引号；第 4 行不是 KEY=VALUE 的格式"),
    ],
)
def test_parse_errors(text, part):
    with pytest.raises(EnvFileError) as info:
        envfile.parse(text, "配置文件 x.env")
    assert str(info.value).startswith("配置文件 x.env 格式不正确：")
    assert part in str(info.value)


def test_load(tmp_path: Path):
    path = tmp_path / "trainer.env"
    path.write_bytes("\ufeffTM_NODE_NAME='节点'\r\n".encode())
    assert envfile.load(path) == {"TM_NODE_NAME": "节点"}


def test_load_errors(tmp_path: Path):
    with pytest.raises(EnvFileError, match="不存在"):
        envfile.load(tmp_path / "missing.env")
    with pytest.raises(EnvFileError, match="无法读取"):
        envfile.load(tmp_path)  # 目录
    bad = tmp_path / "gbk.env"
    bad.write_bytes("TM_NODE_NAME='节点'\n".encode("gbk"))
    with pytest.raises(EnvFileError, match="不是 UTF-8 编码"):
        envfile.load(bad)
    big = tmp_path / "big.env"
    big.write_bytes(b"#" * (envfile.MAX_BYTES + 1))
    with pytest.raises(EnvFileError, match="太大"):
        envfile.load(big)


def test_apply_overrides_existing_values():
    environ = {"TM_NODE_NAME": "old", "PATH": "/bin"}
    envfile.apply({"TM_NODE_NAME": "new", "CUDA_VISIBLE_DEVICES": "1"}, environ)
    assert environ == {"TM_NODE_NAME": "new", "PATH": "/bin", "CUDA_VISIBLE_DEVICES": "1"}


# install.sh 只在 Linux 上运行；Windows 上找到的 bash 可能是 WSL 的启动器，Git Bash 解析命令行参数的规则也不同
@posix_only
@pytest.mark.skipif(shutil.which("bash") is None, reason="bash is not available")
def test_reads_what_install_sh_writes():
    # install.sh 的 env_quote（双引号，转义 \ " $ `）写出的值能原样读回。
    values = ["plain", 'a"b', "a\\b", "$HOME", "`x`", "C:\\dir\\sub", "空 格", "it's"]
    script = (
        "env_quote() {\n"
        + _extract_function(TRAINER_DIR / "scripts" / "install.sh", "env_quote")
        + "\n}\n"
        + 'i=0; for v in "$@"; do printf "K%d=%s\\n" "$i" "$(env_quote "$v")"; i=$((i+1)); done\n'
    )
    out = subprocess.run(
        ["bash", "-c", script, "bash", *values],
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=True,
        timeout=60,
    ).stdout
    assert envfile.parse(out) == {f"K{i}": v for i, v in enumerate(values)}


def _extract_function(path: Path, name: str) -> str:
    """取出 shell 脚本中一个函数的函数体（install.sh 中的函数以单独一行的 } 结束）。"""
    lines = path.read_text(encoding="utf-8").splitlines()
    start = lines.index(f"{name}() {{")
    end = lines.index("}", start)
    return "\n".join(lines[start + 1 : end])
