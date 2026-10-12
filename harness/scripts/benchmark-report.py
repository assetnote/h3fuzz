#!/usr/bin/env python3
"""Build a truthful wrapper report for a fixed-size h3fuzz benchmark."""

import argparse
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from perf004_contract import (  # noqa: E402
    ContractError,
    atomic_write_json,
    comparison_key_errors,
    comparison_keys,
    file_sha256,
)
from sweep_resource_usage import (  # noqa: E402
    ResourceEvidenceError,
    load_and_validate as load_sweep_resource_evidence,
)


def read_json(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {"available": False, "counters": {}}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--wordlist", type=Path, required=True)
    parser.add_argument("--telemetry", type=Path, required=True)
    parser.add_argument("--before", type=Path, required=True)
    parser.add_argument("--after", type=Path, required=True)
    parser.add_argument("--start-ns", type=int, required=True)
    parser.add_argument("--end-ns", type=int, required=True)
    parser.add_argument("--exit-code", type=int, required=True)
    parser.add_argument("--target", required=True)
    parser.add_argument("--mode", required=True)
    parser.add_argument("--expect-strategy", choices=("GET", "HEAD", "RANGE"), required=True)
    parser.add_argument("--connections", type=int, required=True)
    parser.add_argument("--max-connections", type=int, required=True)
    parser.add_argument(
        "--aggregation-mode",
        choices=("ordinary", "baseline", "diagnostic", "sweep", "adaptive", "selected-fixed"),
        default="ordinary",
    )
    parser.add_argument("--comparison-metadata", type=Path)
    parser.add_argument("--origin-correlation", type=Path)
    parser.add_argument("--network-evidence", type=Path, action="append", default=[])
    parser.add_argument("--sweep-resource-evidence", type=Path)
    parser.add_argument("--minimum-main-span-seconds", type=float, default=30.0)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--require-run-valid", action="store_true")
    parser.add_argument("--require-aggregation-eligible", action="store_true")
    args = parser.parse_args()

    with args.wordlist.open("rb") as wordlist_file:
        input_count = sum(1 for _ in wordlist_file)
    elapsed_seconds = max((args.end_ns - args.start_ns) / 1_000_000_000, 0.0)
    logical_rps = input_count / elapsed_seconds if elapsed_seconds else 0.0

    before = read_json(args.before)
    after = read_json(args.after)
    before_counters = before.get("counters", {}) if before.get("available") else {}
    after_counters = after.get("counters", {}) if after.get("available") else {}
    deltas = {
        labels: value - float(before_counters.get(labels, 0.0))
        for labels, value in after_counters.items()
        if value - float(before_counters.get(labels, 0.0)) > 0
    }
    max_server_delta = max(deltas.values(), default=None)
    server_correlated = (
        args.exit_code == 0
        and max_server_delta is not None
        and abs(max_server_delta - input_count) <= max(1.0, input_count * 0.001)
    )

    telemetry = read_json(args.telemetry)
    telemetry_schema_version = telemetry.get("schema_version")
    telemetry_available = telemetry_schema_version in (1, 2)
    telemetry_v2 = telemetry_schema_version == 2
    protocol = telemetry.get("protocol", {}) if telemetry_available else {}
    strategy = telemetry.get("request_strategy", {}) if telemetry_available else {}
    responses = telemetry.get("responses", {}) if telemetry_available else {}
    logical_ledger = telemetry.get("logical_ledger", {}) if telemetry_v2 else {}
    transport = telemetry.get("transport", {}) if telemetry_available else {}
    eligibility = telemetry.get("eligibility", {}) if telemetry_v2 else {}
    drain = telemetry.get("drain", {}) if telemetry_available else {}

    protocol_proven = (
        protocol.get("selected") == "http3"
        and protocol.get("http3_selected") is True
        and int(protocol.get("http3_initial_connections", 0)) > 0
        and int(protocol.get("http3_final_connections", 0)) > 0
    )
    selected_strategy = str(strategy.get("selected_optimization", "")).upper()
    method = str(strategy.get("method", "")).upper()
    range_header = str(strategy.get("range", ""))
    automatic = strategy.get("automatic_optimization_enabled") is True
    mode_mechanics_proven = {
        "auto": automatic and method == "GET",
        "get": not automatic and method == "GET" and not range_header,
        "head": not automatic and method == "HEAD" and not range_header,
        "range": not automatic and method == "GET" and range_header == "bytes=-1",
    }.get(args.mode, False)
    strategy_proven = mode_mechanics_proven and selected_strategy == args.expect_strategy

    optimized_attempts = int(strategy.get("optimized_attempts", -1))
    optimized_successes = int(strategy.get("optimized_successes", -1))
    full_get_fallbacks = int(strategy.get("full_get_fallbacks", -1))
    fallback_transport_errors = int(strategy.get("fallback_transport_errors", -1))
    fallback_untrustworthy = int(strategy.get("fallback_untrustworthy_responses", -1))
    optimization_accounted = (
        optimized_attempts >= 0
        and optimized_successes >= 0
        and full_get_fallbacks >= 0
        and fallback_transport_errors >= 0
        and fallback_untrustworthy >= 0
        and optimized_attempts == optimized_successes + full_get_fallbacks
        and full_get_fallbacks == fallback_transport_errors + fallback_untrustworthy
    )
    fallback_rate = (
        full_get_fallbacks / optimized_attempts if optimized_attempts > 0 else 0.0
    )
    if telemetry_v2:
        scheduled = int(responses.get("scheduled_logical_inputs", -1))
        terminal_outcomes = int(responses.get("terminal_logical_outcomes", -1))
        completed = int(responses.get("successful_logical_responses", -1))
        response_errors = int(responses.get("terminal_logical_errors", -1))
        retries = int(transport.get("application_retries", -1))
        raw_main_span_ns = responses.get("main_request_span_ns")
        main_request_span_ns = (
            int(raw_main_span_ns)
            if isinstance(raw_main_span_ns, (int, float)) and int(raw_main_span_ns) > 0
            else None
        )
    else:
        scheduled = int(responses.get("scheduled", -1))
        completed = int(responses.get("completed_responses", -1))
        response_errors = int(responses.get("errors", -1))
        retries = int(responses.get("retries", -1))
        terminal_outcomes = completed + response_errors if completed >= 0 and response_errors >= 0 else -1
        main_request_span_ns = None
    responses_accounted = (
        scheduled == input_count
        and completed >= 0
        and response_errors >= 0
        and terminal_outcomes == scheduled
        and completed + response_errors == terminal_outcomes
    )
    expects_automatic_optimization = args.mode == "auto" and args.expect_strategy in {"HEAD", "RANGE"}
    expected_optimized_attempts = scheduled + retries if expects_automatic_optimization else 0
    if expects_automatic_optimization:
        optimization_proven = (
            optimization_accounted
            and optimized_attempts == expected_optimized_attempts
            and optimized_attempts > 0
            and optimized_successes > 0
            and fallback_rate <= 0.001
        )
    else:
        optimization_proven = optimization_accounted and optimized_attempts == 0
    if telemetry_v2:
        application_attempts = int(transport.get("application_attempts", -1))
        transport_completed = int(transport.get("completed_attempts", -1))
        transport_errors = int(transport.get("surfaced_attempt_errors", -1))
        transport_optimized_attempts = int(transport.get("optimized_attempts", -1))
        transport_full_get_recoveries = int(transport.get("full_get_recoveries", -1))
        attempt_roles = transport.get("attempt_roles", {})
        role_initial = int(attempt_roles.get("initial", -1))
        role_fallback = int(attempt_roles.get("optimization_fallback", -1))
        role_retry = int(attempt_roles.get("engine_retry", -1))
        role_retry_fallback = int(attempt_roles.get("engine_retry_optimization_fallback", -1))
        attempt_methods = transport.get("methods", {})
        method_get = int(attempt_methods.get("GET", -1))
        method_head = int(attempt_methods.get("HEAD", -1))
        method_other = int(attempt_methods.get("other", -1))
        roles_accounted = (
            min(role_initial, role_fallback, role_retry, role_retry_fallback) >= 0
            and role_initial + role_fallback + role_retry + role_retry_fallback == application_attempts
        )
        methods_accounted = (
            min(method_get, method_head, method_other) >= 0
            and method_get + method_head + method_other == application_attempts
        )
        transport_proven = (
            application_attempts >= 0
            and transport_completed >= 0
            and transport_errors >= 0
            and transport_completed + transport_errors == application_attempts
            and role_initial == scheduled
            and role_retry == retries
            and transport_full_get_recoveries == role_fallback + role_retry_fallback
            and transport_optimized_attempts == optimized_attempts
            and transport_full_get_recoveries == full_get_fallbacks
            and roles_accounted
            and methods_accounted
            and int(transport.get("in_flight", -1)) == 0
        )
    else:
        application_attempts = -1
        transport_completed = int(transport.get("completed_responses", -1))
        transport_errors = int(transport.get("errors", -1))
        transport_optimized_attempts = -1
        transport_full_get_recoveries = -1
        attempt_roles = {}
        attempt_methods = {}
        roles_accounted = False
        methods_accounted = False
        transport_proven = (
            transport_completed >= completed >= 0
            and transport_errors == 0
            and int(transport.get("in_flight", -1)) == 0
        )

    if telemetry_v2:
        expected_expanded_inputs = logical_ledger.get("expected_expanded_inputs")
        assigned_ids = int(logical_ledger.get("assigned_ids", -1))
        scheduled_transitions = int(logical_ledger.get("scheduled_transitions", -1))
        success_terminal_states = int(logical_ledger.get("success_terminal_states", -1))
        error_terminal_states = int(logical_ledger.get("error_terminal_states", -1))
        ledger_anomaly_fields = (
            "pending_scheduled_states",
            "id_gaps",
            "duplicate_schedule_transitions",
            "duplicate_terminal_transitions",
            "invalid_transitions",
            "out_of_range_ids",
        )
        logical_ledger_proven = (
            isinstance(expected_expanded_inputs, int)
            and expected_expanded_inputs == input_count
            and expected_expanded_inputs == assigned_ids == scheduled_transitions
            and scheduled_transitions == success_terminal_states + error_terminal_states
            and success_terminal_states == completed
            and error_terminal_states == response_errors
            and all(int(logical_ledger.get(field, -1)) == 0 for field in ledger_anomaly_fields)
        )
        main_request_timing_proven = (
            (scheduled == 0 and main_request_span_ns is None)
            or (scheduled > 0 and main_request_span_ns is not None)
        )
    else:
        expected_expanded_inputs = None
        assigned_ids = -1
        scheduled_transitions = -1
        success_terminal_states = -1
        error_terminal_states = -1
        logical_ledger_proven = True
        main_request_timing_proven = True
    eligibility_proven = not telemetry_v2 or eligibility.get("eligible") is True
    drain_complete = (
        drain.get("complete") is True
        and drain.get("workers_drained") is True
        and drain.get("runner_closed") is True
        and not drain.get("close_error")
        and int(drain.get("limiter_in_use", -1)) == 0
        and int(drain.get("limiter_waiters", -1)) == 0
        and int(transport.get("in_flight", -1)) == 0
    )
    if telemetry_v2:
        main_request_span_seconds = (
            main_request_span_ns / 1_000_000_000 if main_request_span_ns is not None else None
        )
        completed_response_rps = (
            completed / main_request_span_seconds
            if main_request_span_seconds and completed >= 0
            else None
        )
        rps_measurement_basis = "successful_logical_responses/main_request_span"
    else:
        main_request_span_seconds = None
        completed_response_rps = completed / elapsed_seconds if elapsed_seconds and completed >= 0 else 0.0
        rps_measurement_basis = "completed_responses/process_wall_time_legacy_v1"
    terminal_logical_error_rate = (
        response_errors / scheduled if scheduled > 0 and response_errors >= 0 else None
    )
    application_retry_rate = retries / scheduled if scheduled > 0 and retries >= 0 else None
    surfaced_attempt_error_rate = (
        transport_errors / application_attempts
        if application_attempts > 0 and transport_errors >= 0
        else None
    )
    if telemetry_v2:
        error_rate_gates_passed = (
            scheduled == 0
            and application_attempts == 0
            and response_errors == retries == transport_errors == 0
        ) or (
            terminal_logical_error_rate is not None
            and terminal_logical_error_rate < 0.001
            and application_retry_rate is not None
            and application_retry_rate < 0.001
            and surfaced_attempt_error_rate is not None
            and surfaced_attempt_error_rate < 0.001
        )
    else:
        error_rate_gates_passed = response_errors == 0 and retries == 0 and transport_errors == 0

    run_valid = (
        args.exit_code == 0
        and telemetry_available
        and protocol_proven
        and strategy_proven
        and optimization_proven
        and responses_accounted
        and logical_ledger_proven
        and main_request_timing_proven
        and eligibility_proven
        and error_rate_gates_passed
        and completed == input_count
        and transport_proven
        and drain_complete
    )

    server_expected_requests = (
        application_attempts if telemetry_v2 and application_attempts >= 0 else input_count
    )
    server_correlated = (
        args.exit_code == 0
        and max_server_delta is not None
        and abs(max_server_delta - server_expected_requests)
        <= max(1.0, server_expected_requests * 0.001)
    )

    comparison = None
    comparison_errors = []
    if args.comparison_metadata is not None:
        metadata = read_json(args.comparison_metadata)
        try:
            comparison = comparison_keys(metadata)
            comparison_errors = comparison_key_errors(comparison)
            if metadata.get("eligible") is not True:
                comparison_errors.extend(
                    str(value) for value in metadata.get("ineligibility_reasons", ["comparison_metadata_ineligible"])
                )
        except ContractError as exc:
            comparison_errors = [str(exc)]

    origin_correlation = read_json(args.origin_correlation) if args.origin_correlation else {}
    origin_selected = origin_correlation.get("selected", {}) if isinstance(origin_correlation, dict) else {}
    origin_error_rate = origin_correlation.get("handler_reconciliation_error")
    origin_correlation_available = (
        origin_correlation.get("schema_version") == 1
        and origin_correlation.get("kind") == "origin_correlation"
        and isinstance(origin_error_rate, (int, float))
    )
    origin_correlation_proven = (
        origin_correlation_available
        and origin_correlation.get("eligible") is True
        and float(origin_error_rate) <= 0.001
    )
    server_correlation = {
        "available": origin_correlation_available,
        "eligible": origin_correlation.get("eligible") if origin_correlation_available else False,
        "expected_handler_requests": origin_correlation.get("expected_handler_requests"),
        "observed_handler_requests": origin_selected.get("handler_requests"),
        "error_rate": origin_error_rate if origin_correlation_available else None,
        "ineligibility_reasons": origin_correlation.get("ineligibility_reasons", []),
    }

    receive_buffer_errors_delta = 0
    packet_drops_delta = 0
    network_evidence_available = bool(args.network_evidence)
    network_evidence_eligible = bool(args.network_evidence)
    network_reasons = []
    for evidence_path in args.network_evidence:
        evidence = read_json(evidence_path)
        if evidence.get("schema_version") != 1 or evidence.get("kind") != "network_delta":
            network_evidence_available = False
            network_evidence_eligible = False
            network_reasons.append(f"invalid_network_evidence:{evidence_path}")
            continue
        if evidence.get("eligible") is not True:
            network_evidence_eligible = False
            network_reasons.extend(str(value) for value in evidence.get("ineligibility_reasons", []))
        for row in evidence.get("udp_deltas", []):
            delta = row.get("delta")
            if row.get("name") in {"RcvbufErrors", "InErrors", "InCsumErrors", "MemErrors"} and isinstance(delta, (int, str)) and str(delta).isdigit():
                receive_buffer_errors_delta += int(delta)
        for row in evidence.get("interface_deltas", []):
            for field in ("rx_dropped", "tx_dropped", "rx_errors", "tx_errors"):
                delta = row.get("fields", {}).get(field, {}).get("delta")
                if isinstance(delta, (int, str)) and str(delta).isdigit():
                    packet_drops_delta += int(delta)
    unexplained_delta = receive_buffer_errors_delta + packet_drops_delta
    udp_attribution = {
        "available": network_evidence_available,
        "eligible": network_evidence_eligible and unexplained_delta == 0,
        "receive_buffer_errors_delta": receive_buffer_errors_delta,
        "packet_drops_delta": packet_drops_delta,
        "unexplained_delta": unexplained_delta,
        "ineligibility_reasons": sorted(set(network_reasons)),
    }
    sweep_resource_usage_required = args.aggregation_mode == "sweep"
    sweep_resource_usage = None
    sweep_resource_usage_provenance = None
    sweep_resource_evidence_sha256 = None
    sweep_resource_usage_reasons: list[str] = []
    sweep_resource_usage_valid = False
    if args.sweep_resource_evidence is None:
        if sweep_resource_usage_required:
            sweep_resource_usage_reasons.append("sweep_resource_evidence_missing")
    else:
        try:
            telemetry_run_id = telemetry.get("run_id")
            if not isinstance(telemetry_run_id, str):
                raise ResourceEvidenceError("telemetry_run_id_missing")
            resource_evidence = load_sweep_resource_evidence(
                args.sweep_resource_evidence,
                expected_run_id=telemetry_run_id,
            )
            sweep_resource_evidence_sha256 = file_sha256(args.sweep_resource_evidence)
            if resource_evidence["eligible"] is True:
                sweep_resource_usage = resource_evidence["metrics"]
                sweep_resource_usage_provenance = resource_evidence["provenance"]
                sweep_resource_usage_valid = True
            else:
                sweep_resource_usage_reasons.extend(
                    str(value) for value in resource_evidence["ineligibility_reasons"]
                )
        except (ContractError, OSError) as exc:
            sweep_resource_usage_reasons.append(f"sweep_resource_evidence_invalid:{exc}")
    sweep_resource_usage_reasons = sorted(set(sweep_resource_usage_reasons))
    sweep_resource_gate_passed = (
        not sweep_resource_usage_required or sweep_resource_usage_valid
    )
    aggregating_mode = args.aggregation_mode in {"baseline", "sweep", "adaptive", "selected-fixed"}
    run_gates_passed = (
        run_valid
        and telemetry_v2
        and main_request_span_seconds is not None
        and main_request_span_seconds >= args.minimum_main_span_seconds
        and comparison is not None
        and not comparison_errors
        and origin_correlation_proven
        and udp_attribution["eligible"] is True
        and sweep_resource_gate_passed
    )
    eligible_for_aggregation = aggregating_mode and run_gates_passed

    report = {
        "report_schema_version": 2,
        "aggregation_mode": args.aggregation_mode,
        "target": args.target,
        "mode": args.mode,
        "http3_connections": args.connections,
        "http3_max_connections": args.max_connections,
        "logical_inputs": input_count,
        "process_exit_code": args.exit_code,
        "wall_seconds": elapsed_seconds,
        "process_wall_seconds": elapsed_seconds,
        "main_request_span_ns": main_request_span_ns,
        "main_request_span_seconds": main_request_span_seconds,
        "logical_rps": logical_rps,
        "completed_response_rps": completed_response_rps,
        "rps_measurement_basis": rps_measurement_basis,
        "telemetry_available": telemetry_available,
        "telemetry_schema_version": telemetry_schema_version,
        "selected_protocol": protocol.get("selected"),
        "protocol_selection_reason": protocol.get("selection_reason"),
        "http3_initial_connections": protocol.get("http3_initial_connections"),
        "http3_final_connections": protocol.get("http3_final_connections"),
        "http3_opened_connections": protocol.get("http3_opened_connections"),
        "protocol_proven": protocol_proven,
        "selected_response_strategy": selected_strategy or None,
        "expected_response_strategy": args.expect_strategy,
        "strategy_selection_reason": strategy.get("selection_reason"),
        "strategy_proven": strategy_proven,
        "optimized_attempts": optimized_attempts,
        "expected_optimized_attempts": expected_optimized_attempts,
        "optimized_successes": optimized_successes,
        "full_get_fallbacks": full_get_fallbacks,
        "fallback_transport_errors": fallback_transport_errors,
        "fallback_untrustworthy_responses": fallback_untrustworthy,
        "optimization_accounted": optimization_accounted,
        "optimization_fallback_rate": fallback_rate,
        "optimization_proven": optimization_proven,
        "scheduled_responses": scheduled,
        "scheduled_logical_inputs": scheduled,
        "terminal_logical_outcomes": terminal_outcomes,
        "completed_responses": completed,
        "successful_logical_responses": completed,
        "response_errors": response_errors,
        "terminal_logical_errors": response_errors,
        "retries": retries,
        "terminal_logical_error_rate": terminal_logical_error_rate,
        "application_retry_rate": application_retry_rate,
        "surfaced_attempt_error_rate": surfaced_attempt_error_rate,
        "error_rate_gates_passed": error_rate_gates_passed,
        "responses_accounted": responses_accounted,
        "expected_expanded_inputs": expected_expanded_inputs,
        "assigned_logical_input_ids": assigned_ids,
        "scheduled_ledger_transitions": scheduled_transitions,
        "success_terminal_states": success_terminal_states,
        "error_terminal_states": error_terminal_states,
        "logical_ledger_proven": logical_ledger_proven,
        "logical_ledger": logical_ledger if telemetry_v2 else None,
        "telemetry_eligibility": eligibility if telemetry_v2 else None,
        "telemetry_eligibility_proven": eligibility_proven,
        "application_attempts": application_attempts,
        "transport_completed_responses": transport_completed,
        "transport_errors": transport_errors,
        "transport_optimized_attempts": transport_optimized_attempts,
        "transport_full_get_recoveries": transport_full_get_recoveries,
        "attempt_roles": attempt_roles,
        "attempt_methods": attempt_methods,
        "attempt_roles_accounted": roles_accounted,
        "attempt_methods_accounted": methods_accounted,
        "transport_proven": transport_proven,
        "workers_drained": drain.get("workers_drained"),
        "runner_closed": drain.get("runner_closed"),
        "runner_close_error": drain.get("close_error"),
        "limiter_in_use": drain.get("limiter_in_use"),
        "limiter_waiters": drain.get("limiter_waiters"),
        "drain_complete": drain_complete,
        "metrics_available": bool(before.get("available") and after.get("available")),
        "server_counter_deltas": deltas,
        "server_max_series_delta": max_server_delta,
        "server_expected_application_requests": server_expected_requests,
        "server_correlated": server_correlated,
        "server_correlation": server_correlation,
        "udp_attribution": udp_attribution,
        "sweep_resource_usage_required": sweep_resource_usage_required,
        "sweep_resource_usage_valid": sweep_resource_usage_valid,
        "sweep_resource_usage_ineligibility_reasons": sweep_resource_usage_reasons,
        "sweep_resource_usage": sweep_resource_usage,
        "sweep_resource_usage_provenance": sweep_resource_usage_provenance,
        "sweep_resource_evidence_sha256": sweep_resource_evidence_sha256,
        "comparison_keys": comparison,
        "comparison_key_errors": comparison_errors,
        "run_gates_passed": run_gates_passed,
        "eligible_for_aggregation": eligible_for_aggregation,
        "run_valid": run_valid,
        "certification_eligible": False,
        "certification_note": (
            "A single run cannot certify the 100k milestone. Certification requires the "
            "documented duration, five-run median, server correlation, and UDP-drop evidence."
        ),
        "measurement_warning": (
            "logical_rps is inputs divided by process wall time and is diagnostic only. "
            "completed_response_rps uses successful logical responses and the monotonic "
            "main-request span for schema v2; schema v1 retains its legacy wall-time basis. "
            "Caddy correlation is supporting evidence only."
        ),
    }
    atomic_write_json(args.output, report, refuse_existing=False)
    print(json.dumps(report, sort_keys=True))
    if args.require_run_valid and not run_valid:
        return 3
    if args.require_aggregation_eligible and not eligible_for_aggregation:
        return 3
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
