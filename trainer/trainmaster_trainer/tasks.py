"""任务线程：注册成功后向平台长轮询领取任务并执行。任务有两类：model_export（模型仓库的 pt → ONNX 转换，
model_export.py）与 review_inference（用模型识别审图任务的图片，review_inference.py）。

- 能力：本机能找到每类任务需要的模块（REQUIRED_MODULES；importlib.util.find_spec 检查，不导入）时才在领取时
  带上这类任务：model_export 需要 torch、ultralytics 与 onnx，review_inference 需要 torch 与 ultralytics。
  缺少时记录一次 INFO 日志，之后每 5 分钟重新检查（裸机节点可能随后安装了依赖）。
- 领取：POST /api/v1/compute/agent/tasks/claim（wait_seconds=25，客户端超时 = 等待时间 + 15 秒）；
  没有任务时立即再领。404 unknown_node（节点记录被删除）时请主循环重新注册并等待；其它 404 / 405
  （旧版平台没有任务接口）时每 5 分钟重试一次，只记录一次日志；网络错误与 5xx 按心跳相同的指数退避。
- 一次只执行一个任务（按 kind 交给 ModelExportTask 或 ReviewInferenceTask）。停止（Agent.stop）时结束
  正在运行的子进程，向平台上报可重试的失败（任务立即重新排队），不等待正在进行的长轮询（线程是守护线程，
  进程退出时随之结束；万一领到了任务，租约过期后平台会重新排队）。
"""

from __future__ import annotations

import importlib.util
import logging
import random
import threading
import time
from collections.abc import Callable, Mapping, Sequence
from pathlib import Path
from typing import Any, Protocol

from . import __version__
from . import model_export as model_export_mod
from . import review_inference as review_inference_mod
from .backoff import Backoff
from .client import ApiError, Client, TransportError
from .config import DEFAULT_INFERENCE_IMAGE_TIMEOUT

log = logging.getLogger(__name__)

CLAIM_PATH = "/api/v1/compute/agent/tasks/claim"
CLAIM_WAIT = 25
CLAIM_TIMEOUT_MARGIN = 15.0
# 服务端没有等待就返回了空结果（如正在关闭）时，下一次领取前至少间隔这么久，避免空转。
MIN_CLAIM_INTERVAL = 1.0
UNSUPPORTED_RETRY = 300.0
CAPABILITY_RECHECK = 300.0
REGISTRATION_POLL = 1.0
STOP_JOIN_TIMEOUT = 15.0
# 执行各类任务需要能导入的模块（只检查，不导入：常驻进程不导入 torch）。
REQUIRED_MODULES: dict[str, tuple[str, ...]] = {
    model_export_mod.KIND: ("torch", "ultralytics", "onnx"),
    review_inference_mod.KIND: ("torch", "ultralytics"),
}
# 各类任务在状态目录 work/ 中的工作目录；任务线程启动时删除上次运行（进程崩溃、被强制结束）的残留。
WORK_DIR_PATTERNS = ("model-export-*", f"{review_inference_mod.WORK_DIR_PREFIX}*")


class Runner(Protocol):
    """一个正在执行的任务（ModelExportTask、ReviewInferenceTask）。"""

    def run(self) -> str: ...

    def cancel(self) -> None: ...


class Registration(Protocol):
    """主循环的注册状态（Agent 实现）。epoch 在每次注册成功时加一。"""

    def wait_registered(self, timeout: float) -> int | None:
        """已注册时立即返回当前 epoch；否则最多等待 timeout 秒，仍未注册（或已停止）时返回 None。"""
        ...

    def registration_lost(self, epoch: int) -> None:
        """任务接口返回 unknown_node：epoch 仍是当前的注册时，请主循环重新注册。"""
        ...


def missing_modules(
    names: Sequence[str], find_spec: Callable[[str], Any] = importlib.util.find_spec
) -> list[str]:
    """names 中找不到的顶层模块（不导入它们）。"""
    importlib.invalidate_caches()  # 进程运行期间新安装的包也能找到
    missing = []
    for name in names:
        try:
            found = find_spec(name) is not None
        except (ImportError, ValueError):  # 父包导入失败、__spec__ 为 None 等
            found = False
        if not found:
            missing.append(name)
    return missing


class TaskWorker:
    """后台线程：领取并执行任务。start() / stop() / join() 可从主循环调用。"""

    def __init__(
        self,
        *,
        client: Client,
        node_key: str,
        registration: Registration,
        state_dir: Path,
        node_name: str,
        export_timeout: float,
        find_spec: Callable[[str], Any] = importlib.util.find_spec,
        wait: Callable[[float], bool] | None = None,
        clock: Callable[[], float] = time.monotonic,
        rng: random.Random | None = None,
        export_command: Sequence[str] | None = None,
        progress_interval: float = model_export_mod.PROGRESS_INTERVAL,
        inference_image_timeout: float = DEFAULT_INFERENCE_IMAGE_TIMEOUT,
        inference_command: Sequence[str] | None = None,
    ) -> None:
        self.client = client
        self.node_key = node_key
        self.registration = registration
        self.state_dir = state_dir
        self.node_name = node_name
        self.export_timeout = export_timeout
        self.export_command = list(export_command) if export_command else None
        self.progress_interval = progress_interval
        self.inference_image_timeout = inference_image_timeout
        self.inference_command = list(inference_command) if inference_command else None
        self._find_spec = find_spec
        self._wait_fn = wait
        self._clock = clock
        self._backoff = Backoff(rng=rng)

        self._stop = threading.Event()
        self._lock = threading.Lock()
        self._thread: threading.Thread | None = None
        self._current: Runner | None = None

        self._kinds: list[str] | None = None
        self._kinds_checked_at: float | None = None
        self._unsupported = False  # 平台没有任务接口（已记录日志）
        self._last_error: tuple[Any, ...] | None = None
        self.tasks_run = 0

    # ---- 生命周期 ----

    def start(self) -> bool:
        with self._lock:
            if self._thread is not None:
                return False
            self._thread = threading.Thread(target=self._run, name="task-worker", daemon=True)
        self._thread.start()
        return True

    def stop(self) -> None:
        """请求停止：正在执行的任务被中断（结束它的子进程），线程随后退出。"""
        self._stop.set()
        with self._lock:
            current = self._current
        if current is not None:
            current.cancel()

    def join(self, timeout: float = STOP_JOIN_TIMEOUT) -> None:
        """等待线程退出。只在执行任务时等待（上报失败、清理工作目录）；空闲时线程可能停在长轮询中，不等它。"""
        thread = self._thread
        if thread is None or not thread.is_alive():
            return
        thread.join(timeout if self.busy else min(timeout, 0.2))

    @property
    def alive(self) -> bool:
        thread = self._thread
        return thread is not None and thread.is_alive()

    @property
    def busy(self) -> bool:
        with self._lock:
            return self._current is not None

    @property
    def stopped(self) -> bool:
        return self._stop.is_set()

    def _wait(self, seconds: float) -> bool:
        """等待 seconds 秒或直到停止；返回是否已请求停止。"""
        if seconds > 0:
            if self._wait_fn is not None:
                self._wait_fn(seconds)
            else:
                self._stop.wait(seconds)
        return self._stop.is_set()

    # ---- 主循环 ----

    def _run(self) -> None:
        log.debug("task worker started")
        model_export_mod.clean_work_dirs(self.state_dir, WORK_DIR_PATTERNS)
        while not self._stop.is_set():
            try:
                self._step()
            except Exception:  # 防御：任何意外错误都不应让任务线程退出
                log.exception("unexpected error in the task worker")
                self._wait(self._backoff.next())
        log.debug("task worker stopped")

    def kinds(self) -> list[str]:
        """本机能执行的任务类型；每 5 分钟重新检查一次。"""
        now = self._clock()
        checked = self._kinds_checked_at
        if self._kinds is not None and checked is not None and now - checked < CAPABILITY_RECHECK:
            return self._kinds
        kinds = []
        for kind, modules in REQUIRED_MODULES.items():
            missing = missing_modules(modules, self._find_spec)
            if not missing:
                kinds.append(kind)
                if self._kinds is not None and kind not in self._kinds:
                    log.info("%s tasks enabled: %s can be imported now", kind, ", ".join(modules))
            elif self._kinds is None or kind in self._kinds:
                log.info(
                    "not claiming %s tasks: cannot find %s (install the trainer requirements, "
                    "see trainer/requirements.txt)",
                    kind,
                    ", ".join(missing),
                )
        self._kinds = kinds
        self._kinds_checked_at = now
        return kinds

    def _step(self) -> None:
        epoch = self.registration.wait_registered(REGISTRATION_POLL)
        if epoch is None or self._stop.is_set():
            return
        kinds = self.kinds()
        if not kinds:
            self._wait(CAPABILITY_RECHECK)
            return

        started = self._clock()
        payload = {"node_key": self.node_key, "kinds": kinds, "wait_seconds": CLAIM_WAIT}
        try:
            resp = self.client.post(CLAIM_PATH, payload, timeout=CLAIM_WAIT + CLAIM_TIMEOUT_MARGIN)
        except ApiError as exc:
            self._on_claim_error(exc, epoch)
            return
        except TransportError as exc:
            self._on_failure(("transport",), exc)
            self._wait(self._backoff.next())
            return

        if self._unsupported:
            log.info("the platform supports tasks now, claiming %s", ", ".join(kinds))
            self._unsupported = False
        if self._last_error is not None:
            log.info("claiming tasks again")
            self._last_error = None
        self._backoff.reset()

        task = resp.get("task")
        if task is None:
            # 正常情况下服务端最多等待 wait_seconds 才返回空结果；立即返回时（如正在关闭）稍等再领。
            if self._clock() - started < MIN_CLAIM_INTERVAL:
                self._wait(MIN_CLAIM_INTERVAL)
            return
        if not isinstance(task, dict):
            log.warning("ignoring malformed task from the platform: %r", task)
            self._wait(self._backoff.next())
            return
        self._execute(task)

    def _on_claim_error(self, exc: ApiError, epoch: int) -> None:
        if exc.status == 404 and exc.code == "unknown_node":
            log.debug("task claim: server does not know this node, waiting for registration")
            self.registration.registration_lost(epoch)
            self._wait(self._backoff.next())  # 万一注册后仍是 unknown_node，不要频繁重试
            return
        if exc.status in (404, 405, 501):
            if not self._unsupported:
                log.info(
                    "the platform does not support task claims (HTTP %s %s: %s); model conversions "
                    "and review inference on this node need a newer platform; checking again every "
                    "%d minutes",
                    exc.status,
                    exc.code,
                    exc.message,
                    UNSUPPORTED_RETRY // 60,
                )
                self._unsupported = True
            self._wait(UNSUPPORTED_RETRY)
            return
        self._on_failure((exc.status, exc.code), exc)
        self._wait(self._backoff.next())

    def _on_failure(self, key: tuple[Any, ...], exc: ApiError | TransportError) -> None:
        """同样的错误只记录一次。网络错误只记 DEBUG：主循环的心跳已经报告了连接状态。"""
        repeated = key == self._last_error
        self._last_error = key
        if isinstance(exc, TransportError) or repeated:
            log.debug("task claim failed: %s", exc)
            return
        log.warning(
            "task claim failed (HTTP %s %s: %s); retrying with backoff",
            exc.status,
            exc.code,
            exc.message,
        )

    # ---- 执行任务 ----

    def _runner(self, task: Mapping[str, Any]) -> Runner | None:
        """按 kind 创建任务的执行者；不认识的类型为 None。"""
        kind = task.get("kind")
        if kind == model_export_mod.KIND:
            return model_export_mod.ModelExportTask(
                task,
                client=self.client,
                node_key=self.node_key,
                state_dir=self.state_dir,
                node_name=self.node_name,
                timeout=self.export_timeout,
                command=self.export_command,
                progress_interval=self.progress_interval,
            )
        if kind == review_inference_mod.KIND:
            return review_inference_mod.ReviewInferenceTask(
                task,
                client=self.client,
                node_key=self.node_key,
                state_dir=self.state_dir,
                node_name=self.node_name,
                image_timeout=self.inference_image_timeout,
                command=self.inference_command,
                progress_interval=self.progress_interval,
            )
        return None

    def _execute(self, task: Mapping[str, Any]) -> None:
        runner = self._runner(task)
        if runner is None:
            self._reject(task, f"训练节点（{__version__}）不支持该类任务：{task.get('kind')!r}")
            return
        with self._lock:
            self._current = runner
        try:
            if self._stop.is_set():  # 停止前领到的任务：立即交还（重新排队）
                runner.cancel()
            runner.run()
            self.tasks_run += 1
        finally:
            with self._lock:
                self._current = None

    def _reject(self, task: Mapping[str, Any], message: str) -> None:
        log.warning("cannot run task %r of kind %r", task.get("id"), task.get("kind"))
        url = model_export_mod.fail_url_of(task)
        if url is not None:
            model_export_mod.report_failure(
                self.client,
                self.node_key,
                url,
                model_export_mod.TaskFailed(message),
                attempts=1,
            )
