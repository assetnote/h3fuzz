#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
stage=

cleanup() {
	status=$?
	trap - EXIT INT TERM
	if [[ -n "$stage" && -d "$stage" ]]; then
		if ! rm -rf -- "$stage"; then
			printf '%s\n' "certify: cleanup failed for $stage" >&2
			status=4
		fi
	fi
	if [[ -n "$stage" && -d "${stage}-public" ]]; then
		if ! rm -rf -- "${stage}-public"; then
			printf '%s\n' "certify: cleanup failed for ${stage}-public" >&2
			status=4
		fi
	fi
	exit "$status"
}
trap cleanup EXIT INT TERM

mkdir -p "$HARNESS_DIR/.work"
stage=$(mktemp -d "$HARNESS_DIR/.work/certification-staging.XXXXXX")
chmod 700 "$stage"
export H3FUZZ_CERTIFICATION_STAGING=$stage
python3 "$SCRIPT_DIR/certify.py" "$@"
