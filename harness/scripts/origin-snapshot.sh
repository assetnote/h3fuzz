#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

admin_url=
output=
while (($#)); do
	case "$1" in
	--admin-url)
		(($# >= 2)) || { printf 'error: --admin-url requires a value\n' >&2; exit 2; }
		admin_url=$2
		shift 2
		;;
	--output)
		(($# >= 2)) || { printf 'error: --output requires a value\n' >&2; exit 2; }
		output=$2
		shift 2
		;;
	-h | --help)
		printf 'usage: %s --admin-url http://127.0.0.1:PORT --output ABSOLUTE_PATH\n' "$0"
		exit 0
		;;
	*)
		printf 'error: unknown argument: %s\n' "$1" >&2
		exit 2
		;;
	esac
done

[[ -n "$admin_url" ]] || { printf 'error: --admin-url is required\n' >&2; exit 2; }
[[ "$output" == /* ]] || { printf 'error: --output must be an absolute path\n' >&2; exit 2; }

exec python3 "$SCRIPT_DIR/origin_snapshot.py" capture \
	--admin-url "$admin_url" \
	--output "$output"
