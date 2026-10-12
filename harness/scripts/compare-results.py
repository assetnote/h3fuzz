#!/usr/bin/env python3
"""Compare h3fuzz JSON findings while allowing method-specific response codes."""

import argparse
import base64
import json
from pathlib import Path
import sys


def load_results(path: Path) -> dict[str, dict]:
    text = path.read_text(encoding="utf-8")
    try:
        payload = json.loads(text)
    except json.JSONDecodeError:
        try:
            items = [json.loads(line) for line in text.splitlines() if line.strip()]
        except json.JSONDecodeError as exc:
            raise ValueError(f"cannot read NDJSON {path}: {exc}") from exc
        ndjson = True
    else:
        items = payload.get("results", [])
        ndjson = False

    results: dict[str, dict] = {}
    for item in items:
        value = item.get("input", {}).get("FUZZ")
        if not isinstance(value, str):
            raise ValueError(f"result without string input.FUZZ in {path}: {item!r}")
        if ndjson:
            try:
                value = base64.b64decode(value, validate=True).decode("utf-8")
            except (ValueError, UnicodeDecodeError) as exc:
                raise ValueError(f"invalid base64 input.FUZZ in {path}: {value!r}") from exc
        results[value] = item
    return results


def load_expected(path: Path) -> set[str]:
    return {
        line
        for raw in path.read_text(encoding="utf-8").splitlines()
        if (line := raw.strip()) and not line.startswith("#")
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("baseline", type=Path)
    parser.add_argument("candidate", type=Path)
    parser.add_argument("--expected", type=Path, required=True)
    args = parser.parse_args()

    try:
        baseline = load_results(args.baseline)
        candidate = load_results(args.candidate)
        expected = load_expected(args.expected)
    except (OSError, ValueError) as exc:
        print(exc, file=sys.stderr)
        return 1

    baseline_words = set(baseline)
    candidate_words = set(candidate)
    errors: list[str] = []
    if baseline_words != expected:
        errors.append(
            "baseline differs from fixtures: "
            f"missing={sorted(expected - baseline_words)!r} "
            f"unexpected={sorted(baseline_words - expected)!r}"
        )
    if candidate_words != baseline_words:
        errors.append(
            "automatic mode differs from full GET: "
            f"missing={sorted(baseline_words - candidate_words)!r} "
            f"unexpected={sorted(candidate_words - baseline_words)!r}"
        )

    metadata_differences = []
    for word in sorted(baseline_words & candidate_words):
        left = baseline[word]
        right = candidate[word]
        changed = {
            key: [left.get(key), right.get(key)]
            for key in ("status", "length", "words", "lines", "content-type", "redirectlocation")
            if left.get(key) != right.get(key)
        }
        if changed:
            metadata_differences.append({"word": word, "changed": changed})
        semantic_changes = {
            key: value for key, value in changed.items() if key not in ("words", "lines")
        }
        if semantic_changes:
            errors.append(f"automatic metadata differs for {word!r}: {semantic_changes!r}")

    summary = {
        "baseline_findings": len(baseline_words),
        "candidate_findings": len(candidate_words),
        "expected_findings": len(expected),
        "finding_sets_equal": baseline_words == candidate_words == expected,
        "metadata_differences": metadata_differences,
    }
    print(json.dumps(summary, sort_keys=True))

    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
