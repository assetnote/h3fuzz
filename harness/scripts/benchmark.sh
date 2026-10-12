#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

# Contract-v1 harness modes have a distinct, strict CLI.  Keep the historical
# auto/get/head/range wrapper below for ordinary local comparisons.
previous=
for argument in "$@"; do
	if [[ "$previous" == --mode ]]; then
		case "$argument" in
			baseline|diagnostic|sweep|adaptive|selected-fixed)
				exec python3 "$SCRIPT_DIR/benchmark-v1.py" "$@"
				;;
		esac
	fi
	previous=$argument
done

usage() {
	cat >&2 <<'EOF'
Usage: benchmark.sh [options]

Options:
  --target URL          Target containing FUZZ (default: https://localhost:8443/bench/FUZZ)
  --wordlist PATH       Source wordlist (default: repository final.txt)
  --repeat N            Repeat normalized wordlist N times (default: 1; increase for a sustained run)
  --mode MODE           auto, get, head, or range (default: auto)
  --expect-strategy S   Require GET, HEAD, or RANGE (auto defaults to HEAD on /bench/ and /large/)
  --connections N       Fixed H3 pool size; 0 is adaptive (default: 0)
  --max-connections N   Adaptive upper bound (default: 64)
  --filter-status LIST  Status filter used to suppress result rendering
  --output DIR          Artifact directory (default: harness/.work/benchmark-...)
  --exact-repeats       Do not make repeated words unique
  --skip-fingerprint    Skip the pre-run H3 readiness check
  -h, --help            Show this help

This wrapper reports logical input rate, completed-response rate, drain state,
and optional Caddy counter correlation. A valid run requires telemetry to prove
HTTP/3, the requested response strategy, zero errors, and a clean drain.
EOF
}

target=${H3FUZZ_TARGET:-https://localhost:8443/bench/FUZZ}
source_wordlist=${H3FUZZ_WORDLIST:-"$REPO_ROOT/final.txt"}
repeat=${H3FUZZ_REPEAT:-1}
mode=${H3FUZZ_MODE:-auto}
expect_strategy=${H3FUZZ_EXPECT_STRATEGY:-}
connections=${H3FUZZ_CONNECTIONS:-0}
max_connections=${H3FUZZ_MAX_CONNECTIONS:-64}
filter_status=${H3FUZZ_FILTER_STATUS:-}
run_dir=
round_args=(--unique-rounds)
skip_fingerprint=${H3FUZZ_SKIP_FINGERPRINT:-0}

while (($#)); do
	case "$1" in
		--target) target=$2; shift 2 ;;
		--wordlist) source_wordlist=$2; shift 2 ;;
		--repeat) repeat=$2; shift 2 ;;
		--mode) mode=$2; shift 2 ;;
		--expect-strategy) expect_strategy=$2; shift 2 ;;
		--connections) connections=$2; shift 2 ;;
		--max-connections) max_connections=$2; shift 2 ;;
		--filter-status) filter_status=$2; shift 2 ;;
		--output) run_dir=$2; shift 2 ;;
		--exact-repeats) round_args=(--exact-repeats); shift ;;
		--skip-fingerprint) skip_fingerprint=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) printf 'unknown or incomplete option: %s\n' "$1" >&2; usage; exit 2 ;;
	esac
done

[[ "$target" == *FUZZ* ]] || die "target must contain FUZZ"
[[ "$repeat" =~ ^[1-9][0-9]*$ ]] || die "repeat must be a positive integer"
[[ "$connections" =~ ^[0-9]+$ ]] || die "connections must be a non-negative integer"
[[ "$max_connections" =~ ^[1-9][0-9]*$ ]] || die "max-connections must be a positive integer"
case "$mode" in auto|get|head|range) ;; *) die "mode must be auto, get, head, or range" ;; esac
case "$expect_strategy" in
	"") ;;
	[Gg][Ee][Tt]) expect_strategy=GET ;;
	[Hh][Ee][Aa][Dd]) expect_strategy=HEAD ;;
	[Rr][Aa][Nn][Gg][Ee]) expect_strategy=RANGE ;;
	*) die "expect-strategy must be GET, HEAD, or RANGE" ;;
esac
case "$mode" in
	get)
		[[ -z "$expect_strategy" || "$expect_strategy" == GET ]] || die "get mode requires --expect-strategy GET"
		expect_strategy=GET
		;;
	head)
		[[ -z "$expect_strategy" || "$expect_strategy" == HEAD ]] || die "head mode requires --expect-strategy HEAD"
		expect_strategy=HEAD
		;;
	range)
		[[ -z "$expect_strategy" || "$expect_strategy" == RANGE ]] || die "range mode requires --expect-strategy RANGE"
		expect_strategy=RANGE
		;;
	auto)
		if [[ -z "$expect_strategy" ]]; then
			case "$target" in
				*/bench/*|*/large/*) expect_strategy=HEAD ;;
				*) die "auto mode requires --expect-strategy GET, HEAD, or RANGE for this target" ;;
			esac
		fi
		;;
esac
[[ -f "$source_wordlist" ]] || die "wordlist not found: $source_wordlist"

if [[ -z "$filter_status" ]]; then
	case "$target" in
		*/bench/*) filter_status=204 ;;
		*/large/*) filter_status=200,206 ;;
		*) filter_status=404 ;;
	esac
fi

require_command python3
h3fuzz=$(resolve_h3fuzz)
if [[ -z "$run_dir" ]]; then
	run_dir=$(new_run_dir benchmark)
else
	mkdir -p "$run_dir"
	run_dir=$(cd -- "$run_dir" && pwd)
fi

normalized="$run_dir/wordlist.txt"
"$SCRIPT_DIR/normalize-wordlist.sh" \
	--extension "${H3FUZZ_EXTENSION:-php}" \
	--repeat "$repeat" \
	"${round_args[@]}" \
	"$source_wordlist" "$normalized"

probe_url=${target/FUZZ/healthz}
if [[ "$skip_fingerprint" != 1 ]]; then
	note "checking HTTP/3 readiness at $probe_url"
	"$h3fuzz" \
		-u "$probe_url" \
		-k \
		-http3-fingerprint \
		-http3-probe-timeout "${H3FUZZ_PROBE_TIMEOUT:-2}" \
		>"$run_dir/fingerprint.json" \
		2>"$run_dir/fingerprint.stderr"
	python3 "$SCRIPT_DIR/assert-fingerprint.py" \
		"$run_dir/fingerprint.json" --supported true --high-rate-ready true >/dev/null
fi

python3 "$SCRIPT_DIR/metrics-snapshot.py" \
	"${H3FUZZ_METRICS_URL:-http://127.0.0.1:2019/metrics}" \
	"$run_dir/metrics-before.json"

common=(
	-u "$target"
	-w "$normalized"
	-k
	-http3
	-http3-connections "$connections"
	-http3-max-connections "$max_connections"
	-http3-probe-timeout "${H3FUZZ_PROBE_TIMEOUT:-2}"
	-run-telemetry "$run_dir/run-telemetry.json"
	-noninteractive
	-s
	-fc "$filter_status"
)

case "$mode" in
	auto)
		;;
	get)
		common+=( -no-response-optimization -X GET )
		;;
	head)
		common+=( -no-response-optimization -X HEAD )
		;;
	range)
		common+=( -no-response-optimization -X GET -H 'Range: bytes=-1' )
		;;
esac

note "running mode=$mode expected_strategy=$expect_strategy connections=$connections max_connections=$max_connections target=$target"
start_ns=$(python3 -c 'import time; print(time.monotonic_ns())')
set +e
"$h3fuzz" "${common[@]}" >"$run_dir/h3fuzz.stdout" 2>"$run_dir/h3fuzz.stderr"
status=$?
set -e
end_ns=$(python3 -c 'import time; print(time.monotonic_ns())')

python3 "$SCRIPT_DIR/metrics-snapshot.py" \
	"${H3FUZZ_METRICS_URL:-http://127.0.0.1:2019/metrics}" \
	"$run_dir/metrics-after.json"

set +e
python3 "$SCRIPT_DIR/benchmark-report.py" \
	--wordlist "$normalized" \
	--telemetry "$run_dir/run-telemetry.json" \
	--before "$run_dir/metrics-before.json" \
	--after "$run_dir/metrics-after.json" \
	--start-ns "$start_ns" \
	--end-ns "$end_ns" \
	--exit-code "$status" \
	--target "$target" \
	--mode "$mode" \
	--expect-strategy "$expect_strategy" \
	--connections "$connections" \
	--max-connections "$max_connections" \
	--output "$run_dir/benchmark-report.json" \
	--require-run-valid
report_status=$?
set -e

note "benchmark artifacts: $run_dir"
if ((status != 0)); then
	note "h3fuzz exited with status $status; see $run_dir/h3fuzz.stderr"
	exit "$status"
fi
if ((report_status != 0)); then
	note "benchmark telemetry did not validate the run; see $run_dir/benchmark-report.json"
	exit "$report_status"
fi
