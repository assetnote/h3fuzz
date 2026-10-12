#!/usr/bin/env python3
"""Run and report the immutable PERF-501 fixed HTTP/3 sweep.

Contract version 1 runs exactly three trials at every executed point.  It always
measures the requested core matrix through 64 connections, conditionally skips
unjustified 128/256 points after a plateau or worsening result, and applies the
documented global, inclusive near-best selection rule.  Every retained path is
relative to the manifest directory and every file in that directory is
content-hashed.
"""

from __future__ import annotations

import argparse
import copy
import json
import math
import os
from pathlib import Path
import statistics
import subprocess
import sys
from typing import Any, Iterable
from urllib.parse import urlsplit
import uuid

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
from sweep_resource_usage import validate_metrics_and_provenance  # noqa: E402


SCRIPT_DIR = Path(__file__).resolve().parent
REPORT_TYPE = "h3fuzz-fixed-sweep"
POLICY_TYPE = "h3fuzz-fixed-sweep-decision-policy"
TRIALS_PER_POINT = 3
MINIMUM_SPAN_SECONDS = 30.0
MAXIMUM_ERROR_RATE = 0.001
RESOURCE_FIELDS = (
    "client_cpu_percent",
    "server_cpu_percent",
    "network_utilization_percent",
    "p99_latency_ms",
)
RESOURCE_LIMIT_FIELDS = {
    "client_cpu_percent": "client_cpu_max_percent",
    "server_cpu_percent": "server_cpu_max_percent",
    "network_utilization_percent": "network_utilization_max_percent",
    "p99_latency_ms": "p99_latency_max_ms",
}
SHARED_KEY_FIELDS = ("platform_key", "client_build_key", "workload_key")


class BenchmarkFailure(RuntimeError):
    """The benchmark/snapshot/origin path failed operationally."""


def positive(value: str) -> int:
    try:
        result = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("expected a positive integer") from exc
    if result <= 0:
        raise argparse.ArgumentTypeError("expected a positive integer")
    return result


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--target", required=True)
    parser.add_argument("--wordlist", type=Path, required=True)
    parser.add_argument("--wordlist-manifest", type=Path, required=True)
    parser.add_argument("--extensions", default="")
    parser.add_argument(
        "--expect-strategy", choices=("head", "range", "get"), required=True
    )
    parser.add_argument("--filter-status")
    parser.add_argument("--filter-size")
    parser.add_argument("--connections", required=True)
    parser.add_argument("--streams", required=True)
    parser.add_argument("--trials-per-point", type=positive, required=True)
    parser.add_argument("--max-workers", type=positive, required=True)
    parser.add_argument("--decision-policy", type=Path, required=True)
    parser.add_argument("--server-snapshot-provider", type=Path, required=True)
    parser.add_argument("--server-evidence-provider", type=Path, required=True)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    return parser.parse_args(argv)


def require_absolute(path: Path, label: str, *, kind: str | None = None) -> Path:
    if not path.is_absolute():
        raise ContractError(f"{label} must be absolute")
    resolved = path.resolve()
    if kind == "file" and not resolved.is_file():
        raise ContractError(f"{label} must name an existing file")
    if kind == "executable" and (
        not resolved.is_file() or not os.access(resolved, os.X_OK)
    ):
        raise ContractError(f"{label} must name an executable file")
    return resolved


def parse_csv_positive(value: str, label: str, *, maximum: int | None = None) -> list[int]:
    pieces = value.split(",")
    if not pieces or any(not piece or not piece.isdigit() for piece in pieces):
        raise ContractError(f"{label} must be comma-separated positive integers")
    values = [int(piece) for piece in pieces]
    if any(item <= 0 or (maximum is not None and item > maximum) for item in values):
        suffix = f" no greater than {maximum}" if maximum is not None else ""
        raise ContractError(f"{label} values must be positive{suffix}")
    if len(set(values)) != len(values):
        raise ContractError(f"{label} must not contain duplicates")
    return sorted(values)


def finite_number(
    value: object, label: str, *, positive: bool = False, nonnegative: bool = False
) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ContractError(f"{label} must be a finite number")
    result = float(value)
    if not math.isfinite(result):
        raise ContractError(f"{label} must be a finite number")
    if positive and result <= 0:
        raise ContractError(f"{label} must be positive")
    if nonnegative and result < 0:
        raise ContractError(f"{label} must be nonnegative")
    return result


def nonnegative_integer(value: object) -> int | None:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        return None
    return value


def sha256(value: object, label: str) -> str:
    if (
        not isinstance(value, str)
        or len(value) != 64
        or any(character not in "0123456789abcdef" for character in value)
    ):
        raise ContractError(f"{label} must be a lowercase SHA-256 digest")
    return value


def validate_policy(path: Path) -> dict[str, Any]:
    policy = read_json(path)
    if (
        policy.get("schema_version") != 1
        or policy.get("manifest_type") != POLICY_TYPE
        or policy.get("policy_generation") != "frozen-b0-mad-v1"
    ):
        raise ContractError("--decision-policy is not a contract-v1 frozen B0 policy")
    verify_manifest_hash(policy, str(path))
    noise = finite_number(
        policy.get("noise_band_percent"), "decision policy noise_band_percent", nonnegative=True
    )
    minimum = finite_number(
        policy.get("minimum_improvement_percent"),
        "decision policy minimum_improvement_percent",
        positive=True,
    )
    if not math.isclose(minimum, max(3.0, noise), rel_tol=0.0, abs_tol=1e-12):
        raise ContractError("decision policy minimum improvement is not max(3, noise band)")
    for field in RESOURCE_LIMIT_FIELDS.values():
        finite_number(policy.get(field), f"decision policy {field}", positive=True)
    source = policy.get("source_baseline")
    if not isinstance(source, dict):
        raise ContractError("decision policy lacks its B0 source hashes")
    sha256(source.get("manifest_sha256"), "decision policy B0 manifest_sha256")
    sha256(source.get("file_sha256"), "decision policy B0 file_sha256")
    reports = source.get("reports")
    if not isinstance(reports, list) or len(reports) < 5:
        raise ContractError("decision policy requires at least five B0 report hashes")
    for index, reference in enumerate(reports):
        if (
            not isinstance(reference, dict)
            or set(reference) != {"ordinal", "sha256"}
            or reference.get("ordinal") != index + 1
        ):
            raise ContractError("decision policy B0 report hash index is invalid")
        sha256(reference.get("sha256"), f"decision policy B0 report {index + 1}")
    for field in (*SHARED_KEY_FIELDS, "treatment_key", "cohort_key"):
        sha256(policy.get(field), f"decision policy {field}")
    return policy


def validate_wordlist_manifest(path: Path, wordlist: Path, extensions: list[str]) -> dict[str, Any]:
    manifest = read_json(path)
    if (
        manifest.get("schema_version") != 1
        or manifest.get("manifest_type") != "h3fuzz-frozen-wordlist"
        or manifest.get("frozen") is not True
    ):
        raise ContractError("--wordlist-manifest must name a frozen schema-version-1 wordlist")
    verify_manifest_hash(manifest, str(path))
    expected_hash = sha256(manifest.get("sha256"), "wordlist manifest sha256")
    if file_sha256(wordlist) != expected_hash:
        raise ContractError("frozen wordlist content hash mismatch")
    line_count = manifest.get("line_count")
    if isinstance(line_count, bool) or not isinstance(line_count, int) or line_count <= 0:
        raise ContractError("wordlist manifest line_count must be positive")
    with wordlist.open("rb") as source:
        actual_lines = sum(1 for _ in source)
    if actual_lines != line_count:
        raise ContractError("frozen wordlist line count mismatch")
    if manifest.get("extensions") != extensions:
        raise ContractError("--extensions must exactly match the frozen wordlist manifest")
    if manifest.get("target_seconds") != 30 or manifest.get("headroom_percent") != 20:
        raise ContractError("wordlist manifest is not the contract-v1 30-second frozen corpus")
    expanded = manifest.get("expanded_input_count")
    if isinstance(expanded, bool) or not isinstance(expanded, int) or expanded < line_count:
        raise ContractError("wordlist manifest expanded_input_count is invalid")
    return manifest


def copy_create_only(source: Path, destination: Path) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    descriptor = os.open(destination, flags, 0o600)
    try:
        with source.open("rb") as reader, os.fdopen(descriptor, "wb") as writer:
            descriptor = -1
            for chunk in iter(lambda: reader.read(1024 * 1024), b""):
                writer.write(chunk)
            writer.flush()
            os.fsync(writer.fileno())
    finally:
        if descriptor >= 0:
            os.close(descriptor)
    if file_sha256(source) != file_sha256(destination):
        raise ContractError(f"copied prerequisite hash mismatch: {destination}")


def relative_path(path: Path, root: Path, label: str) -> str:
    resolved = path.resolve()
    try:
        relative = resolved.relative_to(root.resolve())
    except ValueError as exc:
        raise ContractError(f"{label} escapes the sweep artifact root") from exc
    if not resolved.is_file() or resolved.is_symlink():
        raise ContractError(f"{label} must be a regular, non-symlink file")
    return relative.as_posix()


def artifact_reference(path: Path, root: Path, label: str) -> dict[str, str]:
    return {
        "path": relative_path(path, root, label),
        "sha256": file_sha256(path),
    }


def validate_run_gates(report: dict[str, Any]) -> tuple[list[str], float | None]:
    reasons: list[str] = []
    if report.get("aggregation_mode") != "sweep":
        reasons.append("aggregation_mode")
    if report.get("run_gates_passed") is not True:
        reasons.append("run_gates_failed")
    if report.get("eligible_for_aggregation") is not True:
        reasons.append("not_eligible_for_aggregation")
    if report.get("rps_measurement_basis") != "successful_logical_responses/main_request_span":
        reasons.append("invalid_rps_measurement_basis")

    try:
        span = finite_number(report.get("main_request_span_seconds"), "main span", positive=True)
    except ContractError:
        span = None
        reasons.append("invalid_main_request_span")
    if span is not None and span < MINIMUM_SPAN_SECONDS:
        reasons.append("main_request_span_below_30_seconds")

    fields = {
        name: nonnegative_integer(report.get(name))
        for name in (
            "successful_logical_responses",
            "completed_responses",
            "scheduled_logical_inputs",
            "terminal_logical_outcomes",
            "terminal_logical_errors",
            "retries",
            "application_attempts",
            "transport_errors",
        )
    }
    required = (
        "successful_logical_responses",
        "completed_responses",
        "scheduled_logical_inputs",
        "terminal_logical_outcomes",
        "terminal_logical_errors",
        "retries",
    )
    if any(fields[name] is None for name in required):
        reasons.append("count_reconciliation")
    else:
        successful = fields["successful_logical_responses"]
        completed = fields["completed_responses"]
        scheduled = fields["scheduled_logical_inputs"]
        terminal = fields["terminal_logical_outcomes"]
        terminal_errors = fields["terminal_logical_errors"]
        assert None not in (successful, completed, scheduled, terminal, terminal_errors)
        if not (
            successful == completed
            and terminal == scheduled
            and successful + terminal_errors == scheduled
        ):
            reasons.append("count_reconciliation")

    successful = fields["successful_logical_responses"]
    rate = successful / span if successful is not None and span else None
    try:
        reported_rate = finite_number(
            report.get("completed_response_rps"), "completed_response_rps", positive=True
        )
    except ContractError:
        reported_rate = None
    if (
        rate is None
        or reported_rate is None
        or not math.isclose(rate, reported_rate, rel_tol=1e-9, abs_tol=1e-9)
    ):
        reasons.append("completed_response_rps_mismatch")

    scheduled = fields["scheduled_logical_inputs"]
    terminal_errors = fields["terminal_logical_errors"]
    retries = fields["retries"]
    attempts = fields["application_attempts"]
    transport_errors = fields["transport_errors"]
    terminal_rate = terminal_errors / scheduled if terminal_errors is not None and scheduled else None
    retry_rate = retries / scheduled if retries is not None and scheduled else None
    attempt_rate = transport_errors / attempts if transport_errors is not None and attempts else None
    if terminal_rate is None or terminal_rate >= MAXIMUM_ERROR_RATE:
        reasons.append("terminal_logical_error_rate")
    if retry_rate is None or retry_rate >= MAXIMUM_ERROR_RATE:
        reasons.append("application_retry_rate")
    if attempt_rate is None or attempt_rate >= MAXIMUM_ERROR_RATE:
        reasons.append("surfaced_attempt_error_rate")
    if report.get("responses_accounted") is not True or report.get("logical_ledger_proven") is not True:
        reasons.append("logical_ledger")
    if report.get("transport_proven") is not True:
        reasons.append("transport_ledger")
    if report.get("drain_complete") is not True:
        reasons.append("incomplete_drain")

    correlation = report.get("server_correlation")
    if not isinstance(correlation, dict) or correlation.get("available") is not True:
        reasons.append("server_correlation_missing")
    else:
        try:
            error_rate = finite_number(
                correlation.get("error_rate"), "server correlation error", nonnegative=True
            )
        except ContractError:
            error_rate = None
        if error_rate is None or error_rate > MAXIMUM_ERROR_RATE:
            reasons.append("server_reconciliation")

    udp = report.get("udp_attribution")
    if not isinstance(udp, dict) or udp.get("available") is not True:
        reasons.append("udp_attribution_missing")
    elif any(
        nonnegative_integer(udp.get(field)) != 0
        for field in (
            "receive_buffer_errors_delta",
            "packet_drops_delta",
            "unexplained_delta",
        )
    ):
        reasons.append("udp_unexplained_delta")
    return list(dict.fromkeys(reasons)), rate


def resource_observation(report: dict[str, Any], label: str) -> tuple[dict[str, float] | None, list[str]]:
    raw = report.get("sweep_resource_usage")
    if not isinstance(raw, dict):
        return None, ["resource_usage_missing"]
    if (
        report.get("sweep_resource_usage_required") is not True
        or report.get("sweep_resource_usage_valid") is not True
        or report.get("sweep_resource_usage_ineligibility_reasons") != []
    ):
        return None, ["resource_usage_unvalidated"]
    digest = report.get("sweep_resource_evidence_sha256")
    if not isinstance(digest, str) or len(digest) != 64 or any(
        character not in "0123456789abcdef" for character in digest
    ):
        return None, ["resource_evidence_sha256_missing_or_invalid"]
    try:
        result = validate_metrics_and_provenance(
            raw, report.get("sweep_resource_usage_provenance")
        )
    except ContractError as exc:
        return None, [f"resource_usage_provenance_invalid:{exc}"]
    successful = nonnegative_integer(report.get("successful_logical_responses"))
    latency_provenance = report["sweep_resource_usage_provenance"]["p99_latency_ms"]
    if successful is None or latency_provenance.get("sample_count") != successful:
        return None, ["resource_latency_sample_count_mismatch"]
    return result, []


def validate_report_structure(
    report: dict[str, Any],
    *,
    connections: int,
    streams: int,
    max_workers: int,
    wordlist_manifest: dict[str, Any],
    label: str,
) -> dict[str, Any]:
    keys = report.get("comparison_keys")
    key_errors = comparison_key_errors(keys)
    if key_errors:
        raise ContractError(f"{label} comparison keys: {','.join(key_errors)}")
    assert isinstance(keys, dict)
    treatment = keys.get("treatment")
    if not isinstance(treatment, dict):
        raise ContractError(f"{label} treatment is missing")
    expected_worker = {"kind": "bounded", "threads": None, "maximum": max_workers}
    if (
        treatment.get("mode") != "sweep"
        or treatment.get("connection_policy") != {"kind": "fixed", "value": connections}
        or treatment.get("streams") != streams
        or treatment.get("worker_policy") != expected_worker
        or treatment.get("profile_flags") != []
    ):
        raise ContractError(f"{label} treatment does not match the matrix point")
    workload = keys.get("workload")
    platform = keys.get("platform")
    if not isinstance(workload, dict) or not isinstance(platform, dict):
        raise ContractError(f"{label} workload/platform components are missing")
    if workload.get("wordlist_sha256") != wordlist_manifest.get("sha256"):
        raise ContractError(f"{label} frozen wordlist hash mismatch")
    if workload.get("wordlist_line_count") != wordlist_manifest.get("line_count"):
        raise ContractError(f"{label} frozen wordlist line-count mismatch")
    if workload.get("extensions") != wordlist_manifest.get("extensions"):
        raise ContractError(f"{label} extension workload mismatch")
    authority = workload.get("authority")
    server_identity = platform.get("server_host_identity")
    if not isinstance(authority, str) or not authority:
        raise ContractError(f"{label} authority is missing")
    if not isinstance(server_identity, str) or not server_identity:
        raise ContractError(f"{label} server identity is missing")
    return keys


def evaluate_point(
    *,
    connections: int,
    streams: int,
    report_paths: list[Path],
    root: Path,
    policy: dict[str, Any],
    wordlist_manifest: dict[str, Any],
    max_workers: int,
    prior_best: float | None,
) -> dict[str, Any]:
    if len(report_paths) != TRIALS_PER_POINT:
        raise ContractError("every executed point requires exactly three reports")
    report_refs: list[dict[str, str]] = []
    rates: list[float] = []
    run_reasons: list[list[str]] = []
    resources: list[dict[str, float]] = []
    keys_by_run: list[dict[str, Any]] = []
    for index, report_path in enumerate(report_paths, 1):
        label = f"c{connections}-s{streams} trial {index}"
        relative_path(report_path, root, label)
        report = read_json(report_path)
        keys = validate_report_structure(
            report,
            connections=connections,
            streams=streams,
            max_workers=max_workers,
            wordlist_manifest=wordlist_manifest,
            label=label,
        )
        reasons, rate = validate_run_gates(report)
        observation, resource_reasons = resource_observation(report, label)
        reasons.extend(resource_reasons)
        if rate is None:
            raise ContractError(f"{label} lacks a usable completed-response rate")
        rates.append(rate)
        run_reasons.append(list(dict.fromkeys(reasons)))
        if observation is not None:
            resources.append(observation)
        keys_by_run.append(keys)
        report_refs.append(artifact_reference(report_path, root, label))

    first_keys = keys_by_run[0]
    for index, keys in enumerate(keys_by_run[1:], 2):
        if any(
            keys.get(field) != first_keys.get(field)
            for field in (*SHARED_KEY_FIELDS, "treatment_key", "cohort_key")
        ):
            raise ContractError(
                f"c{connections}-s{streams} trial {index} layered key mismatch"
            )

    median_rate = statistics.median(rates)
    threshold = float(policy["minimum_improvement_percent"])
    improvement = (
        100.0 * (median_rate - prior_best) / prior_best
        if prior_best is not None
        else None
    )
    meets_improvement = improvement is not None and improvement >= threshold
    all_run_gates_clean = all(not reasons for reasons in run_reasons)
    resource_summary: dict[str, Any] | None = None
    exceeded: list[str] = []
    if len(resources) == TRIALS_PER_POINT:
        resource_summary = {}
        for field in RESOURCE_FIELDS:
            samples = [item[field] for item in resources]
            maximum = max(samples)
            limit = float(policy[RESOURCE_LIMIT_FIELDS[field]])
            resource_summary[field] = {
                "samples": samples,
                "median": statistics.median(samples),
                "maximum": maximum,
                "limit": limit,
                "exceeded": maximum > limit,
            }
            if maximum > limit:
                exceeded.append(field)

    if not all_run_gates_clean:
        classification = "ineligible"
        selection_eligible = False
    elif resource_summary is None:
        classification = "ineligible"
        selection_eligible = False
    elif exceeded and not meets_improvement:
        classification = "worsened"
        selection_eligible = False
    elif prior_best is not None and not meets_improvement:
        classification = "plateau"
        selection_eligible = True
    elif exceeded:
        classification = "improved_with_resource_override"
        selection_eligible = True
    elif prior_best is None:
        classification = "initial"
        selection_eligible = True
    else:
        classification = "improved"
        selection_eligible = True

    ineligible_reasons = [
        f"trial_{index:02d}:{reason}"
        for index, reasons in enumerate(run_reasons, 1)
        for reason in reasons
    ]
    if exceeded and not meets_improvement:
        ineligible_reasons.extend(f"resource_limit:{field}" for field in exceeded)
        ineligible_reasons.append("resource_limit_without_minimum_throughput_improvement")
    return {
        "connections": connections,
        "streams": streams,
        "status": "executed",
        "classification": classification,
        "reports": report_refs,
        "trial_completed_response_rps": rates,
        "median_completed_response_rps": median_rate,
        "run_eligibility": [not reasons for reasons in run_reasons],
        "run_ineligible_reasons": run_reasons,
        "clean": all_run_gates_clean,
        "selection_eligible": selection_eligible,
        "ineligible_reasons": list(dict.fromkeys(ineligible_reasons)),
        "resource_summary": resource_summary,
        "resource_limits_exceeded": exceeded,
        "prior_best_completed_response_rps": prior_best,
        "improvement_percent_vs_prior_best": improvement,
        "minimum_improvement_percent": threshold,
        "treatment_key": first_keys["treatment_key"],
        "cohort_key": first_keys["cohort_key"],
        "platform_key": first_keys["platform_key"],
        "client_build_key": first_keys["client_build_key"],
        "workload_key": first_keys["workload_key"],
        "authority": first_keys["workload"]["authority"],
        "server_identity": first_keys["platform"]["server_host_identity"],
    }


def skipped_point(
    connections: int,
    streams: int,
    reason: str,
    trigger: dict[str, Any] | None,
) -> dict[str, Any]:
    result: dict[str, Any] = {
        "connections": connections,
        "streams": streams,
        "status": "skipped",
        "skipped_reasons": [reason],
    }
    if trigger is not None:
        result["skipped_after"] = {
            "connections": trigger["connections"],
            "streams": trigger["streams"],
            "classification": trigger["classification"],
        }
    return result


def select_point(points: Iterable[dict[str, Any]], threshold: float) -> dict[str, Any]:
    candidates = [point for point in points if point.get("selection_eligible") is True]
    if not candidates:
        raise ContractError("no clean selection-eligible fixed-sweep point")
    best = max(float(point["median_completed_response_rps"]) for point in candidates)
    near_best: list[dict[str, Any]] = []
    for point in candidates:
        median_rate = float(point["median_completed_response_rps"])
        gap = 100.0 * (best - median_rate) / best
        if gap <= threshold:
            near_best.append(
                {
                    "connections": point["connections"],
                    "streams": point["streams"],
                    "median_completed_response_rps": median_rate,
                    "gap_from_best_percent": gap,
                    "connections_times_streams": point["connections"] * point["streams"],
                    "treatment_key": point["treatment_key"],
                    "cohort_key": point["cohort_key"],
                }
            )
    chosen = min(
        near_best,
        key=lambda point: (
            point["connections_times_streams"],
            point["connections"],
            point["streams"],
        ),
    )
    return {
        "best_median_completed_response_rps": best,
        "near_best_threshold_percent": threshold,
        "inclusive_near_best_formula": "100 * (M_best - M_i) / M_best <= T",
        "near_best": near_best,
        "total_order": ["connections_times_streams", "connections", "streams"],
        "chosen": chosen,
    }


def member_inventory(root: Path, manifest: Path) -> list[dict[str, Any]]:
    members: list[dict[str, Any]] = []
    for candidate in sorted(root.rglob("*")):
        if not candidate.is_file() or candidate.resolve() == manifest.resolve():
            continue
        relative = relative_path(candidate, root, "manifest member")
        members.append(
            {
                "path": relative,
                "sha256": file_sha256(candidate),
                "size": candidate.stat().st_size,
            }
        )
    if not members:
        raise ContractError("fixed sweep has no retained artifact members")
    return members


def build_manifest(
    *,
    root: Path,
    manifest_path: Path,
    policy: dict[str, Any],
    policy_path: Path,
    wordlist_manifest: dict[str, Any],
    wordlist_manifest_path: Path,
    connections: list[int],
    streams: list[int],
    max_workers: int,
    candidates: list[dict[str, Any]],
    global_reasons: list[str],
) -> dict[str, Any]:
    expected_matrix = {(connection, stream) for stream in streams for connection in connections}
    observed_matrix = {
        (candidate.get("connections"), candidate.get("streams")) for candidate in candidates
    }
    if observed_matrix != expected_matrix or len(candidates) != len(expected_matrix):
        raise ContractError("candidate matrix is incomplete or contains duplicate points")

    executed = [candidate for candidate in candidates if candidate.get("status") == "executed"]
    if any(len(candidate.get("reports", [])) != TRIALS_PER_POINT for candidate in executed):
        raise ContractError("every executed matrix point must retain exactly three reports")
    shared_sets = {
        field: {candidate.get(field) for candidate in executed}
        for field in (*SHARED_KEY_FIELDS, "authority", "server_identity")
    }
    mixed_fields = [field for field, values in shared_sets.items() if len(values) != 1]
    if mixed_fields:
        raise ContractError(f"fixed sweep mixes shared layered keys: {','.join(mixed_fields)}")

    selectable = [candidate for candidate in executed if candidate.get("selection_eligible") is True]
    selection: dict[str, Any] | None = None
    selection_error: str | None = None
    try:
        selection = select_point(selectable, float(policy["minimum_improvement_percent"]))
    except ContractError as exc:
        selection_error = str(exc)

    reasons = list(dict.fromkeys(global_reasons + ([selection_error] if selection_error else [])))
    eligible = not reasons and selection is not None
    selected_point: dict[str, Any] | None = None
    adaptive_max_connections: int | None = None
    if eligible and selection is not None:
        chosen = selection["chosen"]
        selected_point = {
            "connections": chosen["connections"],
            "streams": chosen["streams"],
            "median_completed_response_rps": chosen["median_completed_response_rps"],
            "gap_from_best_percent": chosen["gap_from_best_percent"],
            "connections_times_streams": chosen["connections_times_streams"],
            "treatment_key": chosen["treatment_key"],
            "cohort_key": chosen["cohort_key"],
        }
        adaptive_max_connections = chosen["connections"]

    points = [
        {
            "connections": candidate["connections"],
            "streams": candidate["streams"],
            "treatment_key": candidate["treatment_key"],
            "cohort_key": candidate["cohort_key"],
            "clean": True,
            "median_completed_response_rps": candidate["median_completed_response_rps"],
            "reports": copy.deepcopy(candidate["reports"]),
        }
        for candidate in selectable
    ]

    shared = {
        field: next(iter(values)) if len(values) == 1 else None
        for field, values in shared_sets.items()
    }
    manifest = {
        "schema_version": 1,
        "manifest_type": REPORT_TYPE,
        "created_at": utc_now(),
        "eligible": eligible,
        "ineligible_reasons": reasons,
        "trials_per_point": TRIALS_PER_POINT,
        "candidate_connections": connections,
        "candidate_streams": streams,
        "candidate_matrix": candidates,
        "decision_policy": copy.deepcopy(policy),
        "decision_policy_reference": artifact_reference(
            policy_path, root, "decision policy reference"
        ),
        "wordlist_manifest_reference": artifact_reference(
            wordlist_manifest_path, root, "wordlist manifest reference"
        ),
        "wordlist_sha256": wordlist_manifest["sha256"],
        "platform_key": shared["platform_key"],
        "client_build_key": shared["client_build_key"],
        "workload_key": shared["workload_key"],
        "authority": shared["authority"],
        "server_identity": shared["server_identity"],
        "points": points,
        "selection": selection,
        "selected_point": selected_point,
        "adaptive_max_connections": adaptive_max_connections,
        "adaptive_max_workers": max_workers,
        "members": member_inventory(root, manifest_path),
    }
    return add_manifest_hash(manifest)


def validate_final_manifest(manifest: dict[str, Any], manifest_path: Path) -> None:
    verify_manifest_hash(manifest, str(manifest_path))
    if manifest.get("schema_version") != 1 or manifest.get("manifest_type") != REPORT_TYPE:
        raise ContractError("invalid fixed-sweep manifest identity")
    if manifest.get("trials_per_point") != TRIALS_PER_POINT:
        raise ContractError("fixed-sweep manifest changed its trial count")
    members = manifest.get("members")
    if not isinstance(members, list) or not members:
        raise ContractError("fixed-sweep manifest has no members")
    member_map: dict[str, str] = {}
    for index, member in enumerate(members):
        if not isinstance(member, dict) or set(member) != {"path", "sha256", "size"}:
            raise ContractError(f"members[{index}] is not an exact path/hash/size record")
        relative = member.get("path")
        if not isinstance(relative, str) or not relative or Path(relative).is_absolute():
            raise ContractError(f"members[{index}] path is not portable")
        if relative in member_map:
            raise ContractError(f"duplicate member path: {relative}")
        digest = sha256(member.get("sha256"), f"members[{index}].sha256")
        candidate = (manifest_path.parent / relative).resolve()
        try:
            candidate.relative_to(manifest_path.parent.resolve())
        except ValueError as exc:
            raise ContractError(f"member escapes artifact root: {relative}") from exc
        if not candidate.is_file() or candidate.is_symlink():
            raise ContractError(f"missing or unsafe member: {relative}")
        if file_sha256(candidate) != digest or candidate.stat().st_size != member.get("size"):
            raise ContractError(f"member content mismatch: {relative}")
        member_map[relative] = digest
    actual = {
        path.relative_to(manifest_path.parent).as_posix()
        for path in manifest_path.parent.rglob("*")
        if path.is_file() and path.resolve() != manifest_path.resolve()
    }
    if actual != set(member_map):
        raise ContractError("fixed-sweep member tree does not exactly match the manifest")
    for candidate in manifest.get("candidate_matrix", []):
        if candidate.get("status") == "executed":
            reports = candidate.get("reports")
            if not isinstance(reports, list) or len(reports) != TRIALS_PER_POINT:
                raise ContractError("executed candidate does not have exactly three reports")
            for reference in reports:
                if (
                    not isinstance(reference, dict)
                    or set(reference) != {"path", "sha256"}
                    or member_map.get(reference.get("path")) != reference.get("sha256")
                ):
                    raise ContractError("candidate report reference is absent from members")
        elif candidate.get("status") == "skipped":
            reasons = candidate.get("skipped_reasons")
            if not isinstance(reasons, list) or not reasons:
                raise ContractError("skipped candidate lacks a reason")
        else:
            raise ContractError("candidate status must be executed or skipped")
    if manifest.get("eligible") is True:
        selected = manifest.get("selected_point")
        selection = manifest.get("selection")
        if not isinstance(selected, dict) or not isinstance(selection, dict):
            raise ContractError("eligible sweep lacks deterministic selection evidence")
        recomputed = select_point(
            [
                candidate
                for candidate in manifest["candidate_matrix"]
                if candidate.get("selection_eligible") is True
            ],
            float(manifest["decision_policy"]["minimum_improvement_percent"]),
        )
        chosen = recomputed["chosen"]
        if (
            selected.get("connections") != chosen["connections"]
            or selected.get("streams") != chosen["streams"]
            or selected.get("treatment_key") != chosen["treatment_key"]
            or selected.get("cohort_key") != chosen["cohort_key"]
            or manifest.get("adaptive_max_connections") != chosen["connections"]
        ):
            raise ContractError("selected/adaptive point does not match the total tie-break")


def benchmark_command(
    args: argparse.Namespace,
    benchmark: Path,
    *,
    connections: int,
    streams: int,
    trial: int,
    trial_dir: Path,
) -> list[str]:
    command = [
        str(benchmark),
        "--target",
        args.target,
        "--wordlist",
        str(args.wordlist),
        "--mode",
        "sweep",
        "--extensions",
        args.extensions,
        "--expect-strategy",
        args.expect_strategy,
        "--streams",
        str(streams),
        "--connections",
        str(connections),
        "--max-workers",
        str(args.max_workers),
        "--run-id",
        str(uuid.uuid4()),
        "--server-snapshot-provider",
        str(args.server_snapshot_provider),
        "--server-evidence-provider",
        str(args.server_evidence_provider),
        "--client-before",
        str(trial_dir / "client-before.json"),
        "--client-after",
        str(trial_dir / "client-after.json"),
        "--client-runtime-sockets",
        str(trial_dir / "client-runtime-sockets.jsonl"),
        "--server-counters-before",
        str(trial_dir / "server-counters-before.json"),
        "--server-counters-after",
        str(trial_dir / "server-counters-after.json"),
        "--server-stable",
        str(trial_dir / "server-stable.json"),
        "--server-evidence-before",
        str(trial_dir / "server-evidence-before.json"),
        "--server-evidence-after",
        str(trial_dir / "server-evidence-after.json"),
        "--artifact-dir",
        str(trial_dir),
    ]
    if args.filter_status:
        command.extend(("--filter-status", args.filter_status))
    if args.filter_size:
        command.extend(("--filter-size", args.filter_size))
    return command


def run_sweep(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    if not args.target.startswith("https://") or "FUZZ" not in args.target:
        raise ContractError("--target must be an HTTPS URL containing FUZZ")
    parsed_target = urlsplit(args.target)
    if not parsed_target.netloc:
        raise ContractError("--target lacks an authority")
    if not args.filter_status and not args.filter_size:
        raise ContractError("at least one status or size filter is required")
    if args.trials_per_point != TRIALS_PER_POINT:
        raise ContractError("contract version 1 requires --trials-per-point 3")
    connections = parse_csv_positive(args.connections, "--connections", maximum=256)
    streams = parse_csv_positive(args.streams, "--streams")
    extensions = args.extensions.split(",") if args.extensions else []
    if any(not value or value.strip() != value for value in extensions) or len(set(extensions)) != len(extensions):
        raise ContractError("--extensions must be a duplicate-free comma-separated list")

    args.wordlist = require_absolute(args.wordlist, "--wordlist", kind="file")
    args.wordlist_manifest = require_absolute(
        args.wordlist_manifest, "--wordlist-manifest", kind="file"
    )
    args.decision_policy = require_absolute(
        args.decision_policy, "--decision-policy", kind="file"
    )
    args.server_snapshot_provider = require_absolute(
        args.server_snapshot_provider, "--server-snapshot-provider", kind="executable"
    )
    args.server_evidence_provider = require_absolute(
        args.server_evidence_provider, "--server-evidence-provider", kind="executable"
    )
    args.artifact_dir = require_absolute(args.artifact_dir, "--artifact-dir")
    args.manifest = require_absolute(args.manifest, "--manifest")
    if args.artifact_dir.exists():
        raise ContractError("--artifact-dir must not already exist")
    if args.manifest.parent.resolve() != args.artifact_dir.resolve():
        raise ContractError("--manifest must be directly inside --artifact-dir")
    if args.manifest.exists():
        raise ContractError(f"refusing to overwrite immutable artifact: {args.manifest}")

    policy = validate_policy(args.decision_policy)
    wordlist_manifest = validate_wordlist_manifest(
        args.wordlist_manifest, args.wordlist, extensions
    )
    benchmark = require_absolute(
        Path(
            os.environ.get(
                "H3FUZZ_FIXED_SWEEP_BENCHMARK", str(SCRIPT_DIR / "benchmark.sh")
            )
        ),
        "benchmark runner",
        kind="executable",
    )

    args.artifact_dir.mkdir(parents=True)
    os.chmod(args.artifact_dir, 0o700)
    policy_copy = args.artifact_dir / "inputs" / "decision-policy.json"
    wordlist_copy = args.artifact_dir / "inputs" / "wordlist-manifest.json"
    copy_create_only(args.decision_policy, policy_copy)
    copy_create_only(args.wordlist_manifest, wordlist_copy)

    candidates: list[dict[str, Any]] = []
    global_reasons: list[str] = []
    generation_invalid = False
    for stream in streams:
        prior_best: float | None = None
        high_connection_skip_reason: str | None = None
        stop_trigger: dict[str, Any] | None = None
        for connection in connections:
            if generation_invalid:
                candidates.append(
                    skipped_point(
                        connection,
                        stream,
                        "generation_invalid_after_short_trial",
                        stop_trigger,
                    )
                )
                continue
            # Contract v1 always measures every requested core point through 64
            # connections.  The 128/256 points are conditional; a plateau,
            # worsening, or ineligible immediately preceding point is the
            # recorded reason they are not justified.
            if connection > 64 and high_connection_skip_reason is not None:
                candidates.append(
                    skipped_point(
                        connection,
                        stream,
                        high_connection_skip_reason,
                        stop_trigger,
                    )
                )
                continue

            report_paths: list[Path] = []
            for trial in range(1, TRIALS_PER_POINT + 1):
                trial_dir = (
                    args.artifact_dir
                    / "points"
                    / f"c{connection:04d}-s{stream:04d}"
                    / f"trial-{trial:02d}"
                )
                command = benchmark_command(
                    args,
                    benchmark,
                    connections=connection,
                    streams=stream,
                    trial=trial,
                    trial_dir=trial_dir,
                )
                invocation = (
                    args.artifact_dir
                    / "orchestration"
                    / f"c{connection:04d}-s{stream:04d}-trial-{trial:02d}.json"
                )
                atomic_write_json(invocation, {"schema_version": 1, "argv": command})
                completed = subprocess.run(command, check=False)
                if completed.returncode not in (0, 3):
                    raise BenchmarkFailure(
                        f"c{connection}-s{stream} trial {trial} failed with exit "
                        f"{completed.returncode}"
                    )
                report_path = trial_dir / "benchmark-report.json"
                if not report_path.is_file():
                    raise BenchmarkFailure(
                        f"c{connection}-s{stream} trial {trial} produced no benchmark report"
                    )
                report_paths.append(report_path)

            candidate = evaluate_point(
                connections=connection,
                streams=stream,
                report_paths=report_paths,
                root=args.artifact_dir,
                policy=policy,
                wordlist_manifest=wordlist_manifest,
                max_workers=args.max_workers,
                prior_best=prior_best,
            )
            candidates.append(candidate)
            short = any(
                "main_request_span_below_30_seconds" in reasons
                for reasons in candidate["run_ineligible_reasons"]
            )
            if short:
                generation_invalid = True
                global_reasons.append("frozen_wordlist_generation_invalid_short_trial")
                stop_trigger = candidate
                continue
            if candidate["selection_eligible"]:
                rate = float(candidate["median_completed_response_rps"])
                prior_best = max(prior_best or rate, rate)
            classification = candidate["classification"]
            if classification == "plateau":
                high_connection_skip_reason = "high_connection_point_not_justified_after_plateau"
                stop_trigger = candidate
            elif classification == "worsened":
                high_connection_skip_reason = "high_connection_point_not_justified_after_worsening"
                stop_trigger = candidate
            elif classification == "ineligible":
                high_connection_skip_reason = "high_connection_point_not_justified_after_ineligible_point"
                stop_trigger = candidate
            else:
                high_connection_skip_reason = None
                stop_trigger = None

    manifest = build_manifest(
        root=args.artifact_dir,
        manifest_path=args.manifest,
        policy=policy,
        policy_path=policy_copy,
        wordlist_manifest=wordlist_manifest,
        wordlist_manifest_path=wordlist_copy,
        connections=connections,
        streams=streams,
        max_workers=args.max_workers,
        candidates=candidates,
        global_reasons=global_reasons,
    )
    validate_final_manifest(manifest, args.manifest)
    atomic_write_json(args.manifest, manifest)
    validate_final_manifest(read_json(args.manifest), args.manifest)
    return manifest, 0 if manifest["eligible"] else 3


def run(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(argv)
        manifest, status = run_sweep(args)
        print(args.manifest)
        if status == 3:
            print(
                "fixed-sweep: completed but ineligible: "
                + ",".join(manifest["ineligible_reasons"]),
                file=sys.stderr,
            )
        return status
    except ContractError as exc:
        print(f"fixed-sweep: {exc}", file=sys.stderr)
        return 2
    except BenchmarkFailure as exc:
        print(f"fixed-sweep: {exc}", file=sys.stderr)
        return 4
    except Exception as exc:
        print(f"fixed-sweep: unexpected error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(run())
