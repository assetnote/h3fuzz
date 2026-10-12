#!/usr/bin/env python3

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from perf004_contract import add_manifest_hash, atomic_write_json, file_sha256


SCRIPT_DIR = Path(__file__).resolve().parent
SWEEP = SCRIPT_DIR / "fixed-sweep.sh"
CERTIFICATION_REPORT_SCRIPT = SCRIPT_DIR / "certification-report.py"
CERTIFICATION_REPORT_SPEC = importlib.util.spec_from_file_location(
    "fixed_sweep_shell_certification_report", CERTIFICATION_REPORT_SCRIPT
)
certification_report = importlib.util.module_from_spec(CERTIFICATION_REPORT_SPEC)
assert CERTIFICATION_REPORT_SPEC.loader is not None
CERTIFICATION_REPORT_SPEC.loader.exec_module(certification_report)


FAKE_BENCHMARK = r'''#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import sys

args = {}
index = 1
while index < len(sys.argv):
    key = sys.argv[index]
    if index + 1 >= len(sys.argv):
        raise SystemExit(2)
    args[key] = sys.argv[index + 1]
    index += 2

with open(os.environ["FAKE_INVOCATION_LOG"], "a", encoding="utf-8") as output:
    output.write(json.dumps(sys.argv[1:]) + "\n")

artifact = Path(args["--artifact-dir"])
artifact.mkdir(parents=True)
connections = int(args["--connections"])
streams = int(args["--streams"])
maximum = int(args["--max-workers"])
wordlist = Path(args["--wordlist"])
wordlist_hash = hashlib.sha256(wordlist.read_bytes()).hexdigest()
line_count = sum(1 for _ in wordlist.open("rb"))
rate = {4: 100000, 8: 101000, 16: 110000}.get(connections, 100000)
span = 29 if os.environ.get("FAKE_SHORT_CONNECTION") == str(connections) else 30
count = rate * span

platform = {"client_host_identity": "client-a", "server_host_identity": "server-a"}
build = {"source_tree_sha256": "3" * 64, "binary_sha256": "4" * 64}
workload = {
    "authority": "origin.test:8443",
    "route": "/bench/FUZZ",
    "wordlist_sha256": wordlist_hash,
    "wordlist_line_count": line_count,
    "extensions": [value for value in args["--extensions"].split(",") if value],
}
treatment = {
    "mode": "sweep",
    "connection_policy": {"kind": "fixed", "value": connections},
    "streams": streams,
    "worker_policy": {"kind": "bounded", "threads": None, "maximum": maximum},
    "profile_flags": [],
}
def digest(value):
    encoded = json.dumps(value, ensure_ascii=False, allow_nan=False, separators=(",", ":"), sort_keys=True).encode()
    return hashlib.sha256(encoded).hexdigest()
keys = {
    "schema_version": 1,
    "platform": platform,
    "client_build": build,
    "workload": workload,
    "treatment": treatment,
    "platform_key": digest(platform),
    "client_build_key": digest(build),
    "workload_key": digest(workload),
    "treatment_key": digest(treatment),
}
keys["cohort_key"] = digest({name: keys[name] for name in ("platform_key", "client_build_key", "workload_key", "treatment_key")})
report = {
    "aggregation_mode": "sweep",
    "run_gates_passed": True,
    "eligible_for_aggregation": True,
    "rps_measurement_basis": "successful_logical_responses/main_request_span",
    "main_request_span_seconds": float(span),
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
    "udp_attribution": {"available": True, "receive_buffer_errors_delta": 0, "packet_drops_delta": 0, "unexplained_delta": 0},
    "comparison_keys": keys,
}
if os.environ.get("FAKE_OMIT_RESOURCES") != "1":
    report["sweep_resource_usage"] = {
        "client_cpu_percent": 500.0,
        "server_cpu_percent": 450.0,
        "network_utilization_percent": 25.0,
        "p99_latency_ms": 2.0,
    }
    digest_value = "9" * 64
    common_cpu = {
        "source": "perf003_point_pair_process_cpu_time",
        "before_artifact_sha256": digest_value,
        "after_artifact_sha256": digest_value,
        "before_captured_at": "2026-10-11T00:00:00Z",
        "after_captured_at": "2026-10-11T00:00:10Z",
        "duration_ns": "10000000000",
        "pid": 123,
        "start_ticks": "456",
        "formula": "100 * cpu_time_delta_ns / capture_duration_ns",
    }
    report["sweep_resource_usage_required"] = True
    report["sweep_resource_usage_valid"] = True
    report["sweep_resource_usage_ineligibility_reasons"] = []
    report["sweep_resource_evidence_sha256"] = "8" * 64
    report["sweep_resource_usage_provenance"] = {
        "client_cpu_percent": {
            **common_cpu,
            "capture_scope": "client_point_snapshot_interval",
            "role": "h3fuzz",
            "cpu_time_delta_ns": "50000000000",
        },
        "server_cpu_percent": {
            **common_cpu,
            "capture_scope": "server_point_snapshot_interval",
            "role": "origin",
            "cpu_time_delta_ns": "45000000000",
        },
        "network_utilization_percent": {
            "source": "perf003_point_pair_interface_byte_counters",
            "capture_scope": "client_and_server_point_snapshot_intervals",
            "aggregation": "maximum_observed_full_duplex_direction_utilization",
            "formula": "100 * 8 * max(rx_bytes_delta, tx_bytes_delta) / (capture_duration_seconds * link_speed_bits_per_second)",
            "client_before_artifact_sha256": digest_value,
            "client_after_artifact_sha256": digest_value,
            "server_before_artifact_sha256": digest_value,
            "server_after_artifact_sha256": digest_value,
            "observations": [{
                "endpoint": "client",
                "ifindex": 2,
                "name": "eth0",
                "counter_source": "sysfs",
                "link_speed_bits_per_second": "1000000000",
                "capture_duration_ns": "10000000000",
                "rx_bytes_delta": "1",
                "tx_bytes_delta": "312500000",
                "full_duplex_direction_utilization_percent": 25.0,
            }],
        },
        "p99_latency_ms": {
            "source": "run_telemetry.responses.latency",
            "capture_scope": "main_successful_logical_responses",
            "telemetry_artifact_sha256": digest_value,
            "latency_schema_version": 1,
            "clock": "monotonic",
            "unit": "nanoseconds",
            "method": "bounded_histogram_upper_bound",
            "sample_count": count,
            "p99_upper_bound_ns": 2000000,
            "maximum_trackable_ns": 120000000000,
            "overflow_count": 0,
            "formula": "p99_upper_bound_ns / 1000000",
        },
    }
(artifact / "benchmark-report.json").write_text(json.dumps(report), encoding="utf-8")
raise SystemExit(int(os.environ.get("FAKE_BENCHMARK_EXIT", "0")))
'''


class FixedSweepShellTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.wordlist = self.root / "wordlist.txt"
        self.wordlist.write_text("one\ntwo\nthree\n", encoding="utf-8")
        self.wordlist_manifest = self.root / "wordlist-manifest.json"
        atomic_write_json(
            self.wordlist_manifest,
            add_manifest_hash(
                {
                    "schema_version": 1,
                    "manifest_type": "h3fuzz-frozen-wordlist",
                    "created_at": "2026-10-11T00:00:00Z",
                    "frozen": True,
                    "generator_version": 1,
                    "seed": 1,
                    "path": str(self.wordlist),
                    "line_count": 3,
                    "expanded_input_count": 3,
                    "sha256": file_sha256(self.wordlist),
                    "base_corpus": {"path": "/source/base", "line_count": 1, "unique_nonempty_line_count": 1, "sha256": "0" * 64},
                    "extensions": [],
                    "target_rps": 100000,
                    "target_seconds": 30,
                    "headroom_percent": 20,
                    "maximum_calibration_attempts": 3,
                    "calibration_attempts": [],
                    "artifact_directory": "/source/calibration",
                }
            ),
        )
        self.policy = self.root / "policy.json"
        atomic_write_json(
            self.policy,
            add_manifest_hash(
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
                    "noise_band_percent": 2.0,
                    "noise_band_formula": "100 * 1.4826 * MAD(completed_response_rps) / median(completed_response_rps)",
                    "client_cpu_max_percent": 750.0,
                    "server_cpu_max_percent": 700.0,
                    "network_utilization_max_percent": 80.0,
                    "p99_latency_max_ms": 10.0,
                    "minimum_improvement_percent": 3.0,
                    "platform_key": "a" * 64,
                    "client_build_key": "b" * 64,
                    "workload_key": "c" * 64,
                    "treatment_key": "d" * 64,
                    "cohort_key": "e" * 64,
                }
            ),
        )
        self.fake = self.root / "fake-benchmark.py"
        self.fake.write_text(FAKE_BENCHMARK, encoding="utf-8")
        self.fake.chmod(0o755)
        self.provider = self.root / "provider.sh"
        self.provider.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
        self.provider.chmod(0o755)
        self.log = self.root / "invocations.jsonl"

    def tearDown(self):
        self.temporary.cleanup()

    def command(self, artifact: Path, trials: str = "3") -> list[str]:
        return [
            str(SWEEP),
            "--target", "https://origin.test:8443/bench/FUZZ",
            "--wordlist", str(self.wordlist),
            "--wordlist-manifest", str(self.wordlist_manifest),
            "--expect-strategy", "head",
            "--filter-status", "204",
            "--connections", "4,8,128",
            "--streams", "64",
            "--trials-per-point", trials,
            "--max-workers", "65536",
            "--decision-policy", str(self.policy),
            "--server-snapshot-provider", str(self.provider),
            "--server-evidence-provider", str(self.provider),
            "--artifact-dir", str(artifact),
            "--manifest", str(artifact / "fixed-sweep-manifest.json"),
        ]

    def environment(self) -> dict[str, str]:
        environment = dict(os.environ)
        environment["H3FUZZ_FIXED_SWEEP_BENCHMARK"] = str(self.fake)
        environment["FAKE_INVOCATION_LOG"] = str(self.log)
        return environment

    def test_exact_three_trials_plateau_skip_and_immutable_manifest(self):
        artifact = self.root / "sweep"
        first = subprocess.run(
            self.command(artifact), env=self.environment(), check=False, capture_output=True, text=True
        )
        self.assertEqual(first.returncode, 0, first.stderr)
        invocations = self.log.read_text(encoding="utf-8").splitlines()
        self.assertEqual(len(invocations), 6)
        manifest_path = artifact / "fixed-sweep-manifest.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        self.assertEqual(
            [point["status"] for point in manifest["candidate_matrix"]],
            ["executed", "executed", "skipped"],
        )
        self.assertEqual(
            manifest["candidate_matrix"][2]["skipped_reasons"],
            ["high_connection_point_not_justified_after_plateau"],
        )
        self.assertEqual(manifest["selected_point"]["connections"], 4)
        self.assertEqual(manifest["adaptive_max_connections"], 4)
        self.assertEqual(manifest["adaptive_max_workers"], 65536)
        self.assertTrue(all(not Path(member["path"]).is_absolute() for member in manifest["members"]))
        _, certification_selection = certification_report.verify_fixed_sweep(manifest_path)
        self.assertEqual(certification_selection["connections"], 4)
        self.assertEqual(certification_selection["streams"], 64)
        self.assertEqual(certification_selection["adaptive_max_workers"], 65536)

        before = manifest_path.read_bytes()
        second = subprocess.run(
            self.command(artifact), env=self.environment(), check=False, capture_output=True, text=True
        )
        self.assertEqual(second.returncode, 2)
        self.assertEqual(manifest_path.read_bytes(), before)
        self.assertEqual(len(self.log.read_text(encoding="utf-8").splitlines()), 6)

    def test_missing_resources_finishes_ineligible_and_skips_remaining_curve(self):
        artifact = self.root / "ineligible"
        environment = self.environment()
        environment["FAKE_OMIT_RESOURCES"] = "1"
        completed = subprocess.run(
            self.command(artifact), env=environment, check=False, capture_output=True, text=True
        )
        self.assertEqual(completed.returncode, 3, completed.stderr)
        manifest = json.loads((artifact / "fixed-sweep-manifest.json").read_text())
        self.assertFalse(manifest["eligible"])
        self.assertEqual(len(self.log.read_text().splitlines()), 6)
        self.assertEqual(manifest["candidate_matrix"][0]["classification"], "ineligible")
        self.assertEqual(
            [item["status"] for item in manifest["candidate_matrix"]],
            ["executed", "executed", "skipped"],
        )

    def test_trial_count_other_than_three_fails_before_creating_artifacts(self):
        artifact = self.root / "bad-trials"
        completed = subprocess.run(
            self.command(artifact, trials="2"),
            env=self.environment(),
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(completed.returncode, 2)
        self.assertFalse(artifact.exists())
        self.assertFalse(self.log.exists())

    def test_short_trial_invalidates_generation_and_skips_every_later_point(self):
        artifact = self.root / "short"
        environment = self.environment()
        environment["FAKE_SHORT_CONNECTION"] = "4"
        completed = subprocess.run(
            self.command(artifact),
            env=environment,
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(completed.returncode, 3, completed.stderr)
        manifest = json.loads((artifact / "fixed-sweep-manifest.json").read_text())
        self.assertIn(
            "frozen_wordlist_generation_invalid_short_trial",
            manifest["ineligible_reasons"],
        )
        self.assertEqual(
            [item["status"] for item in manifest["candidate_matrix"]],
            ["executed", "skipped", "skipped"],
        )
        self.assertEqual(len(self.log.read_text().splitlines()), 3)


if __name__ == "__main__":
    unittest.main()
