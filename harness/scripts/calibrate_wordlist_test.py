#!/usr/bin/env python3

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest


SCRIPT = Path(__file__).with_name("calibrate-wordlist.sh")


class CalibrateWordlistTest(unittest.TestCase):
    def make_fake_benchmark(self, root: Path, spans: list[float], rates: list[float]) -> Path:
        fake = root / "fake-benchmark.py"
        fake.write_text(
            textwrap.dedent(
                f"""\
                #!/usr/bin/env python3
                import json
                from pathlib import Path
                import sys

                args = sys.argv[1:]
                output = Path(args[args.index('--artifact-dir') + 1])
                state = Path({str(root / 'state')!r})
                attempt = int(state.read_text() if state.exists() else '0')
                state.write_text(str(attempt + 1))
                spans = {spans!r}
                rates = {rates!r}
                span = spans[min(attempt, len(spans) - 1)]
                rate = rates[min(attempt, len(rates) - 1)]
                output.mkdir(parents=True, exist_ok=False)
                (output / 'benchmark-report.json').write_text(json.dumps({{
                    'run_gates_passed': True,
                    'eligible_for_aggregation': True,
                    'main_request_span_seconds': span,
                    'completed_response_rps': rate,
                    'rps_measurement_basis': 'successful_logical_responses/main_request_span',
                }}))
                """
            ),
            encoding="utf-8",
        )
        fake.chmod(0o755)
        return fake

    def invoke(self, root: Path, fake: Path) -> subprocess.CompletedProcess:
        base = root / "base.txt"
        base.write_text("known-hit\nsecond-hit\n", encoding="utf-8")
        output = root / "frozen.txt"
        manifest = root / "frozen.manifest.json"
        artifacts = root / "artifacts"
        env = os.environ.copy()
        env["H3FUZZ_BENCHMARK_SCRIPT"] = str(fake)
        return subprocess.run(
            [
                str(SCRIPT),
                "--target", "https://origin.test:8443/bench/FUZZ",
                "--base-wordlist", str(base),
                "--seed", "1001",
                "--target-rps", "2",
                "--target-seconds", "30",
                "--headroom-percent", "20",
                "--expect-strategy", "head",
                "--filter-status", "204",
                "--streams", "256",
                "--max-connections", "20",
                "--max-workers", "1024",
                "--server-snapshot-provider", str(fake),
                "--server-evidence-provider", str(fake),
                "--output", str(output),
                "--manifest", str(manifest),
                "--artifact-dir", str(artifacts),
            ],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
            check=False,
        )

    def test_calibration_is_deterministic_bounded_and_frozen(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = self.make_fake_benchmark(root, [20.0, 30.0], [4.0, 4.0])
            result = self.invoke(root, fake)
            self.assertEqual(result.returncode, 0, result.stderr)
            manifest = json.loads((root / "frozen.manifest.json").read_text(encoding="utf-8"))
            payload = (root / "frozen.txt").read_bytes()
            self.assertTrue(manifest["frozen"])
            self.assertEqual(manifest["line_count"], 144)
            self.assertEqual(manifest["sha256"], hashlib.sha256(payload).hexdigest())
            self.assertEqual(len(manifest["calibration_attempts"]), 2)
            self.assertEqual(manifest["calibration_attempts"][0]["next_inputs"], 144)
            self.assertEqual(payload.splitlines()[:2], [b"known-hit", b"second-hit"])

    def test_calibration_stops_after_three_underlength_attempts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = self.make_fake_benchmark(root, [1.0, 1.0, 1.0], [4.0, 5.0, 6.0])
            result = self.invoke(root, fake)
            self.assertEqual(result.returncode, 3, result.stderr)
            self.assertEqual((root / "state").read_text(), "3")
            self.assertFalse((root / "frozen.manifest.json").exists())


if __name__ == "__main__":
    unittest.main()
