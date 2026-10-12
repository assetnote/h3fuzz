#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd -- "$HARNESS_DIR/.." && pwd)"

printf '%s\n' 'checking shell syntax'
for script in "$SCRIPT_DIR"/*.sh; do
	bash -n "$script"
done
sh -n "$HARNESS_DIR/server/generate-fixtures.sh"

printf '%s\n' 'checking required harness entrypoints'
for entrypoint in \
	"$SCRIPT_DIR/benchmark.sh" \
	"$SCRIPT_DIR/calibrate-wordlist.sh" \
	"$SCRIPT_DIR/certify.sh" \
	"$SCRIPT_DIR/baseline-report.py" \
	"$SCRIPT_DIR/certification-report.py"; do
	[[ -x "$entrypoint" ]] || { printf 'entrypoint is not executable: %s\n' "$entrypoint" >&2; exit 1; }
done

printf '%s\n' 'checking Python syntax'
python3 - "$SCRIPT_DIR" <<'PY'
import ast
from pathlib import Path
import sys

for path in sorted(Path(sys.argv[1]).glob("*.py")):
    ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
PY

printf '%s\n' 'checking JSON Schema syntax'
python3 - "$HARNESS_DIR/schemas" <<'PY'
import json
from pathlib import Path
import sys

for path in sorted(Path(sys.argv[1]).glob("*.schema.json")):
    schema = json.loads(path.read_text(encoding="utf-8"))
    if schema.get("$schema") != "https://json-schema.org/draft/2020-12/schema":
        raise SystemExit(f"{path}: expected JSON Schema draft 2020-12")
PY

printf '%s\n' 'running harness unit and fixture tests'
python3 -m unittest discover -s "$SCRIPT_DIR" -p '*_test.py'

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
	printf '%s\n' 'checking Compose models (Docker daemon is not required)'
	docker compose -f "$HARNESS_DIR/compose.yaml" config --quiet
	docker compose -f "$HARNESS_DIR/compose.host-network.yaml" config --quiet
else
	printf '%s\n' 'skipping Compose model validation: docker compose is unavailable'
fi

if command -v caddy >/dev/null 2>&1; then
	printf '%s\n' 'checking Caddyfile adaptation with local caddy binary'
	caddy adapt --config "$HARNESS_DIR/Caddyfile.instrumented" --adapter caddyfile --validate >/dev/null
	caddy adapt --config "$HARNESS_DIR/Caddyfile.ceiling" --adapter caddyfile --validate >/dev/null
else
	printf '%s\n' 'skipping Caddy adaptation: local caddy binary is unavailable'
fi

if [[ -f "$REPO_ROOT/final.txt" ]]; then
	printf '%s\n' 'checking final.txt normalization'
	tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/h3fuzz-harness-validate.XXXXXX")
	trap 'rm -rf -- "$tmp_dir"' EXIT
	"$SCRIPT_DIR/normalize-wordlist.sh" "$REPO_ROOT/final.txt" "$tmp_dir/final.php.txt" >/dev/null
	python3 - "$REPO_ROOT/final.txt" "$tmp_dir/final.php.txt" <<'PY'
import hashlib
from pathlib import Path
import sys

source = Path(sys.argv[1]).read_bytes()
expected_hash = "1335d7ff3a5624bc2941b3fa0a42bb5eada5b4ea42b10d3b061aeeb1a4289af7"
if hashlib.sha256(source).hexdigest() != expected_hash:
    raise SystemExit("final.txt SHA-256 differs from the harness fixture expectation")

lines = Path(sys.argv[2]).read_text(encoding="utf-8").splitlines()
if len(lines) != 233_582:
    raise SystemExit(f"expected 233582 normalized entries, got {len(lines)}")
if len(set(lines)) != len(lines):
    raise SystemExit("normalized one-round wordlist is not unique")
if any("%EXT%" in line for line in lines):
    raise SystemExit("normalized wordlist still contains %EXT%")
PY
	rm -rf -- "$tmp_dir"
	trap - EXIT
else
	printf '%s\n' 'skipping final.txt validation: source wordlist is unavailable'
fi

printf '%s\n' 'harness validation passed'
