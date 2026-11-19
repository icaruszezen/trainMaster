"""Test case registry, metadata schema, filtering, and coverage threshold validation."""

from __future__ import annotations

from collections import defaultdict
from dataclasses import dataclass
from typing import Callable

FEATURES: dict[str, str] = {
    "F1": "Standalone Storage Container HTTP Agent (trainmaster-storage)",
    "F2": "Dual-Format Archive Engine (.tar.zst & YOLO .zip)",
    "F3": "Archive Security, Atomic Extraction & TTL Janitor",
    "F4": "Storage Docker Image, Compose Topology & Shell Scripts",
    "F5": "DB Migration 0018 & KindDocker Volume Driver",
    "F6": "Legacy Storage Server Compatibility Matrix Enforcement",
    "F7": "Pack-Transfer-Unpack Online Storage Migration",
    "F8": "Pack-Transfer-Unpack Training Dataset Copy Backend",
    "F9": "Pack-Transfer-Unpack Cross-Node DataVersion Build & Staged Bulk Import",
    "F10": "Python Trainer Checksummed Archive Unpack & Disk Headroom",
    "F11": "Frontend Storage Docker Container Onboarding & Capability Badges",
    "F12": "Frontend Multi-Stage Migration, DataVersion & Training Status UI",
}

VALID_TIERS = (1, 2, 3, 4)


@dataclass(frozen=True)
class TestCaseSpec:
    """Metadata and callable for a single registered opaque-box E2E test."""

    test_id: str
    tier: int
    features: tuple[str, ...]
    title: str
    fn: Callable[..., None]
    module: str

    @property
    def primary_feature(self) -> str:
        return self.features[0]

    @property
    def feature_label(self) -> str:
        return ",".join(self.features)


_REGISTRY: list[TestCaseSpec] = []
_SEEN_IDS: set[str] = set()


def normalize_feature_id(raw: str) -> str:
    val = raw.strip().upper()
    if val.isdigit():
        val = f"F{int(val)}"
    if val not in FEATURES:
        raise ValueError(f"Unknown feature ID {raw!r}; expected one of {list(FEATURES)}")
    return val


def e2e_test(
    *,
    test_id: str,
    tier: int,
    features: str | list[str] | tuple[str, ...],
    title: str,
) -> Callable[[Callable[..., None]], Callable[..., None]]:
    """Register an opaque-box E2E test function with explicit tier & feature metadata."""
    if tier not in VALID_TIERS:
        raise ValueError(f"Invalid tier {tier!r} for {test_id}; expected 1..4")
    if not test_id or not test_id.strip():
        raise ValueError("test_id must be non-empty")
    if test_id in _SEEN_IDS:
        raise ValueError(f"Duplicate E2E test_id registered: {test_id}")

    if isinstance(features, str):
        feat_list = [normalize_feature_id(p) for p in features.split(",") if p.strip()]
    else:
        feat_list = [normalize_feature_id(p) for p in features]
    if not feat_list:
        raise ValueError(f"Test {test_id} must declare at least one feature in F1..F12")
    if tier in (1, 2) and len(feat_list) != 1:
        raise ValueError(f"Tier {tier} test {test_id} must target exactly 1 primary feature, got {feat_list}")

    def decorator(fn: Callable[..., None]) -> Callable[..., None]:
        spec = TestCaseSpec(
            test_id=test_id,
            tier=tier,
            features=tuple(feat_list),
            title=title.strip(),
            fn=fn,
            module=fn.__module__,
        )
        _SEEN_IDS.add(test_id)
        _REGISTRY.append(spec)
        return fn

    return decorator


def all_tests() -> list[TestCaseSpec]:
    return list(_REGISTRY)


def select_tests(
    *,
    tier: str = "all",
    features: list[str] | None = None,
    name_filter: str | None = None,
) -> list[TestCaseSpec]:
    selected = list(_REGISTRY)
    if tier != "all":
        tier_num = int(tier)
        selected = [t for t in selected if t.tier == tier_num]
    if features:
        wanted = {normalize_feature_id(f) for raw in features for f in raw.split(",") if f.strip()}
        selected = [t for t in selected if wanted.intersection(t.features)]
    if name_filter:
        needle = name_filter.lower()
        selected = [
            t
            for t in selected
            if needle in t.test_id.lower()
            or needle in t.title.lower()
            or needle in t.fn.__name__.lower()
        ]
    return selected


def summarize_counts(specs: list[TestCaseSpec] | None = None) -> dict[str, object]:
    items = _REGISTRY if specs is None else specs
    by_tier: dict[int, int] = {1: 0, 2: 0, 3: 0, 4: 0}
    tier_feature_matrix: dict[int, dict[str, int]] = {
        1: {f: 0 for f in FEATURES},
        2: {f: 0 for f in FEATURES},
        3: {f: 0 for f in FEATURES},
        4: {f: 0 for f in FEATURES},
    }
    by_feature_total: dict[str, int] = defaultdict(int)

    for spec in items:
        by_tier[spec.tier] = by_tier.get(spec.tier, 0) + 1
        for feat in spec.features:
            tier_feature_matrix[spec.tier][feat] += 1
            by_feature_total[feat] += 1

    return {
        "total": len(items),
        "by_tier": by_tier,
        "tier_feature_matrix": tier_feature_matrix,
        "by_feature_total": dict(by_feature_total),
    }


def verify_coverage_thresholds() -> list[str]:
    """Validate strict E2E tier and feature test count invariants."""
    summary = summarize_counts(_REGISTRY)
    by_tier: dict[int, int] = summary["by_tier"]  # type: ignore[assignment]
    matrix: dict[int, dict[str, int]] = summary["tier_feature_matrix"]  # type: ignore[assignment]
    total: int = summary["total"]  # type: ignore[assignment]

    problems: list[str] = []
    if by_tier[1] < 60:
        problems.append(f"Tier 1 has {by_tier[1]} tests, expected >= 60")
    if by_tier[2] < 60:
        problems.append(f"Tier 2 has {by_tier[2]} tests, expected >= 60")
    if by_tier[3] < 12:
        problems.append(f"Tier 3 has {by_tier[3]} tests, expected >= 12")
    if by_tier[4] < 6:
        problems.append(f"Tier 4 has {by_tier[4]} tests, expected >= 6")
    if total < 138:
        problems.append(f"Total tests is {total}, expected >= 138")

    for feat in FEATURES:
        if matrix[1][feat] < 5:
            problems.append(f"Tier 1 feature {feat} has {matrix[1][feat]} tests, expected >= 5")
        if matrix[2][feat] < 5:
            problems.append(f"Tier 2 feature {feat} has {matrix[2][feat]} tests, expected >= 5")
        if matrix[3][feat] < 1:
            problems.append(f"Tier 3 feature {feat} has 0 cross-feature tests, expected >= 1")

    return problems
