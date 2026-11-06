"""install.ps1 的真实安装（CI 的 windows-latest：需要管理员权限与网络，会改动这台电脑）。

用假的平台（tests/fake_platform.py）代替服务端，依次：
1. 首次安装：CPU 版 PyTorch，注册计划任务（-Service）。训练节点以 SYSTEM 运行后注册、发送心跳，上报的
   系统为 windows、环境检测为 cpu_only；安装目录、状态目录与配置文件只有 SYSTEM 与管理员可以修改。
2. 不带参数重新运行：沿用配置文件中的设置，更新并重启计划任务，节点标识不变。
3. -Uninstall：删除计划任务，训练节点停止。

运行：python trainer/scripts/tests/install-windows-e2e.py（用运行它的 Python 创建虚拟环境）。
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any, NoReturn

TRAINER_DIR = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(TRAINER_DIR), str(TRAINER_DIR / "tests")]

from fake_platform import FakePlatform  # noqa: E402

from trainmaster_trainer import envfile  # noqa: E402

SCRIPT = TRAINER_DIR / "scripts" / "install.ps1"
TASK = "trainmaster-trainer"
TOKEN = "e2e-token"
PROGRAM_DATA = Path(os.environ.get("PROGRAMDATA", r"C:\ProgramData"))
PREFIX = PROGRAM_DATA / "trainMaster" / "trainer"
STATE_DIR = PROGRAM_DATA / "trainMaster" / "state"
ENV_FILE = PREFIX / "trainer.env"
LOG_FILE = STATE_DIR / "logs" / "trainer.log"


def step(title: str) -> None:
    print(f"\n==== {title} ====", flush=True)


def install(*args: str) -> str:
    """用 Windows PowerShell 5.1 运行 install.ps1，返回输出（失败时结束测试）。"""
    cmd = ["powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass"]
    cmd += ["-File", str(SCRIPT), *args]
    print("+", " ".join(cmd), flush=True)
    env = {k: v for k, v in os.environ.items() if not k.startswith("TM_")}
    proc = subprocess.run(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=env,
        timeout=1800,
        check=False,
    )
    print(proc.stdout, flush=True)
    if proc.returncode != 0:
        fail(f"install.ps1 退出码 {proc.returncode}")
    return proc.stdout


def powershell(code: str) -> str:
    # 从 PowerShell 7 中启动时（CI）继承的 PSModulePath 会让 Windows PowerShell 5.1 加载不了 Get-Acl 等命令所在的模块
    env = {k: v for k, v in os.environ.items() if k.upper() != "PSMODULEPATH"}
    proc = subprocess.run(
        ["powershell", "-NoProfile", "-NonInteractive", "-Command", code],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=env,
        timeout=120,
        check=False,
    )
    if proc.returncode != 0:
        fail(f"PowerShell 失败：{code}\n{proc.stdout}\n{proc.stderr}")
    return proc.stdout.strip()


def task_info() -> dict[str, Any] | None:
    out = powershell(
        "[Console]::OutputEncoding = New-Object Text.UTF8Encoding $false; "
        f"$t = Get-ScheduledTask -TaskName {TASK} -ErrorAction SilentlyContinue; "
        "if ($t) { @{ State = [string]$t.State; UserId = $t.Principal.UserId; "
        "RunLevel = [string]$t.Principal.RunLevel; Execute = $t.Actions[0].Execute; "
        "Arguments = $t.Actions[0].Arguments; Triggers = $t.Triggers.Count } | ConvertTo-Json } "
        "else { 'null' }"
    )
    return json.loads(out)


def acl(path: Path) -> dict[str, Any]:
    out = powershell(
        f"$a = Get-Acl -LiteralPath '{path}'; "
        "$sid = [Security.Principal.SecurityIdentifier]; "
        "@{ Owner = $a.GetOwner($sid).Value; Protected = $a.AreAccessRulesProtected; "
        "Rules = @($a.GetAccessRules($true, $true, $sid) | ForEach-Object { "
        "@{ Sid = $_.IdentityReference.Value; Rights = [string]$_.FileSystemRights; "
        "Type = [string]$_.AccessControlType } }) } | ConvertTo-Json -Depth 4"
    )
    return json.loads(out)


def wait_for(what: str, check: Callable[[], Any], timeout: float) -> Any:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(2)
    fail(f"等待超时（{timeout:.0f} 秒）：{what}")
    return None


def fail(message: str) -> NoReturn:
    print(f"\nFAILED: {message}", flush=True)
    if LOG_FILE.exists():
        print(f"\n---- {LOG_FILE}（最后 200 行）----")
        print("\n".join(LOG_FILE.read_text(encoding="utf-8", errors="replace").splitlines()[-200:]))
    sys.exit(1)


def expect(condition: Any, message: str) -> None:
    if not condition:
        fail(message)


def check_protected(path: Path, users_read: bool) -> None:
    info = acl(path)
    print(f"{path}: {json.dumps(info, ensure_ascii=False)}")
    expect(info["Owner"] == "S-1-5-32-544", f"{path} 的所有者应为 Administrators：{info['Owner']}")
    expect(info["Protected"], f"{path} 不应继承上级目录的权限")
    sids = {r["Sid"] for r in info["Rules"] if r["Type"] == "Allow"}
    expected = {"S-1-5-18", "S-1-5-32-544"} | ({"S-1-5-32-545"} if users_read else set())
    expect(sids == expected, f"{path} 的权限应只授予 {sorted(expected)}：{sorted(sids)}")
    for rule in info["Rules"]:
        if rule["Sid"] == "S-1-5-32-545":
            expect("Write" not in rule["Rights"] and "Modify" not in rule["Rights"],
                   f"普通用户不应能修改 {path}：{rule['Rights']}")  # fmt: skip


def main() -> None:
    # 输出重定向（如 CI）时 Windows 按代码页编码，英文系统的 cp1252 没有中文
    for stream in (sys.stdout, sys.stderr):
        stream.reconfigure(encoding="utf-8", errors="backslashreplace")  # type: ignore[union-attr]
    if sys.platform != "win32":
        print("只能在 Windows 上运行")
        sys.exit(2)
    expect(task_info() is None, f"这台电脑上已有计划任务 {TASK}，请在干净的环境中运行")
    platform = FakePlatform(token=TOKEN, interval=2)
    try:
        run(platform)
    finally:
        platform.close()
    print("\nOK")


def run(platform: FakePlatform) -> None:
    step("1. 首次安装（CPU 版 PyTorch，注册计划任务）")
    out = install(
        "-ServerUrl", platform.url, "-Token", TOKEN, "-Name", "CI Windows 节点",
        "-Variant", "cpu", "-Python", sys.executable, "-Service",
    )  # fmt: skip
    expect("安装完成：PyTorch 变体 cpu，自检通过。" in out, "自检没有通过")
    expect(TOKEN not in out, "输出中不应出现令牌")

    settings = envfile.load(ENV_FILE)
    print(json.dumps(settings, ensure_ascii=False, indent=2))
    expect(settings["TM_SERVER_URL"] == platform.url, "配置文件中的平台地址不对")
    expect(settings["TM_NODE_TOKEN"] == TOKEN, "配置文件中的令牌不对")
    expect(settings["TM_NODE_NAME"] == "CI Windows 节点", "配置文件中的节点名称不对")
    expect(settings["TM_TORCH_VARIANT"] == "cpu", "配置文件中的变体不对")
    expect(Path(settings["TM_LOG_FILE"]) == LOG_FILE, "配置文件中的日志文件不对")

    task = task_info()
    print(json.dumps(task, ensure_ascii=False, indent=2))
    expect(task is not None, "没有注册计划任务")
    assert task is not None
    expect(task["UserId"] in ("SYSTEM", "NT AUTHORITY\\SYSTEM"), "计划任务应以 SYSTEM 运行")
    expect(task["Execute"].strip('"').lower() == str(PREFIX / "Scripts" / "python.exe").lower(),
           f"计划任务运行的程序不对：{task['Execute']}")  # fmt: skip
    expect(f'--env-file "{ENV_FILE}"' in task["Arguments"], "计划任务的参数不对")
    expect(task["Triggers"] == 2, "计划任务应有开机与立即开始两个触发器")

    node = wait_for(
        "训练节点注册并上报环境检测结果",
        lambda: (
            (n := platform.node()) and n.get("heartbeats", 0) >= 1 and n.get("environment") and n
        ),
        600,
    )
    print(
        json.dumps(
            {k: node.get(k) for k in ("name", "registrations", "heartbeats")}, ensure_ascii=False
        )
    )
    expect(node["name"] == "CI Windows 节点", f"节点名称不对：{node['name']}")
    expect(
        node["hardware"]["platform"] == "windows", f"系统不对：{node['hardware'].get('platform')}"
    )
    environment = node["environment"]
    print(json.dumps(environment, ensure_ascii=False, indent=2))
    expect(environment["status"] == "cpu_only", f"环境检测结果不对：{environment['status']}")
    expect(environment["torch_variant"] == "cpu", "环境检测中的变体不对")
    expect(task_info()["State"] == "Running", "计划任务应在运行")  # type: ignore[index]
    expect(LOG_FILE.exists() and LOG_FILE.stat().st_size > 0, f"没有日志文件 {LOG_FILE}")
    node_key = (STATE_DIR / "node-key").read_text(encoding="utf-8").strip()

    check_protected(PROGRAM_DATA / "trainMaster", users_read=True)
    check_protected(PREFIX, users_read=True)
    check_protected(STATE_DIR, users_read=True)
    check_protected(ENV_FILE, users_read=False)

    step("2. 重新运行（沿用配置文件，更新并重启计划任务）")
    registrations = node["registrations"]
    out = install()
    expect("沿用配置文件" in out and "TM_NODE_TOKEN" in out, "应沿用配置文件中的设置")
    expect(f"已注册计划任务 {TASK}：安装后更新并重启它" in out, "应更新已注册的计划任务")
    expect("复用已有的虚拟环境" in out, "应复用虚拟环境")
    expect(
        "Disable-ScheduledTask" in out, "安装期间应禁用计划任务（避免它启动一半新一半旧的训练节点）"
    )
    wait_for(
        "训练节点重新注册",
        lambda: (n := platform.node()) and n.get("registrations", 0) > registrations,
        300,
    )
    expect(len(platform.nodes) == 1, f"节点标识应不变：{list(platform.nodes)}")
    expect((STATE_DIR / "node-key").read_text(encoding="utf-8").strip() == node_key, "节点标识变了")
    expect(envfile.load(ENV_FILE)["TM_NODE_TOKEN"] == TOKEN, "令牌应沿用")
    expect(task_info()["State"] == "Running", "重新运行后计划任务应已启用并在运行")  # type: ignore[index]

    step("3. -Uninstall")
    install("-Uninstall")
    expect(task_info() is None, "计划任务应已删除")
    time.sleep(5)
    before = platform.node()["heartbeats"]  # type: ignore[index]
    time.sleep(10)
    expect(platform.node()["heartbeats"] == before, "训练节点应已停止")  # type: ignore[index]


if __name__ == "__main__":
    main()
