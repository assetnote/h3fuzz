#!/usr/bin/env python3
"""Create immutable B0/B1 baseline manifests for PERF-004 decisions."""

from __future__ import annotations

import argparse
import json
import math
from pathlib import Path
import statistics
import sys
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
from perf004_contract import (  # noqa: E402
    ContractError,
    add_manifest_hash,
    atomic_write_json,
    comparison_key_errors,
    file_sha256,
    read_json,
    summarize_numbers,
    utc_now,
    verify_manifest_hash,
)


def _rate(report: dict[str, Any]) -> float | None:
    value = report.get("completed_response_rps")
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    value = float(value)
    return value if math.isfinite(value) and value > 0 else None


def evaluate_baseline(label: str, reports: list[dict[str, Any]]) -> dict[str, Any]:
    if label not in {"B0", "B1"}:
        raise ContractError("baseline label must be B0 or B1")
    reasons: list[str] = []
    if len(reports) < 5:
        reasons.append("requires_at_least_five_reports")
    rates: list[float] = []
    for index, report in enumerate(reports, 1):
        if report.get("aggregation_mode") != "baseline":
            reasons.append(f"run_{index:02d}:not_baseline_mode")
        if report.get("run_gates_passed") is not True:
            reasons.append(f"run_{index:02d}:run_gates_failed")
        if report.get("eligible_for_aggregation") is not True:
            reasons.append(f"run_{index:02d}:not_eligible_for_aggregation")
        for key_error in comparison_key_errors(report.get("comparison_keys")):
            reasons.append(f"run_{index:02d}:comparison_keys:{key_error}")
        span = report.get("main_request_span_seconds")
        if isinstance(span, bool) or not isinstance(span, (int, float)) or float(span) < 30.0:
            reasons.append(f"run_{index:02d}:short_duration")
        rate = _rate(report)
        if rate is None:
            reasons.append(f"run_{index:02d}:invalid_completed_response_rps")
        else:
            rates.append(rate)
    cohorts = [report.get("comparison_keys", {}).get("cohort_key") for report in reports]
    if cohorts and len(set(cohorts)) != 1:
        reasons.append("mixed_cohort_key")
    median_rate = statistics.median(rates) if rates else None
    if median_rate is None or median_rate <= 0:
        reasons.append("positive_median_required")
        mad = None
        noise = None
    else:
        deviations = [abs(value - median_rate) for value in rates]
        mad = statistics.median(deviations)
        noise = 100.0 * 1.4826 * mad / median_rate
    reasons = list(dict.fromkeys(reasons))
    summary = summarize_numbers(rates)
    return {
        "schema_version": 1,
        "manifest_type": "h3fuzz-baseline",
        "baseline": label,
        "run_count": len(reports),
        "control_window_rates": rates,
        "median_completed_response_rps": median_rate,
        "median_absolute_deviation_completed_response_rps": mad,
        "minimum_completed_response_rps": summary["minimum"],
        "maximum_completed_response_rps": summary["maximum"],
        "standard_deviation_completed_response_rps": summary["standard_deviation"],
        "noise_band_percent": noise,
        "noise_band_formula": "100 * 1.4826 * MAD(completed_response_rps) / median(completed_response_rps)",
        "minimum_improvement_percent": max(3.0, noise) if noise is not None else None,
        "platform_key": reports[0].get("comparison_keys", {}).get("platform_key") if reports else None,
        "client_build_key": reports[0].get("comparison_keys", {}).get("client_build_key") if reports else None,
        "workload_key": reports[0].get("comparison_keys", {}).get("workload_key") if reports else None,
        "treatment_key": reports[0].get("comparison_keys", {}).get("treatment_key") if reports else None,
        "cohort_key": cohorts[0] if cohorts and len(set(cohorts)) == 1 else None,
        "eligible": not reasons,
        "ineligible_reasons": reasons,
    }


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--baseline", choices=("B0", "B1"), required=True)
    parser.add_argument("--report", type=Path, action="append", required=True)
    parser.add_argument("--b0-manifest", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--require-eligible", action="store_true")
    return parser.parse_args(argv)


def run_cli(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(argv)
        reports = [read_json(path) for path in args.report]
        result = evaluate_baseline(args.baseline, reports)
        if args.baseline == "B1":
            if args.b0_manifest is None:
                raise ContractError("B1 requires --b0-manifest")
            b0 = read_json(args.b0_manifest)
            if b0.get("manifest_type") != "h3fuzz-baseline" or b0.get("baseline") != "B0" or b0.get("eligible") is not True:
                raise ContractError("--b0-manifest must name an eligible retained B0 manifest")
            verify_manifest_hash(b0, str(args.b0_manifest))
            for field in ("platform_key", "workload_key"):
                if result[field] != b0.get(field):
                    result["ineligible_reasons"].append(f"b0:{field}_mismatch")
            if result["client_build_key"] == b0.get("client_build_key"):
                result["ineligible_reasons"].append("b0:client_build_key_not_distinct")
            result["eligible"] = not result["ineligible_reasons"]
            result["retained_b0"] = {
                "path": str(args.b0_manifest.resolve()),
                "sha256": file_sha256(args.b0_manifest),
                "noise_band_percent": b0.get("noise_band_percent"),
                "minimum_improvement_percent": b0.get("minimum_improvement_percent"),
            }
        elif args.b0_manifest is not None:
            raise ContractError("B0 does not accept --b0-manifest")
        result["created_at"] = utc_now()
        result["reports"] = [
            {"path": str(path.resolve()), "sha256": file_sha256(path)} for path in args.report
        ]
        result = add_manifest_hash(result)
        atomic_write_json(args.output, result)
        print(json.dumps(result, sort_keys=True))
        if args.require_eligible and not result["eligible"]:
            return 3
        return 0
    except ContractError as exc:
        print(f"baseline-report: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:
        print(f"baseline-report: unexpected error: {exc}", file=sys.stderr)
        return 1


def main() -> int:
    return run_cli()


if __name__ == "__main__":
    raise SystemExit(main())
