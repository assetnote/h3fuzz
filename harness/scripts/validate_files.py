#!/usr/bin/env python3
"""Validate real /files/FUZZ semantics and retain a separate throughput proof."""

from __future__ import annotations

import argparse
import copy
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from typing import Any
from urllib.parse import urlsplit
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from perf004_contract import (  # noqa: E402
    ContractError,
    atomic_write_json,
    canonical_hash,
    comparison_keys,
    file_sha256,
    read_json,
    utc_now,
)
from perf503_contract import (  # noqa: E402
    artifact_reference,
    comparison_value,
    require_absolute,
    require_executable,
    seal_manifest,
    verify_files_manifest,
)


SCRIPT_DIR = Path(__file__).resolve().parent
HARNESS_DIR = SCRIPT_DIR.parent
REPO_ROOT = HARNESS_DIR.parent
SCHEMA = HARNESS_DIR / "schemas" / "files-validation-manifest-v1.schema.json"
EXPECTED_HITS = HARNESS_DIR / "wordlists" / "expected-hits.txt"
CANONICAL_CORRECTNESS = HARNESS_DIR / "wordlists" / "correctness.txt"
GENERATOR = "h3fuzz-files-seeded-corpus-v1"


class OperationalError(RuntimeError):
    pass


def positive(value: str) -> int:
    try:
        result = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("expected a positive integer") from exc
    if result <= 0:
        raise argparse.ArgumentTypeError("expected a positive integer")
    return result


def unsigned(value: str) -> int:
    try:
        result = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("expected an unsigned integer") from exc
    if result < 0:
        raise argparse.ArgumentTypeError("expected an unsigned integer")
    return result


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--target", required=True)
    parser.add_argument("--correctness-wordlist", type=Path, required=True)
    parser.add_argument("--throughput-base-wordlist", type=Path, required=True)
    parser.add_argument("--seed", type=unsigned, required=True)
    parser.add_argument("--target-seconds", type=positive, required=True)
    parser.add_argument("--fixed-sweep-manifest", type=Path, required=True)
    parser.add_argument("--server-snapshot-provider", type=Path, required=True)
    parser.add_argument("--server-evidence-provider", type=Path, required=True)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    return parser.parse_args(argv)


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def validate_args(args: argparse.Namespace) -> None:
    target = urlsplit(args.target)
    if target.scheme != "https" or target.path != "/files/FUZZ" or not target.netloc:
        raise ContractError("--target must be an HTTPS /files/FUZZ URL")
    args.correctness_wordlist = require_absolute(
        args.correctness_wordlist, "--correctness-wordlist", exists=True
    )
    args.throughput_base_wordlist = require_absolute(
        args.throughput_base_wordlist, "--throughput-base-wordlist", exists=True
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
    if args.target_seconds < 30:
        raise ContractError("--target-seconds must be at least 30")
    if args.artifact_dir.exists():
        raise ContractError("--artifact-dir must not already exist")
    if args.manifest.parent != args.artifact_dir:
        raise ContractError("--manifest must be a direct child of --artifact-dir")
    if file_sha256(args.correctness_wordlist) != file_sha256(CANONICAL_CORRECTNESS):
        raise ContractError(
            "--correctness-wordlist must contain the canonical harness correctness corpus"
        )


def read_words(path: Path) -> list[str]:
    result: list[str] = []
    for raw in path.read_text(encoding="utf-8").splitlines():
        value = raw.strip()
        if value and not value.startswith("#"):
            result.append(value)
    return result


def generate_corpus(
    base_path: Path,
    expected_hits: list[str],
    *,
    seed: int,
    target_inputs: int,
    output: Path,
) -> dict[str, Any]:
    base = read_words(base_path)
    counts: dict[str, int] = {}
    unique: list[str] = []
    seen: set[str] = set()
    for value in base:
        counts[value] = counts.get(value, 0) + 1
        if value not in seen:
            unique.append(value)
            seen.add(value)
    base_duplicates = len(base) - len(unique)
    known_hits_added = 0
    for hit in expected_hits:
        if hit not in seen:
            unique.append(hit)
            seen.add(hit)
            known_hits_added += 1
    known_hit_duplicates_removed = sum(max(0, counts.get(hit, 0) - 1) for hit in expected_hits)
    target_inputs = max(target_inputs, len(unique))
    miss_prefix = "__h3fuzz_perf503_miss__"
    categories = ("admin", "api", "assets", "backup", "config", "private", "static")
    extensions = ("txt", "json", "php", "bak", "html", "dat")
    counter = 0
    while len(unique) < target_inputs:
        digest = hashlib.sha256(f"{seed}:{counter}".encode("ascii")).hexdigest()[:24]
        category = categories[(seed + counter) % len(categories)]
        extension = extensions[(seed * 3 + counter) % len(extensions)]
        candidate = f"{miss_prefix}/{category}/{digest}-{counter}.{extension}"
        counter += 1
        if candidate in seen:
            continue
        unique.append(candidate)
        seen.add(candidate)

    output.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{output.name}.", dir=output.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as stream:
            for value in unique:
                stream.write(value)
                stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, output)
    finally:
        if temporary.exists():
            temporary.unlink()
    expected_set = set(expected_hits)
    if not expected_set.issubset(seen):
        raise ContractError("generated corpus lost one or more known hit names")
    return {
        "generator": GENERATOR,
        "seed": seed,
        "target_inputs": target_inputs,
        "line_count": len(unique),
        "sha256": file_sha256(output),
        "known_hit_count": len(expected_set),
        "unique_miss_count": sum(1 for value in unique if value not in expected_set),
        "base_unique_count": len(set(base)),
        "base_duplicate_count": base_duplicates,
        "known_hits_added": known_hits_added,
        "known_hit_duplicates_removed": known_hit_duplicates_removed,
    }


def selected_point(sweep: dict[str, Any], selection: dict[str, Any]) -> dict[str, Any]:
    matches = [
        point
        for point in sweep.get("points", [])
        if point.get("connections") == selection["connections"]
        and point.get("streams") == selection["streams"]
        and point.get("treatment_key") == selection["fixed_treatment_key"]
        and point.get("cohort_key") == selection["fixed_cohort_key"]
    ]
    if len(matches) != 1:
        raise ContractError("fixed sweep does not have exactly one selected executed point")
    return matches[0]


def selected_report(
    sweep_path: Path, sweep: dict[str, Any], point: dict[str, Any]
) -> dict[str, Any]:
    references = point.get("reports")
    if not isinstance(references, list) or not references:
        raise ContractError("selected fixed point has no reports")
    reference = references[0]
    path = (sweep_path.parent / reference["path"]).resolve()
    return read_json(path)


def infer_strategy(report: dict[str, Any]) -> str:
    strategy = report.get("expected_response_strategy") or report.get("selected_response_strategy")
    if not isinstance(strategy, str) or strategy.lower() not in {"head", "range", "get"}:
        raise ContractError("selected fixed report has no supported response strategy")
    return strategy.lower()


def trial_arguments(
    args: argparse.Namespace,
    *,
    wordlist: Path,
    run_id: str,
    run_dir: Path,
    selection: dict[str, Any],
    strategy: str,
) -> list[str]:
    return [
        "--target",
        args.target,
        "--wordlist",
        str(wordlist),
        "--mode",
        "selected-fixed",
        "--expect-strategy",
        strategy,
        "--filter-status",
        "404",
        "--streams",
        str(selection["streams"]),
        "--connections",
        str(selection["connections"]),
        "--max-workers",
        str(selection["adaptive_max_workers"]),
        "--target-rps",
        "100000",
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


def run_long_trial(
    args: argparse.Namespace,
    *,
    wordlist: Path,
    selection: dict[str, Any],
    strategy: str,
) -> tuple[dict[str, Any], Path]:
    run_dir = args.artifact_dir / "long-corpus"
    run_id = str(uuid.uuid4())
    runner = Path(
        os.environ.get("H3FUZZ_PERF503_TRIAL_SCRIPT", str(SCRIPT_DIR / "perf503_trial.py"))
    ).resolve()
    if not runner.is_file():
        raise ContractError(f"PERF-503 trial runner does not exist: {runner}")
    delegated = trial_arguments(
        args,
        wordlist=wordlist,
        run_id=run_id,
        run_dir=run_dir,
        selection=selection,
        strategy=strategy,
    )
    command = [
        sys.executable,
        str(runner),
        "--diagnostic-profile",
        "none",
        "--json-output",
        "--",
        *delegated,
    ]
    atomic_write_json(args.artifact_dir / "long-corpus.argv.json", {"argv": command})
    with (args.artifact_dir / "long-corpus.launcher.stdout").open("wb") as stdout, (
        args.artifact_dir / "long-corpus.launcher.stderr"
    ).open("wb") as stderr:
        completed = subprocess.run(command, stdout=stdout, stderr=stderr, check=False)
    if completed.returncode != 0:
        raise OperationalError(f"long-corpus benchmark failed with exit {completed.returncode}")
    report_path = run_dir / "benchmark-report.json"
    if not report_path.is_file():
        raise OperationalError("long-corpus benchmark report is missing")
    report = read_json(report_path)
    if report.get("run_id") != run_id or report.get("source_eligibility_run") is not False:
        raise OperationalError("long-corpus run provenance is invalid")
    return report, report_path


def resolve_h3fuzz() -> Path:
    configured = os.environ.get("H3FUZZ_BIN")
    candidate = Path(configured).resolve() if configured else (REPO_ROOT / "h3fuzz").resolve()
    return require_executable(candidate, "H3FUZZ_BIN")


def provider(provider_path: Path, phase: str, run_id: str, output: Path) -> None:
    completed = subprocess.run(
        [str(provider_path), "--phase", phase, "--run-id", run_id, "--output", str(output)],
        check=False,
    )
    if completed.returncode != 0:
        raise OperationalError(
            f"provider {provider_path} phase {phase} failed with exit {completed.returncode}"
        )
    if not output.is_file():
        raise OperationalError(f"provider did not write {output}")


def exact_command(
    binary: Path,
    args: argparse.Namespace,
    wordlist: Path,
    selection: dict[str, Any],
    run_id: str,
    run_dir: Path,
    *,
    get_control: bool,
) -> list[str]:
    command = [
        str(binary),
        "-u",
        args.target,
        "-w",
        str(wordlist),
        "-k",
        "-http3",
        "-http3-connections",
        str(selection["connections"]),
        "-http3-streams",
        str(selection["streams"]),
        "-http3-max-workers",
        str(selection["adaptive_max_workers"]),
        "-harness-run-id",
        run_id,
        "-run-telemetry",
        str(run_dir / "run-telemetry.json"),
        "-mc",
        "all",
        "-fc",
        "404",
        "-noninteractive",
        "-s",
        "-json",
    ]
    if get_control:
        command.extend(("-no-response-optimization", "-X", "GET"))
    return command


def exact_telemetry(
    telemetry: dict[str, Any], *, expected_inputs: int, get_control: bool
) -> tuple[int, str]:
    protocol = telemetry.get("protocol", {})
    responses = telemetry.get("responses", {})
    ledger = telemetry.get("logical_ledger", {})
    transport = telemetry.get("transport", {})
    drain = telemetry.get("drain", {})
    eligibility = telemetry.get("eligibility", {})
    strategy_data = telemetry.get("request_strategy", {})
    attempts = transport.get("application_attempts")
    completed_attempts = transport.get("completed_attempts")
    errors = transport.get("surfaced_attempt_errors")
    if telemetry.get("schema_version") != 2:
        raise OperationalError("exact-hit telemetry schema v2 is required")
    if not (
        protocol.get("selected") == "http3"
        and protocol.get("http3_selected") is True
        and isinstance(protocol.get("http3_final_connections"), int)
        and protocol["http3_final_connections"] > 0
    ):
        raise OperationalError("exact-hit run did not prove HTTP/3")
    if not (
        responses.get("scheduled_logical_inputs") == expected_inputs
        and responses.get("successful_logical_responses") == expected_inputs
        and responses.get("terminal_logical_outcomes") == expected_inputs
        and responses.get("terminal_logical_errors") == 0
    ):
        raise OperationalError("exact-hit logical response accounting failed")
    if not (
        ledger.get("expected_expanded_inputs") == expected_inputs
        and ledger.get("assigned_ids") == expected_inputs
        and ledger.get("scheduled_transitions") == expected_inputs
        and ledger.get("success_terminal_states") == expected_inputs
        and ledger.get("error_terminal_states") == 0
        and all(
            ledger.get(field) == 0
            for field in (
                "pending_scheduled_states",
                "id_gaps",
                "duplicate_schedule_transitions",
                "duplicate_terminal_transitions",
                "invalid_transitions",
                "out_of_range_ids",
            )
        )
    ):
        raise OperationalError("exact-hit logical ledger failed")
    if not (
        isinstance(attempts, int)
        and attempts >= expected_inputs
        and completed_attempts == attempts
        and errors == 0
        and transport.get("application_retries") == 0
        and transport.get("in_flight") == 0
    ):
        raise OperationalError("exact-hit transport accounting failed")
    if not (
        drain.get("complete") is True
        and drain.get("workers_drained") is True
        and drain.get("runner_closed") is True
        and not drain.get("close_error")
        and drain.get("limiter_in_use") == 0
        and drain.get("limiter_waiters") == 0
    ):
        raise OperationalError("exact-hit run did not fully drain")
    if eligibility.get("eligible") is not True:
        raise OperationalError("exact-hit run used an ineligible diagnostic treatment")
    strategy = str(strategy_data.get("selected_optimization", "")).upper()
    if get_control:
        if strategy_data.get("automatic_optimization_enabled") is not False:
            raise OperationalError("GET control unexpectedly enabled response optimization")
        strategy = "GET"
    elif (
        strategy not in {"HEAD", "RANGE", "GET"}
        or strategy_data.get("automatic_optimization_enabled") is not True
    ):
        raise OperationalError(f"exact-hit automatic strategy is invalid: {strategy!r}")
    return attempts, strategy


def run_exact_side(
    binary: Path,
    args: argparse.Namespace,
    *,
    wordlist: Path,
    selection: dict[str, Any],
    name: str,
    get_control: bool,
) -> dict[str, Any]:
    run_id = str(uuid.uuid4())
    run_dir = args.artifact_dir / "exact-hit" / name
    run_dir.mkdir(parents=True)
    command = exact_command(
        binary,
        args,
        wordlist,
        selection,
        run_id,
        run_dir,
        get_control=get_control,
    )
    atomic_write_json(run_dir / "argv.json", {"argv": command})
    atomic_write_json(
        run_dir / "environment-overrides.json",
        {key: value for key, value in os.environ.items() if key.startswith("H3FUZZ_")},
    )
    counters_before = run_dir / "server-counters-before.json"
    counters_after = run_dir / "server-counters-after.json"
    server_stable = run_dir / "server-stable.json"
    provider(args.server_snapshot_provider, "before", run_id, counters_before)
    provider(args.server_evidence_provider, "stable", run_id, server_stable)
    provider(
        args.server_evidence_provider,
        "before",
        run_id,
        run_dir / "server-evidence-before.json",
    )
    with (run_dir / "results.ndjson").open("wb") as stdout, (
        run_dir / "h3fuzz.stderr"
    ).open("wb") as stderr:
        completed = subprocess.run(command, stdout=stdout, stderr=stderr, check=False)
    provider(args.server_snapshot_provider, "after", run_id, counters_after)
    provider(
        args.server_evidence_provider,
        "after",
        run_id,
        run_dir / "server-evidence-after.json",
    )
    if completed.returncode != 0:
        raise OperationalError(f"exact-hit {name} exited with status {completed.returncode}")
    telemetry_path = run_dir / "run-telemetry.json"
    if not telemetry_path.is_file():
        raise OperationalError(f"exact-hit {name} telemetry is missing")
    telemetry = read_json(telemetry_path)
    attempts, strategy = exact_telemetry(
        telemetry,
        expected_inputs=len(read_words(wordlist)),
        get_control=get_control,
    )
    correlation = run_dir / "origin-correlation.json"
    correlation_command = [
        sys.executable,
        str(SCRIPT_DIR / "origin_snapshot.py"),
        "correlate",
        "--before",
        str(counters_before),
        "--after",
        str(counters_after),
        "--run-id",
        run_id,
        "--route",
        "files",
        "--protocol",
        "h3",
        "--expected-handler-requests",
        str(attempts),
        "--output",
        str(correlation),
    ]
    correlated = subprocess.run(correlation_command, check=False)
    if correlated.returncode != 0:
        raise OperationalError(f"exact-hit {name} origin correlation failed")
    correlation_data = read_json(correlation)
    if correlation_data.get("eligible") is not True:
        raise OperationalError(f"exact-hit {name} origin evidence is ineligible")
    stable = read_json(server_stable)
    return {
        "name": name,
        "run_id": run_id,
        "strategy": strategy,
        "application_attempts": attempts,
        "server_identity": stable.get("host_identity_sha256"),
        "binary_sha256": file_sha256(binary),
        "argv": artifact_reference(run_dir / "argv.json", args.artifact_dir),
        "results": artifact_reference(run_dir / "results.ndjson", args.artifact_dir),
        "telemetry": artifact_reference(telemetry_path, args.artifact_dir),
        "origin_correlation": artifact_reference(correlation, args.artifact_dir),
        "server_stable": artifact_reference(server_stable, args.artifact_dir),
        "server_counters_before": artifact_reference(counters_before, args.artifact_dir),
        "server_counters_after": artifact_reference(counters_after, args.artifact_dir),
        "server_evidence_before": artifact_reference(
            run_dir / "server-evidence-before.json", args.artifact_dir
        ),
        "server_evidence_after": artifact_reference(
            run_dir / "server-evidence-after.json", args.artifact_dir
        ),
    }


def result_words(compare_module, path: Path) -> set[str]:
    return set(compare_module.load_results(path))


def run(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(argv)
        validate_args(args)
        certification_report = load_module(
            "perf503_certification_report", SCRIPT_DIR / "certification-report.py"
        )
        compare_results = load_module("perf503_compare_results", SCRIPT_DIR / "compare-results.py")
        sweep, selection = certification_report.verify_fixed_sweep(args.fixed_sweep_manifest)
        point = selected_point(sweep, selection)
        source_report = selected_report(args.fixed_sweep_manifest, sweep, point)
        strategy = infer_strategy(source_report)
        target = urlsplit(args.target)
        if target.netloc != sweep.get("authority"):
            raise ContractError("--target authority differs from fixed sweep")
        point_rps = point.get("median_completed_response_rps")
        if not isinstance(point_rps, (int, float)) or isinstance(point_rps, bool) or point_rps <= 0:
            raise ContractError("selected fixed point has no positive median RPS")

        args.artifact_dir.mkdir(mode=0o700)
        inputs = args.artifact_dir / "inputs"
        inputs.mkdir()
        correctness_wordlist = inputs / "correctness.txt"
        throughput_base = inputs / "throughput-base.txt"
        expected_path = inputs / "expected-hits.txt"
        shutil.copy2(args.correctness_wordlist, correctness_wordlist)
        shutil.copy2(args.throughput_base_wordlist, throughput_base)
        shutil.copy2(EXPECTED_HITS, expected_path)
        expected_hits = read_words(expected_path)
        if len(expected_hits) != len(set(expected_hits)) or not expected_hits:
            raise ContractError("expected-hit fixture must be nonempty and unique")

        target_inputs = math.ceil(max(100_000.0, float(point_rps)) * args.target_seconds * 1.20)
        corpus_path = inputs / "seeded-throughput.txt"
        corpus = generate_corpus(
            throughput_base,
            expected_hits,
            seed=args.seed,
            target_inputs=target_inputs,
            output=corpus_path,
        )
        corpus.update(
            {
                "target_seconds": args.target_seconds,
                "headroom_percent": 20,
                "path": corpus_path.relative_to(args.artifact_dir).as_posix(),
            }
        )

        long_report, long_report_path = run_long_trial(
            args, wordlist=corpus_path, selection=selection, strategy=strategy
        )
        long_span = long_report.get("main_request_span_seconds")
        long_completed = long_report.get("successful_logical_responses")
        if (
            long_report.get("run_gates_passed") is not True
            or long_report.get("eligible_for_aggregation") is not True
            or not isinstance(long_span, (int, float))
            or long_span < args.target_seconds
            or long_completed != corpus["line_count"]
        ):
            raise OperationalError("long-corpus benchmark failed duration or accounting gates")
        for field in ("platform_key", "client_build_key"):
            if comparison_value(long_report, field, "long_corpus") != sweep.get(field):
                raise OperationalError(f"long-corpus {field} differs from fixed sweep")
        if long_report.get("authority") != sweep.get("authority"):
            raise OperationalError("long-corpus authority differs from fixed sweep")
        if long_report.get("server_identity") != sweep.get("server_identity"):
            raise OperationalError("long-corpus server identity differs from fixed sweep")
        workload_key = comparison_value(long_report, "workload_key", "long_corpus")
        if workload_key == sweep.get("workload_key"):
            raise OperationalError("/files workload key unexpectedly equals /bench workload key")
        long_results_path = args.artifact_dir / "long-corpus" / "stdout.txt"
        long_words = result_words(compare_results, long_results_path)
        expected_set = set(expected_hits)
        if long_words != expected_set:
            raise OperationalError(
                "long corpus findings differ from known hits: "
                f"missing={sorted(expected_set - long_words)!r} "
                f"unexpected={sorted(long_words - expected_set)!r}"
            )

        binary = resolve_h3fuzz()
        client_build = long_report.get("comparison_keys", {}).get("client_build", {})
        if client_build.get("binary_sha256") != file_sha256(binary):
            raise OperationalError("exact-hit binary differs from long-corpus binary")
        get_run = run_exact_side(
            binary,
            args,
            wordlist=correctness_wordlist,
            selection=selection,
            name="get-control",
            get_control=True,
        )
        optimized_run = run_exact_side(
            binary,
            args,
            wordlist=correctness_wordlist,
            selection=selection,
            name="optimized",
            get_control=False,
        )
        for side in (get_run, optimized_run):
            if side["server_identity"] != sweep.get("server_identity"):
                raise OperationalError(f"exact-hit {side['name']} server identity differs from sweep")

        comparison_path = args.artifact_dir / "exact-hit" / "comparison.json"
        comparison_stderr = args.artifact_dir / "exact-hit" / "comparison.stderr"
        compare_command = [
            sys.executable,
            str(SCRIPT_DIR / "compare-results.py"),
            str(args.artifact_dir / get_run["results"]["path"]),
            str(args.artifact_dir / optimized_run["results"]["path"]),
            "--expected",
            str(expected_path),
        ]
        with comparison_path.open("wb") as stdout, comparison_stderr.open("wb") as stderr:
            comparison_status = subprocess.run(
                compare_command, stdout=stdout, stderr=stderr, check=False
            ).returncode
        if comparison_status != 0:
            raise OperationalError("exact-hit GET/optimized comparison failed")
        comparison = read_json(comparison_path)
        get_words = result_words(
            compare_results, args.artifact_dir / get_run["results"]["path"]
        )
        optimized_words = result_words(
            compare_results, args.artifact_dir / optimized_run["results"]["path"]
        )
        missing = sorted(expected_set - optimized_words)
        unexpected = sorted(optimized_words - expected_set)
        semantic_differences = [
            difference
            for difference in comparison.get("metadata_differences", [])
            if any(
                key not in {"words", "lines"}
                for key in difference.get("changed", {})
            )
        ]
        passed = (
            get_words == optimized_words == expected_set
            and not missing
            and not unexpected
            and not semantic_differences
        )
        if not passed:
            raise OperationalError("exact-hit correctness proof is ineligible")

        long_keys = long_report["comparison_keys"]
        exact_components = {
            "platform": copy.deepcopy(long_keys["platform"]),
            "client_build": copy.deepcopy(long_keys["client_build"]),
            "workload": {
                "authority": target.netloc,
                "route": "/files/FUZZ",
                "protocol": "http3",
                "purpose": "exact-hit-differential",
                "wordlist_sha256": file_sha256(correctness_wordlist),
                "wordlist_line_count": len(read_words(correctness_wordlist)),
                "filter_status": "404",
            },
            "treatment": {
                "kind": "get-versus-optimized-differential",
                "connections": selection["connections"],
                "streams": selection["streams"],
                "optimized_strategy": optimized_run["strategy"],
            },
        }
        exact_report_path = args.artifact_dir / "exact-hit" / "exact-hit-report.json"
        exact_report = {
            "schema_version": 1,
            "report_type": "h3fuzz-files-exact-hit",
            "target": args.target,
            "authority": sweep["authority"],
            "server_identity": sweep["server_identity"],
            "comparison_keys": comparison_keys(exact_components),
            "expected_hits": sorted(expected_set),
            "actual_hits": sorted(optimized_words),
            "false_positives": unexpected,
            "missing_hits": missing,
            "filter_status": "404",
            "filter_matches_get_control": True,
            "metadata_compatible": not semantic_differences,
            "get": get_run,
            "optimized": optimized_run,
            "comparison": artifact_reference(comparison_path, args.artifact_dir),
        }
        atomic_write_json(exact_report_path, exact_report)

        server_handler_requests = long_report.get("server_correlation", {}).get(
            "observed_handler_requests"
        )
        if not isinstance(server_handler_requests, int) or server_handler_requests <= 0:
            raise OperationalError("long-corpus server workload count is missing")
        exact_reference = artifact_reference(exact_report_path, args.artifact_dir)
        long_reference = artifact_reference(long_report_path, args.artifact_dir)
        comparison_reference = artifact_reference(comparison_path, args.artifact_dir)
        body = {
            "schema_version": 1,
            "manifest_type": "h3fuzz-files-validation",
            "created_at": utc_now(),
            "eligible": True,
            "ineligible_reasons": [],
            "platform_key": sweep["platform_key"],
            "client_build_key": sweep["client_build_key"],
            "workload_key": workload_key,
            "authority": sweep["authority"],
            "server_identity": sweep["server_identity"],
            "route": "/files/FUZZ",
            "selected_point": {
                "connections": selection["connections"],
                "streams": selection["streams"],
                "max_workers": selection["adaptive_max_workers"],
                "treatment_key": selection["fixed_treatment_key"],
                "cohort_key": selection["fixed_cohort_key"],
            },
            "ceiling_reference": {
                "manifest_sha256": sweep["manifest_sha256"],
                "workload_key": sweep["workload_key"],
                "authority": sweep["authority"],
                "route": "/bench/FUZZ",
            },
            "corpus": corpus,
            "duplicate_accounting": (
                "Every non-comment exact input is retained at most once; duplicate base entries "
                "are removed, each known hit appears exactly once, and every generated miss is unique."
            ),
            "correctness": {
                "passed": True,
                "expected_hits": len(expected_set),
                "actual_hits": len(optimized_words),
                "false_positives": len(unexpected),
                "missing_hits": missing,
                "unexpected_hits": unexpected,
                "filter_matches_get_control": True,
                "metadata_compatible": not semantic_differences,
                "get_strategy": "GET",
                "optimized_strategy": optimized_run["strategy"],
                "get_application_attempts": get_run["application_attempts"],
                "optimized_application_attempts": optimized_run["application_attempts"],
                "comparison": comparison_reference,
            },
            "throughput": {
                "route": "/files/FUZZ",
                "bench_route": "/bench/FUZZ",
                "distinct_from_bench": True,
                "completed_response_rps": long_report["completed_response_rps"],
                "main_request_span_seconds": long_span,
                "successful_logical_responses": long_completed,
                "server_handler_requests": server_handler_requests,
                "expected_hits": len(expected_set),
                "actual_hits": len(long_words),
                "false_positives": len(long_words - expected_set),
                "report": long_reference,
            },
            "runs": {"exact_hit": exact_reference, "long_corpus": long_reference},
        }
        manifest = seal_manifest(body, args.manifest, SCHEMA)
        verify_files_manifest(args.manifest, SCHEMA)
        print(
            json.dumps(
                {
                    "manifest": str(args.manifest),
                    "manifest_sha256": manifest["manifest_sha256"],
                    "completed_response_rps": long_report["completed_response_rps"],
                },
                sort_keys=True,
            )
        )
        return 0
    except ContractError as exc:
        print(f"validate-files: {exc}", file=sys.stderr)
        return 2
    except (OSError, OperationalError, ValueError) as exc:
        print(f"validate-files: {exc}", file=sys.stderr)
        return 4


if __name__ == "__main__":
    raise SystemExit(run())

