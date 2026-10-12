#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
scratch=

cleanup() {
	status=$?
	if [[ -n "$scratch" && -d "$scratch" ]]; then
		rm -rf -- "$scratch"
	fi
	return "$status"
}
trap cleanup EXIT INT TERM

scratch=$(mktemp -d "${TMPDIR:-/tmp}/h3fuzz-calibration.XXXXXX")
export H3FUZZ_CALIBRATION_SCRATCH=$scratch
python3 "$SCRIPT_DIR/calibrate_wordlist.py" "$@"
