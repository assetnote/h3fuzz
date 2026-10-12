#!/usr/bin/env python3

import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("baseline-report.py")
SPEC = importlib.util.spec_from_file_location("baseline_report", SCRIPT)
baseline_report = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(baseline_report)


def canonical_hash(value: object) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def baseline_run(rate: float, cohort: str = "cohort-a") -> dict:
    platform = {"client_host_identity": "client-a", "server_host_identity": "server-a"}
    build = {"source_tree_sha256": "a" * 64, "binary_sha256": "b" * 64}
    workload = {"authority": "origin.test:8443", "route": "/bench/FUZZ"}
    treatment = {"mode": "baseline", "connections": 20, "streams": 256}
    key_values = {
        "platform_key": canonical_hash(platform),
        "client_build_key": canonical_hash(build),
        "workload_key": canonical_hash(workload),
        "treatment_key": canonical_hash(treatment),
    }
    computed_cohort = canonical_hash(key_values)
    return {
        "aggregation_mode": "baseline",
        "run_gates_passed": True,
        "eligible_for_aggregation": True,
        "completed_response_rps": rate,
        "main_request_span_seconds": 30.0,
        "comparison_keys": {
            "schema_version": 1,
            "platform": platform,
            "client_build": build,
            "workload": workload,
            "treatment": treatment,
            **key_values,
            "cohort_key": computed_cohort if cohort == "cohort-a" else cohort,
        },
    }


class BaselineReportTest(unittest.TestCase):
    def test_b0_manifest_uses_median_absolute_deviation_noise_band(self):
        reports = [baseline_run(value) for value in (100, 101, 99, 100, 102)]
        result = baseline_report.evaluate_baseline("B0", reports)
        self.assertTrue(result["eligible"])
        self.assertEqual(result["median_completed_response_rps"], 100)
        self.assertEqual(result["median_absolute_deviation_completed_response_rps"], 1)
        self.assertEqual(result["control_window_rates"], [100.0, 101.0, 99.0, 100.0, 102.0])
        self.assertAlmostEqual(result["noise_band_percent"], 1.4826)
        self.assertEqual(result["minimum_improvement_percent"], 3.0)

    def test_identical_windows_have_zero_noise(self):
        result = baseline_report.evaluate_baseline(
            "B0", [baseline_run(100) for _ in range(5)]
        )
        self.assertEqual(result["noise_band_percent"], 0.0)

    def test_requires_at_least_five_clean_fixed_duration_windows(self):
        result = baseline_report.evaluate_baseline(
            "B0", [baseline_run(100) for _ in range(4)]
        )
        self.assertFalse(result["eligible"])
        self.assertIn("requires_at_least_five_reports", result["ineligible_reasons"])

    def test_mixed_cohort_is_rejected(self):
        reports = [baseline_run(100) for _ in range(5)]
        reports[-1]["comparison_keys"]["cohort_key"] = "cohort-b"
        result = baseline_report.evaluate_baseline("B0", reports)
        self.assertFalse(result["eligible"])
        self.assertIn("mixed_cohort_key", result["ineligible_reasons"])

    def test_cli_refuses_to_overwrite_immutable_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            paths = []
            for index in range(5):
                path = root / f"run-{index}.json"
                path.write_text(json.dumps(baseline_run(100 + index)), encoding="utf-8")
                paths.append(path)
            output = root / "baseline.json"
            status = baseline_report.run_cli(
                ["--baseline", "B0", *sum((["--report", str(path)] for path in paths), []), "--output", str(output)]
            )
            self.assertEqual(status, 0)
            original = output.read_bytes()
            status = baseline_report.run_cli(
                ["--baseline", "B0", *sum((["--report", str(path)] for path in paths), []), "--output", str(output)]
            )
            self.assertEqual(status, 2)
            self.assertEqual(output.read_bytes(), original)


if __name__ == "__main__":
    unittest.main()
