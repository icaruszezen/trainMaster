#!/usr/bin/env python3
"""Executable opaque-box End-to-End (E2E) test runner for trainMaster storage optimization.

Usage examples:
  python3 e2e/run_e2e.py --list-tests
  python3 e2e/run_e2e.py --validate-harness
  python3 e2e/run_e2e.py --tier 1 --feature F1
  python3 e2e/run_e2e.py --tier all --json-report /tmp/e2e_report.json
"""

from __future__ import annotations

import argparse
import glob
import json
from pathlib import Path
import sys
import time
import traceback

REPO_ROOT = Path(__file__).resolve().parent.parent
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from e2e.harness.cluster import CaseContext, E2ESession  # noqa: E402
from e2e.harness.http_client import enforce_no_proxy_env  # noqa: E402
from e2e.harness.registry import (  # noqa: E402
    FEATURES,
    all_tests,
    select_tests,
    summarize_counts,
    verify_coverage_thresholds,
)
from e2e.harness.validate import run_harness_validation  # noqa: E402
import e2e.tests  # noqa: E402,F401


def _print_list_tests(tier: str, features: list[str] | None, name_filter: str | None) -> int:
    problems = verify_coverage_thresholds()
    selected = select_tests(tier=tier, features=features, name_filter=name_filter)
    full_summary = summarize_counts(all_tests())
    sel_summary = summarize_counts(selected)

    print("=" * 94)
    print("trainMaster Opaque-Box 4-Tier E2E Test Suite — Catalog & Coverage Matrix")
    print("=" * 94)
    matrix: dict[int, dict[str, int]] = full_summary["tier_feature_matrix"]  # type: ignore[assignment]
    by_tier: dict[int, int] = full_summary["by_tier"]  # type: ignore[assignment]

    print(f"{'Feature':<8} {'Description':<58} {'T1':>4} {'T2':>4} {'T3':>4} {'T4':>4}")
    print("-" * 94)
    for feat, desc in FEATURES.items():
        short_desc = desc if len(desc) <= 56 else desc[:53] + "..."
        print(
            f"{feat:<8} {short_desc:<58} "
            f"{matrix[1][feat]:>4} {matrix[2][feat]:>4} {matrix[3][feat]:>4} {matrix[4][feat]:>4}"
        )
    print("-" * 94)
    print(
        f"Registered Totals: Tier 1 = {by_tier[1]} | Tier 2 = {by_tier[2]} | "
        f"Tier 3 = {by_tier[3]} | Tier 4 = {by_tier[4]} | Grand Total = {full_summary['total']}"
    )
    print(f"Selected for filter (tier={tier}, feature={features}): {sel_summary['total']} tests")
    print("=" * 94)
    for spec in selected:
        print(f"[{spec.test_id:<10}] Tier {spec.tier} | {spec.feature_label:<24} | {spec.title}")

    if problems:
        print("\nCOVERAGE THRESHOLD VIOLATIONS:", file=sys.stderr)
        for p in problems:
            print(f"  - {p}", file=sys.stderr)
        return 1
    print("\nCoverage Threshold Verification: PASS (all Tier 1-4 & F1-F12 minimums satisfied)")
    return 0


def _run_selected_tests(
    tier: str,
    features: list[str] | None,
    name_filter: str | None,
    json_report_path: str | None,
) -> int:
    enforce_no_proxy_env()
    problems = verify_coverage_thresholds()
    if problems:
        for p in problems:
            print(f"[THRESHOLD ERROR] {p}", file=sys.stderr)
        return 2

    selected = select_tests(tier=tier, features=features, name_filter=name_filter)
    if not selected:
        print("No E2E tests matched the given --tier / --feature / --filter arguments.", file=sys.stderr)
        return 2

    before_tmp = set(glob.glob("/tmp/tm_e2e_session_*"))
    session = E2ESession()
    results: list[dict[str, object]] = []
    passed = 0
    failed = 0
    started_all = time.monotonic()

    print(f"Running {len(selected)} opaque-box E2E test(s) (scratch: {session.root_dir})...")
    try:
        for idx, spec in enumerate(selected, start=1):
            ctx = CaseContext(session, spec.test_id)
            t0 = time.monotonic()
            status = "passed"
            err_msg = ""
            err_tb = ""
            try:
                spec.fn(ctx)
                passed += 1
                dur_ms = round((time.monotonic() - t0) * 1000, 1)
                print(f"  [{idx:>3}/{len(selected)}] PASS {spec.test_id:<10} ({dur_ms:>7.1f} ms) {spec.title}")
            except BaseException as exc:  # noqa: BLE001
                failed += 1
                status = "failed"
                err_msg = f"{type(exc).__name__}: {exc}"
                err_tb = traceback.format_exc()
                dur_ms = round((time.monotonic() - t0) * 1000, 1)
                print(f"  [{idx:>3}/{len(selected)}] FAIL {spec.test_id:<10} ({dur_ms:>7.1f} ms) {spec.title}")
                print(f"        -> {err_msg}")
            finally:
                ctx.close()

            results.append(
                {
                    "test_id": spec.test_id,
                    "tier": spec.tier,
                    "features": list(spec.features),
                    "title": spec.title,
                    "status": status,
                    "duration_ms": dur_ms,
                    "error": err_msg,
                    "traceback": err_tb,
                }
            )
    finally:
        session.close()

    leaked = sorted(set(glob.glob("/tmp/tm_e2e_session_*")) - before_tmp)
    total_s = round(time.monotonic() - started_all, 2)
    summary_payload = {
        "total": len(selected),
        "passed": passed,
        "failed": failed,
        "duration_seconds": total_s,
        "leaked_session_dirs": leaked,
        "results": results,
    }
    if json_report_path:
        out_p = Path(json_report_path)
        out_p.parent.mkdir(parents=True, exist_ok=True)
        out_p.write_text(json.dumps(summary_payload, ensure_ascii=False, indent=2), encoding="utf-8")

    print("-" * 88)
    print(f"Summary: {passed}/{len(selected)} passed, {failed} failed in {total_s:.2f}s (leaked_tmp={len(leaked)})")
    if leaked:
        print(f"ERROR: Leaked /tmp directories detected: {leaked}", file=sys.stderr)
        return 1
    return 0 if failed == 0 else 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="trainMaster 4-Tier Opaque-Box E2E Test Suite Runner")
    parser.add_argument(
        "--tier",
        choices=("1", "2", "3", "4", "all"),
        default="all",
        help="Test tier to execute or list (1, 2, 3, 4, or all)",
    )
    parser.add_argument(
        "--feature",
        action="append",
        dest="features",
        help="Filter by feature ID F1..F12 (repeatable or comma-separated, e.g. --feature F1,F7)",
    )
    parser.add_argument(
        "--filter",
        dest="name_filter",
        default=None,
        help="Optional substring filter on test_id, function name, or title",
    )
    parser.add_argument(
        "--list-tests",
        action="store_true",
        help="List all registered E2E test cases, print coverage matrix, verify counts, and exit 0",
    )
    parser.add_argument(
        "--validate-harness",
        action="store_true",
        help="Self-test ephemeral PG15, S3 mock, HTTP spy proxy, archive fixtures, and zero-leak cleanup",
    )
    parser.add_argument(
        "--json-report",
        default=None,
        help="Optional path to write structured JSON report",
    )
    args = parser.parse_args(argv)

    if args.list_tests:
        return _print_list_tests(args.tier, args.features, args.name_filter)

    if args.validate_harness:
        report = run_harness_validation()
        print(json.dumps(report, ensure_ascii=False, indent=2))
        if args.json_report:
            Path(args.json_report).write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
        return 0

    return _run_selected_tests(args.tier, args.features, args.name_filter, args.json_report)


if __name__ == "__main__":
    raise SystemExit(main())
