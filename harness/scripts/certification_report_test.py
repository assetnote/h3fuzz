#!/usr/bin/env python3

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("certification-report.py")
SPEC = importlib.util.spec_from_file_location("certification_report", SCRIPT)
certification_report = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(certification_report)


def canonical_hash(value: object) -> str:
    encoded = json.dumps(
        value, ensure_ascii=False, separators=(",", ":"), sort_keys=True
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def comparison_keys(mode: str) -> dict:
    platform = {
        "client_host_identity": "client-a",
        "server_host_identity": "server-a",
        "client_affinity_cpus": "0-7",
        "server_affinity_cpus": "0-7",
        "kernel": "Linux 6.8",
        "network_namespace": "net:[42]",
        "network_path": "same-region-host-network",
        "server_image": "sha256:server",
        "server_config": "sha256:config",
    }
    build = {
        "source_tree_sha256": "a" * 64,
        "binary_sha256": "b" * 64,
        "dependency_sha256": "c" * 64,
        "go_version": "go1.25.1",
    }
    workload = {
        "authority": "origin.test:8443",
        "route": "/bench/FUZZ",
        "protocol": "http3",
        "method": "GET",
        "headers": [],
        "filters": {"status": "204", "size": None},
        "strategy": "HEAD",
        "datagrams": False,
        "timeout_seconds": 10,
        "extensions": [],
        "input_mode": "clusterbomb",
        "wordlist_sha256": "d" * 64,
    }
    if mode == "adaptive":
        treatment = {
            "mode": "adaptive",
            "connection_policy": {"kind": "adaptive", "maximum": 20},
            "streams": 256,
            "worker_policy": {"kind": "bounded", "maximum": 65536},
            "profile_flags": [],
        }
    else:
        treatment = {
            "mode": "selected-fixed",
            "connection_policy": {"kind": "fixed", "value": 20},
            "streams": 256,
            "worker_policy": {"kind": "bounded", "maximum": 65536},
            "profile_flags": [],
        }
    result = {
        "schema_version": 1,
        "platform": platform,
        "client_build": build,
        "workload": workload,
        "treatment": treatment,
    }
    result.update(
        platform_key=canonical_hash(platform),
        client_build_key=canonical_hash(build),
        workload_key=canonical_hash(workload),
        treatment_key=canonical_hash(treatment),
    )
    result["cohort_key"] = canonical_hash(
        {
            "platform_key": result["platform_key"],
            "client_build_key": result["client_build_key"],
            "workload_key": result["workload_key"],
            "treatment_key": result["treatment_key"],
        }
    )
    return result


def valid_run(mode: str, rate: float = 105_000.0) -> dict:
    span = 30.0
    completed = int(rate * span)
    return {
        "report_schema_version": 2,
        "aggregation_mode": mode,
        "run_gates_passed": True,
        "eligible_for_aggregation": True,
        "main_request_span_seconds": span,
        "successful_logical_responses": completed,
        "completed_responses": completed,
        "completed_response_rps": completed / span,
        "logical_rps": 9_999_999.0,
        "rps_measurement_basis": "successful_logical_responses/main_request_span",
        "scheduled_logical_inputs": completed,
        "terminal_logical_outcomes": completed,
        "terminal_logical_errors": 0,
        "response_errors": 0,
        "retries": 0,
        "application_attempts": completed,
        "transport_errors": 0,
        "responses_accounted": True,
        "logical_ledger_proven": True,
        "transport_proven": True,
        "drain_complete": True,
        "server_correlation": {
            "available": True,
            "expected_handler_requests": completed,
            "observed_handler_requests": completed,
            "error_rate": 0.0,
        },
        "udp_attribution": {
            "available": True,
            "receive_buffer_errors_delta": 0,
            "packet_drops_delta": 0,
            "unexplained_delta": 0,
        },
        "comparison_keys": comparison_keys(mode),
    }


def prerequisites() -> dict:
    return {
        "selection": {
            "connections": 20,
            "streams": 256,
            "adaptive_max_connections": 20,
            "adaptive_max_workers": 65536,
        },
        "fixed_sweep": {"verified": True, "eligible": True},
        "files_validation": {"verified": True, "eligible": True},
        "diagnostics": {"verified": True, "eligible": True},
        "wordlist": {"verified": True, "frozen": True},
    }


class CertificationReportTest(unittest.TestCase):
    def evaluate(self, adaptive=None, fixed=None, prereqs=None):
        adaptive = adaptive or [valid_run("adaptive", 105_000 + i) for i in range(5)]
        fixed = fixed or [valid_run("selected-fixed", 110_000 + i) for i in range(5)]
        return certification_report.evaluate_certification(
            adaptive,
            fixed,
            target_rps=100_000,
            prerequisites=prereqs or prerequisites(),
        )

    def test_pass_uses_completed_response_rate_and_five_run_medians(self):
        report = self.evaluate()
        self.assertTrue(report["certification_eligible"])
        self.assertEqual(report["adaptive"]["count"], 5)
        self.assertEqual(report["selected_fixed"]["count"], 5)
        self.assertEqual(report["metric"], "successful_logical_responses/main_request_span")
        self.assertGreater(report["adaptive_vs_fixed_percent"], 90.0)
        self.assertEqual(len(report["adaptive"]["runs"]), 5)
        self.assertTrue(report["adaptive"]["count_reconciliation_passed"])
        self.assertTrue(report["adaptive"]["drain_complete"])
        self.assertEqual(report["adaptive"]["attributable_udp_deltas"]["unexplained_delta"], 0)
        self.assertEqual(report["adaptive"]["pooled_error_rates"]["terminal_logical_error_rate"], 0.0)

    def test_below_rate_fails_even_when_logical_input_rate_is_high(self):
        adaptive = [valid_run("adaptive", 99_999) for _ in range(5)]
        report = self.evaluate(adaptive=adaptive)
        self.assertFalse(report["certification_eligible"])
        self.assertIn("adaptive_median_below_target", report["ineligible_reasons"])

    def test_short_duration_invalidates_entire_group(self):
        adaptive = [valid_run("adaptive") for _ in range(5)]
        adaptive[2]["main_request_span_seconds"] = 29.999
        report = self.evaluate(adaptive=adaptive)
        self.assertIn("adaptive_run_03:main_request_span_below_30_seconds", report["ineligible_reasons"])
        self.assertIn("wordlist_generation_invalid_short_measured_trial", report["ineligible_reasons"])
        self.assertFalse(report["wordlist_generation_valid"])

    def test_terminal_error_rate_gate_is_strictly_below_point_one_percent(self):
        adaptive = [valid_run("adaptive") for _ in range(5)]
        adaptive[0]["terminal_logical_errors"] = adaptive[0]["scheduled_logical_inputs"] // 1000
        report = self.evaluate(adaptive=adaptive)
        self.assertIn("adaptive_run_01:terminal_logical_error_rate", report["ineligible_reasons"])

    def test_udp_drop_is_ineligible(self):
        fixed = [valid_run("selected-fixed", 110_000) for _ in range(5)]
        fixed[4]["udp_attribution"]["packet_drops_delta"] = 1
        fixed[4]["udp_attribution"]["unexplained_delta"] = 1
        report = self.evaluate(fixed=fixed)
        self.assertIn("selected_fixed_run_05:udp_unexplained_delta", report["ineligible_reasons"])

    def test_count_mismatch_is_ineligible(self):
        adaptive = [valid_run("adaptive") for _ in range(5)]
        adaptive[1]["terminal_logical_outcomes"] -= 1
        report = self.evaluate(adaptive=adaptive)
        self.assertIn("adaptive_run_02:count_reconciliation", report["ineligible_reasons"])

    def test_mixed_configuration_within_group_is_rejected(self):
        adaptive = [valid_run("adaptive") for _ in range(5)]
        adaptive[3]["comparison_keys"]["workload"]["timeout_seconds"] = 11
        changed = adaptive[3]["comparison_keys"]["workload"]
        adaptive[3]["comparison_keys"]["workload_key"] = canonical_hash(changed)
        report = self.evaluate(adaptive=adaptive)
        self.assertIn("adaptive:mixed_cohort_key", report["ineligible_reasons"])

    def test_identity_key_mismatch_between_cohorts_is_rejected(self):
        fixed = [valid_run("selected-fixed", 110_000) for _ in range(5)]
        for item in fixed:
            item["comparison_keys"]["platform"]["server_host_identity"] = "server-b"
            item["comparison_keys"]["platform_key"] = canonical_hash(item["comparison_keys"]["platform"])
        report = self.evaluate(fixed=fixed)
        self.assertIn("cohorts:platform_key_mismatch", report["ineligible_reasons"])

    def test_adaptive_median_must_reach_ninety_percent_of_fixed_median(self):
        adaptive = [valid_run("adaptive", 100_000) for _ in range(5)]
        fixed = [valid_run("selected-fixed", 112_000) for _ in range(5)]
        report = self.evaluate(adaptive=adaptive, fixed=fixed)
        self.assertIn("adaptive_median_below_90_percent_of_fixed", report["ineligible_reasons"])

    def test_incomplete_drain_is_ineligible(self):
        adaptive = [valid_run("adaptive") for _ in range(5)]
        adaptive[0]["drain_complete"] = False
        report = self.evaluate(adaptive=adaptive)
        self.assertIn("adaptive_run_01:incomplete_drain", report["ineligible_reasons"])

    def test_missing_prerequisite_is_ineligible(self):
        prereqs = prerequisites()
        prereqs["diagnostics"] = {"verified": False, "eligible": False}
        report = self.evaluate(prereqs=prereqs)
        self.assertIn("prerequisite:diagnostics_unverified", report["ineligible_reasons"])

    def make_sweep(self, root: Path, *, selected_connections: int = 4) -> Path:
        members = []
        points = []
        point_keys = {}
        for connections, rate in ((4, 100_000.0), (8, 101_000.0)):
            keys = comparison_keys("selected-fixed")
            keys["treatment"] = {
                "mode": "sweep",
                "connection_policy": {"kind": "fixed", "value": connections},
                "streams": 64,
                "worker_policy": {"kind": "bounded", "maximum": 65536},
                "profile_flags": [],
            }
            keys["treatment_key"] = canonical_hash(keys["treatment"])
            keys["cohort_key"] = canonical_hash({
                "platform_key": keys["platform_key"],
                "client_build_key": keys["client_build_key"],
                "workload_key": keys["workload_key"],
                "treatment_key": keys["treatment_key"],
            })
            point_keys[connections] = keys
            reports = []
            for run in range(3):
                report = {
                    "aggregation_mode": "sweep",
                    "run_gates_passed": True,
                    "eligible_for_aggregation": True,
                    "completed_response_rps": rate,
                    "comparison_keys": keys,
                }
                relative = f"points/c{connections}/run-{run + 1}.json"
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(json.dumps(report), encoding="utf-8")
                digest = hashlib.sha256(path.read_bytes()).hexdigest()
                members.append({"path": relative, "sha256": digest})
                reports.append({"path": relative, "sha256": digest})
            points.append({
                "connections": connections,
                "streams": 64,
                "treatment_key": keys["treatment_key"],
                "cohort_key": keys["cohort_key"],
                "clean": True,
                "median_completed_response_rps": rate,
                "reports": reports,
            })
        body = {
            "schema_version": 1,
            "manifest_type": "h3fuzz-fixed-sweep",
            "platform_key": point_keys[4]["platform_key"],
            "client_build_key": point_keys[4]["client_build_key"],
            "workload_key": point_keys[4]["workload_key"],
            "authority": "origin.test:8443",
            "server_identity": "server-a",
            "decision_policy": {"noise_band_percent": 2.0, "minimum_improvement_percent": 3.0},
            "points": points,
            "selected_point": {
                "connections": selected_connections,
                "streams": 64,
                "treatment_key": point_keys[selected_connections]["treatment_key"],
                "cohort_key": point_keys[selected_connections]["cohort_key"],
            },
            "adaptive_max_connections": selected_connections,
            "adaptive_max_workers": 65536,
            "members": members,
        }
        body["manifest_sha256"] = canonical_hash(body)
        path = root / "fixed-sweep-manifest.json"
        path.write_text(json.dumps(body), encoding="utf-8")
        return path

    def test_fixed_sweep_recomputes_total_selection_rule(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.make_sweep(Path(directory))
            _, selection = certification_report.verify_fixed_sweep(path)
            self.assertEqual(selection["connections"], 4)
            self.assertEqual(selection["streams"], 64)

    def test_fixed_sweep_rejects_wrong_selected_point(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.make_sweep(Path(directory), selected_connections=8)
            with self.assertRaisesRegex(ValueError, "selected point"):
                certification_report.verify_fixed_sweep(path)

    def test_fixed_sweep_rejects_shared_key_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.make_sweep(Path(directory))
            manifest = json.loads(path.read_text(encoding="utf-8"))
            manifest["platform_key"] = "0" * 64
            manifest.pop("manifest_sha256")
            manifest["manifest_sha256"] = canonical_hash(manifest)
            path.write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "shared key mismatch"):
                certification_report.verify_fixed_sweep(path)

    def test_fixed_sweep_rejects_unmanifested_member(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = self.make_sweep(root)
            (root / "untracked.txt").write_text("not in manifest", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "member tree mismatch"):
                certification_report.verify_fixed_sweep(path)


if __name__ == "__main__":
    unittest.main()
