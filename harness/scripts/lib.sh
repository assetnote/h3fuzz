#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd -- "$HARNESS_DIR/.." && pwd)"

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

note() {
	printf '==> %s\n' "$*" >&2
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

resolve_h3fuzz() {
	local candidate=${H3FUZZ_BIN:-"$REPO_ROOT/h3fuzz"}
	[[ -x "$candidate" ]] || die "h3fuzz binary is not executable at $candidate (build it or set H3FUZZ_BIN)"
	printf '%s\n' "$candidate"
}

new_run_dir() {
	local kind=$1
	local parent=${H3FUZZ_ARTIFACTS_DIR:-"$HARNESS_DIR/.work"}
	local stamp
	stamp=$(date -u '+%Y%m%dT%H%M%SZ')
	mkdir -p "$parent"
	local result="$parent/${kind}-${stamp}-$$"
	mkdir -p "$result"
	printf '%s\n' "$result"
}

