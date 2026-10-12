#!/usr/bin/env python3
"""Freeze the PERF-501 fixed-sweep policy from retained B0 evidence.

The policy is deliberately create-only.  It recomputes the B0 robust noise
estimate from the hashed source reports instead of trusting copied summary
fields, and contains hashes rather than external paths so it remains portable
when copied into a sweep artifact tree.
"""

from __future__ import annotations

import argparse
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
    utc_now,
    verify_manifest_hash,
)


POLICY_TYPE = "h3fuzz-fixed-sweep-decision-policy"
NOISE_FORMULA = (
    "100 * 1.4826 * MAD(completed_response_rps) / "
    "median(completed_response_rps)"
)
SHA256_FIELDS = (
    "platform_key",
    "client_build_key",
    "workload_key",
    "treatment_key",
    "cohort_key",
)


def finite_positive(value: str) -> float:
    try:
        result = float(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("expected a positive decimal") from exc
    if not math.isfinite(result) or result <= 0:
        raise argparse.ArgumentTypeError("expected a finite positive decimal")
    return result


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--baseline-manifest", type=Path, required=True)
    parser.add_argument("--client-cpu-max-percent", type=finite_positive, required=True)
    parser.add_argument("--server-cpu-max-percent", type=finite_positive, required=True)
    parser.add_argument(
        "--network-utilization-max-percent", type=finite_positive, required=True
    )
    parser.add_argument("--p99-latency-max-ms", type=finite_positive, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args(argv)


def require_absolute(path: Path, label: str, *, exists: bool = False) -> Path:
    if not path.is_absolute():
        raise ContractError(f"{label} must be absolute")
    resolved = path.resolve()
    if exists and not resolved.is_file():
        raise ContractError(f"{label} must name an existing file")
    return resolved


def number(value: object, label: str, *, positive: bool = False) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ContractError(f"{label} must be a finite number")
    result = float(value)
    if not math.isfinite(result) or (positive and result <= 0):
        qualifier = "positive " if positive else ""
        raise ContractError(f"{label} must be a finite {qualifier}number")
    return result


def sha256(value: object, label: str) -> str:
    if (
        not isinstance(value, str)
        or len(value) != 64
        or any(character not in "0123456789abcdef" for character in value)
    ):
        raise ContractError(f"{label} must be a lowercase SHA-256 digest")
    return value


def resolve_report_path(reference: dict[str, Any], baseline_path: Path, index: int) -> Path:
    raw_path = reference.get("path")
    if not isinstance(raw_path, str) or not raw_path:
        raise ContractError(f"baseline reports[{index}].path must be nonempty")
    candidate = Path(raw_path)
    if not candidate.is_absolute():
        candidate = baseline_path.parent / candidate
    candidate = candidate.resolve()
    if not candidate.is_file():
        raise ContractError(f"baseline reports[{index}] is missing: {candidate}")
    return candidate


def validate_b0(path: Path) -> dict[str, Any]:
    manifest = read_json(path)
    if (
        manifest.get("schema_version") != 1
        or manifest.get("manifest_type") != "h3fuzz-baseline"
        or manifest.get("baseline") != "B0"
    ):
        raise ContractError("--baseline-manifest must be a B0 schema-version-1 manifest")
    verify_manifest_hash(manifest, str(path))
    if manifest.get("eligible") is not True or manifest.get("ineligible_reasons") != []:
        raise ContractError("--baseline-manifest must be an eligible retained B0 manifest")

    references = manifest.get("reports")
    rates = manifest.get("control_window_rates")
    run_count = manifest.get("run_count")
    if (
        isinstance(run_count, bool)
        or not isinstance(run_count, int)
        or run_count < 5
        or not isinstance(references, list)
        or not isinstance(rates, list)
        or len(references) != run_count
        or len(rates) != run_count
    ):
        raise ContractError("B0 requires at least five reports and matching control rates")

    expected_keys = {
        field: sha256(manifest.get(field), f"baseline {field}")
        for field in SHA256_FIELDS
    }
    verified_rates: list[float] = []
    source_reports: list[dict[str, Any]] = []
    seen_paths: set[Path] = set()
    for index, reference in enumerate(references):
        if not isinstance(reference, dict) or set(reference) != {"path", "sha256"}:
            raise ContractError(
                f"baseline reports[{index}] must be an exact path/SHA-256 reference"
            )
        expected_hash = sha256(reference.get("sha256"), f"baseline reports[{index}].sha256")
        report_path = resolve_report_path(reference, path, index)
        if report_path in seen_paths:
            raise ContractError(f"baseline reports[{index}] duplicates a source report")
        seen_paths.add(report_path)
        actual_hash = file_sha256(report_path)
        if actual_hash != expected_hash:
            raise ContractError(f"baseline reports[{index}] content hash mismatch")

        report = read_json(report_path)
        if report.get("aggregation_mode") != "baseline":
            raise ContractError(f"baseline reports[{index}] is not baseline mode")
        if report.get("run_gates_passed") is not True:
            raise ContractError(f"baseline reports[{index}] failed its run gates")
        if report.get("eligible_for_aggregation") is not True:
            raise ContractError(f"baseline reports[{index}] is aggregation-ineligible")
        span = number(
            report.get("main_request_span_seconds"),
            f"baseline reports[{index}].main_request_span_seconds",
            positive=True,
        )
        if span < 30.0:
            raise ContractError(f"baseline reports[{index}] is shorter than 30 seconds")
        rate = number(
            report.get("completed_response_rps"),
            f"baseline reports[{index}].completed_response_rps",
            positive=True,
        )
        recorded_rate = number(
            rates[index], f"baseline control_window_rates[{index}]", positive=True
        )
        if not math.isclose(rate, recorded_rate, rel_tol=1e-12, abs_tol=1e-12):
            raise ContractError(f"baseline reports[{index}] rate does not match B0")

        keys = report.get("comparison_keys")
        key_errors = comparison_key_errors(keys)
        if key_errors:
            raise ContractError(
                f"baseline reports[{index}] comparison keys: {','.join(key_errors)}"
            )
        assert isinstance(keys, dict)
        if any(keys.get(field) != value for field, value in expected_keys.items()):
            raise ContractError(f"baseline reports[{index}] layered key mismatch")

        verified_rates.append(rate)
        source_reports.append({"ordinal": index + 1, "sha256": actual_hash})

    median_rate = statistics.median(verified_rates)
    mad = statistics.median(abs(value - median_rate) for value in verified_rates)
    noise = 100.0 * 1.4826 * mad / median_rate
    recorded_median = number(
        manifest.get("median_completed_response_rps"),
        "baseline median_completed_response_rps",
        positive=True,
    )
    recorded_mad = number(
        manifest.get("median_absolute_deviation_completed_response_rps"),
        "baseline median_absolute_deviation_completed_response_rps",
    )
    recorded_noise = number(manifest.get("noise_band_percent"), "baseline noise_band_percent")
    recorded_minimum = number(
        manifest.get("minimum_improvement_percent"),
        "baseline minimum_improvement_percent",
        positive=True,
    )
    expected_values = (
        (recorded_median, median_rate, "median"),
        (recorded_mad, mad, "MAD"),
        (recorded_noise, noise, "noise band"),
        (recorded_minimum, max(3.0, noise), "minimum improvement"),
    )
    for actual, expected, label in expected_values:
        if not math.isclose(actual, expected, rel_tol=1e-12, abs_tol=1e-12):
            raise ContractError(f"B0 {label} does not match the frozen source reports")
    if manifest.get("noise_band_formula") != NOISE_FORMULA:
        raise ContractError("B0 noise-band formula is not the contract-v1 formula")

    return {
        "manifest": manifest,
        "manifest_file_sha256": file_sha256(path),
        "manifest_sha256": sha256(
            manifest.get("manifest_sha256"), "baseline manifest_sha256"
        ),
        "reports": source_reports,
        "rates": verified_rates,
        "median": median_rate,
        "mad": mad,
        "noise": noise,
        "keys": expected_keys,
    }


def create_policy(
    baseline_path: Path,
    *,
    client_cpu_max_percent: float,
    server_cpu_max_percent: float,
    network_utilization_max_percent: float,
    p99_latency_max_ms: float,
) -> dict[str, Any]:
    b0 = validate_b0(baseline_path)
    noise = b0["noise"]
    return add_manifest_hash(
        {
            "schema_version": 1,
            "manifest_type": POLICY_TYPE,
            "created_at": utc_now(),
            "policy_generation": "frozen-b0-mad-v1",
            "source_baseline": {
                "manifest_sha256": b0["manifest_sha256"],
                "file_sha256": b0["manifest_file_sha256"],
                "run_count": len(b0["reports"]),
                "reports": b0["reports"],
            },
            "control_window_rates": b0["rates"],
            "median_completed_response_rps": b0["median"],
            "median_absolute_deviation_completed_response_rps": b0["mad"],
            "noise_band_percent": noise,
            "noise_band_formula": NOISE_FORMULA,
            "client_cpu_max_percent": client_cpu_max_percent,
            "server_cpu_max_percent": server_cpu_max_percent,
            "network_utilization_max_percent": network_utilization_max_percent,
            "p99_latency_max_ms": p99_latency_max_ms,
            "minimum_improvement_percent": max(3.0, noise),
            **b0["keys"],
        }
    )


def run(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(argv)
        baseline = require_absolute(
            args.baseline_manifest, "--baseline-manifest", exists=True
        )
        output = require_absolute(args.output, "--output")
        if output.exists():
            raise ContractError(f"refusing to overwrite immutable artifact: {output}")
        policy = create_policy(
            baseline,
            client_cpu_max_percent=args.client_cpu_max_percent,
            server_cpu_max_percent=args.server_cpu_max_percent,
            network_utilization_max_percent=args.network_utilization_max_percent,
            p99_latency_max_ms=args.p99_latency_max_ms,
        )
        atomic_write_json(output, policy)
        print(output)
        return 0
    except ContractError as exc:
        print(f"create-decision-policy: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:
        print(f"create-decision-policy: unexpected error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(run())
