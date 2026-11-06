r"""训练节点的配置文件（trainer.env）：每行一个 KEY=VALUE。

Linux 的裸机安装（scripts/install.sh）由 systemd 的 EnvironmentFile 读取它；Windows 的计划任务没有这样的
机制，由训练节点自己读取（scripts/install.ps1 注册的计划任务运行
``python -m trainmaster_trainer run --env-file C:\ProgramData\trainMaster\trainer\trainer.env``）。
文件中的设置覆盖进程已有的同名环境变量（与 systemd 相同：配置文件就是这个服务的配置），子进程也继承它们
（如 CUDA_VISIBLE_DEVICES）。

格式与 install.sh 写入的相同（也是 systemd EnvironmentFile 与 POSIX shell 共同接受的子集）：
- 空行和 # 开头的行是注释；行首可以有 ``export``；文件可以带 UTF-8 BOM，换行可以是 CRLF；
- KEY 为字母、数字与下划线（不以数字开头）；
- 值用单引号括起来时原样使用；用双引号括起来时反斜杠只转义其后的反斜杠、双引号、$ 与反引号，其它反斜杠
  原样保留；不加引号时去掉首尾空白后原样使用（Windows 路径中的反斜杠不需要转义）。
- 引号后面只能有空白或 # 注释。不支持跨行的值。
"""

from __future__ import annotations

import os
import re
from collections.abc import MutableMapping
from pathlib import Path

_KEY = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
_ESCAPABLE = '\\"$`'
MAX_BYTES = 1 << 20


class EnvFileError(ValueError):
    """配置文件无法读取或格式不正确。消息（中文）直接展示给用户。"""


def _parse_value(raw: str) -> str:
    """一行中 = 之后的部分 → 值；格式不对时抛出 ValueError（中文说明）。"""
    text = raw.strip()
    if not text or text[0] not in "'\"":
        return text
    quote = text[0]
    out: list[str] = []
    i = 1
    while i < len(text):
        c = text[i]
        if c == quote:
            rest = text[i + 1 :].strip()
            if rest and not rest.startswith("#"):
                raise ValueError(f"引号后面还有内容：{rest[:40]!r}")
            return "".join(out)
        if quote == '"' and c == "\\" and i + 1 < len(text) and text[i + 1] in _ESCAPABLE:
            out.append(text[i + 1])
            i += 2
            continue
        out.append(c)
        i += 1
    raise ValueError("缺少结尾的引号")


def parse(text: str, source: str = "配置文件") -> dict[str, str]:
    """解析配置文件的内容；同一个 KEY 出现多次时以最后一次为准。"""
    values: dict[str, str] = {}
    problems: list[str] = []
    for lineno, line in enumerate(text.lstrip("\ufeff").splitlines(), start=1):
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped.startswith(("export ", "export\t")):
            stripped = stripped[len("export") :].lstrip()
        key, sep, value = stripped.partition("=")
        key = key.strip()
        if not sep or not _KEY.fullmatch(key):
            problems.append(f"第 {lineno} 行不是 KEY=VALUE 的格式")
            continue
        try:
            values[key] = _parse_value(value)
        except ValueError as exc:
            problems.append(f"第 {lineno} 行（{key}）：{exc}")
    if problems:
        raise EnvFileError(f"{source} 格式不正确：" + "；".join(problems))
    return values


def load(path: str | Path) -> dict[str, str]:
    """读取并解析配置文件（UTF-8）。"""
    path = Path(path)
    try:
        with path.open("rb") as f:
            data = f.read(MAX_BYTES + 1)
    except FileNotFoundError:
        raise EnvFileError(f"配置文件 {path} 不存在") from None
    except OSError as exc:
        raise EnvFileError(f"无法读取配置文件 {path}：{exc}") from None
    if len(data) > MAX_BYTES:
        raise EnvFileError(f"配置文件 {path} 太大（超过 1 MiB）")
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        raise EnvFileError(f"配置文件 {path} 不是 UTF-8 编码") from None
    return parse(text, f"配置文件 {path}")


def apply(values: dict[str, str], environ: MutableMapping[str, str] | None = None) -> None:
    """把配置写入环境变量（覆盖同名的已有值）。"""
    target = os.environ if environ is None else environ
    for key, value in values.items():
        target[key] = value
