#!/usr/bin/env python3
"""Build and validate fail-closed PERF-501 fixed-sweep resource evidence.

CPU and network observations are derived from retained PERF-003 point pairs.
Response latency comes only from the client's versioned main-response histogram;
QUIC RTT estimates are deliberately not accepted as response-latency evidence.
"""

from __future__ import annotations

import datetime as dt
import math
from pathlib import Path
import re
from typing import Any
import uuid

from perf004_contract import ContractError, file_sha256, read_json


RESOURCE_FIELDS = (
    "client_cpu_percent",
    "server_cpu_percent",
    "network_utilization_percent",
    "p99_latency_ms",
)
LATENCY_FIELDS = {
    "schema_version",
    "source",
    "scope",
    "clock",
    "unit",
    "method",
    "sample_count",
    "p99_upper_bound_ns",
    "maximum_trackable_ns",
    "overflow_count",
}
LATENCY_CONSTANTS = {
    "schema_version": 1,
    "source": "client_final_attempt_time_to_first_byte",
    "scope": "main_successful_logical_responses",
    "clock": "monotonic",
    "unit": "nanoseconds",
    "method": "bounded_histogram_upper_bound",
}
CPU_FORMULA = "100 * cpu_time_delta_ns / capture_duration_ns"
NETWORK_FORMULA = (
    "100 * 8 * max(rx_bytes_delta, tx_bytes_delta) / "
    "(capture_duration_seconds * link_speed_bits_per_second)"
)
LATENCY_FORMULA = "p99_upper_bound_ns / 1000000"
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
DECIMAL_RE = re.compile(r"^(0|[1-9][0-9]*)$")
RFC3339_RE = re.compile(
    r"^(?P<date>[0-9]{4}-[0-9]{2}-[0-9]{2})T"
    r"(?P<time>[0-9]{2}:[0-9]{2}:[0-9]{2})"
    r"(?:\.(?P<fraction>[0-9]{1,9}))?"
    r"(?P<zone>Z|[+-][0-9]{2}:[0-9]{2})$"
)
EPOCH = dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc)


class ResourceEvidenceError(ContractError):
    pass


def _integer(value: object, label: str, *, minimum: int = 0) -> int:
    if not isinstance(value, int) or isinstance(value, bool) or value < minimum:
        raise ResourceEvidenceError(f"{label}_invalid")
    return value


def _decimal(value: object, label: str) -> int:
    if not isinstance(value, str) or DECIMAL_RE.fullmatch(value) is None:
        raise ResourceEvidenceError(f"{label}_invalid")
    return int(value)


def _finite(value: object, label: str) -> float:
    if (
        not isinstance(value, (int, float))
        or isinstance(value, bool)
        or not math.isfinite(float(value))
        or float(value) < 0
    ):
        raise ResourceEvidenceError(f"{label}_invalid")
    return float(value)


def _timestamp_ns(value: object, label: str) -> int:
    if not isinstance(value, str):
        raise ResourceEvidenceError(f"{label}_invalid")
    match = RFC3339_RE.fullmatch(value)
    if match is None:
        raise ResourceEvidenceError(f"{label}_invalid")
    zone = "+00:00" if match.group("zone") == "Z" else match.group("zone")
    try:
        parsed = dt.datetime.fromisoformat(
            f"{match.group('date')}T{match.group('time')}{zone}"
        ).astimezone(dt.timezone.utc)
    except ValueError as exc:
        raise ResourceEvidenceError(f"{label}_invalid") from exc
    seconds = (parsed - EPOCH).days * 86_400 + (parsed - EPOCH).seconds
    fraction = int((match.group("fraction") or "0").ljust(9, "0"))
    return seconds * 1_000_000_000 + fraction


def _load_object(path: Path, label: str) -> dict[str, Any]:
    try:
        value = read_json(path)
    except ContractError as exc:
        raise ResourceEvidenceError(f"{label}_unavailable") from exc
    if not isinstance(value, dict):
        raise ResourceEvidenceError(f"{label}_not_object")
    return value


def _point_pair(
    before_path: Path,
    after_path: Path,
    *,
    endpoint: str,
    run_id: str,
) -> tuple[dict[str, Any], dict[str, Any], int]:
    before = _load_object(before_path, f"{endpoint}_before")
    after = _load_object(after_path, f"{endpoint}_after")
    expected_kind = f"{endpoint}_point"
    for phase, point in (("before", before), ("after", after)):
        if point.get("schema_version") != 1 or point.get("kind") != expected_kind:
            raise ResourceEvidenceError(f"{endpoint}_{phase}_identity_invalid")
        if point.get("phase") != phase or point.get("run_id") != run_id:
            raise ResourceEvidenceError(f"{endpoint}_{phase}_capture_mismatch")
        if point.get("eligible") is False:
            raise ResourceEvidenceError(f"{endpoint}_{phase}_ineligible")
    for field in ("host_identity_sha256", "boot_id", "network_namespace_inode"):
        if not before.get(field) or before.get(field) != after.get(field):
            raise ResourceEvidenceError(f"{endpoint}_{field}_mismatch")
    before_ns = _timestamp_ns(before.get("captured_at"), f"{endpoint}_before_captured_at")
    after_ns = _timestamp_ns(after.get("captured_at"), f"{endpoint}_after_captured_at")
    duration_ns = after_ns - before_ns
    if duration_ns <= 0:
        raise ResourceEvidenceError(f"{endpoint}_capture_duration_invalid")
    return before, after, duration_ns


def _single_process(point: dict[str, Any], role: str, label: str) -> dict[str, Any]:
    processes = point.get("processes")
    if not isinstance(processes, list):
        raise ResourceEvidenceError(f"{label}_processes_invalid")
    selected = [row for row in processes if isinstance(row, dict) and row.get("role") == role]
    if len(selected) != 1:
        raise ResourceEvidenceError(f"{label}_{role}_process_count_invalid")
    return selected[0]


def _cpu_observation(
    before_path: Path,
    after_path: Path,
    *,
    endpoint: str,
    role: str,
    run_id: str,
) -> tuple[float, dict[str, Any]]:
    before, after, duration_ns = _point_pair(
        before_path, after_path, endpoint=endpoint, run_id=run_id
    )
    old = _single_process(before, role, f"{endpoint}_before")
    new = _single_process(after, role, f"{endpoint}_after")
    pid = _integer(old.get("pid"), f"{endpoint}_{role}_pid", minimum=1)
    start_ticks = old.get("start_ticks")
    if (
        new.get("pid") != pid
        or new.get("start_ticks") != start_ticks
        or not isinstance(start_ticks, str)
        or DECIMAL_RE.fullmatch(start_ticks) is None
    ):
        raise ResourceEvidenceError(f"{endpoint}_{role}_identity_mismatch")
    before_cpu = _decimal(old.get("cpu_time_ns"), f"{endpoint}_{role}_before_cpu")
    after_cpu = _decimal(new.get("cpu_time_ns"), f"{endpoint}_{role}_after_cpu")
    if after_cpu < before_cpu:
        raise ResourceEvidenceError(f"{endpoint}_{role}_cpu_counter_reset")
    cpu_delta = after_cpu - before_cpu
    percent = 100.0 * cpu_delta / duration_ns
    if not math.isfinite(percent):
        raise ResourceEvidenceError(f"{endpoint}_{role}_cpu_percent_invalid")
    return percent, {
        "source": "perf003_point_pair_process_cpu_time",
        "capture_scope": f"{endpoint}_point_snapshot_interval",
        "before_artifact_sha256": file_sha256(before_path),
        "after_artifact_sha256": file_sha256(after_path),
        "before_captured_at": before["captured_at"],
        "after_captured_at": after["captured_at"],
        "duration_ns": str(duration_ns),
        "role": role,
        "pid": pid,
        "start_ticks": start_ticks,
        "cpu_time_delta_ns": str(cpu_delta),
        "formula": CPU_FORMULA,
    }


def _unsigned_delta(old: object, new: object, width: object, label: str) -> int:
    old_value = _decimal(old, f"{label}_before")
    new_value = _decimal(new, f"{label}_after")
    width_value = _integer(width, f"{label}_width", minimum=1)
    if width_value > 128:
        raise ResourceEvidenceError(f"{label}_width_invalid")
    limit = 1 << width_value
    if old_value >= limit or new_value >= limit:
        raise ResourceEvidenceError(f"{label}_out_of_range")
    if new_value >= old_value:
        return new_value - old_value
    if old_value >= int(limit * 0.95) and new_value <= int(limit * 0.05):
        return limit - old_value + new_value
    raise ResourceEvidenceError(f"{label}_counter_reset")


def _interface_map(point: dict[str, Any], label: str) -> dict[tuple[int, str, str], dict[str, Any]]:
    interfaces = point.get("interfaces")
    if not isinstance(interfaces, list):
        raise ResourceEvidenceError(f"{label}_interfaces_invalid")
    result: dict[tuple[int, str, str], dict[str, Any]] = {}
    for index, row in enumerate(interfaces):
        if not isinstance(row, dict):
            raise ResourceEvidenceError(f"{label}_interface_{index}_invalid")
        ifindex = _integer(row.get("ifindex"), f"{label}_interface_{index}_ifindex", minimum=1)
        name = row.get("name")
        source = row.get("source")
        if not isinstance(name, str) or not name or source not in {"sysfs", "netlink"}:
            raise ResourceEvidenceError(f"{label}_interface_{index}_identity_invalid")
        key = (ifindex, name, source)
        if key in result:
            raise ResourceEvidenceError(f"{label}_interface_duplicate")
        result[key] = row
    return result


def _network_observations(
    before_path: Path,
    after_path: Path,
    *,
    endpoint: str,
    run_id: str,
) -> list[dict[str, Any]]:
    before, after, duration_ns = _point_pair(
        before_path, after_path, endpoint=endpoint, run_id=run_id
    )
    old_rows = _interface_map(before, f"{endpoint}_before")
    new_rows = _interface_map(after, f"{endpoint}_after")
    if set(old_rows) != set(new_rows):
        raise ResourceEvidenceError(f"{endpoint}_interface_identity_mismatch")
    observations: list[dict[str, Any]] = []
    for key in sorted(old_rows):
        old = old_rows[key]
        new = new_rows[key]
        if new.get("counter_width_bits") != old.get("counter_width_bits"):
            raise ResourceEvidenceError(f"{endpoint}_{key[1]}_counter_width_mismatch")
        rx_delta = _unsigned_delta(
            old.get("rx_bytes"), new.get("rx_bytes"), old.get("counter_width_bits"),
            f"{endpoint}_{key[1]}_rx_bytes",
        )
        tx_delta = _unsigned_delta(
            old.get("tx_bytes"), new.get("tx_bytes"), old.get("counter_width_bits"),
            f"{endpoint}_{key[1]}_tx_bytes",
        )
        if rx_delta == 0 and tx_delta == 0:
            continue
        speed = old.get("link_speed_bits_per_second")
        if speed != new.get("link_speed_bits_per_second"):
            raise ResourceEvidenceError(f"{endpoint}_{key[1]}_link_speed_mismatch")
        speed_value = _decimal(speed, f"{endpoint}_{key[1]}_link_speed")
        if speed_value <= 0:
            raise ResourceEvidenceError(f"{endpoint}_{key[1]}_link_speed_invalid")
        utilization = 100.0 * 8.0 * max(rx_delta, tx_delta) * 1_000_000_000 / (
            duration_ns * speed_value
        )
        if not math.isfinite(utilization):
            raise ResourceEvidenceError(f"{endpoint}_{key[1]}_utilization_invalid")
        observations.append({
            "endpoint": endpoint,
            "ifindex": key[0],
            "name": key[1],
            "counter_source": key[2],
            "link_speed_bits_per_second": str(speed_value),
            "capture_duration_ns": str(duration_ns),
            "rx_bytes_delta": str(rx_delta),
            "tx_bytes_delta": str(tx_delta),
            "full_duplex_direction_utilization_percent": utilization,
        })
    return observations


def validate_latency(
    telemetry: dict[str, Any], run_id: str
) -> tuple[float, dict[str, Any]]:
    if telemetry.get("schema_version") != 2 or telemetry.get("run_id") != run_id:
        raise ResourceEvidenceError("latency_telemetry_identity_mismatch")
    responses = telemetry.get("responses")
    if not isinstance(responses, dict):
        raise ResourceEvidenceError("latency_responses_invalid")
    successful = _integer(
        responses.get("successful_logical_responses"),
        "latency_successful_logical_responses",
    )
    latency = responses.get("latency")
    if not isinstance(latency, dict) or set(latency) != LATENCY_FIELDS:
        raise ResourceEvidenceError("latency_contract_invalid")
    for field, expected in LATENCY_CONSTANTS.items():
        if latency.get(field) != expected:
            raise ResourceEvidenceError(f"latency_{field}_invalid")
    sample_count = _integer(latency.get("sample_count"), "latency_sample_count")
    p99_ns = _integer(latency.get("p99_upper_bound_ns"), "latency_p99_upper_bound_ns")
    maximum_ns = _integer(
        latency.get("maximum_trackable_ns"), "latency_maximum_trackable_ns", minimum=1
    )
    overflow_count = _integer(latency.get("overflow_count"), "latency_overflow_count")
    if sample_count != successful:
        raise ResourceEvidenceError("latency_sample_count_mismatch")
    if successful > 0 and p99_ns == 0:
        raise ResourceEvidenceError("latency_p99_zero_with_samples")
    if p99_ns > maximum_ns:
        raise ResourceEvidenceError("latency_p99_above_trackable_maximum")
    if overflow_count != 0:
        raise ResourceEvidenceError("latency_histogram_overflow")
    return p99_ns / 1_000_000.0, {
        "source": "run_telemetry.responses.latency",
        "capture_scope": LATENCY_CONSTANTS["scope"],
        "latency_schema_version": 1,
        "clock": LATENCY_CONSTANTS["clock"],
        "unit": LATENCY_CONSTANTS["unit"],
        "method": LATENCY_CONSTANTS["method"],
        "sample_count": sample_count,
        "p99_upper_bound_ns": p99_ns,
        "maximum_trackable_ns": maximum_ns,
        "overflow_count": overflow_count,
        "formula": LATENCY_FORMULA,
    }


def build_evidence(
    *,
    run_id: str,
    telemetry_path: Path,
    client_before_path: Path,
    client_after_path: Path,
    server_before_path: Path,
    server_after_path: Path,
) -> dict[str, Any]:
    try:
        uuid.UUID(run_id)
    except (ValueError, AttributeError) as exc:
        raise ResourceEvidenceError("run_id_invalid") from exc

    reasons: list[str] = []
    metrics: dict[str, float] = {}
    provenance: dict[str, Any] = {}
    try:
        value, detail = _cpu_observation(
            client_before_path,
            client_after_path,
            endpoint="client",
            role="h3fuzz",
            run_id=run_id,
        )
        metrics["client_cpu_percent"] = value
        provenance["client_cpu_percent"] = detail
    except (ContractError, OSError) as exc:
        reasons.append(f"client_cpu_percent:{exc}")
    try:
        value, detail = _cpu_observation(
            server_before_path,
            server_after_path,
            endpoint="server",
            role="origin",
            run_id=run_id,
        )
        metrics["server_cpu_percent"] = value
        provenance["server_cpu_percent"] = detail
    except (ContractError, OSError) as exc:
        reasons.append(f"server_cpu_percent:{exc}")
    try:
        observations = [
            *_network_observations(
                client_before_path,
                client_after_path,
                endpoint="client",
                run_id=run_id,
            ),
            *_network_observations(
                server_before_path,
                server_after_path,
                endpoint="server",
                run_id=run_id,
            ),
        ]
        if not observations:
            raise ResourceEvidenceError("no_utilized_interface_observation")
        metrics["network_utilization_percent"] = max(
            row["full_duplex_direction_utilization_percent"] for row in observations
        )
        provenance["network_utilization_percent"] = {
            "source": "perf003_point_pair_interface_byte_counters",
            "capture_scope": "client_and_server_point_snapshot_intervals",
            "aggregation": "maximum_observed_full_duplex_direction_utilization",
            "formula": NETWORK_FORMULA,
            "client_before_artifact_sha256": file_sha256(client_before_path),
            "client_after_artifact_sha256": file_sha256(client_after_path),
            "server_before_artifact_sha256": file_sha256(server_before_path),
            "server_after_artifact_sha256": file_sha256(server_after_path),
            "observations": observations,
        }
    except (ContractError, OSError) as exc:
        reasons.append(f"network_utilization_percent:{exc}")
    try:
        telemetry = _load_object(telemetry_path, "telemetry")
        value, detail = validate_latency(telemetry, run_id)
        detail["telemetry_artifact_sha256"] = file_sha256(telemetry_path)
        metrics["p99_latency_ms"] = value
        provenance["p99_latency_ms"] = detail
    except (ContractError, OSError) as exc:
        reasons.append(f"p99_latency_ms:{exc}")

    reasons = sorted(set(reasons))
    eligible = not reasons and set(metrics) == set(RESOURCE_FIELDS) and set(provenance) == set(RESOURCE_FIELDS)
    evidence = {
        "schema_version": 1,
        "kind": "sweep_resource_usage",
        "run_id": run_id,
        "eligible": eligible,
        "ineligibility_reasons": [] if eligible else reasons or ["resource_evidence_incomplete"],
        "metrics": metrics if eligible else None,
        "provenance": provenance if eligible else None,
    }
    validate_evidence(evidence, expected_run_id=run_id)
    return evidence


def _exact_keys(value: object, expected: set[str], label: str) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != expected:
        raise ResourceEvidenceError(f"{label}_contract_invalid")
    return value


def _sha256(value: object, label: str) -> str:
    if not isinstance(value, str) or SHA256_RE.fullmatch(value) is None:
        raise ResourceEvidenceError(f"{label}_invalid")
    return value


def _close(actual: float, expected: float, label: str) -> None:
    if not math.isclose(actual, expected, rel_tol=1e-12, abs_tol=1e-12):
        raise ResourceEvidenceError(f"{label}_formula_mismatch")


def _validate_cpu_provenance(value: object, field: str) -> float:
    expected_keys = {
        "source", "capture_scope", "before_artifact_sha256", "after_artifact_sha256",
        "before_captured_at", "after_captured_at", "duration_ns", "role", "pid",
        "start_ticks", "cpu_time_delta_ns", "formula",
    }
    row = _exact_keys(value, expected_keys, f"{field}_provenance")
    if row["source"] != "perf003_point_pair_process_cpu_time" or row["formula"] != CPU_FORMULA:
        raise ResourceEvidenceError(f"{field}_provenance_source_invalid")
    expected_scope = "client_point_snapshot_interval" if field == "client_cpu_percent" else "server_point_snapshot_interval"
    expected_role = "h3fuzz" if field == "client_cpu_percent" else "origin"
    if row["capture_scope"] != expected_scope or row["role"] != expected_role:
        raise ResourceEvidenceError(f"{field}_provenance_scope_invalid")
    _sha256(row["before_artifact_sha256"], f"{field}_before_sha256")
    _sha256(row["after_artifact_sha256"], f"{field}_after_sha256")
    before_ns = _timestamp_ns(row["before_captured_at"], f"{field}_before_captured_at")
    after_ns = _timestamp_ns(row["after_captured_at"], f"{field}_after_captured_at")
    duration_ns = _decimal(row["duration_ns"], f"{field}_duration_ns")
    if duration_ns <= 0 or after_ns - before_ns != duration_ns:
        raise ResourceEvidenceError(f"{field}_capture_duration_mismatch")
    _integer(row["pid"], f"{field}_pid", minimum=1)
    _decimal(row["start_ticks"], f"{field}_start_ticks")
    cpu_delta = _decimal(row["cpu_time_delta_ns"], f"{field}_cpu_time_delta_ns")
    return 100.0 * cpu_delta / duration_ns


def _validate_network_provenance(value: object) -> float:
    expected_keys = {
        "source", "capture_scope", "aggregation", "formula",
        "client_before_artifact_sha256", "client_after_artifact_sha256",
        "server_before_artifact_sha256", "server_after_artifact_sha256", "observations",
    }
    row = _exact_keys(value, expected_keys, "network_utilization_percent_provenance")
    if (
        row["source"] != "perf003_point_pair_interface_byte_counters"
        or row["capture_scope"] != "client_and_server_point_snapshot_intervals"
        or row["aggregation"] != "maximum_observed_full_duplex_direction_utilization"
        or row["formula"] != NETWORK_FORMULA
    ):
        raise ResourceEvidenceError("network_utilization_percent_provenance_source_invalid")
    for name in (
        "client_before_artifact_sha256", "client_after_artifact_sha256",
        "server_before_artifact_sha256", "server_after_artifact_sha256",
    ):
        _sha256(row[name], f"network_{name}")
    observations = row["observations"]
    if not isinstance(observations, list) or not observations:
        raise ResourceEvidenceError("network_observations_invalid")
    expected_observation_keys = {
        "endpoint", "ifindex", "name", "counter_source", "link_speed_bits_per_second",
        "capture_duration_ns", "rx_bytes_delta", "tx_bytes_delta",
        "full_duplex_direction_utilization_percent",
    }
    values: list[float] = []
    for index, observation in enumerate(observations):
        item = _exact_keys(observation, expected_observation_keys, f"network_observation_{index}")
        if item["endpoint"] not in {"client", "server"}:
            raise ResourceEvidenceError(f"network_observation_{index}_endpoint_invalid")
        _integer(item["ifindex"], f"network_observation_{index}_ifindex", minimum=1)
        if not isinstance(item["name"], str) or not item["name"] or item["counter_source"] not in {"sysfs", "netlink"}:
            raise ResourceEvidenceError(f"network_observation_{index}_identity_invalid")
        speed = _decimal(item["link_speed_bits_per_second"], f"network_observation_{index}_speed")
        duration = _decimal(item["capture_duration_ns"], f"network_observation_{index}_duration")
        rx_delta = _decimal(item["rx_bytes_delta"], f"network_observation_{index}_rx")
        tx_delta = _decimal(item["tx_bytes_delta"], f"network_observation_{index}_tx")
        if speed <= 0 or duration <= 0 or (rx_delta == 0 and tx_delta == 0):
            raise ResourceEvidenceError(f"network_observation_{index}_values_invalid")
        calculated = 100.0 * 8.0 * max(rx_delta, tx_delta) * 1_000_000_000 / (duration * speed)
        observed = _finite(
            item["full_duplex_direction_utilization_percent"],
            f"network_observation_{index}_utilization",
        )
        _close(observed, calculated, f"network_observation_{index}_utilization")
        values.append(calculated)
    return max(values)


def _validate_latency_provenance(value: object) -> float:
    expected_keys = {
        "source", "capture_scope", "telemetry_artifact_sha256", "latency_schema_version",
        "clock", "unit", "method", "sample_count", "p99_upper_bound_ns",
        "maximum_trackable_ns", "overflow_count", "formula",
    }
    row = _exact_keys(value, expected_keys, "p99_latency_ms_provenance")
    if (
        row["source"] != "run_telemetry.responses.latency"
        or row["capture_scope"] != LATENCY_CONSTANTS["scope"]
        or row["latency_schema_version"] != 1
        or row["clock"] != LATENCY_CONSTANTS["clock"]
        or row["unit"] != LATENCY_CONSTANTS["unit"]
        or row["method"] != LATENCY_CONSTANTS["method"]
        or row["formula"] != LATENCY_FORMULA
    ):
        raise ResourceEvidenceError("p99_latency_ms_provenance_source_invalid")
    _sha256(row["telemetry_artifact_sha256"], "p99_latency_ms_telemetry_sha256")
    _integer(row["sample_count"], "p99_latency_ms_sample_count")
    p99_ns = _integer(row["p99_upper_bound_ns"], "p99_latency_ms_p99_upper_bound_ns")
    maximum_ns = _integer(row["maximum_trackable_ns"], "p99_latency_ms_maximum_trackable_ns", minimum=1)
    if p99_ns > maximum_ns or _integer(row["overflow_count"], "p99_latency_ms_overflow_count") != 0:
        raise ResourceEvidenceError("p99_latency_ms_bounds_invalid")
    return p99_ns / 1_000_000.0


def validate_evidence(
    evidence: object, *, expected_run_id: str | None = None
) -> dict[str, Any]:
    top = _exact_keys(
        evidence,
        {"schema_version", "kind", "run_id", "eligible", "ineligibility_reasons", "metrics", "provenance"},
        "resource_evidence",
    )
    if top["schema_version"] != 1 or top["kind"] != "sweep_resource_usage":
        raise ResourceEvidenceError("resource_evidence_identity_invalid")
    try:
        parsed_run_id = str(uuid.UUID(top["run_id"]))
    except (ValueError, TypeError, AttributeError) as exc:
        raise ResourceEvidenceError("resource_evidence_run_id_invalid") from exc
    if parsed_run_id != top["run_id"] or (expected_run_id is not None and top["run_id"] != expected_run_id):
        raise ResourceEvidenceError("resource_evidence_run_id_mismatch")
    reasons = top["ineligibility_reasons"]
    if not isinstance(reasons, list) or any(not isinstance(item, str) or not item for item in reasons) or len(reasons) != len(set(reasons)):
        raise ResourceEvidenceError("resource_evidence_reasons_invalid")
    if not isinstance(top["eligible"], bool):
        raise ResourceEvidenceError("resource_evidence_eligible_invalid")
    if not top["eligible"]:
        if not reasons or top["metrics"] is not None or top["provenance"] is not None:
            raise ResourceEvidenceError("resource_evidence_ineligible_shape_invalid")
        return top
    if reasons:
        raise ResourceEvidenceError("resource_evidence_eligible_has_reasons")
    validate_metrics_and_provenance(top["metrics"], top["provenance"])
    return top


def validate_metrics_and_provenance(
    metrics_value: object, provenance_value: object
) -> dict[str, float]:
    """Validate report-embedded resource values and recompute every formula."""
    metrics = _exact_keys(metrics_value, set(RESOURCE_FIELDS), "resource_metrics")
    provenance = _exact_keys(
        provenance_value, set(RESOURCE_FIELDS), "resource_provenance"
    )
    calculated = {
        "client_cpu_percent": _validate_cpu_provenance(provenance["client_cpu_percent"], "client_cpu_percent"),
        "server_cpu_percent": _validate_cpu_provenance(provenance["server_cpu_percent"], "server_cpu_percent"),
        "network_utilization_percent": _validate_network_provenance(provenance["network_utilization_percent"]),
        "p99_latency_ms": _validate_latency_provenance(provenance["p99_latency_ms"]),
    }
    for field in RESOURCE_FIELDS:
        observed = _finite(metrics[field], field)
        _close(observed, calculated[field], field)
    return {field: float(metrics[field]) for field in RESOURCE_FIELDS}


def load_and_validate(path: Path, *, expected_run_id: str | None = None) -> dict[str, Any]:
    return validate_evidence(_load_object(path, "resource_evidence"), expected_run_id=expected_run_id)
