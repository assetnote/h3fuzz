#!/usr/bin/env python3
"""Collect isolated, paired CPU/heap/mutex/block/QUIC diagnostic evidence."""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
from typing import Any
from urllib.parse import urlsplit
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from perf004_contract import (  # noqa: E402
    ContractError,
    atomic_write_json,
    file_sha256,
    read_json,
    utc_now,
    verify_manifest_hash,
)
from perf503_contract import (  # noqa: E402
    PROFILES,
    artifact_reference,
    comparison_value,
    profile_flags,
    require_absolute,
    require_executable,
    require_sha256,
    seal_manifest,
    verify_diagnostics_manifest,
)


SCRIPT_DIR = Path(__file__).resolve().parent
HARNESS_DIR = SCRIPT_DIR.parent
SCHEMA = HARNESS_DIR / "schemas" / "diagnostics-manifest-v1.schema.json"


class OperationalError(RuntimeError):
    pass


def load_certification_report():
    path = SCRIPT_DIR / "certification-report.py"
    spec = importlib.util.spec_from_file_location("perf503_certification_report", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--target", required=True)
    parser.add_argument("--wordlist", type=Path, required=True)
    parser.add_argument("--wordlist-manifest", type=Path, required=True)
    parser.add_argument("--expect-strategy", choices=("head", "range", "get"), required=True)
    parser.add_argument("--filter-status")
    parser.add_argument("--filter-size")
    parser.add_argument("--fixed-sweep-manifest", type=Path, required=True)
    parser.add_argument("--profiles", required=True)
    parser.add_argument("--server-snapshot-provider", type=Path, required=True)
    parser.add_argument("--server-evidence-provider", type=Path, required=True)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    return parser.parse_args(argv)


def validate_args(args: argparse.Namespace) -> None:
    target = urlsplit(args.target)
    if target.scheme != "https" or target.path != "/bench/FUZZ" or not target.netloc:
        raise ContractError("--target must be an HTTPS /bench/FUZZ URL")
    args.wordlist = require_absolute(args.wordlist, "--wordlist", exists=True)
    args.wordlist_manifest = require_absolute(
        args.wordlist_manifest, "--wordlist-manifest", exists=True
    )
    args.fixed_sweep_manifest = require_absolute(
        args.fixed_sweep_manifest, "--fixed-sweep-manifest", exists=True
    )
    args.server_snapshot_provider = require_executable(
        args.server_snapshot_provider, "--server-snapshot-provider"
    )
    args.server_evidence_provider = require_executable(
        args.server_evidence_provider, "--server-evidence-provider"
    )
    args.artifact_dir = require_absolute(args.artifact_dir, "--artifact-dir")
    args.manifest = require_absolute(args.manifest, "--manifest")
    if args.artifact_dir.exists():
        raise ContractError("--artifact-dir must not already exist")
    if args.manifest.parent != args.artifact_dir or args.manifest.name == "":
        raise ContractError("--manifest must be a direct child of --artifact-dir")
    requested = args.profiles.split(",")
    if requested != list(PROFILES):
        raise ContractError("--profiles must be exactly cpu,heap,mutex,block,quic")
    if not args.filter_status and not args.filter_size:
        raise ContractError("at least one status or size filter is required")


def wordlist_evidence(wordlist: Path, manifest_path: Path) -> dict[str, Any]:
    manifest = read_json(manifest_path)
    if (
        manifest.get("schema_version") != 1
        or manifest.get("manifest_type") != "h3fuzz-frozen-wordlist"
    ):
        raise ContractError(f"{manifest_path}: expected frozen wordlist schema version 1")
    verify_manifest_hash(manifest, str(manifest_path))
    if manifest.get("frozen") is not True:
        raise ContractError(f"{manifest_path}: wordlist is not frozen")
    if Path(str(manifest.get("path", ""))).resolve() != wordlist.resolve():
        raise ContractError(f"{manifest_path}: manifest path does not name --wordlist")
    digest = file_sha256(wordlist)
    with wordlist.open("rb") as stream:
        line_count = sum(1 for _ in stream)
    if manifest.get("sha256") != digest or manifest.get("line_count") != line_count:
        raise ContractError(f"{manifest_path}: frozen wordlist mismatch")
    return {
        "sha256": digest,
        "line_count": line_count,
        "manifest_sha256": require_sha256(
            manifest.get("manifest_sha256"), "wordlist manifest hash"
        ),
    }


def trial_command(
    args: argparse.Namespace,
    *,
    profile: str,
    run_id: str,
    run_dir: Path,
    selection: dict[str, Any],
) -> list[str]:
    runner = Path(
        os.environ.get("H3FUZZ_PERF503_TRIAL_SCRIPT", str(SCRIPT_DIR / "perf503_trial.py"))
    ).resolve()
    if not runner.is_file():
        raise ContractError(f"PERF-503 trial runner does not exist: {runner}")
    command = [
        sys.executable,
        str(runner),
        "--diagnostic-profile",
        profile,
        "--",
        "--target",
        args.target,
        "--wordlist",
        str(args.wordlist),
        "--mode",
        "diagnostic",
        "--expect-strategy",
        args.expect_strategy,
        "--streams",
        str(selection["streams"]),
        "--connections",
        str(selection["connections"]),
        "--max-workers",
        str(selection["adaptive_max_workers"]),
        "--run-id",
        run_id,
        "--server-snapshot-provider",
        str(args.server_snapshot_provider),
        "--server-evidence-provider",
        str(args.server_evidence_provider),
        "--client-before",
        str(run_dir / "client-before.json"),
        "--client-after",
        str(run_dir / "client-after.json"),
        "--client-runtime-sockets",
        str(run_dir / "client-runtime-sockets.jsonl"),
        "--server-counters-before",
        str(run_dir / "server-counters-before.json"),
        "--server-counters-after",
        str(run_dir / "server-counters-after.json"),
        "--server-stable",
        str(run_dir / "server-stable.json"),
        "--server-evidence-before",
        str(run_dir / "server-evidence-before.json"),
        "--server-evidence-after",
        str(run_dir / "server-evidence-after.json"),
        "--artifact-dir",
        str(run_dir),
    ]
    if args.filter_status:
        command.extend(("--filter-status", args.filter_status))
    if args.filter_size:
        command.extend(("--filter-size", args.filter_size))
    return command


def run_trial(
    args: argparse.Namespace,
    *,
    profile: str,
    run_id: str,
    run_dir: Path,
    selection: dict[str, Any],
    log_prefix: Path,
) -> dict[str, Any]:
    command = trial_command(
        args, profile=profile, run_id=run_id, run_dir=run_dir, selection=selection
    )
    atomic_write_json(log_prefix.with_suffix(".argv.json"), {"argv": command})
    with log_prefix.with_suffix(".stdout").open("wb") as stdout, log_prefix.with_suffix(
        ".stderr"
    ).open("wb") as stderr:
        completed = subprocess.run(command, stdout=stdout, stderr=stderr, check=False)
    if completed.returncode not in (0, 3):
        raise OperationalError(
            f"{profile} diagnostic trial failed with exit {completed.returncode}"
        )
    report_path = run_dir / "benchmark-report.json"
    if not report_path.is_file():
        raise OperationalError(f"{profile} trial did not write {report_path}")
    report = read_json(report_path)
    if report.get("run_id") != run_id or report.get("source_eligibility_run") is not False:
        raise OperationalError(f"{profile} trial provenance is invalid")
    return report


def diagnostic_gates(report: dict[str, Any], profile: str) -> None:
    required_true = (
        "telemetry_available",
        "protocol_proven",
        "strategy_proven",
        "optimization_proven",
        "responses_accounted",
        "logical_ledger_proven",
        "transport_proven",
        "drain_complete",
        "error_rate_gates_passed",
    )
    failures = [field for field in required_true if report.get(field) is not True]
    if report.get("aggregation_mode") != "diagnostic":
        failures.append("aggregation_mode")
    if report.get("eligible_for_aggregation") is not False:
        failures.append("eligible_for_aggregation")
    if profile == "none" and report.get("run_valid") is not True:
        failures.append("run_valid")
    span = report.get("main_request_span_seconds")
    if not isinstance(span, (int, float)) or isinstance(span, bool) or span < 30:
        failures.append("minimum_main_span")
    if failures:
        raise OperationalError(
            f"{profile} diagnostic report failed gates: {sorted(set(failures))}"
        )


def ensure_shared_keys(
    report: dict[str, Any], sweep: dict[str, Any], authority: str, profile: str
) -> None:
    for field in ("platform_key", "client_build_key", "workload_key"):
        if comparison_value(report, field, profile) != sweep.get(field):
            raise OperationalError(f"{profile} {field} differs from fixed sweep")
    if report.get("authority") != authority:
        raise OperationalError(f"{profile} authority differs from target")
    if report.get("server_identity") != sweep.get("server_identity"):
        raise OperationalError(f"{profile} server identity differs from fixed sweep")


def write_quic_artifact(run_dir: Path) -> Path:
    telemetry = read_json(run_dir / "run-telemetry.json")
    quic = telemetry.get("quic")
    if not isinstance(quic, dict) or not quic:
        raise OperationalError("QUIC diagnostic telemetry is absent")
    output = run_dir / "quic-diagnostics.json"
    atomic_write_json(output, {"schema_version": 1, "quic": quic})
    return output


def run(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(argv)
        validate_args(args)
        certification_report = load_certification_report()
        sweep, selection = certification_report.verify_fixed_sweep(args.fixed_sweep_manifest)
        wordlist = wordlist_evidence(args.wordlist, args.wordlist_manifest)
        target = urlsplit(args.target)
        if target.netloc != sweep.get("authority"):
            raise ContractError("--target authority differs from fixed sweep")
        if wordlist["sha256"] != sweep.get("wordlist_sha256"):
            raise ContractError("--wordlist differs from fixed sweep")

        args.artifact_dir.mkdir(mode=0o700)
        profiles: dict[str, Any] = {}
        used_run_ids: set[str] = set()
        for name in PROFILES:
            pair_dir = args.artifact_dir / "profiles" / name
            pair_dir.mkdir(parents=True)
            control_run_id = str(uuid.uuid4())
            profile_run_id = str(uuid.uuid4())
            if control_run_id == profile_run_id or control_run_id in used_run_ids or profile_run_id in used_run_ids:
                raise OperationalError("UUID collision between diagnostic trials")
            used_run_ids.update((control_run_id, profile_run_id))
            control_dir = pair_dir / "control"
            profile_dir = pair_dir / "profile"
            control = run_trial(
                args,
                profile="none",
                run_id=control_run_id,
                run_dir=control_dir,
                selection=selection,
                log_prefix=pair_dir / "control-launcher",
            )
            profiled = run_trial(
                args,
                profile=name,
                run_id=profile_run_id,
                run_dir=profile_dir,
                selection=selection,
                log_prefix=pair_dir / "profile-launcher",
            )
            diagnostic_gates(control, "none")
            diagnostic_gates(profiled, name)
            ensure_shared_keys(control, sweep, target.netloc, f"{name}.control")
            ensure_shared_keys(profiled, sweep, target.netloc, f"{name}.profile")
            if profile_flags(control) != [] or profile_flags(profiled) != [name]:
                raise OperationalError(f"{name} diagnostic treatment was not isolated")
            control_rps = control.get("completed_response_rps")
            profile_rps = profiled.get("completed_response_rps")
            if not isinstance(control_rps, (int, float)) or control_rps <= 0:
                raise OperationalError(f"{name} control did not report positive RPS")
            if not isinstance(profile_rps, (int, float)) or profile_rps <= 0:
                raise OperationalError(f"{name} profile did not report positive RPS")
            artifact = (
                write_quic_artifact(profile_dir)
                if name == "quic"
                else profile_dir / f"{name}.pprof"
            )
            if not artifact.is_file() or artifact.stat().st_size == 0:
                raise OperationalError(f"{name} diagnostic artifact is missing or empty")
            control_treatment = comparison_value(control, "treatment_key", f"{name}.control")
            profile_treatment = comparison_value(profiled, "treatment_key", f"{name}.profile")
            if control_treatment == profile_treatment:
                raise OperationalError(f"{name} did not change the treatment key")
            profiles[name] = {
                "paired_control": True,
                "profile_flags": [name],
                "control_profile_flags": [],
                "profile_run_id": profile_run_id,
                "control_run_id": control_run_id,
                "profile_treatment_key": profile_treatment,
                "control_treatment_key": control_treatment,
                "profile_report": artifact_reference(
                    profile_dir / "benchmark-report.json", args.artifact_dir
                ),
                "control_report": artifact_reference(
                    control_dir / "benchmark-report.json", args.artifact_dir
                ),
                "profile_artifact": artifact_reference(artifact, args.artifact_dir),
                "shared_keys_match": True,
                "source_eligibility_runs": False,
                "overhead": {
                    "control_rps": control_rps,
                    "profile_rps": profile_rps,
                    "rps_delta": profile_rps - control_rps,
                    "overhead_percent": 100.0 * (control_rps - profile_rps) / control_rps,
                },
            }

        body = {
            "schema_version": 1,
            "manifest_type": "h3fuzz-diagnostics",
            "created_at": utc_now(),
            "eligible": True,
            "ineligible_reasons": [],
            "platform_key": sweep["platform_key"],
            "client_build_key": sweep["client_build_key"],
            "workload_key": sweep["workload_key"],
            "authority": sweep["authority"],
            "server_identity": sweep["server_identity"],
            "selected_point": {
                "connections": selection["connections"],
                "streams": selection["streams"],
                "treatment_key": selection["fixed_treatment_key"],
                "cohort_key": selection["fixed_cohort_key"],
            },
            "adaptive_max_workers": selection["adaptive_max_workers"],
            "wordlist": wordlist,
            "ceiling_reference": {
                "manifest_sha256": sweep["manifest_sha256"],
                "selected_treatment_key": selection["fixed_treatment_key"],
                "selected_cohort_key": selection["fixed_cohort_key"],
            },
            "profiles": profiles,
        }
        manifest = seal_manifest(body, args.manifest, SCHEMA)
        verify_diagnostics_manifest(args.manifest, SCHEMA)
        print(json.dumps({"manifest": str(args.manifest), "manifest_sha256": manifest["manifest_sha256"]}, sort_keys=True))
        return 0
    except ContractError as exc:
        print(f"collect-diagnostics: {exc}", file=sys.stderr)
        return 2
    except (OSError, OperationalError, ValueError) as exc:
        print(f"collect-diagnostics: {exc}", file=sys.stderr)
        return 4


if __name__ == "__main__":
    raise SystemExit(run())

