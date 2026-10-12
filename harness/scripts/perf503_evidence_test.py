#!/usr/bin/env python3

from __future__ import annotations

import copy
import json
import os
from pathlib import Path
import sys
import tempfile
import textwrap
import unittest
from unittest import mock
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))

from perf004_contract import (
    add_manifest_hash,
    atomic_write_json,
    canonical_hash,
    comparison_keys as make_comparison_keys,
    file_sha256,
    read_json,
)
from perf503_contract import (
    PROFILES,
    artifact_reference,
    seal_manifest,
    verify_diagnostics_manifest,
    verify_files_manifest,
)
import validate_files
import collect_diagnostics


SCRIPT_DIR = Path(__file__).resolve().parent
HARNESS_DIR = SCRIPT_DIR.parent
FILES_SCHEMA = HARNESS_DIR / "schemas" / "files-validation-manifest-v1.schema.json"
DIAGNOSTICS_SCHEMA = HARNESS_DIR / "schemas" / "diagnostics-manifest-v1.schema.json"
S = "server-fixture"
A = "origin.test:8443"
PLATFORM = {"server_host_identity": S, "fixture": "platform"}
CLIENT_BUILD = {"binary_sha256": "b" * 64, "fixture": "build"}
BENCH_WORKLOAD = {"authority": A, "route": "/bench/FUZZ", "fixture": "workload"}
FILES_WORKLOAD = {"authority": A, "route": "/files/FUZZ", "fixture": "workload"}
P = canonical_hash(PLATFORM)
B = canonical_hash(CLIENT_BUILD)
W = canonical_hash(BENCH_WORKLOAD)
FW = canonical_hash(FILES_WORKLOAD)
T = "4" * 64
C = "5" * 64


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    atomic_write_json(path, value)


def rewrite_manifest(path: Path, mutate) -> None:
    value = read_json(path)
    mutate(value)
    value = add_manifest_hash(value)
    atomic_write_json(path, value, refuse_existing=False)


def diagnostic_report(
    flags: list[str], run_id: str, *, run_valid: bool, rps: float = 100000.0
) -> dict:
    keys = make_comparison_keys(
        {
            "platform": copy.deepcopy(PLATFORM),
            "client_build": copy.deepcopy(CLIENT_BUILD),
            "workload": copy.deepcopy(BENCH_WORKLOAD),
            "treatment": {"profile_flags": flags},
        }
    )
    return {
        "report_schema_version": 2,
        "aggregation_mode": "diagnostic",
        "target": "https://origin.test:8443/bench/FUZZ",
        "run_id": run_id,
        "source_eligibility_run": False,
        "authority": A,
        "server_identity": S,
        "completed_response_rps": rps,
        "main_request_span_seconds": 30.0,
        "telemetry_available": True,
        "protocol_proven": True,
        "strategy_proven": True,
        "optimization_proven": True,
        "responses_accounted": True,
        "logical_ledger_proven": True,
        "transport_proven": True,
        "drain_complete": True,
        "error_rate_gates_passed": True,
        "eligible_for_aggregation": False,
        "run_valid": run_valid,
        "comparison_keys": keys,
    }


def files_keys(*, exact: bool) -> dict:
    workload = (
        {"authority": A, "route": "/files/FUZZ", "purpose": "exact"}
        if exact
        else copy.deepcopy(FILES_WORKLOAD)
    )
    return make_comparison_keys(
        {
            "platform": copy.deepcopy(PLATFORM),
            "client_build": copy.deepcopy(CLIENT_BUILD),
            "workload": workload,
            "treatment": {"kind": "fixture", "exact": exact},
        }
    )


def make_sweep(root: Path, wordlist_sha256: str) -> Path:
    root.mkdir()
    keys = make_comparison_keys(
        {
            "platform": copy.deepcopy(PLATFORM),
            "client_build": copy.deepcopy(CLIENT_BUILD),
            "workload": copy.deepcopy(BENCH_WORKLOAD),
            "treatment": {
                "mode": "sweep",
                "connection_policy": {"kind": "fixed", "value": 8},
                "streams": 256,
                "worker_policy": {"kind": "bounded", "threads": None, "maximum": 65536},
                "profile_flags": [],
            },
        }
    )
    references = []
    members = []
    for index in range(3):
        path = root / "points" / f"run-{index + 1}.json"
        write_json(
            path,
            {
                "aggregation_mode": "sweep",
                "run_gates_passed": True,
                "eligible_for_aggregation": True,
                "completed_response_rps": 105000.0,
                "comparison_keys": keys,
                "expected_response_strategy": "HEAD",
            },
        )
        reference = artifact_reference(path, root)
        references.append(reference)
        members.append(reference)
    body = {
        "schema_version": 1,
        "manifest_type": "h3fuzz-fixed-sweep",
        "eligible": True,
        "ineligible_reasons": [],
        "wordlist_sha256": wordlist_sha256,
        "platform_key": keys["platform_key"],
        "client_build_key": keys["client_build_key"],
        "workload_key": keys["workload_key"],
        "authority": A,
        "server_identity": S,
        "decision_policy": {
            "noise_band_percent": 2.0,
            "minimum_improvement_percent": 3.0,
        },
        "points": [
            {
                "connections": 8,
                "streams": 256,
                "treatment_key": keys["treatment_key"],
                "cohort_key": keys["cohort_key"],
                "clean": True,
                "median_completed_response_rps": 105000.0,
                "reports": references,
            }
        ],
        "selected_point": {
            "connections": 8,
            "streams": 256,
            "treatment_key": keys["treatment_key"],
            "cohort_key": keys["cohort_key"],
        },
        "adaptive_max_connections": 8,
        "adaptive_max_workers": 65536,
        "members": members,
    }
    manifest = root / "fixed-sweep-manifest.json"
    atomic_write_json(manifest, add_manifest_hash(body))
    return manifest


class FilesManifestFixtureTest(unittest.TestCase):
    def test_exact_telemetry_requires_protocol_ledger_transport_and_drain(self):
        telemetry = {
            "schema_version": 2,
            "protocol": {
                "selected": "http3",
                "http3_selected": True,
                "http3_final_connections": 8,
            },
            "responses": {
                "scheduled_logical_inputs": 14,
                "successful_logical_responses": 14,
                "terminal_logical_outcomes": 14,
                "terminal_logical_errors": 0,
            },
            "logical_ledger": {
                "expected_expanded_inputs": 14,
                "assigned_ids": 14,
                "scheduled_transitions": 14,
                "success_terminal_states": 14,
                "error_terminal_states": 0,
                "pending_scheduled_states": 0,
                "id_gaps": 0,
                "duplicate_schedule_transitions": 0,
                "duplicate_terminal_transitions": 0,
                "invalid_transitions": 0,
                "out_of_range_ids": 0,
            },
            "transport": {
                "application_attempts": 16,
                "completed_attempts": 16,
                "surfaced_attempt_errors": 0,
                "application_retries": 0,
                "in_flight": 0,
            },
            "drain": {
                "complete": True,
                "workers_drained": True,
                "runner_closed": True,
                "close_error": None,
                "limiter_in_use": 0,
                "limiter_waiters": 0,
            },
            "eligibility": {"eligible": True},
            "request_strategy": {
                "selected_optimization": "HEAD",
                "automatic_optimization_enabled": True,
            },
        }
        self.assertEqual(
            validate_files.exact_telemetry(
                telemetry, expected_inputs=14, get_control=False
            ),
            (16, "HEAD"),
        )
        broken = copy.deepcopy(telemetry)
        broken["logical_ledger"]["duplicate_terminal_transitions"] = 1
        with self.assertRaises(RuntimeError):
            validate_files.exact_telemetry(
                broken, expected_inputs=14, get_control=False
            )

    def fixture(self, root: Path) -> Path:
        exact_path = root / "exact-hit" / "exact-hit-report.json"
        long_path = root / "long-corpus" / "benchmark-report.json"
        comparison_path = root / "exact-hit" / "comparison.json"
        corpus_path = root / "inputs" / "seeded-throughput.txt"
        corpus_path.parent.mkdir(parents=True)
        corpus_path.write_text("admin\nmissing-a\n", encoding="utf-8")
        write_json(comparison_path, {"finding_sets_equal": True})
        exact = {
            "authority": A,
            "server_identity": S,
            "comparison_keys": files_keys(exact=True),
        }
        long = {
            "target": "https://origin.test:8443/files/FUZZ",
            "authority": A,
            "server_identity": S,
            "comparison_keys": files_keys(exact=False),
        }
        write_json(exact_path, exact)
        write_json(long_path, long)
        exact_ref = artifact_reference(exact_path, root)
        long_ref = artifact_reference(long_path, root)
        body = {
            "schema_version": 1,
            "manifest_type": "h3fuzz-files-validation",
            "created_at": "2026-01-01T00:00:00Z",
            "eligible": True,
            "ineligible_reasons": [],
            "platform_key": P,
            "client_build_key": B,
            "workload_key": FW,
            "authority": A,
            "server_identity": S,
            "route": "/files/FUZZ",
            "selected_point": {
                "connections": 8,
                "streams": 256,
                "max_workers": 65536,
                "treatment_key": T,
                "cohort_key": C,
            },
            "ceiling_reference": {
                "manifest_sha256": "7" * 64,
                "workload_key": "8" * 64,
                "authority": A,
                "route": "/bench/FUZZ",
            },
            "corpus": {
                "generator": "h3fuzz-files-seeded-corpus-v1",
                "seed": 7,
                "target_seconds": 30,
                "headroom_percent": 20,
                "target_inputs": 2,
                "line_count": 2,
                "sha256": file_sha256(corpus_path),
                "known_hit_count": 1,
                "unique_miss_count": 1,
                "base_unique_count": 1,
                "base_duplicate_count": 1,
                "known_hits_added": 0,
                "known_hit_duplicates_removed": 1,
                "path": "inputs/seeded-throughput.txt",
            },
            "duplicate_accounting": "Every exact entry appears once; duplicates are removed.",
            "correctness": {
                "passed": True,
                "expected_hits": 1,
                "actual_hits": 1,
                "false_positives": 0,
                "missing_hits": [],
                "unexpected_hits": [],
                "filter_matches_get_control": True,
                "metadata_compatible": True,
                "get_strategy": "GET",
                "optimized_strategy": "HEAD",
                "get_application_attempts": 2,
                "optimized_application_attempts": 2,
                "comparison": artifact_reference(comparison_path, root),
            },
            "throughput": {
                "route": "/files/FUZZ",
                "bench_route": "/bench/FUZZ",
                "distinct_from_bench": True,
                "completed_response_rps": 100001.0,
                "main_request_span_seconds": 30.0,
                "successful_logical_responses": 3000030,
                "server_handler_requests": 3000030,
                "expected_hits": 1,
                "actual_hits": 1,
                "false_positives": 0,
                "report": long_ref,
            },
            "runs": {"exact_hit": exact_ref, "long_corpus": long_ref},
        }
        manifest = root / "manifest.json"
        seal_manifest(body, manifest, FILES_SCHEMA)
        return manifest

    def test_fixture_validates_and_corpus_is_deterministic(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = self.fixture(root)
            self.assertTrue(verify_files_manifest(manifest, FILES_SCHEMA)["eligible"])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            base = root / "base.txt"
            base.write_text("admin\nadmin\nalpha\n", encoding="utf-8")
            first = root / "first.txt"
            second = root / "second.txt"
            one = validate_files.generate_corpus(
                base, ["admin", "login"], seed=99, target_inputs=50, output=first
            )
            two = validate_files.generate_corpus(
                base, ["admin", "login"], seed=99, target_inputs=50, output=second
            )
            self.assertEqual(first.read_bytes(), second.read_bytes())
            self.assertEqual(one["sha256"], two["sha256"])
            words = first.read_text(encoding="utf-8").splitlines()
            self.assertEqual(len(words), len(set(words)))
            self.assertEqual(words.count("admin"), 1)
            self.assertEqual(words.count("login"), 1)
            self.assertEqual(one["base_duplicate_count"], 1)

    def test_tampered_missing_and_extra_members_fail_closed(self):
        mutations = ("tampered", "missing", "extra")
        for mutation in mutations:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                manifest = self.fixture(root)
                exact = root / "exact-hit" / "exact-hit-report.json"
                if mutation == "tampered":
                    exact.write_text("{}\n", encoding="utf-8")
                elif mutation == "missing":
                    exact.unlink()
                else:
                    (root / "unrecorded.txt").write_text("extra", encoding="utf-8")
                with self.assertRaises(ValueError):
                    verify_files_manifest(manifest, FILES_SCHEMA)

    def test_schema_and_layer_mismatch_fail_closed(self):
        for mutation in ("extra_property", "platform_mismatch", "corpus_accounting"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                manifest = self.fixture(root)
                if mutation == "extra_property":
                    rewrite_manifest(manifest, lambda value: value.__setitem__("unknown", True))
                elif mutation == "platform_mismatch":
                    rewrite_manifest(manifest, lambda value: value.__setitem__("platform_key", "a" * 64))
                else:
                    rewrite_manifest(
                        manifest,
                        lambda value: value["corpus"].__setitem__("unique_miss_count", 0),
                    )
                with self.assertRaises(ValueError):
                    verify_files_manifest(manifest, FILES_SCHEMA)


class DiagnosticsManifestFixtureTest(unittest.TestCase):
    def test_collector_runs_ten_distinct_fake_trials_and_seals_tree(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            wordlist = root / "words.txt"
            wordlist.write_text("one\ntwo\n", encoding="utf-8")
            wordlist_body = {
                "schema_version": 1,
                "manifest_type": "h3fuzz-frozen-wordlist",
                "frozen": True,
                "path": str(wordlist),
                "sha256": file_sha256(wordlist),
                "line_count": 2,
            }
            wordlist_manifest = root / "words.manifest.json"
            atomic_write_json(wordlist_manifest, add_manifest_hash(wordlist_body))
            sweep = make_sweep(root / "sweep", file_sha256(wordlist))
            provider = root / "provider"
            provider.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
            provider.chmod(0o755)

            key_map = {}
            for name in ("none", *PROFILES):
                key_map[name] = make_comparison_keys(
                    {
                        "platform": copy.deepcopy(PLATFORM),
                        "client_build": copy.deepcopy(CLIENT_BUILD),
                        "workload": copy.deepcopy(BENCH_WORKLOAD),
                        "treatment": {
                            "mode": "diagnostic",
                            "profile_flags": [] if name == "none" else [name],
                        },
                    }
                )
            fake = root / "fake-trial.py"
            fake.write_text(
                textwrap.dedent(
                    """\
                    #!/usr/bin/env python3
                    import json, os
                    from pathlib import Path
                    import sys

                    argv = sys.argv[1:]
                    profile = argv[argv.index('--diagnostic-profile') + 1]
                    delegated = argv[argv.index('--') + 1:]
                    def value(name):
                        return delegated[delegated.index(name) + 1]
                    run_dir = Path(value('--artifact-dir'))
                    run_dir.mkdir(parents=True)
                    run_id = value('--run-id')
                    keys = json.loads(os.environ['H3FUZZ_TEST_DIAGNOSTIC_KEYS'])[profile]
                    report = {
                        'report_schema_version': 2,
                        'aggregation_mode': 'diagnostic',
                        'target': value('--target'),
                        'run_id': run_id,
                        'source_eligibility_run': False,
                        'authority': 'origin.test:8443',
                        'server_identity': 'server-fixture',
                        'completed_response_rps': 100000.0 if profile == 'none' else 90000.0,
                        'main_request_span_seconds': 30.0,
                        'telemetry_available': True,
                        'protocol_proven': True,
                        'strategy_proven': True,
                        'optimization_proven': True,
                        'responses_accounted': True,
                        'logical_ledger_proven': True,
                        'transport_proven': True,
                        'drain_complete': True,
                        'error_rate_gates_passed': True,
                        'eligible_for_aggregation': False,
                        'run_valid': profile == 'none',
                        'comparison_keys': keys,
                    }
                    (run_dir / 'benchmark-report.json').write_text(
                        json.dumps(report, sort_keys=True) + '\\n', encoding='utf-8'
                    )
                    if profile == 'quic':
                        (run_dir / 'run-telemetry.json').write_text(
                            json.dumps({'quic': {'aggregate_enabled': True}}) + '\\n',
                            encoding='utf-8',
                        )
                    elif profile != 'none':
                        (run_dir / (profile + '.pprof')).write_bytes(b'profile-' + profile.encode())
                    raise SystemExit(3)
                    """
                ),
                encoding="utf-8",
            )
            artifact_dir = root / "diagnostics"
            manifest = artifact_dir / "manifest.json"
            environment = {
                "H3FUZZ_PERF503_TRIAL_SCRIPT": str(fake),
                "H3FUZZ_TEST_DIAGNOSTIC_KEYS": json.dumps(key_map, sort_keys=True),
            }
            with mock.patch.dict(os.environ, environment, clear=False):
                status = collect_diagnostics.run(
                    [
                        "--target",
                        "https://origin.test:8443/bench/FUZZ",
                        "--wordlist",
                        str(wordlist),
                        "--wordlist-manifest",
                        str(wordlist_manifest),
                        "--expect-strategy",
                        "head",
                        "--filter-status",
                        "204",
                        "--fixed-sweep-manifest",
                        str(sweep),
                        "--profiles",
                        "cpu,heap,mutex,block,quic",
                        "--server-snapshot-provider",
                        str(provider),
                        "--server-evidence-provider",
                        str(provider),
                        "--artifact-dir",
                        str(artifact_dir),
                        "--manifest",
                        str(manifest),
                    ]
                )
            self.assertEqual(status, 0)
            value = verify_diagnostics_manifest(manifest, DIAGNOSTICS_SCHEMA)
            run_ids = {
                pair[field]
                for pair in value["profiles"].values()
                for field in ("profile_run_id", "control_run_id")
            }
            self.assertEqual(len(run_ids), 10)

    def fixture(self, root: Path) -> Path:
        profiles = {}
        for index, name in enumerate(PROFILES):
            control_id = str(uuid.uuid5(uuid.NAMESPACE_DNS, f"{name}-control"))
            profile_id = str(uuid.uuid5(uuid.NAMESPACE_DNS, f"{name}-profile"))
            control_path = root / "profiles" / name / "control" / "benchmark-report.json"
            profile_path = root / "profiles" / name / "profile" / "benchmark-report.json"
            artifact_path = root / "profiles" / name / "profile" / (
                "quic-diagnostics.json" if name == "quic" else f"{name}.pprof"
            )
            control_report = diagnostic_report([], control_id, run_valid=True)
            profile_report = diagnostic_report(
                [name], profile_id, run_valid=False, rps=90000.0
            )
            control_treatment = control_report["comparison_keys"]["treatment_key"]
            profile_treatment = profile_report["comparison_keys"]["treatment_key"]
            write_json(control_path, control_report)
            write_json(profile_path, profile_report)
            artifact_path.write_bytes((name + "-profile").encode("ascii"))
            profiles[name] = {
                "paired_control": True,
                "profile_flags": [name],
                "control_profile_flags": [],
                "profile_run_id": profile_id,
                "control_run_id": control_id,
                "profile_treatment_key": profile_treatment,
                "control_treatment_key": control_treatment,
                "profile_report": artifact_reference(profile_path, root),
                "control_report": artifact_reference(control_path, root),
                "profile_artifact": artifact_reference(artifact_path, root),
                "shared_keys_match": True,
                "source_eligibility_runs": False,
                "overhead": {
                    "control_rps": 100000.0,
                    "profile_rps": 90000.0,
                    "rps_delta": -10000.0,
                    "overhead_percent": 10.0,
                },
            }
        body = {
            "schema_version": 1,
            "manifest_type": "h3fuzz-diagnostics",
            "created_at": "2026-01-01T00:00:00Z",
            "eligible": True,
            "ineligible_reasons": [],
            "platform_key": P,
            "client_build_key": B,
            "workload_key": W,
            "authority": A,
            "server_identity": S,
            "selected_point": {
                "connections": 8,
                "streams": 256,
                "treatment_key": T,
                "cohort_key": C,
            },
            "adaptive_max_workers": 65536,
            "wordlist": {
                "sha256": "6" * 64,
                "line_count": 3600000,
                "manifest_sha256": "7" * 64,
            },
            "ceiling_reference": {
                "manifest_sha256": "8" * 64,
                "selected_treatment_key": T,
                "selected_cohort_key": C,
            },
            "profiles": profiles,
        }
        manifest = root / "manifest.json"
        seal_manifest(body, manifest, DIAGNOSTICS_SCHEMA)
        return manifest

    def test_fixture_validates(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest = self.fixture(Path(directory))
            verified = verify_diagnostics_manifest(manifest, DIAGNOSTICS_SCHEMA)
            self.assertEqual(set(verified["profiles"]), set(PROFILES))

    def test_tamper_missing_extra_and_reuse_fail_closed(self):
        for mutation in ("tampered", "missing", "extra", "reused_control", "wrong_flags"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                manifest = self.fixture(root)
                cpu_report = root / "profiles" / "cpu" / "profile" / "benchmark-report.json"
                if mutation == "tampered":
                    cpu_report.write_text("{}\n", encoding="utf-8")
                elif mutation == "missing":
                    cpu_report.unlink()
                elif mutation == "extra":
                    (root / "extra.log").write_text("untracked", encoding="utf-8")
                elif mutation == "reused_control":
                    def reuse(value):
                        value["profiles"]["heap"]["control_run_id"] = value["profiles"]["cpu"]["control_run_id"]
                    rewrite_manifest(manifest, reuse)
                else:
                    rewrite_manifest(
                        manifest,
                        lambda value: value["profiles"]["cpu"].__setitem__("profile_flags", ["heap"]),
                    )
                with self.assertRaises(ValueError):
                    verify_diagnostics_manifest(manifest, DIAGNOSTICS_SCHEMA)

    def test_mismatched_layer_and_unknown_property_fail_closed(self):
        for mutation in ("workload", "unknown", "overhead"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                manifest = self.fixture(root)
                if mutation == "workload":
                    rewrite_manifest(manifest, lambda value: value.__setitem__("workload_key", "a" * 64))
                elif mutation == "unknown":
                    rewrite_manifest(manifest, lambda value: value.__setitem__("unknown", {}))
                else:
                    rewrite_manifest(
                        manifest,
                        lambda value: value["profiles"]["cpu"]["overhead"].__setitem__(
                            "overhead_percent", 9.0
                        ),
                    )
                with self.assertRaises(ValueError):
                    verify_diagnostics_manifest(manifest, DIAGNOSTICS_SCHEMA)


if __name__ == "__main__":
    unittest.main()

