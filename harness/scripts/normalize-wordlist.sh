#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
Usage: normalize-wordlist.sh [options] SOURCE OUTPUT

Expand final.txt's literal %EXT% token and optionally repeat the list.

Options:
  --extension EXT    Replacement for %EXT% (default: php)
  --repeat N         Number of complete rounds (default: 1)
  --unique-rounds    Prefix repeated entries with round-N- (default if N > 1)
  --exact-repeats    Repeat identical entries instead of prefixing each round
  -h, --help         Show this help
EOF
}

extension=php
repeat=1
round_mode=auto

while (($#)); do
	case "$1" in
		--extension)
			(($# >= 2)) || { usage; exit 2; }
			extension=$2
			shift 2
			;;
		--repeat)
			(($# >= 2)) || { usage; exit 2; }
			repeat=$2
			shift 2
			;;
		--unique-rounds)
			round_mode=unique
			shift
			;;
		--exact-repeats)
			round_mode=exact
			shift
			;;
		-h|--help)
			usage
			exit 0
			;;
		--*)
			printf 'unknown option: %s\n' "$1" >&2
			usage
			exit 2
			;;
		*)
			break
			;;
	esac
done

(($# == 2)) || { usage; exit 2; }
source_file=$1
output_file=$2

[[ -f "$source_file" ]] || { printf 'source wordlist not found: %s\n' "$source_file" >&2; exit 1; }
[[ "$repeat" =~ ^[1-9][0-9]*$ ]] || { printf 'repeat must be a positive integer\n' >&2; exit 2; }
[[ -n "$extension" ]] || { printf 'extension must not be empty\n' >&2; exit 2; }
[[ "$source_file" != "$output_file" ]] || { printf 'source and output must differ\n' >&2; exit 2; }

if [[ "$round_mode" == auto ]]; then
	if ((repeat > 1)); then
		round_mode=unique
	else
		round_mode=exact
	fi
fi

mkdir -p "$(dirname -- "$output_file")"
tmp_file="${output_file}.tmp.$$"
trap 'rm -f -- "$tmp_file"' EXIT

awk -v ext="$extension" -v rounds="$repeat" -v mode="$round_mode" '
	length($0) > 0 {
		gsub(/%EXT%/, ext)
		line[++count] = $0
	}
	END {
		for (round = 1; round <= rounds; round++) {
			for (i = 1; i <= count; i++) {
				if (mode == "unique" && rounds > 1) {
					printf "round-%02d-%s\n", round, line[i]
				} else {
					print line[i]
				}
			}
		}
	}
' "$source_file" >"$tmp_file"

mv -- "$tmp_file" "$output_file"
trap - EXIT

line_count=$(awk 'END { print NR + 0 }' "$output_file")
if command -v sha256sum >/dev/null 2>&1; then
	digest=$(sha256sum "$output_file" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	digest=$(shasum -a 256 "$output_file" | awk '{print $1}')
else
	digest=unavailable
fi

printf 'wordlist=%s lines=%s sha256=%s extension=%s rounds=%s mode=%s\n' \
	"$output_file" "$line_count" "$digest" "$extension" "$repeat" "$round_mode" >&2

