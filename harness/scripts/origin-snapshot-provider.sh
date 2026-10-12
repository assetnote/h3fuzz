#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

phase=
run_id=
output=
while (($#)); do
	case "$1" in
	--phase)
		(($# >= 2)) || { printf 'error: --phase requires a value\n' >&2; exit 2; }
		phase=$2
		shift 2
		;;
	--run-id)
		(($# >= 2)) || { printf 'error: --run-id requires a value\n' >&2; exit 2; }
		run_id=$2
		shift 2
		;;
	--output)
		(($# >= 2)) || { printf 'error: --output requires a value\n' >&2; exit 2; }
		output=$2
		shift 2
		;;
	-h | --help)
		printf 'usage: %s --phase before|after --run-id UUID --output ABSOLUTE_PATH\n' "$0"
		exit 0
		;;
	*)
		printf 'error: unknown argument: %s\n' "$1" >&2
		exit 2
		;;
	esac
done

[[ "$phase" == before || "$phase" == after ]] || { printf 'error: --phase must be before or after\n' >&2; exit 2; }
[[ "$run_id" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]] || {
	printf 'error: --run-id must be a UUID\n' >&2
	exit 2
}
[[ "$output" == /* ]] || { printf 'error: --output must be an absolute path\n' >&2; exit 2; }

admin_url=${H3FUZZ_ORIGIN_ADMIN_URL:-}
[[ -n "$admin_url" ]] || {
	printf 'error: H3FUZZ_ORIGIN_ADMIN_URL must name the server-local loopback admin URL\n' >&2
	exit 2
}

# phase and run_id intentionally select the provider invocation, not a subset
# of counters. Certification needs the complete global before/after snapshots.
exec "$SCRIPT_DIR/origin-snapshot.sh" \
	--admin-url "$admin_url" \
	--output "$output"
