#!/bin/bash

# Create an isolated, auditable Linux netem profile for the HTTP/3 harness.
#
# The script intentionally does not use sudo.  It must be invoked by an
# operator that already has the required privilege, and fails closed before
# changing networking when the platform, tools, state directory, or ownership
# checks do not pass.

set -Eeuo pipefail

readonly EXIT_USAGE=2
readonly EXIT_REFUSED=3
readonly EXIT_CLEANUP_RESIDUE=4
readonly DEFAULT_STATE_ROOT=/run/h3fuzz/network-matrix
readonly OWNER_PREFIX=h3fuzz-network-matrix

ACTION=
PROFILE_ID=
RTT_MS=
LOSS_PERCENT=
SEED=
PEER_STREAM_LIMIT=256
WORDLIST_PROFILE=long
THROTTLE_RPS=0
THROTTLE_STATUS=429
FAILURE_PROFILE=none
FAILURE_AFTER_MS=0
FAILURE_DURATION_MS=0
APPLY_OPTION_SEEN=0

STATE_ROOT=${H3FUZZ_NETWORK_MATRIX_STATE_DIR:-$DEFAULT_STATE_ROOT}
TEST_MODE=${H3FUZZ_NETWORK_MATRIX_TEST_MODE:-0}
PROFILE_DIR=
MANIFEST_PATH=
COMMAND_LOG_PATH=
HOST_IF=
PEER_IF=
NAMESPACE=
OWNER_TOKEN=
OWNER_TAG=
NAMESPACE_INODE=
HOST_ADDRESS=
PEER_ADDRESS=
SUBNET=
ONE_WAY_DELAY_US=
CREATED_AT=

LOCK_DIR=
LOCK_TOKEN=
LOCK_HELD=0
ROLLBACK_NEEDED=0
NS_CREATED=0
HOST_CREATED=0
HOST_QDISC_CREATED=0
PEER_QDISC_CREATED=0
IN_EXIT_TRAP=0

IP_BIN=
TC_BIN=
PYTHON_BIN=
UNAME_BIN=
ID_BIN=
DATE_BIN=
STAT_BIN=

usage() {
	cat >&2 <<'EOF'
usage:
  network-matrix.sh apply --profile-id ID --rtt-ms MS --loss-percent PERCENT --seed N [options]
  network-matrix.sh status --profile-id ID
  network-matrix.sh cleanup --profile-id ID

apply options:
  --peer-stream-limit N       origin peer stream limit metadata (default: 256)
  --wordlist-profile KIND     short or long (default: long)
  --throttle-rps N            deterministic server throttle metadata (default: 0)
  --throttle-status CODE      throttle response status (default: 429)
  --failure-profile KIND      none or transient-link-down (default: none)
  --failure-after-ms N        external failure trigger offset
  --failure-duration-ms N     external failure duration

The listed latency is round-trip time.  The profile shapes both directions at
one half of that value.  State and evidence are kept under
/run/h3fuzz/network-matrix by default.
EOF
}

say_error() {
	printf 'error: %s\n' "$*" >&2
}

usage_error() {
	say_error "$*"
	usage
	exit "$EXIT_USAGE"
}

refuse() {
	say_error "$*"
	exit "$EXIT_REFUSED"
}

resolve_command() {
	local name=$1
	local resolved
	resolved=$(command -v -- "$name" 2>/dev/null || true)
	[[ -n "$resolved" ]] || refuse "required command not found: $name"
	case "$resolved" in
		/*) ;;
		*) refuse "required command did not resolve to an absolute path: $name" ;;
	esac
	printf '%s\n' "$resolved"
}

validate_safe_path() {
	local label=$1
	local value=$2
	[[ "$value" == /* ]] || refuse "$label must be an absolute path"
	[[ "$value" != / ]] || refuse "$label must not be the filesystem root"
	[[ "$value" =~ ^/[A-Za-z0-9._/-]+$ ]] || refuse "$label contains unsafe characters"
	[[ "$value" != *//* ]] || refuse "$label must be normalized"
	[[ "$value" != */./* && "$value" != */. ]] || refuse "$label must not contain . components"
	[[ "$value" != */../* && "$value" != */.. ]] || refuse "$label must not contain .. components"
}

validate_profile_id() {
	[[ "$PROFILE_ID" =~ ^[a-z0-9][a-z0-9._-]{0,62}$ ]] || \
		usage_error "--profile-id must match [a-z0-9][a-z0-9._-]{0,62}"
}

validate_uint() {
	local label=$1
	local value=$2
	[[ "$value" =~ ^[0-9]+$ ]] || usage_error "$label must be an unsigned integer"
}

validate_apply_options() {
	[[ -n "$RTT_MS" ]] || usage_error "apply requires --rtt-ms"
	[[ -n "$LOSS_PERCENT" ]] || usage_error "apply requires --loss-percent"
	[[ -n "$SEED" ]] || usage_error "apply requires --seed"

	[[ "$RTT_MS" =~ ^[0-9]+([.][0-9]+)?$ ]] || usage_error "--rtt-ms must be numeric"
	[[ "$LOSS_PERCENT" =~ ^[0-9]+([.][0-9]+)?$ ]] || usage_error "--loss-percent must be numeric"
	validate_uint --seed "$SEED"
	validate_uint --peer-stream-limit "$PEER_STREAM_LIMIT"
	validate_uint --throttle-rps "$THROTTLE_RPS"
	validate_uint --throttle-status "$THROTTLE_STATUS"
	validate_uint --failure-after-ms "$FAILURE_AFTER_MS"
	validate_uint --failure-duration-ms "$FAILURE_DURATION_MS"

	case "$WORDLIST_PROFILE" in
		short|long) ;;
		*) usage_error "--wordlist-profile must be short or long" ;;
	esac
	case "$FAILURE_PROFILE" in
		none|transient-link-down) ;;
		*) usage_error "--failure-profile must be none or transient-link-down" ;;
	esac

	local validation
	validation=$(
		"$PYTHON_BIN" - "$RTT_MS" "$LOSS_PERCENT" "$SEED" \
			"$PEER_STREAM_LIMIT" "$THROTTLE_RPS" "$THROTTLE_STATUS" \
			"$FAILURE_PROFILE" "$FAILURE_AFTER_MS" "$FAILURE_DURATION_MS" <<'PY'
from decimal import Decimal, InvalidOperation
import sys

try:
    rtt = Decimal(sys.argv[1])
    loss = Decimal(sys.argv[2])
except InvalidOperation:
    raise SystemExit(2)
seed = int(sys.argv[3])
streams = int(sys.argv[4])
throttle_rps = int(sys.argv[5])
throttle_status = int(sys.argv[6])
failure = sys.argv[7]
failure_after = int(sys.argv[8])
failure_duration = int(sys.argv[9])

errors = []
if not (Decimal("0") < rtt <= Decimal("60000")):
    errors.append("RTT must be greater than 0 and at most 60000 ms")
if not (Decimal("0") <= loss <= Decimal("100")):
    errors.append("loss must be between 0 and 100 percent")
half_us = rtt * Decimal("500")
if half_us != half_us.to_integral_value():
    errors.append("RTT must be expressible as an integer number of half-RTT microseconds")
if not (1 <= seed <= 4294967295):
    errors.append("seed must be between 1 and 4294967295")
if not (1 <= streams <= 2305843009213693951):
    errors.append("peer stream limit is outside the QUIC range")
if throttle_rps < 0:
    errors.append("throttle RPS must not be negative")
if not (100 <= throttle_status <= 599):
    errors.append("throttle status must be between 100 and 599")
if failure == "none" and (failure_after != 0 or failure_duration != 0):
    errors.append("failure timing requires --failure-profile transient-link-down")
if failure == "transient-link-down" and (failure_after <= 0 or failure_duration <= 0):
    errors.append("transient-link-down requires positive failure timing")
if errors:
    print("; ".join(errors))
    raise SystemExit(1)
def canonical_decimal(value):
    rendered = format(value, "f")
    if "." in rendered:
        rendered = rendered.rstrip("0").rstrip(".")
    return rendered or "0"

print("\t".join([
    str(int(half_us)), canonical_decimal(rtt), canonical_decimal(loss),
    str(seed), str(streams), str(throttle_rps), str(throttle_status),
    str(failure_after), str(failure_duration),
]))
PY
	) || usage_error "invalid apply profile: ${validation:-numeric validation failed}"
	IFS=$'\t' read -r ONE_WAY_DELAY_US RTT_MS LOSS_PERCENT SEED PEER_STREAM_LIMIT \
		THROTTLE_RPS THROTTLE_STATUS FAILURE_AFTER_MS FAILURE_DURATION_MS <<<"$validation"
}

parse_arguments() {
	(( $# >= 1 )) || usage_error "missing action"
	ACTION=$1
	shift
	case "$ACTION" in
		apply|status|cleanup) ;;
		-h|--help|help)
			usage
			exit 0
			;;
		*) usage_error "unknown action: $ACTION" ;;
	esac

	while (( $# > 0 )); do
		case "$1" in
			--profile-id|--rtt-ms|--loss-percent|--seed|--peer-stream-limit|--wordlist-profile|--throttle-rps|--throttle-status|--failure-profile|--failure-after-ms|--failure-duration-ms)
				(( $# >= 2 )) || usage_error "$1 requires a value"
				case "$1" in
					--profile-id) PROFILE_ID=$2 ;;
					--rtt-ms) RTT_MS=$2; APPLY_OPTION_SEEN=1 ;;
					--loss-percent) LOSS_PERCENT=$2; APPLY_OPTION_SEEN=1 ;;
					--seed) SEED=$2; APPLY_OPTION_SEEN=1 ;;
					--peer-stream-limit) PEER_STREAM_LIMIT=$2; APPLY_OPTION_SEEN=1 ;;
					--wordlist-profile) WORDLIST_PROFILE=$2; APPLY_OPTION_SEEN=1 ;;
					--throttle-rps) THROTTLE_RPS=$2; APPLY_OPTION_SEEN=1 ;;
					--throttle-status) THROTTLE_STATUS=$2; APPLY_OPTION_SEEN=1 ;;
					--failure-profile) FAILURE_PROFILE=$2; APPLY_OPTION_SEEN=1 ;;
					--failure-after-ms) FAILURE_AFTER_MS=$2; APPLY_OPTION_SEEN=1 ;;
					--failure-duration-ms) FAILURE_DURATION_MS=$2; APPLY_OPTION_SEEN=1 ;;
				esac
				shift 2
				;;
			-h|--help)
				usage
				exit 0
				;;
			*) usage_error "unknown option: $1" ;;
		esac
	done

	[[ -n "$PROFILE_ID" ]] || usage_error "$ACTION requires --profile-id"
	validate_profile_id
	if [[ "$ACTION" == apply ]]; then
		validate_apply_options
	else
		(( APPLY_OPTION_SEEN == 0 )) || \
			usage_error "$ACTION accepts only --profile-id"
	fi
}

prepare_tools_and_platform() {
	if [[ "$TEST_MODE" != 1 ]]; then
		# Running a network mutation as root with a caller-controlled PATH is not
		# acceptable.  Test mode exists solely for the fake-command test suite.
		export PATH=/usr/sbin:/usr/bin:/sbin:/bin
	fi
	UNAME_BIN=$(resolve_command uname)
	ID_BIN=$(resolve_command id)
	PYTHON_BIN=$(resolve_command python3)
	[[ $("$UNAME_BIN" -s) == Linux ]] || refuse "network profiles require native Linux"
	[[ $("$ID_BIN" -u) == 0 ]] || refuse "network profiles require effective UID 0 / CAP_NET_ADMIN"
	if [[ "$TEST_MODE" != 1 ]]; then
		"$PYTHON_BIN" - <<'PY' || refuse "network profiles require effective CAP_NET_ADMIN and CAP_SYS_ADMIN"
capabilities = None
with open("/proc/self/status", encoding="ascii") as source:
    for line in source:
        if line.startswith("CapEff:"):
            capabilities = int(line.split()[1], 16)
            break
if capabilities is None:
    raise SystemExit(1)
cap_net_admin = 1 << 12
cap_sys_admin = 1 << 21
if capabilities & cap_net_admin == 0 or capabilities & cap_sys_admin == 0:
    raise SystemExit(1)
PY
	fi

	IP_BIN=$(resolve_command ip)
	TC_BIN=$(resolve_command tc)
	DATE_BIN=$(resolve_command date)
	STAT_BIN=$(resolve_command stat)
	resolve_command mkdir >/dev/null
	resolve_command chmod >/dev/null
	resolve_command rm >/dev/null
	resolve_command rmdir >/dev/null
	resolve_command mv >/dev/null
	resolve_command cat >/dev/null
}

prepare_state_root() {
	if [[ "$TEST_MODE" != 1 && "$STATE_ROOT" != "$DEFAULT_STATE_ROOT" ]]; then
		refuse "a custom state directory is allowed only in explicit fake-command test mode"
	fi
	validate_safe_path state-directory "$STATE_ROOT"
	if [[ -L "$STATE_ROOT" ]]; then
		refuse "state directory must not be a symbolic link: $STATE_ROOT"
	fi
	if [[ ! -e "$STATE_ROOT" ]]; then
		local parent=${STATE_ROOT%/*}
		if [[ ! -d "$parent" && "$STATE_ROOT" == "$DEFAULT_STATE_ROOT" ]]; then
			local grandparent=${parent%/*}
			[[ -d "$grandparent" && ! -L "$grandparent" ]] || refuse "unsafe state parent: $grandparent"
			mkdir -- "$parent" || refuse "cannot create state parent: $parent"
			chmod 0700 "$parent" || refuse "cannot secure state parent: $parent"
		fi
		[[ -d "$parent" && ! -L "$parent" ]] || refuse "state parent must be an existing real directory: $parent"
		mkdir -- "$STATE_ROOT" || refuse "cannot create state directory: $STATE_ROOT"
		chmod 0700 "$STATE_ROOT" || refuse "cannot secure state directory: $STATE_ROOT"
	fi
	[[ -d "$STATE_ROOT" && ! -L "$STATE_ROOT" ]] || refuse "state path is not a real directory: $STATE_ROOT"

	if [[ "$TEST_MODE" != 1 ]]; then
		local state_stat
		state_stat=$("$STAT_BIN" -Lc '%u %a' "$STATE_ROOT") || refuse "cannot inspect state directory"
		local owner=${state_stat%% *}
		local mode=${state_stat##* }
		[[ "$owner" == 0 ]] || refuse "state directory must be owned by root"
		# Decimal digits are intentional: reject any group/world permission bits.
		local tail=$((10#$mode % 100))
		(( tail == 0 )) || refuse "state directory must not grant group/world permissions"
	fi

	PROFILE_DIR="$STATE_ROOT/profiles/$PROFILE_ID"
	MANIFEST_PATH="$PROFILE_DIR/profile.json"
	COMMAND_LOG_PATH="$PROFILE_DIR/commands-executed.jsonl"
}

acquire_lock() {
	LOCK_DIR="$STATE_ROOT/.mutation-lock"
	LOCK_TOKEN=$(
		"$PYTHON_BIN" - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
	)
	if ! mkdir -- "$LOCK_DIR" 2>/dev/null; then
		refuse "another network-matrix mutation is active (lock: $LOCK_DIR)"
	fi
	printf '%s\n' "$LOCK_TOKEN" >"$LOCK_DIR/owner-token"
	chmod 0600 "$LOCK_DIR/owner-token"
	LOCK_HELD=1
}

release_lock() {
	(( LOCK_HELD == 1 )) || return 0
	local observed=
	if [[ -f "$LOCK_DIR/owner-token" ]]; then
		IFS= read -r observed <"$LOCK_DIR/owner-token" || true
	fi
	if [[ "$observed" == "$LOCK_TOKEN" ]]; then
		rm -f -- "$LOCK_DIR/owner-token"
		rmdir -- "$LOCK_DIR" 2>/dev/null || true
	fi
	LOCK_HELD=0
}

profile_hash() {
	"$PYTHON_BIN" - "$PROFILE_ID" <<'PY'
import hashlib, sys
print(hashlib.sha256(sys.argv[1].encode("ascii")).hexdigest())
PY
}

derive_resources() {
	local digest
	digest=$(profile_hash)
	local suffix=${digest:0:10}
	NAMESPACE="h3n-$suffix"
	HOST_IF="h3h-$suffix"
	PEER_IF="h3p-$suffix"

	local byte0=$((16#${digest:10:2}))
	local byte1=$((16#${digest:12:2}))
	local byte2=$((16#${digest:14:2}))
	local second=$((18 + byte0 % 2))
	local third=$byte1
	local fourth=$(((byte2 % 64) * 4))
	SUBNET="198.$second.$third.$fourth/30"
	HOST_ADDRESS="198.$second.$third.$((fourth + 1))/30"
	PEER_ADDRESS="198.$second.$third.$((fourth + 2))/30"
}

check_owned_subnet_collision() {
	local result
	result=$("$PYTHON_BIN" - "$STATE_ROOT/profiles" "$SUBNET" "$PROFILE_ID" <<'PY'
import glob, json, os, sys
directory, wanted, requested_id = sys.argv[1:]
for path in sorted(glob.glob(os.path.join(directory, "*", "profile.json"))):
    try:
        with open(path, encoding="utf-8") as source:
            value = json.load(source)
        if value.get("manifest_type") != "h3fuzz-network-matrix-profile":
            print("invalid:" + path)
            raise SystemExit(0)
        if value.get("profile_id") == requested_id or value.get("lifecycle") == "cleaned":
            continue
        if value.get("network", {}).get("subnet") == wanted:
            print("collision:" + str(value.get("profile_id", "unknown")))
            raise SystemExit(0)
    except (OSError, ValueError, TypeError, KeyError):
        print("invalid:" + path)
        raise SystemExit(0)
print("none")
PY
	) || refuse "cannot inspect existing profile subnet ownership"
	case "$result" in
		none) ;;
		collision:*) refuse "derived subnet collides with profile ${result#collision:}: $SUBNET" ;;
		invalid:*) refuse "existing profile record is invalid: ${result#invalid:}" ;;
		*) refuse "unexpected subnet ownership inspection result" ;;
	esac
}

namespace_exists() {
	local wanted=$1
	local first rest
	while IFS=' ' read -r first rest; do
		[[ "$first" == "$wanted" ]] && return 0
	done < <("$IP_BIN" netns list 2>/dev/null || true)
	return 1
}

host_link_exists() {
	"$IP_BIN" link show dev "$1" >/dev/null 2>&1
}

peer_link_exists() {
	local ns=$1
	local dev=$2
	"$IP_BIN" netns exec "$ns" "$IP_BIN" link show dev "$dev" >/dev/null 2>&1
}

namespace_inode() {
	"$IP_BIN" netns exec "$1" "$STAT_BIN" -Lc '%i' /proc/self/ns/net 2>/dev/null
}

link_alias_host() {
	local tmp="$PROFILE_DIR/.host-alias.$$"
	"$IP_BIN" -j -d link show dev "$1" >"$tmp"
	"$PYTHON_BIN" - "$tmp" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
if not isinstance(value, list) or len(value) != 1:
    raise SystemExit(1)
print(value[0].get("ifalias", ""))
PY
	local rc=$?
	rm -f -- "$tmp"
	return "$rc"
}

link_alias_peer() {
	local ns=$1
	local dev=$2
	local tmp="$PROFILE_DIR/.peer-alias.$$"
	"$IP_BIN" netns exec "$ns" "$IP_BIN" -j -d link show dev "$dev" >"$tmp"
	"$PYTHON_BIN" - "$tmp" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
if not isinstance(value, list) or len(value) != 1:
    raise SystemExit(1)
print(value[0].get("ifalias", ""))
PY
	local rc=$?
	rm -f -- "$tmp"
	return "$rc"
}

log_command() {
	local phase=$1
	shift
	[[ -n "$COMMAND_LOG_PATH" && -d "$PROFILE_DIR" ]] || return 0
	"$PYTHON_BIN" - "$COMMAND_LOG_PATH" "$phase" "$@" <<'PY'
import datetime, json, os, sys
path, phase, *argv = sys.argv[1:]
record = {
    "recorded_at": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
    "phase": phase,
    "argv": argv,
}
with open(path, "a", encoding="utf-8") as output:
    output.write(json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n")
    output.flush()
    os.fsync(output.fileno())
PY
}

run_mutation() {
	local phase=$1
	shift
	log_command "$phase" "$@"
	"$@"
}

atomic_json_update() {
	local lifecycle=$1
	local namespace_inode_value=${2:-}
	"$PYTHON_BIN" - "$MANIFEST_PATH" "$lifecycle" "$namespace_inode_value" <<'PY'
import datetime, json, os, sys, tempfile
path, lifecycle, inode = sys.argv[1:]
with open(path, encoding="utf-8") as source:
    value = json.load(source)
value["lifecycle"] = lifecycle
value["updated_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
if inode:
    value["network"]["namespace_inode"] = int(inode)
for phase in ("before", "applied", "observed", "after"):
    evidence_path = os.path.join(os.path.dirname(path), phase + ".json")
    value["evidence"][phase] = os.path.basename(evidence_path) if os.path.isfile(evidence_path) else None
directory = os.path.dirname(path)
fd, temporary = tempfile.mkstemp(prefix=".profile.", dir=directory)
try:
    with os.fdopen(fd, "w", encoding="utf-8") as output:
        json.dump(value, output, indent=2, sort_keys=True)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
}

create_manifest() {
	MANIFEST_PATH=$MANIFEST_PATH PROFILE_ID=$PROFILE_ID OWNER_TOKEN=$OWNER_TOKEN OWNER_TAG=$OWNER_TAG \
	NAMESPACE=$NAMESPACE HOST_IF=$HOST_IF PEER_IF=$PEER_IF SUBNET=$SUBNET \
	HOST_ADDRESS=$HOST_ADDRESS PEER_ADDRESS=$PEER_ADDRESS RTT_MS=$RTT_MS \
	ONE_WAY_DELAY_US=$ONE_WAY_DELAY_US LOSS_PERCENT=$LOSS_PERCENT SEED=$SEED \
	PEER_STREAM_LIMIT=$PEER_STREAM_LIMIT WORDLIST_PROFILE=$WORDLIST_PROFILE \
	THROTTLE_RPS=$THROTTLE_RPS THROTTLE_STATUS=$THROTTLE_STATUS \
	FAILURE_PROFILE=$FAILURE_PROFILE FAILURE_AFTER_MS=$FAILURE_AFTER_MS \
	FAILURE_DURATION_MS=$FAILURE_DURATION_MS CREATED_AT=$CREATED_AT \
	IP_BIN=$IP_BIN TC_BIN=$TC_BIN STAT_BIN=$STAT_BIN SCRIPT_PATH=$0 \
	"$PYTHON_BIN" - <<'PY'
import json, os, tempfile
from decimal import Decimal

e = os.environ
loss = Decimal(e["LOSS_PERCENT"])
netem = ["netem", "limit", "1000000", "delay", e["ONE_WAY_DELAY_US"] + "us"]
if loss != 0:
    netem += ["loss", "random", e["LOSS_PERCENT"] + "%", "seed", e["SEED"]]

host_qdisc = [e["TC_BIN"], "qdisc", "add", "dev", e["HOST_IF"], "root", "handle", "1:"] + netem
peer_qdisc = [e["IP_BIN"], "netns", "exec", e["NAMESPACE"], e["TC_BIN"], "qdisc", "add", "dev", e["PEER_IF"], "root", "handle", "1:"] + netem
apply_commands = [
    [e["IP_BIN"], "netns", "add", e["NAMESPACE"]],
    [e["IP_BIN"], "link", "add", e["HOST_IF"], "type", "veth", "peer", "name", e["PEER_IF"]],
    [e["IP_BIN"], "link", "set", "dev", e["HOST_IF"], "alias", e["OWNER_TAG"]],
    [e["IP_BIN"], "link", "set", "dev", e["PEER_IF"], "alias", e["OWNER_TAG"]],
    [e["IP_BIN"], "link", "set", "dev", e["PEER_IF"], "netns", e["NAMESPACE"]],
    [e["IP_BIN"], "address", "add", e["HOST_ADDRESS"], "dev", e["HOST_IF"]],
    [e["IP_BIN"], "netns", "exec", e["NAMESPACE"], e["IP_BIN"], "address", "add", e["PEER_ADDRESS"], "dev", e["PEER_IF"]],
    [e["IP_BIN"], "link", "set", "dev", e["HOST_IF"], "up"],
    [e["IP_BIN"], "netns", "exec", e["NAMESPACE"], e["IP_BIN"], "link", "set", "dev", "lo", "up"],
    [e["IP_BIN"], "netns", "exec", e["NAMESPACE"], e["IP_BIN"], "link", "set", "dev", e["PEER_IF"], "up"],
    host_qdisc,
    peer_qdisc,
]
cleanup_commands = [
    [e["TC_BIN"], "qdisc", "del", "dev", e["HOST_IF"], "root"],
    [e["IP_BIN"], "netns", "exec", e["NAMESPACE"], e["TC_BIN"], "qdisc", "del", "dev", e["PEER_IF"], "root"],
    [e["IP_BIN"], "link", "del", "dev", e["HOST_IF"]],
    [e["IP_BIN"], "netns", "del", e["NAMESPACE"]],
]
failure_enabled = e["FAILURE_PROFILE"] != "none"
value = {
    "schema_version": 1,
    "manifest_type": "h3fuzz-network-matrix-profile",
    "profile_id": e["PROFILE_ID"],
    "owner": {
        "token": e["OWNER_TOKEN"],
        "interface_alias": e["OWNER_TAG"],
        "ownership_model": "profile-record+namespace-inode+interface-alias",
    },
    "lifecycle": "prepared",
    "created_at": e["CREATED_AT"],
    "updated_at": e["CREATED_AT"],
    "platform_requirement": {
        "kernel": "Linux",
        "privilege": "effective UID 0",
        "capability": "CAP_NET_ADMIN",
        "additional_namespace_capability": "CAP_SYS_ADMIN",
        "native_linux_only": True,
    },
    "network": {
        "namespace": e["NAMESPACE"],
        "namespace_inode": None,
        "subnet": e["SUBNET"],
        "host_address": e["HOST_ADDRESS"],
        "peer_address": e["PEER_ADDRESS"],
        "host_interface": e["HOST_IF"],
        "peer_interface": e["PEER_IF"],
        "qdisc": "netem",
        "qdisc_handle": "1:",
        "qdisc_packet_limit": 1000000,
        "rtt_ms": str(Decimal(e["RTT_MS"])),
        "listed_latency_semantics": "round_trip_time",
        "one_way_delay_us": int(e["ONE_WAY_DELAY_US"]),
        "directions": [
            {"name": "client_to_server", "egress_interface": e["HOST_IF"], "delay_us": int(e["ONE_WAY_DELAY_US"])},
            {"name": "server_to_client", "namespace": e["NAMESPACE"], "egress_interface": e["PEER_IF"], "delay_us": int(e["ONE_WAY_DELAY_US"])},
        ],
        "loss": {
            "model": "netem_random",
            "configured_percent": str(loss),
            "configured_seed": int(e["SEED"]),
            "seed_applied": loss != 0,
            "observed_percent": None,
            "observation_required": True,
            "probabilistic": True,
        },
    },
    "workload": {
        "peer_stream_limit": {
            "configured": int(e["PEER_STREAM_LIMIT"]),
            "origin_argv": ["-max-incoming-streams", e["PEER_STREAM_LIMIT"]],
            "origin_acknowledgement_required": True,
        },
        "wordlist_profile": e["WORDLIST_PROFILE"],
        "server_throttle": {
            "requests_per_second": int(e["THROTTLE_RPS"]),
            "response_status": int(e["THROTTLE_STATUS"]),
            "enabled": int(e["THROTTLE_RPS"]) > 0,
            "enforcement": "external_origin_orchestrator",
        },
        "transient_failure": {
            "profile": e["FAILURE_PROFILE"],
            "enabled": failure_enabled,
            "after_ms": int(e["FAILURE_AFTER_MS"]),
            "duration_ms": int(e["FAILURE_DURATION_MS"]),
            "mechanism": "external_orchestrator_link_down_up" if failure_enabled else "none",
        },
    },
    "commands": {
        "apply": apply_commands,
        "cleanup": cleanup_commands,
        "namespace_exec_prefix": [e["IP_BIN"], "netns", "exec", e["NAMESPACE"]],
    },
    "evidence": {"before": None, "applied": None, "observed": None, "after": None, "executed_commands": "commands-executed.jsonl"},
}
path = e["MANIFEST_PATH"]
fd, temporary = tempfile.mkstemp(prefix=".profile.", dir=os.path.dirname(path))
try:
    with os.fdopen(fd, "w", encoding="utf-8") as output:
        json.dump(value, output, indent=2, sort_keys=True)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
}

capture_optional() {
	local output=$1
	shift
	if "$@" >"$output" 2>/dev/null; then
		return 0
	fi
	printf '[]\n' >"$output"
	return 1
}

write_snapshot() {
	local phase=$1
	local destination="$PROFILE_DIR/$phase.json"
	local temporary_dir="$PROFILE_DIR/.$phase-snapshot.$$"
	mkdir -- "$temporary_dir"

	"$IP_BIN" netns list >"$temporary_dir/namespaces.txt" 2>"$temporary_dir/namespaces.stderr" || true
	local ns_present=false
	local host_present=false
	local peer_present=false
	local inode=
	if namespace_exists "$NAMESPACE"; then
		ns_present=true
		inode=$(namespace_inode "$NAMESPACE" || true)
	fi
	if host_link_exists "$HOST_IF"; then
		host_present=true
		capture_optional "$temporary_dir/host-link.json" "$IP_BIN" -j -d link show dev "$HOST_IF" || true
		capture_optional "$temporary_dir/host-qdisc.json" "$TC_BIN" -j -s qdisc show dev "$HOST_IF" || true
	else
		printf '[]\n' >"$temporary_dir/host-link.json"
		printf '[]\n' >"$temporary_dir/host-qdisc.json"
	fi
	if [[ "$ns_present" == true ]] && peer_link_exists "$NAMESPACE" "$PEER_IF"; then
		peer_present=true
		capture_optional "$temporary_dir/peer-link.json" "$IP_BIN" netns exec "$NAMESPACE" "$IP_BIN" -j -d link show dev "$PEER_IF" || true
		capture_optional "$temporary_dir/peer-qdisc.json" "$IP_BIN" netns exec "$NAMESPACE" "$TC_BIN" -j -s qdisc show dev "$PEER_IF" || true
	else
		printf '[]\n' >"$temporary_dir/peer-link.json"
		printf '[]\n' >"$temporary_dir/peer-qdisc.json"
	fi

	"$PYTHON_BIN" - "$destination" "$phase" "$PROFILE_ID" "$NAMESPACE" "$ns_present" "${inode:-}" \
		"$HOST_IF" "$host_present" "$PEER_IF" "$peer_present" "$temporary_dir" <<'PY'
import datetime, json, os, sys, tempfile

(destination, phase, profile_id, namespace, ns_present, inode,
 host_if, host_present, peer_if, peer_present, source_dir) = sys.argv[1:]

def load_json(name):
    with open(os.path.join(source_dir, name), encoding="utf-8") as source:
        return json.load(source)

with open(os.path.join(source_dir, "namespaces.txt"), encoding="utf-8") as source:
    namespace_listing = source.read().splitlines()
with open(os.path.join(source_dir, "namespaces.stderr"), encoding="utf-8") as source:
    namespace_stderr = source.read()
host_qdisc = load_json("host-qdisc.json")
peer_qdisc = load_json("peer-qdisc.json")

def observed_loss(qdiscs):
    netem = [item for item in qdiscs if item.get("kind") == "netem"]
    packets = sum(int(item.get("packets", 0)) for item in netem)
    drops = sum(int(item.get("drops", 0)) for item in netem)
    denominator = packets + drops
    return {
        "available": bool(netem),
        "packets": packets,
        "drops": drops,
        "percent": (drops * 100.0 / denominator) if denominator else None,
        "basis": "tc_netem_drops/(tc_netem_packets+tc_netem_drops)",
    }

value = {
    "schema_version": 1,
    "manifest_type": "h3fuzz-network-matrix-state",
    "phase": phase,
    "profile_id": profile_id,
    "captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
    "namespace": {
        "name": namespace,
        "present": ns_present == "true",
        "inode": int(inode) if inode else None,
        "listing": namespace_listing,
        "listing_stderr": namespace_stderr,
    },
    "interfaces": {
        "host": {
            "name": host_if,
            "present": host_present == "true",
            "link": load_json("host-link.json"),
            "qdisc": host_qdisc,
            "observed_loss": observed_loss(host_qdisc),
        },
        "peer": {
            "name": peer_if,
            "namespace": namespace,
            "present": peer_present == "true",
            "link": load_json("peer-link.json"),
            "qdisc": peer_qdisc,
            "observed_loss": observed_loss(peer_qdisc),
        },
    },
}
fd, temporary = tempfile.mkstemp(prefix="." + phase + ".", dir=os.path.dirname(destination))
try:
    with os.fdopen(fd, "w", encoding="utf-8") as output:
        json.dump(value, output, indent=2, sort_keys=True)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    os.chmod(temporary, 0o600)
    os.replace(temporary, destination)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
	rm -f -- "$temporary_dir/namespaces.txt" "$temporary_dir/namespaces.stderr" \
		"$temporary_dir/host-link.json" "$temporary_dir/host-qdisc.json" \
		"$temporary_dir/peer-link.json" "$temporary_dir/peer-qdisc.json"
	rmdir -- "$temporary_dir"
}

verify_netem_state() {
	local host_file="$PROFILE_DIR/.verify-host-qdisc.$$"
	local peer_file="$PROFILE_DIR/.verify-peer-qdisc.$$"
	"$TC_BIN" -j -s qdisc show dev "$HOST_IF" >"$host_file"
	"$IP_BIN" netns exec "$NAMESPACE" "$TC_BIN" -j -s qdisc show dev "$PEER_IF" >"$peer_file"
	"$PYTHON_BIN" - "$host_file" "$peer_file" <<'PY'
import json, sys
for path in sys.argv[1:]:
    with open(path, encoding="utf-8") as source:
        qdiscs = json.load(source)
    if not any(item.get("kind") == "netem" and item.get("handle") == "1:" for item in qdiscs):
        raise SystemExit("expected owned netem qdisc handle 1: is absent")
PY
	local rc=$?
	rm -f -- "$host_file" "$peer_file"
	return "$rc"
}

load_manifest() {
	[[ -d "$PROFILE_DIR" && ! -L "$PROFILE_DIR" ]] || refuse "profile state directory is missing or unsafe"
	[[ -f "$MANIFEST_PATH" && ! -L "$MANIFEST_PATH" ]] || refuse "unknown profile: $PROFILE_ID"
	local record_file="$PROFILE_DIR/.record.$$"
	if ! "$PYTHON_BIN" - "$MANIFEST_PATH" "$PROFILE_ID" >"$record_file" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    value = json.load(source)
if value.get("schema_version") != 1 or value.get("manifest_type") != "h3fuzz-network-matrix-profile":
    raise SystemExit(1)
if value.get("profile_id") != sys.argv[2]:
    raise SystemExit(1)
fields = [
    value["owner"]["token"],
    value["owner"]["interface_alias"],
    value["network"]["namespace"],
    str(value["network"].get("namespace_inode") or ""),
    value["network"]["host_interface"],
    value["network"]["peer_interface"],
    value["lifecycle"],
]
if any("\n" in field for field in fields):
    raise SystemExit(1)
print("\n".join(fields))
PY
	then
		rm -f -- "$record_file"
		refuse "profile record is invalid or does not own the requested ID"
	fi
	local index=0 line
	while IFS= read -r line; do
		case "$index" in
			0) OWNER_TOKEN=$line ;;
			1) OWNER_TAG=$line ;;
			2) NAMESPACE=$line ;;
			3) NAMESPACE_INODE=$line ;;
			4) HOST_IF=$line ;;
			5) PEER_IF=$line ;;
			6) PROFILE_LIFECYCLE=$line ;;
		esac
		index=$((index + 1))
	done <"$record_file"
	rm -f -- "$record_file"
	[[ "$index" == 7 && -n "$OWNER_TOKEN" && -n "$OWNER_TAG" && -n "$NAMESPACE" && -n "$HOST_IF" && -n "$PEER_IF" ]] || \
		refuse "profile record is incomplete"
	[[ "$OWNER_TOKEN" =~ ^[0-9a-f]{32}$ ]] || refuse "profile owner token is invalid"
	[[ "$OWNER_TAG" == "$OWNER_PREFIX:$PROFILE_ID:$OWNER_TOKEN" ]] || refuse "profile owner alias is invalid"
	[[ -z "$NAMESPACE_INODE" || "$NAMESPACE_INODE" =~ ^[0-9]+$ ]] || refuse "profile namespace inode is invalid"
	local digest suffix
	digest=$(profile_hash)
	suffix=${digest:0:10}
	[[ "$NAMESPACE" == "h3n-$suffix" && "$HOST_IF" == "h3h-$suffix" && "$PEER_IF" == "h3p-$suffix" ]] || \
		refuse "profile resource names do not derive from the requested profile ID"
	case "$PROFILE_LIFECYCLE" in
		prepared|applying|active|cleaning|cleaned|cleanup_residue|apply_failed_rolled_back|apply_failed_cleanup_residue) ;;
		*) refuse "profile lifecycle is invalid" ;;
	esac
}

verify_current_ownership() {
	local host_present=0
	local ns_present=0
	local peer_present=0
	if host_link_exists "$HOST_IF"; then
		host_present=1
		local observed_host_alias
		observed_host_alias=$(link_alias_host "$HOST_IF") || refuse "cannot inspect host interface ownership"
		[[ "$observed_host_alias" == "$OWNER_TAG" ]] || refuse "host interface is owned by a different profile"
	fi
	if namespace_exists "$NAMESPACE"; then
		ns_present=1
		local observed_inode
		observed_inode=$(namespace_inode "$NAMESPACE") || refuse "cannot inspect namespace ownership"
		[[ -n "$NAMESPACE_INODE" && "$observed_inode" == "$NAMESPACE_INODE" ]] || \
			refuse "namespace inode does not match the profile owner"
		if peer_link_exists "$NAMESPACE" "$PEER_IF"; then
			peer_present=1
			local observed_peer_alias
			observed_peer_alias=$(link_alias_peer "$NAMESPACE" "$PEER_IF") || refuse "cannot inspect peer interface ownership"
			[[ "$observed_peer_alias" == "$OWNER_TAG" ]] || refuse "peer interface is owned by a different profile"
		fi
	fi
	CURRENT_HOST_PRESENT=$host_present
	CURRENT_NS_PRESENT=$ns_present
	CURRENT_PEER_PRESENT=$peer_present
}

print_status() {
	local status_snapshot="$PROFILE_DIR/.status.$$"
	write_snapshot status
	mv -- "$PROFILE_DIR/status.json" "$status_snapshot"
	"$PYTHON_BIN" - "$MANIFEST_PATH" "$status_snapshot" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    profile = json.load(source)
with open(sys.argv[2], encoding="utf-8") as source:
    live = json.load(source)
print(json.dumps({"profile": profile, "live_state": live}, indent=2, sort_keys=True))
PY
	rm -f -- "$status_snapshot"
}

cleanup_created_resources() {
	# This function is used only by the apply process itself.  Its creation
	# flags are stronger ownership evidence than aliases that might not yet
	# have been installed when a partial command failed.
	set +e
	if (( PEER_QDISC_CREATED == 1 )) && namespace_exists "$NAMESPACE"; then
		run_mutation rollback "$IP_BIN" netns exec "$NAMESPACE" "$TC_BIN" qdisc del dev "$PEER_IF" root >/dev/null 2>&1
	fi
	if (( HOST_QDISC_CREATED == 1 )) && host_link_exists "$HOST_IF"; then
		run_mutation rollback "$TC_BIN" qdisc del dev "$HOST_IF" root >/dev/null 2>&1
	fi
	if (( HOST_CREATED == 1 )) && host_link_exists "$HOST_IF"; then
		run_mutation rollback "$IP_BIN" link del dev "$HOST_IF" >/dev/null 2>&1
	fi
	if (( NS_CREATED == 1 )) && namespace_exists "$NAMESPACE"; then
		run_mutation rollback "$IP_BIN" netns del "$NAMESPACE" >/dev/null 2>&1
	fi
	local residue=0
	host_link_exists "$HOST_IF" && residue=1
	namespace_exists "$NAMESPACE" && residue=1
	write_snapshot after || residue=1
	if (( residue == 1 )); then
		atomic_json_update apply_failed_cleanup_residue "${NAMESPACE_INODE:-}" || true
		set -e
		return "$EXIT_CLEANUP_RESIDUE"
	fi
	atomic_json_update apply_failed_rolled_back "${NAMESPACE_INODE:-}" || true
	set -e
	return 0
}

on_exit() {
	local rc=$?
	(( IN_EXIT_TRAP == 0 )) || return
	IN_EXIT_TRAP=1
	trap - EXIT
	if (( rc != 0 && ROLLBACK_NEEDED == 1 )); then
		if ! cleanup_created_resources; then
			rc=$EXIT_CLEANUP_RESIDUE
		fi
	fi
	release_lock
	exit "$rc"
}

trap on_exit EXIT
trap 'exit 130' HUP INT TERM

apply_profile() {
	acquire_lock
	[[ ! -e "$PROFILE_DIR" && ! -L "$PROFILE_DIR" ]] || refuse "profile ID already has a state record: $PROFILE_ID"
	derive_resources
	check_owned_subnet_collision
	if namespace_exists "$NAMESPACE"; then
		refuse "derived namespace collides with an existing namespace: $NAMESPACE"
	fi
	if host_link_exists "$HOST_IF"; then
		refuse "derived host interface collides with an existing interface: $HOST_IF"
	fi

	mkdir -- "$STATE_ROOT/profiles" 2>/dev/null || [[ -d "$STATE_ROOT/profiles" && ! -L "$STATE_ROOT/profiles" ]] || \
		refuse "cannot create profiles state directory"
	[[ ! -L "$STATE_ROOT/profiles" ]] || refuse "profiles state directory must not be a symbolic link"
	chmod 0700 "$STATE_ROOT/profiles"
	mkdir -- "$PROFILE_DIR" || refuse "cannot reserve profile state directory"
	chmod 0700 "$PROFILE_DIR"
	OWNER_TOKEN=$(
		"$PYTHON_BIN" - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
	)
	OWNER_TAG="$OWNER_PREFIX:$PROFILE_ID:$OWNER_TOKEN"
	CREATED_AT=$("$DATE_BIN" -u '+%Y-%m-%dT%H:%M:%SZ')
	: >"$COMMAND_LOG_PATH"
	chmod 0600 "$COMMAND_LOG_PATH"
	write_snapshot before
	create_manifest
	atomic_json_update prepared

	# The EXIT trap is already installed.  From this assignment onward, every
	# failure takes the owned-resource rollback path.
	ROLLBACK_NEEDED=1
	NS_CREATED=1
	run_mutation apply "$IP_BIN" netns add "$NAMESPACE"
	NAMESPACE_INODE=$(namespace_inode "$NAMESPACE")
	[[ "$NAMESPACE_INODE" =~ ^[0-9]+$ ]] || { say_error "cannot establish namespace inode ownership"; return 1; }
	atomic_json_update applying "$NAMESPACE_INODE"

	HOST_CREATED=1
	run_mutation apply "$IP_BIN" link add "$HOST_IF" type veth peer name "$PEER_IF"
	run_mutation apply "$IP_BIN" link set dev "$HOST_IF" alias "$OWNER_TAG"
	run_mutation apply "$IP_BIN" link set dev "$PEER_IF" alias "$OWNER_TAG"
	run_mutation apply "$IP_BIN" link set dev "$PEER_IF" netns "$NAMESPACE"
	run_mutation apply "$IP_BIN" address add "$HOST_ADDRESS" dev "$HOST_IF"
	run_mutation apply "$IP_BIN" netns exec "$NAMESPACE" "$IP_BIN" address add "$PEER_ADDRESS" dev "$PEER_IF"
	run_mutation apply "$IP_BIN" link set dev "$HOST_IF" up
	run_mutation apply "$IP_BIN" netns exec "$NAMESPACE" "$IP_BIN" link set dev lo up
	run_mutation apply "$IP_BIN" netns exec "$NAMESPACE" "$IP_BIN" link set dev "$PEER_IF" up

	local netem=(netem limit 1000000 delay "${ONE_WAY_DELAY_US}us")
	if [[ "$LOSS_PERCENT" != 0 && "$LOSS_PERCENT" != 0.0 && "$LOSS_PERCENT" != 0.00 ]]; then
		netem+=(loss random "${LOSS_PERCENT}%" seed "$SEED")
	fi
	HOST_QDISC_CREATED=1
	run_mutation apply "$TC_BIN" qdisc add dev "$HOST_IF" root handle 1: "${netem[@]}"
	PEER_QDISC_CREATED=1
	run_mutation apply "$IP_BIN" netns exec "$NAMESPACE" "$TC_BIN" qdisc add dev "$PEER_IF" root handle 1: "${netem[@]}"

	verify_netem_state
	local host_alias peer_alias
	host_alias=$(link_alias_host "$HOST_IF")
	peer_alias=$(link_alias_peer "$NAMESPACE" "$PEER_IF")
	[[ "$host_alias" == "$OWNER_TAG" && "$peer_alias" == "$OWNER_TAG" ]] || { say_error "applied interface ownership check failed"; return 1; }
	write_snapshot applied
	atomic_json_update active "$NAMESPACE_INODE"
	ROLLBACK_NEEDED=0
	cat "$MANIFEST_PATH"
}

status_profile() {
	acquire_lock
	load_manifest
	verify_current_ownership
	if [[ "$PROFILE_LIFECYCLE" == active ]]; then
		(( CURRENT_HOST_PRESENT == 1 && CURRENT_NS_PRESENT == 1 && CURRENT_PEER_PRESENT == 1 )) || \
			refuse "active profile is missing one or more owned resources"
		verify_netem_state || refuse "active profile is missing one or more owned netem qdiscs"
	fi
	print_status
}

cleanup_profile() {
	acquire_lock
	load_manifest
	verify_current_ownership

	if [[ "$PROFILE_LIFECYCLE" == cleaned ]]; then
		if (( CURRENT_HOST_PRESENT == 0 && CURRENT_NS_PRESENT == 0 )); then
			cat "$MANIFEST_PATH"
			return 0
		fi
		say_error "cleaned profile ID now collides with live resources; refusing to delete them"
		return "$EXIT_CLEANUP_RESIDUE"
	fi
	# Preserve the final traffic-control counters before removing the qdiscs.
	# This is the authoritative observed-loss sample for the completed profile.
	write_snapshot observed
	atomic_json_update cleaning "$NAMESPACE_INODE"

	set +e
	if (( CURRENT_HOST_PRESENT == 1 )); then
		run_mutation cleanup "$TC_BIN" qdisc del dev "$HOST_IF" root >/dev/null 2>&1 || true
	fi
	if (( CURRENT_NS_PRESENT == 1 )) && peer_link_exists "$NAMESPACE" "$PEER_IF"; then
		run_mutation cleanup "$IP_BIN" netns exec "$NAMESPACE" "$TC_BIN" qdisc del dev "$PEER_IF" root >/dev/null 2>&1 || true
	fi
	if host_link_exists "$HOST_IF"; then
		run_mutation cleanup "$IP_BIN" link del dev "$HOST_IF" >/dev/null 2>&1 || true
	fi
	if namespace_exists "$NAMESPACE"; then
		run_mutation cleanup "$IP_BIN" netns del "$NAMESPACE" >/dev/null 2>&1 || true
	fi
	set -e

	local residue=0
	host_link_exists "$HOST_IF" && residue=1
	namespace_exists "$NAMESPACE" && residue=1
	write_snapshot after || residue=1
	if (( residue == 1 )); then
		atomic_json_update cleanup_residue "$NAMESPACE_INODE"
		say_error "cleanup left owned network resources behind"
		return "$EXIT_CLEANUP_RESIDUE"
	fi
	# A harmless qdisc-delete error can occur when an operator already removed
	# the owned qdisc.  Absence of the exclusively owned interfaces/namespaces is
	# the authoritative cleanup result.
	atomic_json_update cleaned "$NAMESPACE_INODE"
	cat "$MANIFEST_PATH"
}

main() {
	if (( $# == 1 )) && [[ "$1" == -h || "$1" == --help || "$1" == help ]]; then
		usage
		return 0
	fi
	# Tools must be prepared before numeric validation because Decimal-based
	# validation intentionally uses the same pinned Python executable recorded
	# in profile commands.
	prepare_tools_and_platform
	parse_arguments "$@"
	prepare_state_root
	case "$ACTION" in
		apply) apply_profile ;;
		status) status_profile ;;
		cleanup) cleanup_profile ;;
	esac
}

main "$@"
