#!/usr/bin/env python3
"""Run benchmark-v1 with a single, explicit PERF-503 diagnostic treatment."""

from __future__ import annotations

import argparse
import importlib.util
from pathlib import Path
import sys
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parent))
from perf004_contract import atomic_write_json, read_json  # noqa: E402


SCRIPT_DIR = Path(__file__).resolve().parent
BENCHMARK_PATH = SCRIPT_DIR / "benchmark-v1.py"
PROFILE_FLAGS = {
    "cpu": "profile-cpu",
    "heap": "profile-heap",
    "mutex": "profile-mutex",
    "block": "profile-block",
}


def load_benchmark():
    spec = importlib.util.spec_from_file_location("perf503_benchmark_v1", BENCHMARK_PATH)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def option(arguments: list[str], name: str) -> str:
    try:
        index = arguments.index(name)
        return arguments[index + 1]
    except (ValueError, IndexError) as exc:
        raise ValueError(f"missing required delegated option {name}") from exc


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument(
        "--diagnostic-profile",
        choices=("none", "cpu", "heap", "mutex", "block", "quic"),
        default="none",
    )
    parser.add_argument("--json-output", action="store_true")
    parser.add_argument("arguments", nargs=argparse.REMAINDER)
    args = parser.parse_args(argv)
    delegated = list(args.arguments)
    if delegated[:1] == ["--"]:
        delegated.pop(0)
    if not delegated:
        parser.error("benchmark-v1 arguments are required after --")

    artifact_dir = Path(option(delegated, "--artifact-dir")).resolve()
    run_id = option(delegated, "--run-id")
    target = option(delegated, "--target")
    mode = option(delegated, "--mode")
    if args.diagnostic_profile != "none" and mode != "diagnostic":
        parser.error("a diagnostic profile requires --mode diagnostic")

    benchmark = load_benchmark()
    original_binary_command = benchmark.binary_command
    original_build_metadata = benchmark.build_metadata

    def binary_command(namespace, binary, phase_file, telemetry, debug):
        command, response_mode = original_binary_command(
            namespace, binary, phase_file, telemetry, debug
        )
        if args.json_output:
            command.append("-json")
        if args.diagnostic_profile in PROFILE_FLAGS:
            command.extend(
                (
                    "-" + PROFILE_FLAGS[args.diagnostic_profile],
                    str(namespace.artifact_dir / f"{args.diagnostic_profile}.pprof"),
                )
            )
        elif args.diagnostic_profile == "quic":
            command.append("-quic-telemetry")
        return command, response_mode

    def build_metadata(namespace, client, server, binary):
        result = original_build_metadata(namespace, client, server, binary)
        result["treatment"]["profile_flags"] = (
            [] if args.diagnostic_profile == "none" else [args.diagnostic_profile]
        )
        return result

    benchmark.binary_command = binary_command
    benchmark.build_metadata = build_metadata
    status = benchmark.run(delegated)

    report_path = artifact_dir / "benchmark-report.json"
    if report_path.is_file():
        report = read_json(report_path)
        metadata = read_json(artifact_dir / "comparison-metadata.json")
        platform = metadata.get("platform", {})
        report.update(
            {
                "run_id": run_id,
                "source_eligibility_run": False,
                "diagnostic_profile": args.diagnostic_profile,
                "authority": urlsplit(target).netloc,
                "server_identity": platform.get("server_host_identity"),
            }
        )
        atomic_write_json(report_path, report, refuse_existing=False)
    return status


if __name__ == "__main__":
    raise SystemExit(main())

