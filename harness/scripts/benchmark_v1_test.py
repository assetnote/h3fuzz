#!/usr/bin/env python3

import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("benchmark-v1.py")
SPEC = importlib.util.spec_from_file_location("benchmark_v1", SCRIPT)
benchmark_v1 = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(benchmark_v1)


class BenchmarkV1ContractTest(unittest.TestCase):
    def parse(self, root: Path, mode: str, extra: list[str]):
        wordlist = root / "words.txt"
        wordlist.write_text("one\n", encoding="utf-8")
        provider = root / "provider"
        provider.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        provider.chmod(0o755)
        artifact = root / "run"
        common = [
            "--target", "https://origin.test:8443/bench/FUZZ",
            "--wordlist", str(wordlist),
            "--mode", mode,
            "--expect-strategy", "head",
            "--filter-status", "204",
            "--streams", "256",
            "--run-id", "11111111-1111-4111-8111-111111111111",
            "--server-snapshot-provider", str(provider),
            "--server-evidence-provider", str(provider),
            "--client-before", str(artifact / "client-before.json"),
            "--client-after", str(artifact / "client-after.json"),
            "--client-runtime-sockets", str(artifact / "runtime.jsonl"),
            "--server-counters-before", str(artifact / "counters-before.json"),
            "--server-counters-after", str(artifact / "counters-after.json"),
            "--server-stable", str(artifact / "server-stable.json"),
            "--server-evidence-before", str(artifact / "server-before.json"),
            "--server-evidence-after", str(artifact / "server-after.json"),
            "--artifact-dir", str(artifact),
        ]
        return benchmark_v1.parse_args([*common, *extra])

    def test_adaptive_rejects_manual_fixed_connection(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.parse(Path(directory), "adaptive", [
                "--connections", "20", "--max-connections", "20", "--max-workers", "65536"
            ])
            with self.assertRaisesRegex(ValueError, "rejects --connections"):
                benchmark_v1.validate(args)

    def test_selected_fixed_rejects_manual_adaptive_maximum(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.parse(Path(directory), "selected-fixed", [
                "--connections", "20", "--max-connections", "20", "--max-workers", "65536"
            ])
            with self.assertRaisesRegex(ValueError, "rejects --max-connections"):
                benchmark_v1.validate(args)

    def test_sweep_rejects_explicit_legacy_threads(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.parse(Path(directory), "sweep", [
                "--connections", "20", "--threads", "5120", "--max-workers", "65536"
            ])
            with self.assertRaisesRegex(ValueError, "rejects --threads"):
                benchmark_v1.validate(args)

    def test_sweep_produces_versioned_resource_evidence_for_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = self.parse(
                root,
                "sweep",
                ["--connections", "8", "--max-workers", "65536"],
            )
            expected = {
                "schema_version": 1,
                "kind": "sweep_resource_usage",
                "run_id": args.run_id,
            }
            telemetry = args.artifact_dir / "run-telemetry.json"
            with (
                mock.patch.object(
                    benchmark_v1,
                    "build_sweep_resource_evidence",
                    return_value=expected,
                ) as build,
                mock.patch.object(benchmark_v1, "atomic_write_json") as write,
            ):
                output = benchmark_v1.produce_sweep_resource_evidence(args, telemetry)
            self.assertEqual(output, args.artifact_dir / "sweep-resource-usage.json")
            build.assert_called_once_with(
                run_id=args.run_id,
                telemetry_path=telemetry,
                client_before_path=args.client_before,
                client_after_path=args.client_after,
                server_before_path=args.server_evidence_before,
                server_after_path=args.server_evidence_after,
            )
            write.assert_called_once_with(output, expected)

    def test_non_sweep_mode_does_not_emit_sweep_resource_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.parse(
                Path(directory),
                "selected-fixed",
                ["--connections", "8", "--max-workers", "65536"],
            )
            with mock.patch.object(benchmark_v1, "build_sweep_resource_evidence") as build:
                output = benchmark_v1.produce_sweep_resource_evidence(
                    args, args.artifact_dir / "run-telemetry.json"
                )
            self.assertIsNone(output)
            build.assert_not_called()


if __name__ == "__main__":
    unittest.main()
