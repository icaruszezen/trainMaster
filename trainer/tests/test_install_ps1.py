"""scripts/install.ps1：Windows 的安装脚本。

脚本文本的检查在任何系统上运行；其余的在 Windows 上（CI 的 windows-latest）用 Windows PowerShell 5.1
（有 PowerShell 7 时也用它）运行：语法检查、配置文件的读写与 envfile.py 一致、命令行参数的拼接、-DryRun 的
检测结果与安装步骤（假的 nvidia-smi）、从平台下载后以脚本块运行。真实的安装（创建虚拟环境、安装 PyTorch、
注册计划任务）由 CI 运行 scripts/tests/install-windows-e2e.py 完成。
"""

from __future__ import annotations

import base64
import io
import json
import os
import re
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

import pytest
from fake_platform import FakePlatform
from fakes import TRAINER_DIR, WINDOWS, write_fake_smi

from trainmaster_trainer import envfile, variants

SCRIPT = TRAINER_DIR / "scripts" / "install.ps1"
MAIN_MARKER = "# ==== 主流程 ===="

POWERSHELL = shutil.which("powershell") if WINDOWS else None  # Windows PowerShell 5.1
PWSH = shutil.which("pwsh") if WINDOWS else None  # PowerShell 7（有时）
SHELLS = [s for s in (POWERSHELL, PWSH) if s]
needs_powershell = pytest.mark.skipif(POWERSHELL is None, reason="需要 Windows PowerShell")
each_shell = pytest.mark.parametrize(
    "shell", SHELLS or [None], ids=[Path(s).stem for s in SHELLS] or ["none"]
)

SMI_GPUS = (
    "0, GPU-aaaa, NVIDIA GeForce RTX 4090, 24564, 1024, 35, 45, 120.40, 450.00, 30, 8.9, "
    "00000000:01:00.0\n"
)
SMI_HEADER = (
    "| NVIDIA-SMI 581.57                 Driver Version: 581.57         CUDA Version: 13.0     |\n"
)


def ps_quote(value: str | Path) -> str:
    return "'" + str(value).replace("'", "''") + "'"


def run_ps(code: str, shell: str | None = None, timeout: float = 180, **env: str):
    """运行一段 PowerShell 代码（-EncodedCommand，输出按 UTF-8 解码）。"""
    prelude = "[Console]::OutputEncoding = New-Object Text.UTF8Encoding $false\n"
    encoded = base64.b64encode((prelude + code).encode("utf-16-le")).decode()
    return _run(
        [shell or POWERSHELL, "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded],
        timeout,
        env,
    )


def run_script(*args: str, shell: str | None = None, timeout: float = 300, **env: str):
    """用 -File 运行 install.ps1。"""
    cmd = [shell or POWERSHELL, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass"]
    return _run([*cmd, "-File", str(SCRIPT), *args], timeout, env)


def _run(cmd: list[str], timeout: float, env: dict[str, str]):
    full_env = {k: v for k, v in os.environ.items() if not k.startswith("TM_")}
    full_env.update(env)
    proc = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=full_env,
        timeout=timeout,
        check=False,
    )
    print(proc.stdout)
    print(proc.stderr, file=sys.stderr)
    return proc


def load_functions(params: str = "") -> str:
    """定义 install.ps1 的函数与变量而不运行安装：点运行（dot-source）主流程之前的部分。"""
    return (
        f"$text = [IO.File]::ReadAllText({ps_quote(SCRIPT)}, [Text.Encoding]::UTF8)\n"
        f"$head = $text.Substring(0, $text.IndexOf('{MAIN_MARKER}'))\n"
        f". ([scriptblock]::Create($head)) {params}\n"
    )


def script_text() -> str:
    return SCRIPT.read_bytes().decode("utf-8-sig")


# ---- 脚本文本（任何系统）----


def test_script_has_utf8_bom():
    # Windows PowerShell 5.1 用 -File 运行没有 BOM 的脚本时按系统的代码页（如 GBK）读取，中文会变成乱码
    assert SCRIPT.read_bytes().startswith(b"\xef\xbb\xbf")


def _code_lines() -> list[str]:
    lines, in_block = [], False
    for line in script_text().splitlines():
        stripped = line.strip()
        if stripped.startswith("<#"):
            in_block = True
        if not in_block and not stripped.startswith("#"):
            lines.append(line)
        if stripped.endswith("#>"):
            in_block = False
    return lines


def test_script_does_not_use_caller_dependent_automatic_variables():
    # 以脚本块运行（平台给出的命令）时 $PSScriptRoot / $PSCommandPath 是调用方的值；
    # exit 会关闭用户的 PowerShell 窗口，只能在用 -File 运行时使用（最后一行）。
    code = "\n".join(_code_lines())
    assert "$PSScriptRoot" not in code
    assert "$PSCommandPath" not in code
    exits = [line.strip() for line in _code_lines() if re.search(r"\bexit\b", line)]
    assert exits == ["if ($ScriptPath) { exit $Ctx.ExitCode }"]


def test_main_marker_is_unique():
    # load_functions 在这一行之前截断脚本
    assert script_text().count(MAIN_MARKER) == 1


# ---- 语法 ----


@needs_powershell
@each_shell
def test_script_parses(shell):
    # 语法错误，以及用中文引号（“”‘’，PowerShell 也把它们当作引号）括起来的字符串
    code = (
        f"$tokens = $null; $errors = $null\n"
        f"[void][System.Management.Automation.Language.Parser]::ParseFile({ps_quote(SCRIPT)}, "
        "[ref]$tokens, [ref]$errors)\n"
        "foreach ($e in $errors) { 'ERROR ' + $e.Extent.StartLineNumber + ': ' + $e.Message }\n"
        "foreach ($t in $tokens) {\n"
        "  $kinds = 'StringLiteral', 'StringExpandable', 'HereStringLiteral', 'HereStringExpandable'\n"
        "  if ($kinds -contains [string]$t.Kind -and $t.Text[0] -notin @([char]39, [char]34, [char]64)) {\n"
        "    'QUOTE ' + $t.Extent.StartLineNumber + ': ' + $t.Text }\n"
        "}\n"
        "'parsed'\n"
    )
    proc = run_ps(code, shell)
    assert proc.returncode == 0, proc.stderr
    assert proc.stdout.strip().splitlines() == ["parsed"]


# ---- 配置文件 ----

ENV_VALUES = [
    "plain",
    "it's",
    'a"b',
    "C:\\dir\\sub\\",
    "\\\\server\\share",
    "$HOME and `x`",
    "空 格",
    "中文'引号\"",
    "",
    "back\\\\slash\"quote'",
    "#not comment",
]


@needs_powershell
def test_env_values_round_trip(tmp_path: Path):
    # install.ps1 写出的值，envfile.py（训练节点）读回来原样不变
    values = tmp_path / "values.json"
    values.write_text(json.dumps(ENV_VALUES), encoding="utf-8")
    out = tmp_path / "out.env"
    code = load_functions() + (
        f"$values = [IO.File]::ReadAllText({ps_quote(values)}, [Text.Encoding]::UTF8) | ConvertFrom-Json\n"
        '$lines = for ($i = 0; $i -lt $values.Count; $i++) { "K$i=" + (ConvertTo-EnvValue $values[$i]) }\n'
        f'[IO.File]::WriteAllText({ps_quote(out)}, ($lines -join "`r`n"), '
        "(New-Object Text.UTF8Encoding $false))\n"
    )
    proc = run_ps(code)
    assert proc.returncode == 0, proc.stderr
    parsed = envfile.parse(out.read_text(encoding="utf-8"))
    assert parsed == {f"K{i}": v for i, v in enumerate(ENV_VALUES)}


MANAGED_KEYS = {
    "TM_SERVER_URL",
    "TM_NODE_TOKEN",
    "TM_NODE_NAME",
    "TM_TORCH_VARIANT",
    "TM_STATE_DIR",
    "TM_DATA_DIR",
    "TM_LOG_FILE",
    "CUDA_DEVICE_ORDER",
}


ENV_FILES = [
    '\ufeff# 注释\r\n\r\nTM_SERVER_URL=\'http://10.0.0.2:8080\'\r\nTM_NODE_TOKEN="a\\"b\\$c"\r\n'
    "export TM_NODE_NAME=显卡机 1\r\nCUDA_VISIBLE_DEVICES=0,1\r\nTM_DATA_DIR = 'D:\\data'   # 数据\r\n"
    "HTTP_PROXY='http://proxy:3128'\r\nTM_SERVER_URL='http://10.0.0.3:8080'\r\n",
    "TM_STATE_DIR=C:\\ProgramData\\trainMaster\\state\nTM_LOG_FILE=''\nEXTRA=x # 值的一部分\n",
    "",
]


@needs_powershell
@pytest.mark.parametrize("text", ENV_FILES)
def test_read_env_file_matches_envfile_py(tmp_path: Path, text: str):
    path = tmp_path / "trainer.env"
    path.write_bytes(text.encode("utf-8"))
    out = tmp_path / "out.json"
    code = load_functions() + (
        f"$r = Read-EnvFile {ps_quote(path)}\n"
        "$json = @{ Settings = $r.Settings; Extra = @($r.Extra) } | ConvertTo-Json -Depth 5\n"
        f"[IO.File]::WriteAllText({ps_quote(out)}, $json, (New-Object Text.UTF8Encoding $false))\n"
    )
    proc = run_ps(code)
    assert proc.returncode == 0, proc.stderr
    result = json.loads(out.read_text(encoding="utf-8"))
    expected = envfile.parse(text)
    managed = {k: v for k, v in expected.items() if k in MANAGED_KEYS}
    assert (result["Settings"] or {}) == managed
    # 其它设置原样保留：写回配置文件后训练节点读到的值不变
    extra = envfile.parse("\n".join(result["Extra"] or []))
    assert extra == {k: v for k, v in expected.items() if k not in MANAGED_KEYS}


def test_managed_keys_match_script():
    match = re.search(r"\$ManagedKeys = @\(([^)]*)\)", script_text())
    assert match is not None
    assert set(re.findall(r"'([A-Z_]+)'", match.group(1))) == MANAGED_KEYS


@needs_powershell
@pytest.mark.parametrize(
    ("text", "line"),
    [
        ("A=1\nNOT A LINE\n", 2),
        ("TM_NODE_NAME='x\n", 1),
        ('TM_NODE_NAME="x" y\n', 1),
        # 其它设置的格式也要检查：训练节点读取时任何一行不对都无法启动
        ("A=1\nHTTPS_PROXY='http://proxy:3128\n", 2),
    ],
)
def test_read_env_file_errors_like_envfile_py(tmp_path: Path, text: str, line: int):
    path = tmp_path / "trainer.env"
    path.write_text(text, encoding="utf-8")
    with pytest.raises(envfile.EnvFileError, match=f"第 {line} 行"):
        envfile.parse(text)
    code = load_functions() + (
        f"try {{ Read-EnvFile {ps_quote(path)} | Out-Null; 'no error' }} "
        "catch { $_.Exception.Message }\n"
    )
    proc = run_ps(code)
    assert f"第 {line} 行" in proc.stdout
    assert proc.stdout.startswith("TM_INSTALL_ABORT:")


# ---- 命令行参数 ----

ARGS = [
    "",
    "a b",
    'a"b',
    "C:\\path with space\\",
    "\\\\server\\share\\",
    'a\\\\"b',
    "中文 参数",
    "--opt=x y",
    "trailing\\",
    '"',
]


@needs_powershell
def test_invoke_capture_passes_arguments_exactly():
    code = load_functions() + (
        "$args2 = @(" + ", ".join(ps_quote(a) for a in ARGS) + ")\n"
        f"$r = Invoke-Capture {ps_quote(sys.executable)} "
        "(@('-c', 'import json, sys; print(json.dumps(sys.argv[1:]), end=\"\")') + $args2)\n"
        "$r.Stdout\n"
    )
    proc = run_ps(code)
    assert proc.returncode == 0, proc.stderr
    assert json.loads(proc.stdout.strip().splitlines()[-1]) == ARGS


# ---- -DryRun ----


def _dry_run_args(tmp_path: Path) -> list[str]:
    return [
        "-Python",
        sys.executable,
        "-Prefix",
        str(tmp_path / "trainer"),
        "-StateDir",
        str(tmp_path / "state"),
        "-DryRun",
    ]


@needs_powershell
@each_shell
def test_dry_run_with_gpu(tmp_path: Path, shell):
    smi = write_fake_smi(tmp_path / "smi", SMI_GPUS, SMI_HEADER)
    args = ["-ServerUrl", "http://192.0.2.1:8080/", "-Token", "s3cret!", "-Name", "显卡机 1"]
    proc = run_script(*args, *_dry_run_args(tmp_path), shell=shell, TM_NVIDIA_SMI=str(smi))
    assert proc.returncode == 0, proc.stdout
    out = proc.stdout
    cu130 = variants.get_variant("cu130")
    assert cu130 is not None
    assert "[0] NVIDIA GeForce RTX 4090（显存 24 GiB，计算能力 8.9）" in out
    assert "驱动：581.57" in out
    assert "推荐的 PyTorch 变体：cu130" in out
    assert f"torch=={cu130.torch}+cu130" in out
    assert "--extra-index-url https://download.pytorch.org/whl/cu130" in out
    assert "TM_SERVER_URL='http://192.0.2.1:8080'" in out
    assert "TM_NODE_TOKEN='<已隐藏，共 7 个字符>'" in out
    assert "TM_NODE_NAME='显卡机 1'" in out
    assert "TM_TORCH_VARIANT='cu130'" in out
    assert "s3cret!" not in out
    assert "以上为预览（-DryRun），没有做任何修改。" in out
    assert not (tmp_path / "trainer").exists()
    assert not (tmp_path / "state").exists()


@needs_powershell
def test_dry_run_without_gpu_and_manual_variant(tmp_path: Path):
    args = ["-ServerUrl", "http://192.0.2.1:8080", "-Variant", "cu126", "-Service"]
    proc = run_script(
        *args, *_dry_run_args(tmp_path), TM_NVIDIA_SMI=str(tmp_path / "no-nvidia-smi")
    )
    assert proc.returncode == 0, proc.stdout
    out = proc.stdout
    assert "显卡：未检测到 NVIDIA 显卡（将作为 CPU 训练节点）" in out
    assert "推荐的 PyTorch 变体：cpu" in out
    assert "使用手动指定的变体：cu126" in out
    assert "TM_NODE_TOKEN=''" in out
    assert "Register-ScheduledTask -TaskName trainmaster-trainer" in out


@needs_powershell
def test_dry_run_with_powershell7_module_path(tmp_path: Path):
    # 在从 PowerShell 7 中启动的程序（如 CI 中的 Python）里运行 Windows PowerShell 5.1 时继承了 PowerShell 7 的
    # 模块目录：脚本要去掉它们，否则 Get-Acl / Set-Acl 所在的模块加载失败
    pwsh_modules = [str(Path(PWSH).parent / "Modules")] if PWSH else []
    module_path = ";".join([*pwsh_modules, os.environ.get("PSMODULEPATH", "")])
    (tmp_path / "trainer").mkdir()
    proc = run_script(
        "-ServerUrl", "http://192.0.2.1:8080", "-Variant", "cpu", *_dry_run_args(tmp_path),
        PSMODULEPATH=module_path, TM_NVIDIA_SMI=str(tmp_path / "no-nvidia-smi"),
    )  # fmt: skip
    assert proc.returncode == 0, proc.stdout
    assert "无法读取权限" not in proc.stdout
    assert "Microsoft.PowerShell.Security" not in proc.stdout


@needs_powershell
@pytest.mark.parametrize(
    ("args", "code", "message"),
    [
        ([], 2, "缺少 -ServerUrl"),
        (["-ServerUrl", "ftp://x"], 2, "-ServerUrl 不合法"),
        (["-ServerUrl", "http://x", "-Token", "a b"], 2, "-Token 只能包含可见的 ASCII 字符"),
        (["-ServerUrl", "http://x", "-Variant", "cu999"], 2, "未知的变体 cu999"),
        (["-ServerUrl", "http://x", "-StateDir", "state"], 2, "-StateDir 必须是绝对路径"),
        (["-ServerUrl", "http://x", "-Python", "C:\\no\\python.exe"], 1, "无法运行"),
    ],
)
def test_dry_run_errors(tmp_path: Path, args, code, message):
    base = ["-Prefix", str(tmp_path / "trainer"), "-DryRun"]
    if "-Python" not in args:
        base += ["-Python", sys.executable]
    proc = run_script(*args, *base, TM_NVIDIA_SMI=str(tmp_path / "no-nvidia-smi"))
    assert proc.returncode == code, proc.stdout
    assert message in proc.stdout


@needs_powershell
def test_existing_prefix_that_is_not_a_venv(tmp_path: Path):
    prefix = tmp_path / "trainer"
    prefix.mkdir()
    (prefix / "other.txt").write_text("x", encoding="utf-8")
    proc = run_script(
        "-ServerUrl", "http://x", "-Python", sys.executable, "-Prefix", str(prefix), "-DryRun"
    )
    assert proc.returncode == 1
    assert "已存在且不是虚拟环境" in proc.stdout


@needs_powershell
def test_help():
    proc = run_script("-Help")
    assert proc.returncode == 0
    assert "用法：install.ps1 -ServerUrl URL" in proc.stdout


# ---- 从平台下载并以脚本块运行（“接入节点”中给出的命令）----


def trainer_zip() -> bytes:
    """与平台的 trainer.zip 相同的内容（见 backend/internal/trainerpkg）。"""
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        for name in ("pyproject.toml", "requirements.txt", "scripts/install.ps1"):
            zf.write(TRAINER_DIR / name, name)
        for path in sorted((TRAINER_DIR / "trainmaster_trainer").rglob("*")):
            if path.is_file() and "__pycache__" not in path.parts:
                zf.write(path, path.relative_to(TRAINER_DIR).as_posix())
    return buf.getvalue()


@pytest.fixture
def platform():
    p = FakePlatform()
    p.files["/api/v1/compute/windows/install.ps1"] = (
        SCRIPT.read_bytes().removeprefix(b"\xef\xbb\xbf"),
        "text/plain; charset=utf-8",
    )
    p.files["/api/v1/compute/windows/trainer.zip"] = (trainer_zip(), "application/zip")
    yield p
    p.close()


@needs_powershell
@each_shell
def test_scriptblock_from_platform(tmp_path: Path, platform, shell):
    smi = write_fake_smi(tmp_path / "smi", SMI_GPUS, SMI_HEADER)
    url = platform.url
    command = (
        f"& ([scriptblock]::Create((irm '{url}/api/v1/compute/windows/install.ps1'))) "
        f"-ServerUrl '{url}' -Token 'tok' -InstallPython -Service "
        + " ".join(ps_quote(a) if not a.startswith("-") else a for a in _dry_run_args(tmp_path))
    )
    # 在另一个脚本中运行：安装结束后（成功或失败）调用方继续运行，$LASTEXITCODE 为结果
    wrapper = tmp_path / "wrapper.ps1"
    wrapper.write_text(
        "[Console]::OutputEncoding = New-Object Text.UTF8Encoding $false\n"
        f"{command}\n"
        '"exit=$LASTEXITCODE"\n'
        f"& ([scriptblock]::Create((irm '{url}/api/v1/compute/windows/install.ps1'))) -DryRun\n"
        '"exit=$LASTEXITCODE"\n'
        '"after"\n',
        encoding="utf-8-sig",
    )
    proc = _run(
        [
            shell,
            "-NoProfile",
            "-NonInteractive",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            str(wrapper),
        ],
        300,
        {"TM_NVIDIA_SMI": str(smi)},
    )
    out = proc.stdout
    assert f"从平台下载训练节点：{url}/api/v1/compute/windows/trainer.zip" in out
    assert "推荐的 PyTorch 变体：cu130" in out
    assert "以上为预览（-DryRun），没有做任何修改。" in out
    lines = out.strip().splitlines()
    assert "exit=0" in lines
    assert lines[-2:] == ["exit=2", "after"]
    assert "缺少 -ServerUrl" in out
