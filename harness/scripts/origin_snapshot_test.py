#!/usr/bin/env python3

import importlib.util
import http.server
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest


SCRIPT_DIR = Path(__file__).resolve().parent
HARNESS_DIR = SCRIPT_DIR.parent
FIXTURES = HARNESS_DIR / "tests" / "fixtures" / "perf002"
MODULE_PATH = SCRIPT_DIR / "origin_snapshot.py"


class OriginSnapshotTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("origin_snapshot", MODULE_PATH)
        if spec is None or spec.loader is None:
            raise RuntimeError(f"cannot load {MODULE_PATH}")
        cls.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.module)

    def test_committed_correlation_fixtures_validate_against_schema(self):
        for name in ("before.json", "after.json", "after-reset.json", "after-restart.json"):
            snapshot = json.loads((FIXTURES / name).read_text(encoding="utf-8"))
            self.module.validate_snapshot(snapshot)

    def test_correlation_selects_exact_run_route_and_protocol(self):
        before = json.loads((FIXTURES / "before.json").read_text(encoding="utf-8"))
        after = json.loads((FIXTURES / "after.json").read_text(encoding="utf-8"))

        result = self.module.correlate_snapshots(
            before,
            after,
            run_id="11111111-2222-4333-8444-555555555555",
            route="bench",
            protocol="h3",
            expected_handler_requests=103,
        )

        self.assertTrue(result["eligible"])
        self.assertEqual(result["ineligibility_reasons"], [])
        self.assertEqual(result["selected"]["handler_requests"], 103)
        self.assertEqual(result["selected"]["by_role"]["initial"], 100)
        self.assertEqual(result["selected"]["by_role"]["optimization_fallback"], 2)
        self.assertEqual(result["selected"]["by_role"]["engine_retry"], 1)
        self.assertEqual(result["selected"]["by_method"], {"GET": 3, "HEAD": 100})
        self.assertEqual(result["unmarked_handler_requests"], 1)
        self.assertEqual(result["handler_reconciliation_error"], 0.0)
        self.assertIsNone(result["selected"]["h3_attempts"])
        self.assertEqual(
            result["selected"]["h3_transport_unavailability_reason"],
            "request_level_quic_hooks_unavailable",
        )

    def test_correlation_rejects_malformed_run_selector(self):
        before = json.loads((FIXTURES / "before.json").read_text(encoding="utf-8"))
        after = json.loads((FIXTURES / "after.json").read_text(encoding="utf-8"))

        with self.assertRaisesRegex(ValueError, "run_id must be a UUID"):
            self.module.correlate_snapshots(
                before,
                after,
                run_id="not-a-run-id",
                route="bench",
                protocol="h3",
            )

    def test_correlation_rejects_counter_reset_and_origin_restart(self):
        before = json.loads((FIXTURES / "before.json").read_text(encoding="utf-8"))
        cases = (
            ("after-reset.json", "origin_counter_epoch_changed"),
            ("after-restart.json", "origin_instance_changed"),
        )
        for fixture, reason in cases:
            with self.subTest(fixture=fixture):
                after = json.loads((FIXTURES / fixture).read_text(encoding="utf-8"))
                result = self.module.correlate_snapshots(
                    before,
                    after,
                    run_id="11111111-2222-4333-8444-555555555555",
                    route="bench",
                    protocol="h3",
                    expected_handler_requests=103,
                )
                self.assertFalse(result["eligible"])
                self.assertIn(reason, result["ineligibility_reasons"])

    def test_correlation_rejects_missing_h3_mismatch_and_apparent_wrap(self):
        before = json.loads((FIXTURES / "before.json").read_text(encoding="utf-8"))
        after = json.loads((FIXTURES / "after.json").read_text(encoding="utf-8"))

        missing = self.module.correlate_snapshots(
            before,
            after,
            run_id="11111111-2222-4333-8444-555555555555",
            route="files",
            protocol="h3",
            expected_handler_requests=103,
        )
        self.assertFalse(missing["eligible"])
        self.assertIn(
            "route_protocol_correlation_missing", missing["ineligibility_reasons"]
        )

        mismatch = self.module.correlate_snapshots(
            before,
            after,
            run_id="11111111-2222-4333-8444-555555555555",
            route="bench",
            protocol="h3",
            expected_handler_requests=104,
        )
        self.assertFalse(mismatch["eligible"])
        self.assertIn(
            "handler_reconciliation_exceeds_0.1_percent",
            mismatch["ineligibility_reasons"],
        )

        before["origin"].pop("counter_epoch")
        after["origin"].pop("counter_epoch")
        after["counters"][0]["handler_requests"] = 1
        wrapped = self.module.correlate_snapshots(
            before,
            after,
            run_id="11111111-2222-4333-8444-555555555555",
            route="bench",
            protocol="h3",
            expected_handler_requests=103,
        )
        self.assertFalse(wrapped["eligible"])
        self.assertIn("origin_counter_reset_or_wrap", wrapped["ineligibility_reasons"])

    def test_correlation_rejects_transport_ledger_availability_change(self):
        before = json.loads((FIXTURES / "before.json").read_text(encoding="utf-8"))
        after = json.loads((FIXTURES / "after.json").read_text(encoding="utf-8"))
        after_row = next(
            row
            for row in after["counters"]
            if row["phase"] == "main"
            and row["route"] == "bench"
            and row["protocol"] == "h3"
            and row["method"] == "GET"
            and row["role"] == "initial"
        )
        before_row = dict(after_row)
        before_row["handler_requests"] = 0
        before["counters"].append(before_row)
        after_row["h3_attempts"] = 20
        after_row["h3_request_rejections"] = 0

        result = self.module.correlate_snapshots(
            before,
            after,
            run_id="11111111-2222-4333-8444-555555555555",
            route="bench",
            protocol="h3",
        )

        self.assertFalse(result["eligible"])
        self.assertIn(
            "h3_transport_ledger_availability_changed",
            result["ineligibility_reasons"],
        )

    def test_capture_writes_valid_snapshot_atomically_and_owner_only(self):
        body = (FIXTURES / "before.json").read_bytes()

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path != "/admin/v1/snapshot":
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *_args):
                return

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "snapshot.json"
            self.module.capture_snapshot(
                f"http://127.0.0.1:{server.server_port}", output
            )
            captured = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(captured["snapshot_id"], "00000000-0000-4000-8000-000000000001")
            self.assertEqual(os.stat(output).st_mode & 0o777, 0o600)
            self.assertEqual(list(Path(directory).glob(".*.tmp")), [])

    def test_provider_implements_exact_before_after_argv_contract(self):
        body = (FIXTURES / "before.json").read_bytes()

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *_args):
                return

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "provider.json"
            environment = os.environ.copy()
            environment["H3FUZZ_ORIGIN_ADMIN_URL"] = (
                f"http://127.0.0.1:{server.server_port}"
            )
            result = subprocess.run(
                [
                    str(SCRIPT_DIR / "origin-snapshot-provider.sh"),
                    "--phase",
                    "before",
                    "--run-id",
                    "11111111-2222-4333-8444-555555555555",
                    "--output",
                    str(output),
                ],
                env=environment,
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                json.loads(output.read_text(encoding="utf-8"))["snapshot_id"],
                "00000000-0000-4000-8000-000000000001",
            )


if __name__ == "__main__":
    unittest.main()
