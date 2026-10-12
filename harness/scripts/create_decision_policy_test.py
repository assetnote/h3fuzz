#!/usr/bin/env python3

import argparse
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("create-decision-policy.py")
SPEC = importlib.util.spec_from_file_location("create_decision_policy", SCRIPT)
create_decision_policy = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(create_decision_policy)

from perf004_contract import (  # noqa: E402
    add_manifest_hash,
    atomic_write_json,
    comparison_keys,
    file_sha256,
    verify_manifest_hash,
)


def baseline_components() -> dict:
    return {
        "platform": {
            "client_host_identity": "client-a",
            "server_host_identity": "server-a",
        },
        "client_build": {
            "source_tree_sha256": "a" * 64,
            "binary_sha256": "b" * 64,
        },
        "workload": {
            "authority": "origin.test:8443",
            "route": "/bench/FUZZ",
            "wordlist_sha256": "c" * 64,
        },
        "treatment": {
            "mode": "baseline",
            "connection_policy": {"kind": "fixed", "value": 8},
            "streams": 256,
        },
    }


class CreateDecisionPolicyTest(unittest.TestCase):
    def make_b0(self, root: Path, rates=(100.0, 101.0, 99.0, 100.0, 102.0)) -> Path:
        keys = comparison_keys(baseline_components())
        reports = []
        for index, rate in enumerate(rates, 1):
            report = {
                "aggregation_mode": "baseline",
                "run_gates_passed": True,
                "eligible_for_aggregation": True,
                "main_request_span_seconds": 30.0,
                "completed_response_rps": float(rate),
                "comparison_keys": keys,
            }
            path = root / f"run-{index:02d}.json"
            atomic_write_json(path, report)
            reports.append({"path": str(path.resolve()), "sha256": file_sha256(path)})
        median = 100.0
        mad = 1.0
        if len(set(rates)) == 1:
            median = float(rates[0])
            mad = 0.0
        noise = 100.0 * 1.4826 * mad / median
        manifest = add_manifest_hash(
            {
                "schema_version": 1,
                "manifest_type": "h3fuzz-baseline",
                "baseline": "B0",
                "created_at": "2026-10-11T00:00:00Z",
                "run_count": len(rates),
                "reports": reports,
                "control_window_rates": [float(value) for value in rates],
                "median_completed_response_rps": median,
                "median_absolute_deviation_completed_response_rps": mad,
                "minimum_completed_response_rps": min(rates),
                "maximum_completed_response_rps": max(rates),
                "standard_deviation_completed_response_rps": 0.0,
                "noise_band_percent": noise,
                "noise_band_formula": create_decision_policy.NOISE_FORMULA,
                "minimum_improvement_percent": max(3.0, noise),
                "platform_key": keys["platform_key"],
                "client_build_key": keys["client_build_key"],
                "workload_key": keys["workload_key"],
                "treatment_key": keys["treatment_key"],
                "cohort_key": keys["cohort_key"],
                "eligible": True,
                "ineligible_reasons": [],
            }
        )
        path = root / "baseline-manifest.json"
        atomic_write_json(path, manifest)
        return path

    def policy(self, baseline: Path) -> dict:
        return create_decision_policy.create_policy(
            baseline,
            client_cpu_max_percent=750.0,
            server_cpu_max_percent=700.0,
            network_utilization_max_percent=80.0,
            p99_latency_max_ms=10.0,
        )

    def test_freezes_exact_b0_mad_and_all_source_hashes(self):
        with tempfile.TemporaryDirectory() as directory:
            baseline = self.make_b0(Path(directory))
            policy = self.policy(baseline)
            verify_manifest_hash(policy, "fixture policy")
            self.assertEqual(policy["manifest_type"], create_decision_policy.POLICY_TYPE)
            self.assertAlmostEqual(policy["noise_band_percent"], 1.4826)
            self.assertEqual(policy["minimum_improvement_percent"], 3.0)
            self.assertEqual(policy["source_baseline"]["run_count"], 5)
            self.assertEqual(len(policy["source_baseline"]["reports"]), 5)
            self.assertEqual(
                policy["source_baseline"]["file_sha256"], file_sha256(baseline)
            )
            encoded = json.dumps(policy)
            self.assertNotIn(str(Path(directory).resolve()), encoded)
            self.assertEqual(policy["client_cpu_max_percent"], 750.0)

    def test_identical_windows_freeze_zero_noise_and_three_percent_floor(self):
        with tempfile.TemporaryDirectory() as directory:
            baseline = self.make_b0(Path(directory), rates=(100, 100, 100, 100, 100))
            policy = self.policy(baseline)
            self.assertEqual(policy["noise_band_percent"], 0.0)
            self.assertEqual(policy["minimum_improvement_percent"], 3.0)

    def test_rejects_tampered_source_report_even_if_manifest_summary_is_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            baseline = self.make_b0(root)
            report = root / "run-03.json"
            report.write_text('{"tampered":true}\n', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "content hash mismatch"):
                self.policy(baseline)

    def test_rejects_tampered_baseline_self_hash(self):
        with tempfile.TemporaryDirectory() as directory:
            baseline = self.make_b0(Path(directory))
            value = json.loads(baseline.read_text(encoding="utf-8"))
            value["noise_band_percent"] = 999
            baseline.write_text(json.dumps(value), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "manifest_sha256 mismatch"):
                self.policy(baseline)

    def test_rejects_rate_not_derived_from_hashed_report(self):
        with tempfile.TemporaryDirectory() as directory:
            baseline = self.make_b0(Path(directory))
            value = json.loads(baseline.read_text(encoding="utf-8"))
            value["control_window_rates"][0] = 90
            value.pop("manifest_sha256")
            value = add_manifest_hash(value)
            baseline.write_text(json.dumps(value), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "rate does not match B0"):
                self.policy(baseline)

    def test_rejects_nonfinite_limits(self):
        with self.assertRaises(argparse.ArgumentTypeError):
            create_decision_policy.finite_positive("nan")
        with self.assertRaises(argparse.ArgumentTypeError):
            create_decision_policy.finite_positive("inf")

    def test_cli_refuses_to_overwrite_policy(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            baseline = self.make_b0(root)
            output = root / "policy.json"
            argv = [
                "--baseline-manifest",
                str(baseline),
                "--client-cpu-max-percent",
                "750",
                "--server-cpu-max-percent",
                "700",
                "--network-utilization-max-percent",
                "80",
                "--p99-latency-max-ms",
                "10",
                "--output",
                str(output),
            ]
            self.assertEqual(create_decision_policy.run(argv), 0)
            first = output.read_bytes()
            self.assertEqual(create_decision_policy.run(argv), 2)
            self.assertEqual(output.read_bytes(), first)


if __name__ == "__main__":
    unittest.main()
