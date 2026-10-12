#!/usr/bin/env python3
"""Strict PERF-004 certification orchestration backend."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
import importlib.util

from perf004_contract import (  # noqa: E402
    ContractError,
    add_manifest_hash,
    atomic_write_json,
    file_sha256,
    read_json,
    utc_now,
)


CERTIFICATION_SCRIPT = Path(__file__).with_name("certification-report.py")
SPEC = importlib.util.spec_from_file_location("certification_report_backend", CERTIFICATION_SCRIPT)
certification_report = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(certification_report)

SENSITIVE = re.compile(
    r"(?i)(authorization|cookie|credential|password|passwd|secret|token|api[-_]?key|private[-_]?key|proxy)"
)
ARCHIVE_NAMES = {
    "h3fuzz-certification-raw.tar.zst",
    "h3fuzz-certification-raw.tar.zst.sha256",
    "h3fuzz-certification-public.tar.zst",
    "h3fuzz-certification-public.tar.zst.sha256",
}


class OperationalFailure(RuntimeError):
    pass


class RetentionFailure(RuntimeError):
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
    parser.add_argument("--wordlist-manifest", type=Path, required=True)
    parser.add_argument("--extensions", default="")
    parser.add_argument("--expect-strategy", choices=("head", "range", "get"), required=True)
    parser.add_argument("--filter-status")
    parser.add_argument("--filter-size")
    parser.add_argument("--fixed-sweep-manifest", type=Path, required=True)
    parser.add_argument("--files-validation-manifest", type=Path, required=True)
    parser.add_argument("--diagnostics-manifest", type=Path, required=True)
    parser.add_argument("--target-rps", type=positive, required=True)
    parser.add_argument("--server-snapshot-provider", type=Path, required=True)
    parser.add_argument("--server-evidence-provider", type=Path, required=True)
    parser.add_argument("--artifact-store", type=Path, required=True)
    return parser.parse_args(argv)


def absolute(path: Path, option: str, *, must_exist: bool = True) -> Path:
    if not path.is_absolute():
        raise ContractError(f"{option} must be an absolute path")
    result = path.resolve()
    if must_exist and not result.exists():
        raise ContractError(f"{option} does not exist: {result}")
    return result


def within(path: Path, parent: Path) -> bool:
    try:
        path.resolve().relative_to(parent.resolve())
        return True
    except ValueError:
        return False


def validate_args(args: argparse.Namespace) -> None:
    if not args.target.startswith("https://") or "FUZZ" not in args.target:
        raise ContractError("--target must be an HTTPS URL containing FUZZ")
    if args.target_rps != 100_000:
        raise ContractError("contract version 1 requires --target-rps 100000")
    for field in (
        "wordlist", "wordlist_manifest", "fixed_sweep_manifest",
        "files_validation_manifest", "diagnostics_manifest",
        "server_snapshot_provider", "server_evidence_provider", "artifact_store",
    ):
        setattr(args, field, absolute(getattr(args, field), f"--{field.replace('_', '-')}"))
    if not args.artifact_store.is_dir():
        raise ContractError("--artifact-store must be an existing durable directory")
    harness_work = Path(__file__).resolve().parent.parent / ".work"
    if within(args.artifact_store, harness_work):
        raise ContractError("--artifact-store must be outside harness/.work")
    for provider in (args.server_snapshot_provider, args.server_evidence_provider):
        if not provider.is_file() or not os.access(provider, os.X_OK):
            raise ContractError(f"provider is not executable: {provider}")


def copy_prerequisite_tree(source_manifest: Path, destination: Path) -> None:
    source_root = source_manifest.parent
    if destination.exists():
        raise RetentionFailure(f"destination already exists: {destination}")
    shutil.copytree(source_root, destination, symlinks=False)


def run_benchmark(
    args: argparse.Namespace,
    stage: Path,
    cohort: str,
    index: int,
    selection: dict[str, int],
) -> Path:
    run_dir = stage / "cohorts" / cohort / f"run-{index:02d}"
    benchmark = Path(
        os.environ.get("H3FUZZ_BENCHMARK_SCRIPT", str(Path(__file__).with_name("benchmark.sh")))
    )
    run_id = str(uuid.uuid4())
    command = [
        str(benchmark),
        "--target", args.target,
        "--wordlist", str(args.wordlist),
        "--mode", cohort,
        "--expect-strategy", args.expect_strategy,
        "--streams", str(selection["streams"]),
        "--max-workers", str(selection["adaptive_max_workers"]),
        "--run-id", run_id,
        "--server-snapshot-provider", str(args.server_snapshot_provider),
        "--server-evidence-provider", str(args.server_evidence_provider),
        "--client-before", str(run_dir / "client-before.json"),
        "--client-after", str(run_dir / "client-after.json"),
        "--client-runtime-sockets", str(run_dir / "client-runtime-sockets.jsonl"),
        "--server-counters-before", str(run_dir / "server-counters-before.json"),
        "--server-counters-after", str(run_dir / "server-counters-after.json"),
        "--server-stable", str(run_dir / "server-stable.json"),
        "--server-evidence-before", str(run_dir / "server-evidence-before.json"),
        "--server-evidence-after", str(run_dir / "server-evidence-after.json"),
        "--artifact-dir", str(run_dir),
        "--target-rps", str(args.target_rps),
    ]
    if cohort == "adaptive":
        command.extend(("--max-connections", str(selection["adaptive_max_connections"])))
    else:
        command.extend(("--connections", str(selection["connections"])))
    if args.extensions:
        command.extend(("--extensions", args.extensions))
    if args.filter_status:
        command.extend(("--filter-status", args.filter_status))
    if args.filter_size:
        command.extend(("--filter-size", args.filter_size))
    run_dir.parent.mkdir(parents=True, exist_ok=True)
    completed = subprocess.run(command, check=False)
    if completed.returncode != 0:
        raise OperationalFailure(
            f"{cohort} run {index} failed with benchmark exit {completed.returncode}"
        )
    report_path = run_dir / "benchmark-report.json"
    if not report_path.is_file():
        raise OperationalFailure(f"{cohort} run {index} did not produce benchmark-report.json")
    report = read_json(report_path)
    span = report.get("main_request_span_seconds")
    if not isinstance(span, (int, float)) or span < 30:
        atomic_write_json(run_dir / "generation-invalidation.json", {
            "schema_version": 1,
            "reason": "main_request_span_below_30_seconds",
            "cohort": cohort,
            "run": index,
            "observed_main_request_span_seconds": span,
            "required_main_request_span_seconds": 30,
            "operator_action": "recalibrate a larger frozen wordlist and restart every measured trial",
        })
    return report_path


def run_alternating_cohorts(
    args: argparse.Namespace,
    stage: Path,
    selection: dict[str, int],
    *,
    runner=run_benchmark,
) -> tuple[list[Path], list[Path], list[dict[str, object]]]:
    """Run paired adaptive/fixed trials in an explicit drift-resistant order."""
    adaptive_reports: list[Path] = []
    fixed_reports: list[Path] = []
    execution_order: list[dict[str, object]] = []
    cohorts = (
        ("adaptive", adaptive_reports),
        ("selected-fixed", fixed_reports),
    )
    for index in range(1, 6):
        for cohort, reports in cohorts:
            report_path = runner(args, stage, cohort, index, selection)
            reports.append(report_path)
            execution_order.append({
                "sequence": len(execution_order) + 1,
                "cohort": cohort,
                "run": index,
                "path": str(report_path.relative_to(stage)),
            })
    return adaptive_reports, fixed_reports, execution_order


def redact(value: object, key: str = "") -> object:
    if SENSITIVE.search(key):
        return "[REDACTED]"
    if isinstance(value, dict):
        return {name: redact(child, name) for name, child in value.items()}
    if isinstance(value, list):
        return [redact(child, key) for child in value]
    if isinstance(value, str) and (
        re.search(r"(?i)bearer\s+[A-Za-z0-9._~+/-]+", value)
        or re.search(r"(?i)(token|password|secret|credential)=([^&\s]+)", value)
    ):
        return "[REDACTED]"
    return value


def build_public_tree(raw: Path, public: Path) -> None:
    allowed_exact = {
        Path("aggregate/certification-report.json"),
        Path("aggregate/certification-report.md"),
        Path("environment/client-stable.json"),
        Path("environment/server-stable.json"),
    }
    for path in raw.rglob("*"):
        if not path.is_file():
            continue
        relative = path.relative_to(raw)
        allowed = (
            relative in allowed_exact
            or path.name == "benchmark-report.json"
            or ("imports" in relative.parts and path.suffix == ".json" and "manifest" in path.name)
        )
        if not allowed:
            continue
        destination = public / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        if path.suffix == ".json":
            atomic_write_json(destination, redact(read_json(path)))
        else:
            text = path.read_text(encoding="utf-8")
            destination.write_text(text, encoding="utf-8")


def tree_manifest(root: Path, manifest_relative: Path, kind: str, artifact_uri: str) -> dict:
    members = []
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        relative = path.relative_to(root)
        if relative == manifest_relative or path.name in ARCHIVE_NAMES:
            continue
        members.append({"path": relative.as_posix(), "sha256": file_sha256(path), "size": path.stat().st_size})
    return add_manifest_hash({
        "schema_version": 1,
        "manifest_type": kind,
        "created_at": utc_now(),
        "artifact_uri": artifact_uri,
        "members": members,
    })


def create_zstd_archive(source: Path, destination: Path) -> None:
    tar = shutil.which("tar")
    zstd = shutil.which("zstd")
    if not tar or not zstd:
        raise RetentionFailure("tar and zstd are required for certification retention")
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary_parent = source.parent if within(destination, source) else destination.parent
    temporary = temporary_parent / f".{destination.name}.{uuid.uuid4().hex}.tmp"
    try:
        with temporary.open("wb") as output:
            tar_process = subprocess.Popen(
                [tar, "-C", str(source), "-cf", "-", "."], stdout=subprocess.PIPE
            )
            assert tar_process.stdout is not None
            zstd_process = subprocess.Popen(
                [zstd, "-q", "-T0", "-19", "-c"],
                stdin=tar_process.stdout,
                stdout=output,
            )
            tar_process.stdout.close()
            zstd_status = zstd_process.wait()
            tar_status = tar_process.wait()
        if tar_status != 0 or zstd_status != 0:
            raise RetentionFailure("tar/zstd archive creation failed")
        os.chmod(temporary, 0o600)
        os.replace(temporary, destination)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def write_sha256(path: Path) -> None:
    digest_path = path.with_name(path.name + ".sha256")
    digest_path.write_text(f"{file_sha256(path)}  {path.name}\n", encoding="ascii")
    os.chmod(digest_path, 0o600)


def retain_bundle(stage: Path, public: Path, store: Path, certification_id: str) -> Path:
    raw_archive = stage / "h3fuzz-certification-raw.tar.zst"
    public_archive = stage / "h3fuzz-certification-public.tar.zst"
    create_zstd_archive(stage, raw_archive)
    create_zstd_archive(public, public_archive)
    write_sha256(raw_archive)
    write_sha256(public_archive)
    for path in stage.rglob("*"):
        os.chmod(path, 0o700 if path.is_dir() else 0o600)
    final = store / certification_id
    if final.exists():
        raise RetentionFailure(f"immutable certification directory already exists: {final}")
    temporary = store / f".{certification_id}.{uuid.uuid4().hex}.tmp"
    try:
        shutil.copytree(stage, temporary)
        os.replace(temporary, final)
    finally:
        if temporary.exists():
            shutil.rmtree(temporary)
    for archive in (final / raw_archive.name, final / public_archive.name):
        expected = archive.with_name(archive.name + ".sha256").read_text(encoding="ascii").split()[0]
        if file_sha256(archive) != expected:
            raise RetentionFailure(f"retained archive hash mismatch: {archive}")
    return final


def run(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(argv)
        validate_args(args)
        stage_text = os.environ.get("H3FUZZ_CERTIFICATION_STAGING")
        if not stage_text:
            raise ContractError("certify.sh must provide a staging directory")
        stage = Path(stage_text).resolve()
        if not stage.is_dir() or not within(stage, Path(__file__).resolve().parent.parent / ".work"):
            raise ContractError("staging directory must exist under harness/.work")

        preflight_namespace = argparse.Namespace(
            wordlist=args.wordlist,
            wordlist_manifest=args.wordlist_manifest,
            fixed_sweep_manifest=args.fixed_sweep_manifest,
            files_validation_manifest=args.files_validation_manifest,
            diagnostics_manifest=args.diagnostics_manifest,
            target_rps=args.target_rps,
        )
        prerequisites = certification_report.verify_prerequisites(preflight_namespace)
        selection = prerequisites["selection"]
        certification_id = f"certification-{utc_now().replace(':', '').replace('-', '')}-{uuid.uuid4().hex[:12]}"
        artifact_uri = f"file://{args.artifact_store / certification_id}"

        copy_prerequisite_tree(args.fixed_sweep_manifest, stage / "imports" / "fixed-sweep")
        copy_prerequisite_tree(args.files_validation_manifest, stage / "imports" / "files-validation")
        copy_prerequisite_tree(args.diagnostics_manifest, stage / "imports" / "diagnostics")
        shutil.copy2(args.wordlist_manifest, stage / "imports" / "wordlist-manifest.json")

        adaptive_reports, fixed_reports, execution_order = run_alternating_cohorts(
            args, stage, selection
        )
        report = certification_report.evaluate_certification(
            [read_json(path) for path in adaptive_reports],
            [read_json(path) for path in fixed_reports],
            target_rps=args.target_rps,
            prerequisites={name: prerequisites[name] for name in (
                "selection", "fixed_sweep", "files_validation", "diagnostics", "wordlist"
            )},
        )
        report.update(
            created_at=utc_now(), certification_id=certification_id, artifact_uri=artifact_uri,
            cohort_execution_order=execution_order,
            source_reports={
                "adaptive": [{"path": str(path.relative_to(stage)), "sha256": file_sha256(path)} for path in adaptive_reports],
                "selected_fixed": [{"path": str(path.relative_to(stage)), "sha256": file_sha256(path)} for path in fixed_reports],
            },
        )
        report = add_manifest_hash(report)
        aggregate = stage / "aggregate"
        atomic_write_json(aggregate / "certification-report.json", report)
        (aggregate / "certification-report.md").write_text(
            certification_report.markdown_report(report), encoding="utf-8"
        )

        public_root = stage.parent / f"{stage.name}-public"
        build_public_tree(stage, public_root)
        public_manifest_path = public_root / "manifests" / "publishable-manifest.json"
        atomic_write_json(
            public_manifest_path,
            tree_manifest(public_root, Path("manifests/publishable-manifest.json"), "h3fuzz-publishable-artifact-manifest", artifact_uri),
        )
        shutil.copy2(public_manifest_path, stage / "manifests" / "publishable-manifest.json")
        raw_manifest_path = stage / "manifests" / "raw-manifest.json"
        atomic_write_json(
            raw_manifest_path,
            tree_manifest(stage, Path("manifests/raw-manifest.json"), "h3fuzz-raw-artifact-manifest", artifact_uri),
        )
        final = retain_bundle(stage, public_root, args.artifact_store, certification_id)
        shutil.rmtree(public_root)
        print(json.dumps({
            "certification_eligible": report["certification_eligible"],
            "artifact_directory": str(final),
            "artifact_uri": artifact_uri,
        }, sort_keys=True))
        return 0 if report["certification_eligible"] else 3
    except ContractError as exc:
        print(f"certify: {exc}", file=sys.stderr)
        return 2
    except OperationalFailure as exc:
        print(f"certify: {exc}", file=sys.stderr)
        return 4
    except RetentionFailure as exc:
        print(f"certify: {exc}", file=sys.stderr)
        return 5
    except Exception as exc:
        print(f"certify: unexpected error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(run())
