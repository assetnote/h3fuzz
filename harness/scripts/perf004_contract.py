#!/usr/bin/env python3
"""Shared, versioned PERF-004 artifact helpers.

The helpers deliberately use canonical JSON for identities and write manifests
with create-only atomic replacement.  A manifest's ``manifest_sha256`` covers
the complete JSON object with only that field removed.
"""

from __future__ import annotations

import copy
import datetime as dt
import hashlib
import json
import math
import os
from pathlib import Path
import tempfile
from typing import Any, Iterable


HEX64 = set("0123456789abcdef")


class ContractError(ValueError):
    pass


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def canonical_bytes(value: object) -> bytes:
    return json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")


def canonical_hash(value: object) -> str:
    return hashlib.sha256(canonical_bytes(value)).hexdigest()


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise ContractError(f"cannot read {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise ContractError(f"invalid JSON in {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ContractError(f"{path}: top-level JSON value must be an object")
    return value


def add_manifest_hash(value: dict[str, Any]) -> dict[str, Any]:
    result = copy.deepcopy(value)
    result.pop("manifest_sha256", None)
    result["manifest_sha256"] = canonical_hash(result)
    return result


def verify_manifest_hash(value: dict[str, Any], label: str) -> None:
    actual = value.get("manifest_sha256")
    body = copy.deepcopy(value)
    body.pop("manifest_sha256", None)
    expected = canonical_hash(body)
    if actual != expected:
        raise ContractError(f"{label}: manifest_sha256 mismatch")


def atomic_write_json(path: Path, value: dict[str, Any], *, refuse_existing: bool = True) -> None:
    path = path.resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    if refuse_existing and path.exists():
        raise ContractError(f"refusing to overwrite immutable artifact: {path}")
    data = json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2, sort_keys=True) + "\n"
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, 0o600)
        if refuse_existing:
            try:
                os.link(temporary, path)
            except FileExistsError as exc:
                raise ContractError(f"refusing to overwrite immutable artifact: {path}") from exc
            temporary.unlink()
        else:
            os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def finite_number(value: object, label: str, *, minimum: float | None = None) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ContractError(f"{label}: expected a finite number")
    result = float(value)
    if not math.isfinite(result):
        raise ContractError(f"{label}: expected a finite number")
    if minimum is not None and result < minimum:
        raise ContractError(f"{label}: expected at least {minimum}")
    return result


def integer(value: object, label: str, *, minimum: int = 0) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
        raise ContractError(f"{label}: expected an integer >= {minimum}")
    return value


def verify_relative_members(manifest: dict[str, Any], manifest_path: Path) -> list[Path]:
    members = manifest.get("members")
    if not isinstance(members, list) or not members:
        raise ContractError(f"{manifest_path}: nonempty members list is required")
    root = manifest_path.resolve().parent
    verified: list[Path] = []
    seen: set[str] = set()
    for index, member in enumerate(members):
        if not isinstance(member, dict):
            raise ContractError(f"{manifest_path}: members[{index}] must be an object")
        relative = member.get("path")
        expected_hash = member.get("sha256")
        if not isinstance(relative, str) or not relative or Path(relative).is_absolute():
            raise ContractError(f"{manifest_path}: members[{index}].path must be relative")
        if relative in seen:
            raise ContractError(f"{manifest_path}: duplicate member path {relative}")
        seen.add(relative)
        candidate = (root / relative).resolve()
        try:
            candidate.relative_to(root)
        except ValueError as exc:
            raise ContractError(f"{manifest_path}: member escapes artifact root: {relative}") from exc
        if not candidate.is_file():
            raise ContractError(f"{manifest_path}: missing member {relative}")
        if not isinstance(expected_hash, str) or len(expected_hash) != 64:
            raise ContractError(f"{manifest_path}: invalid hash for {relative}")
        if file_sha256(candidate) != expected_hash:
            raise ContractError(f"{manifest_path}: hash mismatch for {relative}")
        verified.append(candidate)
    return verified


COMPARISON_COMPONENTS = ("platform", "client_build", "workload", "treatment")


def comparison_keys(components: dict[str, Any]) -> dict[str, Any]:
    result: dict[str, Any] = {"schema_version": 1}
    for component in COMPARISON_COMPONENTS:
        value = components.get(component)
        if not isinstance(value, dict):
            raise ContractError(f"comparison component {component} must be an object")
        result[component] = copy.deepcopy(value)
        result[f"{component}_key" if component != "client_build" else "client_build_key"] = canonical_hash(value)
    key_set = {
        "platform_key": result["platform_key"],
        "client_build_key": result["client_build_key"],
        "workload_key": result["workload_key"],
        "treatment_key": result["treatment_key"],
    }
    result["cohort_key"] = canonical_hash(key_set)
    return result


def comparison_key_errors(keys: object) -> list[str]:
    if not isinstance(keys, dict):
        return ["comparison_keys_missing"]
    errors: list[str] = []
    expected: dict[str, str] = {}
    for component in COMPARISON_COMPONENTS:
        value = keys.get(component)
        field = f"{component}_key" if component != "client_build" else "client_build_key"
        if not isinstance(value, dict):
            errors.append(f"{component}_component_missing")
            continue
        expected[field] = canonical_hash(value)
        if keys.get(field) != expected[field]:
            errors.append(f"{field}_mismatch")
    if len(expected) == 4:
        cohort = canonical_hash(expected)
        if keys.get("cohort_key") != cohort:
            errors.append("cohort_key_mismatch")
    return errors


def summarize_numbers(values: Iterable[float]) -> dict[str, float | int | None]:
    from statistics import median, pstdev

    samples = list(values)
    if not samples:
        return {"count": 0, "minimum": None, "median": None, "maximum": None, "standard_deviation": None}
    return {
        "count": len(samples),
        "minimum": min(samples),
        "median": median(samples),
        "maximum": max(samples),
        "standard_deviation": pstdev(samples),
    }
