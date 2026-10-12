#!/usr/bin/env python3
"""Contract-v1 single-trial runner used by benchmark.sh harness modes.

This is intentionally strict: absent Linux/socket/origin evidence makes an
aggregation trial return 3 instead of silently falling back to legacy metrics.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from typing import Any
from urllib.parse import urlsplit
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from perf004_contract import ContractError, atomic_write_json, canonical_hash, file_sha256, read_json  # noqa: E402
from sweep_resource_usage import build_evidence as build_sweep_resource_evidence  # noqa: E402


SCRIPT_DIR = Path(__file__).resolve().parent
HARNESS_DIR = SCRIPT_DIR.parent
REPO_ROOT = HARNESS_DIR.parent
FIXED_MODES = {"baseline", "diagnostic", "sweep", "selected-fixed"}
AGGREGATING_MODES = {"baseline", "sweep", "adaptive", "selected-fixed"}


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


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--target", required=True)
    parser.add_argument("--wordlist", type=Path, required=True)
    parser.add_argument("--mode", choices=("baseline", "diagnostic", "sweep", "adaptive", "selected-fixed"), required=True)
    parser.add_argument("--extensions", default="")
    parser.add_argument("--expect-strategy", choices=("head", "range", "get"), required=True)
    parser.add_argument("--filter-status")
    parser.add_argument("--filter-size")
    parser.add_argument("--streams", type=positive, required=True)
    parser.add_argument("--connections", type=positive)
    parser.add_argument("--max-connections", type=positive)
    parser.add_argument("--threads", type=positive)
    parser.add_argument("--max-workers", type=positive)
    parser.add_argument("--target-rps", type=positive, default=100_000)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--server-snapshot-provider", type=Path, required=True)
    parser.add_argument("--server-evidence-provider", type=Path, required=True)
    parser.add_argument("--client-before", type=Path, required=True)
    parser.add_argument("--client-after", type=Path, required=True)
    parser.add_argument("--client-runtime-sockets", type=Path, required=True)
    parser.add_argument("--server-counters-before", type=Path, required=True)
    parser.add_argument("--server-counters-after", type=Path, required=True)
    parser.add_argument("--server-stable", type=Path, required=True)
    parser.add_argument("--server-evidence-before", type=Path, required=True)
    parser.add_argument("--server-evidence-after", type=Path, required=True)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    return parser.parse_args(argv)


def absolute(path: Path, label: str, *, exists: bool = False) -> Path:
    if not path.is_absolute():
        raise ContractError(f"{label} must be absolute")
    result = path.resolve()
    if exists and not result.exists():
        raise ContractError(f"{label} does not exist: {result}")
    return result


def under(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def validate(args: argparse.Namespace) -> None:
    if not args.target.startswith("https://") or "FUZZ" not in args.target:
        raise ContractError("--target must be an HTTPS URL containing FUZZ")
    try:
        uuid.UUID(args.run_id)
    except ValueError as exc:
        raise ContractError("--run-id must be a UUID") from exc
    args.wordlist = absolute(args.wordlist, "--wordlist", exists=True)
    args.artifact_dir = absolute(args.artifact_dir, "--artifact-dir")
    if args.artifact_dir.exists():
        raise ContractError("--artifact-dir must not already exist")
    for field in (
        "server_snapshot_provider", "server_evidence_provider", "client_before", "client_after",
        "client_runtime_sockets", "server_counters_before", "server_counters_after", "server_stable",
        "server_evidence_before", "server_evidence_after",
    ):
        path = absolute(getattr(args, field), f"--{field.replace('_', '-')}")
        setattr(args, field, path)
        if field.endswith("provider"):
            if not path.is_file() or not os.access(path, os.X_OK):
                raise ContractError(f"provider is not executable: {path}")
        elif not under(path, args.artifact_dir):
            raise ContractError(f"--{field.replace('_', '-')} must be inside --artifact-dir")
    if not args.filter_status and not args.filter_size:
        raise ContractError("at least one baseline status or size filter is required")
    if args.mode in FIXED_MODES and args.connections is None:
        raise ContractError(f"{args.mode} mode requires --connections")
    if args.mode == "adaptive" and args.max_connections is None:
        raise ContractError("adaptive mode requires --max-connections")
    if args.mode != "adaptive" and args.max_connections is not None:
        raise ContractError(f"{args.mode} mode rejects --max-connections")
    if args.mode == "adaptive" and args.connections is not None:
        raise ContractError("adaptive mode rejects --connections")
    if args.threads is not None and args.mode not in {"baseline", "diagnostic"}:
        raise ContractError(f"{args.mode} mode rejects --threads")
    if args.mode in {"adaptive", "selected-fixed"} and args.max_workers is None:
        raise ContractError(f"{args.mode} mode requires --max-workers")
    if args.mode == "sweep" and args.max_workers is None:
        raise ContractError("sweep mode requires --max-workers")


def call(command: list[str], *, allowed: tuple[int, ...] = (0,), label: str) -> int:
    completed = subprocess.run(command, check=False)
    if completed.returncode not in allowed:
        raise OperationalError(f"{label} failed with exit {completed.returncode}")
    return completed.returncode


def provider(provider: Path, phase: str, run_id: str, output: Path, label: str) -> int:
    output.parent.mkdir(parents=True, exist_ok=True)
    status = call(
        [str(provider), "--phase", phase, "--run-id", run_id, "--output", str(output)],
        allowed=(0, 3),
        label=label,
    )
    if not output.is_file():
        raise OperationalError(f"{label} did not produce {output}")
    return status


def last_phase(path: Path) -> str | None:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
        return json.loads(lines[-1]).get("phase") if lines else None
    except (OSError, json.JSONDecodeError, IndexError):
        return None


def snapshot_point(path: Path, phase: str, run_id: str, pid: int) -> bool:
    try:
        path.unlink()
    except FileNotFoundError:
        pass
    status = subprocess.run(
        [
            str(SCRIPT_DIR / "network-snapshot.sh"), "--mode", "point", "--kind", "client_point",
            "--phase", phase, "--run-id", run_id, "--process", f"h3fuzz:{pid}", "--output", str(path),
        ],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    ).returncode
    if status != 0 or not path.is_file():
        return False
    try:
        return read_json(path).get("eligible") is True
    except ContractError:
        return False


def runtime_sample(path: Path, phase: str, run_id: str, pid: int, append: bool) -> bool:
    command = [
        str(SCRIPT_DIR / "network-snapshot.sh"), "--mode", "runtime-sockets", "--phase", phase,
        "--run-id", run_id, "--process", f"h3fuzz:{pid}", "--output", str(path),
    ]
    if append:
        command.append("--append")
    return subprocess.run(
        command,
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    ).returncode == 0


def binary_command(args: argparse.Namespace, binary: Path, phase_file: Path, telemetry: Path, debug: Path) -> tuple[list[str], str]:
    command = [
        str(binary), "-u", args.target, "-w", str(args.wordlist), "-k", "-http3",
        "-http3-streams", str(args.streams), "-harness-run-id", args.run_id,
        "-harness-mode", args.mode, "-harness-phase-file", str(phase_file),
        "-run-telemetry", str(telemetry), "-debug-log", str(debug), "-noninteractive", "-s",
    ]
    if args.connections is not None:
        command.extend(("-http3-connections", str(args.connections)))
    if args.max_connections is not None:
        command.extend(("-http3-max-connections", str(args.max_connections)))
    if args.max_workers is not None:
        command.extend(("-http3-max-workers", str(args.max_workers)))
    if args.threads is not None:
        command.extend(("-t", str(args.threads)))
    if args.mode in {"adaptive", "selected-fixed"}:
        command.extend(("-http3-high-rate", "-http3-high-rate-acknowledge", "-http3-target-rps", str(args.target_rps)))
    if args.extensions:
        command.extend(("-e", args.extensions))
    if args.filter_status:
        command.extend(("-fc", args.filter_status))
    if args.filter_size:
        command.extend(("-fs", args.filter_size))
    response_mode = "auto"
    if args.expect_strategy == "get":
        command.extend(("-no-response-optimization", "-X", "GET"))
        response_mode = "get"
    elif args.expect_strategy == "range":
        command.extend(("-no-response-optimization", "-X", "GET", "-H", "Range: bytes=-1"))
        response_mode = "range"
    return command, response_mode


def compare_network(before: Path, after: Path, output: Path) -> int:
    attribution = os.environ.get("H3FUZZ_NETWORK_ATTRIBUTION", "")
    option = {
        "dedicated-host": "--dedicated-host",
        "isolated-network-namespace": "--isolated-network-namespace",
    }.get(attribution)
    if option is None:
        raise ContractError(
            "H3FUZZ_NETWORK_ATTRIBUTION must be dedicated-host or isolated-network-namespace"
        )
    return call(
        [str(SCRIPT_DIR / "network-snapshot.sh"), "--mode", "compare", "--before", str(before),
         "--after", str(after), option, "--output", str(output)],
        allowed=(0, 3),
        label="network evidence comparison",
    )


def route_name(target: str) -> str:
    path = urlsplit(target).path
    if "/files/" in path:
        return "files"
    if "/large/" in path:
        return "large"
    if "/bench/" in path:
        return "bench"
    raise ContractError("target route must be /bench/, /files/, or /large/")


def build_metadata(args: argparse.Namespace, client: dict[str, Any], server: dict[str, Any], binary: Path) -> dict[str, Any]:
    target = urlsplit(args.target)
    platform = {
        "client_host_identity": client.get("host_identity_sha256"),
        "server_host_identity": server.get("host_identity_sha256"),
        "client_cpu": client.get("cpu"),
        "server_cpu": server.get("cpu"),
        "client_kernel": client.get("kernel"),
        "server_kernel": server.get("kernel"),
        "client_udp_sysctls_bytes": client.get("udp_sysctls_bytes"),
        "server_udp_sysctls_bytes": server.get("udp_sysctls_bytes"),
        "client_quic_udp_buffer_target": client.get("quic_udp_buffer_target"),
        "server_quic_udp_buffer_target": server.get("quic_udp_buffer_target"),
        "client_network_namespace": client.get("network_namespace_inode"),
        "server_network_namespace": server.get("network_namespace_inode"),
        "network_path": {
            "identity": os.environ.get("H3FUZZ_NETWORK_PATH_ID"),
            "target": client.get("target"),
            "attribution": os.environ.get("H3FUZZ_NETWORK_ATTRIBUTION"),
        },
        "server_origin": server.get("origin"),
    }
    modules = client.get("h3fuzz", {}).get("modules", {})
    build = {
        "source_tree_sha256": client.get("source_tree", {}).get("canonical_sha256"),
        "source_git_commit": client.get("source_tree", {}).get("git_commit"),
        "source_dirty": client.get("source_tree", {}).get("dirty"),
        "binary_sha256": file_sha256(binary),
        "dependency_sha256": canonical_hash(modules),
        "modules": modules,
        "go_version": client.get("h3fuzz", {}).get("go_version"),
    }
    workload = {
        "authority": target.netloc,
        "route": target.path,
        "protocol": "http3",
        "method": "GET" if args.expect_strategy in {"get", "range", "head"} else None,
        "headers": ["Range: bytes=-1"] if args.expect_strategy == "range" else [],
        "filters": {"status": args.filter_status, "size": args.filter_size},
        "strategy": args.expect_strategy.upper(),
        "datagrams": False,
        "timeouts": {"request_seconds": None, "probe_seconds": os.environ.get("H3FUZZ_PROBE_TIMEOUT", "2")},
        "extensions": [value for value in args.extensions.split(",") if value],
        "input_mode": "clusterbomb",
        "wordlist_sha256": file_sha256(args.wordlist),
        "wordlist_line_count": sum(1 for _ in args.wordlist.open("rb")),
    }
    connection = (
        {"kind": "adaptive", "maximum": args.max_connections}
        if args.mode == "adaptive"
        else {"kind": "fixed", "value": args.connections}
    )
    treatment = {
        "mode": args.mode,
        "connection_policy": connection,
        "streams": args.streams,
        "worker_policy": {
            "kind": "explicit_threads" if args.threads is not None else "bounded",
            "threads": args.threads,
            "maximum": args.max_workers,
        },
        "profile_flags": [],
    }
    evidence_reasons = []
    if client.get("eligible") is not True:
        evidence_reasons.extend(f"client:{value}" for value in client.get("ineligibility_reasons", ["stable_evidence_ineligible"]))
    if server.get("eligible") is not True:
        evidence_reasons.extend(f"server:{value}" for value in server.get("ineligibility_reasons", ["stable_evidence_ineligible"]))
    return {
        "schema_version": 1,
        "eligible": not evidence_reasons,
        "ineligibility_reasons": sorted(set(evidence_reasons)),
        "platform": platform,
        "client_build": build,
        "workload": workload,
        "treatment": treatment,
    }


def produce_sweep_resource_evidence(
    args: argparse.Namespace, telemetry_path: Path
) -> Path | None:
    if args.mode != "sweep":
        return None
    output = args.artifact_dir / "sweep-resource-usage.json"
    atomic_write_json(
        output,
        build_sweep_resource_evidence(
            run_id=args.run_id,
            telemetry_path=telemetry_path,
            client_before_path=args.client_before,
            client_after_path=args.client_after,
            server_before_path=args.server_evidence_before,
            server_after_path=args.server_evidence_after,
        ),
    )
    return output


def run(argv: list[str] | None = None) -> int:
    process: subprocess.Popen[bytes] | None = None
    try:
        args = parse_args(argv)
        validate(args)
        args.artifact_dir.mkdir(parents=True)
        os.chmod(args.artifact_dir, 0o700)
        binary = Path(os.environ.get("H3FUZZ_BIN", str(REPO_ROOT / "h3fuzz"))).resolve()
        if not binary.is_file() or not os.access(binary, os.X_OK):
            raise ContractError(f"h3fuzz binary is not executable: {binary}")
        phase_file = args.artifact_dir / "phase.jsonl"
        telemetry = args.artifact_dir / "run-telemetry.json"
        debug = args.artifact_dir / "h3fuzz-debug.log"
        command, response_mode = binary_command(args, binary, phase_file, telemetry, debug)
        atomic_write_json(args.artifact_dir / "argv.json", {"argv": command})
        environment = {
            key: value for key, value in os.environ.items()
            if key.startswith("H3FUZZ_")
        }
        atomic_write_json(args.artifact_dir / "environment-overrides.json", environment)

        provider(args.server_snapshot_provider, "before", args.run_id, args.server_counters_before, "origin before snapshot")
        provider(args.server_evidence_provider, "stable", args.run_id, args.server_stable, "server stable evidence")
        provider(args.server_evidence_provider, "before", args.run_id, args.server_evidence_before, "server before evidence")

        metrics_before = args.artifact_dir / "metrics-before.json"
        metrics_after = args.artifact_dir / "metrics-after.json"
        call([sys.executable, str(SCRIPT_DIR / "metrics-snapshot.py"), os.environ.get("H3FUZZ_METRICS_URL", "http://127.0.0.1:2019/metrics"), str(metrics_before)], label="metrics before")
        stdout = (args.artifact_dir / "h3fuzz.stdout").open("wb")
        stderr = (args.artifact_dir / "h3fuzz.stderr").open("wb")
        start_ns = time.monotonic_ns()
        process = subprocess.Popen(command, stdout=stdout, stderr=stderr)
        before_done = False
        after_done = False
        runtime_written = False
        while process.poll() is None:
            phase = last_phase(phase_file)
            if not before_done and phase in {"warmup", "qualification", "main"}:
                before_done = snapshot_point(args.client_before, "before", args.run_id, process.pid)
            if phase == "main":
                runtime_written = runtime_sample(
                    args.client_runtime_sockets, "main", args.run_id, process.pid, runtime_written
                ) or runtime_written
            if not after_done and phase == "drain":
                after_done = snapshot_point(args.client_after, "after", args.run_id, process.pid)
                runtime_written = runtime_sample(
                    args.client_runtime_sockets, "drain", args.run_id, process.pid, runtime_written
                ) or runtime_written
            time.sleep(0.05)
        status = process.wait()
        process = None
        end_ns = time.monotonic_ns()
        stdout.close()
        stderr.close()

        provider(args.server_snapshot_provider, "after", args.run_id, args.server_counters_after, "origin after snapshot")
        provider(args.server_evidence_provider, "after", args.run_id, args.server_evidence_after, "server after evidence")
        call([sys.executable, str(SCRIPT_DIR / "metrics-snapshot.py"), os.environ.get("H3FUZZ_METRICS_URL", "http://127.0.0.1:2019/metrics"), str(metrics_after)], label="metrics after")
        if status != 0:
            raise OperationalError(f"h3fuzz exited with status {status}")
        if not before_done or not after_done or not runtime_written:
            raise OperationalError("live client UDP socket evidence was incomplete")

        client_network = args.artifact_dir / "client-network-delta.json"
        server_network = args.artifact_dir / "server-network-delta.json"
        compare_network(args.client_before, args.client_after, client_network)
        compare_network(args.server_evidence_before, args.server_evidence_after, server_network)

        sweep_resource_evidence = produce_sweep_resource_evidence(args, telemetry)

        client_stable = args.artifact_dir / "client-stable.json"
        call([
            str(SCRIPT_DIR / "environment-snapshot.sh"), "--kind", "client_stable",
            "--output", str(client_stable), "--source-root", str(REPO_ROOT), "--binary", str(binary),
            "--command-json", str(args.artifact_dir / "argv.json"),
            "--environment-json", str(args.artifact_dir / "environment-overrides.json"),
            "--debug-log", str(debug), "--target", args.target, "--wordlist", str(args.wordlist),
            "--repeat-mode", "frozen-exact",
            *(["--harness-image", os.environ["H3FUZZ_HARNESS_IMAGE"]] if os.environ.get("H3FUZZ_HARNESS_IMAGE") else []),
        ], label="client stable evidence")
        client_stable_data = read_json(client_stable)
        server_stable_data = read_json(args.server_stable)
        metadata = build_metadata(args, client_stable_data, server_stable_data, binary)
        comparison_path = args.artifact_dir / "comparison-metadata.json"
        atomic_write_json(comparison_path, metadata)

        telemetry_data = read_json(telemetry)
        expected_attempts = telemetry_data.get("transport", {}).get("application_attempts")
        if not isinstance(expected_attempts, int) or expected_attempts < 0:
            raise OperationalError("telemetry did not provide application_attempts for origin correlation")
        correlation = args.artifact_dir / "origin-correlation.json"
        call([
            sys.executable, str(SCRIPT_DIR / "origin_snapshot.py"), "correlate",
            "--before", str(args.server_counters_before), "--after", str(args.server_counters_after),
            "--run-id", args.run_id, "--route", route_name(args.target), "--protocol", "h3",
            "--expected-handler-requests", str(expected_attempts), "--output", str(correlation),
        ], allowed=(0, 3), label="origin correlation")

        report_command = [
            sys.executable, str(SCRIPT_DIR / "benchmark-report.py"), "--wordlist", str(args.wordlist),
            "--telemetry", str(telemetry), "--before", str(metrics_before), "--after", str(metrics_after),
            "--start-ns", str(start_ns), "--end-ns", str(end_ns), "--exit-code", str(status),
            "--target", args.target, "--mode", response_mode, "--expect-strategy", args.expect_strategy.upper(),
            "--connections", str(args.connections or 0), "--max-connections", str(args.max_connections or args.connections or 0),
            "--aggregation-mode", args.mode, "--comparison-metadata", str(comparison_path),
            "--origin-correlation", str(correlation), "--network-evidence", str(client_network),
            "--network-evidence", str(server_network), "--output", str(args.artifact_dir / "benchmark-report.json"),
            "--require-run-valid", "--require-aggregation-eligible",
        ]
        if sweep_resource_evidence is not None:
            report_command.extend(("--sweep-resource-evidence", str(sweep_resource_evidence)))
        return call(report_command, allowed=(0, 3), label="benchmark report")
    except ContractError as exc:
        print(f"benchmark: {exc}", file=sys.stderr)
        return 2
    except OperationalError as exc:
        print(f"benchmark: {exc}", file=sys.stderr)
        return 4
    except Exception as exc:
        print(f"benchmark: unexpected error: {exc}", file=sys.stderr)
        return 1
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


if __name__ == "__main__":
    raise SystemExit(run())
