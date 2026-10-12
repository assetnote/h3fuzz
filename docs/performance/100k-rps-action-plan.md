# 100k RPS implementation action plan

[Performance documentation](README.md)

Status: PERF-202 passed; separated native-Linux certification
infrastructure-blocked

Phase 1, the A42 prepared-header integration, and selected Phase 2 audits are
complete; PERF-202 and PERF-205 passed, and PERF-203 is
hypothesis-disproved/skipped.

Prepared: 2026-10-11

Implementation update: the Phase 1 adaptive-worker, connection-generation, and
explicit high-rate policy work is complete. Automatic HTTP/3 mode deliberately
seeds 8 connections, then can grow 8 to 16 to 32, rather than copying Turbo
Intruder's 10-connection default. The regular-file wordlist arena has also
passed an isolated current-source native-Linux A/B at a shared-host fixed-4
diagnostic point. Portable producers now cover deterministic fixed-sweep
selection, resource evidence, `/files` validation, paired diagnostics, and
alternating certification cohorts. The controlled network matrix has been
implemented and tested through fake command boundaries, but it has not run a
live namespace or `tc` profile. Fresh current-source CPU/allocation profiles and
a restricted direct-RoundTripper A/B are also retained. The latter missed its
predeclared 3% RPS gate and is rejected. A42 then integrated the narrower
prepared-header seam and sustained a 114,578.588 RPS median in five ordinary
local adaptive-default runs. PERF-203 subsequently rejected a matcher fast path
because the retained profile attributes only 0.649169% cumulative CPU to
matcher evaluation, while PERF-205 completed its ranked pipeline audit without
accepting another product change. PERF-202 now removes trace allocation only
from telemetry-off eligible automatic HEAD/Range requests; exact timing remains
on telemetry, generic, rejected, and fallback paths. These implementation and
diagnostic results do
not replace the required separated-host native-Linux matrix and release
certification. The PERF task problem statements below are retained as design
context; each status paragraph distinguishes shipped tooling from live
evidence.

## Objective

Make h3fuzz capable of demonstrating at least 100,000 completed HTTP/3
responses per second under a controlled, reproducible workload while preserving
content-discovery correctness, safe HEAD and Range selection, transport error
visibility, and ffuf compatibility.

This is a performance engineering plan, not a promise that every remote target
can reach 100k. The target server, round-trip time, packet loss, UDP buffers,
CPU, and response behavior can impose a lower ceiling. The implementation must
distinguish a client limit from a server or network limit.

Read [HTTP/3 implementation and 100k RPS analysis](http3-100k-analysis.md)
before changing code. That audit is the technical basis for this plan.
Its source baseline is PortSwigger's
[HTTP/3 research](https://portswigger.net/research/http3-in-burp-suite),
[Turbo Intruder at `7b9d1f9`](https://github.com/portswigger/turbo-intruder/tree/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47),
[HTTP3-Adapter at `f7ead65`](https://github.com/t0xodile-swig/HTTP3-Adapter/tree/f7ead6559af7565bd90acfa43b2abdd177a40a85),
and h3fuzz's pinned quic-go v0.63.0 source. Re-audit dependency behavior if any
of those pins change.

## Current evidence and gap

h3fuzz already implements the main research pattern:

- direct H3 negotiation and a bounded concurrent-stream fingerprint
- multiple independent QUIC connections
- 256 local stream-admission slots per connection by default
- adaptive connection tuning
- an adaptive worker supervisor with capacity proposal/acknowledgement
- exact-generation connection retirement and bounded reconnects
- automatic HEAD and final-byte Range qualification
- runtime GET recovery
- post-drain request and transport accounting

It is not transport-engine equivalent to Turbo Intruder. Earlier 3.98-to-5.05
second audit observations of approximately 46.3k to 58.7k RPS are historical
starting points, not the current result. The final local work includes a sealed
5+5 Darwin diagnostic above 100k. The production-integrated prepared-header
path then sustained a 114,578.588 RPS median across five ordinary adaptive
default runs on the same local machine, with exact hit and origin accounting.
The current build after PERF-202 then sustained a 127,008.228 RPS median in a
five-run telemetry-on regression cohort with exact latency, prepared-header,
origin, ledger, and drain accounting. Its descriptive difference from A42 is
not causal optimization evidence because the repeated input and cohort timing
differ and no alternating A/B was run.
Current-source native-Linux wordlist-storage ablation and profiling evidence is
clean around 60k on the shared host. A narrow direct-RoundTripper bypass
improved the arm median by only 2.0445% and failed its acceptance gate. Capacity
accounting also shows that the existing 4-core, 8-thread co-located
`mothership` cannot supply the client and origin CPU budget needed at 100k. The
native-Linux 5+5 cohort, a second same-region native-Linux host for isolated
origin resources, effective UDP receive and send buffers of at least 7,340,032
bytes on both hosts, and an authoritative Git checkout with a real current
`HEAD` are still missing.

### Current PERF state ledger

This is the consolidated state ledger as of 2026-10-11:

- `passed`: PERF-001, PERF-002, PERF-004, PERF-101, PERF-102, PERF-103,
  PERF-104, PERF-201, PERF-202, PERF-205, PERF-303
- `hypothesis-disproved/skipped`: PERF-203
- `infrastructure-blocked`: PERF-003, PERF-005, PERF-006, PERF-204,
  PERF-301, PERF-302, PERF-501, PERF-502, PERF-503, PERF-504
- non-terminal/conditional: PERF-401, PERF-402, PERF-403

This ledger does not claim that the whole plan or any phase containing a blocked
or non-terminal item is complete. In particular, the infrastructure-blocked
measurement, matrix, sweep, cohort, and release-evidence work still prevents
Phase 5 certification and a README claim.

### Retained local diagnostic results

The alternating cohort used five ordinary adaptive-default runs and five
fixed-8 runs, with a fresh instrumented origin per run:

| Mode | Runs | Median completed-response RPS | Every run above 100k and at least 30 s |
| --- | ---: | ---: | --- |
| Adaptive default | 5 | 113,206.928 | Yes |
| Fixed 8 | 5 | 113,980.998 | Yes |

The adaptive median was 99.320878% of fixed. The cohort recorded 37,373,120
exact successful logical outcomes and exactly reconciled marked H3 HEAD
application attempts with `/bench` origin counters. Every strict error, retry,
and fallback counter was zero, and every run fully drained. The retained
aggregate is
`harness/.work/final-local-cohort-20261011T042635Z/aggregate.json`; its outer
`SHA256SUMS` hashes to
`3ee08416787f72363c569116a8beb3ce280417d22865500c88fb46038bc582f2`.

The subsequent A42 cohort exercised the final production integration rather
than the isolated prototype. Five ordinary adaptive-default detected-H3 runs
all exceeded 100k RPS, with a median of 114,578.588 and a range of
107,810.617–115,762.320. They completed 18,686,560 successful logical
responses in total and recorded exactly 18,686,560 prepared-header attempts
and hits, zero prepared-header fallbacks, zero logical, transport, QUIC, or
retry errors, exact origin-handler reconciliation, and complete drain. Its
aggregate is
`harness/.work/a42-final-local-cohort-20261011T092449Z/aggregate.json`; the
77-entry `SHA256SUMS` verifies cleanly and hashes to
`81d4c37df3ef65f356c430f324b76794294cb38062c9d1625f5e3518dda14a63`.

The A45 current-source regression cohort then exercised the accepted PERF-202
build with run telemetry enabled, so every request retained exact final-attempt
`WroteRequest`-to-`GotFirstResponseByte` timing and the histogram source remained
`client_final_attempt_time_to_first_byte`. Five ordinary adaptive-default
detected-H3 runs were valid, lasted 40.289–40.509 seconds, and all exceeded
100k RPS. Their median was 127,008.228 RPS, with a range of
126,857.206–127,548.829. Total successes, exact latency samples,
prepared-header attempts and hits, and origin-handler requests were all
25,694,020; overflows, prepared fallbacks, errors, and retries were zero, and
every ledger and drain gate passed. The retained bundle is
`harness/.work/a45-final-local-cohort-20261011T104118Z`; its verified 77-entry
`SHA256SUMS` hashes to
`6757c5a580ec02d10a2639ca0ece3399675b0e466b88a73d192e9cd29ed26bd1`.
Its 10.8481% median difference from A42 is descriptive only: the repeated input
and cohort timing differ and there was no alternating A/B, so it is not a
causal PERF-202 throughput claim. PERF-202's causal acceptance evidence is the
telemetry-off allocation benchmark below.

An earlier preliminary run at
`harness/.work/a45-final-local-cohort-20261011T103912Z` was otherwise clean at
125,931.412 RPS but lasted only 29.677361 seconds. It was rejected solely for
missing the 30-second duration gate, and a fresh longer-input cohort was run;
it is not included in the accepted aggregate.

The separate `/files` correctness bundle produced the exact 12-item finding set
under both automatic HEAD and forced GET. Its automatic run made 14 HEAD
attempts plus five semantically justified directory-redirect GET fallbacks; its
body-aware run proved the one-byte body was exactly `x`. All 45 checks passed
with zero transport or QUIC errors and full drain. The bundle is at
`harness/.work/local-files-proof-20261011T043725Z-r3/validation-summary.json`;
its outer `SHA256SUMS` hashes to
`c20f6b4346c98afba590eb89338b4501062ad9d4e46cbcf0f78c9276c343502b`.

All four local bundles are explicitly diagnostic/non-certification because
they ran on Darwin. The performance cohorts lack the required native-Linux
client/server UDP-drop evidence, and the semantic `/files` fallbacks make that
small bundle correctness-only rather than a transport-ceiling result. The
README remains gated on the native-Linux certification bundle.

### A42 dependency and release provenance hardening

The A42 verifier now treats stock qpack v0.6.0 as part of the reproducible
dependency boundary. Metadata schema v2 records the qpack module, version,
module sum, and `go.mod` sum. Online and strict-offline verification use
read-only module selection to require that exact version and both sums, and
reject any replacement; wrong-version and replacement fixtures fail closed.
The verifier and metadata SHA-256 values are
`fb667f1afb21a1b6c0a188aced38a4c5d9bc10ab782e3ddee7ecea89947c0425`
and `437473d35ca51ece2887a50fd3b29bd75b6d496629db843528e256b1c0650b54`.
The unchanged A42 patch hashes to
`72fc88948abb88f32b866fc10d8ee6680832b86827ec0c60db6134945d60292f`.

Release archives now include `THIRD_PARTY_NOTICES.md`, the A42 patch, and the
upstream metadata. A GoReleaser v2.18.3 fixture snapshot produced 16 archives;
every archive contained each provenance input exactly once with matching
hashes. The `.goreleaser.yml` SHA-256 is
`41ba29df826cf1cf6c8ac70cc4bf78f70ed633372a0e0bba2c51c36088d55a81`.
This validates the archive contract, not every release target: the real
all-platform snapshot still encounters the pre-existing FreeBSD/arm
`modernc.org/libc` cross-compilation failure. The completion re-audit passed the
full nested quic-go test suite, its configured race/vet checks, the root
full/race/vet checks, and the complete harness validation.

### Retained native-Linux wordlist-storage diagnostic

An isolated current-source A/B compared legacy per-entry regular-file storage
with the contiguous byte arena at one fixed-4, 256-stream, 704-worker point.
The client and origin shared the same native-Linux host; the two binaries
differed only in whether regular files selected the new arena storage.
Predeclared clean pairs 2, 5, and 7 produced:

| Variant | Clean runs | Median completed-response RPS |
| --- | ---: | ---: |
| Legacy storage | 3 | 58,117.946 |
| Arena storage | 3 | 60,410.762 |

The median-of-arm RPS improvement was 3.9451%. Within-pair improvements were
6.4091%, 3.4877%, and 3.9895%, for a 3.9895% median; arena median client user
time fell 9.1902% and median maximum RSS fell 42.3891%. Every selected run
lasted at least 30 seconds, completed and exactly reconciled 2,335,820 logical
responses, recorded zero logical, transport, QUIC, retry, fallback, or UDP
errors, and fully drained. The response-latency histogram independently
recorded exactly 2,335,820 final-attempt TTFB samples per run with zero
overflow, providing live-load validation of that telemetry contract.

Invalid pairs were retained rather than promoted: arena run 1 recorded 44
`RcvbufErrors` and excessive preflight load; baseline runs 3, 4, and 6 recorded
17, 21, and 33 `RcvbufErrors`, respectively, and their treatments were not
launched. The 263-entry bundle is
`harness/.work/remote-arena-isolated-stable-20261011T060400Z/remote-bundle`;
the outer `SHA256SUMS` hashes to
`6c3901cbbe1b01e2f9a24029e4b7e96a5b9e76192697bb00620846fb6378fb74`.
This accepts the arena for the tested operating point and supplies native-Linux
diagnostic evidence only. It is not adaptive, does not reach 100k, uses a
shared host, and cannot satisfy the release-certification gate or authorize a
README change.

### Retained current-source native-Linux profiles

Two serial fixed-4, 256-stream, 704-worker runs profiled the current source on
the shared native-Linux host. The CPU-profile run reached 60,205.028 completed
responses per second over 38.7978 seconds; the allocation-profile run reached
60,331.897 over 38.7162 seconds. Each run completed and exactly reconciled
2,335,820 successful logical responses, passed all 18 checks, recorded zero
terminal, transport, QUIC, retry, fallback, or scoped UDP error deltas, and
fully drained.

The CPU profile sampled 143.26 CPU seconds in 39.03 wall-clock seconds.
`SimpleRunner.execute` accounted for 62.82% cumulative CPU,
`runtime.mallocgc` for 11.52%, and Linux syscalls for 7.63%. The allocation
profile recorded 23.40 GiB total allocation; visible leaders included
`Job.runTask` (1.45 GiB), `SimpleRunner.execute` (1.14 GiB), HTTP/3 header
parsing (1.12 GiB), `http.Header.Clone` (1.03 GiB), single-wordlist input-map
materialization (0.86 GiB), `http.NewRequestWithContext` (0.85 GiB), and
`Request.WithContext` (0.69 GiB). This ranks the request/client and task/input
paths for the next narrow experiments; it does not attribute the entire gap to
one function or certify throughput.

The retained bundle is
`harness/.work/current-hotpath-profile-20261011T064500Z`; the outer
`SHA256SUMS` hashes to
`1ae0fc7c1cc6840a2692cb5fe1cc78aafc37491a561bfb290da7e3ed2720b410`.
It is diagnostic because client and origin shared one host and profiling itself
perturbs the measured process.

### Retained direct-RoundTripper diagnostic

A strictly eligible owned-HTTP/3 path bypassed `http.Client.Do` and invoked the
selected RoundTripper directly while preserving timeouts, error shape, tracing,
body ownership, and the ordinary fallback path. Three alternating clean pairs
at the same fixed-4 point produced baseline and treatment medians of 60,425.995
and 61,661.386 completed responses per second. The 2.0445% arm-median gain and
2.1048% median within-pair gain missed the predeclared 3% RPS acceptance gate,
so this approach is rejected. The individual within-pair gains were 1.8830%,
2.1048%, and 2.4761%; treatment median user CPU fell 4.0048% and median maximum
RSS fell 15.4917%.

The treatment change was reverted after the failed gate. The accepted product
source corresponds to the baseline binary SHA-256
`b7d75777eb365d6d9f64d42fa292ff846693fc732e30937f7ea4f4772f6a8692`;
the treatment binary remains only inside the retained evidence bundle.

All six runs lasted at least 30 seconds, exactly reconciled 2,335,820 successful
logical responses, recorded zero terminal, transport, QUIC, retry, fallback,
or scoped UDP drop/buffer errors, and fully drained. The retained bundle is
`harness/.work/remote-direct-roundtrip-ab-20261011T070443Z/remote-bundle`; its
outer `SHA256SUMS` hash is
`5aef755fd4e0a05b42cf75bdc855518bdeae2177526fb55a2c614899f7436b62`.
This is a rejected
single-host fixed-mode optimization experiment, not certification. The
host-wide `Udp.NoPorts` counter rose by one in baseline run 3; that counter was
outside the predeclared scoped UDP error gates and is retained rather than
described as zero.

### Native-Linux host-capacity boundary

The clean shared-host diagnostics imply 61.7856 microseconds of client CPU and
47.9189 microseconds of origin CPU per completed response. At 100,000 responses
per second those rates require 6.1786 and 4.7919 logical CPUs, respectively, or
10.9705 combined on a 4-core, 8-thread host. Even the ideal arithmetic ceiling
is only 72,923 co-located responses per second before scheduler, kernel, or
measurement overhead. Code work alone therefore cannot turn this host layout
into legitimate 100k certification evidence.

The next certification environment needs a second same-region native-Linux
host for the origin so `mothership` can run the client alone, plus at least
7,340,032-byte effective UDP receive and send buffers on both hosts. The current
212,992-byte ceiling is below that requirement and needs operator access to
change. It also needs a real authoritative clone or worktree containing the
exact current source and a genuine Git `HEAD`; the present exported workspace
has no `.git`, so its `git_commit=null` evidence is certification-ineligible and
must not be forged with `git init` or fixture edits. A local Darwin run, Docker
Desktop, multiple client processes, or replay traffic cannot substitute for
that environment.

Port 8443 is not a certification requirement and is occupied by a long-running
service on `mothership`; that service must not be touched. A fresh campaign may
use another data port, provided both TCP and UDP are opened and the identical
target authority is used consistently through every prerequisite, sweep,
cohort, and manifest. The origin administration endpoint remains loopback-only.

The PortSwigger and h3fuzz figures are not directly comparable. PortSwigger's
article does not publish the raw artifact, exact denominator, error rate,
latency, full interval, hardware, or origin workload. This plan deliberately
uses a stricter, explicit completed-response definition.

## Definition of done

Describe the retained Darwin result only as a local diagnostic. Do not make an
unqualified 100k claim or update the README until one retained native-Linux
certification bundle satisfies every condition below.

### Correctness gates

- `harness/scripts/smoke.sh` passes.
- `harness/scripts/correctness.sh` passes all response-strategy cases.
- `go test ./...` passes.
- `go test -race ./pkg/engine ./pkg/runner ./pkg/ffuf` passes.
- The run proves direct HTTP/3. A downgrade is a failed certification run.
- The run proves the expected HEAD or Range strategy, or explicitly certifies a
  GET workload.
- Every logical input has one terminal outcome.
- Every completed response is accounted for after all workers and transport
  streams drain.
- No performance change weakens runtime validation or GET recovery.

### Performance gates

- Native Linux client and server, preferably separate hosts in the same region.
- Host networking, not Docker Desktop or a published-port VM path.
- Five independent adaptive runs using the same configuration, plus five
  validation runs at the selected fixed setting.
- At least 30 seconds of main-request span in each run. Preflight,
  warmup, response qualification, and shutdown are excluded.
- Median of the five adaptive runs is at least 100,000 successful logical
  responses per main-request-span second.
- Terminal logical error rate and surfaced attempt error rate are each
  below 0.1 percent in every run.
- Application-level retry rate is below 0.1 percent and is reported separately.
  Internally recovered quic-go errors and its one-shot retry for the two
  supported cases are separate counters when instrumentation can observe them.
  Never label an unobserved internal retry rate as zero.
- No attributable increase in UDP receive-buffer errors or packet-drop
  counters. On a dedicated host or namespace this means a literal zero delta;
  on an approved shared host it means zero unexplained delta after the retained
  same-length idle-noise baseline.
- Route- and protocol-specific handler observations reconcile with marked
  application attempts that could reach the handler to within 0.1 percent.
  HTTP/3 attempts rejected before handler dispatch and quic-go retries are a
  separate transport-layer ledger. Do not add an internal retry to the handler
  expectation merely because a retry occurred: the failed first attempt for
  `errConnUnusable` and `H3_REQUEST_REJECTED` does not reach the route handler.
  Strict certification requires enough client and server transport evidence to
  classify every mismatch. If internal attempt or pre-handler failure
  visibility is unavailable and counts disagree, the run is ineligible rather
  than normalized. Fingerprint, qualification, and warmup probes are counted
  separately.
- All workers drain, all stream-admission permits return, the runner closes,
  and no in-flight transport work remains.
- Automatic mode reaches at least 90 percent of the best clean fixed-connection
  result in the same environment.
- A separate `/files/FUZZ` result is reported. The constant 204 `/bench/FUZZ`
  transport ceiling must not be described as filesystem discovery throughput.

### Metric definition

Use this certification metric:

```text
logical_completed_response_rps = successful logical responses / main-request span seconds
application_retry_rate = engine retry attempts / scheduled logical inputs
surfaced_attempt_error_rate = surfaced attempt errors / application transport attempts
terminal_logical_error_rate = terminal logical errors / scheduled logical inputs
```

An HTTP status such as 204, 403, or 404 is a completed response. A DNS, TLS,
QUIC, timeout, reset, or other transport failure is not. Queued inputs,
scheduled inputs, volatile UI rate, and process-wide wall time are not
substitutes for the certification metric.

The report must keep these units distinct:

- logical inputs scheduled
- exactly-once terminal logical outcomes
- successful logical responses returned to the engine
- surfaced application transport-attempt errors
- terminal logical errors after application-level retry policy
- application-level retries
- optimized HEAD or Range wire attempts
- full GET recovery attempts
- internally handled quic-go errors and retry attempts, when observable
- marked route-handler invocations observed by the server
- pre-handler H3 attempts and rejections observed by transport instrumentation
  when available

No count may be inferred only by subtracting another count. In particular,
`completed = scheduled - errors` is not proof that work was neither lost nor
duplicated.

Use these reconciliation units and never mix them:

```text
expected_handler_requests =
    application_transport_attempts - observed_pre_handler_application_failures

handler_reconciliation_error =
    abs(server_marked_handler_requests - expected_handler_requests) /
    max(1, expected_handler_requests)
```

An application attempt that receives a response is known to have reached the
handler. A surfaced error is not automatically a pre-handler failure because a
body read can fail after dispatch. Transport-level H3 attempts, including a
first rejected or unusable attempt and its internal retry, require separate
client or server protocol instrumentation. If the implementation cannot place
a failed attempt in one of these ledgers, strict reconciliation is unavailable
and the run cannot certify 100k.

`observed_pre_handler_application_failures` counts whole application
`RoundTrip` calls for which instrumentation proves that no marked route-handler
invocation occurred. It never counts an internal first rejection separately.
For example, one application attempt with an internal
`H3_REQUEST_REJECTED` followed by an internally retried success contributes one
application attempt, zero pre-handler application failures, one handler
request, and one rejection plus one retry in the separate internal ledger.

## Guardrails and non-goals

- Only test systems that are owned or explicitly authorized.
- Never hide, discard, or reclassify transport errors to improve the number.
- Do not remove the HEAD and Range qualification checks.
- Do not silently fall back to H2 or H1 in a certification run.
- Preserve the ordinary ffuf path and all existing flags.
- Keep performance paths behind eligibility checks when they cannot preserve
  every ffuf feature.
- Do not copy AGPL Turbo Intruder implementation code. Implement independently
  against public protocol specifications and permitted dependencies.
- Do not implement Single Datagram Attack, intentionally held QPACK
  blocked-stream race gates, kettled requests, DATAGRAM-based race delivery, or
  0-RTT as part of this project. Normal bounded QPACK dynamic-table compression
  and acknowledgement handling may be evaluated separately as an ordinary
  encoding optimization. The race features are not required for ordinary 100k
  content discovery.
- Do not port HTTP3-Adapter's single-connection reuse or speculative fallback
  behavior as a performance strategy. It is a compatibility reference, not the
  engine behind PortSwigger's throughput result.
- Do not increase streams above 256 by default. PortSwigger's audited source
  records degradation at 512 in one environment. Sweep and measure instead.
- Do not optimize the web database or browser rendering as part of the
  transport-ceiling milestone. Certification uses CLI mode.

## Execution order

Implement the work in this order:

1. Phase 0: make the measurement credible and capture a native-Linux baseline.
2. Phase 1: remove known client bottlenecks and verify connection recovery.
3. Phase 2: profile and reduce hot-path CPU, allocation, and contention.
4. Phase 3: validate and harden the adaptive controller under realistic
   latency and loss.
5. Phase 4: evaluate deeper HTTP/3 encoding changes only if profiles still
   justify them.
6. Phase 5: run certification, retain evidence, and update claims.

The current critical path is to obtain the second native-Linux host and UDP
buffer settings above, then run the live matrix and certification with separated
client/origin resources. In parallel, only the two remaining profile-justified
request/input experiments are queued: a strictly selected-H3 bodyless-GET
request template that bundles one-pass preparation with frozen headers, and a
strictly gated single-wordlist task that lazily materializes its public input
map. Each must preserve every eligibility/fallback/observer semantic and clear
its own end-to-end gate. The rejected RoundTripper bypass is not a basis for
adding more complexity.

Do not start a custom HTTP/3 encoder before Phases 0 through 2 identify the
remaining bottleneck. Environment, server, or ordinary Go hot-path work may be
the actual limit.

## Instructions for the implementing agent

- Treat the definition of done and correctness guardrails as binding.
- Work phase by phase. Do not collapse all tasks into one unreviewable change.
- Begin with PERF-001 through PERF-006 and produce the unchanged-client native
  baseline before attributing the gap to any one code path.
- Follow the critical path: credible measurement, disabled-autocalibration fast
  exit, adaptive worker protocol, verified quic-go recovery, profile-guided hot
  path, adaptive controller, then optional encoding work.
- PERF-101 can proceed alongside instrumentation. PERF-102 and PERF-103 require
  one agreed pool/advisor state model and one integration owner; do not let
  parallel branches invent incompatible lifecycle state.
- Add failing tests or benchmarks that expose each problem before changing its
  implementation.
- Preserve old telemetry parsing when the schema changes.
- Keep every optimization independently measurable and independently
  revertible.
- After each task, run its focused tests, the race detector for affected
  concurrency code, and the complete suite before starting the next task.
- If the available machine is not native Linux, complete functional work and
  portable tests, but leave the certification gate explicitly pending. Do not
  substitute a macOS Docker Desktop result.
- Stop after any phase whose exit gate fails. Diagnose the failed invariant
  before continuing to deeper optimization.
- If a profile disproves a hypothesis, record the rejected approach and do not
  merge complexity merely because it resembles Turbo Intruder.
- Do not update the README to claim 100k until PERF-504 produces an eligible,
  retained bundle.

### Work-item states and decision authority

Every PERF task and phase ends in exactly one state:

- `passed`: every acceptance condition is met and evidence is retained
- `infrastructure-blocked`: implementation or portable tests are complete, but
  named hardware, kernel, network, server, or external dependency evidence is
  unavailable
- `hypothesis-disproved/skipped`: measurement disproved an optional
  optimization or its documented entry condition was not met
- `failed`: a correctness, lifecycle, accounting, or required performance gate
  failed

Only optional optimization tasks may finish as
`hypothesis-disproved/skipped`. A required task that is blocked does not permit
Phase 5 certification. Record the state, evidence path, owner role, and next
decision in the task report.

Use these roles even if one person fills several of them:

- Integration owner: owns shared runner, engine, telemetry, and merge order.
- Measurement owner: owns metric definitions, report schemas, calibration, and
  certification eligibility decisions.
- Harness owner: owns the origin, network shaping, server snapshots, cleanup,
  and environment reproducibility.
- Performance owner: owns profiles, benchmarks, optimization hypotheses, and
  before/after evidence.
- Dependency maintainer: reviews quic-go and qpack changes and prepares any
  upstream proposal.
- Release approver: independently checks the final raw and redacted bundles and
  authorizes a public README claim.

Publishing an upstream issue, adopting a private dependency fork, or changing
public performance claims requires explicit maintainer or user approval. An
implementing agent may prepare the draft and evidence, but that preparation is
not permission to publish or adopt it.

## Phase 0: measurement and reproducibility

### PERF-001: add main-request timing and exactly-once accounting

Problem:

`-run-telemetry` records counts and drain state but has no duration. The harness
currently divides completed responses by whole-process wall time, which includes
protocol preflight, pool warmup, response-strategy qualification, and shutdown.
The current engine telemetry also derives completed responses as scheduled
minus errors. That arithmetic cannot detect a lost or duplicated task and makes
`terminal_outcomes == scheduled` tautological unless success and terminal
outcomes are incremented independently.
Current pool counters also include response-qualification traffic, while warmup
and internal dependency retries have different visibility. They cannot be used
as a main-request transport-error denominator without request-role separation.

Primary files:

- `pkg/ffuf/run_telemetry.go`
- `pkg/engine/run_telemetry.go`
- `pkg/engine/job.go`
- `harness/scripts/benchmark-report.py`
- `harness/scripts/benchmark_report_test.py`

Implementation:

1. Record the monotonic time immediately before the first outer main
   `Runner.Execute` call. Do not include HTTP/3 fingerprinting, connection
   warmup, or response qualification.
2. Record the maximum terminal timestamp among all scheduled main inputs after
   each outer task's successful logical response or final error has completed
   engine response and error handling.
   Inputs complete out of order, so this is not the input that was scheduled
   last.
3. Serialize the span between those two events as
   `responses.main_request_span_ns`, not as absolute monotonic timestamps.
   Certification rejects pause, request delay, and rate limiting, so the span
   represents the continuous offered-load interval. Keep paused runs useful for
   diagnostics, but mark them ineligible rather than silently subtracting time.
4. Independently increment `scheduled`, `successful_logical_responses`,
   `terminal_outcomes`, and `terminal_errors`. Exactly one outer input attempt
   records a terminal outcome even when existing retry recursion performs more
   than one transport attempt. Do not derive successful responses by
   subtraction.
5. Assign every outer logical input a monotonically increasing `logical_input_id`
   when it is scheduled. Carry that ID through retry recursion. When structured
   certification telemetry is active, maintain a chunked compact state ledger
   with atomic transitions `unseen -> scheduled -> success|error`. A duplicate
   schedule, terminal transition from any state other than `scheduled`, missing
   terminal state, ID gap, or out-of-range ID is an invariant failure. Legitimate
   duplicate wordlist values still receive different IDs. Bound serialized
   anomaly samples to the first 100 IDs while retaining exact anomaly counts.
   The ledger may be disabled for ordinary runs without structured telemetry,
   but it is mandatory in every sweep and eligibility trial.
   Before the producer starts, obtain `expected_expanded_inputs` from the same
   input-provider plan that applies wordlist modes and extensions. Sweep and
   eligibility runs reject an unknown total and require the assigned ID count
   to equal it. This detects a producer that silently stops before assigning an
   input, which per-assigned-ID transitions alone cannot detect.
6. Add main-phase transport counters that exclude qualification and warmup:
   attempts, completed response headers or bodies as appropriate, surfaced
   errors, application retries, optimized attempts, and full GET recoveries.
   Give every application-issued transport attempt exactly one role such as
   `initial`, `optimization_fallback`, `engine_retry`, or
   `engine_retry_optimization_fallback`, and record method separately. Role
   counters must be mutually exclusive and sum to total application attempts.
7. Optionally serialize UTC diagnostic timestamps for ordinary runs. Harness
   runs must emit the versioned phase-file transitions defined below so the
   external live-socket sampler can identify the main phase. Never compute RPS
   from those wall-clock timestamps.
8. Define an empty scan as having zero scheduled inputs, zero terminal outcomes,
   no request span, and no RPS. For a one-input scan, use the actual positive
   monotonic span rather than inventing a denominator.
9. Make `benchmark-report.py` calculate completed-response RPS from successful
   logical responses and the main-request span. Retain process wall time as a
   separate diagnostic.
10. Version the telemetry schema and keep the report reader backward-compatible
   with old artifacts.

Tests:

- Unit test with an injected monotonic clock that artificial preflight and
  shutdown delays do not enter the main-request span.
- Unit test concurrent first-start and maximum-terminal updates under `-race`,
  including out-of-order completion.
- Unit test mixed completed and failed inputs account for every scheduled input
  without inferring one counter from another.
- Unit test one lost ID plus one duplicated terminal callback. Aggregate totals
  are deliberately made equal, but the state ledger must report both anomalies
  and make the run ineligible.
- Unit test an expanded input provider that reports N but stops after N-1
  assignments. All assigned IDs complete, but certification must still fail.
- Race-test chunk growth, scheduling, terminal transitions, retry recursion,
  cancellation, and bounded anomaly serialization.
- Unit test that application retry recursion records several main transport
  attempts but exactly one terminal logical outcome.
- Unit tests for empty and one-input scans without wall-clock sleeps.
- Report parser tests for old and new telemetry schemas.

Acceptance:

- Main-request span is positive for a non-empty real scan.
- Completed-response RPS uses successful logical responses and the main-request
  span.
- Ledger counts prove every assigned ID made exactly one scheduled transition
  and one terminal transition, with zero gaps, pending IDs, duplicates, invalid
  transitions, or out-of-range IDs, and assigned IDs equal the input provider's
  expected expanded count.
- Process wall time and main-request span are both visible and clearly named.
- The disabled instrumentation path adds no per-request allocation.

### PERF-002: make server correlation route- and protocol-specific

Problem:

The current harness chooses the largest positive Caddy metric-series delta.
That counter is not specific to `/bench/FUZZ` or HTTP/3, and `run_valid` does not
require server correlation.

Primary files:

- new `harness/cmd/instrumented-origin/main.go` and tests
- new `harness/Dockerfile.instrumented-origin`
- `harness/compose.yaml`
- `harness/compose.host-network.yaml`
- `harness/Caddyfile.ceiling`
- `pkg/ffuf/run_telemetry.go`
- `pkg/ffuf/optionsparser.go`
- `pkg/ffuf/flags.go`
- `pkg/runner/simple.go`
- `pkg/webserver/server.go` and option-sanitization tests
- `harness/scripts/metrics-snapshot.py`
- `harness/scripts/benchmark-report.py`
- `docs/performance/harness.md`

Implementation:

1. Add a dedicated Go HTTP/3 harness origin with the `/bench/`, `/files/`, and
   `/large/` benchmark routes. Use atomic in-process counters keyed by workload
   route, negotiated protocol, method, and benchmark phase, exposed as JSON on
   a separate loopback-only admin listener. Include a server-local snapshot
   command that writes the versioned JSON to a requested file, so a separate
   server host need not expose the admin listener to the client. Do not enable
   per-request disk logging. Keep Caddy as the correctness and interoperability
   origin.
2. Add a harness-only configuration option that injects a fixed phase-marker
   header in `SimpleRunner.Execute` after fingerprinting, pool warmup, and
   response qualification have completed. It must be unavailable to web
   sessions and off by default. Qualification and warmup requests must not
   inherit it; both an optimized attempt and its full GET recovery must inherit
   it.
3. Count a request as main workload only when both the expected route and exact
   phase marker are present. Record unmarked probes separately.
4. Snapshot counters before and after a run and correlate the selected route's
   HTTP/3 main handler delta with the handler reconciliation formula above.
   The expected application-issued total is the sum of the mutually exclusive
   attempt-role counters from PERF-001. Do not add quic-go's internal retry to
   this handler count. Instead, report a second H3-attempt ledger when supported
   protocol hooks expose pre-handler attempts and rejections. Treat every
   discrepancy, including an attempt that failed before handler dispatch, as
   evidence to classify, not as a value to normalize away.
5. Require the selected route's HTTP/3 delta for certification. Preserve an
   uninstrumented `Caddyfile.ceiling` run as a supporting ceiling comparison,
   but it cannot independently certify request accounting.
6. Add a same-origin, startup-time counter enable switch so the control and
   instrumented paths differ only by phase-header parsing and counter updates.
   Measure their overhead with at least five paired, alternating runs at fixed
   CPU affinity. Require the instrumented median to remain within 2 percent of
   the uninstrumented median and report a confidence interval or the complete
   paired deltas. A comparison against Caddy does not isolate this overhead.
7. Add a startup-time peer incoming-stream limit backed by quic-go's supported
   configuration. Include the configured and effective value in `/admin/v1`
   metadata and the server snapshot. PERF-301 uses this knob for 64-stream and
   256-stream matrix points.

Tests:

- H1, H2, and H3 requests increment distinct counters.
- `/healthz`, fingerprint, warmup, and strategy probes cannot inflate the main
  workload count even when they use the same `/bench/` route and HTTP/3.
- An optimized request followed by GET recovery produces two marked server
  requests and two client main transport attempts, but one terminal logical
  outcome.
- A synthetic pre-handler rejection does not increment the route-handler
  counter and is reported only in the transport ledger.
- Concurrent increments are exact.
- Counter-enabled median throughput is within 2 percent of the counter-disabled
  route under the paired-run protocol.
- The origin reports and enforces each configured peer stream limit.

Acceptance:

- Certification fails when route-specific H3 correlation is missing.
- Client and server main-workload counts can be compared without heuristic
  series selection.

### PERF-003: capture environment, UDP, and build evidence

Primary files:

- new `harness/scripts/environment-snapshot.sh`
- new `harness/scripts/network-snapshot.sh`
- new `harness/schemas/server-stable-v1.schema.json`
- new `harness/schemas/server-point-v1.schema.json`
- new `harness/schemas/client-runtime-socket-v1.schema.json`
- `harness/scripts/benchmark.sh`
- `docs/performance/harness.md`

Capture at minimum:

- h3fuzz commit plus a canonical source-tree hash that includes uncommitted
  tracked changes and untracked build inputs; a dirty boolean alone is not an
  identity
- Go version and module versions for quic-go and qpack
- binary SHA-256
- kernel, distribution, CPU model, logical CPU count, memory, and container
  runtime version
- harness image digest
- exact command and sanitized environment overrides
- target, resolved address, and measured RTT
- wordlist path, line count, SHA-256, and repeat mode
- CPU affinity for client and server
- kernel UDP defaults and maxima, including `rmem_default`, `rmem_max`,
  `wmem_default`, and `wmem_max`
- actual receive and send buffer sizes for the h3fuzz and origin UDP sockets,
  using `ss -u -m -p`, a socket hook, or an equivalent supported mechanism.
  The client socket exists only during the process, so `benchmark.sh` must run
  a PID-scoped sampler after pool warmup and during the main phase, not attempt
  to infer its size from before and after snapshots
- a retained `-debug-log` or dedicated log capture containing quic-go
  UDP-buffer warnings and whether the requested buffer increase was
  successfully applied; ordinary CLI mode discards standard log output and
  quic-go suppresses repeated buffer warnings process-wide, so absence without
  an explicit capture is not evidence
- per-interface or per-network-namespace UDP receive errors, input errors,
  receive-buffer errors, drops, and counter width before and after each run
- client and server CPU use, throttling, frequency policy, and process affinity

Collect the same identity, CPU, network, and socket evidence on both client and
server when they are separate hosts. `benchmark.sh` owns explicit per-trial
client and server snapshot paths. `certify.sh` uses the versioned server counter
and evidence provider executables defined below and creates those paths inside
each trial directory. Neither script embeds SSH credentials. Store stable host
identity and configuration separately from volatile measurements.

Do not make the harness mutate host sysctls. It may print operator-run commands
recommended by the installed quic-go version, then verify the applied maxima,
actual socket buffers, and absence of the related quic-go warning. Host-global
counters are acceptable only on dedicated hosts or isolated network
namespaces. Otherwise, capture a same-length idle noise baseline and make any
nonzero unexplained delta ineligible. Handle counter reset and wrap explicitly.
For a locally built image, record the image ID and configuration hash; record a
registry digest only when one exists.

For pinned quic-go v0.63.0 on Linux, the library's receive and send target is
`7 << 20`, or 7,340,032 bytes. Capture raw `SO_RCVBUF` and `SO_SNDBUF` values
and apply the same comparison as quic-go: the value returned by `getsockopt`
must be at least 7,340,032 bytes for each socket and direction. Also report the
operator-requested value and note Linux's kernel doubling convention so tools
that display requested versus effective memory are not compared as if they had
identical units. Derive and record the target from the pinned dependency when
that version changes.

Acceptance:

- Every certification bundle contains before and after snapshots.
- Every eligibility trial contains `client-runtime-sockets.jsonl` with at least
  one main-phase sample for every live h3fuzz UDP socket, tied to PID, local
  address, slot or shared transport identity when available, and sample time.
- Both host identities are present for a two-host run, and every UDP counter is
  attributable or accompanied by the approved idle-noise baseline.
- Actual socket buffers, not only host maxima, meet the version-derived target
  above and
  a retained log capture contains no unresolved quic-go buffer warning.
- Missing mandatory Linux evidence makes the bundle ineligible, not merely a
  warning.

### PERF-004: add a five-by-five certification orchestrator

Primary files:

- new `harness/scripts/calibrate-wordlist.sh`
- new `harness/scripts/baseline-report.py`
- new `harness/scripts/certify.sh`
- new `harness/scripts/certification-report.py`
- new tests beside the report script
- new `harness/schemas/baseline-manifest-v1.schema.json`
- `docs/performance/harness.md`

Implementation:

1. Make `calibrate-wordlist.sh` generate a candidate long wordlist from a base
   corpus and recorded deterministic seed. Run an unmeasured duration check,
   then compute the next size exactly as:

   ```text
   next_inputs = ceil(max(target_rps, fastest_clean_observed_rps) * 30 * 1.20)
   ```

   Increase it at most three times. Freeze that exact file, seed, line count,
   SHA-256, generator version, base-corpus hash, calibration reports, and formula
   inputs in a wordlist manifest before any measured trial. Calibration
   attempts are never part of the five runs. `certify.sh` accepts only this
   frozen file plus manifest. If any fixed-sweep or eligibility trial later has
   less than 30 seconds of main-request span, invalidate the entire generation,
   rerun calibration to a larger frozen file, and restart all measured trials.
   Never extend the file between runs in one cohort.
2. Run five independent adaptive trials with unique artifact directories and
   the frozen wordlist. Run five more trials at the selected fixed setting for
   a like-for-like adaptive comparison. A preliminary fixed sweep is separate
   from both five-run groups. Require PERF-501's content-hashed sweep manifest,
   verify every referenced report and selection calculation, and derive the
   selected fixed setting from it rather than accepting an arbitrary value.
3. Verify every measured main-request span is at least 30 seconds. A short
   measured trial invalidates the group; it does not silently regenerate the
   wordlist between runs.
4. Compute median, minimum, maximum, standard deviation, terminal logical error
   rate, application retry rate, surfaced attempt error rate, count
   reconciliation, drain result, and attributable UDP deltas using the exact
   denominators in this document.
5. Build layered canonical keys rather than one overloaded environment key:

   - `platform_key`: client and server host identities, CPU allocation and
     affinity, kernel, UDP and socket-buffer targets, network namespace and
     path identity, server image and config. It excludes h3fuzz build and all
     swept settings.
   - `client_build_key`: canonical source-tree hash including dirty content,
     binary and dependency hashes, and Go version.
   - `workload_key`: target authority and route, protocol, method, normalized
     headers, filters, strategy, datagram setting, timeouts, extensions, input
     mode, frozen wordlist hash, and all other request semantics. It excludes
     run ID, output paths, timestamps, and orchestration-only high-rate flags.
   - `treatment_key`: baseline, sweep, adaptive, selected-fixed, or diagnostic
     mode; fixed or adaptive connection values and bounds; streams; worker
     policy; and profiling flags.
   - `cohort_key`: hash of all four keys. Refuse different cohort keys inside
     one five-run group.

6. A fixed sweep shares platform, build, and workload keys but intentionally
   has one treatment key per point. B0 and B1 comparisons require the same
   platform and workload keys but retain different build keys. Adaptive and
   selected-fixed cohort comparison requires identical platform, build,
   workload, and stream values; the only allowed treatment differences are the
   declared connection and worker policies. Diagnostics match the selected
   point except for their named profile flag. `/files/FUZZ` has a distinct
   workload key but must match platform, build, authority, and server identity.
   The 90 percent gate applies to the two cohort medians, not one observation.
7. Keep three statuses distinct. A single `benchmark-report.json` records
   `run_gates_passed`, whether all per-run invariants passed, and
   `eligible_for_aggregation`, whether its intended mode may feed a sweep or
   cohort. Only the aggregate certification report contains
   `certification_eligible`. A clean sweep run can be eligible for selection
   without being a certification claim, and a clean adaptive or selected-fixed
   run can be eligible for its cohort without proving the five-run median.
8. Stage raw evidence under the ignored `harness/.work` directory. Create two
   distinct outputs: an owner-readable raw archive with exact commands,
   environment, logs, and all run files, and a separately generated redacted
   publishable archive. Exclude an archive's own manifest from its member-hash
   list, then hash each final archive. Require the `--artifact-store` argument
   to name durable local or mounted storage outside `harness/.work`, and record
   the CI release-artifact or external-storage URI.
   A staging directory alone is not retention.

Acceptance:

- Fixture tests cover pass, below-rate, short-duration, error-rate, UDP-drop,
  count-mismatch, mixed-config, identity-key mismatch, bounded calibration,
  fixed-versus-adaptive comparison, missing durable storage, and
  incomplete-drain failures.
- The script exits nonzero for every ineligible bundle.
- It never substitutes logical input rate for completed-response rate.

### PERF-005: add opt-in profiling without changing the default hot path

Primary files:

- `main.go`
- `pkg/ffuf/optionsparser.go`
- `pkg/ffuf/flags.go`
- `pkg/webserver/server.go`
- web option sanitization and security tests
- new profiling helper package or file

Add CLI-only options for CPU, heap/allocation, mutex, and block profiles. Keep
them unavailable in web sessions: omit them from the web flag registry and
reject submitted profile paths in server-side option sanitization. Enable
runtime profile rates only when requested, restore the previous mutex and block
profile rates on every exit path, use non-colliding output names, and create
artifacts with owner-only permissions. Profile lifecycle belongs in `main.go`
or a focused helper; flag registration alone is insufficient.

Add stable microbenchmarks for:

- one optimized HEAD request through `SimpleRunner.Execute`
- `SimpleRunner.Prepare` for one URL keyword
- single-wordlist input extraction
- matcher evaluation for status and size filters
- HTTP/3 pool slot reservation at 10, 20, 40, 64, 128, and 256 connections
- disabled autocalibration handling

Record `ns/op`, `B/op`, `allocs/op`, mutex samples, CPU profile, GC time, and
goroutine count for the native baseline.

Acceptance:

- Profiling flags have zero behavior change when absent.
- A web API caller cannot enable profiling or choose a server-side profile path.
- Benchmark output is retained with the certification manifest.
- Profiled runs are labeled diagnostic and never contribute to the five
  certification-rate trials. Use the identical configuration in separate runs
  with profiling disabled for eligibility.

### PERF-006: add bounded QUIC transport diagnostics

Problem:

Application error counters and host UDP drops cannot show path loss,
retransmission pressure, PTO activity, RTT changes, congestion-window collapse,
or internal connection replacement. Those signals are needed to tell a client
CPU ceiling from a QUIC or network ceiling.

Primary files:

- `pkg/runner/http3_fingerprint.go`
- `pkg/runner/http3_pool.go`
- `pkg/ffuf/run_telemetry.go`
- new focused QUIC telemetry implementation and tests

Implementation:

1. Split diagnostics into two layers. A minimal bounded generation-lifecycle
   recorder is mandatory whenever certification telemetry is active and records
   only connection open, handshake, close, slot, and generation identity.
   Optional aggregate packet diagnostics are enabled by `-quic-telemetry`.
   Implement both through supported `quic.Config.Tracer` and `httptrace`
   callbacks. Do not install `http3.Transport.Dial` merely for observation:
   quic-go's nil-Dial path owns and reuses a private UDP transport, and replacing
   it can change socket and performance semantics.
2. Record connections opened and closed, handshake failures, packets sent and
   lost, PTO count changes, latest and smoothed RTT, minimum RTT, congestion
   window, and bytes in flight when those fields are available through the
   pinned public API. Record unsupported fields as unavailable, not zero.
3. Keep callbacks nonblocking and bounded. Use atomics and periodic snapshots;
   do not serialize qlog events or append an unbounded event list on the hot
   path.
4. Put full qlog behind a separate diagnostic flag and never enable it in a
   certification-rate trial unless paired measurements prove negligible cost.
5. Measure each recorder layer independently with paired runs. The minimal
   generation recorder must remain enabled in every eligibility trial because
   a separate diagnostic run cannot prove that the measured run drained. If it
   costs more than 2 percent median throughput, optimize it before
   certification rather than disabling it. If optional packet aggregation
   costs more than 2 percent, keep that layer in separate diagnostic trials and
   use the mandatory lifecycle ledger plus attributable host, interface, and
   server counters for eligibility runs.
6. The scan pool currently enables QUIC and HTTP/3 datagram negotiation even
   though ordinary discovery does not send datagrams. Compare it enabled and
   disabled for interoperability, handshake bytes, CPU, and throughput. Disable
   it in scan transports unless a measured or compatibility requirement
   justifies it. Disabling means clearing both
   `quic.Config.EnableDatagrams` and `http3.Transport.EnableDatagrams`; clearing
   only one is invalid or still negotiates the QUIC extension. The fingerprint
   may still negotiate and report datagram support. Do not change this default
   until the B0 pre-optimization baseline below is complete.

Acceptance:

- Synthetic trace events produce exact aggregate counters under `-race`.
- With PERF-006 diagnostics disabled, no PERF-006 recorder is allocated and no
  PERF-006 `quic.Config.Tracer` is installed or invoked. Existing request-level
  `httptrace.ClientTrace` behavior is outside this assertion. Certification
  telemetry is not the disabled path: it installs only the mandatory minimal
  generation recorder unless `-quic-telemetry` is also set.
- Connection-generation counters support PERF-103 fault injection.
- Reported loss, PTO, and RTT changes agree with a controlled network test.
- The retained overhead comparison determines whether aggregate tracing is
  allowed in certification trials.

### Phase 0 exit gate

Capture B0, the pre-optimization baseline, so instrumentation work is not
confused with an optimization. PERF-001 through PERF-005 measurement plumbing
is present, PERF-006 recording is disabled, datagram settings retain their
original values, and no Phase 1 behavior is enabled. Run the current omitted
`-t` behavior plus explicit fixed `-t` values. B0 alone is the Phase 0 gate.

For B0, run three trials at every retained matrix point on native Linux. Test:

- routes: `/bench/FUZZ`, `/large/FUZZ`, `/files/FUZZ`
- connections: 4, 8, 10, 16, 20, 32, 40, and 64 initially, then 128 and
  256 when latency and observed usable peer streams imply that 64 cannot expose
  100k capacity
- streams: 64, 128, 256
- automatic mode with omitted `-t`, plus explicit fixed `-t` values selected
  from the Little's Law estimate and capped by current implementation limits

Derive the upper connection point from Little's Law rather than stopping at 64:

```text
required_connections = ceil((100000 * mean_response_latency_seconds) /
                            observed_usable_streams_per_connection)
```

Add measured headroom and never exceed the existing 256-connection safety
limit. Before the sweep, estimate a noise band from at least five fixed windows
or repeated control runs. Declare a plateau only when the next clean median
fails to exceed the prior best by `max(3 percent, noise_band)`. Declare a point
worse when an error, UDP-drop, loss, or drain gate fails, or when client CPU,
server CPU, or final-attempt TTFB p99 exceeds a predeclared environment limit without the
same minimum throughput improvement. Record the limits before viewing the
sweep. The default maximum may remain 64.

Put client and server on separate CPU sets or separate same-region hosts.
Record whether client CPU, server CPU, UDP loss, or network capacity saturates
first. Before attributing a sub-100k ceiling to the client, demonstrate at least
120k aggregate clean H3 responses per second with two isolated half-load client
processes against the same origin, or replace or tune the origin and network.
If aggregate rate remains near the one-client ceiling, client optimization is
blocked on origin or network headroom evidence.

Deliverable: `baseline-report.md`, `baseline-manifest.json` validated against
`baseline-manifest-v1.schema.json`, and the B0 machine-readable artifact bundle.
The manifest records the platform/workload comparison key, every trial hash,
at least five fixed-duration clean control-window rates, their median, MAD, and
the exact noise-band calculation used below. On a non-Linux development
machine, mark this exit gate
`infrastructure-blocked`, not failed. Portable implementation and unit work may
continue, but Phase 5 certification remains pending. A genuine correctness or
accounting failure still stops dependent work.

## Phase 1: known client bottlenecks

### PERF-101: remove disabled autocalibration from the response hot path

Problem:

Every successful response currently calls `ffuf.HostURLFromRequest`, then
acquires `calibMutex`, before `CalibrateIfNeeded` observes that autocalibration
is disabled.

Primary files:

- `pkg/engine/job.go:817-818`
- `pkg/engine/autocalibration.go:157-161`
- `pkg/engine/autocalibration_test.go`
- `pkg/engine/autocalibration_mutation_test.go`

Implementation:

1. Store an immutable `autocalibrationEnabled` field on `Job` during
   construction and check it before parsing the URL. Do not read mutable shared
   configuration on every response.
2. Check it again inside `CalibrateIfNeeded` for API safety, before taking the
   mutex.
3. Preserve per-host calibration serialization when enabled.
4. Do not change calibration ordering or filters.

Tests and acceptance:

- A test holds `calibMutex` while the disabled response path completes, proving
  that path does not wait for the lock. A narrow seam or counter proves it does
  zero host parses.
- `testing.AllocsPerRun` reports zero allocations for the disabled calibration
  guard itself.
- Enabled global and per-host calibration tests still pass under `-race`.
- Retain at least ten before and after benchmark samples and a `benchstat`
  comparison. Require at least a 20 percent reduction in targeted disabled-path
  `ns/op` with no regression in the end-to-end optimized request benchmark, or
  document that the hypothesis was disproved and do not retain extra machinery.

### PERF-102: make request-worker capacity follow adaptive pool capacity (complete)

Implementation status (2026-10-11): complete. Automatic mode now starts with
an 8-connection seed and grows worker capacity after acknowledged pool growth;
the ordinary doubling sequence is 8 to 16 to 32 while throughput justifies it.
One supervisor owns worker lifecycle, proposal and acknowledgement identity is
generation- and epoch-safe, `-http3-max-workers` supplies an adaptive ceiling,
and explicit `-t` retains fixed legacy semantics. CLI, config, web, history, and
import paths retain explicit-value provenance. The seed of 8 is a deliberate,
measured deviation from Turbo Intruder's default of 10.

Plan-draft problem (historical):

At plan-draft time, automatic mode attempted up to 10 initial connections and,
when `-t` was omitted, derived workers from successfully opened connections
times configured streams, capped by 65,536 and remaining inputs. Partial warmup
could make the starting value lower than 2,560. The tuner could open more
connections, but the job worker pool was created once. The web console
explicitly defaulted to 1,000 workers. The pool's worker ceiling was also
established before the remaining-input cap, so a short input could be reported
inconsistently. At 50 ms latency, 100k requires at least 5,000 in-flight
requests.

Primary files:

- `pkg/ffuf/interfaces.go`
- `pkg/runner/http3_pool.go`
- `pkg/runner/simple.go`
- `pkg/engine/job.go`
- `pkg/assembly/build.go`
- `pkg/webserver/manager.go`
- `pkg/webserver/profiles.go`
- `frontend/src/components/NewSessionWizard.tsx`
- `frontend/src/pages/SettingsPage.tsx`
- `frontend/src/utils/explicitFlags.ts`
- associated engine, runner, assembly, and web tests

Plan-draft design contract (implemented):

1. Introduce a narrow bidirectional capacity interface instead of importing a
   concrete runner into the engine. The pool publishes its desired request
   capacity; the engine acknowledges the actual live worker count.
2. Use a replaying, latest-value subscription with a one-element coalescing
   buffer. Assign every queue execution a monotonically increasing
   `execution_generation`. Every `CapacityProposal` carries that generation, a
   monotonically increasing process-local `epoch`, desired worker count, cause,
   and publication timestamp. Every `CapacityAck` carries the same generation
   and epoch, proposed count, actual live count, remaining-input cap, supervisor
   state, and acknowledgement timestamp. Publication and acknowledgement must
   never block the tuner. A job subscribes once per queue execution. A new
   subscription emits a ready event, receives the current desired value under a
   fresh epoch for its own generation, and unsubscribes before the next
   recursion or sniper job and during stop.
3. Make one worker supervisor own all worker creation and retirement. It grows
   to `active connections * streams`, bounded by the automatic worker ceiling,
   the 65,536 safety cap, and a synchronized remaining-input count published by
   the input producer. Do not read `Input.Position()` concurrently.
4. Treat omitted `-t` as adaptive sizing. Preserve explicit `-t` as a fixed
   legacy worker count and hard cap, including explicit `-t 40`, whose value is
   otherwise indistinguishable from the default. Add a separate
   `-http3-max-workers` bound for adaptive mode rather than overloading `-t` as
   both an initial count and a changing ceiling.
5. Preserve option provenance through CLI, config, persisted web defaults,
   history, session creation, and imported commands. CLI, web creation, and the
   current frontend importer already preserve explicit `-t`; retain their
   tests. Fix the remaining TOML, `.ffufrc`, and persisted-history ambiguity in
   which an explicit value of 40 is currently indistinguishable from omission.
   Give `-http3-max-workers` the same explicit-value provenance end to end.
6. Shrink workers only between tasks. Never cancel an in-flight request merely
   because the tuner reduced capacity.
7. Apply only the newest `(execution_generation, epoch)` tuple. A coalesced
   proposal supersedes every older unacknowledged tuple, and an acknowledgement
   from an old generation or older epoch is recorded as stale but cannot reset
   the tuning baseline. Fully join the old supervisor and its acknowledgement
   sender before activating the next generation. After a pool resize,
   asynchronously await the matching live-worker acknowledgement, update
   `http3Pool.workerCeiling` to the acknowledged value, then reset the tuning
   baseline and begin a fresh measurement window. The timeout is
   `max(2 seconds, 2 * tune_interval)`. On timeout, cancellation, unsubscribe,
   or missing listener, keep the last acknowledged ceiling, mark the proposal
   unapplied with a reason, and suspend further pool growth. An acknowledgement
   for a timed-out tuple is recorded as `late_after_timeout` and ignored. While
   a current subscriber remains ready, publish the same current desired value
   under a fresh epoch no more than once per tune interval; a matching fresh
   acknowledgement resumes growth. A new subscriber ready event triggers that
   republish immediately. Never block a request or the tuner goroutine while
   waiting.
8. Define shutdown ordering so no `WaitGroup.Add` can race `Wait`: stop advisor
   updates, tell the supervisor no more workers may be added, close the task
   producer, let workers drain or cancel, join the supervisor, then wait for
   workers. Make stop, pause, queue transitions, input exhaustion, and max-time
   cancellation safe throughout.
9. In the web form, distinguish an automatically selected worker count from an
   explicit user count. Do not silently retain the 1,000-worker override in the
   high-rate preset.
10. Expose execution generation, proposal epoch, acknowledgement generation and
   epoch, stale, late-after-timeout, republished, and timed-out proposal counts,
   proposed, acknowledged-live, desired, retiring, and peak worker counts in the
   Phase 1 telemetry schema. Define `peak_main_in_flight` as the
   maximum number of simultaneous application transport calls made for marked
   main work, including optimization fallback and application retry roles but
   excluding fingerprint, warmup, and qualification. It is not worker count or
   reserved limiter capacity.

Tests:

- A deterministic, explicitly non-default fake advisor grows 10 to 20 to 40
  connections and then shrinks; those fixture values do not describe the
  ordinary 8-connection seed.
- No tasks are lost or duplicated during resize.
- Stop and pause cannot strand a worker.
- Input exhaustion caps growth.
- Explicit `-t 40` remains distinguishable from omission, and explicit
  `-t 1000` remains fixed and capped across CLI, config, web, and CLI-import
  paths.
- Slow acknowledgement, old-generation and old-epoch acknowledgements,
  late-after-timeout acknowledgements, republish and recovery, coalesced
  updates, missing listeners, unsubscribe, recursion transitions, and
  concurrent stop produce the specified tuple outcome without blocked
  publishers, permanent growth suspension, false baseline resets, or leaked
  subscribers.
- `go test -race` covers resize, supervisor shutdown, stop, and queue transition
  interleavings.

Acceptance:

- Default growth from 8 to 16 connections can increase dispatcher capacity.
- Final telemetry reports zero live workers and zero limiter use after drain.
- Idle goroutine and memory cost is measured and documented.
- Deterministic PERF-102 supervisor tests reach 5,000 live workers without
  network traffic. The 50 ms, peer-limit-256 end-to-end capacity gate remains a
  PERF-301 environment test rather than unfinished PERF-102 implementation.

### PERF-103: instrument and verify connection-generation recovery (complete)

Implementation status (2026-10-11): complete. The stock fault fixture proved
that a late generation-A error could evict healthy replacement B and force C.
The production transport now owns each slot's current QUIC generation, retires
by exact identity, holds accounting through response-body lifetime, and uses a
shared reconnect gate with bounded concurrency and jittered backoff. The long
fault gate recovered to at least 90 percent of its pre-fault rate, surfaced zero
errors during the 30-second observation, created exactly one replacement, and
fully drained all generations. Context cancellation does not retire a healthy
generation, and the pinned quic-go retry asymmetry remains preserved.

Plan-draft problem (historical):

An h3fuzz pool slot owns an `http3.Transport`, not one immutable QUIC
connection. In steady state it normally caches one connection for the target
authority, but old in-flight generations can overlap a replacement. quic-go
v0.63.0 has precise, asymmetric recovery behavior:

- a dial error removes the cached entry and returns the error; the same request
  is not retried, but a later request can dial again
- the first non-context `ClientConn.RoundTrip` error removes the hostname entry
- that first error is retried once only for `errConnUnusable`, where no request
  stream was opened, or for `H3_REQUEST_REJECTED` when the body is absent or can
  be recreated with `GetBody`
- a second-attempt error is returned before another cache removal

Removal is keyed only by hostname, not expected generation. A late error from
old generation A can therefore remove replacement B. Removed generations are
also no longer in the transport map closed by `Transport.Close`, and keepalive
can prevent an otherwise healthy removed generation from idling out.
`http3Pool.RoundTrip` normally sees only the final surfaced result. It is
therefore incorrect either to assume recovery is complete or to add a second
retirement state machine before measuring these exact cases.

Turbo Intruder's explicit connection retirement remains an important behavior
to test, but it is not proof that h3fuzz needs the same mechanism above
quic-go's transport.

Primary files:

- `pkg/runner/http3_pool.go`
- `pkg/runner/http3_pool_test.go`
- `pkg/runner/owned_http3_transport.go`
- `pkg/runner/http3_reconnect_gate.go`
- `pkg/ffuf/run_telemetry.go`
- `harness/quicfault/`
- PERF-006 QUIC counter support

Plan-draft design contract (implemented):

1. Compose a per-slot `quic.Config.Tracer` with any configured tracer and add
   `httptrace` connect and connection callbacks to the request trace. Assign a
   slot ID and monotonically increasing connection generation, and record dial
   attempts, successes, failures, opens, and closes without replacing the
   transport's nil-Dial socket behavior.
2. Classify caller cancellation, deadline cancellation, QUIC stream or HTTP/3
   request rejection, connection-fatal errors, internal recovered generations,
   and final application-visible errors separately. Do not count quic-go's
   invisible one-shot retry as an application retry.
3. Use a connection-aware origin or UDP proxy that can terminate exactly one
   selected QUIC connection after a deterministic request count while many
   streams remain in flight. It must also trigger a dial failure, an unusable
   connection before stream creation, `H3_REQUEST_REJECTED`, and a second
   attempt failure. A Caddy route response is not sufficient fault injection.
4. Prove each dependency behavior listed in the problem statement. Exercise a
   bodyless request, a body with `GetBody`, and a non-replayable body. Track
   internal H3 attempts in the transport ledger; do not expect a rejected or
   never-opened first attempt to appear in the route-handler counter.
5. Report generation changes, internally handled failures and retry attempts
   when observable, final surfaced attempt errors, request-stream errors, live
   historical generations, and time below usable capacity. Define recovery as
   a successful handshake for a new generation plus at least one successful
   concurrent probe and a one-second rolling response rate at or above 90
   percent of the pre-fault five-second rate. Time below usable capacity starts
   at fault injection and ends only at that recovery point. Local limiter
   capacity alone is not evidence of recovery. Define
   `recovery_deadline = max(10 seconds, 3 * Http3ProbeTimeout seconds)` using the
   effective `-http3-probe-timeout` value stored in telemetry. If a dependency
   hook cannot expose an internal attempt, label it `unobserved`; never infer
   zero.
6. Merge no custom recovery code if one injected failure creates at most one
   replacement generation, recovery completes within
   `recovery_deadline`, surfaced attempt errors in
   the following 30 seconds remain below 0.1 percent, and all historical
   generations close at runner drain. A sustained defect is any violation of
   those numeric gates.
7. Only if the test demonstrates a remaining defect, add the smallest
   conditional recovery mechanism. Capacity loss or an orphaned live
   generation both qualify. If pool-slot retirement is selected, couple an
   atomic accepting/state check with the `inFlight` CAS; a reserver holding an
   old published snapshot must roll back its reservation if retirement wins.
   Remove retired tombstones from active and maximum counts so they can never
   be republished.
8. For graceful retirement, stop new admission, wait for `inFlight == 0` or a
   bounded drain timeout, then close. Close immediately only when the underlying
   connection is already terminal, and account every failed stream.
9. Bound conditional replacements to `min(4, ceil(desired/4))` concurrent dials.
   Use exponential backoff from 250 ms to 10 seconds with up to 20 percent
   jitter, driven by an injected clock and random source. Feed replacement
   failures into adaptive backoff.
10. Do not set `http3.Transport.Dial` only to observe connections. A nonnil Dial
    bypasses quic-go's private shared UDP transport lifecycle. If explicit
    generation handles are ultimately required to close orphaned generations,
    either fix the behavior upstream or deliberately reproduce per-slot packet
    connection and `quic.Transport` ownership, close it on every exit path, and
    re-run socket, CPU, allocation, and throughput comparisons.

Tests:

- Kill one connection with many concurrent streams while other slots remain
  healthy, then deliver late errors from the old generation after its
  replacement is usable.
- Confirm quic-go creates a new connection generation in that same transport
  slot and capacity recovers, or retain the evidence that triggers conditional
  pool retirement.
- Confirm a caller cancellation, deadline, stream reset, or rejected request
  does not retire an otherwise healthy slot.
- Confirm dial-error, `errConnUnusable`, `H3_REQUEST_REJECTED`, second-attempt
  error, bodyless, replayable-body, and non-replayable-body behavior matches the
  exact quic-go contract and telemetry never double-counts a logical outcome.
- Assert bounded dial and generation churn after the final old-generation
  error, and assert zero live tracked generations after drain and close.
- If conditional retirement is implemented, race a reserver holding an old
  snapshot against retirement and prove it cannot admit new work to that slot.
- Confirm total origin failure backs off without a reconnect storm using the
  fake clock and deterministic jitter source.
- Run all tests with the race detector.

Acceptance:

- The tested quic-go recovery path meets the numeric usable-capacity, error,
  churn, and generation-drain gates, with internal and surfaced outcomes
  clearly separated.
- Extra recovery code exists only when retained fault-injection evidence shows
  a gate failure, including an orphan-generation leak.
- No request accounting or drain invariant changes.

### PERF-104: add a dedicated high-rate CLI preset without weakening defaults (implementation complete)

Implementation status (2026-10-11): complete. The four high-rate controls are
CLI-only, require explicit acknowledgement, reject incompatible or
underprovisioned scans before load, size workers from explicit or measured
latency, and record the complete policy derivation in telemetry. Ordinary CLI,
explicit-worker, stored web, imported CLI, and raw web-API behavior remains
separate. Focused policy, web-isolation, input-bound, Go, race, and benchmark
contract tests pass. This is implementation evidence, not a native-Linux B1 or
Phase 5 result.

Primary files:

- `main.go`
- `pkg/ffuf/optionsparser.go`
- `pkg/ffuf/flags.go`
- `pkg/runner/response_optimization.go`
- `pkg/assembly/build.go`
- `pkg/webserver/server.go` and option-sanitization tests

Implement one exact interface:

```text
-http3-max-workers <positive cap, default 65536>

-http3-high-rate
-http3-high-rate-acknowledge
-http3-target-rps 100000
-http3-latency-ms <optional positive value>
```

`-http3-max-workers` is a general HTTP/3 adaptive-worker option and must be
available through CLI, config, web defaults, session persistence, and CLI
import with explicit-value provenance. The four high-rate policy flags are
CLI-only, omitted from the web registry, and rejected if submitted to the web
API.

`-http3-high-rate` is an opt-in ceiling-test policy, not an ordinary default.
It must fail before sending probes unless
`-http3-high-rate-acknowledge` is also present.

Policy:

1. Force HTTP/3, unlimited rate, no delay, automatic HEAD and Range
   qualification, and noninteractive mode. Default to 256 local streams, but
   respect an explicitly supplied `-http3-streams`; this is how PERF-501 sweeps
   64, 128, and 256. Require an explicit `-run-telemetry <path>` and fail before
   probing when it is absent. A protocol downgrade is fatal.
2. Use the explicit `-http3-latency-ms` when present. Otherwise use the largest
   successful mean from the full-GET and selected-strategy qualification
   samples and label the source `measured_preflight`. Compute:

   ```text
   desired_workers = ceil(target_rps * latency_seconds * 1.25)
   connection_capacity = fixed_connections when fixed mode is selected,
                         otherwise max_connections
   effective_workers = min(desired_workers,
                           connection_capacity * streams,
                           http3_max_workers,
                           65536)
   ```

   Fail with a sizing explanation when the configured bounds cannot supply
   `desired_workers`; never silently claim the requested target is provisioned.
   Preliminary PERF-501 sweep points intentionally test underprovisioned fixed
   settings and therefore run with ordinary explicit HTTP/3 flags plus telemetry,
   not this preset. The selected fixed and adaptive eligibility cohorts use the
   preset and must pass its capacity check.
3. Omitted `-t` enables runner-derived workers. Adaptive connection mode may
   resize them; fixed connection mode derives the fixed pool capacity and caps
   it by remaining inputs. Reject explicit `-t` with this preset so legacy
   fixed worker semantics cannot be confused with `-http3-max-workers`.
4. Require one same-origin URL fuzz keyword, bodyless GET, identity or absent
   `Accept-Encoding`, and a known baseline status or logical-size filter for a
   ceiling route. `-s` alone is not accepted because it does not suppress
   matched result processing.
5. Reject `-ac`, `-ach`, raw mode, host or header fuzzing, request data,
   non-GET methods, user `Range`, body-dependent matchers or filters, active
   scrapers, redirects, proxy and replay, recursion, any preflight or
   postflight, `-o`, `-od`, audit logging, `-sa`, `-se`, `-sf`, nonzero
   `-maxtime` or `-maxtime-job`, nonzero rate or delay, and any option that
   changes the eligible request shape or can stop the producer early.
   Postflight is specifically rejected because it adds at least one request per
   input.
6. Allow fixed non-fuzzed headers and automatically added User-Agent behavior,
   but display their encoded cost in PERF-401 diagnostics.
7. Calculate expanded input count after extensions. Warn below
   `target_rps * 36` inputs. "Certification mode" means execution by
   `harness/scripts/certify.sh`, not another binary flag. That script rejects a
   smaller frozen input before invoking h3fuzz so a 30-second run retains 20
   percent duration headroom.

Tests and acceptance:

- Table-test every rejected feature and compatible status or size matcher and
  filter combination.
- Test explicit and measured latency, rounding, headroom, maximum bounds,
  insufficient capacity, expanded input count, and missing acknowledgement.
- Test that the four high-rate policy flags cannot be enabled through stored
  web options, session creation, raw API JSON, or imported CLI settings in the
  web UI. Separately prove that `-http3-max-workers` round-trips through those
  paths.
- Print the complete effective policy and sizing calculation before load begins
  and store it in telemetry.
- Do not turn every ordinary CLI or web scan into a 100k attempt.

### Phase 1 exit gate

- All correctness and race tests pass.
- Capture B1 by repeating B0's native benchmark and profile matrix after
  PERF-101 through PERF-104, including the adaptive worker ceiling. Retain B0
  and B1 separately under the same platform/workload comparison key. Their
  client build keys must differ and remain recorded; never require unchanged
  source or binary hashes across an optimization comparison.
- Every improvement is reported separately before combining results.
- If B1 provisionally reaches 100k, still complete Phase 3 and Phase 5. Skip
  Phase 4 only when profiles do not justify its deeper work.

## Phase 2: profile-guided hot-path reduction

Only optimize costs visible in profiles. Retain before and after benchmark
outputs for every task.

### PERF-201: reduce input and request construction allocations

Incremental implementation status (A31, 2026-10-11): complete and accepted for
regular-file wordlist storage. Regular files now retain one contiguous byte
arena plus pointer-free end offsets, promoting offsets from `uint32` to
`uint64` rather than truncating wordlists beyond 4 GiB. Stdin retains its stream
semantics. Characterization covers line endings, blanks, comments, final lines,
maximum logical lines, extension expansion, reset/cursor behavior, encoders,
and multi-provider modes. The retained three-pair native-Linux fixed-4 ablation
above cleared the predeclared 3% end-to-end gate: median-of-arm RPS improved
3.9451%, median within-pair RPS improved 3.9895%, median user time fell 9.1902%,
and median maximum RSS fell 42.3891%. This closes the storage subtask, not the
remaining request-construction candidates or the Phase 2 exit gate.

Current candidates include:

- one `map[string][]byte` per single-wordlist input
- repeated keyword substitution and header-map copies
- `http.NewRequestWithContext` URL parsing per request
- per-request `bytes.Reader`, request context, trace, and fallback state
- repeated default User-Agent handling

Primary files:

- `pkg/input/input.go` and the concrete wordlist provider
- `pkg/ffuf/interfaces.go`
- `pkg/ffuf/request.go`
- `pkg/engine/job.go`
- `pkg/runner/simple.go`
- output, replay, and autocalibration paths that consume `Request.Input`

Implementation direction:

1. Add an optional compact-input interface for one pitchfork wordlist, one URL
   keyword, and no encoder. Preserve `InputProvider.Value()` and
   `RunnerProvider.Prepare(map[string][]byte)` through a compatibility adapter
   for existing providers and all ineligible modes.
2. Carry the compact key and byte slice through the engine and runner without a
   map. Lazily materialize `Request.Input` only when a matched result, output,
   replay, retry, or autocalibration consumer needs the public map.
3. Precompute immutable request-template data and canonical headers.
4. Pool only objects with unambiguous ownership and a proven lifecycle. Never
   return a request, URL, header map, or buffer to a pool before its response
   body is closed.
5. Investigate a pre-parsed URL template for URL-only fuzzing. Preserve ffuf's
   escaping, query, slash, raw URL, and custom keyword behavior in tests.
6. Keep the ordinary path for headers, bodies, host fuzzing, multiple inputs,
   encoders, raw mode, and preflight variables.

Fresh profile update (A36-A41, 2026-10-11): the current-source allocation
profile attributes 1.03 GiB to `http.Header.Clone`, 0.85 GiB to
`http.NewRequestWithContext`, 0.69 GiB to `Request.WithContext`, and 0.86 GiB
(3.68% of 23.40 GiB) to `MainInputProvider.clusterbombValue`. A narrow direct
RoundTripper bypass reduced median user CPU and RSS but improved arm-median RPS
only 2.0445%, below its 3% gate, and is rejected. The fail-closed bodyless-GET
request-template prototype was also rejected before live A/B: its ten-run
`Prepare+Execute` medians improved from 1,813.5 to 1,148.5 ns, but bytes fell
only 19.49% and allocations only 27.59%, missing the predeclared 50% micro gate.
The complete change was reverted. A40's isolated compact-input prototype was
also rejected without a product change: its theoretical observer-free ceiling
saved only 95.35 ns/request, 0.154% of measured client CPU, while any public
runner or observer materialization restored the allocation. Evidence is sealed
under `harness/.work/a40-compact-input-prototype` with manifest SHA-256
`f0c8d8bdc81172c083feda2f76d57ff4a45ec89b662f2b9d99027599ec11f37c`.
The isolated prepared-QPACK experiment that encodes only the changing `:path`
has now passed its whole-program native-Linux paired diagnostic gate. After
fixing a prototype that incorrectly held its cache mutex across stream I/O, the
lock-safe pooled version reduced quic-go's complete header writer by 79.60% on
Darwin and 81.38% on Linux in microbenchmarks, eliminating its 500 B and eight
allocations per operation. In three clean alternating pairs, baseline and
treatment arm medians were 60,383.715 and 62,815.724 RPS (+4.0276%), while
median total client CPU per successful response fell from 61.6486 to 56.7381
microseconds (-7.9653%). Every pair independently cleared both the 3% RPS and
5% CPU gates; all six runs had exact request/origin reconciliation, zero UDP
drops, clean drains, and verified manifests. The retained same-host diagnostic
is `harness/.work/prepared-qpack-only-ab-20261011T082058Z`; its 219-entry
closed-world `SHA256SUMS` hashes to
`033a72f9b92a63f0200ff5cc667ab69df0d0a77b651a5ad4807bb89bae462909`.

A42 has now integrated the accepted seam in a full-source, pinned quic-go
v0.63.0 fork. The dependency opt-in remains default-off; the runner attaches
it only after selected-H3, non-replay, reliable-HEAD qualification, independent
of the explicit high-rate policy. It uses stock qpack v0.6.0 field composition,
bounded request state, generic contexts for both semantic fallback branches,
and schema-v2 attempt, hit, fallback, and reason counters. Unsafe method, body,
compression, URL, header, Range, qlog, and trace shapes fail closed to the
ordinary writer. The root full/vet suite, focused root race tests, nested
HTTP/3 race/vet tests, 112 harness tests, offline fork reconstruction,
qlog/httptrace tests, exact byte parity, and semantic fuzzing pass. The final
local production cohort then recorded
18,686,560 hits from 18,686,560 attempts with zero fallbacks at a
114,578.588 RPS median. This is accepted product integration and a local
default-path result, but not native-Linux release certification.

Acceptance:

- Before implementation, the Phase 0 profile must attribute at least 5 percent
  of client CPU or 10 percent of allocation volume to these candidates. If it
  does not, close the task with the retained evidence and no code change.
- If implemented, the named single-keyword optimized-request benchmark has at
  least 50 percent fewer allocations and bytes per operation than its frozen
  baseline, and paired end-to-end runs show either at least 3 percent median RPS
  gain or at least 5 percent client CPU headroom at equal RPS.
- Identical generated requests across the existing characterization corpus.
- Matched results, replay, retry, and autocalibration retain exactly the same
  input values, and no pooled-object aliasing appears under `-race` or stress.

### PERF-202: remove unnecessary per-request timing and tracing cost

Task state (2026-10-11): `passed`. The accepted fast path is deliberately
narrow. Telemetry-on requests, generic requests, requests rejected from
automatic response optimization, and both transport-error and
response-validation fallback GET paths keep
exact final-attempt `WroteRequest`-to-`GotFirstResponseByte` tracing. Only a
telemetry-off request that remains eligible for automatic HEAD or Range omits
`httptrace`; its duration is the conservative interval from immediately before
`Client.Do` until response headers are available. Response-time matchers and
filters cannot silently select this interval because they reject automatic
HEAD/Range eligibility.

Primary files:

- `pkg/runner/simple.go`
- response-time matcher and filter tests
- optimized execution benchmarks

Implementation:

1. A mutex-backed exact timing state tracks the pending write and latest
   completed response-header callback across redirects, then atomically freezes
   after `Client.Do`. Duplicate or post-freeze callbacks cannot corrupt the
   result, and every fallback gets a fresh state.
2. Two independent final reviews returned GO after focused redirect, fallback,
   late-callback, and race coverage.
3. In serialized current-source benchmarks, the exact timing primitive was
   288 B/op and 5 allocs/op while the trace-free timing primitive was 0 B/op and
   0 allocs/op. The full optimized HEAD path measured 2,132–2,134 B/op and 20
   allocs/op. Its sealed historical HEAD baseline recorded 26 allocs/op, so the
   binding comparisons are exact timing 5 to 0 and optimized HEAD 26 to 20;
   each independently passes the at-least-one-allocation gate. Current Range
   measured 2,309–2,310 B/op and 27 allocs/op. Its contemporaneous 33-allocation
   pre-change observation was not retained as raw output and is non-binding
   historical context only.
4. The accepted runtime SHA-256 is
   `0e9255439c7320b2219f8115429bab6204e7b00291a6771ea9555d8886950b04`.
   The optimized-execute, marker, and response-optimization test hashes are
   `495b1defbe207dda749c7f2d8740dc5af676930676946d758e38e619df2d532f`,
   `cda49ea6ecd9bf4d70b37c2d885ecf8030d0c8ede35267437d5d0cf6ffe88592`,
   and `62737c242ace23f5b63995c68eb18f49719a5909d2d8e92ae2c475e5cf9e97fe`.

The raw evidence is sealed at
`harness/.work/perf202-trace-timing-audit-20261011T105656Z`. Its nine-entry
manifest verifies cleanly and hashes to
`3257f1ae5deb7686bab98e601969ef8127de5c82506de1be4e065a68d4946eb2`;
focused and repeated race tests pass. The copied historical HEAD raw output
hashes to
`b03e75bbe6dbef23438c32cb57a71d74f826a2b06171a03aa5e14bc4f5e389c9`
and was sourced byte-for-byte from the verified PERF-201 context bundle, whose
manifest hashes to
`16df1fd81762ebb67758ca8fe0e8ebefddf0da5245399315bb91ea24c2b34ebd`.
Only its allocation count is compared because that historical run used a
different CPU setting and source checkpoint.

The A45 telemetry-on local cohort is regression evidence that this current
build keeps the homogeneous exact histogram source and remains above 100k; it
does not execute the trace-free branch and is not causal performance evidence
for PERF-202. Its client, frozen origin, and 5,138,804-line repeated-wordlist
hashes are respectively
`d30540f97ba46089f98e83d07a9be28c99cdc868099bd8b1b99b4f7d9b68aab7`,
`a903297d97153d6bbe874f91e430868f656286b4789a4e15f8dab4e766561f81`,
and `8bae81e001b8b919587f87e557c3c814cbc4d8c09df25b8de993528f745ab0fd`.

Acceptance:

- Duration semantics have explicit tests and documentation.
- No time matcher or filter silently changes meaning.
- Merge only if the trace path is visible in the Phase 0 profile and the chosen
  approach removes at least one allocation per eligible request or provides at
  least a 3 percent paired median RPS gain without higher CPU at equal load.

### PERF-203: add a status and logical-size matcher fast path

Task state (2026-10-11): `hypothesis-disproved/skipped`; no production matcher
fast path was merged. The retained clean native-Linux profile attributes 0.93
of 143.26 sampled CPU-seconds to `MatcherManager.Matches`, or 0.649169%
cumulative CPU (0.132626% flat). `StatusFilter.Filter` accounts for 0.090744%
flat CPU. Even conservatively holding matcher cost constant after A41/A42's
7.9653% client-CPU-per-success reduction projects only 0.705353%, still 7.09
times below the 5% implementation gate.

Test-only characterization differentially covers every three-digit status,
logical-size boundaries, global and per-domain filters, `and`/`or`, empty
configuration, and unsupported-provider fallback. Focused, full-package, and
race tests pass. The local timings were collected while other performance work
was active and are advisory; their stable contract was 0 B/op and 0 allocs/op.
The retained profile, not those timings, is the binding decision. Evidence is
sealed at `harness/.work/perf203-matcher-audit-20261011T095739Z`; its verified
`SHA256SUMS` hashes to
`4e487a8dc6f411b529a93226e59f8993f2d105ecc8e35880265c8937363735ec`.

Automatic HEAD and Range are already incompatible with word, line, regex, and
time matchers and filters. Exploit that bounded eligibility:

Primary files:

- `pkg/filter/filter.go`
- `pkg/engine/job.go`
- matcher-manager and live-filter tests

1. Compile an immutable status and size decision plan before workers start.
2. Avoid generic provider map iteration and unnecessary result fields for a
   response that is immediately filtered.
3. Include global and per-domain filters in the plan. Invalidate and atomically
   replace the complete snapshot after every live filter mutation so no request
   observes a partially updated configuration.
4. Preserve matcher and filter `and` and `or` semantics and fall back to
   `MatcherManager.Matches` for any unsupported provider.

Acceptance:

- Exhaustive differential tests compare the compiled plan with
  `MatcherManager.Matches` for status, size, global, per-domain, `and`, `or`,
  empty, and unsupported-provider cases.
- Live filters remain race-free.
- Matcher evaluation is below 5 percent of client CPU at the target load, or no
  fast path is merged.

### PERF-204: reduce HTTP/3 slot-admission contention

Current reservation can scan every active slot with atomic compare-and-swap
operations when sampled slots are full.

Primary files:

- `pkg/runner/http3_pool.go`
- `pkg/runner/http3_pool_test.go`

PERF-103 has finalized the slot and connection-generation model, so this
prerequisite is satisfied.

Implementation direction:

- Benchmark the existing rotating full scan at realistic occupancy.
- Consider sharded ready queues, a lock-free available-slot ring, or per-worker
  affinity with bounded work stealing.
- Preserve exact per-connection stream limits during concurrent resize and
  connection-generation changes and any conditional retirement.
- Avoid a global lock on each request.

Acceptance:

- A parallel reservation benchmark with fixed `GOMAXPROCS`, pool sizes,
  occupancy distributions, and waiter counts records `ns/op`, `B/op`, and
  `allocs/op` against the frozen baseline.
- Reservation consumes less than 5 percent of client CPU in the end-to-end
  certification-host profile. Treat this as a profile goal, not a portable
  unit-test threshold.
- Complexity is justified by profile evidence.
- No over-admission appears in a high-contention race test.

### PERF-205: audit remaining response-pipeline work

Task state (2026-10-11): `passed`. Candidate ranking is complete; no additional
product hot-path change was accepted by this task. The retained clean
native-Linux fixed-4 profile contains 143.26 CPU-seconds and 25,126,213,944 sampled
allocation bytes across 2,335,820 successful H3 HEAD responses. It used
`-noninteractive -s -fc 204`, recorded no error or retry, produced an empty
stdout, and fully drained. Consequently it measures the certification CLI
no-match path directly, while matched-result-only branches are explicitly
reported as unexercised rather than free.

Primary files:

- `pkg/engine/job.go`
- `pkg/runner/simple.go`
- `pkg/output/`
- `pkg/filter/`
- focused benchmark files beside each changed package

Profile these operations before changing them:

- output diagnostic interface assertions and atomic counters
- response and request object creation
- result hash generation for matched responses
- progress refresh on every match
- scraper checks when no scraper is active
- pause checkpoints in externally managed web mode
- retry recursion and error formatting

The ranking below sums cumulative pprof values only at non-overlapping call
boundaries. Request/response construction comprises `SimpleRunner.Prepare`,
`http.NewRequestWithContext`, `Request.WithContext`, and `ffuf.NewResponse`;
their 6.09 CPU-seconds and 2,329,945,880 allocation bytes are therefore not
double-counted. `0 sampled` means that pprof attributed no sample in this
workload, not that the operation has zero cost in every mode.

| Rank | Candidate | CPU percentage | Allocation percentage | Contention evidence | Proposed change | Semantic risk | Binding decision |
| ---: | --- | ---: | ---: | --- | --- | --- | --- |
| 1 | Response and request object creation | 4.251% | 9.273% | No candidate-local lock was visible; no mutex/block profile was retained. | Continue only the separately gated PERF-201 request/template work and PERF-202 trace work; keep `NewResponse` allocation-free. | High: URL, header, context, timing, retry, and ownership semantics. | Qualifies on allocation, but is routed to PERF-201/PERF-202; no overlapping PERF-205 implementation. |
| 2 | Output diagnostic interface assertions and atomic counters | 0.056% directly attributed; at most 0.147% even after conservatively charging every `sync/atomic.(*Int64).Add` sample | 0 sampled | CLI diagnostic assertion is false and the counter is atomic, not mutex-backed; no candidate-local blocking sample. | None. | Medium: removing the counters weakens exact completion and embedded diagnostics. | Reject below the 5% gate. |
| 3 | Scraper checks with no active rule | 0.056% | 0 sampled | Empty `Scraper.Execute` had no attributed lock or allocation. | None. | Medium: initialization and live scraper behavior must remain compatible. | Reject below the 5% gate. |
| 4 | Pause checkpoint | 0.007% | 0 sampled | Certification CLI returned before the `RWMutex`; externally managed web contention was not profiled. | None for CLI; preserve the web pause barrier. | High: changing it can break externally managed pause semantics. | Reject below the gate and outside certification mode. |
| 5 | Result hash generation for matched responses | 0 sampled | 0 sampled | Not entered because every 204 was filtered. | Profile a matched-result workload only if discovery-output throughput becomes the target. | High: `FFUFHASH` is externally visible result identity. | Reject for the transport-ceiling path. |
| 6 | Progress refresh on every match | 0 sampled | 0 sampled | Not entered because every 204 was filtered; periodic progress remained separate. | None; preserve interactive and externally managed progress. | Medium: changes affect result visibility and progress consumers. | Reject for the transport-ceiling path. |
| 7 | Retry recursion and error formatting | 0 sampled | 0 sampled | Not entered: application retries, surfaced errors, and terminal errors were all zero. | None; preserve recovery and diagnostics. | High: this defines failure recovery and user-visible errors. | Reject for a clean ceiling workload. |

The source-level response-construction characterization now has a focused
benchmark beside its existing zero-allocation test:

```sh
go test ./pkg/ffuf -run '^TestNewResponse' -count=1
go test ./pkg/ffuf -run '^$' -bench '^BenchmarkNewResponseMetadata$' \
  -benchmem -count=10
```

The benchmark contract is zero allocations; nanoseconds must be compared only
in a serialized idle-host run. The complete machine-readable attribution,
profile hashes, limitations, and reproduction command are retained at
`harness/.work/perf205-response-pipeline-audit-20261011T095545Z`. In particular,
no mutex or block profile exists for the retained run, so this audit does not
close the Phase 2 contention-profile exit gate or claim zero contention.
The evidence bundle's verified `SHA256SUMS` hashes to
`578c1fe1d5ccc4b0f40b6071289282e9dfef190897d8b62cf5fa90c1bc23f002`.

Certification remains CLI-only. Do not distort ordinary web pause or live
filter semantics merely to improve the CLI ceiling.

Acceptance:

- Produce a ranked table with CPU percentage, allocation percentage, contention
  evidence, proposed change, semantic risk, and decision for every candidate.
- Implement only candidates contributing at least 5 percent of measured client
  CPU or allocation volume, unless a lower-cost change has separately measured
  end-to-end benefit.
- Every accepted change has a characterization test, a package benchmark, and
  paired end-to-end evidence. Record rejected candidates so a later agent does
  not repeat the same speculative work.

### Phase 2 exit gate

- Publish a before and after CPU flame graph, allocation profile, mutex profile,
  and microbenchmark table.
- Name the new limiting component.
- Repeat the native fixed sweep. Do not infer success from a microbenchmark.

## Phase 3: adaptive controller validation

### PERF-301: build a controlled latency, loss, and stream-limit matrix (tooling complete; live matrix pending)

Implementation status (2026-10-11): `network-matrix.sh` now supplies
fail-closed `apply`, `status`, and idempotent `cleanup` operations with
profile-owned namespaces, qdiscs, interface identity, collision detection,
rollback, captured before/after state, and the bounded mandatory, safety, and
exploratory fixture set below. Its shell tests use fake `ip`, `tc`, and related
commands and cover lifecycle, partial failure, residue, ownership, collision,
unsafe-input, platform, privilege, and missing-tool behavior. No real namespace
or `tc` mutation has been run for this evidence, so none of the live matrix,
50 ms worker-capacity, loss, recovery, or Phase 3 gates is claimed complete.

Primary files:

- new `harness/scripts/network-matrix.sh` and shell tests
- the configurable origin from PERF-002
- matrix fixtures and report tests under `harness/`

Add native-Linux test profiles for:

- RTT: 1, 10, 25, 50, and 100 ms
- controlled loss: 0, 0.01, 0.1, and 1 percent
- peer stream limits below, equal to, and above the local 256 target
- transient connection failure and recovery
- short and long wordlists
- server throttling and 429 responses

Use Linux network namespaces plus `tc netem`, or a dedicated proxy outside the
measured client process. Define listed latency values as round-trip time and
shape both directions at half the value. Record interface, namespace, qdisc,
direction, seed when supported, `CAP_NET_ADMIN` requirement, exact commands,
and before and after state. Install cleanup traps and fail if old qdiscs remain.
Loss is probabilistic even in a controlled profile, so record observed packet
loss as well as the configured value.

Use the configurable Go HTTP/3 origin from PERF-002 to set peer stream limits;
traffic control cannot do this. The origin must report the applied limit.

Bound the matrix:

- mandatory clean convergence gates: 1, 25, 50, and 100 ms RTT at zero loss,
  with peer limits 64 and 256, using a long wordlist
- mandatory safety gates: 25 ms at 0.1 and 1 percent loss, one transient
  connection failure, one 429 profile, and one deliberately short wordlist
- exploratory points: 10 ms RTT, 0.01 percent loss, other peer limits, and full
  Cartesian combinations only when a mandatory point exposes a defect

Short-wordlist and adverse-condition runs test safe bounded behavior and clean
drain. They are not required to converge to 90 percent of a fixed optimum.

The mandatory 50 ms, zero-loss, peer-limit-256 point also closes PERF-102's
end-to-end worker gate. With at least 20 healthy connections and a sufficiently
long input, `peak_main_in_flight` must reach at least 5,000 when
`-http3-max-workers` permits it. Its adaptive completed-response median must be
at least 90 percent of the equivalent fixed 20-connection,
256-stream, 5,000-or-more-worker median.

### PERF-302: validate tuning decisions

For each clean, saturated, sufficiently long mandatory point:

- sweep fixed connections first
- compare automatic mode with the best clean fixed result
- inspect connection growth, binary refinement, failure backoff, zero-progress
  backoff, steady reprobe, worker growth, connection-generation recovery, and
  any conditional slot replacement
- estimate a predeclared noise band from at least five fixed windows or runs;
  accept growth only when the measured improvement clears both 3 percent and
  that noise band
- prevent one-second transient noise from locking in a bad pool size

Acceptance:

- Automatic-mode median reaches at least 90 percent of the best clean fixed
  median for the same point. This gate does not apply to deliberate loss,
  throttling, failure, 429, or short-input safety tests.
- After convergence, it changes desired connection count no more than twice in
  any 30-second steady interval unless demand, latency, loss, or failure state
  changes.
- It does not grow while demand is unsaturated.
- It returns to at least 90 percent of its pre-failure five-second rolling rate
  within PERF-103's exact `recovery_deadline` after a transient connection
  failure.
- It does not convert server 429 responses into transport-failure backoff.

### PERF-303: expose effective limits

Primary files:

- `pkg/ffuf/run_telemetry.go`
- `pkg/runner/run_telemetry.go`
- HTTP/3 preflight report types and serializers
- schema compatibility and report fixture tests

Preflight and run telemetry should distinguish:

- configured maximum connections and streams
- opened and closed connection generations, plus active, accepting, draining,
  and retired pool slots when conditional retirement exists
- desired, live, and peak workers
- local admission capacity
- observed limiter occupancy and waiters
- peer-induced stream blocking when a supported quic-go hook exposes it
- completed responses, retries, surfaced transport failures, recovered
  generations, and conditional slot replacements

Worker and recovery counters needed by PERF-102 and PERF-103 landed with those
tasks; PERF-303 consolidates and documents them rather than deferring their
first implementation. Peer stream blocking is best-effort and non-gating unless
a supported dependency hook is identified. Always report configured local
admission and observed limiter queueing separately. Do not claim that the
16-stream fingerprint discovered the peer's maximum.

Acceptance:

- Old and new telemetry schemas parse without inventing unavailable values.
- Configured, desired, acknowledged, observed, and peer-derived values have
  distinct field names and provenance.
- Cross-field invariants and final zero in-flight state are fixture-tested.

### Phase 3 exit gate

- Every mandatory clean and safety matrix point has a retained report, or a
  named infrastructure dependency is recorded as blocked.
- Clean automatic-mode medians meet the 90 percent fixed-mode gate, including
  the 50 ms worker-capacity gate.
- Fault recovery meets PERF-103's usable-capacity, churn, error, and generation
  drain thresholds.
- Controller stability, no-growth-when-unsaturated, zero final in-flight work,
  and telemetry provenance gates pass.
- A failed correctness or lifecycle invariant stops Phase 4 and Phase 5. A
  performance miss with clean accounting proceeds to Phase 4 only when the
  profile gate below is satisfied.

## Phase 4: deeper HTTP/3 work, conditional on profiles

Start this phase only if the native client remains below 100k, the server and
network have demonstrated at least 20 percent headroom at the client's offered
load, and profiles attribute more than 5 percent of client CPU or allocation
volume to HTTP request encoding, `net/http` orchestration, or quic-go/QPACK
work. Complete Phase 3 first. Record Phase 4 as `hypothesis-disproved/skipped`
when these conditions do not hold.

### PERF-401: minimize request headers

Primary files:

- `pkg/runner/simple.go`
- high-rate preset construction and tests
- an offline pinned-qpack measurement helper or supported quic-go
  instrumentation spike

1. Enumerate the actual encoded field set: `:method`, `:scheme`, `:authority`,
   `:path`, the automatically added User-Agent, and every configured fixed
   header. Preserve the effective authority. An empty textual `Host` header is
   not the goal.
2. Confirm that a redundant ordinary `host` field is not emitted when
   `:authority` already carries `Request.Host` or the URL authority.
3. Add a minimal ceiling preset that omits only optional, tool-added headers.
   Never drop user-configured authorization, cookies, content negotiation, or
   routing headers. Do not send the Akamai/browser preset during a ceiling run
   unless the origin requires it.
4. Report uncompressed field bytes and encoded request-header bytes per request.
   First use a supported dependency hook. If none exists, encode an identical
   field list offline with the pinned qpack version and clearly label that value
   as an estimate rather than an on-wire observation.

Acceptance: smaller headers preserve identical authority, configured headers,
automatic User-Agent behavior, and target results. Merge only when paired
trials improve median RPS by at least `max(3 percent, noise_band)`, or reduce
client CPU or transmitted header bytes by at least 5 percent at equal RPS.

### PERF-402: evaluate dynamic QPACK authority reuse

The pinned qpack v0.6.0 implementation uses static and literal fields with no
dynamic table. Turbo Intruder explicitly reuses repeated `:authority` values
and batches decoder acknowledgements.

Implementation approach:

1. Measure header-encoding CPU and bytes before writing code.
2. Produce an upstream feasibility note against the pinned quic-go and qpack
   public APIs. h3fuzz cannot change transport-owned QPACK encoding solely in
   this repository today. Open or reference an upstream design issue before
   implementation.
3. Prefer an upstream-capable quic-go/qpack design. A private transport fork is
   a separately approved maintenance and security decision, not an incidental
   implementation detail.
4. Bound normal dynamic-table capacity and blocked-stream count, and define
   table eviction, connection reset, and decoder acknowledgement behavior. This
   is ordinary compression, distinct from intentionally held QPACK
   blocked-stream race gates, which remain out of scope.
5. Batch acknowledgements only when protocol timing remains compliant and no
   shared blocking lock is added to the response path.
6. Interoperate with Caddy and at least one independent H3 server.
7. Keep a static/literal fallback.

Acceptance:

- Merge only when paired trials improve median RPS by at least
  `max(5 percent, noise_band)`, or reduce client CPU by at least 5 percent at
  equal RPS.
- Zero decoding errors, blocked-stream leaks, or interoperability regressions.

This optimization is a candidate, not an established prerequisite for 100k.

### PERF-403: prototype a restricted direct H3 discovery path if still needed

Diagnostic update (A38, 2026-10-11): a smaller public-API experiment bypassed
`http.Client.Do` for an owned, strictly eligible HTTP/3 RoundTripper. Across
three clean alternating pairs it improved the treatment arm median by 2.0445%
and median within-pair RPS by 2.1048%, while reducing median user CPU by 4.0048%
and maximum RSS by 15.4917%. Because the predeclared acceptance gate required at
least 3% RPS, that bypass is rejected and reverted to accepted binary SHA-256
`b7d75777eb365d6d9f64d42fa292ff846693fc732e30937f7ea4f4772f6a8692`.
This experiment did not implement the lower-level owned-connection design below;
that larger spike remains conditional on new separated-host profiles after the
cheaper queued request/input work.

Turbo Intruder reduces hot-path work with a composed request buffer, one stream
write and flush, and buffered response reads. If `net/http` remains the proven
client bottleneck, begin with a bounded feasibility spike. Benchmark quic-go
v0.63's public `ClientConn.OpenRequestStream` plus
`RequestStream.SendRequestHeader`, `RequestStream.Close`, and
`RequestStream.ReadResponse` APIs before proposing a raw encoder.
`http3.Transport` does not expose its cached `ClientConn`, so this spike must
own a `*quic.Conn`, create the client with `Transport.NewClientConn`, and replace
the transport's cache, redial, and one-shot retry lifecycle explicitly. It is
not merely a cheaper call into the existing connection cache.

Primary deliverables:

- a throwaway benchmark under an internal experimental package
- a request and response differential test against `SimpleRunner`
- a design note covering connection ownership, cancellation, deadlines,
  control streams, SETTINGS, GOAWAY, peer stream limits, QPACK lifecycle, and
  clean shutdown
- a go or no-go decision with profile and RPS evidence

Eligibility must be narrow:

- one same-origin URL keyword
- bodyless GET transformed to qualified HEAD or Range
- no redirect following, proxy, preflight, replay, recursion, body matcher,
  body scraper, raw mode, audit output, or request-body fuzzing
- status and logical-size matching only

Requirements:

1. Exit after the feasibility spike unless it preserves cancellation and
   protocol lifecycle correctly and improves the eligible benchmark by at least
   5 percent.
2. If a lower-level implementation is still justified, encode pseudo-headers
   and ordinary headers according to HTTP/3 and QPACK specifications and own
   control streams, SETTINGS, GOAWAY, QPACK streams, peer limits, and shutdown
   explicitly.
3. Compose each HEADERS and optional DATA sequence into the minimum practical
   number of buffers and writes.
4. Parse and validate status, content length, content range, content type, and
   redirect metadata needed by h3fuzz.
   Loop over nonterminal 1xx responses, populate the response `Request` and TLS
   state expected by callers, and always drain or close the response body.
5. Preserve response-strategy validation and full GET recovery through the
   standard path when the wire response is untrustworthy.
6. Pool buffers with strict ownership and bounded sizes.
7. Add fuzz tests for frame and header decoding.
8. Differentially test requests and results against the standard path.
9. Never import or copy Turbo Intruder code.
10. Wait for handshake completion before ordinary requests, or use the regular
    `quic.Dial` path. Raw `OpenRequestStream` does not itself provide the
    ordinary-method 0-RTT guard that `ClientConn.RoundTrip` provides.

Acceptance:

- The restricted path is optional and reports when it is ineligible.
- Existing ffuf behavior remains on the standard path.
- Differential tests cover status, redirects, malformed metadata, empty
  bodies, pseudo-header validation, QPACK limits, oversized headers, trailers,
  stream cancellation, connection closure, deadlines, GOAWAY, and GET recovery.
- Merge only if it closes a measured gap that Phases 1 through 3 did not.

### Phase 4 exit gate

- Each of PERF-401 through PERF-403 is recorded as passed,
  hypothesis-disproved/skipped, infrastructure-blocked, or failed.
- Every merged path meets its numeric benefit threshold and all differential,
  interoperability, race, fuzz, drain, and fallback tests.
- No private transport fork or upstream issue is created without maintainer or
  user approval. The implementing agent may prepare a design and draft issue,
  but may not publish it or add the dependency without that approval.
- Repeat the fixed native sweep after any merged Phase 4 change. A clean miss is
  reported honestly and does not become a 100k claim.

## Phase 5: certification and release evidence

### PERF-501: run the final fixed sweep (producer complete; live sweep pending)

Implementation status (2026-10-11): the decision-policy generator, fixed-sweep
runner and reporter, schemas, deterministic inclusive near-best selection,
relative artifact hashing, and fail-closed tamper validation are implemented.
Every sweep point requires genuine client CPU, server CPU, network utilization,
and final-attempt time-to-first-byte p99 evidence before it can be clean. The
portable suites pass, but no final native-Linux sweep or selected-fixed
validation cohort is claimed here.

Primary files:

- new `harness/scripts/fixed-sweep.sh`
- new `harness/scripts/create-decision-policy.py`
- new `harness/scripts/fixed-sweep-report.py` and fixture tests
- certification schema and artifact tests

On the certification environment:

- connections: 4, 8, 10, 16, 20, 32, 40, and 64, plus 128 and 256 when the
  measured latency and usable stream capacity justify them
- streams: 64, 128, 256
- the frozen wordlist from PERF-004, sized for at least 30 seconds of
  main-request span
- HEAD ceiling, Range ceiling, and GET control where supported

Run exactly three trials per fixed matrix point and rank clean medians, not
single observations. Use the predeclared Phase 0 noise band. A plateau means
the next clean median fails to improve on the prior best by
`max(3 percent, noise_band)`. A point worsens when an eligibility error, UDP
drop, loss, reconciliation, or drain gate fails, or when client CPU, server CPU,
or final-attempt TTFB p99 exceeds its predeclared limit without the same minimum throughput
improvement. Then run five validation trials at the selected fixed setting.

`fixed-sweep.sh` emits a content-hashed `fixed-sweep-manifest.json` containing
the complete candidate matrix, executed and skipped points with reasons, three
raw reports per executed point, clean or ineligible status, medians, the
predeclared noise band and resource limits, and the selected point. Let
`M_best` be the highest clean median and
`T = max(3, noise_band_percent)`. Form the near-best set using the inclusive
rule `100 * (M_best - M_i) / M_best <= T`. From that set choose the candidate
with the lowest `connections * streams`, then fewer connections, then fewer
streams. This is a total, non-pairwise selection rule. The manifest records
every intermediate value and the derivation. Set `adaptive_max_connections` equal to the
selected fixed connection count and use the selected stream count for both
eligibility cohorts. This guarantees the adaptive controller is allowed to
reach, but not exceed, the fixed optimum used for comparison. The manifest
records the shared platform, build, and workload keys plus every point's
treatment and cohort keys. It also records `adaptive_max_workers` equal to the
fixed sweep's `--max-workers`; certification may not replace that bound.

### PERF-502: run five adaptive trials (alternating runner complete; live cohort pending)

Implementation status (2026-10-11): `certify.py` executes the cohorts as five
explicit pairs—`adaptive-1`, `selected-fixed-1`, through `adaptive-5`,
`selected-fixed-5`—and records the ten-step `cohort_execution_order`. This
removes an all-adaptive-then-all-fixed time-drift bias without changing the
five-report requirement for either cohort. The ordering contract is tested;
the native-Linux cohort itself remains pending.

Use the selected stream count and `adaptive_max_connections` derived in the
fixed-sweep manifest. Run five adaptive trials without changing settings,
alternating each one with the corresponding selected-fixed validation trial,
and compare the two medians for the same route, strategy, streams, input, and
environment. The headline
certification requires the five-run adaptive median to reach 100k and at least
90 percent of the fixed median. Store every raw report and the aggregate
certification report.

### PERF-503: validate discovery realism (producers complete; live manifests pending)

Implementation status (2026-10-11): `validate-files.sh` and
`collect-diagnostics.sh`, their Python contracts/trial adapter, and both
versioned schemas are implemented. Fixture tests cover exact hits, seeded long
corpora, false positives, filter equivalence, profile/control pairing, layered
keys, missing or extra members, and tamper rejection. No live native-Linux
`/files` or diagnostic manifest produced by these scripts is claimed here.

Primary files:

- new `harness/scripts/validate-files.sh`
- new `harness/scripts/collect-diagnostics.sh`
- new `harness/schemas/files-validation-manifest-v1.schema.json`
- new `harness/schemas/diagnostics-manifest-v1.schema.json`
- fixture and tamper tests for both manifests

Run `/files/FUZZ` separately. Use `harness/wordlists/correctness.txt` once for
exact hit correctness. Generate and freeze a second seeded, sufficiently long
realistic corpus containing the known hit names and unique miss names for
throughput. Do not prefix or rewrite the known paths when repeating input,
because that destroys the seeded hits. Define duplicate accounting if any exact
inputs are repeated. Prove:

- every expected file is found
- no false result is introduced by HEAD or Range reconstruction
- result filters behave identically to the GET control for compatible fields
- throughput and server workload are reported separately from `/bench/FUZZ`

`validate-files.sh` produces a content-hashed manifest covering the exact-hit
correctness run and the seeded long-corpus throughput run, with layered keys,
client and server evidence, request strategy, expected and actual hits, false
positives, filter comparison, throughput, and relative artifact hashes.
`collect-diagnostics.sh` runs separate CPU, heap, mutex, block, and QUIC
diagnostic trials using the selected sweep configuration. It pairs each profile
with an unprofiled control, records overhead, never reuses an eligibility run,
and emits a content-hashed manifest matching the selected point's platform,
build, and workload keys except for the named diagnostic treatment. Both
manifests are required inputs to `certify.sh`.

### PERF-504: retain and publish the evidence manifest

The bundle must contain:

- all ten eligibility-run reports and raw telemetry, comprising five adaptive
  and five selected-fixed runs
- every raw preliminary fixed-sweep report used to select the fixed setting
- environment and UDP snapshots
- route/protocol server counters
- aggregate statistics and eligibility decision
- exact commands
- binary, source, wordlist, and image hashes
- separately labeled diagnostic CPU, allocation, mutex, block, and QUIC
  profiles from matching configurations, never from any of the ten eligibility
  runs
- fixed-sweep comparison
- `/files/FUZZ` correctness and throughput result
- known limitations

Create two archives. The permission-restricted raw archive contains exact
commands, environment, logs, reports, and telemetry. The separately generated
publishable archive contains a redacted manifest and safe evidence, removing
Authorization and Cookie headers, proxy credentials, target secrets, private
URLs, tokens, and sensitive environment values while retaining hashes and
non-secret configuration needed for comparison. Never describe unsanitized
commands as publishable. Complete each internal manifest, hash each final
archive, record both hashes, and retain both in the durable store required by
PERF-004 with access appropriate to their sensitivity.

Only after the aggregate passes should README wording change from "pursuing
100k" to a precise claim tied to the tested environment and workload.

### Phase 5 exit gate

- `certify.sh` exits 0 and both archive hashes verify from durable storage.
- All ten eligibility runs pass duration, accounting, error, reconciliation,
  UDP, protocol, strategy, and drain gates independently. Individual run RPS is
  reported but has no 100k threshold; the rate threshold applies to the
  adaptive cohort median below.
- The adaptive median is at least 100,000 completed logical responses per
  main-request-span second and at least 90 percent of the selected-fixed median.
- `/files/FUZZ` correctness passes and its separately labeled throughput result
  is present.
- The release approver records `passed`. Any other task or phase state leaves
  the public claim unchanged.

## Versioned implementation contract

This section defines contract version 1. The implementing agent should use
these exact names so tasks developed in parallel interoperate. A change requires
a short decision record, schema-version increment when serialized data changes,
and fixture compatibility tests.

### Binary flags

General HTTP/3 option, available through CLI, config, web defaults, persisted
sessions, and CLI import:

```text
-http3-max-workers <positive integer, default 65536>
```

CLI-only ceiling policy options:

```text
-http3-high-rate
-http3-high-rate-acknowledge
-http3-target-rps <positive integer, default 100000>
-http3-latency-ms <positive decimal milliseconds>
```

CLI-only harness and diagnostic options, rejected by the web API:

```text
-harness-run-id <UUID>
-harness-mode baseline|diagnostic|sweep|adaptive|selected-fixed
-harness-phase-file <path>
-profile-cpu <path>
-profile-heap <path>
-profile-mutex <path>
-profile-block <path>
-quic-telemetry
-quic-qlog-dir <directory>
```

`-run-telemetry <path>` remains the required structured client report. The
high-rate preset requires it. `-quic-telemetry` writes bounded aggregates into
that report. `-quic-qlog-dir` writes full diagnostic qlogs and makes the run
ineligible unless a retained overhead experiment explicitly allows them.

The three harness flags are accepted only in CLI mode. Harness mode and an
owner-only phase file are required when a run ID is present; all three must be
absent for an ordinary run.
`SimpleRunner.Execute` adds the following headers to marked main-workload
transport attempts after fingerprint, warmup, and strategy qualification:

```text
X-H3fuzz-Harness-Run: <UUID>
X-H3fuzz-Harness-Role: initial|optimization_fallback|engine_retry|engine_retry_optimization_fallback
```

Reject a user-supplied collision with either header. Both an optimized attempt
and its GET recovery carry the same run ID and their distinct role. Do not add
these headers when the flag is absent.

The binary creates `-harness-phase-file` with mode 0600 before network work and
appends one JSON line per transition, flushing each line:

```json
{
  "schema_version": 1,
  "run_id": "UUID",
  "pid": 1,
  "process_start_ticks": "unsigned-decimal-string",
  "sequence": 1,
  "phase": "fingerprint|warmup|qualification|main|drain|closed",
  "at": "RFC3339Nano"
}
```

Sequence starts at 1 and increases by one. `main` is emitted immediately before
the first marked outer `Runner.Execute`; `drain` is emitted after the last
logical terminal transition and before transport shutdown; `closed` follows
all worker and transport joins. The socket sampler tails this file to label live
samples. Missing, duplicate, reordered, PID-mismatched, or unflushed phase
events make a harness run ineligible. The monotonic span inside telemetry
remains the RPS denominator; wall-clock phase events are synchronization only.

### Client telemetry schema

`run-telemetry.json` has top-level `schema_version: 2` and retains a reader for
schema version 1. Version 2 contains at minimum:

```json
{
  "schema_version": 2,
  "run_id": null,
  "eligible_mode": "ordinary|baseline|diagnostic|sweep|adaptive|selected_fixed",
  "responses": {
    "main_request_span_ns": null,
    "scheduled_logical_inputs": 0,
    "terminal_logical_outcomes": 0,
    "successful_logical_responses": 0,
    "terminal_logical_errors": 0,
    "latency": {
      "schema_version": 1,
      "source": "client_final_attempt_time_to_first_byte",
      "scope": "main_successful_logical_responses",
      "clock": "monotonic",
      "unit": "nanoseconds",
      "method": "bounded_histogram_upper_bound",
      "sample_count": 0,
      "p99_upper_bound_ns": 0,
      "maximum_trackable_ns": 120000000000,
      "overflow_count": 0
    }
  },
  "logical_ledger": {
    "expected_expanded_inputs": null,
    "assigned_ids": 0,
    "scheduled_transitions": 0,
    "success_terminal_states": 0,
    "error_terminal_states": 0,
    "pending_scheduled_states": 0,
    "id_gaps": 0,
    "duplicate_schedule_transitions": 0,
    "duplicate_terminal_transitions": 0,
    "invalid_transitions": 0,
    "out_of_range_ids": 0,
    "anomaly_sample_ids": []
  },
  "transport": {
    "application_attempts": 0,
    "surfaced_attempt_errors": 0,
    "application_retries": 0,
    "observed_pre_handler_application_failures": 0,
    "internal_attempts": null,
    "internal_retries": null
  },
  "workers": {
    "latest_proposal_epoch": 0,
    "latest_ack_epoch": 0,
    "desired": 0,
    "live": 0,
    "retiring": 0,
    "peak_live": 0,
    "peak_main_in_flight": 0
  },
  "drain": {
    "live_workers": 0,
    "main_in_flight": 0,
    "limiter_in_use": 0,
    "tracked_connection_generations": 0
  }
}
```

`run_id` is a UUID for harness runs and JSON `null` for an ordinary
`-run-telemetry` scan without `-harness-run-id`. `main_request_span_ns` is a
positive integer for every nonempty main run and JSON `null` for an empty scan;
zero is invalid. In a sweep or eligibility report, every logical-ledger anomaly
field must be zero and
`expected_expanded_inputs == assigned_ids == scheduled_transitions ==
success_terminal_states + error_terminal_states`. The expected count may be
JSON `null` only for an ordinary non-certification run.

`responses.latency` is versioned separately and summarizes
`ffuf.Response.Duration`: monotonic time to first byte for the final attempt of
each successful main logical response. It is not full-body application response
time and is not QUIC smoothed RTT. `sample_count` must equal
`successful_logical_responses`. `p99_upper_bound_ns` is the upper edge of the
bounded-histogram bucket containing the nearest-rank p99 sample, so it is a
conservative bound rather than an exact quantile. Sweep evidence requires a
positive bound when samples exist, a bound no greater than
`maximum_trackable_ns`, and zero overflow. The object is omitted when structured
run telemetry is disabled. The retained native-Linux arena A/B exercised this
path under six clean loads: every run recorded exactly 2,335,820 samples, equal
to its successful logical-response count, with zero overflow. That validates
the producer under load but is not Phase 5 latency or rate evidence.

Add role, method, pool, generation, QUIC aggregate, policy, timing, and
effective-limit fields described by the PERF tasks. Use JSON `null` plus a
machine-readable unavailability reason for unsupported dependency metrics.
Never serialize an unavailable counter as zero.

### Origin counter schema and collection

The dedicated origin exposes `GET /admin/v1/snapshot` only on its loopback
admin listener. Its response and the server-local snapshot file use:

```json
{
  "schema_version": 1,
  "snapshot_id": "UUID",
  "captured_at": "RFC3339Nano",
  "origin": {
    "origin_instance_id": "UUID",
    "started_at": "RFC3339Nano",
    "build_sha256": "hex",
    "config_sha256": "hex",
    "configured_max_incoming_streams": 256,
    "effective_max_incoming_streams": 256
  },
  "counters": [
    {
      "run_id": null,
      "phase": "main|unmarked",
      "route": "bench|large|files|other",
      "protocol": "h1|h2|h3",
      "method": "HEAD|GET|other",
      "role": null,
      "handler_requests": 0,
      "h3_attempts": null,
      "h3_request_rejections": null
    }
  ]
}
```

`run_id` is the marker UUID for `phase: "main"` and JSON `null` for unmarked
fingerprint, warmup, qualification, health, and unrelated requests. Before and
after snapshots provide the global delta for unmarked traffic. `role` is one of
the marked role values for main work and JSON `null` for unmarked traffic.

The server-side command is:

```text
harness/scripts/origin-snapshot.sh \
  --admin-url http://127.0.0.1:<admin-port> \
  --output <absolute-json-path>
```

It writes to a temporary sibling file, validates the schema, renames atomically,
and creates the result owner-readable. A remote deployment runs this command on
the server. A client-side provider wrapper may use SSH or an artifact service,
but credentials and transport belong to the operator, not `certify.sh`.
Before and after snapshots must have the same `origin_instance_id`; a restart,
counter reset, or apparent wrap makes the trial ineligible.

The snapshot-provider executable contract is:

```text
<provider> --phase before|after --run-id <UUID> --output <absolute-json-path>
```

`certify.sh` executes the provider directly with argv, never through `sh -c`.
The provider must return only after the complete server-local snapshot has been
copied to the requested client path.

PERF-003 host evidence uses a separate provider because origin counters are not
a substitute for kernel, socket, interface, cgroup, and CPU evidence:

```text
<server-evidence-provider> \
  --phase stable|before|after \
  --run-id <UUID> \
  --output <absolute-json-path>
```

The `stable` output contains host identity, kernel, CPU allocation and affinity,
sysctls, origin image and config identity, and the evidence script version.
`before` and `after` contain interface and namespace UDP counters, socket
buffers, process and cgroup CPU, throttling, frequency policy, and timestamps.
The provider runs PERF-003's versioned server-local snapshot scripts and copies
their output atomically. `certify.sh` hashes stable output into the environment
key and retains before and after output for every trial.

Validate these files against committed JSON Schemas. Unknown additive fields
are allowed, but the following required contract is stable.

`server-stable-v1.schema.json` requires:

```json
{
  "schema_version": 1,
  "kind": "server_stable",
  "provider_version": "string",
  "captured_at": "RFC3339Nano",
  "host_identity_sha256": "hex",
  "boot_id": "UUID",
  "kernel": {"sysname": "Linux", "release": "string"},
  "cpu": {
    "model": "string",
    "logical_cpus": 1,
    "affinity_cpus": [0],
    "frequency_governor": "string"
  },
  "memory_bytes": 1,
  "network_namespace_inode": "decimal-string",
  "udp_sysctls_bytes": {
    "rmem_default": 1,
    "rmem_max": 1,
    "wmem_default": 1,
    "wmem_max": 1
  },
  "origin": {
    "binary_sha256": "hex",
    "image_id_or_digest": "string",
    "config_sha256": "hex"
  }
}
```

`server-point-v1.schema.json` requires:

```json
{
  "schema_version": 1,
  "kind": "server_point",
  "phase": "before|after",
  "run_id": "UUID",
  "captured_at": "RFC3339Nano",
  "host_identity_sha256": "hex",
  "boot_id": "UUID",
  "network_namespace_inode": "decimal-string",
  "udp_counters": [
    {
      "source": "/proc/net/snmp:Udp",
      "name": "RcvbufErrors",
      "value": "unsigned-decimal-string",
      "width_bits": 64
    }
  ],
  "interfaces": [
    {
      "ifindex": 1,
      "name": "string",
      "link_speed_bits_per_second": "unsigned-decimal-string-or-null",
      "rx_bytes": "unsigned-decimal-string",
      "tx_bytes": "unsigned-decimal-string",
      "rx_dropped": "unsigned-decimal-string",
      "tx_dropped": "unsigned-decimal-string",
      "rx_errors": "unsigned-decimal-string",
      "tx_errors": "unsigned-decimal-string",
      "counter_width_bits": 64,
      "source": "sysfs|netlink"
    }
  ],
  "processes": [
    {
      "role": "origin",
      "pid": 1,
      "start_ticks": "unsigned-decimal-string",
      "affinity_cpus": [0],
      "cpu_time_ns": "unsigned-decimal-string",
      "cgroup_path_sha256": "hex",
      "cgroup_cpu_throttled_ns": "unsigned-decimal-string"
    }
  ],
  "sockets": [
    {
      "role": "origin",
      "pid": 1,
      "inode": "unsigned-decimal-string",
      "local_address": "string",
      "raw_so_rcvbuf_bytes": 7340032,
      "raw_so_sndbuf_bytes": 7340032,
      "source": "getsockopt|ss",
      "linux_kernel_doubling_applies": true
    }
  ]
}
```

Counter values are decimal strings to avoid JSON integer precision loss. Before
and after files must match host identity, boot ID, namespace inode, interface
index, process PID and start ticks, and counter source. Compute deltas with
arbitrary-precision unsigned integers. Treat a decrease as a wrap only when the
old value lies in the top 5 percent and the new value in the bottom 5 percent
of the declared counter width; otherwise classify it as reset and make the run
ineligible. Missing mandatory counters or sockets are JSON `null` with a reason
and make an eligibility run fail.

Each line of `client-runtime-sockets.jsonl` validates against
`client-runtime-socket-v1.schema.json` and requires schema version, run ID,
RFC3339Nano sample time, `warmup|main|drain` phase, PID and process start ticks,
socket inode, local address, raw receive and send buffer bytes, source, and the
Linux doubling boolean. At least one `main` sample is required for every
observed h3fuzz UDP socket. Raw buffer values use `getsockopt` units and the
7,340,032-byte gate defined in PERF-003.

### Harness command contracts

Wordlist calibration:

```text
harness/scripts/calibrate-wordlist.sh \
  --target <https-url-containing-FUZZ> \
  --base-wordlist <absolute-path> \
  --seed <unsigned-integer> \
  --target-rps <positive-integer> \
  --target-seconds 30 \
  --headroom-percent 20 \
  [--extensions <comma-separated>] \
  --expect-strategy head|range|get \
  [--filter-status <ffuf-expression>] \
  [--filter-size <ffuf-expression>] \
  --streams <positive-integer> \
  --max-connections <positive-integer> \
  --max-workers <positive-integer> \
  --server-snapshot-provider <absolute-executable> \
  --server-evidence-provider <absolute-executable> \
  --output <absolute-wordlist-path> \
  --manifest <absolute-json-path> \
  --artifact-dir <absolute-directory>
```

The script performs at most three unmeasured sizing iterations, uses the exact
formula in PERF-004, and asks the h3fuzz input provider for the expanded count
when extensions are present. It writes deterministic unique misses while
preserving known hit entries and marks the manifest `frozen: true` only after
hashing the final file and all generation inputs.

Single trial:

```text
harness/scripts/benchmark.sh \
  --target <https-url-containing-FUZZ> \
  --wordlist <absolute-path> \
  --mode baseline|diagnostic|sweep|adaptive|selected-fixed \
  [--extensions <comma-separated>] \
  --expect-strategy head|range|get \
  [--filter-status <ffuf-expression>] \
  [--filter-size <ffuf-expression>] \
  --streams <positive-integer> \
  [--connections <positive-integer>] \
  [--max-connections <positive-integer>] \
  [--threads <positive-integer>] \
  [--max-workers <positive-integer>] \
  --run-id <UUID> \
  --server-snapshot-provider <absolute-executable> \
  --server-evidence-provider <absolute-executable> \
  --client-before <absolute-json-path> \
  --client-after <absolute-json-path> \
  --client-runtime-sockets <absolute-jsonl-path> \
  --server-counters-before <absolute-json-path> \
  --server-counters-after <absolute-json-path> \
  --server-stable <absolute-json-path> \
  --server-evidence-before <absolute-json-path> \
  --server-evidence-after <absolute-json-path> \
  --artifact-dir <absolute-directory>
```

Fixed modes require `--connections`; adaptive mode requires
`--max-connections`. Sweep mode may be underprovisioned and may set
`eligible_for_aggregation` for fixed-point selection, but it never sets the
aggregate-only `certification_eligible` field. Adaptive and selected-fixed modes
omit `--threads`; baseline or diagnostic mode is the only place to deliberately
measure legacy explicit worker semantics. The script
requires at least one baseline status or logical-size filter and fails when the
preflight-selected request strategy differs from `--expect-strategy`. The script
captures client before and after snapshots itself. It starts a PID-scoped
socket sampler before h3fuzz, waits for a post-warmup UDP socket, samples through
the main phase, and stops it after process exit. Failure to obtain a live sample
makes the trial ineligible. The script uses the provider for the
server counter paths and the evidence provider for the three server evidence
paths. It records the literal argv array, not a shell-reconstructed string.

Mode-to-binary policy is exact:

| Benchmark mode | Binary policy |
| --- | --- |
| `baseline` | Set harness mode and run ID, fixed connections and streams, structured telemetry, and the logical ledger. Do not set the high-rate preset. Permit optional explicit `-t`. Keep original datagram settings and disable PERF-006 lifecycle and packet recorders. Use only for B0/B1 comparison, never Phase 5. |
| `sweep` | Set harness mode and run ID, fixed connections and streams, structured telemetry, logical ledger, and mandatory minimal generation lifecycle recording. Omit `-t` and the high-rate preset so intentionally underprovisioned points report rather than fail. |
| `adaptive` | Set harness mode and run ID, `-http3-high-rate`, acknowledgement, target RPS, manifest-derived maximum connections and streams, maximum workers, structured telemetry, logical ledger, and minimal generation recording. Omit fixed connections and `-t`. |
| `selected-fixed` | Set harness mode and run ID, `-http3-high-rate`, acknowledgement, target RPS, manifest-selected fixed connections and streams, maximum workers, structured telemetry, logical ledger, and minimal generation recording. Omit maximum connections and `-t`. |
| `diagnostic` | Reproduce the manifest-selected effective configuration, add exactly one requested profile or QUIC diagnostic layer, set harness mode and run ID, and mark the run ineligible for rate aggregation. Pair it with an unprofiled control. |

Reject mode-incompatible arguments rather than silently ignoring them. All
modes force direct HTTP/3 and fail downgrade. `baseline` is the sole exception
to mandatory generation lifecycle recording because its purpose is to preserve
pre-PERF-006 behavior; its drain evidence is diagnostic, not certification
evidence.

For every `sweep` run, `benchmark-v1.py` derives and validates a versioned
`sweep-resource-usage.json` before `benchmark-report.py` can mark the point
aggregation-eligible. Client and origin CPU percentages come from matched
PERF-003 process-time point pairs. Network utilization is the maximum observed
full-duplex direction utilization from matched client and server interface-byte
deltas and recorded link speeds. Latency p99 comes only from the versioned
`responses.latency.p99_upper_bound_ns` final-attempt TTFB histogram. The report
retains the input hashes, formulas, capture scopes, interface identities, sample
count, histogram maximum, and overflow count. Missing, reset, mismatched,
unbounded, overflowing, or otherwise invalid evidence makes the sweep point
ineligible; QUIC RTT is never substituted.

Fixed sweep and deterministic selection:

```text
harness/scripts/fixed-sweep.sh \
  --target <https-url-containing-FUZZ> \
  --wordlist <frozen-absolute-path> \
  --wordlist-manifest <absolute-json-path> \
  [--extensions <comma-separated>] \
  --expect-strategy head|range|get \
  [--filter-status <ffuf-expression>] \
  [--filter-size <ffuf-expression>] \
  --connections <comma-separated-positive-integers> \
  --streams <comma-separated-positive-integers> \
  --trials-per-point 3 \
  --max-workers <positive-integer> \
  --decision-policy <absolute-json-path> \
  --server-snapshot-provider <absolute-executable> \
  --server-evidence-provider <absolute-executable> \
  --artifact-dir <absolute-directory> \
  --manifest <absolute-json-path>
```

`--trials-per-point` must equal three in contract version 1. Decision-policy schema
version 1 contains `noise_band_percent`, `client_cpu_max_percent`,
`server_cpu_max_percent`, `network_utilization_max_percent`,
`p99_latency_max_ms`, and `minimum_improvement_percent`.
`noise_band_percent` is finite and nonnegative; the resource limits are finite
positive numbers recorded before the sweep, and
`minimum_improvement_percent == max(3, noise_band_percent)`. The script invokes
`benchmark.sh`, hashes every raw point, applies PERF-501's clean-point and
tie-break rules, and writes `fixed-sweep-manifest.json`. Referenced paths are
relative to the manifest's parent artifact root so the complete tree is
portable.

Create that policy before starting the sweep:

```text
python3 harness/scripts/create-decision-policy.py \
  --baseline-manifest <absolute-json-path> \
  --client-cpu-max-percent <positive-decimal> \
  --server-cpu-max-percent <positive-decimal> \
  --network-utilization-max-percent <positive-decimal> \
  --p99-latency-max-ms <positive-decimal> \
  --output <absolute-json-path>
```

The command accepts the retained B0 baseline manifest only. Given clean
fixed-duration window rates `r`, compute
`noise_band_percent = 100 * 1.4826 * median(abs(r - median(r))) / median(r)`.
Require at least five samples and a positive median; identical windows validly
produce zero. Compute `minimum_improvement_percent`, write the creation time and
all source hashes, and refuse to overwrite an existing policy. B1 reports the
same estimator as drift evidence but never changes the frozen B0 policy.
Creating the policy after the sweep artifact directory exists makes the sweep
ineligible.

Discovery validation:

```text
harness/scripts/validate-files.sh \
  --target <https-url-containing-/files/FUZZ> \
  --correctness-wordlist <absolute-path> \
  --throughput-base-wordlist <absolute-path> \
  --seed <unsigned-integer> \
  --target-seconds 30 \
  --fixed-sweep-manifest <absolute-json-path> \
  --server-snapshot-provider <absolute-executable> \
  --server-evidence-provider <absolute-executable> \
  --artifact-dir <absolute-directory> \
  --manifest <absolute-json-path>
```

Diagnostic collection:

```text
harness/scripts/collect-diagnostics.sh \
  --target <https-url-containing-/bench/FUZZ> \
  --wordlist <frozen-absolute-path> \
  --wordlist-manifest <absolute-json-path> \
  --expect-strategy head|range|get \
  [--filter-status <ffuf-expression>] \
  [--filter-size <ffuf-expression>] \
  --fixed-sweep-manifest <absolute-json-path> \
  --profiles cpu,heap,mutex,block,quic \
  --server-snapshot-provider <absolute-executable> \
  --server-evidence-provider <absolute-executable> \
  --artifact-dir <absolute-directory> \
  --manifest <absolute-json-path>
```

Both scripts validate their version 1 manifest schema, use paths relative to
the manifest parent, hash every member, and record the layered comparison keys.
`validate-files.sh` requires the same platform and build keys and authority as the ceiling target and
both the exact-hit and long-corpus run. `collect-diagnostics.sh` performs a
separate run and clean paired control for every requested profile. A missing,
extra, mismatched, or tampered member is a certification prerequisite failure.

Five-by-five certification:

```text
harness/scripts/certify.sh \
  --target <https-url-containing-FUZZ> \
  --wordlist <frozen-absolute-path> \
  --wordlist-manifest <absolute-json-path> \
  [--extensions <comma-separated>] \
  --expect-strategy head|range|get \
  [--filter-status <ffuf-expression>] \
  [--filter-size <ffuf-expression>] \
  --fixed-sweep-manifest <absolute-json-path> \
  --files-validation-manifest <absolute-json-path> \
  --diagnostics-manifest <absolute-json-path> \
  --target-rps 100000 \
  --server-snapshot-provider <absolute-executable> \
  --server-evidence-provider <absolute-executable> \
  --artifact-store <durable-absolute-directory>
```

This command always runs five adaptive and five selected-fixed eligibility
trials in the explicit order `adaptive-1`, `selected-fixed-1`, through
`adaptive-5`, `selected-fixed-5`, and records that order as
`cohort_execution_order`. Trial count is not user-reducible in certification
mode. It refuses an
unfrozen or undersized wordlist, mixed environment or cohort key, missing
server counter snapshot, non-native-Linux host, and nonempty diagnostic flags.
It resolves and verifies every sweep artifact relative to the sweep manifest,
recomputes the selected fixed connection and stream setting, uses that stream
setting, `adaptive_max_connections`, and `adaptive_max_workers` for the two
cohorts, verifies the discovery and diagnostic manifest schemas, hashes, and
required layered keys, copies all three evidence trees into the certification
bundle, and rejects any caller-selected connection, stream, or worker override.

Exit codes are stable:

| Code | Meaning |
| --- | --- |
| 0 | Eligible certification passed every gate |
| 1 | Unexpected internal tooling error |
| 2 | Invalid arguments or missing prerequisite |
| 3 | Runs completed but the certification is ineligible or below threshold |
| 4 | Benchmark, snapshot-provider, origin, network, or cleanup failure |
| 5 | Raw or publishable artifact retention or hashing failure |

### Artifact layout

The durable store contains one immutable directory per certification ID:

```text
<store>/<certification-id>/
  aggregate/certification-report.json
  aggregate/certification-report.md
  environment/client-stable.json
  environment/server-stable.json
  cohorts/adaptive/run-01/ ... run-05/
  cohorts/selected-fixed/run-01/ ... run-05/
  sweeps/<point-id>/run-01/ ... run-03/
  discovery/files-correctness/
  discovery/files-throughput/
  diagnostics/cpu/
  diagnostics/heap/
  diagnostics/mutex/
  diagnostics/block/
  diagnostics/quic/
  manifests/raw-manifest.json
  manifests/publishable-manifest.json
  h3fuzz-certification-raw.tar.zst
  h3fuzz-certification-raw.tar.zst.sha256
  h3fuzz-certification-public.tar.zst
  h3fuzz-certification-public.tar.zst.sha256
```

Every eligibility run directory contains client telemetry, client and server
before and after snapshots, server counters, stdout and stderr, exact argv JSON,
the client runtime socket sample, environment and cohort keys, eligibility
report, and hashes. Every sweep report is retained even when it loses. Raw
files and the raw archive are mode 0600;
the publishable archive is built only from a redaction allowlist.

### Canonical phase commands

These commands become valid after their owning PERF tasks are implemented.
They are templates with explicit paths, not evidence by themselves.

B0 fixed baseline point:

```sh
go build -o ./h3fuzz .
harness/scripts/benchmark.sh \
  --target https://SERVER_NAME:8443/bench/FUZZ \
  --wordlist /data/h3fuzz/frozen-baseline.txt \
  --mode sweep --expect-strategy head --filter-status 204 \
  --connections 20 --streams 256 --threads 5120 \
  --run-id 11111111-1111-4111-8111-111111111111 \
  --server-snapshot-provider /opt/h3fuzz/server-snapshot-provider \
  --server-evidence-provider /opt/h3fuzz/server-evidence-provider \
  --client-before /data/h3fuzz/b0/client-before.json \
  --client-after /data/h3fuzz/b0/client-after.json \
  --client-runtime-sockets /data/h3fuzz/b0/client-runtime-sockets.jsonl \
  --server-counters-before /data/h3fuzz/b0/server-before.json \
  --server-counters-after /data/h3fuzz/b0/server-after.json \
  --server-stable /data/h3fuzz/b0/server-stable.json \
  --server-evidence-before /data/h3fuzz/b0/server-evidence-before.json \
  --server-evidence-after /data/h3fuzz/b0/server-evidence-after.json \
  --artifact-dir /data/h3fuzz/b0/c20-s256-t5120-run1
```

Phase 3 controlled point:

```sh
trap 'harness/scripts/network-matrix.sh cleanup --profile-id rtt50-loss0' EXIT
harness/scripts/network-matrix.sh apply \
  --profile-id rtt50-loss0 --rtt-ms 50 --loss-percent 0 --seed 1001
harness/scripts/benchmark.sh \
  --target https://SERVER_NAME:8443/bench/FUZZ \
  --wordlist /data/h3fuzz/frozen-matrix.txt \
  --mode adaptive --expect-strategy head --filter-status 204 \
  --max-connections 20 --streams 256 --max-workers 65536 \
  --run-id 22222222-2222-4222-8222-222222222222 \
  --server-snapshot-provider /opt/h3fuzz/server-snapshot-provider \
  --server-evidence-provider /opt/h3fuzz/server-evidence-provider \
  --client-before /data/h3fuzz/matrix/client-before.json \
  --client-after /data/h3fuzz/matrix/client-after.json \
  --client-runtime-sockets /data/h3fuzz/matrix/client-runtime-sockets.jsonl \
  --server-counters-before /data/h3fuzz/matrix/server-before.json \
  --server-counters-after /data/h3fuzz/matrix/server-after.json \
  --server-stable /data/h3fuzz/matrix/server-stable.json \
  --server-evidence-before /data/h3fuzz/matrix/server-evidence-before.json \
  --server-evidence-after /data/h3fuzz/matrix/server-evidence-after.json \
  --artifact-dir /data/h3fuzz/matrix/rtt50-loss0
```

Phase 5 certification:

```sh
harness/scripts/calibrate-wordlist.sh \
  --target https://SERVER_NAME:8443/bench/FUZZ \
  --base-wordlist /data/h3fuzz/final.txt --seed 1001 \
  --target-rps 100000 --target-seconds 30 --headroom-percent 20 \
  --expect-strategy head --filter-status 204 \
  --streams 256 --max-connections 64 --max-workers 65536 \
  --server-snapshot-provider /opt/h3fuzz/server-snapshot-provider \
  --server-evidence-provider /opt/h3fuzz/server-evidence-provider \
  --output /data/h3fuzz/frozen-certification.txt \
  --manifest /data/h3fuzz/frozen-certification.manifest.json \
  --artifact-dir /data/h3fuzz/calibration
harness/scripts/certify.sh \
  --target https://SERVER_NAME:8443/bench/FUZZ \
  --wordlist /data/h3fuzz/frozen-certification.txt \
  --wordlist-manifest /data/h3fuzz/frozen-certification.manifest.json \
  --expect-strategy head --filter-status 204 \
  --fixed-sweep-manifest /data/h3fuzz/fixed-sweep/manifest.json \
  --files-validation-manifest /data/h3fuzz/files-validation/manifest.json \
  --diagnostics-manifest /data/h3fuzz/diagnostics/manifest.json \
  --target-rps 100000 \
  --server-snapshot-provider /opt/h3fuzz/server-snapshot-provider \
  --server-evidence-provider /opt/h3fuzz/server-evidence-provider \
  --artifact-store /data/h3fuzz/certifications
```

`network-matrix.sh` installs qdiscs and namespaces only for its exact profile ID,
records before and after state, and has an idempotent cleanup subcommand. Every
runner installs a trap before mutation. After any run, verify that no qdisc,
namespace, origin process, or compose project owned by its run ID remains. A
cleanup failure returns exit code 4 and makes the trial ineligible.

## Suggested task ownership

These workstreams can run in parallel only where the dependency column allows:

| Workstream | Tasks | Accountable role | Dependency |
| --- | --- | --- | --- |
| Core accounting | PERF-001 | Measurement owner | None |
| Origin correlation | PERF-002 | Harness owner | PERF-001 request-role schema |
| Environment evidence | PERF-003 | Harness owner | None |
| Certification tooling | PERF-004 | Measurement owner | PERF-001, PERF-002, PERF-003 |
| Profiles | PERF-005 | Performance owner | None; baseline comparison needs PERF-003 |
| QUIC diagnostics | PERF-006 | Dependency maintainer | Telemetry schema agreed with PERF-001 |
| Autocalibration fast exit | PERF-101 | Performance owner | B0 baseline |
| Engine concurrency | PERF-102 | Integration owner | PERF-001 schema and shared pool/advisor design |
| Connection recovery | PERF-103 | Integration owner | PERF-006, fault harness, shared PERF-102 state model |
| High-rate preset | PERF-104 | Integration owner | PERF-001, PERF-102, response eligibility rules |
| Request and matcher hot path | PERF-201, PERF-202, PERF-203, PERF-205 | Performance owner | B0 profiles, PERF-101 |
| Admission hot path | PERF-204 | Performance owner | B0 profiles, PERF-103 slot model |
| Network matrix | PERF-301 | Harness owner | PERF-002, PERF-003, PERF-006 |
| Adaptive validation | PERF-302 | Measurement owner | PERF-102, PERF-103, PERF-301 |
| Effective-limit reporting | PERF-303 | Measurement owner | Schemas from PERF-001, PERF-102, PERF-103 |
| Deep H3 prototype | PERF-401 through PERF-403 | Dependency maintainer | Phase 2 and Phase 3 exit gates |
| Certification | PERF-501 through PERF-504 | Release approver | All required exit gates |

One integration owner should control shared changes to `pkg/engine/job.go`,
`pkg/runner/simple.go`, `pkg/runner/http3_pool.go`, and telemetry types. Do not
allow parallel branches to invent incompatible worker, pool, and telemetry
state models.

## Required validation after each phase

```sh
gofmt -w <changed-go-files>
go test ./pkg/ffuf ./pkg/engine ./pkg/runner ./pkg/assembly ./pkg/webserver
go test -race ./pkg/engine ./pkg/runner ./pkg/ffuf ./pkg/webserver
go test ./...
go test -run '^$' -bench . -benchmem ./pkg/input ./pkg/engine ./pkg/runner ./pkg/filter
python3 -m unittest harness/scripts/benchmark_report_test.py
python3 -m unittest harness/scripts/certification_report_test.py
harness/scripts/validate.sh
go build -o ./h3fuzz .
docker compose -f harness/compose.yaml up --build --wait
harness/scripts/smoke.sh
harness/scripts/correctness.sh
pnpm --dir frontend test
pnpm --dir frontend lint
pnpm --dir frontend build
```

Run the frontend commands only when frontend or generated option-schema behavior
changes. Add a shell cleanup trap that always stops and removes the correctness
compose project, including on test failure. The certification script must use
the separate native host-network profile and perform its own verified cleanup;
the portable `compose.yaml` run above is correctness evidence only.

Run throughput certification only on the documented native-Linux profile. A
Docker Desktop result is useful for correctness and relative regression, not a
universal performance claim.

## Implementation-agent reporting format

For every PERF task, the implementing agent must report:

1. Root cause or hypothesis.
2. Files and interfaces changed.
3. Correctness tests added.
4. Race and failure tests added.
5. For performance-affecting code, before and after `ns/op`, `B/op`,
   `allocs/op`, and logical completed-response RPS. For evidence, documentation,
   and feasibility tasks, record `N/A` with the reason instead of inventing a
   measurement.
6. Where applicable, CPU, allocation, mutex, QUIC, UDP, client CPU, and server
   CPU observations.
7. Any regression or rejected approach.
8. Whether the task's acceptance gate passed.
9. Private raw artifact paths and hashes, plus the redacted publishable artifact
   path and hash when one is produced.

An RPS improvement without clean accounting, error evidence, and drain proof is
not an accepted result.
