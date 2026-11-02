"""agent.py：对本地 http.server 的假平台运行主循环（假时钟让等待瞬间完成）。"""

from __future__ import annotations

import json
import logging
import os
import random
import signal
import socket
import subprocess
import sys
import threading
import time
from pathlib import Path
from typing import Any

import pytest
from fake_platform import HEARTBEAT, REGISTER, FakePlatform, wait_until
from fakes import TRAINER_DIR

from trainmaster_trainer import gpu
from trainmaster_trainer.agent import Agent, Backoff, check_heartbeat
from trainmaster_trainer.config import Config
from trainmaster_trainer.hardware import HardwareProbe

TOKEN = "right-token-0123456789"


class FakeClock:
    """monotonic 时钟的替身：sleep 立即返回并把时间向前推。"""

    def __init__(self) -> None:
        self.now = 1000.0
        self.sleeps: list[float] = []
        self.lock = threading.Lock()

    def __call__(self) -> float:
        return self.now

    def sleep(self, seconds: float) -> None:
        with self.lock:
            self.sleeps.append(seconds)
            self.now += seconds
        time.sleep(0.002)  # 让出 CPU 给检测线程与服务端线程


class FakeDetector:
    def __init__(self, gate: threading.Event | None = None, error: Exception | None = None) -> None:
        self.gate = gate
        self.error = error
        self.calls = 0
        self.hardware_seen: list[dict[str, Any]] = []
        self.host_drivers_seen: list[str | None] = []
        self.cancelled = threading.Event()

    def detect(self, hardware, *, device_nodes=(), pci_gpu_count=None, host_driver_version=None):
        self.calls += 1
        self.hardware_seen.append(hardware)
        self.host_drivers_seen.append(host_driver_version)
        if self.gate is not None:
            while not self.gate.wait(0.01):
                if self.cancelled.is_set():
                    break
        if self.error is not None:
            raise self.error
        return {"status": "cpu_only", "accelerator": "cpu", "issues": [], "run": self.calls}

    def cancel(self) -> None:
        self.cancelled.set()


class FakeHardware:
    def __init__(self) -> None:
        self.count = 0
        self.extra: dict[str, Any] = {}  # 加到 hardware 中的其它字段

    def __call__(self) -> HardwareProbe:
        self.count += 1
        return HardwareProbe(
            hardware={"os": "Test OS", "gpus": [], "probe": self.count, **self.extra},
            gpu=gpu.GpuProbe(None, [], {"method": "none", "error": None, "host_gpu_count": None}),
            device_nodes=[],
            host_driver_version="440.100",
        )


class Harness:
    def __init__(
        self, tmp_path: Path, url: str, *, token: str | None = TOKEN, detector=None, client=None
    ):
        self.clock = FakeClock()
        self.detector = detector or FakeDetector()
        self.hardware = FakeHardware()
        cfg = Config(
            server_url=url,
            node_token=token,
            node_name="gpu-01",
            state_dir=tmp_path,
            data_dir=tmp_path,
        )
        self.agent = Agent(
            cfg,
            client=client,
            probe_hardware=self.hardware,
            detector=self.detector,
            sleep=self.clock.sleep,
            clock=self.clock,
            rng=random.Random(1),
            node_key="node-key-1",
            hostname="host-1",
        )
        self.exit_code: int | None = None
        self.thread = threading.Thread(target=self._run, daemon=True)

    def _run(self) -> None:
        self.exit_code = self.agent.run()

    def __enter__(self) -> Harness:
        self.thread.start()
        return self

    def __exit__(self, *exc: Any) -> None:
        self.agent.stop()
        self.thread.join(timeout=10)
        assert not self.thread.is_alive()
        assert self.exit_code == 0


@pytest.fixture
def platform():
    p = FakePlatform(token=TOKEN)
    yield p
    p.close()


def registered(platform: FakePlatform) -> dict[str, Any] | None:
    return platform.node()


def test_register_then_heartbeat_with_environment(platform, tmp_path, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    gate = threading.Event()
    with Harness(tmp_path, platform.url, detector=FakeDetector(gate)) as h:
        node = wait_until(lambda: registered(platform))
        reg = platform.requests[0]
        assert reg["path"] == REGISTER
        body = reg["body"]
        assert body["node_key"] == "node-key-1"
        assert body["name"] == "gpu-01"
        assert body["hostname"] == "host-1"
        assert body["agent_version"] == "0.1.0"
        assert body["hardware"]["probe"] == 1
        assert body["environment"] is None  # 检测还没有完成
        assert body["metrics"]["cpu_percent"] is None  # 第一次采集
        assert reg["headers"]["Authorization"] == f"Bearer {TOKEN}"

        # 检测期间心跳照常
        wait_until(lambda: node.get("heartbeats", 0) >= 3)
        assert node["environment"] is None
        gate.set()
        wait_until(lambda: node.get("environment_updates") == 1)
        assert node["environment"]["run"] == 1
        heartbeats = wait_until(lambda: node["heartbeats"] >= 6 and node["heartbeats"])
        # 检测结果只上报一次；之后的心跳 environment 为 null
        assert node["environment_updates"] == 1
        assert heartbeats >= 6
        last = [r for r in platform.requests if r["path"] == HEARTBEAT][-1]["body"]
        assert last["environment"] is None and last["hardware"] is None
        assert isinstance(last["metrics"]["cpu_percent"], float)

        data = json.loads((tmp_path / "heartbeat").read_text())
        assert data["interval_seconds"] == 10 and data["node_id"] == 1
        assert h.agent.registered and h.agent.node_id == 1
        # 心跳之间按服务端给出的间隔等待
        assert h.clock.sleeps[-1] == pytest.approx(10.0)
    assert h.detector.calls == 1
    messages = [r.getMessage() for r in caplog.records]
    assert any("registered with" in m for m in messages)
    assert any("environment detection finished" in m for m in messages)
    # 不会每次心跳都打日志
    assert len(messages) < 15


def test_environment_ready_before_registration_is_sent_in_register(platform, tmp_path):
    platform.mode = "503"
    with Harness(tmp_path, platform.url) as h:
        wait_until(lambda: h.detector.calls == 1 and h.agent._last_environment is not None)
        platform.mode = "up"
        node = wait_until(lambda: registered(platform))
        assert node["environment"]["run"] == 1
        wait_until(lambda: node.get("heartbeats", 0) >= 2)
        assert node.get("environment_updates") is None  # 已随注册上报，心跳中不再重复


def test_unknown_node_registers_again_with_last_environment(platform, tmp_path, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    with Harness(tmp_path, platform.url):
        node = wait_until(lambda: registered(platform))
        # 检测结果可能随注册或随之后的心跳上报
        wait_until(lambda: node.get("environment") is not None)
        with platform.lock:
            platform.nodes.clear()  # 节点在平台上被删除
        new = wait_until(lambda: registered(platform))
        assert new["node_id"] == 2
        assert new["environment"]["run"] == 1  # 重新注册时带上最近一次的检测结果
        wait_until(lambda: new.get("heartbeats", 0) >= 2)
    assert any("registering again" in r.getMessage() for r in caplog.records)


def test_wrong_token_backs_off_and_recovers(platform, tmp_path, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    with Harness(tmp_path, platform.url, token="wrong-token-000000000") as h:
        wait_until(lambda: platform.count(REGISTER) >= 7)
        assert h.thread.is_alive()
        assert registered(platform) is None
        delays = h.clock.sleeps[:7]
        for i, delay in enumerate(delays):
            base = min(60.0, 2.0**i)
            assert 0.75 * base <= delay <= min(60.0, 1.25 * base), delays
        errors = [
            r for r in caplog.records if r.levelno == logging.ERROR and "401" in r.getMessage()
        ]
        assert len(errors) == 1  # 同样的错误只记录一次
        assert "TM_NODE_TOKEN" in errors[0].getMessage()

        platform.token = "wrong-token-000000000"  # 服务端改用节点的令牌
        node = wait_until(lambda: registered(platform))
        wait_until(lambda: node.get("heartbeats", 0) >= 1)
        assert h.agent._backoff.attempt == 0


def test_backoff_is_capped(platform, tmp_path):
    platform.mode = "503"
    with Harness(tmp_path, platform.url) as h:
        wait_until(lambda: platform.count(REGISTER) >= 12)
        assert max(h.clock.sleeps) <= 60.0
        assert h.clock.sleeps[10] >= 45.0


def test_validation_error_on_register(platform, tmp_path, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    platform.mode = "422"
    with Harness(tmp_path, platform.url):
        wait_until(lambda: platform.count(REGISTER) >= 3)
        platform.mode = "up"
        wait_until(lambda: registered(platform))
    assert any(
        r.levelno == logging.ERROR
        and "422" in r.getMessage()
        and "节点名称不合法" in r.getMessage()
        for r in caplog.records
    )


def test_probe_requested_triggers_new_probe(platform, tmp_path):
    gate = threading.Event()
    with Harness(tmp_path, platform.url, detector=FakeDetector(gate)) as h:
        node = wait_until(lambda: registered(platform))
        gate.set()  # 注册之后才完成检测，结果随心跳上报
        wait_until(lambda: node.get("environment_updates") == 1)
        assert h.hardware.count == 1
        with platform.lock:
            node["probe_requested"] = True
        wait_until(lambda: node.get("environment_updates") == 2)
        assert node["probe_requested"] is False
        assert node["hardware_updates"] == 1
        assert node["hardware"]["probe"] == 2
        assert node["environment"]["run"] == 2
        assert h.detector.hardware_seen[-1]["probe"] == 2
        assert h.detector.host_drivers_seen == ["440.100", "440.100"]  # 诊断信息交给检测
        hb = node["heartbeats"]
        wait_until(lambda: node["heartbeats"] >= hb + 3)
        assert h.detector.calls == 2  # 请求被清除后不再重复检测


def test_probe_request_ignored_while_detection_runs(platform, tmp_path):
    gate = threading.Event()
    with Harness(tmp_path, platform.url, detector=FakeDetector(gate)) as h:
        node = wait_until(lambda: registered(platform))
        with platform.lock:
            node["probe_requested"] = True
        wait_until(lambda: node.get("heartbeats", 0) >= 5)
        assert h.detector.calls == 1 and h.hardware.count == 1
        gate.set()
        wait_until(lambda: node.get("environment_updates") == 1)
        hb = node["heartbeats"]
        wait_until(lambda: node["heartbeats"] >= hb + 3)
        assert h.detector.calls == 1  # 检测结果清除了请求


def test_restart_marks_node_detecting_without_second_detection(platform, tmp_path):
    """重启后注册时检测还没完成：平台保留上次的结果并标记为正在检测（probe_requested），
    节点忽略这个请求，不会再启动一次检测；结果上报后请求被清除。"""
    old = {"status": "error", "issues": [{"code": "driver_too_old"}], "run": 0}
    with platform.lock:
        platform.nodes["node-key-1"] = {"node_id": 9, "environment": old, "probe_requested": False}
    gate = threading.Event()
    with Harness(tmp_path, platform.url, detector=FakeDetector(gate)) as h:
        wait_until(lambda: (registered(platform) or {}).get("registrations"))
        node = registered(platform)
        assert node["probe_requested"] is True
        assert node["environment"] is old  # 页面在“正在检测”之下仍显示上次的结果
        wait_until(lambda: node.get("heartbeats", 0) >= 5)
        assert h.detector.calls == 1 and h.hardware.count == 1
        gate.set()
        wait_until(lambda: node.get("environment_updates") == 1)
        assert node["probe_requested"] is False
        assert node["environment"]["run"] == 1
        hb = node["heartbeats"]
        wait_until(lambda: node["heartbeats"] >= hb + 3)
        assert h.detector.calls == 1 and h.hardware.count == 1


def test_probe_request_answered_by_result_finished_during_heartbeat(tmp_path):
    """检测在一次心跳请求进行期间完成：这次响应里的 probe_requested 由随后上报的结果回应。"""
    gate = threading.Event()
    detector = FakeDetector(gate)
    sent: list[dict[str, Any]] = []
    holder: dict[str, Agent] = {}

    class Scripted:
        def register(self, payload):
            return {"node_id": 7, "heartbeat_interval_seconds": 10}

        def heartbeat(self, payload):
            sent.append(payload)
            if len(sent) == 1:
                gate.set()  # 检测在请求进行期间完成
                wait_until(lambda: holder["agent"]._pending_environment is not None)
            # 服务端在收到检测结果之前一直返回 probe_requested
            return {"probe_requested": not any(p["environment"] for p in sent)}

    h = Harness(tmp_path, "http://127.0.0.1:9", detector=detector, client=Scripted())
    holder["agent"] = h.agent
    with h:
        wait_until(lambda: len(sent) >= 5)
        assert sent[0]["environment"] is None
        assert sent[1]["environment"]["run"] == 1
        assert detector.calls == 1 and h.hardware.count == 1


def test_server_down_then_recovers(platform, tmp_path, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    with Harness(tmp_path, platform.url) as h:
        node = wait_until(lambda: registered(platform))
        wait_until(lambda: node.get("heartbeats", 0) >= 2)
        platform.mode = "drop"
        start = len(h.clock.sleeps)
        wait_until(lambda: len(h.clock.sleeps) >= start + 5)
        backoff = h.clock.sleeps[start + 1 : start + 5]
        assert backoff == sorted(backoff) and backoff[0] < 2.5  # 指数退避，而不是按心跳间隔
        assert h.thread.is_alive()
        platform.mode = "up"
        hb = node["heartbeats"]
        wait_until(lambda: node["heartbeats"] >= hb + 2)
        assert h.agent._backoff.attempt == 0
    messages = [r.getMessage() for r in caplog.records]
    assert sum("lost connection" in m for m in messages) == 1
    assert sum("connection to" in m and "restored" in m for m in messages) == 1


def test_server_error_during_heartbeat(platform, tmp_path, caplog):
    caplog.set_level(logging.INFO, logger="trainmaster_trainer")
    with Harness(tmp_path, platform.url):
        node = wait_until(lambda: registered(platform))
        wait_until(lambda: node.get("heartbeats", 0) >= 1)
        platform.mode = "503"
        wait_until(lambda: platform.count(HEARTBEAT) >= node["heartbeats"] + 3)
        platform.mode = "up"
        hb = node["heartbeats"]
        wait_until(lambda: node["heartbeats"] >= hb + 1)
    assert any("server error during heartbeat" in r.getMessage() for r in caplog.records)


def test_server_unreachable_at_start(tmp_path):
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    with Harness(tmp_path, f"http://127.0.0.1:{port}") as h:
        wait_until(lambda: len(h.clock.sleeps) >= 4)
        assert not h.agent.registered
    assert not (tmp_path / "heartbeat").exists()


def test_interval_from_server(platform, tmp_path):
    platform.interval = 3
    with Harness(tmp_path, platform.url) as h:
        node = wait_until(lambda: registered(platform))
        wait_until(lambda: node.get("heartbeats", 0) >= 3)
        assert h.clock.sleeps[-1] == pytest.approx(3.0)
        platform.interval = "bogus"
        hb = node["heartbeats"]
        wait_until(lambda: node["heartbeats"] >= hb + 2)
        assert h.agent.interval == 3.0


def test_stop_cancels_running_detection(platform, tmp_path):
    detector = FakeDetector(threading.Event())  # 永不放行，直到取消
    h = Harness(tmp_path, platform.url, detector=detector)
    with h:
        wait_until(lambda: registered(platform))
        wait_until(lambda: detector.calls == 1)
    assert detector.cancelled.is_set()
    assert not h.agent.detecting


def test_detector_crash_reports_error_environment(platform, tmp_path):
    with Harness(tmp_path, platform.url, detector=FakeDetector(error=RuntimeError("boom"))):
        node = wait_until(lambda: registered(platform))
        wait_until(lambda: node.get("environment_updates") == 1)
        env = node["environment"]
        assert env["status"] == "error"
        assert "boom" in env["self_test"]["message"]


def test_detector_crash_keeps_the_probe_diagnostics(platform, tmp_path):
    # 检测出错时的结果同样按探测到的诊断信息判断：显卡没有传入容器、宿主机驱动 440.100 过旧时
    # 提示升级驱动，而不是安装 NVIDIA Container Toolkit。
    h = Harness(tmp_path, platform.url, detector=FakeDetector(error=RuntimeError("boom")))
    h.hardware.extra = {
        "arch": "x86_64",
        "in_container": True,
        "gpu_detection": {"method": "none", "error": None, "host_gpu_count": 1},
    }
    with h:
        node = wait_until(lambda: registered(platform))
        # 检测很快出错：结果可能在注册时就已上报，也可能在之后的心跳中上报
        env = wait_until(lambda: node.get("environment"))
        codes = [i["code"] for i in env["issues"]]
    assert "driver_too_old" in codes and "gpu_not_passed_through" not in codes


def test_unexpected_client_error_does_not_stop_loop(platform, tmp_path, caplog):
    from trainmaster_trainer.client import Client

    class Flaky(Client):
        calls = 0

        def heartbeat(self, payload):
            Flaky.calls += 1
            if Flaky.calls == 2:
                raise RuntimeError("unexpected bug")
            return super().heartbeat(payload)

    with Harness(tmp_path, platform.url, client=Flaky(platform.url, TOKEN)):
        node = wait_until(lambda: registered(platform))
        wait_until(lambda: node.get("heartbeats", 0) >= 3)
    assert any("unexpected error in the agent loop" in r.getMessage() for r in caplog.records)


def test_agent_requires_server_url(tmp_path):
    with pytest.raises(ValueError):
        Agent(Config(state_dir=tmp_path), node_key="k")


def test_heartbeat_file_write_failure_is_logged_once(platform, tmp_path, caplog):
    state = tmp_path / "state-file"
    state.write_text("not a directory")
    cfg = Config(server_url=platform.url, node_token=TOKEN, state_dir=state, data_dir=tmp_path)
    clock = FakeClock()
    agent = Agent(
        cfg,
        probe_hardware=FakeHardware(),
        detector=FakeDetector(),
        sleep=clock.sleep,
        clock=clock,
        node_key="k-unwritable",
    )
    thread = threading.Thread(target=agent.run, daemon=True)
    thread.start()
    try:
        node = wait_until(lambda: registered(platform))
        wait_until(lambda: node.get("heartbeats", 0) >= 3)
    finally:
        agent.stop()
        thread.join(timeout=10)
    warnings = [r for r in caplog.records if "cannot write heartbeat file" in r.getMessage()]
    assert len(warnings) == 1


# ---- Backoff ----


def test_backoff_sequence():
    b = Backoff(rng=random.Random(42))
    delays = [b.next() for _ in range(10)]
    for i, d in enumerate(delays):
        base = min(60.0, 2.0**i)
        assert 0.75 * base <= d <= min(60.0, 1.25 * base)
    b.reset()
    assert b.next() <= 1.25
    for _ in range(100):
        assert b.next() <= 60.0


# ---- healthcheck ----


def write_heartbeat(d: Path, unix: float, interval: float = 10) -> None:
    d.mkdir(parents=True, exist_ok=True)
    (d / "heartbeat").write_text(json.dumps({"unix": unix, "interval_seconds": interval}))


def test_check_heartbeat(tmp_path: Path):
    a, b = tmp_path / "a", tmp_path / "b"
    ok, message = check_heartbeat([a, b], now=1000)
    assert not ok and "没有心跳记录" in message
    write_heartbeat(a, 1000 - 59)
    ok, message = check_heartbeat([a, b], now=1000)
    assert ok and "59 秒前" in message
    write_heartbeat(a, 1000 - 61)
    ok, message = check_heartbeat([a, b], now=1000)
    assert not ok and "超过 60 秒" in message
    write_heartbeat(b, 1000 - 5)  # 退回的状态目录中有更新的心跳
    assert check_heartbeat([a, b], now=1000)[0]


def test_check_heartbeat_ignores_corrupt_files(tmp_path: Path):
    (tmp_path / "heartbeat").write_text("{not json")
    assert not check_heartbeat([tmp_path], now=0)[0]
    (tmp_path / "heartbeat").write_text(json.dumps({"interval_seconds": 10}))
    assert not check_heartbeat([tmp_path], now=0)[0]
    write_heartbeat(tmp_path, 100, interval=60)
    assert check_heartbeat([tmp_path], now=100 + 3 * 60 + 29)[0]


# ---- 端到端：python -m trainmaster_trainer run 收到 SIGTERM 后退出码为 0 ----


def test_run_command_end_to_end_and_sigterm(platform, tmp_path: Path):
    platform.interval = 1
    env = dict(
        os.environ,
        PYTHONPATH=str(TRAINER_DIR),
        TM_SERVER_URL=platform.url,
        TM_NODE_TOKEN=TOKEN,
        TM_NODE_NAME="e2e-node",
        TM_STATE_DIR=str(tmp_path / "state"),
        TM_DATA_DIR=str(tmp_path),
        TM_LOG_LEVEL="debug",
        TM_NVIDIA_SMI=str(tmp_path / "no-nvidia-smi"),
    )
    proc = subprocess.Popen(
        [sys.executable, "-m", "trainmaster_trainer", "run"],
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    try:
        node = wait_until(lambda: registered(platform), timeout=30)
        assert node["name"] == "e2e-node"
        wait_until(lambda: node.get("environment") is not None, timeout=60)
        assert node["environment"]["status"] in ("error", "cpu_only", "ready")
        wait_until(lambda: node.get("heartbeats", 0) >= 2, timeout=30)
        key = (tmp_path / "state" / "node-key").read_text().strip()
        assert key in platform.nodes

        health = subprocess.run(
            [sys.executable, "-m", "trainmaster_trainer", "healthcheck"],
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
        )
        assert health.returncode == 0, health.stderr
        proc.send_signal(signal.SIGTERM)
        _, stderr = proc.communicate(timeout=30)
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.communicate()
    assert proc.returncode == 0, stderr
    assert "received SIGTERM, stopping" in stderr
    assert "trainer stopped" in stderr
