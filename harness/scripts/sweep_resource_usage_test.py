#!/usr/bin/env python3

import json
from pathlib import Path
import sys
import tempfile
import unittest


sys.path.insert(0, str(Path(__file__).resolve().parent))
import sweep_resource_usage as resource  # noqa: E402


RUN_ID = "11111111-1111-4111-8111-111111111111"


def latency(sample_count: int = 2, *, overflow_count: int = 0) -> dict:
    return {
        "schema_version": 1,
        "source": "client_final_attempt_time_to_first_byte",
        "scope": "main_successful_logical_responses",
        "clock": "monotonic",
        "unit": "nanoseconds",
        "method": "bounded_histogram_upper_bound",
        "sample_count": sample_count,
        "p99_upper_bound_ns": 2_000_000,
        "maximum_trackable_ns": 120_000_000_000,
        "overflow_count": overflow_count,
    }


def telemetry(response_latency: dict | None = None) -> dict:
    responses = {
        "successful_logical_responses": 2,
    }
    if response_latency is not None:
        responses["latency"] = response_latency
    return {
        "schema_version": 2,
        "run_id": RUN_ID,
        "responses": responses,
        "quic": {
            "latest_rtt_ns": 100,
            "smoothed_rtt_ns": 200,
        },
    }


def point(
    endpoint: str,
    phase: str,
    *,
    cpu_time_ns: int,
    rx_bytes: int,
    tx_bytes: int,
    link_speed: str | None = "1000000000",
) -> dict:
    role = "h3fuzz" if endpoint == "client" else "origin"
    captured_at = (
        "2026-10-11T00:00:00.000000000Z"
        if phase == "before"
        else "2026-10-11T00:00:10.000000000Z"
    )
    return {
        "schema_version": 1,
        "kind": f"{endpoint}_point",
        "phase": phase,
        "run_id": RUN_ID,
        "captured_at": captured_at,
        "host_identity_sha256": ("a" if endpoint == "client" else "b") * 64,
        "boot_id": (
            "22222222-2222-4222-8222-222222222222"
            if endpoint == "client"
            else "33333333-3333-4333-8333-333333333333"
        ),
        "network_namespace_inode": "123",
        "processes": [{
            "role": role,
            "pid": 123 if endpoint == "client" else 456,
            "start_ticks": "789",
            "cpu_time_ns": str(cpu_time_ns),
        }],
        "interfaces": [{
            "ifindex": 2,
            "name": "eth0",
            "source": "sysfs",
            "link_speed_bits_per_second": link_speed,
            "rx_bytes": str(rx_bytes),
            "tx_bytes": str(tx_bytes),
            "counter_width_bits": 64,
        }],
    }


class SweepResourceUsageTest(unittest.TestCase):
    def create_fixture(
        self,
        root: Path,
        *,
        response_latency: dict | None = None,
        client_link_speed: str | None = "1000000000",
    ) -> dict[str, Path]:
        values = {
            "telemetry": telemetry(latency() if response_latency is None else response_latency),
            "client_before": point(
                "client", "before", cpu_time_ns=1_000_000_000,
                rx_bytes=1_000, tx_bytes=2_000, link_speed=client_link_speed,
            ),
            "client_after": point(
                "client", "after", cpu_time_ns=6_000_000_000,
                rx_bytes=10_001_000, tx_bytes=100_002_000, link_speed=client_link_speed,
            ),
            "server_before": point(
                "server", "before", cpu_time_ns=2_000_000_000,
                rx_bytes=3_000, tx_bytes=4_000,
            ),
            "server_after": point(
                "server", "after", cpu_time_ns=9_500_000_000,
                rx_bytes=100_003_000, tx_bytes=10_004_000,
            ),
        }
        paths = {}
        for name, value in values.items():
            path = root / f"{name}.json"
            path.write_text(json.dumps(value), encoding="utf-8")
            paths[name] = path
        return paths

    def build(self, paths: dict[str, Path]) -> dict:
        return resource.build_evidence(
            run_id=RUN_ID,
            telemetry_path=paths["telemetry"],
            client_before_path=paths["client_before"],
            client_after_path=paths["client_after"],
            server_before_path=paths["server_before"],
            server_after_path=paths["server_after"],
        )

    def test_derives_all_metrics_and_retains_formula_provenance(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = self.build(self.create_fixture(Path(directory)))
        self.assertTrue(evidence["eligible"])
        self.assertAlmostEqual(evidence["metrics"]["client_cpu_percent"], 50.0)
        self.assertAlmostEqual(evidence["metrics"]["server_cpu_percent"], 75.0)
        self.assertAlmostEqual(evidence["metrics"]["network_utilization_percent"], 8.0)
        self.assertAlmostEqual(evidence["metrics"]["p99_latency_ms"], 2.0)
        self.assertEqual(
            evidence["provenance"]["p99_latency_ms"]["source"],
            "run_telemetry.responses.latency",
        )
        resource.validate_evidence(evidence, expected_run_id=RUN_ID)

    def test_quic_rtt_is_not_accepted_when_response_latency_is_absent(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self.create_fixture(Path(directory))
            value = telemetry(None)
            paths["telemetry"].write_text(json.dumps(value), encoding="utf-8")
            evidence = self.build(paths)
        self.assertFalse(evidence["eligible"])
        self.assertIsNone(evidence["metrics"])
        self.assertTrue(any("latency_contract_invalid" in item for item in evidence["ineligibility_reasons"]))

    def test_latency_sample_mismatch_and_overflow_fail_closed(self):
        for bad_latency, expected in (
            (latency(sample_count=1), "latency_sample_count_mismatch"),
            (latency(overflow_count=1), "latency_histogram_overflow"),
        ):
            with self.subTest(expected=expected), tempfile.TemporaryDirectory() as directory:
                evidence = self.build(
                    self.create_fixture(Path(directory), response_latency=bad_latency)
                )
                self.assertFalse(evidence["eligible"])
                self.assertTrue(any(expected in item for item in evidence["ineligibility_reasons"]))

    def test_utilized_interface_without_link_speed_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = self.build(
                self.create_fixture(Path(directory), client_link_speed=None)
            )
        self.assertFalse(evidence["eligible"])
        self.assertTrue(any("link_speed_invalid" in item for item in evidence["ineligibility_reasons"]))

    def test_validator_recomputes_metrics_from_provenance(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = self.build(self.create_fixture(Path(directory)))
        evidence["metrics"]["client_cpu_percent"] = 49.0
        with self.assertRaisesRegex(resource.ResourceEvidenceError, "formula_mismatch"):
            resource.validate_evidence(evidence, expected_run_id=RUN_ID)

    def test_committed_schemas_match_the_runtime_contract(self):
        schema_dir = Path(__file__).resolve().parent.parent / "schemas"
        latency_schema = json.loads(
            (schema_dir / "run-response-latency-v1.schema.json").read_text(encoding="utf-8")
        )
        evidence_schema = json.loads(
            (schema_dir / "sweep-resource-usage-v1.schema.json").read_text(encoding="utf-8")
        )
        self.assertEqual(set(latency_schema["required"]), resource.LATENCY_FIELDS)
        self.assertEqual(latency_schema["properties"]["method"]["const"], resource.LATENCY_CONSTANTS["method"])
        self.assertEqual(evidence_schema["properties"]["kind"]["const"], "sweep_resource_usage")
        self.assertEqual(
            set(evidence_schema["$defs"]["resourceMetrics"]["required"]),
            set(resource.RESOURCE_FIELDS),
        )


if __name__ == "__main__":
    unittest.main()
