#!/usr/bin/env python3
"""Validate paired counter-enabled/control throughput measurements."""

from __future__ import annotations

import argparse
import json
import math
import os
from pathlib import Path
import statistics
import sys
import tempfile
from typing import Any


def analyze_pairs(document: dict[str, Any]) -> dict[str, Any]:
    if document.get("schema_version") != 1:
        raise ValueError("paired-run input must have schema_version 1")
    affinity = document.get("cpu_affinity")
    if not isinstance(affinity, str) or not affinity:
        raise ValueError("cpu_affinity must be a nonempty string")
    pairs = document.get("pairs")
    if not isinstance(pairs, list) or len(pairs) < 5:
        raise ValueError("at least five paired runs are required")

    relative_deltas: list[float] = []
    normalized_pairs: list[dict[str, Any]] = []
    previous_first: str | None = None
    for index, pair in enumerate(pairs, start=1):
        if not isinstance(pair, dict) or pair.get("pair") != index:
            raise ValueError("pair numbers must be consecutive and start at 1")
        first = pair.get("first")
        if first not in ("disabled", "enabled"):
            raise ValueError(f"pair {index}: first must be disabled or enabled")
        if previous_first == first:
            raise ValueError("the first run in each pair must alternate")
        previous_first = first
        disabled = _positive_rate(pair.get("disabled_rps"), index, "disabled_rps")
        enabled = _positive_rate(pair.get("enabled_rps"), index, "enabled_rps")
        delta = enabled / disabled - 1.0
        relative_deltas.append(delta)
        normalized_pairs.append(
            {
                "pair": index,
                "first": first,
                "disabled_rps": disabled,
                "enabled_rps": enabled,
                "relative_delta": delta,
            }
        )

    median_delta = statistics.median(relative_deltas)
    reasons: list[str] = []
    if median_delta < -0.02:
        reasons.append("instrumented_median_more_than_2_percent_below_control")
    return {
        "schema_version": 1,
        "kind": "origin_counter_overhead",
        "cpu_affinity": affinity,
        "pair_count": len(pairs),
        "eligible": not reasons,
        "ineligibility_reasons": reasons,
        "median_relative_delta": median_delta,
        "paired_relative_deltas": relative_deltas,
        "pairs": normalized_pairs,
    }


def _positive_rate(value: Any, pair: int, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"pair {pair}: {name} must be numeric")
    result = float(value)
    if not math.isfinite(result) or result <= 0:
        raise ValueError(f"pair {pair}: {name} must be positive and finite")
    return result


def _write_atomic(value: Any, output: Path) -> None:
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
    finally:
        if temporary_name is not None:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass


def main(arguments: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(arguments)
    try:
        document = json.loads(args.input.read_text(encoding="utf-8"))
        result = analyze_pairs(document)
        _write_atomic(result, args.output)
        return 0 if result["eligible"] else 3
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
