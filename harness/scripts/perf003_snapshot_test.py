#!/usr/bin/env python3

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tempfile
import unittest

SCRIPT_DIR = Path(__file__).resolve().parent
HARNESS_DIR = SCRIPT_DIR.parent
FIXTURE = HARNESS_DIR / "tests" / "fixtures" / "perf003"
ENVIRONMENT_SNAPSHOT = SCRIPT_DIR / "environment-snapshot.sh"
NETWORK_SNAPSHOT = SCRIPT_DIR / "network-snapshot.sh"


class Perf003SnapshotTest(unittest.TestCase):
    def run_server_stable(
        self,
        output: Path,
        *,
        fixture_root: Path | None = None,
        source_root: Path | None = None,
        debug_log: Path | None = None,
        rtt_ms: float | None = None,
    ) -> dict:
        command = [
            str(ENVIRONMENT_SNAPSHOT),
            "--kind",
            "server_stable",
            "--output",
            str(output),
            "--fixture-root",
            str(fixture_root or FIXTURE / "linux"),
            "--source-root",
            str(source_root or FIXTURE / "source"),
            "--binary",
            str(FIXTURE / "input" / "h3fuzz"),
            "--origin-binary",
            str(FIXTURE / "input" / "origin"),
            "--origin-image",
            "sha256:fixture-origin-image",
            "--origin-config",
            str(FIXTURE / "input" / "origin.json"),
            "--harness-image",
            "h3fuzz-harness:fixture",
            "--command-json",
            str(FIXTURE / "input" / "command.json"),
            "--environment-json",
            str(FIXTURE / "input" / "environment.json"),
            "--debug-log",
            str(debug_log or FIXTURE / "input" / "h3fuzz-debug.log"),
            "--target",
            "https://origin.example:8443/bench/FUZZ",
            "--wordlist",
            str(FIXTURE / "input" / "words.txt"),
            "--repeat-mode",
            "exact:2",
            "--client-pid",
            "4242",
            "--server-pid",
            "5252",
        ]
        if rtt_ms is not None:
            command.extend(["--rtt-ms", str(rtt_ms)])
        subprocess.run(
            command,
            check=True,
            text=True,
            capture_output=True,
        )
        return json.loads(output.read_text(encoding="utf-8"))

    def test_server_stable_snapshot_captures_reproducible_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "server-stable.json"
            snapshot = self.run_server_stable(output)
            self.assertEqual(snapshot["schema_version"], 1)
            self.assertEqual(snapshot["kind"], "server_stable")
            self.assertEqual(snapshot["captured_at"], "2026-10-11T00:00:00.123456789Z")
            self.assertEqual(
                snapshot["kernel"], {"sysname": "Linux", "release": "6.8.0-fixture"}
            )
            self.assertEqual(snapshot["cpu"]["affinity_cpus"], [0, 1])
            self.assertEqual(snapshot["memory_bytes"], 16_777_216_000)
            self.assertEqual(snapshot["network_namespace_inode"], "4026533000")
            self.assertEqual(
                snapshot["udp_sysctls_bytes"],
                {
                    "rmem_default": 212_992,
                    "rmem_max": 7_340_032,
                    "wmem_default": 212_992,
                    "wmem_max": 7_340_032,
                },
            )
            self.assertRegex(snapshot["host_identity_sha256"], r"^[0-9a-f]{64}$")
            self.assertRegex(
                snapshot["source_tree"]["canonical_sha256"], r"^[0-9a-f]{64}$"
            )
            self.assertEqual(snapshot["source_tree"]["file_count"], 2)
            self.assertEqual(
                snapshot["h3fuzz"]["modules"]["github.com/quic-go/quic-go"], "v0.63.0"
            )
            self.assertEqual(
                snapshot["h3fuzz"]["modules"]["github.com/quic-go/qpack"], "v0.6.0"
            )
            self.assertEqual(
                snapshot["h3fuzz"]["binary_sha256"],
                hashlib.sha256((FIXTURE / "input" / "h3fuzz").read_bytes()).hexdigest(),
            )
            self.assertEqual(snapshot["target"]["resolved_addresses"], ["203.0.113.10"])
            self.assertEqual(snapshot["target"]["measured_rtt_ms"], 1.25)
            self.assertEqual(snapshot["wordlist"]["line_count"], 3)
            self.assertEqual(snapshot["wordlist"]["repeat_mode"], "exact:2")
            self.assertEqual(snapshot["affinity"]["client"]["cpus"], [0])
            self.assertEqual(snapshot["affinity"]["server"]["cpus"], [1])
            self.assertEqual(
                snapshot["invocation"]["environment_overrides"]["H3FUZZ_TOKEN"],
                "<redacted>",
            )
            self.assertEqual(snapshot["invocation"]["argv"][-1], "<redacted>")
            self.assertTrue(snapshot["quic_go_log"]["retained"])
            self.assertFalse(snapshot["quic_go_log"]["unresolved_udp_buffer_warning"])
            self.assertEqual(snapshot["quic_go_log"]["udp_buffer_warning_count"], 0)
            self.assertRegex(snapshot["quic_go_log"]["sha256"], r"^[0-9a-f]{64}$")
            self.assertTrue(snapshot["eligible"])
            self.assertEqual(snapshot["ineligibility_reasons"], [])
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)

    def test_explicit_measured_rtt_overrides_fixture_probe(self):
        with tempfile.TemporaryDirectory() as directory:
            snapshot = self.run_server_stable(
                Path(directory) / "server-stable.json", rtt_ms=4.75
            )
            self.assertEqual(snapshot["target"]["measured_rtt_ms"], 4.75)
            self.assertEqual(snapshot["target"]["rtt_source"], "operator_measurement")

    def test_unresolved_quic_go_udp_buffer_warning_makes_snapshot_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            debug_log = root / "h3fuzz-debug.log"
            debug_log.write_text(
                "failed to sufficiently increase receive buffer size "
                "(was: 212 kiB, wanted: 7168 kiB, got: 416 kiB)\n",
                encoding="utf-8",
            )
            snapshot = self.run_server_stable(
                root / "server-stable.json", debug_log=debug_log
            )

            self.assertTrue(snapshot["quic_go_log"]["unresolved_udp_buffer_warning"])
            self.assertEqual(snapshot["quic_go_log"]["udp_buffer_warning_count"], 1)
            self.assertFalse(snapshot["eligible"])
            self.assertIn(
                "unresolved_quic_go_udp_buffer_warning",
                snapshot["ineligibility_reasons"],
            )

    @unittest.skipUnless(
        shutil.which("git"), "git is required for source identity test"
    )
    def test_source_identity_changes_for_tracked_edits_and_untracked_build_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            shutil.copytree(FIXTURE / "source", source)
            subprocess.run(["git", "init", "-q", str(source)], check=True)
            subprocess.run(
                ["git", "-C", str(source), "config", "user.name", "Fixture"], check=True
            )
            subprocess.run(
                [
                    "git",
                    "-C",
                    str(source),
                    "config",
                    "user.email",
                    "fixture@example.invalid",
                ],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(source), "add", "go.mod", "main.go"], check=True
            )
            subprocess.run(
                ["git", "-C", str(source), "commit", "-qm", "fixture"], check=True
            )

            clean = self.run_server_stable(root / "clean.json", source_root=source)[
                "source_tree"
            ]
            self.assertTrue(clean["git_available"])
            self.assertFalse(clean["dirty"])

            (source / "main.go").write_text(
                "package main\n\nfunc main() { println(1) }\n", encoding="utf-8"
            )
            tracked = self.run_server_stable(root / "tracked.json", source_root=source)[
                "source_tree"
            ]
            self.assertTrue(tracked["dirty"])
            self.assertNotEqual(clean["canonical_sha256"], tracked["canonical_sha256"])

            (source / "generated.go").write_text(
                "package main\n\nconst generated = true\n", encoding="utf-8"
            )
            untracked = self.run_server_stable(
                root / "untracked.json", source_root=source
            )["source_tree"]
            self.assertEqual(untracked["file_count"], tracked["file_count"] + 1)
            self.assertNotEqual(
                tracked["canonical_sha256"], untracked["canonical_sha256"]
            )

            os.chmod(source / "main.go", 0o755)
            mode_changed = self.run_server_stable(
                root / "mode-changed.json", source_root=source
            )["source_tree"]
            self.assertNotEqual(
                untracked["canonical_sha256"], mode_changed["canonical_sha256"]
            )

    def test_portable_client_snapshot_is_retained_as_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "client-stable.json"
            subprocess.run(
                [
                    str(ENVIRONMENT_SNAPSHOT),
                    "--kind",
                    "client_stable",
                    "--output",
                    str(output),
                    "--fixture-root",
                    str(FIXTURE / "portable"),
                    "--source-root",
                    str(FIXTURE / "source"),
                    "--binary",
                    str(FIXTURE / "input" / "h3fuzz"),
                    "--command-json",
                    str(FIXTURE / "input" / "command.json"),
                    "--environment-json",
                    str(FIXTURE / "input" / "environment.json"),
                    "--debug-log",
                    str(FIXTURE / "input" / "h3fuzz-debug.log"),
                    "--target",
                    "https://origin.example:8443/bench/FUZZ",
                    "--wordlist",
                    str(FIXTURE / "input" / "words.txt"),
                    "--repeat-mode",
                    "exact:2",
                ],
                check=True,
                text=True,
                capture_output=True,
            )

            snapshot = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(snapshot["kind"], "client_stable")
            self.assertEqual(snapshot["kernel"]["sysname"], "Darwin")
            self.assertIsNone(snapshot["boot_id"])
            self.assertIsNone(snapshot["network_namespace_inode"])
            self.assertIsNone(snapshot["udp_sysctls_bytes"])
            self.assertFalse(snapshot["eligible"])
            self.assertIn("native_linux_required", snapshot["ineligibility_reasons"])
            self.assertIn("boot_id_unavailable", snapshot["ineligibility_reasons"])
            self.assertIn(
                "network_namespace_unavailable", snapshot["ineligibility_reasons"]
            )
            self.assertIn("udp_sysctls_unavailable", snapshot["ineligibility_reasons"])

    def test_portable_network_point_is_retained_as_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "client-before.json"
            subprocess.run(
                [
                    str(NETWORK_SNAPSHOT),
                    "--mode",
                    "point",
                    "--kind",
                    "client_point",
                    "--phase",
                    "before",
                    "--run-id",
                    "12345678-1234-4abc-8def-1234567890ab",
                    "--output",
                    str(output),
                    "--fixture-root",
                    str(FIXTURE / "portable"),
                    "--process",
                    "h3fuzz:4242",
                ],
                check=True,
                text=True,
                capture_output=True,
            )

            snapshot = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(snapshot["kind"], "client_point")
            self.assertIsNone(snapshot["boot_id"])
            self.assertIsNone(snapshot["network_namespace_inode"])
            self.assertIsNone(snapshot["udp_counters"])
            self.assertIsNone(snapshot["interfaces"])
            self.assertIsNone(snapshot["processes"])
            self.assertIsNone(snapshot["sockets"])
            self.assertFalse(snapshot["eligible"])
            self.assertIn("native_linux_required", snapshot["ineligibility_reasons"])

    def test_server_point_snapshot_captures_attributable_counters_and_sockets(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "server-before.json"
            subprocess.run(
                [
                    str(NETWORK_SNAPSHOT),
                    "--mode",
                    "point",
                    "--phase",
                    "before",
                    "--run-id",
                    "12345678-1234-4abc-8def-1234567890ab",
                    "--output",
                    str(output),
                    "--fixture-root",
                    str(FIXTURE / "linux"),
                    "--process",
                    "origin:5252",
                ],
                check=True,
                text=True,
                capture_output=True,
            )

            snapshot = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(snapshot["schema_version"], 1)
            self.assertEqual(snapshot["kind"], "server_point")
            self.assertEqual(snapshot["phase"], "before")
            self.assertEqual(snapshot["run_id"], "12345678-1234-4abc-8def-1234567890ab")
            counters = {item["name"]: item for item in snapshot["udp_counters"]}
            self.assertEqual(counters["RcvbufErrors"]["value"], "4")
            self.assertEqual(counters["InErrors"]["value"], "3")
            self.assertEqual(counters["SndbufErrors"]["width_bits"], 64)
            self.assertEqual(snapshot["interfaces"][0]["name"], "eth0")
            self.assertEqual(
                snapshot["interfaces"][0]["link_speed_bits_per_second"], "25000000000"
            )
            self.assertEqual(snapshot["interfaces"][0]["rx_dropped"], "13")
            self.assertEqual(snapshot["processes"][0]["role"], "origin")
            self.assertEqual(snapshot["processes"][0]["start_ticks"], "12345")
            self.assertEqual(snapshot["processes"][0]["cpu_time_ns"], "1500000000")
            self.assertEqual(
                snapshot["processes"][0]["cgroup_cpu_throttled_ns"], "789000"
            )
            self.assertEqual(snapshot["sockets"][0]["inode"], "9001")
            self.assertEqual(snapshot["sockets"][0]["local_address"], "127.0.0.1:8443")
            self.assertEqual(snapshot["sockets"][0]["raw_so_rcvbuf_bytes"], 7_340_032)
            self.assertEqual(snapshot["sockets"][0]["raw_so_sndbuf_bytes"], 7_340_032)
            self.assertEqual(
                snapshot["sockets"][0]["operator_requested_so_rcvbuf_bytes"], 7_340_032
            )
            self.assertEqual(
                snapshot["sockets"][0]["operator_requested_so_sndbuf_bytes"], 7_340_032
            )
            self.assertTrue(snapshot["sockets"][0]["linux_kernel_doubling_applies"])
            self.assertTrue(snapshot["eligible"])
            self.assertEqual(snapshot["ineligibility_reasons"], [])
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)

    def test_ancestor_cgroup_throttle_counter_is_retained_but_not_process_scoped(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "linux"
            shutil.copytree(FIXTURE / "linux", fixture)
            (fixture / "sys/fs/cgroup/bench/cpu.stat").write_text(
                "usage_usec 123456\nuser_usec 100000\nsystem_usec 23456\n",
                encoding="utf-8",
            )
            (fixture / "sys/fs/cgroup/cpu.stat").write_text(
                "usage_usec 999999\nnr_throttled 0\nthrottled_usec 0\n",
                encoding="utf-8",
            )
            output = root / "server-before.json"
            subprocess.run(
                [
                    str(NETWORK_SNAPSHOT),
                    "--mode",
                    "point",
                    "--phase",
                    "before",
                    "--run-id",
                    "12345678-1234-4abc-8def-1234567890ab",
                    "--output",
                    str(output),
                    "--fixture-root",
                    str(fixture),
                    "--process",
                    "origin:5252",
                ],
                check=True,
                text=True,
                capture_output=True,
            )

            snapshot = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(snapshot["processes"][0]["cpu_time_ns"], "1500000000")
            self.assertEqual(snapshot["processes"][0]["cgroup_cpu_throttled_ns"], "0")
            self.assertEqual(
                snapshot["processes"][0]["cgroup_cpu_throttle_scope"], "ancestor"
            )
            self.assertFalse(snapshot["eligible"])
            self.assertIn(
                "cgroup_cpu_throttling_ancestor_scoped",
                snapshot["ineligibility_reasons"],
            )

    def test_counter_comparison_distinguishes_wrap_from_reset(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            before_path = root / "before.json"
            subprocess.run(
                [
                    str(NETWORK_SNAPSHOT),
                    "--mode",
                    "point",
                    "--phase",
                    "before",
                    "--run-id",
                    "12345678-1234-4abc-8def-1234567890ab",
                    "--output",
                    str(before_path),
                    "--fixture-root",
                    str(FIXTURE / "linux"),
                    "--process",
                    "origin:5252",
                ],
                check=True,
                text=True,
                capture_output=True,
            )
            before = json.loads(before_path.read_text(encoding="utf-8"))
            before_out = next(
                item
                for item in before["udp_counters"]
                if item["name"] == "OutDatagrams"
            )
            before_out["value"] = "250"
            before_out["width_bits"] = 8
            before_path.write_text(json.dumps(before), encoding="utf-8")

            after = json.loads(json.dumps(before))
            after["phase"] = "after"
            after_out = next(
                item for item in after["udp_counters"] if item["name"] == "OutDatagrams"
            )
            after_out["value"] = "3"
            after_path = root / "after.json"
            after_path.write_text(json.dumps(after), encoding="utf-8")
            wrap_output = root / "wrap.json"
            wrap_result = subprocess.run(
                [
                    str(NETWORK_SNAPSHOT),
                    "--mode",
                    "compare",
                    "--before",
                    str(before_path),
                    "--after",
                    str(after_path),
                    "--output",
                    str(wrap_output),
                    "--dedicated-host",
                ],
                text=True,
                capture_output=True,
            )
            self.assertEqual(wrap_result.returncode, 0)
            wrap = json.loads(wrap_output.read_text(encoding="utf-8"))
            out_delta = next(
                item for item in wrap["udp_deltas"] if item["name"] == "OutDatagrams"
            )
            self.assertEqual(out_delta["classification"], "wrap")
            self.assertEqual(out_delta["delta"], "9")
            self.assertTrue(wrap["eligible"])

            before_out["value"] = "100"
            before_path.write_text(json.dumps(before), encoding="utf-8")
            reset_output = root / "reset.json"
            reset_result = subprocess.run(
                [
                    str(NETWORK_SNAPSHOT),
                    "--mode",
                    "compare",
                    "--before",
                    str(before_path),
                    "--after",
                    str(after_path),
                    "--output",
                    str(reset_output),
                    "--dedicated-host",
                ],
                text=True,
                capture_output=True,
            )
            self.assertEqual(reset_result.returncode, 3)
            reset = json.loads(reset_output.read_text(encoding="utf-8"))
            out_delta = next(
                item for item in reset["udp_deltas"] if item["name"] == "OutDatagrams"
            )
            self.assertEqual(out_delta["classification"], "reset")
            self.assertIsNone(out_delta["delta"])
            self.assertFalse(reset["eligible"])
            self.assertIn("udp_counter_reset", reset["ineligibility_reasons"])

    def test_runtime_sampler_writes_one_schema_valid_main_record_per_udp_socket(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "client-runtime-sockets.jsonl"
            subprocess.run(
                [
                    str(NETWORK_SNAPSHOT),
                    "--mode",
                    "runtime-sockets",
                    "--phase",
                    "main",
                    "--run-id",
                    "12345678-1234-4abc-8def-1234567890ab",
                    "--output",
                    str(output),
                    "--fixture-root",
                    str(FIXTURE / "linux"),
                    "--process",
                    "h3fuzz:4242",
                    "--socket-identity",
                    "9002=shared:pool-0",
                ],
                check=True,
                text=True,
                capture_output=True,
            )

            records = [
                json.loads(line)
                for line in output.read_text(encoding="utf-8").splitlines()
            ]
            self.assertEqual(len(records), 1)
            record = records[0]
            self.assertEqual(record["schema_version"], 1)
            self.assertEqual(record["kind"], "client_runtime_socket")
            self.assertEqual(record["phase"], "main")
            self.assertEqual(record["sampled_at"], "2026-10-11T00:00:00.123456789Z")
            self.assertEqual(record["pid"], 4242)
            self.assertEqual(record["process_start_ticks"], "23456")
            self.assertEqual(record["socket_inode"], "9002")
            self.assertEqual(record["local_address"], "192.0.2.10:51000")
            self.assertEqual(record["raw_so_rcvbuf_bytes"], 7_340_032)
            self.assertEqual(record["raw_so_sndbuf_bytes"], 7_340_032)
            self.assertEqual(record["operator_requested_so_rcvbuf_bytes"], 7_340_032)
            self.assertEqual(record["operator_requested_so_sndbuf_bytes"], 7_340_032)
            self.assertEqual(record["source"], "ss")
            self.assertTrue(record["linux_kernel_doubling_applies"])
            self.assertEqual(record["shared_transport_identity"], "pool-0")
            self.assertIsNone(record["slot"])
            self.assertTrue(record["meets_buffer_target"])
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)

    def test_runtime_sampler_retains_below_target_evidence_and_exits_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "linux"
            shutil.copytree(FIXTURE / "linux", fixture)
            ss_path = fixture / "ss.txt"
            ss_path.write_text(
                ss_path.read_text(encoding="utf-8").replace(
                    "pid=4242,fd=9)) ino:9002 skmem:(r0,rb7340032,t0,tb7340032",
                    "pid=4242,fd=9)) ino:9002 skmem:(r0,rb1048576,t0,tb2097152",
                ),
                encoding="utf-8",
            )
            output = root / "client-runtime-sockets.jsonl"
            result = subprocess.run(
                [
                    str(NETWORK_SNAPSHOT),
                    "--mode",
                    "runtime-sockets",
                    "--phase",
                    "main",
                    "--run-id",
                    "12345678-1234-4abc-8def-1234567890ab",
                    "--output",
                    str(output),
                    "--fixture-root",
                    str(fixture),
                    "--process",
                    "h3fuzz:4242",
                ],
                text=True,
                capture_output=True,
            )

            self.assertEqual(result.returncode, 3)
            record = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(record["raw_so_rcvbuf_bytes"], 1_048_576)
            self.assertEqual(record["raw_so_sndbuf_bytes"], 2_097_152)
            self.assertFalse(record["meets_buffer_target"])


if __name__ == "__main__":
    unittest.main()
