#!/usr/bin/env python3

import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("benchmark-report.py")
SPEC = importlib.util.spec_from_file_location("benchmark_report", SCRIPT)
benchmark_report = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(benchmark_report)


class BenchmarkReportTest(unittest.TestCase):
    def run_report(
        self,
        telemetry: dict,
        mode: str = "range",
        expect_strategy: str = "RANGE",
        words: str = "one\ntwo\n",
        aggregation_mode: str = "ordinary",
        with_certification_evidence: bool = False,
        sweep_resource_evidence: dict | None = None,
        require_aggregation_eligible: bool = False,
    ) -> tuple[int, dict]:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            wordlist = root / "words.txt"
            wordlist.write_text(words, encoding="utf-8")
            metrics = root / "metrics.json"
            metrics.write_text('{"available": false, "counters": {}}\n', encoding="utf-8")
            telemetry_path = root / "telemetry.json"
            telemetry_path.write_text(json.dumps(telemetry), encoding="utf-8")
            output = root / "report.json"
            argv = [
                str(SCRIPT),
                "--wordlist", str(wordlist),
                "--telemetry", str(telemetry_path),
                "--before", str(metrics),
                "--after", str(metrics),
                "--start-ns", "1000000000",
                "--end-ns", "2000000000",
                "--exit-code", "0",
                "--target", "https://example.test/FUZZ",
                "--mode", mode,
                "--expect-strategy", expect_strategy,
                "--connections", "2",
                "--max-connections", "2",
                "--aggregation-mode", aggregation_mode,
                "--output", str(output),
                "--require-run-valid",
            ]
            if with_certification_evidence:
                metadata = root / "comparison.json"
                metadata.write_text(json.dumps({
                    "eligible": True,
                    "ineligibility_reasons": [],
                    "platform": {"host": "fixture"},
                    "client_build": {"binary": "fixture"},
                    "workload": {"target": "https://example.test/FUZZ"},
                    "treatment": {"mode": aggregation_mode, "streams": 256},
                }), encoding="utf-8")
                origin = root / "origin-correlation.json"
                origin.write_text(json.dumps({
                    "schema_version": 1,
                    "kind": "origin_correlation",
                    "eligible": True,
                    "reasons": [],
                    "expected_handler_requests": 2,
                    "handler_reconciliation_error": 0.0,
                    "selected": {"handler_requests": 2},
                }), encoding="utf-8")
                network = root / "network-delta.json"
                network.write_text(json.dumps({
                    "schema_version": 1,
                    "kind": "network_delta",
                    "eligible": True,
                    "ineligibility_reasons": [],
                    "udp_deltas": [],
                    "interface_deltas": [],
                }), encoding="utf-8")
                argv.extend([
                    "--comparison-metadata", str(metadata),
                    "--origin-correlation", str(origin),
                    "--network-evidence", str(network),
                ])
            if sweep_resource_evidence is not None:
                resource_path = root / "sweep-resource-usage.json"
                resource_path.write_text(
                    json.dumps(sweep_resource_evidence), encoding="utf-8"
                )
                argv.extend(["--sweep-resource-evidence", str(resource_path)])
            if require_aggregation_eligible:
                argv.append("--require-aggregation-eligible")
            with mock.patch.object(sys, "argv", argv):
                status = benchmark_report.main()
            return status, json.loads(output.read_text(encoding="utf-8"))

    def test_validates_fully_accounted_http3_range_run_without_certifying(self):
        status, report = self.run_report(valid_telemetry())
        self.assertEqual(status, 0)
        self.assertTrue(report["run_valid"])
        self.assertFalse(report["certification_eligible"])
        self.assertEqual(report["completed_responses"], 2)
        self.assertEqual(report["completed_response_rps"], 2.0)
        self.assertEqual(report["telemetry_schema_version"], 1)
        self.assertEqual(report["rps_measurement_basis"], "completed_responses/process_wall_time_legacy_v1")

    def test_schema_v2_uses_successes_and_monotonic_main_span_for_rps(self):
        status, report = self.run_report(valid_telemetry_v2())
        self.assertEqual(status, 0)
        self.assertTrue(report["run_valid"])
        self.assertEqual(report["telemetry_schema_version"], 2)
        self.assertEqual(report["process_wall_seconds"], 1.0)
        self.assertEqual(report["main_request_span_seconds"], 0.5)
        self.assertEqual(report["successful_logical_responses"], 2)
        self.assertEqual(report["completed_response_rps"], 4.0)
        self.assertEqual(report["rps_measurement_basis"], "successful_logical_responses/main_request_span")

    def test_schema_v2_separates_run_gates_from_aggregation_eligibility(self):
        telemetry = valid_telemetry_v2()
        telemetry["responses"]["main_request_span_ns"] = 30_000_000_000
        status, report = self.run_report(
            telemetry,
            aggregation_mode="adaptive",
            with_certification_evidence=True,
        )
        self.assertEqual(status, 0)
        self.assertTrue(report["run_gates_passed"])
        self.assertTrue(report["eligible_for_aggregation"])
        self.assertFalse(report["certification_eligible"])
        self.assertEqual(report["comparison_keys"]["workload"]["target"], "https://example.test/FUZZ")

    def test_sweep_requires_valid_resource_usage_and_retains_provenance(self):
        telemetry = valid_telemetry_v2()
        telemetry["run_id"] = RESOURCE_RUN_ID
        telemetry["responses"]["main_request_span_ns"] = 30_000_000_000
        status, report = self.run_report(
            telemetry,
            aggregation_mode="sweep",
            with_certification_evidence=True,
            sweep_resource_evidence=valid_sweep_resource_evidence(),
            require_aggregation_eligible=True,
        )
        self.assertEqual(status, 0)
        self.assertTrue(report["run_gates_passed"])
        self.assertTrue(report["eligible_for_aggregation"])
        self.assertTrue(report["sweep_resource_usage_valid"])
        self.assertEqual(report["sweep_resource_usage"]["p99_latency_ms"], 2.0)
        self.assertEqual(
            report["sweep_resource_usage_provenance"]["p99_latency_ms"]["source"],
            "run_telemetry.responses.latency",
        )
        self.assertRegex(report["sweep_resource_evidence_sha256"], r"^[0-9a-f]{64}$")

    def test_sweep_missing_resource_usage_fails_aggregation_closed(self):
        telemetry = valid_telemetry_v2()
        telemetry["run_id"] = RESOURCE_RUN_ID
        telemetry["responses"]["main_request_span_ns"] = 30_000_000_000
        status, report = self.run_report(
            telemetry,
            aggregation_mode="sweep",
            with_certification_evidence=True,
            require_aggregation_eligible=True,
        )
        self.assertEqual(status, 3)
        self.assertTrue(report["run_valid"])
        self.assertFalse(report["run_gates_passed"])
        self.assertFalse(report["eligible_for_aggregation"])
        self.assertIsNone(report["sweep_resource_usage"])
        self.assertIn(
            "sweep_resource_evidence_missing",
            report["sweep_resource_usage_ineligibility_reasons"],
        )

    def test_sweep_rejects_resource_metric_not_derived_from_provenance(self):
        telemetry = valid_telemetry_v2()
        telemetry["run_id"] = RESOURCE_RUN_ID
        telemetry["responses"]["main_request_span_ns"] = 30_000_000_000
        evidence = valid_sweep_resource_evidence()
        evidence["metrics"]["client_cpu_percent"] = 12.0
        status, report = self.run_report(
            telemetry,
            aggregation_mode="sweep",
            with_certification_evidence=True,
            sweep_resource_evidence=evidence,
            require_aggregation_eligible=True,
        )
        self.assertEqual(status, 3)
        self.assertFalse(report["sweep_resource_usage_valid"])
        self.assertIsNone(report["sweep_resource_usage"])
        self.assertTrue(any(
            "formula_mismatch" in reason
            for reason in report["sweep_resource_usage_ineligibility_reasons"]
        ))

    def test_schema_v2_empty_scan_has_no_span_and_no_rps(self):
        telemetry = valid_telemetry_v2()
        telemetry["responses"] = {
            "main_request_span_ns": None,
            "scheduled_logical_inputs": 0,
            "terminal_logical_outcomes": 0,
            "successful_logical_responses": 0,
            "terminal_logical_errors": 0,
        }
        telemetry["logical_ledger"].update({
            "expected_expanded_inputs": 0,
            "assigned_ids": 0,
            "scheduled_transitions": 0,
            "success_terminal_states": 0,
        })
        telemetry["transport"].update({
            "application_attempts": 0,
            "completed_attempts": 0,
        })
        telemetry["transport"]["attempt_roles"]["initial"] = 0
        telemetry["transport"]["methods"]["GET"] = 0
        status, report = self.run_report(telemetry, words="")
        self.assertEqual(status, 0)
        self.assertIsNone(report["main_request_span_ns"])
        self.assertIsNone(report["completed_response_rps"])

    def test_schema_v2_one_input_uses_actual_positive_span(self):
        telemetry = valid_telemetry_v2()
        telemetry["responses"].update({
            "main_request_span_ns": 250,
            "scheduled_logical_inputs": 1,
            "terminal_logical_outcomes": 1,
            "successful_logical_responses": 1,
        })
        telemetry["logical_ledger"].update({
            "expected_expanded_inputs": 1,
            "assigned_ids": 1,
            "scheduled_transitions": 1,
            "success_terminal_states": 1,
        })
        telemetry["transport"].update({
            "application_attempts": 1,
            "completed_attempts": 1,
        })
        telemetry["transport"]["attempt_roles"]["initial"] = 1
        telemetry["transport"]["methods"]["GET"] = 1
        status, report = self.run_report(telemetry, words="one\n")
        self.assertEqual(status, 0)
        self.assertEqual(report["main_request_span_ns"], 250)
        self.assertEqual(report["completed_response_rps"], 4_000_000.0)

    def test_schema_v2_rejects_balanced_lost_and_duplicate_ledger(self):
        telemetry = valid_telemetry_v2()
        telemetry["logical_ledger"].update({
            "success_terminal_states": 1,
            "error_terminal_states": 0,
            "pending_scheduled_states": 1,
            "duplicate_terminal_transitions": 1,
            "anomaly_sample_ids": [1, 2],
        })
        status, report = self.run_report(telemetry)
        self.assertEqual(status, 3)
        self.assertTrue(report["responses_accounted"])
        self.assertFalse(report["logical_ledger_proven"])
        self.assertFalse(report["run_valid"])

    def test_schema_v2_rejects_paused_measurement(self):
        telemetry = valid_telemetry_v2()
        telemetry["eligibility"] = {
            "eligible": False,
            "pause_observed": True,
            "request_delay": False,
            "rate_limited": False,
            "ineligible_reasons": ["pause_observed"],
        }
        status, report = self.run_report(telemetry)
        self.assertEqual(status, 3)
        self.assertFalse(report["telemetry_eligibility_proven"])
        self.assertFalse(report["run_valid"])

    def test_rejects_protocol_mismatch(self):
        telemetry = valid_telemetry()
        telemetry["protocol"]["selected"] = "http1.1"
        telemetry["protocol"]["http3_selected"] = False
        status, report = self.run_report(telemetry)
        self.assertEqual(status, 3)
        self.assertFalse(report["protocol_proven"])
        self.assertFalse(report["run_valid"])
        self.assertFalse(report["certification_eligible"])

    def test_rejects_auto_fallback_that_does_not_match_expected_strategy(self):
        telemetry = valid_telemetry()
        telemetry["request_strategy"] = {
            "method": "GET",
            "automatic_optimization_enabled": True,
            "selected_optimization": "GET",
        }
        status, report = self.run_report(telemetry, mode="auto", expect_strategy="HEAD")
        self.assertEqual(status, 3)
        self.assertEqual(report["expected_response_strategy"], "HEAD")
        self.assertEqual(report["selected_response_strategy"], "GET")
        self.assertFalse(report["strategy_proven"])
        self.assertFalse(report["run_valid"])

    def test_rejects_auto_head_with_zero_optimized_successes(self):
        telemetry = valid_telemetry()
        telemetry["request_strategy"] = {
            "method": "GET",
            "automatic_optimization_enabled": True,
            "selected_optimization": "HEAD",
            "optimized_attempts": 2,
            "optimized_successes": 0,
            "full_get_fallbacks": 2,
            "fallback_transport_errors": 0,
            "fallback_untrustworthy_responses": 2,
        }
        status, report = self.run_report(telemetry, mode="auto", expect_strategy="HEAD")
        self.assertEqual(status, 3)
        self.assertTrue(report["strategy_proven"])
        self.assertFalse(report["optimization_proven"])
        self.assertFalse(report["run_valid"])

    def test_validates_auto_head_when_every_input_was_optimized(self):
        telemetry = valid_telemetry()
        telemetry["request_strategy"] = {
            "method": "GET",
            "automatic_optimization_enabled": True,
            "selected_optimization": "HEAD",
            "optimized_attempts": 2,
            "optimized_successes": 2,
            "full_get_fallbacks": 0,
            "fallback_transport_errors": 0,
            "fallback_untrustworthy_responses": 0,
        }
        status, report = self.run_report(telemetry, mode="auto", expect_strategy="HEAD")
        self.assertEqual(status, 0)
        self.assertEqual(report["expected_optimized_attempts"], 2)
        self.assertTrue(report["optimization_proven"])
        self.assertTrue(report["run_valid"])

    def test_rejects_auto_head_that_did_not_optimize_every_scheduled_input(self):
        telemetry = valid_telemetry()
        telemetry["request_strategy"] = {
            "method": "GET",
            "automatic_optimization_enabled": True,
            "selected_optimization": "HEAD",
            "optimized_attempts": 1,
            "optimized_successes": 1,
            "full_get_fallbacks": 0,
            "fallback_transport_errors": 0,
            "fallback_untrustworthy_responses": 0,
        }
        status, report = self.run_report(telemetry, mode="auto", expect_strategy="HEAD")
        self.assertEqual(status, 3)
        self.assertTrue(report["strategy_proven"])
        self.assertFalse(report["optimization_proven"])
        self.assertFalse(report["run_valid"])

    def test_retry_is_accounted_in_expected_attempts_but_invalidates_clean_run(self):
        telemetry = valid_telemetry()
        telemetry["request_strategy"] = {
            "method": "GET",
            "automatic_optimization_enabled": True,
            "selected_optimization": "HEAD",
            "optimized_attempts": 3,
            "optimized_successes": 3,
            "full_get_fallbacks": 0,
            "fallback_transport_errors": 0,
            "fallback_untrustworthy_responses": 0,
        }
        telemetry["responses"]["retries"] = 1
        status, report = self.run_report(telemetry, mode="auto", expect_strategy="HEAD")
        self.assertEqual(status, 3)
        self.assertEqual(report["expected_optimized_attempts"], 3)
        self.assertTrue(report["optimization_proven"])
        self.assertFalse(report["run_valid"])

RESOURCE_RUN_ID = "11111111-1111-4111-8111-111111111111"


def valid_sweep_resource_evidence() -> dict:
    digest = "a" * 64
    before = "2026-10-11T00:00:00.000000000Z"
    after = "2026-10-11T00:00:10.000000000Z"
    cpu_common = {
        "source": "perf003_point_pair_process_cpu_time",
        "before_artifact_sha256": digest,
        "after_artifact_sha256": digest,
        "before_captured_at": before,
        "after_captured_at": after,
        "duration_ns": "10000000000",
        "pid": 123,
        "start_ticks": "456",
        "formula": "100 * cpu_time_delta_ns / capture_duration_ns",
    }
    return {
        "schema_version": 1,
        "kind": "sweep_resource_usage",
        "run_id": RESOURCE_RUN_ID,
        "eligible": True,
        "ineligibility_reasons": [],
        "metrics": {
            "client_cpu_percent": 50.0,
            "server_cpu_percent": 75.0,
            "network_utilization_percent": 8.0,
            "p99_latency_ms": 2.0,
        },
        "provenance": {
            "client_cpu_percent": {
                **cpu_common,
                "capture_scope": "client_point_snapshot_interval",
                "role": "h3fuzz",
                "cpu_time_delta_ns": "5000000000",
            },
            "server_cpu_percent": {
                **cpu_common,
                "capture_scope": "server_point_snapshot_interval",
                "role": "origin",
                "cpu_time_delta_ns": "7500000000",
            },
            "network_utilization_percent": {
                "source": "perf003_point_pair_interface_byte_counters",
                "capture_scope": "client_and_server_point_snapshot_intervals",
                "aggregation": "maximum_observed_full_duplex_direction_utilization",
                "formula": (
                    "100 * 8 * max(rx_bytes_delta, tx_bytes_delta) / "
                    "(capture_duration_seconds * link_speed_bits_per_second)"
                ),
                "client_before_artifact_sha256": digest,
                "client_after_artifact_sha256": digest,
                "server_before_artifact_sha256": digest,
                "server_after_artifact_sha256": digest,
                "observations": [{
                    "endpoint": "client",
                    "ifindex": 2,
                    "name": "eth0",
                    "counter_source": "sysfs",
                    "link_speed_bits_per_second": "1000000000",
                    "capture_duration_ns": "10000000000",
                    "rx_bytes_delta": "10000000",
                    "tx_bytes_delta": "100000000",
                    "full_duplex_direction_utilization_percent": 8.0,
                }],
            },
            "p99_latency_ms": {
                "source": "run_telemetry.responses.latency",
                "capture_scope": "main_successful_logical_responses",
                "telemetry_artifact_sha256": digest,
                "latency_schema_version": 1,
                "clock": "monotonic",
                "unit": "nanoseconds",
                "method": "bounded_histogram_upper_bound",
                "sample_count": 2,
                "p99_upper_bound_ns": 2_000_000,
                "maximum_trackable_ns": 120_000_000_000,
                "overflow_count": 0,
                "formula": "p99_upper_bound_ns / 1000000",
            },
        },
    }


def valid_telemetry() -> dict:
    return {
        "schema_version": 1,
        "protocol": {
            "selected": "http3",
            "http3_selected": True,
            "http3_initial_connections": 2,
            "http3_final_connections": 2,
        },
        "request_strategy": {
            "method": "GET",
            "range": "bytes=-1",
            "automatic_optimization_enabled": False,
            "selected_optimization": "RANGE",
            "optimized_attempts": 0,
            "optimized_successes": 0,
            "full_get_fallbacks": 0,
            "fallback_transport_errors": 0,
            "fallback_untrustworthy_responses": 0,
        },
        "responses": {
            "scheduled": 2,
            "completed_responses": 2,
            "errors": 0,
            "retries": 0,
        },
        "transport": {
            "completed_responses": 2,
            "errors": 0,
            "in_flight": 0,
        },
        "drain": {
            "workers_drained": True,
            "runner_closed": True,
            "limiter_in_use": 0,
            "limiter_waiters": 0,
            "complete": True,
        },
    }


def valid_telemetry_v2() -> dict:
    return {
        "schema_version": 2,
        "run_id": None,
        "eligible_mode": "ordinary",
        "protocol": {
            "selected": "http3",
            "http3_selected": True,
            "http3_initial_connections": 2,
            "http3_final_connections": 2,
        },
        "request_strategy": {
            "method": "GET",
            "range": "bytes=-1",
            "automatic_optimization_enabled": False,
            "selected_optimization": "RANGE",
            "optimized_attempts": 0,
            "optimized_successes": 0,
            "full_get_fallbacks": 0,
            "fallback_transport_errors": 0,
            "fallback_untrustworthy_responses": 0,
        },
        "responses": {
            "main_request_span_ns": 500_000_000,
            "scheduled_logical_inputs": 2,
            "terminal_logical_outcomes": 2,
            "successful_logical_responses": 2,
            "terminal_logical_errors": 0,
        },
        "logical_ledger": {
            "expected_expanded_inputs": 2,
            "assigned_ids": 2,
            "scheduled_transitions": 2,
            "success_terminal_states": 2,
            "error_terminal_states": 0,
            "pending_scheduled_states": 0,
            "id_gaps": 0,
            "duplicate_schedule_transitions": 0,
            "duplicate_terminal_transitions": 0,
            "invalid_transitions": 0,
            "out_of_range_ids": 0,
            "anomaly_sample_ids": [],
        },
        "eligibility": {
            "eligible": True,
            "pause_observed": False,
            "request_delay": False,
            "rate_limited": False,
            "ineligible_reasons": [],
        },
        "transport": {
            "application_attempts": 2,
            "completed_attempts": 2,
            "surfaced_attempt_errors": 0,
            "application_retries": 0,
            "optimized_attempts": 0,
            "full_get_recoveries": 0,
            "attempt_roles": {
                "initial": 2,
                "optimization_fallback": 0,
                "engine_retry": 0,
                "engine_retry_optimization_fallback": 0,
            },
            "methods": {"GET": 2, "HEAD": 0, "other": 0},
            "in_flight": 0,
        },
        "drain": {
            "workers_drained": True,
            "runner_closed": True,
            "limiter_in_use": 0,
            "limiter_waiters": 0,
            "complete": True,
        },
    }


if __name__ == "__main__":
    unittest.main()
