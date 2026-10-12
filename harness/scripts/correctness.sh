#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

require_command python3
h3fuzz=$(resolve_h3fuzz)
run_dir=$(new_run_dir correctness)
base_url=${H3FUZZ_BASE_URL:-https://localhost:8443}
wordlist=${H3FUZZ_CORRECTNESS_WORDLIST:-"$HARNESS_DIR/wordlists/correctness.txt"}
expected=${H3FUZZ_EXPECTED_HITS:-"$HARNESS_DIR/wordlists/expected-hits.txt"}
probe_timeout=${H3FUZZ_PROBE_TIMEOUT:-2}

[[ -f "$wordlist" ]] || die "correctness wordlist not found: $wordlist"
[[ -f "$expected" ]] || die "expected-hit list not found: $expected"

cases=(good head-constant head-405 runtime-head-reject runtime-range-reject range-ignore range-lie neither)

for case_name in "${cases[@]}"; do
	note "checking automatic response optimization on $case_name"
	baseline="$run_dir/${case_name}-get.ndjson"
	candidate="$run_dir/${case_name}-auto.ndjson"

	common=(
		-u "$base_url/matrix/$case_name/FUZZ"
		-w "$wordlist"
		-k
		-http3
		-http3-connections 1
		-http3-max-connections 4
		-http3-probe-timeout "$probe_timeout"
		-mc all
		-fc 404
		-noninteractive
		-json
	)

	"$h3fuzz" "${common[@]}" \
		-no-response-optimization \
		-X GET \
		>"$baseline" \
		2>"$run_dir/${case_name}-get.stderr"

	"$h3fuzz" "${common[@]}" \
		>"$candidate" \
		2>"$run_dir/${case_name}-auto.stderr"

	case "$case_name" in
		good|runtime-head-reject) expected_strategy=HEAD ;;
		runtime-range-reject) expected_strategy=RANGE ;;
		head-constant|head-405) expected_strategy=RANGE ;;
		range-ignore|range-lie|neither) expected_strategy=GET ;;
	esac
	if ! grep -Eq "Response mode[[:space:]]*: ${expected_strategy}:" "$run_dir/${case_name}-auto.stderr"; then
		die "$case_name selected the wrong response strategy (expected $expected_strategy); see $run_dir/${case_name}-auto.stderr"
	fi

	python3 "$SCRIPT_DIR/compare-results.py" \
		"$baseline" "$candidate" --expected "$expected" \
		>"$run_dir/${case_name}-comparison.json"
	python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); print("findings={} metadata_differences={}".format(p["candidate_findings"], len(p["metadata_differences"])))' \
		"$run_dir/${case_name}-comparison.json"
done

note "correctness matrix passed; artifacts: $run_dir"
