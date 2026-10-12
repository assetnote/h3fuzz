#!/usr/bin/env python3

from pathlib import Path
import importlib.util
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("certify.sh")
BACKEND = Path(__file__).with_name("certify.py")
SPEC = importlib.util.spec_from_file_location("certify_backend_test", BACKEND)
certify = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(certify)


class CertifyContractTest(unittest.TestCase):
    def test_measured_cohorts_are_interleaved(self):
        calls: list[tuple[str, int]] = []

        def fake_runner(args, stage, cohort, index, selection):
            calls.append((cohort, index))
            return stage / cohort / f"run-{index:02d}" / "benchmark-report.json"

        adaptive, fixed, order = certify.run_alternating_cohorts(
            object(), Path("/stage"), {}, runner=fake_runner
        )

        self.assertEqual(
            calls,
            [item for index in range(1, 6) for item in (("adaptive", index), ("selected-fixed", index))],
        )
        self.assertEqual(len(adaptive), 5)
        self.assertEqual(len(fixed), 5)
        self.assertEqual([entry["sequence"] for entry in order], list(range(1, 11)))
        self.assertEqual([entry["cohort"] for entry in order], [cohort for cohort, _ in calls])

    def base_args(self, root: Path) -> list[str]:
        files = {}
        for name in ("wordlist", "wordlist-manifest", "fixed-sweep", "files", "diagnostics"):
            path = root / f"{name}.json"
            path.write_text("{}\n", encoding="utf-8")
            files[name] = path
        files["wordlist"].write_text("one\n", encoding="utf-8")
        store = root / "store"
        store.mkdir()
        provider = root / "provider.sh"
        provider.write_text('#!/bin/sh\ntouch "$0.called"\nexit 99\n', encoding="utf-8")
        provider.chmod(0o700)
        return [
            "--target", "https://origin.test:8443/bench/FUZZ",
            "--wordlist", str(files["wordlist"]),
            "--wordlist-manifest", str(files["wordlist-manifest"]),
            "--expect-strategy", "head",
            "--filter-status", "204",
            "--fixed-sweep-manifest", str(files["fixed-sweep"]),
            "--files-validation-manifest", str(files["files"]),
            "--diagnostics-manifest", str(files["diagnostics"]),
            "--target-rps", "100000",
            "--server-snapshot-provider", str(provider),
            "--server-evidence-provider", str(provider),
            "--artifact-store", str(store),
        ]

    def test_rejects_manual_connection_stream_and_worker_overrides(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.base_args(Path(directory))
            for flag in ("--connections", "--streams", "--max-workers"):
                result = subprocess.run(
                    [str(SCRIPT), *args, flag, "20"],
                    text=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    check=False,
                )
                self.assertEqual(result.returncode, 2, (flag, result.stderr))

    def test_rejects_artifact_store_inside_harness_work(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.base_args(Path(directory))
            work_store = SCRIPT.parent.parent / ".work" / "not-durable"
            index = args.index("--artifact-store") + 1
            args[index] = str(work_store)
            result = subprocess.run(
                [str(SCRIPT), *args],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )
            self.assertEqual(result.returncode, 2, result.stderr)

    def test_malformed_prerequisite_is_rejected_before_any_benchmark(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = self.base_args(root)
            result = subprocess.run(
                [str(SCRIPT), *args],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=False,
            )
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertFalse((root / "provider.sh.called").exists())


if __name__ == "__main__":
    unittest.main()
