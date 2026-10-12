#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

require_command python3
h3fuzz=$(resolve_h3fuzz)
run_dir=$(new_run_dir smoke)
probe_timeout=${H3FUZZ_PROBE_TIMEOUT:-2}

run_probe() {
	local name=$1
	local url=$2
	local expected_exit=$3
	local supported=$4
	local ready=$5
	local stdout_file="$run_dir/${name}.json"
	local stderr_file="$run_dir/${name}.stderr"

	note "fingerprinting $name at $url"
	set +e
	"$h3fuzz" \
		-u "$url" \
		-k \
		-http3-fingerprint \
		-http3-probe-timeout "$probe_timeout" \
		>"$stdout_file" 2>"$stderr_file"
	local status=$?
	set -e

	if ((status != expected_exit)); then
		printf 'fingerprint %s exited %s, expected %s\n' "$name" "$status" "$expected_exit" >&2
		printf '%s\n' "--- stderr ---" >&2
		sed -n '1,120p' "$stderr_file" >&2
		exit 1
	fi

	python3 "$SCRIPT_DIR/assert-fingerprint.py" \
		"$stdout_file" \
		--supported "$supported" \
		--high-rate-ready "$ready"
}

run_probe h3-mixed "https://localhost:8443/healthz" 0 true true
run_probe no-h3 "https://localhost:8444/healthz" 2 false either
run_probe udp-unreachable "https://localhost:8445/healthz" 2 false either
run_probe h3-only "https://localhost:8446/healthz" 0 true true

printf '%s\n' healthz >"$run_dir/one-word.txt"
run_auto() {
	local name=$1
	local url=$2
	local expected_protocol=$3
	shift 3
	local stdout_file="$run_dir/${name}.stdout"
	local stderr_file="$run_dir/${name}.stderr"

	note "checking automatic protocol selection for $name at $url"
	"$h3fuzz" \
		-u "$url" \
		-w "$run_dir/one-word.txt" \
		-k \
		-http3-probe-timeout "$probe_timeout" \
		-no-response-optimization \
		-noninteractive \
		-mc all \
		"$@" \
		>"$stdout_file" 2>"$stderr_file"
	if ! grep -Eq "Protocol[[:space:]]*: ${expected_protocol}" "$stderr_file"; then
		die "$name selected the wrong protocol (expected $expected_protocol); see $stderr_file"
	fi
}

run_auto auto-h3 "https://localhost:8443/FUZZ" "HTTP/3"
run_auto auto-no-h3 "https://localhost:8444/FUZZ" "HTTP/1.1"
run_auto auto-udp-unreachable "https://localhost:8445/FUZZ" "HTTP/1.1"
run_auto disabled-h3 "https://localhost:8443/FUZZ" "HTTP/1.1" -no-http3

note "smoke tests passed; artifacts: $run_dir"
