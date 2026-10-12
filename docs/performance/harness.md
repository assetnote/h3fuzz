# HTTP/3 correctness and throughput harness

[Performance documentation](README.md)

This harness exercises HTTP/3 discovery, safe HEAD/Range selection, fallback,
and sustained request throughput against a pinned official Caddy build. It has
two deliberately separate purposes:

- `Caddyfile.instrumented` enables Caddy counters for correctness and request
  accounting.
- `Caddyfile.ceiling` removes metrics overhead for transport-ceiling runs.

No access log is enabled in either mode. Static fixtures are baked into the
container, so Docker Desktop bind-mount I/O is not part of the benchmark.

## Prerequisites

- Docker Compose v2
- Bash and Python 3
- A built h3fuzz binary (default: `./h3fuzz` at repository root)

Build the application and start the portable instrumented origin:

```sh
go build -o ./h3fuzz .
docker compose -f harness/compose.yaml up --build --wait
```

The Caddy base is pinned to `caddy:2.11.4-alpine` and its registry digest in
`Dockerfile`. Updating it should be an explicit, reviewed change.

## Protocol topology

| URL | Published transport | Expected result |
| --- | --- | --- |
| `https://localhost:8443` | TCP and UDP; H1/H2/H3 | H3 supported and high-rate-ready |
| `https://localhost:8444` | TCP; H1/H2 only | H3 unsupported |
| `https://localhost:8445` | TCP only; internal H3 UDP is unpublished | bounded H3 failure/fallback |
| `https://localhost:8446` | UDP; H3 only | direct H3 succeeds without TCP bootstrap |
| `http://localhost:2019` | TCP, loopback only | Caddy admin and optional metrics |

Caddy uses its internal CA and does not alter the host trust store. Harness
commands pass `-k`. To exercise verified TLS separately, copy the root CA from
`/data/caddy/pki/authorities/local/root.crt` and explicitly configure a test
client to trust that file.

Run the protocol checks:

```sh
harness/scripts/smoke.sh
```

`-http3-fingerprint` is expected to emit one JSON object, exit 0 for a usable
H3 origin, exit 2 for an unsupported/unsuitable origin, and keep diagnostics on
stderr. Smoke artifacts are retained under `harness/.work/`.

## Response-optimization correctness

The route matrix includes:

- a normal static origin;
- HEAD that falsely returns 200 for every path;
- HEAD blocked with 405;
- a route that qualifies HEAD globally but rejects it for one real candidate;
- a route that qualifies Range globally but rejects it for one real candidate;
- Range silently ignored;
- a plausible one-byte 206 returned for missing paths as well as real files;
- an origin where neither optimization is safe;
- zero-byte, one-byte, 1 MiB, redirecting-directory, and missing resources.

Run full-GET versus automatic-mode result-set comparisons:

```sh
harness/scripts/correctness.sh
```

The finding set must be identical. Metadata differences are reported because
a legitimate Range request returns 206 rather than the original 200. Body-based
ffuf behavior is a separate safety constraint: `-fw`, `-fl`, `-mr`, `-fr`, and
other content-dependent behavior cannot be reproduced from an empty HEAD or a
one-byte Range. Automatic optimization must use full GET when those features
are active. Full size may only be synthesized from a validated `Content-Length`
or `Content-Range` total.

## Wordlist handling

The checked-in `final.txt` has 233,582 unique non-empty entries. Of these,
116,796 contain literal `%EXT%`, which is not a valid Go URL escape. The harness
expands it deterministically without changing the source file:

```sh
harness/scripts/normalize-wordlist.sh final.txt /tmp/final.php.txt
harness/scripts/normalize-wordlist.sh --repeat 14 final.txt /tmp/final-30s.txt
```

One-round expansion to `php` remains 233,582 unique entries. Repeated rounds are
prefixed by default so every generated URL is distinct. Use `--exact-repeats`
only when intentionally measuring a hot repeated path.

## Benchmarking

First perform an instrumented accounting run:

```sh
harness/scripts/benchmark.sh --repeat 1 --mode auto --connections 0
```

The wrapper records wall time, logical inputs, the application exit status,
and per-series Caddy request-counter deltas. It also asks h3fuzz to write an
atomic `run-telemetry.json` after shutdown. That file distinguishes logical
completed responses from HTTP/3 transport attempts (which can include probes
and retries), records the selected protocol and response strategy, and proves
that workers, stream admission, and the transport drained cleanly. For automatic
HEAD/Range it also accounts for every optimized attempt as either a validated
success or a reasoned full-GET fallback (transport error versus untrustworthy
response).

`benchmark-report.json` sets `run_valid` (also exposed as
`run_gates_passed`) only when the process exits
successfully, HTTP/3 and the requested mode are proven, every input is accounted
for as one terminal outcome, the per-run error-rate gates pass, and drain is complete.
A selected automatic optimization must have at least one validated success, no
unexplained fallbacks, and a full-GET fallback rate no greater than 0.1%.
Its optimized-attempt count must equal scheduled inputs plus recovered retries;
Legacy telemetry still requires zero surfaced errors and retries. Version 2
telemetry keeps terminal logical errors, application retries, and surfaced
attempt errors separate and applies the documented strict-below-0.1% gates.
A failed proof makes `benchmark.sh` exit non-zero while retaining all artifacts.
The per-run `certification_eligible` field remains false: certification requires
the duration, repeated-run median, server correlation, and UDP-drop evidence in
the native-Linux profile below. `logical_rps` remains inputs divided by process wall time;
For telemetry schema version 2, `completed_response_rps` is successful logical
responses divided by the monotonic main-request span; preflight, warmup,
qualification, and shutdown are excluded. Schema version 1 retains its labeled
legacy wall-time calculation. Caddy correlation is supporting evidence rather
than the client completion counter.

Compare explicit safe modes and connection counts as needed:

```sh
harness/scripts/benchmark.sh --repeat 1 --mode get --connections 8
harness/scripts/benchmark.sh --repeat 1 --mode head --connections 8
harness/scripts/benchmark.sh --repeat 1 --mode range --connections 8
harness/scripts/benchmark.sh --repeat 1 --mode auto --connections 0 --max-connections 64
```

Automatic mode requires an exact expected strategy so a silent fallback to GET
cannot pass validation. The standard `/bench/` and `/large/` routes default to
`HEAD`; for another route, pass `--expect-strategy GET`, `HEAD`, or `RANGE` based
on its documented behavior.

`/bench/FUZZ` is an empty constant-response transport ceiling. Also report
`/files/FUZZ` for real file discovery and `/large/FUZZ` when demonstrating the
bandwidth reduction from HEAD or `Range: bytes=-1`; do not present the ceiling
route as filesystem discovery throughput. The script automatically filters 204
on `/bench/`, 200/206 on `/large/`, and 404 elsewhere to prevent per-result
rendering from becoming the bottleneck; override this with `--filter-status`.

For an uninstrumented run, recreate Caddy with the ceiling configuration:

```sh
H3FUZZ_CADDYFILE=Caddyfile.ceiling \
  docker compose -f harness/compose.yaml up -d --force-recreate --wait
harness/scripts/benchmark.sh --repeat 14 --mode auto --connections 0
```

Fourteen rounds are 3,270,148 requests; the measured rate determines how long
that run takes. Never substitute queued/dispatched input counts for completed
RPS or claim a throughput threshold that the resulting report did not measure.

## Native-Linux certification profile

The separate host-network file removes Docker NAT/port publishing:

```sh
docker compose -f harness/compose.yaml down
docker compose -f harness/compose.host-network.yaml up --build --wait
```

Use it on native Linux, pin client and server to disjoint CPU sets, and raise
the host limits recommended by quic-go when appropriate:

```text
net.core.rmem_max=7340032
net.core.wmem_max=7340032
```

Do not have the harness mutate these host settings automatically. Record the
kernel, CPU, Docker version, image digest, h3fuzz commit, Go/quic-go versions,
RTT, wordlist hash, connection settings, UDP drop counters, and exact command.

PERF-003's read-only collectors write atomic, owner-readable evidence. Paths
passed to `--output` must be absolute. A stable server snapshot is produced
with explicit origin identities so a missing binary, image, or config cannot be
mistaken for evidence:

```sh
harness/scripts/environment-snapshot.sh \
  --kind server_stable \
  --output /absolute/trial/server-stable.json \
  --source-root "$PWD" \
  --binary "$PWD/h3fuzz" \
  --origin-binary /absolute/origin \
  --origin-image sha256:... \
  --origin-config /absolute/origin-config.json \
  --harness-image h3fuzz-harness:local \
  --command-json /absolute/trial/command.json \
  --environment-json /absolute/trial/environment-overrides.json \
  --debug-log /absolute/trial/h3fuzz-debug.log \
  --target https://origin.example:8443/bench/FUZZ \
  --wordlist /absolute/words.txt \
  --repeat-mode exact:14
```

`command.json` is the exact argv array and `environment-overrides.json` is an
object containing only overrides relevant to the run. Secret-like argument and
environment names are redacted. The source identity hashes current tracked
content, uncommitted edits, file modes, and non-ignored untracked inputs; the
dirty flag is supporting metadata, not the identity. On non-Linux hosts a
`client_stable` snapshot is retained with `eligible: false` and explicit
missing-evidence reasons.

Capture server or client points before and after a trial, then sample the
client PID after pool warmup and during the main phase:

```sh
harness/scripts/network-snapshot.sh \
  --mode point --kind server_point --phase before \
  --run-id "$RUN_ID" --process origin:"$ORIGIN_PID" \
  --output /absolute/trial/server-before.json

harness/scripts/network-snapshot.sh \
  --mode runtime-sockets --phase main \
  --run-id "$RUN_ID" --process h3fuzz:"$H3FUZZ_PID" \
  --output /absolute/trial/client-runtime-sockets.jsonl --append

harness/scripts/network-snapshot.sh \
  --mode point --kind server_point --phase after \
  --run-id "$RUN_ID" --process origin:"$ORIGIN_PID" \
  --output /absolute/trial/server-after.json
```

Runtime sampling uses PID-attributed `ss -u -m -p -e` data. Its `rb` and `tb`
limits are recorded as raw Linux socket-buffer values and compared with
quic-go v0.63.0's 7,340,032-byte target; Linux's buffer-doubling convention is
recorded separately. A below-target runtime sample is still written but exits
with status 3. Repeated calls need `--append`; optional
`--socket-identity INODE=slot:N` or `INODE=shared:NAME` ties sockets to pool
identity when the caller has it.

Use comparison mode to classify arbitrary-precision deltas, resets, and wraps.
Only the top-five-percent to bottom-five-percent transition of the declared
counter width is a wrap. Declare a dedicated host or isolated namespace, or
provide a same-length idle pair; comparison never silently subtracts ambient
traffic:

```sh
harness/scripts/network-snapshot.sh \
  --mode compare \
  --before /absolute/trial/server-before.json \
  --after /absolute/trial/server-after.json \
  --dedicated-host \
  --output /absolute/trial/server-network-delta.json
```

The scripts never change sysctls and contain no SSH credentials. The committed
schemas are `schemas/server-stable-v1.schema.json`,
`schemas/server-point-v1.schema.json`, and
`schemas/client-runtime-socket-v1.schema.json`; additive fields remain allowed.

A defensible local milestone is the median of five runs at or above 100,000
completed responses/second, each measured for at least 30 seconds, with under
0.1% transport errors, no increasing UDP-drop counter, and an instrumented run
whose server/client counts differ by under 0.1%. Automatic tuning should reach
at least 90% of the best safe explicit connection/method sweep on the same host.

PortSwigger reports 100k RPS from a laptop to a remote origin over Wi-Fi and
about 180k from a same-region cloud host, but does not publish enough hardware
and origin detail for an exact scientific reproduction. Docker Desktop also
proxies published networking through a VM/backend. On macOS, enable Docker
Desktop's `Use kernel networking for UDP` option. Treat Desktop results as
functional and relative-regression evidence, not a universal 100k release gate.

## PERF-004 calibration and certification

Certification uses an immutable, seeded wordlist. Calibration runs at most
three unmeasured sizing attempts and freezes the first clean file whose main
request span reaches 30 seconds. After a short attempt, its next expanded input
count is exactly
`ceil(max(target_rps, fastest_clean_observed_rps) * 30 * 1.20)`. Calibration
attempts never enter either five-run cohort.

```sh
harness/scripts/calibrate-wordlist.sh \
  --target https://origin.example:8443/bench/FUZZ \
  --base-wordlist /data/h3fuzz/final.txt --seed 1001 \
  --target-rps 100000 --target-seconds 30 --headroom-percent 20 \
  --expect-strategy head --filter-status 204 \
  --streams 256 --max-connections 64 --max-workers 65536 \
  --server-snapshot-provider /opt/h3fuzz/origin-snapshot-provider \
  --server-evidence-provider /opt/h3fuzz/server-evidence-provider \
  --output /data/h3fuzz/frozen-certification.txt \
  --manifest /data/h3fuzz/frozen-certification.manifest.json \
  --artifact-dir /data/h3fuzz/calibration
```

The manifest records the exact seed, generator version, base-corpus hash,
frozen line and expanded-input counts, wordlist hash, every calibration report,
and every formula input. With `--extensions`, calibration requires an
executable `H3FUZZ_INPUT_COUNT_PROVIDER`; it refuses to guess h3fuzz expansion.
Outputs are create-only and atomically installed.

`baseline-report.py` creates immutable B0 or B1 manifests from at least five
clean `baseline` reports. B0 freezes
`100 * 1.4826 * median(abs(r - median(r))) / median(r)` as its noise band. B1
may reference B0 for drift evidence but cannot rewrite that policy.

```sh
python3 harness/scripts/baseline-report.py \
  --baseline B0 \
  --report /data/h3fuzz/b0/run-01/benchmark-report.json \
  --report /data/h3fuzz/b0/run-02/benchmark-report.json \
  --report /data/h3fuzz/b0/run-03/benchmark-report.json \
  --report /data/h3fuzz/b0/run-04/benchmark-report.json \
  --report /data/h3fuzz/b0/run-05/benchmark-report.json \
  --output /data/h3fuzz/b0/baseline-manifest.json --require-eligible
```

The canonical certification command accepts no connection, stream, worker, or
trial-count overrides. It verifies the content hashes and deterministic
selection calculation in the fixed-sweep manifest, then takes the selected
streams, fixed connections, adaptive maximum connections, and maximum workers
from that manifest. The files-validation and diagnostic manifests are mandatory;
diagnostics must contain paired controls for exactly CPU, heap, mutex, block,
and QUIC. Missing Phase-5 producers are prerequisite failures, not waived gates.

All three prerequisite manifests use schema version 1, a self
`manifest_sha256`, and an exact `members` path/SHA-256 inventory relative to the
manifest directory; unlisted files are rejected. The fixed sweep records real
`platform_key`, `client_build_key`, and `workload_key` hashes plus `authority`
and `server_identity`. Every point records its `treatment_key`, `cohort_key`,
and exactly three hashed sweep reports whose canonical keys and fixed
connection/stream treatment agree with that point. Its selected point also
records the selected treatment and cohort keys.

The files-validation provider contract records a distinct `/files/FUZZ`
`workload_key`, the shared platform/build/authority/server identity, hashed
`runs.exact_hit` and `runs.long_corpus` references, nonempty
`duplicate_accounting`, and correctness counts proving expected hits equal
actual hits, zero false positives, and GET-control filter equivalence. The
diagnostics contract records the selected sweep point and shared keys. Each of
the exact five profile entries supplies one profiled and one control report
reference, `[profile-name]` versus empty profile flags, distinct canonical
treatment keys, and `paired_control: true`. These strict contracts intentionally
reject placeholder manifests until the Phase-5 producers emit complete
evidence.

```sh
H3FUZZ_NETWORK_ATTRIBUTION=dedicated-host \
H3FUZZ_NETWORK_PATH_ID=same-region-host-network \
harness/scripts/certify.sh \
  --target https://origin.example:8443/bench/FUZZ \
  --wordlist /data/h3fuzz/frozen-certification.txt \
  --wordlist-manifest /data/h3fuzz/frozen-certification.manifest.json \
  --expect-strategy head --filter-status 204 \
  --fixed-sweep-manifest /data/h3fuzz/fixed-sweep/fixed-sweep-manifest.json \
  --files-validation-manifest /data/h3fuzz/files-validation/manifest.json \
  --diagnostics-manifest /data/h3fuzz/diagnostics/manifest.json \
  --target-rps 100000 \
  --server-snapshot-provider /opt/h3fuzz/origin-snapshot-provider \
  --server-evidence-provider /opt/h3fuzz/server-evidence-provider \
  --artifact-store /mnt/durable/h3fuzz-certifications
```

The strict single-trial path uses the versioned harness mode, run ID, and phase
file flags. It samples live client UDP sockets, calls the server providers by
argv, and requires the origin correlation report for the marked route and H3
protocol. Until the client injects the specified harness marker into main
requests, route-specific counters cannot reconcile and the trial fails closed.

Every aggregate contains five adaptive and five selected-fixed reports. Each
run must independently pass duration, ledger, error-rate, server-reconciliation,
UDP-attribution, protocol, strategy, and drain gates. The headline gate uses
the median completed-response RPS—not `logical_rps`—and requires at least
100,000 RPS and 90% of the selected-fixed median. `run_gates_passed`,
`eligible_for_aggregation`, and aggregate-only `certification_eligible` are
deliberately distinct.

Raw evidence is staged only under ignored `harness/.work`; successful retention
atomically installs an immutable directory in `--artifact-store`. The raw and
separately allowlisted/redacted public archives each have a content manifest
that excludes itself from its member list and a final archive SHA-256. Exit
codes are stable: 0 passed, 1 internal error, 2 invalid/missing prerequisite,
3 completed but ineligible, 4 benchmark/provider/cleanup failure, and 5
retention or hashing failure.

## Validation and cleanup

Static validation does not require a running Docker daemon:

```sh
harness/scripts/validate.sh
```

It checks shell/Python syntax, Compose models when the CLI is available,
the PERF-004 report/calibration/certification fixtures, `final.txt`'s expected
hash and normalized shape, and Caddy adaptation when a
local `caddy` binary exists. Runtime validation still requires Docker.

```sh
docker compose -f harness/compose.yaml down
```

Named Caddy volumes intentionally retain the local CA between restarts. Add
`--volumes` only when deliberately resetting it.

Primary implementation references:

- [PortSwigger HTTP/3 research](https://portswigger.net/research/http3-in-burp-suite)
- [Caddy protocol options](https://caddyserver.com/docs/caddyfile/options)
- [Caddy metrics](https://caddyserver.com/docs/metrics)
- [quic-go optimization guidance](https://quic-go.net/docs/quic/optimizations/)
- [Docker host networking](https://docs.docker.com/engine/network/drivers/host/)
- [RFC 9114](https://www.rfc-editor.org/rfc/rfc9114.html)
