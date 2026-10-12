#!/usr/bin/env python3

import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("fixed-sweep-report.py")
SPEC = importlib.util.spec_from_file_location("fixed_sweep_report", SCRIPT)
fixed_sweep_report = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(fixed_sweep_report)

from perf004_contract import (  # noqa: E402
    add_manifest_hash,
    atomic_write_json,
    comparison_keys,
    file_sha256,
)


def policy(noise: float = 2.0) -> dict:
    return add_manifest_hash(
        {
            "schema_version": 1,
            "manifest_type": "h3fuzz-fixed-sweep-decision-policy",
            "created_at": "2026-10-11T00:00:00Z",
            "policy_generation": "frozen-b0-mad-v1",
            "source_baseline": {
                "manifest_sha256": "1" * 64,
                "file_sha256": "2" * 64,
                "run_count": 5,
                "reports": [
                    {"ordinal": index, "sha256": f"{index:x}" * 64}
                    for index in range(1, 6)
                ],
            },
            "control_window_rates": [100000.0] * 5,
            "median_completed_response_rps": 100000.0,
            "median_absolute_deviation_completed_response_rps": 0.0,
            "noise_band_percent": noise,
            "noise_band_formula": "100 * 1.4826 * MAD(completed_response_rps) / median(completed_response_rps)",
            "client_cpu_max_percent": 750.0,
            "server_cpu_max_percent": 700.0,
            "network_utilization_max_percent": 80.0,
            "p99_latency_max_ms": 10.0,
            "minimum_improvement_percent": max(3.0, noise),
            "platform_key": "a" * 64,
            "client_build_key": "b" * 64,
            "workload_key": "c" * 64,
            "treatment_key": "d" * 64,
            "cohort_key": "e" * 64,
        }
    )


def wordlist_manifest(wordlist_hash: str = "f" * 64) -> dict:
    return add_manifest_hash(
        {
            "schema_version": 1,
            "manifest_type": "h3fuzz-frozen-wordlist",
            "created_at": "2026-10-11T00:00:00Z",
            "frozen": True,
            "generator_version": 1,
            "seed": 1,
            "path": "/source/wordlist.txt",
            "line_count": 3_000_000,
            "expanded_input_count": 3_000_000,
            "sha256": wordlist_hash,
            "base_corpus": {"path": "/source/base", "line_count": 1, "unique_nonempty_line_count": 1, "sha256": "0" * 64},
            "extensions": [],
            "target_rps": 100000,
            "target_seconds": 30,
            "headroom_percent": 20,
            "maximum_calibration_attempts": 3,
            "calibration_attempts": [],
            "artifact_directory": "/source/calibration",
        }
    )


def report_components(connections: int, streams: int, maximum: int, wordlist: dict) -> dict:
    return {
        "platform": {
            "client_host_identity": "client-a",
            "server_host_identity": "server-a",
            "kernel": "Linux 6.8",
        },
        "client_build": {
            "source_tree_sha256": "3" * 64,
            "binary_sha256": "4" * 64,
        },
        "workload": {
            "authority": "origin.test:8443",
            "route": "/bench/FUZZ",
            "wordlist_sha256": wordlist["sha256"],
            "wordlist_line_count": wordlist["line_count"],
            "extensions": wordlist["extensions"],
        },
        "treatment": {
            "mode": "sweep",
            "connection_policy": {"kind": "fixed", "value": connections},
            "streams": streams,
            "worker_policy": {"kind": "bounded", "threads": None, "maximum": maximum},
            "profile_flags": [],
        },
    }


def resource_provenance(resources: dict, sample_count: int) -> dict:
    digest = "9" * 64
    duration_ns = 10_000_000_000
    speed = 1_000_000_000
    client_cpu_delta = round(resources["client_cpu_percent"] * duration_ns / 100)
    server_cpu_delta = round(resources["server_cpu_percent"] * duration_ns / 100)
    network_bytes = round(
        resources["network_utilization_percent"]
        * duration_ns
        * speed
        / (100 * 8 * 1_000_000_000)
    )
    common_cpu = {
        "source": "perf003_point_pair_process_cpu_time",
        "before_artifact_sha256": digest,
        "after_artifact_sha256": digest,
        "before_captured_at": "2026-10-11T00:00:00Z",
        "after_captured_at": "2026-10-11T00:00:10Z",
        "duration_ns": str(duration_ns),
        "pid": 123,
        "start_ticks": "456",
        "formula": "100 * cpu_time_delta_ns / capture_duration_ns",
    }
    return {
        "client_cpu_percent": {
            **common_cpu,
            "capture_scope": "client_point_snapshot_interval",
            "role": "h3fuzz",
            "cpu_time_delta_ns": str(client_cpu_delta),
        },
        "server_cpu_percent": {
            **common_cpu,
            "capture_scope": "server_point_snapshot_interval",
            "role": "origin",
            "cpu_time_delta_ns": str(server_cpu_delta),
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
                "link_speed_bits_per_second": str(speed),
                "capture_duration_ns": str(duration_ns),
                "rx_bytes_delta": "1",
                "tx_bytes_delta": str(network_bytes),
                "full_duplex_direction_utilization_percent": resources["network_utilization_percent"],
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
            "sample_count": sample_count,
            "p99_upper_bound_ns": round(resources["p99_latency_ms"] * 1_000_000),
            "maximum_trackable_ns": 120_000_000_000,
            "overflow_count": 0,
            "formula": "p99_upper_bound_ns / 1000000",
        },
    }


def valid_report(
    connections: int,
    streams: int,
    rate: int,
    wordlist: dict,
    *,
    maximum: int = 65536,
    resources: dict | None = None,
) -> dict:
    count = rate * 30
    if resources is None:
        resources = {
            "client_cpu_percent": 500.0,
            "server_cpu_percent": 450.0,
            "network_utilization_percent": 25.0,
            "p99_latency_ms": 2.0,
        }
    return {
        "aggregation_mode": "sweep",
        "run_gates_passed": True,
        "eligible_for_aggregation": True,
        "rps_measurement_basis": "successful_logical_responses/main_request_span",
        "main_request_span_seconds": 30.0,
        "completed_response_rps": float(rate),
        "successful_logical_responses": count,
        "completed_responses": count,
        "scheduled_logical_inputs": count,
        "terminal_logical_outcomes": count,
        "terminal_logical_errors": 0,
        "retries": 0,
        "application_attempts": count,
        "transport_errors": 0,
        "responses_accounted": True,
        "logical_ledger_proven": True,
        "transport_proven": True,
        "drain_complete": True,
        "server_correlation": {"available": True, "error_rate": 0.0},
        "udp_attribution": {
            "available": True,
            "receive_buffer_errors_delta": 0,
            "packet_drops_delta": 0,
            "unexplained_delta": 0,
        },
        "comparison_keys": comparison_keys(
            report_components(connections, streams, maximum, wordlist)
        ),
        "sweep_resource_usage": resources,
        "sweep_resource_usage_valid": True,
        "sweep_resource_usage_provenance": resource_provenance(resources, count),
        "sweep_resource_usage_required": True,
        "sweep_resource_usage_ineligibility_reasons": [],
        "sweep_resource_evidence_sha256": "8" * 64,
    }


class FixedSweepReportTest(unittest.TestCase):
    def write_reports(
        self,
        root: Path,
        connections: int,
        streams: int,
        rates: tuple[int, int, int],
        wordlist: dict,
        *,
        mutate=None,
    ) -> list[Path]:
        paths = []
        for trial, rate in enumerate(rates, 1):
            report = valid_report(connections, streams, rate, wordlist)
            if mutate is not None:
                mutate(report, trial)
            path = root / "points" / f"c{connections}" / f"trial-{trial}" / "benchmark-report.json"
            atomic_write_json(path, report)
            paths.append(path)
        return paths

    def evaluate(
        self,
        root: Path,
        connections: int,
        streams: int,
        rates: tuple[int, int, int],
        *,
        prior_best: float | None = None,
        mutate=None,
    ) -> dict:
        words = wordlist_manifest()
        paths = self.write_reports(root, connections, streams, rates, words, mutate=mutate)
        return fixed_sweep_report.evaluate_point(
            connections=connections,
            streams=streams,
            report_paths=paths,
            root=root,
            policy=policy(),
            wordlist_manifest=words,
            max_workers=65536,
            prior_best=prior_best,
        )

    def test_clean_point_requires_exactly_three_reports_and_portable_refs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            point = self.evaluate(root, 8, 256, (100000, 101000, 99000))
            self.assertTrue(point["clean"])
            self.assertTrue(point["selection_eligible"])
            self.assertEqual(point["median_completed_response_rps"], 100000.0)
            self.assertEqual(len(point["reports"]), 3)
            self.assertTrue(all(not Path(ref["path"]).is_absolute() for ref in point["reports"]))
            with self.assertRaisesRegex(ValueError, "exactly three"):
                fixed_sweep_report.evaluate_point(
                    connections=8,
                    streams=256,
                    report_paths=[root / point["reports"][0]["path"]],
                    root=root,
                    policy=policy(),
                    wordlist_manifest=wordlist_manifest(),
                    max_workers=65536,
                    prior_best=None,
                )

    def test_missing_resource_observation_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            def remove_resources(report, _trial):
                report.pop("sweep_resource_usage")

            point = self.evaluate(
                Path(directory), 8, 256, (100000, 100000, 100000), mutate=remove_resources
            )
            self.assertFalse(point["selection_eligible"])
            self.assertEqual(point["classification"], "ineligible")
            self.assertIn("trial_01:resource_usage_missing", point["ineligible_reasons"])

    def test_resource_observation_without_provenance_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            def remove_provenance(report, _trial):
                report.pop("sweep_resource_usage_provenance")

            point = self.evaluate(
                Path(directory), 8, 256, (100000, 100000, 100000), mutate=remove_provenance
            )
            self.assertFalse(point["selection_eligible"])
            self.assertTrue(any(
                reason.startswith("trial_01:resource_usage_provenance_invalid:")
                for reason in point["ineligible_reasons"]
            ))

    def test_resource_latency_sample_count_mismatch_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            def change_sample_count(report, _trial):
                report["sweep_resource_usage_provenance"]["p99_latency_ms"][
                    "sample_count"
                ] -= 1

            point = self.evaluate(
                Path(directory), 8, 256, (100000, 100000, 100000), mutate=change_sample_count
            )
            self.assertFalse(point["selection_eligible"])
            self.assertIn(
                "trial_01:resource_latency_sample_count_mismatch",
                point["ineligible_reasons"],
            )

    def test_resource_limit_needs_same_minimum_throughput_improvement(self):
        def high_cpu(report, _trial):
            report["sweep_resource_usage"]["client_cpu_percent"] = 800.0
            report["sweep_resource_usage_provenance"]["client_cpu_percent"][
                "cpu_time_delta_ns"
            ] = "80000000000"

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            worsened = self.evaluate(
                root / "worse",
                8,
                256,
                (102000, 102000, 102000),
                prior_best=100000.0,
                mutate=high_cpu,
            )
            self.assertEqual(worsened["classification"], "worsened")
            self.assertFalse(worsened["selection_eligible"])
            improved = self.evaluate(
                root / "improved",
                8,
                256,
                (103000, 103000, 103000),
                prior_best=100000.0,
                mutate=high_cpu,
            )
            self.assertEqual(improved["classification"], "improved_with_resource_override")
            self.assertTrue(improved["selection_eligible"])

    def test_inclusive_near_best_boundary_and_total_tie_break(self):
        def candidate(connections, streams, rate):
            token = f"{connections % 16:x}"
            return {
                "connections": connections,
                "streams": streams,
                "median_completed_response_rps": rate,
                "selection_eligible": True,
                "treatment_key": token * 64,
                "cohort_key": "f" * 64,
            }

        result = fixed_sweep_report.select_point(
            [candidate(4, 256, 97000.0), candidate(8, 128, 100000.0)], 3.0
        )
        self.assertEqual(result["near_best"][0]["gap_from_best_percent"], 3.0)
        self.assertEqual(result["chosen"]["connections"], 4)
        self.assertEqual(result["chosen"]["streams"], 256)
        tied = fixed_sweep_report.select_point(
            [candidate(8, 128, 100000.0), candidate(4, 256, 100000.0)], 3.0
        )
        self.assertEqual((tied["chosen"]["connections"], tied["chosen"]["streams"]), (4, 256))

    def test_short_trial_is_explicitly_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            def shorten(report, trial):
                if trial == 2:
                    report["main_request_span_seconds"] = 29.0

            point = self.evaluate(
                Path(directory), 8, 256, (100000, 100000, 100000), mutate=shorten
            )
            self.assertFalse(point["selection_eligible"])
            self.assertIn(
                "main_request_span_below_30_seconds",
                point["run_ineligible_reasons"][1],
            )

    def test_treatment_tamper_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            def tamper(report, trial):
                if trial == 3:
                    report["comparison_keys"]["treatment"]["streams"] = 64

            with self.assertRaisesRegex(ValueError, "comparison keys|treatment"):
                self.evaluate(
                    Path(directory), 8, 256, (100000, 100000, 100000), mutate=tamper
                )

    def test_manifest_is_portable_hashed_and_sets_selected_adaptive_bounds(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            words = wordlist_manifest()
            frozen_policy = policy()
            policy_path = root / "inputs" / "decision-policy.json"
            wordlist_path = root / "inputs" / "wordlist-manifest.json"
            atomic_write_json(policy_path, frozen_policy)
            atomic_write_json(wordlist_path, words)
            low = fixed_sweep_report.evaluate_point(
                connections=4,
                streams=64,
                report_paths=self.write_reports(root / "low", 4, 64, (97000, 97000, 97000), words),
                root=root,
                policy=frozen_policy,
                wordlist_manifest=words,
                max_workers=65536,
                prior_best=None,
            )
            high = fixed_sweep_report.evaluate_point(
                connections=8,
                streams=64,
                report_paths=self.write_reports(root / "high", 8, 64, (100000, 100000, 100000), words),
                root=root,
                policy=frozen_policy,
                wordlist_manifest=words,
                max_workers=65536,
                prior_best=97000.0,
            )
            manifest_path = root / "fixed-sweep-manifest.json"
            manifest = fixed_sweep_report.build_manifest(
                root=root,
                manifest_path=manifest_path,
                policy=frozen_policy,
                policy_path=policy_path,
                wordlist_manifest=words,
                wordlist_manifest_path=wordlist_path,
                connections=[4, 8],
                streams=[64],
                max_workers=65536,
                candidates=[low, high],
                global_reasons=[],
            )
            fixed_sweep_report.validate_final_manifest(manifest, manifest_path)
            atomic_write_json(manifest_path, manifest)
            fixed_sweep_report.validate_final_manifest(manifest, manifest_path)
            self.assertTrue(manifest["eligible"])
            self.assertEqual(manifest["selected_point"]["connections"], 4)
            self.assertEqual(manifest["adaptive_max_connections"], 4)
            self.assertEqual(manifest["adaptive_max_workers"], 65536)
            self.assertTrue(all(not Path(member["path"]).is_absolute() for member in manifest["members"]))
            selected_reports = manifest["points"][0]["reports"]
            self.assertTrue(all(ref["sha256"] for ref in selected_reports))

            selection_tamper = copy.deepcopy(manifest)
            selection_tamper["selected_point"]["connections"] = 8
            selection_tamper["adaptive_max_connections"] = 8
            selection_tamper.pop("manifest_sha256")
            selection_tamper = add_manifest_hash(selection_tamper)
            with self.assertRaisesRegex(ValueError, "total tie-break"):
                fixed_sweep_report.validate_final_manifest(selection_tamper, manifest_path)

            path_tamper = copy.deepcopy(manifest)
            path_tamper["members"][0]["path"] = "../escape.json"
            path_tamper.pop("manifest_sha256")
            path_tamper = add_manifest_hash(path_tamper)
            with self.assertRaisesRegex(ValueError, "escapes artifact root"):
                fixed_sweep_report.validate_final_manifest(path_tamper, manifest_path)

            report_to_tamper = root / selected_reports[0]["path"]
            report_to_tamper.write_text("{}\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "member content mismatch"):
                fixed_sweep_report.validate_final_manifest(manifest, manifest_path)

    def test_skipped_candidate_requires_reason(self):
        candidate = fixed_sweep_report.skipped_point(16, 64, "prior_point_reached_plateau", None)
        self.assertEqual(candidate["status"], "skipped")
        self.assertEqual(candidate["skipped_reasons"], ["prior_point_reached_plateau"])


if __name__ == "__main__":
    unittest.main()
