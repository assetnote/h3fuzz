#!/usr/bin/env python3
"""Shared, fail-closed evidence helpers for the PERF-503 harness tools."""

from __future__ import annotations

import copy
import json
import math
import os
from pathlib import Path
import re
from typing import Any

from perf004_contract import (
    ContractError,
    add_manifest_hash,
    atomic_write_json,
    comparison_key_errors,
    file_sha256,
    read_json,
    verify_manifest_hash,
    verify_relative_members,
)


SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
PROFILES = ("cpu", "heap", "mutex", "block", "quic")


def require_sha256(value: object, label: str) -> str:
    if not isinstance(value, str) or SHA256_RE.fullmatch(value) is None:
        raise ContractError(f"{label}: lowercase SHA-256 is required")
    return value


def require_absolute(path: Path, label: str, *, exists: bool = False) -> Path:
    if not path.is_absolute():
        raise ContractError(f"{label} must be an absolute path")
    result = path.resolve()
    if exists and not result.exists():
        raise ContractError(f"{label} does not exist: {result}")
    return result


def require_executable(path: Path, label: str) -> Path:
    result = require_absolute(path, label, exists=True)
    if not result.is_file() or not os.access(result, os.X_OK):
        raise ContractError(f"{label} is not executable: {result}")
    return result


def artifact_reference(path: Path, root: Path) -> dict[str, str]:
    root = root.resolve()
    resolved = path.resolve()
    try:
        relative = resolved.relative_to(root).as_posix()
    except ValueError as exc:
        raise ContractError(f"artifact escapes manifest root: {resolved}") from exc
    if not resolved.is_file() or resolved.is_symlink():
        raise ContractError(f"artifact must be a regular non-symlink file: {resolved}")
    return {"path": relative, "sha256": file_sha256(resolved)}


def inventory(root: Path, manifest_path: Path) -> list[dict[str, Any]]:
    root = root.resolve()
    manifest_path = manifest_path.resolve()
    result: list[dict[str, Any]] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.resolve() == manifest_path:
            continue
        if path.is_symlink():
            raise ContractError(f"artifact tree contains a symlink: {path}")
        result.append(
            {
                "path": path.relative_to(root).as_posix(),
                "sha256": file_sha256(path),
                "size_bytes": path.stat().st_size,
            }
        )
    if not result:
        raise ContractError("artifact tree must contain at least one member")
    return result


def _resolve_pointer(schema: dict[str, Any], pointer: str) -> object:
    if not pointer.startswith("#/"):
        raise ContractError(f"unsupported schema reference: {pointer}")
    value: object = schema
    for raw in pointer[2:].split("/"):
        token = raw.replace("~1", "/").replace("~0", "~")
        if not isinstance(value, dict) or token not in value:
            raise ContractError(f"unresolved schema reference: {pointer}")
        value = value[token]
    return value


def _type_matches(value: object, expected: str) -> bool:
    if expected == "object":
        return isinstance(value, dict)
    if expected == "array":
        return isinstance(value, list)
    if expected == "string":
        return isinstance(value, str)
    if expected == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if expected == "number":
        return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(float(value))
    if expected == "boolean":
        return isinstance(value, bool)
    if expected == "null":
        return value is None
    raise ContractError(f"unsupported schema type: {expected}")


def _validate_node(value: object, node: object, root: dict[str, Any], label: str) -> None:
    if not isinstance(node, dict):
        raise ContractError(f"{label}: malformed schema node")
    if "$ref" in node:
        target = _resolve_pointer(root, node["$ref"])
        _validate_node(value, target, root, label)
        return
    if "const" in node and value != node["const"]:
        raise ContractError(f"{label}: expected constant {node['const']!r}")
    if "enum" in node and value not in node["enum"]:
        raise ContractError(f"{label}: value is outside the allowed enumeration")
    expected_type = node.get("type")
    if expected_type is not None:
        candidates = expected_type if isinstance(expected_type, list) else [expected_type]
        if not any(_type_matches(value, candidate) for candidate in candidates):
            raise ContractError(f"{label}: expected schema type {expected_type!r}")
    if isinstance(value, dict):
        required = node.get("required", [])
        for key in required:
            if key not in value:
                raise ContractError(f"{label}: missing required property {key!r}")
        properties = node.get("properties", {})
        if node.get("additionalProperties") is False:
            extras = sorted(set(value) - set(properties))
            if extras:
                raise ContractError(f"{label}: unexpected properties {extras!r}")
        for key, child in properties.items():
            if key in value:
                _validate_node(value[key], child, root, f"{label}.{key}")
    if isinstance(value, list):
        if len(value) < int(node.get("minItems", 0)):
            raise ContractError(f"{label}: too few items")
        if "maxItems" in node and len(value) > int(node["maxItems"]):
            raise ContractError(f"{label}: too many items")
        if node.get("uniqueItems") is True:
            encoded = [json.dumps(item, sort_keys=True, separators=(",", ":")) for item in value]
            if len(encoded) != len(set(encoded)):
                raise ContractError(f"{label}: duplicate array items")
        if "items" in node:
            for index, item in enumerate(value):
                _validate_node(item, node["items"], root, f"{label}[{index}]")
    if isinstance(value, str):
        if len(value) < int(node.get("minLength", 0)):
            raise ContractError(f"{label}: string is too short")
        pattern = node.get("pattern")
        if pattern is not None and re.search(pattern, value) is None:
            raise ContractError(f"{label}: string does not match {pattern!r}")
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if "minimum" in node and value < node["minimum"]:
            raise ContractError(f"{label}: value is below the minimum")
        if "exclusiveMinimum" in node and value <= node["exclusiveMinimum"]:
            raise ContractError(f"{label}: value is not above the exclusive minimum")


def validate_schema(value: object, schema_path: Path) -> None:
    schema = read_json(schema_path)
    _validate_node(value, schema, schema, str(schema_path))


def member_index(manifest: dict[str, Any]) -> dict[str, dict[str, Any]]:
    members = manifest.get("members")
    if not isinstance(members, list):
        raise ContractError("manifest members must be an array")
    return {
        member["path"]: member
        for member in members
        if isinstance(member, dict) and isinstance(member.get("path"), str)
    }


def require_reference(manifest: dict[str, Any], reference: object, label: str) -> Path:
    if not isinstance(reference, dict) or set(reference) != {"path", "sha256"}:
        raise ContractError(f"{label}: exact path/hash reference is required")
    path = reference.get("path")
    digest = require_sha256(reference.get("sha256"), f"{label}.sha256")
    if not isinstance(path, str):
        raise ContractError(f"{label}.path must be a string")
    member = member_index(manifest).get(path)
    if not member or member.get("sha256") != digest:
        raise ContractError(f"{label}: reference is absent from members")
    return Path(path)


def verify_tree(manifest_path: Path, schema_path: Path) -> dict[str, Any]:
    manifest_path = manifest_path.resolve()
    manifest = read_json(manifest_path)
    validate_schema(manifest, schema_path)
    verify_manifest_hash(manifest, str(manifest_path))
    verified = verify_relative_members(manifest, manifest_path)
    root = manifest_path.parent
    recorded = {member["path"] for member in manifest["members"]}
    actual: set[str] = set()
    for candidate in root.rglob("*"):
        if not candidate.is_file() or candidate.resolve() == manifest_path:
            continue
        if candidate.is_symlink():
            raise ContractError(f"{manifest_path}: symlink member is forbidden: {candidate}")
        actual.add(candidate.relative_to(root).as_posix())
    if recorded != actual:
        raise ContractError(
            f"{manifest_path}: member tree mismatch "
            f"(missing={sorted(recorded - actual)}, extra={sorted(actual - recorded)})"
        )
    by_path = member_index(manifest)
    for candidate in verified:
        relative = candidate.relative_to(root).as_posix()
        if by_path[relative].get("size_bytes") != candidate.stat().st_size:
            raise ContractError(f"{manifest_path}: size mismatch for {relative}")
    return manifest


def seal_manifest(body: dict[str, Any], manifest_path: Path, schema_path: Path) -> dict[str, Any]:
    manifest_path = manifest_path.resolve()
    if manifest_path.exists():
        raise ContractError(f"refusing to overwrite immutable manifest: {manifest_path}")
    root = manifest_path.parent
    body = copy.deepcopy(body)
    body["members"] = inventory(root, manifest_path)
    manifest = add_manifest_hash(body)
    validate_schema(manifest, schema_path)
    atomic_write_json(manifest_path, manifest)
    verify_tree(manifest_path, schema_path)
    return manifest


def profile_flags(report: dict[str, Any]) -> list[str] | None:
    keys = report.get("comparison_keys")
    if not isinstance(keys, dict):
        return None
    treatment = keys.get("treatment")
    if not isinstance(treatment, dict):
        return None
    value = treatment.get("profile_flags")
    return value if isinstance(value, list) and all(isinstance(item, str) for item in value) else None


def comparison_value(report: dict[str, Any], field: str, label: str) -> str:
    keys = report.get("comparison_keys")
    if not isinstance(keys, dict):
        raise ContractError(f"{label}: comparison_keys are missing")
    errors = comparison_key_errors(keys)
    if errors:
        raise ContractError(f"{label}: invalid comparison keys: {','.join(errors)}")
    return require_sha256(keys.get(field), f"{label}.{field}")


def verify_files_manifest(manifest_path: Path, schema_path: Path) -> dict[str, Any]:
    manifest = verify_tree(manifest_path, schema_path)
    if manifest.get("eligible") is not True or manifest.get("ineligible_reasons") != []:
        raise ContractError(f"{manifest_path}: files validation is not eligible")
    correctness = manifest["correctness"]
    if not (
        correctness["passed"] is True
        and correctness["expected_hits"] > 0
        and correctness["actual_hits"] == correctness["expected_hits"]
        and correctness["false_positives"] == 0
        and correctness["missing_hits"] == []
        and correctness["unexpected_hits"] == []
        and correctness["filter_matches_get_control"] is True
    ):
        raise ContractError(f"{manifest_path}: correctness proof failed")
    root = manifest_path.resolve().parent
    corpus = manifest["corpus"]
    corpus_member = member_index(manifest).get(corpus["path"])
    corpus_path = root / corpus["path"]
    if corpus_member is None or corpus_member.get("sha256") != corpus["sha256"]:
        raise ContractError(f"{manifest_path}: corpus path/hash is absent from members")
    with corpus_path.open("rb") as stream:
        corpus_lines = sum(1 for _ in stream)
    if (
        corpus_lines != corpus["line_count"]
        or corpus["target_inputs"] != corpus["line_count"]
        or corpus["known_hit_count"] + corpus["unique_miss_count"]
        != corpus["line_count"]
    ):
        raise ContractError(f"{manifest_path}: corpus accounting mismatch")
    require_reference(
        manifest, correctness["comparison"], "correctness.comparison"
    )
    exact_relative = require_reference(manifest, manifest["runs"]["exact_hit"], "runs.exact_hit")
    long_relative = require_reference(manifest, manifest["runs"]["long_corpus"], "runs.long_corpus")
    if manifest["throughput"]["report"] != manifest["runs"]["long_corpus"]:
        raise ContractError(f"{manifest_path}: throughput report/run reference mismatch")
    exact = read_json(root / exact_relative)
    long_report = read_json(root / long_relative)
    for name, report in (("exact_hit", exact), ("long_corpus", long_report)):
        keys = report.get("comparison_keys")
        if not isinstance(keys, dict):
            raise ContractError(f"{manifest_path}: {name} has no comparison keys")
        errors = comparison_key_errors(keys)
        if errors:
            raise ContractError(
                f"{manifest_path}: {name} comparison keys: {','.join(errors)}"
            )
        for field in ("platform_key", "client_build_key"):
            if keys.get(field) != manifest[field]:
                raise ContractError(f"{manifest_path}: {name} {field} mismatch")
        if report.get("authority") != manifest["authority"]:
            raise ContractError(f"{manifest_path}: {name} authority mismatch")
        if report.get("server_identity") != manifest["server_identity"]:
            raise ContractError(f"{manifest_path}: {name} server identity mismatch")
    if comparison_value(long_report, "workload_key", "long_corpus") != manifest["workload_key"]:
        raise ContractError(f"{manifest_path}: long-corpus workload key mismatch")
    if long_report.get("target", "").split("?", 1)[0].endswith("/bench/FUZZ"):
        raise ContractError(f"{manifest_path}: /bench workload cannot satisfy files validation")
    if manifest["throughput"]["distinct_from_bench"] is not True:
        raise ContractError(f"{manifest_path}: files throughput is not distinguished from /bench")
    return manifest


def _diagnostic_report_valid(report: dict[str, Any], profile: str) -> bool:
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
    if report.get("aggregation_mode") != "diagnostic":
        return False
    if report.get("eligible_for_aggregation") is not False:
        return False
    if any(report.get(field) is not True for field in required_true):
        return False
    span = report.get("main_request_span_seconds")
    if (
        not isinstance(span, (int, float))
        or isinstance(span, bool)
        or not math.isfinite(float(span))
        or span < 30
    ):
        return False
    if profile == "none" and report.get("run_valid") is not True:
        return False
    return True


def verify_diagnostics_manifest(manifest_path: Path, schema_path: Path) -> dict[str, Any]:
    manifest = verify_tree(manifest_path, schema_path)
    if manifest.get("eligible") is not True or manifest.get("ineligible_reasons") != []:
        raise ContractError(f"{manifest_path}: diagnostics are not eligible")
    if set(manifest["profiles"]) != set(PROFILES):
        raise ContractError(f"{manifest_path}: exact profile set is required")
    root = manifest_path.resolve().parent
    seen_runs: set[str] = set()
    seen_reports: set[str] = set()
    for name in PROFILES:
        pair = manifest["profiles"][name]
        if pair["profile_flags"] != [name] or pair["control_profile_flags"] != []:
            raise ContractError(f"{manifest_path}: invalid flags for {name}")
        for role, expected_flags in (("profile", [name]), ("control", [])):
            run_id = pair[f"{role}_run_id"]
            if run_id in seen_runs:
                raise ContractError(f"{manifest_path}: diagnostic run reused: {run_id}")
            seen_runs.add(run_id)
            reference = pair[f"{role}_report"]
            relative = require_reference(manifest, reference, f"profiles.{name}.{role}_report")
            if relative.as_posix() in seen_reports:
                raise ContractError(f"{manifest_path}: diagnostic report reused: {relative}")
            seen_reports.add(relative.as_posix())
            report = read_json(root / relative)
            if report.get("run_id") != run_id:
                raise ContractError(f"{manifest_path}: {name} {role} run ID mismatch")
            if report.get("source_eligibility_run") is not False:
                raise ContractError(f"{manifest_path}: eligibility run reused by {name} {role}")
            if profile_flags(report) != expected_flags:
                raise ContractError(f"{manifest_path}: {name} {role} profile flags mismatch")
            if not _diagnostic_report_valid(report, name if role == "profile" else "none"):
                raise ContractError(f"{manifest_path}: invalid {name} {role} diagnostic report")
            for field in ("platform_key", "client_build_key", "workload_key"):
                if comparison_value(report, field, f"{name}.{role}") != manifest[field]:
                    raise ContractError(f"{manifest_path}: {name} {role} {field} mismatch")
            if report.get("authority") != manifest["authority"] or report.get("server_identity") != manifest["server_identity"]:
                raise ContractError(f"{manifest_path}: {name} {role} origin identity mismatch")
        artifact_relative = require_reference(
            manifest, pair["profile_artifact"], f"profiles.{name}.profile_artifact"
        )
        artifact_member = member_index(manifest)[artifact_relative.as_posix()]
        if artifact_member.get("size_bytes", 0) <= 0:
            raise ContractError(f"{manifest_path}: {name} profile artifact is empty")
        profile_report = read_json(root / Path(pair["profile_report"]["path"]))
        control_report = read_json(root / Path(pair["control_report"]["path"]))
        if comparison_value(profile_report, "treatment_key", name) != pair["profile_treatment_key"]:
            raise ContractError(f"{manifest_path}: {name} profile treatment mismatch")
        if comparison_value(control_report, "treatment_key", name) != pair["control_treatment_key"]:
            raise ContractError(f"{manifest_path}: {name} control treatment mismatch")
        if pair["profile_treatment_key"] == pair["control_treatment_key"]:
            raise ContractError(f"{manifest_path}: {name} treatment was not isolated")
        control_rps = control_report.get("completed_response_rps")
        profile_rps = profile_report.get("completed_response_rps")
        overhead = pair["overhead"]
        if not all(
            isinstance(value, (int, float)) and not isinstance(value, bool)
            for value in (control_rps, profile_rps)
        ) or control_rps <= 0 or profile_rps <= 0:
            raise ContractError(f"{manifest_path}: {name} has invalid paired rates")
        expected_delta = profile_rps - control_rps
        expected_percent = 100.0 * (control_rps - profile_rps) / control_rps
        if (
            not math.isclose(overhead["control_rps"], control_rps, rel_tol=1e-12)
            or not math.isclose(overhead["profile_rps"], profile_rps, rel_tol=1e-12)
            or not math.isclose(overhead["rps_delta"], expected_delta, rel_tol=1e-12, abs_tol=1e-12)
            or not math.isclose(
                overhead["overhead_percent"], expected_percent, rel_tol=1e-12, abs_tol=1e-12
            )
        ):
            raise ContractError(f"{manifest_path}: {name} overhead accounting mismatch")
    if len(seen_runs) != 2 * len(PROFILES):
        raise ContractError(f"{manifest_path}: every profile needs a distinct paired control")
    return manifest

