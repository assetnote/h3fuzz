#!/usr/bin/env python3
"""Validate and aggregate PERF-004 five-by-five certification evidence."""

from __future__ import annotations

import argparse
import copy
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
    canonical_hash,
    comparison_key_errors,
    file_sha256,
    read_json,
    summarize_numbers,
    utc_now,
    verify_manifest_hash,
    verify_relative_members,
)


REPORT_VERSION = 1
MINIMUM_SPAN_SECONDS = 30.0
MAXIMUM_ERROR_RATE = 0.001


def _number(value: object) -> float | None:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    result = float(value)
    return result if math.isfinite(result) else None


def _integer(value: object) -> int | None:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        return None
    return value


def validate_run(report: dict[str, Any], expected_mode: str) -> tuple[list[str], dict[str, Any]]:
    reasons: list[str] = []
    if report.get("aggregation_mode") != expected_mode:
        reasons.append("aggregation_mode")
    if report.get("run_gates_passed") is not True:
        reasons.append("run_gates_failed")
    if report.get("eligible_for_aggregation") is not True:
        reasons.append("not_eligible_for_aggregation")
    if report.get("rps_measurement_basis") != "successful_logical_responses/main_request_span":
        reasons.append("invalid_rps_measurement_basis")

    span = _number(report.get("main_request_span_seconds"))
    if span is None or span < MINIMUM_SPAN_SECONDS:
        reasons.append("main_request_span_below_30_seconds")

    successful = _integer(report.get("successful_logical_responses"))
    completed = _integer(report.get("completed_responses"))
    scheduled = _integer(report.get("scheduled_logical_inputs"))
    terminal = _integer(report.get("terminal_logical_outcomes"))
    terminal_errors = _integer(report.get("terminal_logical_errors"))
    retries = _integer(report.get("retries"))
    application_attempts = _integer(report.get("application_attempts"))
    attempt_errors = _integer(report.get("transport_errors"))
    if None in (successful, completed, scheduled, terminal, terminal_errors, retries):
        reasons.append("count_reconciliation")
    elif not (
        successful == completed
        and terminal == scheduled
        and successful + terminal_errors == scheduled
    ):
        reasons.append("count_reconciliation")

    reported_rate = _number(report.get("completed_response_rps"))
    recomputed_rate = successful / span if successful is not None and span and span > 0 else None
    if (
        reported_rate is None
        or recomputed_rate is None
        or not math.isclose(reported_rate, recomputed_rate, rel_tol=1e-9, abs_tol=1e-9)
    ):
        reasons.append("completed_response_rps_mismatch")

    terminal_rate = terminal_errors / scheduled if terminal_errors is not None and scheduled else None
    retry_rate = retries / scheduled if retries is not None and scheduled else None
    attempt_rate = attempt_errors / application_attempts if attempt_errors is not None and application_attempts else None
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
        correlation_error = _number(correlation.get("error_rate"))
        if correlation_error is None or correlation_error > MAXIMUM_ERROR_RATE:
            reasons.append("server_reconciliation")

    udp = report.get("udp_attribution")
    if not isinstance(udp, dict) or udp.get("available") is not True:
        reasons.append("udp_attribution_missing")
    elif any(_integer(udp.get(field)) != 0 for field in (
        "receive_buffer_errors_delta", "packet_drops_delta", "unexplained_delta"
    )):
        reasons.append("udp_unexplained_delta")

    for key_error in comparison_key_errors(report.get("comparison_keys")):
        reasons.append(f"comparison_keys:{key_error}")

    count_reconciled = None not in (successful, completed, scheduled, terminal, terminal_errors, retries) and (
        successful == completed
        and terminal == scheduled
        and successful + terminal_errors == scheduled
    )
    udp_deltas = {
        field: _integer(udp.get(field)) if isinstance(udp, dict) else None
        for field in ("receive_buffer_errors_delta", "packet_drops_delta", "unexplained_delta")
    }
    return reasons, {
        "completed_response_rps": recomputed_rate,
        "terminal_logical_error_rate": terminal_rate,
        "application_retry_rate": retry_rate,
        "surfaced_attempt_error_rate": attempt_rate,
        "count_reconciled": count_reconciled,
        "drain_complete": report.get("drain_complete") is True,
        "udp_deltas": udp_deltas,
        "server_correlation": copy.deepcopy(correlation) if isinstance(correlation, dict) else None,
        "counts": {
            "successful_logical_responses": successful,
            "completed_responses": completed,
            "scheduled_logical_inputs": scheduled,
            "terminal_logical_outcomes": terminal,
            "terminal_logical_errors": terminal_errors,
            "retries": retries,
            "application_attempts": application_attempts,
            "transport_errors": attempt_errors,
        },
    }


def _cohort(
    reports: list[dict[str, Any]], expected_mode: str, label: str
) -> tuple[dict[str, Any], list[str]]:
    reasons: list[str] = []
    rates: list[float] = []
    terminal_rates: list[float] = []
    retry_rates: list[float] = []
    attempt_rates: list[float] = []
    run_summaries: list[dict[str, Any]] = []
    if len(reports) != 5:
        reasons.append(f"{label}:requires_exactly_five_runs")
    for index, report in enumerate(reports, 1):
        run_reasons, metrics = validate_run(report, expected_mode)
        reasons.extend(f"{label}_run_{index:02d}:{reason}" for reason in run_reasons)
        if metrics["completed_response_rps"] is not None:
            rates.append(float(metrics["completed_response_rps"]))
        if metrics["terminal_logical_error_rate"] is not None:
            terminal_rates.append(float(metrics["terminal_logical_error_rate"]))
        if metrics["application_retry_rate"] is not None:
            retry_rates.append(float(metrics["application_retry_rate"]))
        if metrics["surfaced_attempt_error_rate"] is not None:
            attempt_rates.append(float(metrics["surfaced_attempt_error_rate"]))
        run_summaries.append({
            "run": index,
            "completed_response_rps": metrics["completed_response_rps"],
            "terminal_logical_error_rate": metrics["terminal_logical_error_rate"],
            "application_retry_rate": metrics["application_retry_rate"],
            "surfaced_attempt_error_rate": metrics["surfaced_attempt_error_rate"],
            "count_reconciled": metrics["count_reconciled"],
            "drain_complete": metrics["drain_complete"],
            "udp_deltas": metrics["udp_deltas"],
            "server_correlation": metrics["server_correlation"],
            "counts": metrics["counts"],
            "run_gates_passed": report.get("run_gates_passed") is True,
            "eligible_for_aggregation": report.get("eligible_for_aggregation") is True,
        })

    keys = [report.get("comparison_keys", {}).get("cohort_key") for report in reports]
    layered_key_sets = {
        field: {report.get("comparison_keys", {}).get(field) for report in reports}
        for field in ("platform_key", "client_build_key", "workload_key", "treatment_key")
    }
    if keys and (
        len(set(keys)) != 1
        or any(len(values) != 1 for values in layered_key_sets.values())
    ):
        reasons.append(f"{label}:mixed_cohort_key")
    summary = summarize_numbers(rates)
    udp_fields = ("receive_buffer_errors_delta", "packet_drops_delta", "unexplained_delta")
    udp_totals = {
        field: sum(
            run["udp_deltas"][field]
            for run in run_summaries
            if isinstance(run["udp_deltas"].get(field), int)
        )
        for field in udp_fields
    }
    count_fields = (
        "successful_logical_responses", "completed_responses", "scheduled_logical_inputs",
        "terminal_logical_outcomes", "terminal_logical_errors", "retries",
        "application_attempts", "transport_errors",
    )
    count_totals = {
        field: sum(run["counts"][field] for run in run_summaries)
        for field in count_fields
        if all(isinstance(run["counts"].get(field), int) for run in run_summaries)
    }
    pooled_rates = {
        "terminal_logical_error_rate": (
            count_totals["terminal_logical_errors"] / count_totals["scheduled_logical_inputs"]
            if count_totals.get("scheduled_logical_inputs") else None
        ),
        "application_retry_rate": (
            count_totals["retries"] / count_totals["scheduled_logical_inputs"]
            if count_totals.get("scheduled_logical_inputs") else None
        ),
        "surfaced_attempt_error_rate": (
            count_totals["transport_errors"] / count_totals["application_attempts"]
            if count_totals.get("application_attempts") else None
        ),
    }
    summary.update(
        runs=run_summaries,
        terminal_logical_error_rate=summarize_numbers(terminal_rates),
        application_retry_rate=summarize_numbers(retry_rates),
        surfaced_attempt_error_rate=summarize_numbers(attempt_rates),
        terminal_logical_error_rate_max=max(terminal_rates, default=None),
        application_retry_rate_max=max(retry_rates, default=None),
        surfaced_attempt_error_rate_max=max(attempt_rates, default=None),
        count_reconciliation_passed=(len(run_summaries) == 5 and all(run["count_reconciled"] for run in run_summaries)),
        drain_complete=(len(run_summaries) == 5 and all(run["drain_complete"] for run in run_summaries)),
        attributable_udp_deltas=udp_totals,
        count_totals=count_totals,
        pooled_error_rates=pooled_rates,
        cohort_key=keys[0] if keys and len(set(keys)) == 1 else None,
    )
    return summary, reasons


def evaluate_certification(
    adaptive_reports: list[dict[str, Any]],
    selected_fixed_reports: list[dict[str, Any]],
    *,
    target_rps: float,
    prerequisites: dict[str, dict[str, Any]],
) -> dict[str, Any]:
    reasons: list[str] = []
    adaptive, adaptive_reasons = _cohort(adaptive_reports, "adaptive", "adaptive")
    selected, selected_reasons = _cohort(
        selected_fixed_reports, "selected-fixed", "selected_fixed"
    )
    reasons.extend(adaptive_reasons)
    reasons.extend(selected_reasons)
    generation_valid = not any(reason.endswith("main_request_span_below_30_seconds") for reason in reasons)
    if not generation_valid:
        reasons.append("wordlist_generation_invalid_short_measured_trial")

    for name in ("fixed_sweep", "files_validation", "diagnostics", "wordlist"):
        prerequisite = prerequisites.get(name)
        if not isinstance(prerequisite, dict) or prerequisite.get("verified") is not True:
            reasons.append(f"prerequisite:{name}_unverified")
        elif prerequisite.get("eligible") is not True and prerequisite.get("frozen") is not True:
            reasons.append(f"prerequisite:{name}_ineligible")

    adaptive_keys = adaptive_reports[0].get("comparison_keys", {}) if adaptive_reports else {}
    selected_keys = selected_fixed_reports[0].get("comparison_keys", {}) if selected_fixed_reports else {}
    for field in ("platform_key", "client_build_key", "workload_key"):
        if adaptive_keys.get(field) != selected_keys.get(field):
            reasons.append(f"cohorts:{field}_mismatch")
    adaptive_treatment = adaptive_keys.get("treatment", {})
    selected_treatment = selected_keys.get("treatment", {})
    if adaptive_treatment.get("streams") != selected_treatment.get("streams"):
        reasons.append("cohorts:stream_mismatch")
    allowed_treatment_fields = {"mode", "connection_policy", "worker_policy"}
    treatment_fields = set(adaptive_treatment) | set(selected_treatment)
    for field in sorted(treatment_fields - allowed_treatment_fields):
        if adaptive_treatment.get(field) != selected_treatment.get(field):
            reasons.append(f"cohorts:undeclared_treatment_difference:{field}")

    selection = prerequisites.get("selection")
    if not isinstance(selection, dict):
        reasons.append("prerequisite:selection_unverified")
    else:
        expected_adaptive = {
            "mode": "adaptive",
            "connection_policy": {"kind": "adaptive", "maximum": selection.get("adaptive_max_connections")},
            "streams": selection.get("streams"),
            "worker_policy": {"kind": "bounded", "maximum": selection.get("adaptive_max_workers")},
            "profile_flags": [],
        }
        expected_fixed = {
            "mode": "selected-fixed",
            "connection_policy": {"kind": "fixed", "value": selection.get("connections")},
            "streams": selection.get("streams"),
            "worker_policy": {"kind": "bounded", "maximum": selection.get("adaptive_max_workers")},
            "profile_flags": [],
        }
        if adaptive_treatment != expected_adaptive:
            reasons.append("cohorts:adaptive_treatment_not_selected_policy")
        if selected_treatment != expected_fixed:
            reasons.append("cohorts:selected_fixed_treatment_not_selected_policy")

    adaptive_median = adaptive.get("median")
    selected_median = selected.get("median")
    comparison_percent = None
    if isinstance(adaptive_median, (int, float)) and adaptive_median < target_rps:
        reasons.append("adaptive_median_below_target")
    if isinstance(adaptive_median, (int, float)) and isinstance(selected_median, (int, float)) and selected_median > 0:
        comparison_percent = 100.0 * adaptive_median / selected_median
        if comparison_percent < 90.0:
            reasons.append("adaptive_median_below_90_percent_of_fixed")
    else:
        reasons.append("cohort_median_unavailable")

    reasons = list(dict.fromkeys(reasons))
    return {
        "schema_version": REPORT_VERSION,
        "manifest_type": "h3fuzz-certification-report",
        "metric": "successful_logical_responses/main_request_span",
        "target_rps": target_rps,
        "minimum_main_request_span_seconds": MINIMUM_SPAN_SECONDS,
        "wordlist_generation_valid": generation_valid,
        "adaptive": adaptive,
        "selected_fixed": selected,
        "adaptive_vs_fixed_percent": comparison_percent,
        "prerequisites": copy.deepcopy(prerequisites),
        "certification_eligible": not reasons,
        "ineligible_reasons": reasons,
    }


def _manifest_kind(manifest: dict[str, Any], expected: str, path: Path) -> None:
    if manifest.get("schema_version") != 1 or manifest.get("manifest_type") != expected:
        raise ContractError(f"{path}: expected {expected} schema version 1")
    verify_manifest_hash(manifest, str(path))
    verify_relative_members(manifest, path)
    root = path.resolve().parent
    recorded = {member["path"] for member in manifest["members"]}
    actual = {
        candidate.relative_to(root).as_posix()
        for candidate in root.rglob("*")
        if candidate.is_file() and candidate.resolve() != path.resolve()
    }
    if recorded != actual:
        missing = sorted(recorded - actual)
        extra = sorted(actual - recorded)
        raise ContractError(f"{path}: member tree mismatch (missing={missing}, extra={extra})")


def _require_sha256(value: object, label: str) -> str:
    if not isinstance(value, str) or len(value) != 64 or any(character not in "0123456789abcdef" for character in value):
        raise ContractError(f"{label}: canonical SHA-256 key is required")
    return value


def _verify_member_reference(manifest: dict[str, Any], reference: object, label: str) -> None:
    if not isinstance(reference, dict) or set(reference) != {"path", "sha256"}:
        raise ContractError(f"{label}: exact path/hash member reference is required")
    if not isinstance(reference["path"], str):
        raise ContractError(f"{label}: member path must be a string")
    _require_sha256(reference["sha256"], f"{label}.sha256")
    member_hashes = {
        member.get("path"): member.get("sha256")
        for member in manifest["members"]
        if isinstance(member, dict)
    }
    if member_hashes.get(reference["path"]) != reference["sha256"]:
        raise ContractError(f"{label}: reference is absent from manifest members")


def verify_wordlist(wordlist: Path, manifest_path: Path, target_rps: int) -> dict[str, Any]:
    manifest = read_json(manifest_path)
    if manifest.get("schema_version") != 1 or manifest.get("manifest_type") != "h3fuzz-frozen-wordlist":
        raise ContractError(f"{manifest_path}: expected h3fuzz-frozen-wordlist schema version 1")
    verify_manifest_hash(manifest, str(manifest_path))
    if manifest.get("frozen") is not True:
        raise ContractError(f"{manifest_path}: wordlist is not frozen")
    if Path(str(manifest.get("path", ""))).resolve() != wordlist.resolve():
        raise ContractError(f"{manifest_path}: manifest path does not name the supplied wordlist")
    if manifest.get("target_seconds") != 30 or manifest.get("headroom_percent") != 20:
        raise ContractError(f"{manifest_path}: calibration formula contract mismatch")
    if not isinstance(manifest.get("target_rps"), int) or manifest["target_rps"] < target_rps:
        raise ContractError(f"{manifest_path}: calibrated target RPS is below certification target")
    if manifest.get("sha256") != file_sha256(wordlist):
        raise ContractError(f"{manifest_path}: frozen wordlist hash mismatch")
    line_count = sum(1 for _ in wordlist.open("rb"))
    if manifest.get("line_count") != line_count:
        raise ContractError(f"{manifest_path}: frozen wordlist line count mismatch")
    minimum = math.ceil(target_rps * 30 * 1.20)
    expanded = int(manifest.get("expanded_input_count", -1))
    extensions = manifest.get("extensions")
    if not isinstance(extensions, list) or any(not isinstance(value, str) or not value for value in extensions):
        raise ContractError(f"{manifest_path}: extensions must be a string array")
    if not extensions and expanded != line_count:
        raise ContractError(f"{manifest_path}: unextended expanded count must equal line count")
    if expanded < minimum:
        raise ContractError(f"{manifest_path}: frozen wordlist is undersized")
    attempts = manifest.get("calibration_attempts")
    if not isinstance(attempts, list) or not 1 <= len(attempts) <= 3:
        raise ContractError(f"{manifest_path}: one to three calibration attempts are required")
    final_span = attempts[-1].get("main_request_span_seconds") if isinstance(attempts[-1], dict) else None
    if not isinstance(final_span, (int, float)) or final_span < 30:
        raise ContractError(f"{manifest_path}: final calibration did not prove a 30 second span")
    return manifest


def verify_fixed_sweep(path: Path) -> tuple[dict[str, Any], dict[str, Any]]:
    manifest = read_json(path)
    _manifest_kind(manifest, "h3fuzz-fixed-sweep", path)
    shared_keys = {
        field: _require_sha256(manifest.get(field), f"{path}: {field}")
        for field in ("platform_key", "client_build_key", "workload_key")
    }
    member_hashes = {
        member["path"]: member["sha256"] for member in manifest["members"]
        if isinstance(member, dict) and isinstance(member.get("path"), str)
    }
    points = manifest.get("points")
    policy = manifest.get("decision_policy")
    if not isinstance(points, list) or not points or not isinstance(policy, dict):
        raise ContractError(f"{path}: points and decision_policy are required")
    noise = _number(policy.get("noise_band_percent"))
    minimum_improvement = _number(policy.get("minimum_improvement_percent"))
    if noise is None or noise < 0 or minimum_improvement != max(3.0, noise):
        raise ContractError(f"{path}: invalid immutable decision policy")
    clean_points: list[tuple[float, int, int, str, str]] = []
    observed_authorities: set[str] = set()
    observed_server_identities: set[str] = set()
    for index, point in enumerate(points):
        if not isinstance(point, dict):
            raise ContractError(f"{path}: points[{index}] must be an object")
        connections = point.get("connections")
        streams = point.get("streams")
        if isinstance(connections, bool) or not isinstance(connections, int) or connections <= 0:
            raise ContractError(f"{path}: points[{index}].connections must be positive")
        if isinstance(streams, bool) or not isinstance(streams, int) or streams <= 0:
            raise ContractError(f"{path}: points[{index}].streams must be positive")
        point_treatment_key = _require_sha256(point.get("treatment_key"), f"{path}: points[{index}].treatment_key")
        point_cohort_key = _require_sha256(point.get("cohort_key"), f"{path}: points[{index}].cohort_key")
        report_refs = point.get("reports")
        if not isinstance(report_refs, list) or len(report_refs) != 3:
            raise ContractError(f"{path}: every executed point requires three reports")
        reports = []
        for report_index, reference in enumerate(report_refs):
            if not isinstance(reference, dict) or set(reference) != {"path", "sha256"}:
                raise ContractError(f"{path}: point {index} report {report_index} must be a path/hash reference")
            relative = reference["path"]
            digest = reference["sha256"]
            if member_hashes.get(relative) != digest:
                raise ContractError(f"{path}: point {index} report hash is absent from members")
            report_path = (path.parent / relative).resolve()
            try:
                report_path.relative_to(path.parent.resolve())
            except ValueError as exc:
                raise ContractError(f"{path}: point report escapes artifact root") from exc
            if file_sha256(report_path) != digest:
                raise ContractError(f"{path}: point {index} report hash mismatch")
            report = read_json(report_path)
            if report.get("aggregation_mode") != "sweep":
                raise ContractError(f"{path}: point {index} report {report_index} is not a sweep report")
            keys = report.get("comparison_keys")
            key_errors = comparison_key_errors(keys)
            if key_errors:
                raise ContractError(f"{path}: point {index} report {report_index} comparison keys: {','.join(key_errors)}")
            if any(keys.get(field) != value for field, value in shared_keys.items()):
                raise ContractError(f"{path}: point {index} report {report_index} shared key mismatch")
            treatment = keys["treatment"]
            if (
                treatment.get("mode") != "sweep"
                or treatment.get("streams") != streams
                or treatment.get("connection_policy") != {"kind": "fixed", "value": connections}
                or treatment.get("profile_flags") != []
                or keys.get("treatment_key") != point_treatment_key
                or keys.get("cohort_key") != point_cohort_key
            ):
                raise ContractError(f"{path}: point {index} report {report_index} treatment mismatch")
            authority = keys["workload"].get("authority")
            server_identity = keys["platform"].get("server_host_identity")
            if not isinstance(authority, str) or not authority or not isinstance(server_identity, str) or not server_identity:
                raise ContractError(f"{path}: point {index} report {report_index} lacks authority/server identity")
            observed_authorities.add(authority)
            observed_server_identities.add(server_identity)
            reports.append(report)
        rates = [_number(item.get("completed_response_rps")) for item in reports if isinstance(item, dict)]
        clean = all(
            isinstance(item, dict)
            and item.get("run_gates_passed") is True
            and item.get("eligible_for_aggregation") is True
            for item in reports
        ) and all(rate is not None for rate in rates)
        median = statistics.median(rate for rate in rates if rate is not None) if clean else None
        if point.get("clean") is not clean or (clean and point.get("median_completed_response_rps") != median):
            raise ContractError(f"{path}: point {index} selection calculation mismatch")
        if clean and median is not None:
            clean_points.append((median, connections, streams, point_treatment_key, point_cohort_key))
    if not clean_points:
        raise ContractError(f"{path}: no clean sweep point")
    best = max(value[0] for value in clean_points)
    near = [value for value in clean_points if 100.0 * (best - value[0]) / best <= minimum_improvement]
    chosen = min(near, key=lambda value: (value[1] * value[2], value[1], value[2]))
    selected = manifest.get("selected_point")
    if not isinstance(selected, dict) or selected.get("connections") != chosen[1] or selected.get("streams") != chosen[2]:
        raise ContractError(f"{path}: selected point does not match deterministic rule")
    if manifest.get("adaptive_max_connections") != chosen[1]:
        raise ContractError(f"{path}: adaptive_max_connections must equal selected connections")
    if not isinstance(manifest.get("adaptive_max_workers"), int) or manifest["adaptive_max_workers"] <= 0:
        raise ContractError(f"{path}: adaptive_max_workers is required")
    if len(observed_authorities) != 1 or manifest.get("authority") not in observed_authorities:
        raise ContractError(f"{path}: authority must match every sweep report")
    if len(observed_server_identities) != 1 or manifest.get("server_identity") not in observed_server_identities:
        raise ContractError(f"{path}: server_identity must match every sweep report")
    if selected.get("treatment_key") != chosen[3] or selected.get("cohort_key") != chosen[4]:
        raise ContractError(f"{path}: selected point key mismatch")
    return manifest, {
        "connections": chosen[1],
        "streams": chosen[2],
        "adaptive_max_connections": chosen[1],
        "adaptive_max_workers": manifest["adaptive_max_workers"],
        "fixed_treatment_key": chosen[3],
        "fixed_cohort_key": chosen[4],
        "platform_key": shared_keys["platform_key"],
        "client_build_key": shared_keys["client_build_key"],
        "workload_key": shared_keys["workload_key"],
        "authority": manifest["authority"],
        "server_identity": manifest["server_identity"],
    }


def verify_prerequisites(args: argparse.Namespace) -> dict[str, Any]:
    wordlist = verify_wordlist(args.wordlist, args.wordlist_manifest, args.target_rps)
    sweep, selection = verify_fixed_sweep(args.fixed_sweep_manifest)
    files = read_json(args.files_validation_manifest)
    diagnostics = read_json(args.diagnostics_manifest)
    _manifest_kind(files, "h3fuzz-files-validation", args.files_validation_manifest)
    _manifest_kind(diagnostics, "h3fuzz-diagnostics", args.diagnostics_manifest)
    correctness = files.get("correctness")
    if (
        files.get("eligible") is not True
        or not isinstance(correctness, dict)
        or correctness.get("passed") is not True
        or not isinstance(correctness.get("expected_hits"), int)
        or correctness["expected_hits"] <= 0
        or correctness.get("actual_hits") != correctness["expected_hits"]
        or correctness.get("false_positives") != 0
        or correctness.get("filter_matches_get_control") is not True
    ):
        raise ContractError(f"{args.files_validation_manifest}: discovery validation is ineligible")
    files_runs = files.get("runs")
    if not isinstance(files_runs, dict) or set(files_runs) != {"exact_hit", "long_corpus"}:
        raise ContractError(f"{args.files_validation_manifest}: exact-hit and long-corpus runs are required")
    for run_name, reference in files_runs.items():
        _verify_member_reference(files, reference, f"{args.files_validation_manifest}: runs.{run_name}")
    if not isinstance(files.get("duplicate_accounting"), str) or not files["duplicate_accounting"]:
        raise ContractError(f"{args.files_validation_manifest}: duplicate accounting is required")
    required_profiles = {"cpu", "heap", "mutex", "block", "quic"}
    profiles = diagnostics.get("profiles")
    if diagnostics.get("eligible") is not True or not isinstance(profiles, dict) or set(profiles) != required_profiles:
        raise ContractError(f"{args.diagnostics_manifest}: exact diagnostic profile set is required")
    for name, profile in profiles.items():
        if (
            not isinstance(profile, dict)
            or profile.get("paired_control") is not True
            or profile.get("profile_flags") != [name]
            or profile.get("control_profile_flags") != []
        ):
            raise ContractError(f"{args.diagnostics_manifest}: {name} lacks a paired control")
        _require_sha256(profile.get("profile_treatment_key"), f"{args.diagnostics_manifest}: {name}.profile_treatment_key")
        _require_sha256(profile.get("control_treatment_key"), f"{args.diagnostics_manifest}: {name}.control_treatment_key")
        _verify_member_reference(diagnostics, profile.get("profile_report"), f"{args.diagnostics_manifest}: {name}.profile_report")
        _verify_member_reference(diagnostics, profile.get("control_report"), f"{args.diagnostics_manifest}: {name}.control_report")
    for manifest, label in ((files, "files"), (diagnostics, "diagnostics")):
        for field in ("platform_key", "client_build_key"):
            _require_sha256(manifest.get(field), f"{label}: {field}")
            if manifest.get(field) != sweep.get(field):
                raise ContractError(f"{label}: {field} mismatch with sweep")
        for field in ("authority", "server_identity"):
            if manifest.get(field) != sweep.get(field):
                raise ContractError(f"{label}: {field} mismatch with sweep")
    _require_sha256(files.get("workload_key"), "files: workload_key")
    if files["workload_key"] == sweep.get("workload_key") or files.get("route") != "/files/FUZZ":
        raise ContractError("files: distinct /files/FUZZ workload is required")
    _require_sha256(diagnostics.get("workload_key"), "diagnostics: workload_key")
    if diagnostics.get("workload_key") != sweep.get("workload_key"):
        raise ContractError("diagnostics: workload_key mismatch with sweep")
    expected_point = {
        "connections": selection["connections"],
        "streams": selection["streams"],
        "treatment_key": selection["fixed_treatment_key"],
        "cohort_key": selection["fixed_cohort_key"],
    }
    if diagnostics.get("selected_point") != expected_point:
        raise ContractError("diagnostics: selected_point mismatch with sweep")
    return {
        "selection": selection,
        "fixed_sweep": {"verified": True, "eligible": True, "manifest_sha256": sweep["manifest_sha256"]},
        "files_validation": {"verified": True, "eligible": True, "manifest_sha256": files["manifest_sha256"]},
        "diagnostics": {"verified": True, "eligible": True, "manifest_sha256": diagnostics["manifest_sha256"]},
        "wordlist": {"verified": True, "frozen": True, "manifest_sha256": wordlist["manifest_sha256"]},
    }


def markdown_report(report: dict[str, Any]) -> str:
    adaptive = report["adaptive"]
    selected = report["selected_fixed"]
    status = "ELIGIBLE" if report["certification_eligible"] else "INELIGIBLE"
    lines = [
        "# h3fuzz certification report",
        "",
        f"Status: **{status}**",
        "",
        f"- Adaptive median completed-response RPS: {adaptive['median']}",
        f"- Selected-fixed median completed-response RPS: {selected['median']}",
        f"- Adaptive/fixed percentage: {report['adaptive_vs_fixed_percent']}",
        f"- Metric: `{report['metric']}`",
    ]
    if report["ineligible_reasons"]:
        lines.extend(["", "## Ineligible reasons", ""])
        lines.extend(f"- `{reason}`" for reason in report["ineligible_reasons"])
    return "\n".join(lines) + "\n"


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--adaptive-report", type=Path, action="append", default=[])
    parser.add_argument("--selected-fixed-report", type=Path, action="append", default=[])
    parser.add_argument("--wordlist", type=Path)
    parser.add_argument("--wordlist-manifest", type=Path)
    parser.add_argument("--fixed-sweep-manifest", type=Path)
    parser.add_argument("--files-validation-manifest", type=Path)
    parser.add_argument("--diagnostics-manifest", type=Path)
    parser.add_argument("--target-rps", type=int, required=True)
    parser.add_argument("--preflight-output", type=Path)
    parser.add_argument("--output-json", type=Path)
    parser.add_argument("--output-markdown", type=Path)
    parser.add_argument("--require-eligible", action="store_true")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(argv)
        required = (
            "wordlist", "wordlist_manifest", "fixed_sweep_manifest",
            "files_validation_manifest", "diagnostics_manifest",
        )
        if any(getattr(args, field) is None for field in required):
            raise ContractError("all prerequisite manifest arguments are required")
        prerequisites = verify_prerequisites(args)
        if args.preflight_output is not None:
            atomic_write_json(args.preflight_output, add_manifest_hash({
                "schema_version": 1,
                "manifest_type": "h3fuzz-certification-preflight",
                "created_at": utc_now(),
                **prerequisites,
            }))
            return 0
        if len(args.adaptive_report) != 5 or len(args.selected_fixed_report) != 5:
            raise ContractError("exactly five adaptive and five selected-fixed reports are required")
        if args.output_json is None or args.output_markdown is None:
            raise ContractError("--output-json and --output-markdown are required")
        adaptive = [read_json(path) for path in args.adaptive_report]
        selected = [read_json(path) for path in args.selected_fixed_report]
        report = evaluate_certification(
            adaptive,
            selected,
            target_rps=args.target_rps,
            prerequisites={name: prerequisites[name] for name in (
                "fixed_sweep", "files_validation", "diagnostics", "wordlist"
            )},
        )
        report["created_at"] = utc_now()
        report["source_reports"] = {
            "adaptive": [{"path": str(path), "sha256": file_sha256(path)} for path in args.adaptive_report],
            "selected_fixed": [{"path": str(path), "sha256": file_sha256(path)} for path in args.selected_fixed_report],
        }
        report = add_manifest_hash(report)
        atomic_write_json(args.output_json, report)
        args.output_markdown.parent.mkdir(parents=True, exist_ok=True)
        if args.output_markdown.exists():
            raise ContractError(f"refusing to overwrite immutable artifact: {args.output_markdown}")
        args.output_markdown.write_text(markdown_report(report), encoding="utf-8")
        return 0 if report["certification_eligible"] or not args.require_eligible else 3
    except ContractError as exc:
        print(f"certification-report: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:  # stable unexpected-tooling code
        print(f"certification-report: unexpected error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
