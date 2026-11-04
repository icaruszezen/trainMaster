"""训练节点主循环：注册 → 定期心跳（上报实时指标）→ 按需重新检测硬件与 CUDA 环境。
第一次注册成功后启动任务线程（tasks.TaskWorker），领取并执行平台的任务（pt → ONNX 转换、审图识别）。

- 服务端不可达、5xx、401（令牌不对）、422 都不会让进程退出：记录清晰的日志后指数退避重试
  （1 秒起，最长 60 秒，带抖动），配置在服务端修正后自动恢复。
- CUDA 环境检测在后台线程中运行（实际工作在子进程中），检测期间心跳照常，结果在下一次心跳中上报。
- 每次与服务端通信成功后更新 STATE_DIR/heartbeat，供 Docker HEALTHCHECK（healthcheck 子命令）判断。
- 日志只在状态变化时输出 INFO（连接成功、断开、恢复、重新注册、检测完成），不会每次心跳都打日志。
"""

from __future__ import annotations

import json
import logging
import random
import socket
import threading
import time
from collections.abc import Callable, Iterable
from pathlib import Path
from typing import Any, Protocol

from . import __version__
from . import hardware as hardware_mod
from . import issues as issues_mod
from . import node_key as node_key_mod
from .backoff import BACKOFF_BASE, BACKOFF_CAP, Backoff
from .client import ApiError, Client, TransportError
from .config import Config
from .environment import EnvironmentDetector, deployment_from, failure_raw, utc_now
from .tasks import TaskWorker

__all__ = ["BACKOFF_BASE", "BACKOFF_CAP", "Agent", "Backoff", "check_heartbeat"]

log = logging.getLogger(__name__)

HEARTBEAT_FILE = "heartbeat"
DEFAULT_INTERVAL = 10.0
MIN_INTERVAL = 1.0
MAX_INTERVAL = 300.0
HEALTH_GRACE_SECONDS = 30.0


class Detector(Protocol):
    def detect(
        self,
        hardware: dict[str, Any],
        *,
        device_nodes: Iterable[str] = (),
        pci_gpu_count: int | None = None,
        host_driver_version: str | None = None,
    ) -> dict[str, Any]: ...

    def cancel(self) -> None: ...


def _interval(resp: dict[str, Any], current: float) -> float:
    value = resp.get("heartbeat_interval_seconds")
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return current
    return float(min(MAX_INTERVAL, max(MIN_INTERVAL, value)))


def default_task_worker(agent: Agent) -> TaskWorker:
    """任务线程使用自己的客户端（长轮询不占用心跳的连接，停止时互不影响）。"""
    cfg = agent.config
    assert cfg.server_url is not None
    return TaskWorker(
        client=Client(cfg.server_url, cfg.node_token),
        node_key=agent.node_key,
        registration=agent,
        state_dir=cfg.state_dir,
        node_name=cfg.node_name,
        export_timeout=cfg.export_timeout,
        inference_image_timeout=cfg.inference_image_timeout,
    )


class Agent:
    def __init__(
        self,
        config: Config,
        *,
        client: Client | None = None,
        probe_hardware: Callable[[], hardware_mod.HardwareProbe] | None = None,
        detector: Detector | None = None,
        sleep: Callable[[float], None] | None = None,
        clock: Callable[[], float] = time.monotonic,
        wall_clock: Callable[[], float] = time.time,
        rng: random.Random | None = None,
        node_key: str | None = None,
        hostname: str | None = None,
        task_worker: Callable[[Agent], TaskWorker | None] | None = None,
    ) -> None:
        if not config.server_url:
            raise ValueError("server_url is required")
        self.config = config
        self.client = client or Client(config.server_url, config.node_token)
        self._probe_hardware = probe_hardware or hardware_mod.probe
        self.deployment = deployment_from(config)
        self.detector: Detector = detector or EnvironmentDetector(
            state_dir=config.state_dir,
            torch_variant=config.torch_variant,
            self_test=config.self_test,
            timeout=config.self_test_timeout,
            deployment=self.deployment,
        )
        self._sleep = sleep
        self._clock = clock
        self._wall_clock = wall_clock
        self._backoff = Backoff(rng=rng)
        self.node_key = node_key or node_key_mod.load_or_create(config.state_dir)
        self.hostname = hostname if hostname is not None else socket.gethostname()

        self._stop = threading.Event()
        self._lock = threading.Lock()
        self._probe: hardware_mod.HardwareProbe | None = None
        self._metrics: hardware_mod.MetricsCollector | None = None
        self._detect_thread: threading.Thread | None = None
        self._pending_environment: dict[str, Any] | None = None
        self._pending_hardware: dict[str, Any] | None = None
        self._last_environment: dict[str, Any] | None = None
        self._task_worker_factory = task_worker or default_task_worker
        self.tasks: TaskWorker | None = None

        # registered 与 epoch（每次注册成功加一）由 _registration 保护：任务线程据此等待注册，
        # 收到 unknown_node 时请主循环重新注册（registration_lost）。
        self._registration = threading.Condition()
        self._epoch = 0
        self.registered = False
        self.node_id: Any = None
        self.interval = DEFAULT_INTERVAL
        self._next_heartbeat = 0.0
        self._connected: bool | None = None
        self._last_error: tuple[Any, ...] | None = None
        self._heartbeat_file_error = False

    # ---- 生命周期 ----

    def stop(self) -> None:
        """请求停止（可从信号处理函数或其它线程调用）：主循环尽快退出，正在运行的检测子进程与
        转换子进程被终止。"""
        self._stop.set()
        try:
            self.detector.cancel()
        except Exception as exc:
            log.debug("cancelling environment detection failed: %s", exc)
        tasks = self.tasks
        if tasks is not None:
            tasks.stop()
        with self._registration:
            self._registration.notify_all()  # 唤醒等待注册的任务线程

    @property
    def stopped(self) -> bool:
        return self._stop.is_set()

    def wait(self, seconds: float) -> bool:
        """等待 seconds 秒或直到停止；返回是否已请求停止。"""
        if seconds > 0:
            if self._sleep is not None:
                self._sleep(seconds)
            else:
                self._stop.wait(seconds)
        return self._stop.is_set()

    def run(self) -> int:
        log.info(
            "trainer %s starting: server=%s name=%s node_key=%s state_dir=%s",
            __version__,
            self.config.server_url,
            self.config.node_name,
            self.node_key,
            self.config.state_dir,
        )
        if self.config.state_dir_warning:
            log.warning("%s", self.config.state_dir_warning)
        try:
            while self._probe is None:
                try:
                    self._probe = self._probe_hardware()
                except Exception:  # 防御：探测本身有容错，这里只防意外错误
                    log.exception("hardware probe failed")
                    if self.wait(self._backoff.next()):
                        return 0
            self._backoff.reset()
            self._metrics = hardware_mod.MetricsCollector(
                self.config.data_dir, self._probe.gpu.sampler
            )
            self._log_hardware(self._probe.hardware)
            self.start_detection(self._probe)
            while not self._stop.is_set():
                try:
                    self._step()
                except Exception:  # 防御：任何意外错误都不应让节点退出
                    log.exception("unexpected error in the agent loop")
                    self.wait(self._backoff.next())
        finally:
            self._shutdown()
        return 0

    def _shutdown(self) -> None:
        try:
            self.detector.cancel()
        except Exception as exc:
            log.debug("cancelling environment detection failed: %s", exc)
        tasks = self.tasks
        if tasks is not None:
            tasks.stop()
            tasks.join()
        thread = self._detect_thread
        if thread is not None and thread.is_alive():
            thread.join(timeout=10)
        if self._probe is not None:
            self._probe.gpu.sampler.close()
        log.info("trainer stopped")

    # ---- 注册状态（任务线程使用）----

    def _set_registered(self, value: bool) -> None:
        with self._registration:
            if value:
                self._epoch += 1
            self.registered = value
            self._registration.notify_all()

    def wait_registered(self, timeout: float) -> int | None:
        """已注册时返回当前 epoch；否则最多等待 timeout 秒。已停止时返回 None。"""
        with self._registration:
            self._registration.wait_for(
                lambda: self.registered or self._stop.is_set(), timeout=timeout
            )
            if self._stop.is_set() or not self.registered:
                return None
            return self._epoch

    def registration_lost(self, epoch: int) -> None:
        """任务接口返回 unknown_node：若 epoch 仍是当前的注册，下一步重新注册（已重新注册过时忽略）。"""
        with self._registration:
            if self.registered and self._epoch == epoch:
                log.info("server does not know this node (unknown_node), registering again")
                self.registered = False

    def _start_tasks(self) -> None:
        if self.tasks is not None or self._stop.is_set():
            return
        try:
            self.tasks = self._task_worker_factory(self)
        except Exception:  # 防御：任务线程出错不应影响注册与心跳
            log.exception("cannot create the task worker")
            self._task_worker_factory = lambda agent: None
            return
        if self.tasks is not None:
            self.tasks.start()

    # ---- 环境检测 ----

    @property
    def detecting(self) -> bool:
        thread = self._detect_thread
        return thread is not None and thread.is_alive()

    def start_detection(self, probe: hardware_mod.HardwareProbe) -> bool:
        with self._lock:
            if self._detect_thread is not None and self._detect_thread.is_alive():
                return False
            thread = threading.Thread(
                target=self._run_detection, args=(probe,), name="environment-detection", daemon=True
            )
            self._detect_thread = thread
        thread.start()
        return True

    def _run_detection(self, probe: hardware_mod.HardwareProbe) -> None:
        started = time.monotonic()
        log.info("detecting CUDA / PyTorch environment")
        try:
            env = self.detector.detect(
                probe.hardware,
                device_nodes=probe.device_nodes,
                pci_gpu_count=probe.pci_gpu_count,
                host_driver_version=probe.host_driver_version,
            )
        except Exception as exc:
            log.exception("environment detection failed")
            # 诊断信息同样交给它：否则如宿主机驱动过旧时会改报“显卡没有传入容器”（建议安装 Toolkit）。
            env = issues_mod.build_environment(
                probe.hardware,
                failure_raw(f"环境检测出错：{type(exc).__name__}: {exc}"),
                torch_variant=self.config.torch_variant,
                probed_at=utc_now(),
                device_nodes=probe.device_nodes,
                pci_gpu_count=probe.pci_gpu_count,
                host_driver_version=probe.host_driver_version,
                deployment=self.deployment,
            )
        if self._stop.is_set():
            return
        with self._lock:
            self._pending_environment = env
            self._last_environment = env
        log.info(
            "environment detection finished in %.1fs: status=%s accelerator=%s torch=%s cuda=%s self_test=%s",
            time.monotonic() - started,
            env.get("status"),
            env.get("accelerator"),
            env.get("torch_version"),
            env.get("torch_cuda_version"),
            (env.get("self_test") or {}).get("status"),
        )
        for item in env.get("issues") or []:
            if item.get("level") in ("error", "warning"):
                log.warning("environment %s [%s]: %s", item["level"], item["code"], item["message"])

    # ---- 与服务端通信 ----

    def _step(self) -> None:
        if not self.registered:
            self._register()
            return
        delay = self._next_heartbeat - self._clock()
        if delay > 0 and self.wait(delay):
            return
        self._heartbeat()

    def _collect_metrics(self) -> dict[str, Any] | None:
        if self._metrics is None:
            return None
        try:
            return self._metrics.collect()
        except Exception:  # 指标采集失败时仍发送心跳
            log.exception("collecting metrics failed")
            return None

    def _register(self) -> None:
        assert self._probe is not None
        with self._lock:
            environment = self._pending_environment or self._last_environment
        payload = {
            "node_key": self.node_key,
            "name": self.config.node_name,
            "hostname": self.hostname,
            "agent_version": __version__,
            "hardware": self._probe.hardware,
            "environment": environment,
            "metrics": self._collect_metrics(),
        }
        try:
            resp = self.client.register(payload)
        except (ApiError, TransportError) as exc:
            self._on_failure("register", exc)
            self.wait(self._backoff.next())
            return

        self._set_registered(True)
        self.node_id = resp.get("node_id")
        self.interval = _interval(resp, self.interval)
        self._backoff.reset()
        with self._lock:
            if environment is not None and self._pending_environment is environment:
                self._pending_environment = None
            self._pending_hardware = None
        log.info(
            "registered with %s as node %s (heartbeat every %gs)",
            self.config.server_url,
            self.node_id,
            self.interval,
        )
        self._connected = True
        self._last_error = None
        self._write_heartbeat_file()
        self._next_heartbeat = self._clock() + self.interval
        self._start_tasks()

    def _heartbeat(self) -> None:
        with self._lock:
            environment = self._pending_environment
            hardware = self._pending_hardware
        detecting_at_send = self.detecting
        payload = {
            "node_key": self.node_key,
            "metrics": self._collect_metrics(),
            "environment": environment,
            "hardware": hardware,
        }
        self._next_heartbeat = self._clock() + self.interval
        try:
            resp = self.client.heartbeat(payload)
        except ApiError as exc:
            if exc.status == 404 and exc.code == "unknown_node":
                log.info("server does not know this node (unknown_node), registering again")
                self._set_registered(False)
                return
            self._on_failure("heartbeat", exc)
            self._next_heartbeat = self._clock() + self._backoff.next()
            return
        except TransportError as exc:
            self._on_failure("heartbeat", exc)
            self._next_heartbeat = self._clock() + self._backoff.next()
            return

        if self._connected is False:
            log.info("connection to %s restored", self.config.server_url)
        self._connected = True
        self._last_error = None
        self._backoff.reset()
        with self._lock:
            if environment is not None and self._pending_environment is environment:
                self._pending_environment = None
            if hardware is not None and self._pending_hardware is hardware:
                self._pending_hardware = None
        new_interval = _interval(resp, self.interval)
        if new_interval != self.interval:
            log.info("heartbeat interval changed to %gs", new_interval)
            self.interval = new_interval
            self._next_heartbeat = self._clock() + self.interval
        self._write_heartbeat_file()

        # 检测结果一旦上报就会清除服务端的检测请求。以下情况忽略 probe_requested，避免重复检测：
        # 这次心跳带着检测结果；发送时检测正在进行（结果随后上报）；检测结果已经产生但还没有上报。
        if resp.get("probe_requested") is True and environment is None and not detecting_at_send:
            with self._lock:
                unsent = self._pending_environment is not None
            if not unsent and not self.detecting:
                log.info("server requested a new hardware and environment probe")
                self.reprobe()

    def reprobe(self) -> None:
        """重新探测硬件（下一次心跳上报）并在后台重新检测 CUDA 环境。"""
        old = self._probe
        try:
            probe = self._probe_hardware()
        except Exception:
            log.exception("hardware probe failed")
            return
        self._probe = probe
        if self._metrics is not None:
            self._metrics.sampler = probe.gpu.sampler
        if old is not None and old.gpu.sampler is not probe.gpu.sampler:
            old.gpu.sampler.close()
        with self._lock:
            self._pending_hardware = probe.hardware
        self._log_hardware(probe.hardware)
        self.start_detection(probe)

    def _on_failure(self, phase: str, exc: ApiError | TransportError) -> None:
        if isinstance(exc, ApiError):
            key: tuple[Any, ...] = (phase, exc.status, exc.code)
        else:
            key = (phase, "transport")
        repeated = self._connected is False and key == self._last_error
        was_connected = self._connected is True
        self._connected = False
        self._last_error = key
        if repeated:
            log.debug("%s failed again: %s", phase, exc)
            return

        url = self.config.server_url
        if isinstance(exc, TransportError):
            if was_connected:
                log.warning("lost connection to %s: %s; retrying with backoff", url, exc)
            else:
                log.warning("cannot reach %s: %s; retrying with backoff", url, exc)
        elif exc.status == 401:
            log.error(
                "server rejected the node token (HTTP 401 %s: %s); set TM_NODE_TOKEN to the value of "
                "TM_NODE_TOKEN in the platform's .env and restart the trainer; retrying with backoff",
                exc.code,
                exc.message,
            )
        elif exc.status == 404 and phase == "register":
            log.error(
                "%s does not provide the compute node API (HTTP 404 %s: %s); check TM_SERVER_URL "
                "and upgrade the platform; retrying with backoff",
                url,
                exc.code,
                exc.message,
            )
        elif 400 <= exc.status < 500:
            log.error(
                "server rejected the %s request (HTTP %s %s: %s); retrying with backoff",
                phase,
                exc.status,
                exc.code,
                exc.message,
            )
        else:
            log.warning(
                "server error during %s (HTTP %s %s: %s); retrying with backoff",
                phase,
                exc.status,
                exc.code,
                exc.message,
            )

    def _write_heartbeat_file(self) -> None:
        data = {
            "time": utc_now(),
            "unix": self._wall_clock(),
            "interval_seconds": self.interval,
            "node_id": self.node_id,
        }
        try:
            node_key_mod.atomic_write_text(
                self.config.state_dir / HEARTBEAT_FILE, json.dumps(data) + "\n"
            )
        except OSError as exc:
            if not self._heartbeat_file_error:
                log.warning("cannot write heartbeat file in %s: %s", self.config.state_dir, exc)
            self._heartbeat_file_error = True
        else:
            self._heartbeat_file_error = False

    @staticmethod
    def _log_hardware(hw: dict[str, Any]) -> None:
        cpu = hw.get("cpu") or {}
        gpus = hw.get("gpus") or []
        detection = hw.get("gpu_detection") or {}
        log.info(
            "hardware: cpu=%s (%s cores), memory=%s bytes, gpus=%d via %s%s",
            cpu.get("model"),
            cpu.get("logical_cores"),
            (hw.get("memory") or {}).get("total_bytes"),
            len(gpus),
            detection.get("method"),
            f" (error: {detection['error']})" if detection.get("error") else "",
        )
        for g in gpus:
            log.info(
                "gpu %s: %s, cc %s, %s bytes",
                g.get("index"),
                g.get("name"),
                g.get("compute_capability"),
                g.get("memory_total_bytes"),
            )


def check_heartbeat(state_dirs: Iterable[Path], now: float | None = None) -> tuple[bool, str]:
    """healthcheck：最近一次成功心跳是否在 3 × 心跳间隔 + 30 秒内。返回 (是否健康, 中文说明)。"""
    now = time.time() if now is None else now
    newest: tuple[float, float] | None = None
    for d in state_dirs:
        try:
            data = json.loads((d / HEARTBEAT_FILE).read_text(encoding="utf-8"))
            unix = float(data["unix"])
            interval = float(data.get("interval_seconds") or DEFAULT_INTERVAL)
        except (OSError, ValueError, KeyError, TypeError):
            continue
        if newest is None or unix > newest[0]:
            newest = (unix, interval)
    if newest is None:
        return False, "还没有成功连接过平台（没有心跳记录）"
    unix, interval = newest
    age = max(0.0, now - unix)
    limit = 3 * interval + HEALTH_GRACE_SECONDS
    if age <= limit:
        return True, f"最近一次心跳在 {age:.0f} 秒前"
    return False, f"最近一次成功心跳在 {age:.0f} 秒前（超过 {limit:.0f} 秒），无法连接平台"
