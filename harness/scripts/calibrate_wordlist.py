#!/usr/bin/env python3
"""Bounded deterministic wordlist calibration implementation."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from perf004_contract import (  # noqa: E402
    ContractError,
    add_manifest_hash,
    atomic_write_json,
    file_sha256,
    read_json,
    utc_now,
)


GENERATOR_VERSION = "h3fuzz-deterministic-wordlist-v1"
MAX_ATTEMPTS = 3


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
    parser = argparse.ArgumentParser()
    parser.add_argument("--target", required=True)
    parser.add_argument("--base-wordlist", type=Path, required=True)
    parser.add_argument("--seed", type=unsigned, required=True)
    parser.add_argument("--target-rps", type=positive, required=True)
    parser.add_argument("--target-seconds", type=positive, required=True)
    parser.add_argument("--headroom-percent", type=positive, required=True)
    parser.add_argument("--extensions", default="")
    parser.add_argument("--expect-strategy", choices=("head", "range", "get"), required=True)
    parser.add_argument("--filter-status")
    parser.add_argument("--filter-size")
    parser.add_argument("--streams", type=positive, required=True)
    parser.add_argument("--max-connections", type=positive, required=True)
    parser.add_argument("--max-workers", type=positive, required=True)
    parser.add_argument("--server-snapshot-provider", type=Path, required=True)
    parser.add_argument("--server-evidence-provider", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    return parser.parse_args(argv)


def require_absolute(path: Path, label: str) -> Path:
    if not path.is_absolute():
        raise ContractError(f"{label} must be an absolute path")
    return path.resolve()


def generate_candidate(base_lines: list[str], seed: int, count: int, path: Path) -> None:
    if count < len(base_lines):
        raise ContractError("candidate count cannot preserve every unique base entry")
    path.parent.mkdir(parents=True, exist_ok=True)
    seen = set(base_lines)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as output:
            for line in base_lines:
                output.write(line + "\n")
            synthetic_index = 0
            while len(seen) < count:
                digest = hashlib.sha256(f"{seed}:{synthetic_index}".encode()).hexdigest()[:20]
                candidate = f"__h3fuzz_miss_{seed}_{synthetic_index}_{digest}"
                synthetic_index += 1
                if candidate in seen:
                    continue
                seen.add(candidate)
                output.write(candidate + "\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def expanded_count(path: Path, extensions: str) -> int:
    if not extensions:
        return sum(1 for _ in path.open("rb"))
    provider = os.environ.get("H3FUZZ_INPUT_COUNT_PROVIDER")
    if not provider or not Path(provider).is_absolute() or not os.access(provider, os.X_OK):
        raise ContractError(
            "--extensions requires executable H3FUZZ_INPUT_COUNT_PROVIDER; refusing to guess h3fuzz expansion"
        )
    completed = subprocess.run(
        [provider, "--wordlist", str(path), "--extensions", extensions],
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if completed.returncode != 0:
        raise ContractError(f"input count provider failed: {completed.stderr.strip()}")
    try:
        value = int(completed.stdout.strip())
    except ValueError as exc:
        raise ContractError("input count provider did not return an integer") from exc
    if value <= 0:
        raise ContractError("input count provider returned a non-positive count")
    return value


def freeze_file(source: Path, destination: Path) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists():
        raise ContractError(f"refusing to overwrite frozen wordlist: {destination}")
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{destination.name}.", dir=destination.parent)
    temporary = Path(temporary_name)
    try:
        with source.open("rb") as input_file, os.fdopen(descriptor, "wb") as output_file:
            shutil.copyfileobj(input_file, output_file)
            output_file.flush()
            os.fsync(output_file.fileno())
        os.chmod(temporary, 0o600)
        try:
            os.link(temporary, destination)
        except FileExistsError as exc:
            raise ContractError(f"refusing to overwrite frozen wordlist: {destination}") from exc
        temporary.unlink()
        directory_fd = os.open(destination.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def benchmark_command(args: argparse.Namespace, candidate: Path, run_dir: Path) -> list[str]:
    benchmark = Path(
        os.environ.get("H3FUZZ_BENCHMARK_SCRIPT", str(Path(__file__).with_name("benchmark.sh")))
    )
    command = [
        str(benchmark),
        "--target", args.target,
        "--wordlist", str(candidate),
        "--mode", "adaptive",
        "--expect-strategy", args.expect_strategy,
        "--streams", str(args.streams),
        "--max-connections", str(args.max_connections),
        "--max-workers", str(args.max_workers),
        "--run-id", str(uuid.uuid4()),
        "--server-snapshot-provider", str(args.server_snapshot_provider),
        "--server-evidence-provider", str(args.server_evidence_provider),
        "--artifact-dir", str(run_dir),
    ]
    if args.extensions:
        command.extend(("--extensions", args.extensions))
    if args.filter_status:
        command.extend(("--filter-status", args.filter_status))
    if args.filter_size:
        command.extend(("--filter-size", args.filter_size))
    return command


def run(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(argv)
        if not args.target.startswith("https://") or "FUZZ" not in args.target:
            raise ContractError("--target must be an HTTPS URL containing FUZZ")
        if args.target_seconds != 30:
            raise ContractError("contract version 1 requires --target-seconds 30")
        if args.headroom_percent != 20:
            raise ContractError("contract version 1 requires --headroom-percent 20")
        for field in (
            "base_wordlist", "server_snapshot_provider", "server_evidence_provider",
            "output", "manifest", "artifact_dir",
        ):
            setattr(args, field, require_absolute(getattr(args, field), f"--{field.replace('_', '-')}"))
        if not args.base_wordlist.is_file():
            raise ContractError(f"base wordlist not found: {args.base_wordlist}")
        for provider in (args.server_snapshot_provider, args.server_evidence_provider):
            if not provider.is_file() or not os.access(provider, os.X_OK):
                raise ContractError(f"provider is not executable: {provider}")
        if args.output.exists() or args.manifest.exists():
            raise ContractError("frozen output and manifest are immutable and must not already exist")
        if args.artifact_dir.exists() and any(args.artifact_dir.iterdir()):
            raise ContractError("calibration artifact directory must not already contain files")
        args.artifact_dir.mkdir(parents=True, exist_ok=True)
        os.chmod(args.artifact_dir, 0o700)

        raw_lines = args.base_wordlist.read_text(encoding="utf-8").splitlines()
        base_lines = list(dict.fromkeys(line for line in raw_lines if line))
        if not base_lines:
            raise ContractError("base wordlist has no nonempty entries")
        base_hash = file_sha256(args.base_wordlist)
        fastest_clean = 0.0
        requested_inputs = max(len(base_lines), math.ceil(args.target_rps * 30 * 1.20))
        attempts: list[dict[str, object]] = []
        final_candidate: Path | None = None
        final_expanded = 0

        for attempt in range(1, MAX_ATTEMPTS + 1):
            candidate = args.artifact_dir / "candidates" / f"candidate-{attempt:02d}.txt"
            generate_candidate(base_lines, args.seed, requested_inputs, candidate)
            actual_expanded = expanded_count(candidate, args.extensions)
            run_dir = args.artifact_dir / f"attempt-{attempt:02d}"
            command = benchmark_command(args, candidate, run_dir)
            command_path = args.artifact_dir / f"attempt-{attempt:02d}.argv.json"
            atomic_write_json(command_path, {"argv": command})
            completed = subprocess.run(command, check=False)
            report_path = run_dir / "benchmark-report.json"
            if completed.returncode != 0 or not report_path.is_file():
                raise ContractError(f"calibration benchmark attempt {attempt} failed")
            report = read_json(report_path)
            clean = report.get("run_gates_passed") is True
            rate = report.get("completed_response_rps")
            span = report.get("main_request_span_seconds")
            if clean and isinstance(rate, (int, float)) and math.isfinite(float(rate)) and rate > 0:
                fastest_clean = max(fastest_clean, float(rate))
            next_inputs = math.ceil(max(args.target_rps, fastest_clean) * 30 * 1.20)
            record: dict[str, object] = {
                "attempt": attempt,
                "requested_line_count": requested_inputs,
                "expanded_input_count": actual_expanded,
                "wordlist_sha256": file_sha256(candidate),
                "report": str(report_path.relative_to(args.artifact_dir)),
                "report_sha256": file_sha256(report_path),
                "run_gates_passed": clean,
                "main_request_span_seconds": span,
                "completed_response_rps": rate,
                "formula": {
                    "target_rps": args.target_rps,
                    "fastest_clean_observed_rps": fastest_clean,
                    "target_seconds": 30,
                    "headroom_percent": 20,
                    "next_inputs": next_inputs,
                },
                "next_inputs": next_inputs,
            }
            attempts.append(record)
            if clean and isinstance(span, (int, float)) and float(span) >= 30.0:
                final_candidate = candidate
                final_expanded = actual_expanded
                break
            if next_inputs <= requested_inputs:
                raise ContractError(
                    "short calibration produced a non-increasing exact sizing result"
                )
            requested_inputs = next_inputs

        if final_candidate is None:
            print("calibrate-wordlist: three bounded attempts did not reach 30 seconds", file=sys.stderr)
            return 3

        freeze_file(final_candidate, args.output)
        manifest = add_manifest_hash({
            "schema_version": 1,
            "manifest_type": "h3fuzz-frozen-wordlist",
            "created_at": utc_now(),
            "frozen": True,
            "generator_version": GENERATOR_VERSION,
            "seed": args.seed,
            "path": str(args.output),
            "line_count": sum(1 for _ in args.output.open("rb")),
            "expanded_input_count": final_expanded,
            "sha256": file_sha256(args.output),
            "base_corpus": {
                "path": str(args.base_wordlist),
                "line_count": len(raw_lines),
                "unique_nonempty_line_count": len(base_lines),
                "sha256": base_hash,
            },
            "extensions": [value for value in args.extensions.split(",") if value],
            "target_rps": args.target_rps,
            "target_seconds": 30,
            "headroom_percent": 20,
            "maximum_calibration_attempts": MAX_ATTEMPTS,
            "calibration_attempts": attempts,
            "artifact_directory": str(args.artifact_dir),
        })
        atomic_write_json(args.manifest, manifest)
        print(json.dumps(manifest, sort_keys=True))
        return 0
    except ContractError as exc:
        print(f"calibrate-wordlist: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:
        print(f"calibrate-wordlist: unexpected error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(run())
