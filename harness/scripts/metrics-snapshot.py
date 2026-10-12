#!/usr/bin/env python3
"""Fetch only Caddy's request counters without retaining the full exposition."""

import argparse
import json
import re
from pathlib import Path
import urllib.error
import urllib.request


COUNTER_RE = re.compile(
    r'^caddy_http_requests_total(?P<labels>\{[^}]*\})?\s+(?P<value>[0-9.eE+-]+)$'
)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("url")
    parser.add_argument("output", type=Path)
    args = parser.parse_args()

    result: dict[str, object] = {"available": False, "url": args.url, "counters": {}}
    try:
        with urllib.request.urlopen(args.url, timeout=2) as response:
            text = response.read().decode("utf-8", errors="replace")
        counters = {}
        for line in text.splitlines():
            match = COUNTER_RE.match(line)
            if match:
                counters[match.group("labels") or "{}"] = float(match.group("value"))
        result.update({"available": True, "counters": counters})
    except (OSError, urllib.error.URLError) as exc:
        result["error"] = str(exc)

    args.output.write_text(json.dumps(result, sort_keys=True) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

