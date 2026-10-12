#!/usr/bin/env python3
"""Validate the stable JSON contract emitted by -http3-fingerprint."""

import argparse
import json
from pathlib import Path
import sys


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("path", type=Path)
    parser.add_argument("--supported", choices=("true", "false"), required=True)
    parser.add_argument("--high-rate-ready", choices=("true", "false", "either"), default="either")
    args = parser.parse_args()

    try:
        payload = json.loads(args.path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"invalid fingerprint JSON in {args.path}: {exc}", file=sys.stderr)
        return 1

    expected_supported = args.supported == "true"
    if payload.get("supported") is not expected_supported:
        print(
            f"supported mismatch: expected {expected_supported}, got {payload.get('supported')!r}",
            file=sys.stderr,
        )
        return 1

    if args.high_rate_ready != "either":
        expected_ready = args.high_rate_ready == "true"
        if payload.get("high_rate_ready") is not expected_ready:
            print(
                f"high_rate_ready mismatch: expected {expected_ready}, "
                f"got {payload.get('high_rate_ready')!r}",
                file=sys.stderr,
            )
            return 1

    if expected_supported:
        if payload.get("alpn") != "h3":
            print(f"expected ALPN h3, got {payload.get('alpn')!r}", file=sys.stderr)
            return 1
        if not str(payload.get("http_version", "")).startswith("HTTP/3"):
            print(f"expected HTTP/3 response, got {payload.get('http_version')!r}", file=sys.stderr)
            return 1
        attempted = payload.get("concurrent_streams_attempted")
        completed = payload.get("concurrent_streams_completed")
        if not isinstance(attempted, int) or not isinstance(completed, int) or attempted <= 0 or completed != attempted:
            print(
                f"concurrent stream probe incomplete: attempted={attempted!r}, completed={completed!r}",
                file=sys.stderr,
            )
            return 1

    print(
        json.dumps(
            {
                "target": payload.get("target"),
                "supported": payload.get("supported"),
                "high_rate_ready": payload.get("high_rate_ready"),
                "reason": payload.get("reason"),
                "alpn": payload.get("alpn"),
                "http_version": payload.get("http_version"),
                "connection_reused": payload.get("connection_reused"),
                "streams": [
                    payload.get("concurrent_streams_completed"),
                    payload.get("concurrent_streams_attempted"),
                ],
                "elapsed_ms": payload.get("elapsed_ms"),
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

