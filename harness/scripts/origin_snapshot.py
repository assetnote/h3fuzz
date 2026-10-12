#!/usr/bin/env python3
"""Capture, validate, and correlate instrumented-origin counter snapshots."""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
from pathlib import Path
import re
import tempfile
from typing import Any
import urllib.parse
import urllib.request
import sys


SCRIPT_DIR = Path(__file__).resolve().parent
HARNESS_DIR = SCRIPT_DIR.parent
SCHEMA_PATH = HARNESS_DIR / "schemas" / "origin-counter-v1.schema.json"
UUID_RE = re.compile(
    r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$",
    re.IGNORECASE,
)


class SnapshotValidationError(ValueError):
    pass


def capture_snapshot(admin_url: str, output: Path, *, timeout: float = 5.0) -> None:
    """Fetch one loopback admin snapshot and atomically install mode-0600 JSON."""

    output = Path(output)
    if not output.is_absolute():
        raise ValueError("snapshot output path must be absolute")
    parsed = urllib.parse.urlparse(admin_url)
    if parsed.scheme != "http" or not parsed.hostname or not _is_loopback_host(parsed.hostname):
        raise ValueError("admin URL must use plain HTTP on a loopback host")
    snapshot_url = admin_url.rstrip("/")
    if not snapshot_url.endswith("/admin/v1/snapshot"):
        snapshot_url += "/admin/v1/snapshot"

    request = urllib.request.Request(
        snapshot_url,
        headers={"Accept": "application/json"},
        method="GET",
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        body = response.read(64 * 1024 * 1024 + 1)
    if len(body) > 64 * 1024 * 1024:
        raise ValueError("origin snapshot exceeds 64 MiB limit")
    try:
        snapshot = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise SnapshotValidationError(f"origin returned invalid JSON: {error}") from error
    validate_snapshot(snapshot)

    _write_json_atomic(snapshot, output)


def _write_json_atomic(value: Any, output: Path) -> None:
    output = Path(output)
    if not output.is_absolute():
        raise ValueError("output path must be absolute")
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary_name: str | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            prefix=f".{output.name}.",
            suffix=".tmp",
            dir=output.parent,
            delete=False,
        ) as temporary:
            temporary_name = temporary.name
            os.chmod(temporary_name, 0o600)
            json.dump(value, temporary, sort_keys=True, separators=(",", ":"))
            temporary.write("\n")
            temporary.flush()
            os.fsync(temporary.fileno())
        os.replace(temporary_name, output)
        temporary_name = None
        os.chmod(output, 0o600)
        directory_fd = os.open(output.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if temporary_name is not None:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass


def _is_loopback_host(host: str) -> bool:
    if host.lower() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def validate_snapshot(snapshot: Any) -> None:
    """Validate the committed JSON Schema plus cross-field counter invariants."""

    schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    errors = _schema_errors(snapshot, schema, "$")
    if not errors and isinstance(snapshot, dict):
        origin = snapshot["origin"]
        configured = origin["configured_max_incoming_streams"]
        effective = origin["effective_max_incoming_streams"]
        if effective > configured:
            errors.append("$.origin.effective_max_incoming_streams exceeds configured value")

        keys: set[tuple[Any, ...]] = set()
        for index, row in enumerate(snapshot["counters"]):
            row_path = f"$.counters[{index}]"
            if row["phase"] == "main":
                if row["run_id"] is None or row["role"] is None:
                    errors.append(f"{row_path}: main rows require run_id and role")
            elif row["run_id"] is not None or row["role"] is not None:
                errors.append(f"{row_path}: unmarked rows require null run_id and role")

            attempts = row["h3_attempts"]
            rejections = row["h3_request_rejections"]
            if (attempts is None) != (rejections is None):
                errors.append(f"{row_path}: H3 transport counters must both be null or both integers")
            if attempts is not None and rejections > attempts:
                errors.append(f"{row_path}: H3 request rejections exceed attempts")
            if row["protocol"] != "h3" and (attempts is not None or rejections is not None):
                errors.append(f"{row_path}: transport counters are only valid for h3")

            key = (
                row["run_id"],
                row["phase"],
                row["route"],
                row["protocol"],
                row["method"],
                row["role"],
            )
            if key in keys:
                errors.append(f"{row_path}: duplicate counter key {key!r}")
            keys.add(key)

    if errors:
        raise SnapshotValidationError("; ".join(errors))


def correlate_snapshots(
    before: dict[str, Any],
    after: dict[str, Any],
    *,
    run_id: str,
    route: str,
    protocol: str,
    expected_handler_requests: int | None = None,
) -> dict[str, Any]:
    """Return exact counter deltas without normalizing resets or missing rows."""

    if UUID_RE.fullmatch(run_id) is None:
        raise ValueError("run_id must be a UUID")
    validate_snapshot(before)
    validate_snapshot(after)
    reasons: list[str] = []
    before_origin = before["origin"]
    after_origin = after["origin"]

    if before_origin["origin_instance_id"] != after_origin["origin_instance_id"]:
        reasons.append("origin_instance_changed")
    for field in (
        "started_at",
        "build_sha256",
        "config_sha256",
        "configured_max_incoming_streams",
        "effective_max_incoming_streams",
    ):
        if before_origin[field] != after_origin[field]:
            reasons.append(f"origin_{field}_changed")

    before_epoch = before_origin.get("counter_epoch")
    after_epoch = after_origin.get("counter_epoch")
    if (before_epoch is None) != (after_epoch is None):
        reasons.append("origin_counter_epoch_incomparable")
    elif before_epoch is not None and before_epoch != after_epoch:
        reasons.append("origin_counter_epoch_changed")

    before_rows = {_counter_key(row): row for row in before["counters"]}
    after_rows = {_counter_key(row): row for row in after["counters"]}
    deltas: list[dict[str, Any]] = []
    reset_seen = False
    for key in sorted(set(before_rows) | set(after_rows), key=_sortable_counter_key):
        old = before_rows.get(key)
        new = after_rows.get(key)
        old_handler = 0 if old is None else old["handler_requests"]
        new_handler = 0 if new is None else new["handler_requests"]
        if new_handler < old_handler:
            reset_seen = True

        template = new if new is not None else old
        assert template is not None
        attempts_delta, attempt_state = _optional_counter_delta(old, new, "h3_attempts")
        rejections_delta, rejection_state = _optional_counter_delta(
            old, new, "h3_request_rejections"
        )
        if "decreased" in (attempt_state, rejection_state):
            reset_seen = True
        if attempt_state != rejection_state or "changed" in (
            attempt_state,
            rejection_state,
        ):
            reasons.append("h3_transport_ledger_availability_changed")

        deltas.append(
            {
                "run_id": template["run_id"],
                "phase": template["phase"],
                "route": template["route"],
                "protocol": template["protocol"],
                "method": template["method"],
                "role": template["role"],
                "handler_requests": None if new_handler < old_handler else new_handler - old_handler,
                "h3_attempts": attempts_delta,
                "h3_request_rejections": rejections_delta,
            }
        )
    if reset_seen:
        reasons.append("origin_counter_reset_or_wrap")

    selected_rows = [
        row
        for row in deltas
        if row["phase"] == "main"
        and row["run_id"] == run_id.lower()
        and row["route"] == route
        and row["protocol"] == protocol
    ]
    if not selected_rows:
        reasons.append("route_protocol_correlation_missing")

    selected_handler = _sum_available(row["handler_requests"] for row in selected_rows)
    by_role: dict[str, int] = {}
    by_method: dict[str, int] = {}
    if selected_handler is not None:
        for row in selected_rows:
            value = row["handler_requests"]
            if value is None:
                continue
            by_role[row["role"]] = by_role.get(row["role"], 0) + value
            by_method[row["method"]] = by_method.get(row["method"], 0) + value

    selected_attempts = _sum_available(row["h3_attempts"] for row in selected_rows)
    selected_rejections = _sum_available(
        row["h3_request_rejections"] for row in selected_rows
    )
    transport_reason = None
    if selected_attempts is None or selected_rejections is None:
        selected_attempts = None
        selected_rejections = None
        transport_reason = "request_level_quic_hooks_unavailable"

    reconciliation_error: float | None = None
    if expected_handler_requests is not None and selected_handler is not None:
        reconciliation_error = abs(selected_handler - expected_handler_requests) / max(
            1, expected_handler_requests
        )
        if reconciliation_error > 0.001:
            reasons.append("handler_reconciliation_exceeds_0.1_percent")

    unmarked = _sum_available(
        row["handler_requests"] for row in deltas if row["phase"] == "unmarked"
    )
    unique_reasons = sorted(set(reasons))
    return {
        "schema_version": 1,
        "kind": "origin_correlation",
        "run_id": run_id.lower(),
        "route": route,
        "protocol": protocol,
        "origin_instance_id": after_origin["origin_instance_id"],
        "counter_epoch": after_epoch,
        "eligible": not unique_reasons,
        "ineligibility_reasons": unique_reasons,
        "expected_handler_requests": expected_handler_requests,
        "handler_reconciliation_error": reconciliation_error,
        "unmarked_handler_requests": unmarked,
        "selected": {
            "handler_requests": selected_handler,
            "by_role": dict(sorted(by_role.items())),
            "by_method": dict(sorted(by_method.items())),
            "h3_attempts": selected_attempts,
            "h3_request_rejections": selected_rejections,
            "h3_transport_unavailability_reason": transport_reason,
        },
        "counter_deltas": deltas,
    }


def _counter_key(row: dict[str, Any]) -> tuple[Any, ...]:
    return (
        row["run_id"],
        row["phase"],
        row["route"],
        row["protocol"],
        row["method"],
        row["role"],
    )


def _sortable_counter_key(key: tuple[Any, ...]) -> tuple[str, ...]:
    return tuple("" if item is None else str(item) for item in key)


def _optional_counter_delta(
    old: dict[str, Any] | None, new: dict[str, Any] | None, field: str
) -> tuple[int | None, str]:
    old_value = None if old is None else old[field]
    new_value = None if new is None else new[field]
    if old is None and new_value is not None:
        return new_value, "available"
    if old_value is None and new_value is None:
        return None, "unavailable"
    if old_value is None or new_value is None:
        return None, "changed"
    if new_value < old_value:
        return None, "decreased"
    return new_value - old_value, "available"


def _sum_available(values: Any) -> int | None:
    materialized = list(values)
    if not materialized:
        return None
    if any(value is None for value in materialized):
        return None
    return sum(materialized)


def _schema_errors(instance: Any, schema: dict[str, Any], path: str) -> list[str]:
    errors: list[str] = []

    if "anyOf" in schema:
        branches = [_schema_errors(instance, branch, path) for branch in schema["anyOf"]]
        if all(branch for branch in branches):
            errors.append(f"{path}: does not match any allowed schema")
        return errors

    if "const" in schema and instance != schema["const"]:
        errors.append(f"{path}: expected constant {schema['const']!r}")
    if "enum" in schema and instance not in schema["enum"]:
        errors.append(f"{path}: value {instance!r} is not allowed")

    expected_type = schema.get("type")
    if expected_type is not None and not _matches_type(instance, expected_type):
        errors.append(f"{path}: expected {expected_type}")
        return errors

    if isinstance(instance, dict):
        for required in schema.get("required", []):
            if required not in instance:
                errors.append(f"{path}: missing required property {required!r}")
        properties = schema.get("properties", {})
        for name, value in instance.items():
            if name in properties:
                errors.extend(_schema_errors(value, properties[name], f"{path}.{name}"))
            elif schema.get("additionalProperties") is False:
                errors.append(f"{path}: unexpected property {name!r}")

    if isinstance(instance, list) and "items" in schema:
        for index, value in enumerate(instance):
            errors.extend(_schema_errors(value, schema["items"], f"{path}[{index}]"))

    if isinstance(instance, str) and "pattern" in schema:
        if re.fullmatch(schema["pattern"], instance) is None:
            errors.append(f"{path}: value does not match required pattern")
    if isinstance(instance, int) and not isinstance(instance, bool) and "minimum" in schema:
        if instance < schema["minimum"]:
            errors.append(f"{path}: value is below minimum {schema['minimum']}")
    return errors


def _matches_type(instance: Any, expected: str) -> bool:
    if expected == "object":
        return isinstance(instance, dict)
    if expected == "array":
        return isinstance(instance, list)
    if expected == "string":
        return isinstance(instance, str)
    if expected == "integer":
        return isinstance(instance, int) and not isinstance(instance, bool)
    if expected == "null":
        return instance is None
    if expected == "boolean":
        return isinstance(instance, bool)
    return False


def _load_snapshot(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise SnapshotValidationError(f"cannot read {path}: {error}") from error
    validate_snapshot(value)
    return value


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    capture = subparsers.add_parser("capture", help="capture one admin snapshot")
    capture.add_argument("--admin-url", required=True)
    capture.add_argument("--output", type=Path, required=True)

    validate = subparsers.add_parser("validate", help="validate one snapshot")
    validate.add_argument("--input", type=Path, required=True)

    correlate = subparsers.add_parser("correlate", help="compute exact before/after deltas")
    correlate.add_argument("--before", type=Path, required=True)
    correlate.add_argument("--after", type=Path, required=True)
    correlate.add_argument("--run-id", required=True)
    correlate.add_argument("--route", choices=("bench", "large", "files"), required=True)
    correlate.add_argument("--protocol", choices=("h1", "h2", "h3"), default="h3")
    correlate.add_argument("--expected-handler-requests", type=int)
    correlate.add_argument("--output", type=Path, required=True)
    return parser


def main(arguments: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(arguments)
    try:
        if args.command == "capture":
            capture_snapshot(args.admin_url, args.output)
            return 0
        if args.command == "validate":
            _load_snapshot(args.input)
            return 0
        if args.command == "correlate":
            if args.expected_handler_requests is not None and args.expected_handler_requests < 0:
                raise ValueError("expected handler requests cannot be negative")
            result = correlate_snapshots(
                _load_snapshot(args.before),
                _load_snapshot(args.after),
                run_id=args.run_id,
                route=args.route,
                protocol=args.protocol,
                expected_handler_requests=args.expected_handler_requests,
            )
            _write_json_atomic(result, args.output)
            return 0 if result["eligible"] else 3
    except (OSError, ValueError, SnapshotValidationError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 2
    parser.error("unknown command")
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
