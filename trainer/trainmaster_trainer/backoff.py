"""指数退避：主循环（注册、心跳）与任务线程（领取任务）共用。"""

from __future__ import annotations

import random

BACKOFF_BASE = 1.0
BACKOFF_CAP = 60.0


class Backoff:
    """指数退避：1、2、4 … 秒，最长 cap 秒，每次乘以 0.75–1.25 的随机系数，避免多个节点同时重试。"""

    def __init__(
        self, base: float = BACKOFF_BASE, cap: float = BACKOFF_CAP, rng: random.Random | None = None
    ):
        self.base = base
        self.cap = cap
        self.rng = rng or random.Random()
        self.attempt = 0

    def next(self) -> float:
        delay = min(self.cap, self.base * (2 ** min(self.attempt, 30)))
        self.attempt += 1
        return min(self.cap, delay * (0.75 + 0.5 * self.rng.random()))

    def reset(self) -> None:
        self.attempt = 0
