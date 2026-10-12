# HTTP/3 implementation and 100k RPS analysis

[Performance documentation](README.md)

Audit date: 2026-10-11

This document compares h3fuzz with the implementation and techniques described
by Tom Stacey and PortSwigger in
[HTTP/3 in Burp Suite: it's time to find a bigger wordlist](https://portswigger.net/research/http3-in-burp-suite).
The source comparison used these revisions:

- [Turbo Intruder `7b9d1f9`](https://github.com/portswigger/turbo-intruder/tree/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47)
- [HTTP3-Adapter `f7ead65`](https://github.com/t0xodile-swig/HTTP3-Adapter/tree/f7ead6559af7565bd90acfa43b2abdd177a40a85)

The audited Turbo Intruder revision is dated 2026-10-07, 14 days after the
research article was published. Source-level constants and behavior in this
report describe that audited revision. They may not be identical to the code
used for the article's original measurement.

The conclusions are based on source review, h3fuzz unit and integration tests,
the Docker harness, fresh current-build runs, and the benchmark reports present
in the audit workspace.

Raw benchmark artifacts are retained privately and are not distributed in this
repository because they contain host and environment metadata. Paths under
`harness/.work` below identify their locations in the measurement workspace,
not files available in a public checkout. This report preserves summary
measurements, provenance hashes, acceptance decisions, and limitations, but a
reader cannot independently verify the private bundles from this repository
alone.

## Executive verdict

h3fuzz implements the main ideas needed to apply PortSwigger's research to
content discovery:

- direct HTTP/3 negotiation and connection reuse
- multiple independent QUIC connections
- concurrent request streams on every connection
- adaptive connection-count tuning
- an adaptive request-worker supervisor with explicit fixed-worker compatibility
- explicit, generation-aware connection recovery
- an acknowledged CLI-only high-rate policy with latency-derived capacity checks
- conservative HEAD and final-byte Range qualification
- runtime recovery to GET when an optimized response is not trustworthy
- post-drain transport accounting
- fail-closed fixed-sweep, resource, `/files`, diagnostic, and certification
  artifact producers

h3fuzz does not match Turbo Intruder at the transport-engine level. Turbo
Intruder has a custom HTTP/3 request path built on Kwik. It controls request
encoding, QPACK use, response buffering, connection lifetime, dispatch
capacity, and several synchronization details. h3fuzz uses quic-go's
`http3.Transport` through Go's `net/http` request model. It therefore shares
the research strategy, but not Turbo Intruder's optimized hot path.

h3fuzz can configure enough in-flight work to pursue 100,000 completed
responses per second on a sufficiently low-latency, sufficiently capable
target. A sealed local Darwin diagnostic now demonstrates that processing rate
on the controlled `/bench` workload: five adaptive runs had a median of
113,206.928 completed responses per second, five fixed-8 runs had a median of
113,980.998, and every run exceeded 100,000 for at least 30 seconds. This is
privately retained evidence of local capability. After integrating the accepted
prepared-header optimization, five more ordinary adaptive-default detected-H3
runs had a 114,578.588 RPS median and every run again exceeded 100,000. The
production cohort recorded exact prepared-header and origin accounting with no
fallbacks or errors. After PERF-202, a five-run current-source regression cohort
with exact run telemetry had a 127,008.228 RPS median and every run exceeded
100,000 for more than 40 seconds. That later result demonstrates that the
accepted build did not regress the local default path in the measured
environment; it is not a causal PERF-202 speedup.
None of these results is release certification: the client and origin ran on
Darwin, where the required native-Linux client/server UDP-drop evidence is
unavailable.

The fresh current-source native-Linux profile is materially lower because its
client and origin share `mothership`: CPU- and allocation-profile runs reached
60,205.028 and 60,331.897 RPS. Resource accounting shows that this 4-core,
8-thread layout has an ideal co-located ceiling of about 72,923 RPS at the
observed per-response client and origin CPU costs. A second same-region
native-Linux origin host, effective UDP receive and send buffers of at least
7,340,032 bytes on both hosts, and an authoritative current-source Git checkout
with a real `HEAD` are therefore prerequisites for a legitimate 100k Linux
attempt on this client, not optional benchmark polish.

These local numbers are not quantitatively comparable with PortSwigger's
headline figure. PortSwigger's article does not publish the raw artifact, exact
RPS denominator, post-drain behavior, error rate, full measurement interval,
latency, hardware, or server workload. The current h3fuzz harness measures
successful logical responses over the main-request span, independently checks
exactly-once terminal outcomes, and reconciles marked application attempts with
route-, protocol-, method-, and run-specific origin counts. The methodologies
remain materially different or undocumented.

The defensible conclusion is:

> The core PortSwigger techniques are implemented and validated for correctness,
> and the current build after PERF-202 sustained more than 100k RPS under the
> ordinary local adaptive default with exact accounting and telemetry. This is
> local regression evidence, not causal optimization evidence. Native-Linux
> UDP-drop certification on separated hosts remains pending, so the README
> claim remains gated.

## What PortSwigger's result depends on

PortSwigger reports more than 100,000 RPS from a laptop to a remote host over
Wi-Fi and about 180,000 RPS from a cloud host in the same region as the target.
The article's heading also describes one million requests in ten seconds, but
it does not publish enough measurement detail for direct reproduction. The
research identifies three practical requirements.

### Minimize the response

The first requirement is to minimize request and response bytes. PortSwigger
recommends `HEAD` or a final-byte range request:

```http
GET /candidate HTTP/1.1
Range: bytes=-1
```

This matters even with HTTP/3. A full body still consumes server work, network
bandwidth, buffers, decoding work, memory, and garbage collection. Turbo
Intruder also reads the response to completion, so a small response is a key
part of its measured result, not an optional enhancement. PortSwigger also
notes that its HTTP/3 engine can omit the textual `Host` header because the
`:authority` pseudo-header is implied by the target.

h3fuzz and Go also derive `:authority` from the target URL, but h3fuzz adds a
default User-Agent and users can configure much larger browser-like header
sets. Avoid the Akamai or browser header preset for a transport-ceiling run
unless those headers are required by the target. Request-byte minimization is
not otherwise specialized to the same extent as Turbo Intruder.

h3fuzz reproduces this technique conservatively. It compares three GET
baselines with three HEAD responses. If HEAD is not reliable, it tests a
one-byte Range response. If neither is reliable, it retains GET. The relevant
implementation is in `pkg/runner/response_optimization.go`. Runtime validation
and GET recovery are in `pkg/runner/simple.go`.

### Keep many streams active across several connections

The audited Turbo Intruder revision defaults to 10 HTTP/3 connections and a
local admission target of 256 concurrent streams per connection. Its source
comments record a separate, environment-unspecified one-million-request test
taking 14 seconds at 100 streams, 13 seconds at 256 streams, and degrading at
512 streams because of packet loss and retransmission. This is not published
evidence from the article's Wi-Fi run. The default of 256 is a reasonable
source-measured starting point, not a portable optimum or a guarantee that the
peer makes 256 bidirectional streams immediately available.

h3fuzz retains the 256-stream local-admission default, but deliberately seeds
automatic mode with 8 connections rather than Turbo Intruder's 10. Local
diagnostics on both bare-machine macOS and Linux/Docker favored 8 over the prior
10-connection policy; neither environment substitutes for the native-Linux
certification cohort. The adaptive controller can then double
8 to 16 to 32 while throughput improves. Each pool slot owns a separate
transport and explicit QUIC connection generation. Atomic per-slot admission
prevents a connection from exceeding its configured stream budget. The peer's
advertised QUIC stream limit can still block underlying request streams. See
`pkg/runner/http3_pool.go` and `pkg/runner/owned_http3_transport.go`.

### Tune against completed responses and failures

In the audited revision, PortSwigger's AUTO engine probes H3, H2, and H1 in
parallel, prefers the highest available version, caches capability for 60
seconds, and adjusts concurrency during a run. Its HTTP/3 controller samples
every 250 ms, measures one-second windows, requires the engine to be saturated
for most of a window, doubles concurrency while throughput improves, refines
between the best and first non-improving value, backs off after failures, and
periodically probes above a stable result.

h3fuzz substantially reproduces this policy:

- 250 ms sampling
- one-second measurement windows
- 75 percent saturation requirement
- doubling while throughput improves
- 3 percent minimum improvement
- binary refinement after a non-improving result
- failure and zero-progress backoff
- a 10 percent probe after 30 seconds in steady state

These constants and the controller are in `pkg/runner/http3_pool.go`.

h3fuzz's protocol selection is not identical. It performs a direct H3
fingerprint during setup and uses its TCP transport if setup fails in automatic
mode. Once H3 is selected, a same-origin H3 request error does not cause a
mid-run protocol downgrade. The `originRoutingTransport` uses TCP for a
different redirect origin, not as transparent recovery for the selected
origin. HEAD or Range recovery to a full GET is a separate request-strategy
fallback and remains on the selected transport.

## Architecture comparison

| Area | Turbo Intruder | h3fuzz | Assessment |
| --- | --- | --- | --- |
| Protocol proof | Direct HTTP/3 engine | Direct QUIC handshake, ALPN `h3`, SETTINGS, reuse, and 16-stream burst | Same intent, with a bounded functional test in h3fuzz |
| Automatic protocol selection | Parallel H3, H2, and H1 probes with preference and caching | One direct H3 setup probe, then TCP fallback before the run | Similar outcome for ordinary setup, different algorithm and no same-origin mid-run downgrade |
| Connections | Multiple independent connections | One explicitly owned current QUIC generation per pool slot; a draining retired generation can overlap its replacement | Strong match with explicit lifecycle ownership |
| Streams | 256 per connection by default | 256 per connection by default | Match |
| Initial adaptive pool | 10 connections | 8 connections, then 8 to 16 to 32 growth while measurements justify it | Deliberate evidence-based deviation |
| Adaptive sampling | 250 ms samples and one-second windows | 250 ms samples and one-second windows | Match |
| Tuning policy | Saturation, growth, refinement, failure backoff, periodic reprobe | Same main policy with h3fuzz-specific zero-progress backoff | Strong conceptual match |
| Response minimization | User selects HEAD or Range | HEAD or Range is qualified automatically, with GET fallback | h3fuzz adds safety checks suited to discovery |
| Request dispatch | Virtual threads up to a 65,536 in-flight ceiling on supported JDKs | Supervised Go workers with an automatic cap of 65,536 and an optional lower `-http3-max-workers` bound | Similar ceiling, different runtime model |
| Worker growth | Dispatch capacity is resized with adaptive capacity | A sole-owner supervisor grows workers after acknowledged pool-capacity proposals; explicit `-t` remains fixed | Strong conceptual match with explicit compatibility semantics |
| Connection failure | Failed connections are explicitly retired and replenished | Exact per-slot generations are retired by identity and replenished through a bounded shared reconnect gate | Same recovery intent; h3fuzz now has retained fault-injection and full-drain proof |
| HTTP/3 stack | Custom Kwik-based request engine | quic-go `http3.Transport` and `net/http` | Material difference |
| Request encoding | Contiguous encoded request buffer with one stream write and flush | Standard Go request construction and transport encoding | Turbo Intruder has the leaner controlled hot path |
| QPACK | Explicit repeated authority reuse and decoder-acknowledgement handling | The pinned qpack dependency uses static and literal fields and no dynamic table | Turbo's dynamic authority reuse and acknowledgement path is absent |
| Response reads | Explicit buffering in the custom engine | Standard Go response-body path | Not equivalent |
| Connection lifetime | Default one million requests per connection | Persistent quic-go connection with no matching request-count control | Similar goal, different control |
| Race gates | Single Datagram Attack and QPACK blocked streams | Not implemented | Out of scope for ordinary content discovery throughput |
| Kettled requests | Implemented | Not implemented | Out of scope |

Turbo Intruder source references include its
[default HTTP/3 settings](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/resources/ScriptEnvironment.py#L377),
[stream default and measurements](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/HTTP3RequestEngine.kt#L1958),
[virtual-thread selection](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/HTTP3RequestEngine.kt#L230),
[in-flight limits](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/HTTP3RequestEngine.kt#L1974),
[failed-connection retirement measurements](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/HTTP3RequestEngine.kt#L1158),
[adaptive controller](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/AdaptiveController.kt#L37),
and [adaptive policy](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/AdaptivePolicy.kt#L35).
Its lower-level differences are visible in the
[custom request encoder](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/hp3/h3/Http3ClientConnection.java#L429),
[QPACK authority handling](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/hp3/h3/Http3ClientConnection.java#L248),
[decoder acknowledgement path](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/hp3/h3/Http3ClientConnection.java#L364),
and [buffered response path](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/H3Connection.kt#L369).

The pinned `github.com/quic-go/qpack` v0.6.0 encoder emits a zero dynamic-table
prefix and encodes static or literal fields. Dynamic authority reuse is
therefore absent from h3fuzz's current dependency stack, not merely hidden
behind quic-go. It is a candidate optimization whose effect must be measured,
not a prerequisite established by the available evidence.

The corresponding h3fuzz implementation map is:

| Concern | Local source |
| --- | --- |
| Pool defaults and controller constants | `pkg/runner/http3_pool.go:20-27` |
| Independent transports and atomic stream admission | `pkg/runner/http3_pool.go`, `pkg/runner/owned_http3_transport.go` |
| Adaptive controller | `pkg/runner/http3_pool.go` |
| Initial and adaptive worker sizing | `pkg/runner/http3_pool.go`, `pkg/runner/simple.go` |
| Capacity proposal/acknowledgement and worker supervisor | `pkg/ffuf/interfaces.go`, `pkg/engine/job.go` |
| Exact-generation retirement and reconnect gating | `pkg/runner/owned_http3_transport.go`, `pkg/runner/http3_reconnect_gate.go` |
| Direct fingerprint and 16-stream burst | `pkg/runner/http3_fingerprint.go:26-159` |
| HEAD and Range qualification | `pkg/runner/response_optimization.go:269-359` |
| Optimization eligibility | `pkg/runner/response_optimization.go:377-476` |
| Runtime validation and GET recovery | `pkg/runner/simple.go:740-799`, `890-965` |
| Per-response calibration call and mutex | `pkg/engine/job.go:817-818`, `pkg/engine/autocalibration.go:157-161` |
| Worker-option provenance and persistence | `pkg/assembly/build.go`, `pkg/webserver/manager.go`, `pkg/webserver/profiles.go` |

## HTTP3-Adapter's role

HTTP3-Adapter is a Burp compatibility layer, not the engine behind the 100k
result. It converts Burp requests to HTTP/3, remembers origin support, opens one
stream for each request, and materializes the response. When connection reuse
is enabled it pools one connection per origin; reuse can also be disabled. It
does not implement Turbo Intruder's multi-connection performance controller.

Its fallback is deliberately narrow. An unrequested, speculative H3 attempt
for an unknown origin can fall back when initial H3 establishment fails.
Explicit `X-Http3: 1` requests, origins already believed to support H3, and
per-exchange failures fail visibly rather than silently downgrading. The
adapter should therefore be treated as a protocol-conversion and compatibility
reference, not the performance baseline for h3fuzz. Relevant code includes its
[mode decision](https://github.com/t0xodile-swig/HTTP3-Adapter/blob/f7ead6559af7565bd90acfa43b2abdd177a40a85/src/main/java/hp3/adapter/Hp3ModeDecider.java#L24),
[HTTP handler](https://github.com/t0xodile-swig/HTTP3-Adapter/blob/f7ead6559af7565bd90acfa43b2abdd177a40a85/src/main/java/hp3/adapter/Hp3HttpHandler.java#L98),
[connection pool](https://github.com/t0xodile-swig/HTTP3-Adapter/blob/f7ead6559af7565bd90acfa43b2abdd177a40a85/src/main/java/hp3/adapter/Hp3ConnectionPool.java#L12),
[failure outcome](https://github.com/t0xodile-swig/HTTP3-Adapter/blob/f7ead6559af7565bd90acfa43b2abdd177a40a85/src/main/java/hp3/adapter/Hp3FailureOutcome.java#L23),
and [per-request exchange](https://github.com/t0xodile-swig/HTTP3-Adapter/blob/f7ead6559af7565bd90acfa43b2abdd177a40a85/src/main/java/hp3/adapter/Hp3Exchange.java#L27).

## Adversarial findings and resolutions in h3fuzz

### Adaptive worker capacity now follows connection capacity

The audit baseline exposed a real lifecycle defect: automatic mode attempted 10
initial connections, derived one worker count during setup, and could later add
connections without adding application workers. PERF-102 is complete and
removes that static ceiling. Automatic mode now seeds up to 8 connections and
256 streams, publishes capacity changes, and uses one engine supervisor to
acknowledge and grow live workers. The default growth sequence is therefore
8 to 16 to 32 connections, with corresponding nominal capacities of 2,048,
4,096, and 8,192 workers before remaining-input and configured caps.

The distinction between automatic and explicit worker policy is preserved.
Omitted `-t` enables supervised sizing, bounded by remaining inputs, the 65,536
safety cap, and `-http3-max-workers`. An explicit `-t` remains a fixed legacy
worker count and cap. Capacity proposals and acknowledgements carry execution
generation and epoch identity, and shutdown joins the sole owner before waiting
for workers, so resize cannot race `WaitGroup.Add` against `Wait`.

Little's Law still describes the capacity required for a target rate:

```text
required workers approximately equal target RPS multiplied by latency in seconds
```

At 100k RPS, the 2,048-worker initial seed is sufficient only when average
end-to-end response latency is about 20.48 ms or less. It is a seed, not a
permanent ceiling: a justified move to 16 connections supplies 4,096 nominal
slots and a move to 32 supplies 8,192. CLI, config, web, history, and import
paths preserve whether `-t` and `-http3-max-workers` were explicitly supplied.

h3fuzz defaults the adaptive ceiling to 64 connections and hard-limits the
connection flags to 256. Turbo Intruder's current policy does not have the same
connection-flag bounds, although its source caps in-flight dispatch at 4,096
platform threads or 65,536 virtual threads. h3fuzz's 8-connection starting seed
is an intentional measured deviation from Turbo Intruder's 10; its dynamic
worker lifecycle now follows the same principle while retaining Go-specific
bounds and explicit-worker compatibility.

### Disabled autocalibration bypasses parsing and the global mutex

The audit baseline called `CalibrateIfNeeded` after every successful request,
parsed the target URL, and acquired `calibMutex` before discovering that
autocalibration was disabled. PERF-101 moved the immutable enabled check ahead
of both operations while retaining enabled per-host serialization. The disabled
response path therefore no longer introduces that global lock or parse cost.

### The standard Go request path does more work per request

h3fuzz constructs and processes ordinary `net/http` requests, attaches tracing,
copies maps, performs ffuf matching and filtering, and creates result data.
Turbo Intruder's custom path deliberately reduces selected allocations and
synchronized operations with a composed request buffer, one write and flush,
buffered response reads, and targeted locking. It does not eliminate all
allocation or synchronization. Configuring more concurrency cannot recover
throughput once one CPU-intensive stage is saturated.

### Fresh profiles keep request/client and task/input work in scope

The retained current-source fixed-4 native-Linux CPU profile attributes 62.82%
cumulative CPU to `SimpleRunner.execute`, 11.52% to `runtime.mallocgc`, and
7.63% flat CPU to Linux syscalls. Its paired allocation profile recorded
23.40 GiB, including 1.45 GiB flat in `Job.runTask`, 1.14 GiB in
`SimpleRunner.execute`, 1.12 GiB in HTTP/3 header parsing, 1.03 GiB in
`http.Header.Clone`, 0.86 GiB in single-wordlist input-map materialization,
0.85 GiB in `http.NewRequestWithContext`, and 0.69 GiB in
`Request.WithContext`.

That evidence justifies narrow experiments around request preparation and the
single-wordlist task representation. It does not justify assuming that one
library frame explains the entire gap, or replacing the HTTP/3 lifecycle before
cheaper candidates have been measured.

### Matcher and residual-pipeline audits rejected speculative work

PERF-203 ended `hypothesis-disproved/skipped`, with no production matcher fast
path. The retained clean native-Linux profile attributes 0.93 of 143.26 sampled
CPU-seconds to `MatcherManager.Matches`, or 0.649169% cumulative CPU; even the
conservative post-A42 projection is only 0.705353%, far below the 5% merge gate.
Test-only differential coverage freezes status, logical-size, global,
per-domain, `and`/`or`, empty, and unsupported-provider behavior. Its sealed
evidence bundle is
`harness/.work/perf203-matcher-audit-20261011T095739Z`, whose `SHA256SUMS`
hashes to
`4e487a8dc6f411b529a93226e59f8993f2d105ecc8e35880265c8937363735ec`.

PERF-205 passed its audit without accepting another product change. Of seven
ranked candidates, request/response construction was highest at 4.251% CPU and
9.273% allocation, but that work remains owned by the separately gated
PERF-201/PERF-202 paths. Direct diagnostics/counters, empty scraper execution,
and the CLI pause checkpoint were all below 0.15% CPU; match-only and error-only
branches were correctly reported as unentered by the filtered clean workload.
No mutex or block profile was retained, so the Phase 2 contention gate remains
open. The verified PERF-205 bundle is
`harness/.work/perf205-response-pipeline-audit-20261011T095545Z`, with manifest
SHA-256
`578c1fe1d5ccc4b0f40b6071289282e9dfef190897d8b62cf5fa90c1bc23f002`.

### Trace-free timing is restricted to a telemetry-off optimized path

PERF-202 passed on allocation evidence. Generic requests, requests rejected
from automatic optimization, both transport-error and response-validation
fallback GET paths, and every telemetry-on request retain exact final-attempt
`WroteRequest`-to-`GotFirstResponseByte` timing. Only telemetry-off requests
that remain eligible for automatic HEAD or Range omit `httptrace`; their
duration spans immediately before `Client.Do` to available response headers.
Time matchers and filters reject automatic optimization, so they cannot
silently observe the alternate interval.

The exact helper now tracks pending and latest-completed redirect hops under a
mutex and freezes atomically after `Client.Do`; fallbacks receive fresh timing
state. Two independent final reviews returned GO. Serialized current-source
benchmarks measured the exact primitive at 288 B/op and 5 allocs/op and the
trace-free primitive at 0 B/op and 0 allocs/op. Full optimized HEAD measured
2,132–2,134 B/op and 20 allocs/op against a sealed historical 26-allocation
baseline. Those paired exact-timing 5-to-0 and HEAD 26-to-20 comparisons are the
binding PERF-202 acceptance evidence. Current Range measured 2,309–2,310 B/op
and 27 allocs/op; its contemporaneous 33-allocation pre-change observation was
not retained as raw output and is non-binding historical context only. The
later telemetry-on A45 cohort exercises the exact path and is regression
evidence only.

The runtime SHA-256 is
`0e9255439c7320b2219f8115429bab6204e7b00291a6771ea9555d8886950b04`.
The optimized-execute, marker, and response-optimization test hashes are
`495b1defbe207dda749c7f2d8740dc5af676930676946d758e38e619df2d532f`,
`cda49ea6ecd9bf4d70b37c2d885ecf8030d0c8ede35267437d5d0cf6ffe88592`,
and `62737c242ace23f5b63995c68eb18f49719a5909d2d8e92ae2c475e5cf9e97fe`.

The nine raw evidence entries are sealed at
`harness/.work/perf202-trace-timing-audit-20261011T105656Z`; the manifest
verifies and hashes to
`3257f1ae5deb7686bab98e601969ef8127de5c82506de1be4e065a68d4946eb2`.
Focused and repeated race tests pass. Its byte-for-byte historical HEAD raw
copy hashes to
`b03e75bbe6dbef23438c32cb57a71d74f826a2b06171a03aa5e14bc4f5e389c9`
and comes from the verified PERF-201 context bundle with manifest SHA-256
`16df1fd81762ebb67758ca8fe0e8ebefddf0da5245399315bb91ea24c2b34ebd`.
Only the historical allocation count is compared because CPU settings and the
source checkpoint differ.

### A direct RoundTripper call did not clear its acceptance gate

A restricted treatment bypassed `http.Client.Do` only for the owned selected-H3
path and invoked its RoundTripper directly. Three clean alternating pairs raised
the arm median from 60,425.995 to 61,661.386 RPS, a 2.0445% gain. Pair gains
were 1.8830%, 2.1048%, and 2.4761% (median 2.1048%); median user CPU fell
4.0048% and maximum RSS fell 15.4917%.

The predeclared end-to-end gate was at least 3% RPS, so the bypass is rejected
despite its resource reductions. This was narrower than a custom direct-H3
engine. Its product-source change was reverted to the accepted baseline binary,
SHA-256
`b7d75777eb365d6d9f64d42fa292ff846693fc732e30937f7ea4f4772f6a8692`.
A lower-level owned-connection/QPACK path remains conditional on future
separated-host profiles, not the automatic next step.

### Regular-file wordlists no longer retain one object per line

The current regular-file provider stores bytes in one contiguous arena with
pointer-free end offsets, using `uint32` until a larger wordlist requires
`uint64`. Stdin remains streaming, and line, comment, extension, reset, encoder,
and multi-provider semantics are characterized separately. In a retained
current-source native-Linux fixed-4 A/B, arena storage raised median-of-arm RPS
by 3.9451%, reduced median user time by 9.1902%, and reduced median maximum RSS
by 42.3891%. This accepts the storage optimization at that operating point; it
does not imply that the rest of the standard Go request path is optimized.

### Connection generations are explicitly owned and fault-tested

Turbo Intruder identifies failed connections, retires them, and replenishes the
pool. The audit identified a concrete mismatch in quic-go v0.63.0's cached
transport ownership: hostname-keyed removal lets a late error from generation A
evict healthy replacement B, and removed generations can escape the normal
`Transport.Close` map. The stock fault fixture reproduced that A/B/C sequence.

PERF-103 is complete. Each h3fuzz pool slot now owns its current QUIC generation
explicitly, retires only that exact generation with compare-and-swap identity,
and accounts for the generation until its response body finishes. Caller
cancellation does not retire a healthy generation, while the pinned quic-go
rules for bodyless, replayable, and non-replayable retries remain intact. A
shared reconnect gate limits concurrent replacement dials to
`min(4, ceil(desired_connections / 4))` and applies jittered 250 ms to 10 second
backoff. Retired generations close after drain or a bounded timeout, and runner
shutdown closes every tracked generation.

The connection-aware fault harness proved recovery to at least 90 percent of
the pre-fault rate with zero surfaced errors in the 30-second observation,
exactly one replacement, and full generation drain. This is retained recovery
evidence, not a claim that failures are free or that every network behaves the
same way.

### Connection selection has a linear worst case

Per-request connection selection begins at a rotating slot and scans the
active slots until it reserves stream capacity. Aggregate admission makes a
full scan uncommon, but contention can turn this into work proportional to the
number of active connections. Very large pools should be justified by measured
gain rather than selected preemptively.

### The QUIC configuration is mostly library-default behavior

h3fuzz sets handshake and idle timeouts, keepalive, and datagram support. It
does not directly configure receive windows or congestion behavior. quic-go
v0.63.0 attempts to raise UDP receive and send buffers to 7,340,032 bytes
(7 MiB) on non-OpenBSD
systems, but kernel maxima can prevent that, and ordinary h3fuzz CLI mode
discards the standard log unless debug logging is enabled. Actual live socket
sizes and a retained debug log therefore remain required evidence. PortSwigger's
custom stack also explicitly attempts to
raise UDP send and receive buffers in
[`QuicSockets.java`](https://github.com/portswigger/turbo-intruder/blob/7b9d1f9db1a268aed8d62a3415e9fd52c1cecf47/src/hp3/quic/QuicSockets.java#L11).

### A successful fingerprint is not a capacity measurement

The fingerprint proves ALPN `h3`, receipt of HTTP/3 SETTINGS, one reused
connection, and a 16-stream burst. It does not measure the peer's sustained
stream capacity, prove that 256 peer streams will be available, measure loss
response, identify the optimal connection count, or measure RPS. It also
connects to the port in `-u` and does not follow Alt-Svc to a different port.

### Body-dependent features intentionally disable HEAD and Range

Automatic response minimization is disabled by features that require a full
body or can change request semantics. Important examples include:

- word, line, regular-expression, or response-time matchers and filters,
  including `-fw`, `-fl`, `-mr`, `-fr`, and `-ft`
- autocalibration
- active body scrapers
- any configured preflight request
- `-o`, `-od`, or audit logging
- raw URL mode
- non-GET methods, request bodies, fuzzed headers, and a user-supplied Range
  header

Status and logical-size matchers and filters are compatible with optimized
responses. `-run-telemetry` is also compatible. The selected strategy and
reason should be checked before treating a run as a high-rate run.

### HEAD and Range evidence is bounded

The three-sample qualifier reduces false-negative risk; it cannot prove that
HEAD or Range is semantically equivalent to GET for every application route.
Runtime checks reject new statuses, malformed metadata, bodies that violate
the selected contract, and several other changes. They cannot detect a route
whose HEAD response retains a trusted status and plausible `Content-Length`
while its GET behavior differs semantically. High-consequence scans should
compare a representative GET sample or disable optimization.

### Race features are not throughput prerequisites

Turbo Intruder's Single Datagram Attack and QPACK blocked-stream gates improve
the synchronization of race-condition requests. Ordinary high-throughput
content discovery does not depend on DATAGRAM-based race delivery,
intentionally held blocked-stream race gates, kettled requests, or 0-RTT.
h3fuzz currently enables datagram negotiation in its pool and fingerprint but
does not use datagrams for ordinary discovery requests. The scan-pool setting
should be disabled unless an interoperability or measured performance test
justifies it; the fingerprint may continue to report peer support.

### Short inputs prevent adaptive convergence

The checked-in `final.txt` contains 233,582 inputs. At 100k RPS it lasts only
2.34 seconds before extension expansion. That is not long enough to evaluate a
controller with one-second windows and a 30-second steady-state reprobe. Use a
larger real wordlist, expansion, or the harness's unique repeated rounds for
performance measurement.

## Configuration for pursuing 100k RPS

Only scan systems you own or are explicitly authorized to test. These settings
can cause substantial load even when responses are bodyless.

### 1. Prove HTTP/3 function first

```sh
./h3fuzz \
  -u https://target.example/FUZZ \
  -http3-fingerprint
```

Require `supported: true`, `high_rate_ready: true`, `connection_reused: true`,
and 16 of 16 completed concurrent streams. Treat that as a prerequisite, not a
throughput prediction.

### 2. Keep the high-rate path eligible

Use a bodyless GET discovery target and leave automatic response optimization
enabled. Confirm that preflight selected HEAD or RANGE. Use status or size
filters for known noise. Avoid body-dependent filters and features during the
ceiling measurement.

Do not use `-ignore-body` as a substitute for automatic qualification. It does
not prove that HEAD or Range preserves the discovery signal, and it can remove
information if a request has to fall back to GET.

For a controlled ceiling or eligibility run, use the explicit high-rate policy:

```sh
./h3fuzz \
  -u https://target.example/FUZZ \
  -w /path/to/long-wordlist.txt \
  -http3-high-rate \
  -http3-high-rate-acknowledge \
  -http3-target-rps 100000 \
  -http3-max-connections 64 \
  -http3-streams 256 \
  -http3-max-workers 16384 \
  -mc all -fc 404 \
  -run-telemetry run.json
```

This CLI-only preset forces direct HTTP/3 and the qualified bodyless path,
rejects incompatible features, and fails before load if the declared bounds
cannot supply the target. It derives the worker requirement from the larger
successful preflight mean unless `-http3-latency-ms` supplies an explicit
controlled value. It does not change ordinary CLI, explicit `-t`, or web/API
defaults.

### 3. Size concurrency from latency

Use this starting estimate, then add measured headroom:

```text
workers = target RPS * average response latency in seconds
connections = ceiling(workers / streams per connection)
```

At 256 streams per connection:

| Average latency | Workers needed for 100k | Mathematical minimum connections |
| ---: | ---: | ---: |
| 10 ms | 1,000 | 4 |
| 25 ms | 2,500 | 10 |
| 50 ms | 5,000 | 20 |
| 100 ms | 10,000 | 40 |

These are lower bounds. CPU cost, scheduling, loss, server limits, and uneven
stream completion require headroom.

### 4. Run adaptive mode long enough

For an ordinary adaptive discovery run outside the high-rate eligibility
policy, a practical command is:

```sh
./h3fuzz \
  -u https://target.example/FUZZ \
  -w /path/to/long-wordlist.txt \
  -e aspx,htm,html,js \
  -http3 \
  -http3-connections 0 \
  -http3-max-connections 64 \
  -http3-streams 256 \
  -http3-max-workers 16384 \
  -rate 0 \
  -mc all -fc 404 \
  -noninteractive -s \
  -run-telemetry run.json
```

`-http3` is intentional here. It turns an HTTP/3 downgrade into a failed run
instead of silently benchmarking another protocol. Omitting `-t` enables the
adaptive worker supervisor; `-http3-max-workers 16384` bounds it at the full
64 by 256 admission budget. It is a ceiling, not a universal recommendation.
Reduce it when latency requires less concurrency or when CPU, memory, or the
target cannot sustain it.

This command keeps non-404 discoveries. If the target returns many matching
responses, formatting and storing those results can dominate the measurement.
For a transport-ceiling run, use a controlled route and filter its known
baseline response. The harness uses `-fc 204` for `/bench/FUZZ` and redirects
the tool's output to an artifact file. Keep a separate `/files/FUZZ` or real
target run for discovery realism.

For the checked-in DirSearch-format list, keep `-D` and specify extensions:

```sh
./h3fuzz \
  -u https://target.example/FUZZ \
  -w final.txt \
  -D -e aspx,htm,html,js \
  -http3 \
  -http3-connections 0 \
  -http3-max-connections 64 \
  -http3-streams 256 \
  -http3-max-workers 16384 \
  -rate 0 \
  -mc all -fc 404 \
  -noninteractive -s \
  -run-telemetry run.json
```

### 5. Sweep fixed connection counts

Adaptive tuning is useful for long scans, but a controlled sweep reveals the
best stable point and detects a broken tuner assumption. Test fixed connection
counts such as 4, 8, 10, 16, 20, 32, 40, and 64. Keep 256 streams initially.
For example:

```sh
./h3fuzz \
  -u https://target.example/FUZZ \
  -w /path/to/long-wordlist.txt \
  -http3 \
  -http3-connections 20 \
  -http3-streams 256 \
  -rate 0 \
  -mc all -fc 404 \
  -noninteractive -s \
  -run-telemetry fixed-20.json
```

When `-t` is omitted in fixed mode, h3fuzz sizes workers from connections times
streams, capped at 65,536 and by total input count. Increase the connection
count until completed RPS plateaus or failures, retries, packet loss, or CPU
time increase. Return to the last clean setting. Do not assume that 64
connections or 512 streams is faster.

For certification, use `harness/scripts/fixed-sweep.sh` instead of selecting a
single manual observation. Its frozen B0 policy requires exactly three trials
per point, genuine client/origin CPU and network evidence, and the client's
final-attempt TTFB p99. It selects from the inclusive near-best set with a
deterministic concurrency tie-break and hashes every input. Missing or invalid
resource provenance makes a point ineligible; QUIC smoothed RTT is not a
response-latency substitute.

### 6. Control the environment and measurement

- Prefer a same-region host when measuring a remote target.
- Use native Linux and host networking for certification.
- Raise UDP receive and send buffer limits when quic-go reports that they are
  too small.
- Separate client and server CPU allocation for a local test.
- Keep `-rate 0` and do not configure request delay.
- Do not enable autocalibration, body scrapers, pre/postflight requests, replay,
  recursion, `-o`, `-od`, audit logging, or large per-result output during a
  transport-ceiling measurement. `-run-telemetry` remains compatible.
- Use CLI mode for the ceiling test so browser rendering and database writes are
  not part of the transport result.
- Measure completed responses after all workers and streams drain. Do not use
  queued inputs or a one-second UI sample as the result.
- Record latency, CPU, memory, UDP drops, retransmission symptoms, retries,
  errors, exact flags, versions, and the wordlist hash.

## Harness evidence

The harness tests more than raw speed. Its protocol matrix contains a
normal H1/H2/H3 origin, a TCP-only endpoint, an unpublished-UDP failure case,
an H3-only endpoint, and routes that deliberately lie about or reject HEAD and
Range behavior.

### Retained local Darwin performance cohort

The final local cohort alternated five ordinary adaptive-default runs with five
fixed-8 runs, using a fresh instrumented origin for every run. Its retained
aggregate is
`harness/.work/final-local-cohort-20261011T042635Z/aggregate.json`.

| Mode | Runs | Median completed-response RPS | All runs above 100k | All main spans at least 30 s |
| --- | ---: | ---: | --- | --- |
| Adaptive default | 5 | 113,206.928 | Yes | Yes |
| Fixed 8 | 5 | 113,980.998 | Yes | Yes |

The adaptive-to-fixed median ratio was 99.320878%. Across all ten runs, the
cohort recorded 37,373,120 exact successful logical outcomes. Each run exactly
reconciled marked H3 HEAD application attempts with the `/bench`
route and origin counters, recorded zero terminal, application, surfaced
transport, or QUIC errors, zero application or internal retries, zero semantic
fallbacks, and a complete worker, limiter, transport, and generation drain.
The cohort `SHA256SUMS` file hashes to
`3ee08416787f72363c569116a8beb3ce280417d22865500c88fb46038bc582f2`.

This cohort is explicitly `diagnostic/non-certification`. Both client and
origin ran on Darwin, not native Linux, and the required native-Linux
client/server UDP-drop evidence cannot be collected from this environment. It
passes the local numerical, duration, accounting, reconciliation, and drain
gates but cannot authorize the README claim.

### Retained A42 production-integration local cohort

The final production binary, built after integrating the accepted
prepared-header seam, ran five ordinary adaptive-default detected-H3 trials on
the same Apple M2 Max. Its retained aggregate is
`harness/.work/a42-final-local-cohort-20261011T092449Z/aggregate.json`.

| Runs | Median completed-response RPS | Minimum | Maximum | All runs above 100k and at least 30 s |
| ---: | ---: | ---: | ---: | --- |
| 5 | 114,578.588 | 107,810.617 | 115,762.320 | Yes |

The runs completed 18,686,560 successful logical responses in total. Runtime
telemetry recorded exactly 18,686,560 prepared-header attempts and hits, zero
fallbacks in every reason category, zero logical, transport, QUIC, or retry
errors, exact origin-handler reconciliation, and complete worker, limiter,
transport, and generation drain. The final Darwin binary hashes to
`212a1f189cbb082d567c909b7f7619bf7696e62dad6908ad1a89c4ee98c5d10a`.
The bundle's 77-entry `SHA256SUMS` verifies cleanly and hashes to
`81d4c37df3ef65f356c430f324b76794294cb38062c9d1625f5e3518dda14a63`.

This is direct evidence for the integrated product and its ordinary local
default, not merely the isolated A41 prototype. It remains
`diagnostic/non-certification` because both endpoints ran on Darwin and cannot
supply the required native-Linux UDP evidence.

The dependency and release boundary has since been hardened without changing
the A42 patch. Metadata schema v2 pins stock qpack v0.6.0 by module/version,
module sum, and `go.mod` sum; the verifier proves read-only module selection,
rejects replacements, and passes both online and strict-offline reconstruction.
Wrong-qpack and replacement fixtures fail closed. The verifier, metadata, and
unchanged patch SHA-256 values are respectively
`fb667f1afb21a1b6c0a188aced38a4c5d9bc10ab782e3ddee7ecea89947c0425`,
`437473d35ca51ece2887a50fd3b29bd75b6d496629db843528e256b1c0650b54`,
and `72fc88948abb88f32b866fc10d8ee6680832b86827ec0c60db6134945d60292f`.

GoReleaser archives now carry the notice, A42 patch, and upstream metadata. A
v2.18.3 fixture snapshot produced 16 archives and verified exactly one matching
copy of every provenance input in each archive; `.goreleaser.yml` hashes to
`41ba29df826cf1cf6c8ac70cc4bf78f70ed633372a0e0bba2c51c36088d55a81`.
This is an archive-contract proof, not a claim that every release target builds:
the real all-platform snapshot retains a pre-existing FreeBSD/arm
`modernc.org/libc` cross-compilation failure. The completion re-audit passed the
full nested quic-go tests plus configured nested race/vet checks, root
full/race/vet checks, and the complete harness validation.

### Retained A45 PERF-202 current-source local cohort

The accepted PERF-202 build ran five ordinary adaptive-default detected-H3
trials with run telemetry enabled. Telemetry-on deliberately retains exact
final-attempt `WroteRequest`-to-`GotFirstResponseByte` tracing, so this cohort
tests the current integrated product and homogeneous
`client_final_attempt_time_to_first_byte` histogram contract rather than the
new trace-free branch.

| Run | Main-request span (s) | Completed-response RPS |
| ---: | ---: | ---: |
| 1 | 40.288915583 | 127,548.828893 |
| 2 | 40.326503958 | 127,429.940501 |
| 3 | 40.500973625 | 126,880.999148 |
| 4 | 40.460402292 | 127,008.228018 |
| 5 | 40.508570083 | 126,857.205512 |

All five were valid. The median was 127,008.228 RPS, with a range of
126,857.206–127,548.829. Total successes, latency samples, prepared-header
attempts and hits, and origin-handler requests were all exactly 25,694,020.
Latency overflow, prepared fallback, logical error, transport error, QUIC
error, application retry, and internal retry counts were zero; all logical
ledgers and worker, limiter, transport, and generation drains were clean.

The privately retained cohort used the local path
`harness/.work/a45-final-local-cohort-20261011T104118Z`. Its client and frozen
origin hashes are
`d30540f97ba46089f98e83d07a9be28c99cdc868099bd8b1b99b4f7d9b68aab7`
and `a903297d97153d6bbe874f91e430868f656286b4789a4e15f8dab4e766561f81`.
The 5,138,804-line repeated wordlist hashes to
`8bae81e001b8b919587f87e557c3c814cbc4d8c09df25b8de993528f745ab0fd`.
The verified 77-entry `SHA256SUMS` hashes to
`6757c5a580ec02d10a2639ca0ece3399675b0e466b88a73d192e9cd29ed26bd1`.

Its median is descriptively 10.8481% above A42, but the longer repeated input,
cohort timing, and lack of an alternating A/B make that comparison non-causal.
PERF-202 is accepted because the telemetry-off eligible-path benchmark removes
six allocations, not because of this RPS difference. This Darwin loopback
cohort remains `diagnostic/non-certification` and cannot authorize the README
claim.

The preliminary directory
`harness/.work/a45-final-local-cohort-20261011T103912Z` retains one otherwise
clean 125,931.412 RPS run whose main span was 29.677361 seconds. It was rejected
solely for missing the 30-second gate; none of it enters the accepted aggregate,
and the fresh longer-input cohort above supersedes it.

### Retained native-Linux wordlist arena A/B

The current-source storage ablation is retained at
`harness/.work/remote-arena-isolated-stable-20261011T060400Z/remote-bundle`.
It used a shared client/origin native-Linux host at fixed 4 connections, 256
streams, and 704 explicit workers. The baseline and treatment binaries differed
only in whether regular files selected legacy slice-per-entry storage or the new
contiguous arena.

| Variant | Clean runs | Median completed-response RPS |
| --- | ---: | ---: |
| Legacy storage | 3 | 58,117.946 |
| Arena storage | 3 | 60,410.762 |

Predeclared clean pairs 2, 5, and 7 improved by 6.4091%, 3.4877%, and 3.9895%,
respectively. Median within-pair improvement was 3.9895%; comparing the two arm
medians gives 3.9451%. Median client user time changed by -9.1902%, and median
maximum RSS by -42.3891%. All six selected runs lasted at least 30 seconds,
exactly reconciled 2,335,820 H3/HEAD successes apiece, reported zero logical,
transport, QUIC, retry, fallback, and UDP errors, and fully drained.

This run also validates the bounded final-attempt TTFB histogram under native
Linux load: its sample count was exactly 2,335,820 in every selected run, with
zero overflow. The conservative p99 upper bounds ranged from 26.738688 to
28.835840 ms. Arena run 1 was excluded for 44 `RcvbufErrors` plus excessive
preflight load; baseline runs 3, 4, and 6 were excluded for 17, 21, and 33
`RcvbufErrors`, and their treatments were correctly skipped. The bundle retains
all exclusions and 263 hashed entries; its outer `SHA256SUMS` hash is
`6c3901cbbe1b01e2f9a24029e4b7e96a5b9e76192697bb00620846fb6378fb74`.

This is isolated optimization and telemetry evidence, not a 100k
certification. It is one fixed setting on a shared host, not the required
adaptive/selected-fixed 5+5 cohort with isolated client and origin resources.

### Retained current-source CPU and allocation profiles

The profile bundle is
`harness/.work/current-hotpath-profile-20261011T064500Z`. It contains serial
CPU- and allocation-profile runs of the current source at fixed 4 connections,
256 streams per connection, and 704 request workers:

| Profile | Completed-response RPS | Main span | Exact successful responses |
| --- | ---: | ---: | ---: |
| CPU | 60,205.028 | 38.7978 s | 2,335,820 |
| Allocation | 60,331.897 | 38.7162 s | 2,335,820 |

Both runs passed all 18 checks, exactly reconciled their logical and origin
counts, recorded zero terminal, transport, QUIC, retry, fallback, and scoped UDP
errors, and fully drained. The profile findings rank the ordinary request/client
and task/input paths as described above. The outer `SHA256SUMS` hash is
`1ae0fc7c1cc6840a2692cb5fe1cc78aafc37491a561bfb290da7e3ed2720b410`.
Profiling and shared-host co-location make this diagnostic, not certification.

### Retained direct-RoundTripper A/B

The rejected treatment is retained at
`harness/.work/remote-direct-roundtrip-ab-20261011T070443Z/remote-bundle`.
Three clean alternating pairs produced:

| Variant | Clean runs | Median completed-response RPS |
| --- | ---: | ---: |
| Standard `http.Client.Do` | 3 | 60,425.995 |
| Direct owned-H3 RoundTripper | 3 | 61,661.386 |

All six runs lasted at least 30 seconds, exactly reconciled 2,335,820 successful
logical responses apiece, recorded zero terminal, transport, QUIC, retry,
fallback, and scoped UDP drop/buffer errors, and fully drained. Baseline run 3
did record a host-wide `Udp.NoPorts` increase of one outside the predeclared
scoped UDP gates; the bundle retains it explicitly. The treatment's 2.0445%
arm-median gain failed the 3% gate, and the product source was reverted to the
baseline binary SHA-256
`b7d75777eb365d6d9f64d42fa292ff846693fc732e30937f7ea4f4772f6a8692`.
The bundle's outer `SHA256SUMS` hash is
`5aef755fd4e0a05b42cf75bdc855518bdeae2177526fb55a2c614899f7436b62`.

### The current co-located host cannot supply the 100k CPU budget

The accepted arena diagnostics imply 61.7856 microseconds of client CPU per
response and 47.9189 microseconds of origin CPU per response. At 100k RPS these
consume 6.1786 and 4.7919 logical CPUs, or 10.9705 combined, on a host with only
8 logical CPUs. The corresponding ideal co-located ceiling is 72,923 RPS before
kernel, scheduler, and measurement overhead.

This is a capacity boundary, not evidence that the client itself cannot reach
100k. Client-only operation would require about 6.18 of the available 8 logical
CPUs at the observed cost. A meaningful next run therefore places the origin on
a second same-region native-Linux host and raises effective UDP receive and send
buffers on both hosts to at least 7,340,032 bytes. `mothership` is currently
capped at 212,992 bytes and changing it requires operator access. Certification
also needs an authoritative clone or worktree containing the exact current
source and a genuine Git `HEAD`. The present export has no `.git`, so its
`git_commit=null` evidence is ineligible; creating a synthetic repository or
editing fixture fields would not repair that provenance gap.

Port 8443 is not required and is already occupied by a long-running service on
`mothership`; that service must not be touched. Another data port is valid when
both TCP and UDP are opened and the same target authority is used for an entire
fresh campaign, including every prerequisite, sweep, cohort, and manifest. The
origin administration listener remains loopback-only.

### Retained local `/files` correctness evidence

The separate Darwin correctness bundle at
`harness/.work/local-files-proof-20261011T043725Z-r3/validation-summary.json`
passed all 45 checks. Automatic HEAD and forced GET each returned the exact same
12-item finding set. The automatic run issued 14 HEAD attempts and five
semantically justified GET fallbacks for directory redirects; the body-aware
run proved the one-byte response body was exactly `x`. All three runs selected
H3, exactly reconciled route, method, role, and run-ID counters, reported zero
transport and QUIC errors, and fully drained. Its `SHA256SUMS` file hashes to
`c20f6b4346c98afba590eb89338b4501062ad9d4e46cbcf0f78c9276c343502b`.

This bundle is correctness-only and diagnostic. The five intentional semantic
fallbacks make it unsuitable for the transport-ceiling fallback-rate gate, and
Darwin still does not satisfy native-Linux certification.

### Historical audit baseline

Before the measurement and performance work, the short audit observations were:

| Run | Inputs | Wall time | Completed-response RPS | Pool | Strategy | Transport errors |
| --- | ---: | ---: | ---: | --- | --- | ---: |
| Historical unretained observation, adaptive | 233,582 | 5.050 s | 46,254.56 | 10 to 20 | HEAD | 0 |
| Historical unretained observation, fixed | 233,582 | 4.867 s | 47,993.21 | 20 fixed | HEAD | 0 |
| Historical strongest uncommitted artifact | 233,582 | 3.982 s | 58,659.45 | 10 to 10, adaptive | HEAD | 0 |

The strongest report inspected in that historical baseline was at
`harness/.work/benchmark-20261003T033715Z-65691/benchmark-report.json`.
Those rows explain the starting gap; they are not the current result and were
not used in the sealed final local cohort.

### What the harness proves

For a valid run, the harness can prove that:

- direct HTTP/3 was negotiated and selected
- the expected GET, HEAD, or Range strategy was selected
- every scheduled logical input reached exactly one terminal success or error,
  with gaps, duplicates, and invalid transitions checked independently
- optimized attempts and any GET recovery are accounted for
- application-surfaced transport errors and application-level retries are
  separated from observable internal attempts and retries
- workers, stream admission, and the transport drained cleanly
- marked application attempts reconcile against route-, protocol-, method-,
  role-, and run-ID-specific origin counters
- fixed-sweep reports derive client and origin CPU plus link utilization from
  matched PERF-003 point pairs, and final-attempt TTFB p99 from the versioned
  bounded histogram in `responses.latency`
- `/files` and diagnostic producers enforce layered comparison keys, relative
  hashes, exact discovery accounting, and one unprofiled control per profile
- certification executes five explicit adaptive/selected-fixed pairs and
  records the ten-step order, reducing cohort time-drift bias

This is meaningful functional evidence that the research techniques are
present and their accounting and fallbacks work on the tested routes. It does
not prove that the adaptive policy is optimal or transport-equivalent to Turbo
Intruder.

### What the harness does not yet prove

The default `/bench/FUZZ` route returns an empty constant 204 response. It
measures the transport ceiling, not real filesystem or application discovery.
The retained `/files` bundle separately proves the exact discovery set,
directory redirect fallback semantics, and one-byte body matching, but it is a
small correctness fixture rather than a throughput workload.

The retained 100k 5+5 cohort, A42 production cohort, and A45 current-source
regression cohort all ran on Darwin. The native-Linux arena A/B, profiles, and
rejected direct-RoundTripper A/B add scoped UDP, live-load, and hot-path
evidence, but they remain sub-100k fixed-4 diagnostics with client and origin
sharing one host, not an isolated adaptive certification environment. Docker
Desktop evidence is also diagnostic because it includes a VM and published-port
networking path. The harness therefore does not yet provide an eligible
release-certification bundle or prove universal target performance.

The native-Linux network-matrix controller and its bounded profile fixture are
implemented, but validation to date uses fake `ip`, `tc`, and related command
boundaries. No live namespace/qdisc point has been run, so the latency, loss,
peer-limit, transient-failure, and 50 ms PERF-102 environment gates remain
unproven.

### Remaining native-Linux certification

First provision a second same-region native-Linux host, run the client alone on
`mothership`, and place the origin on the second host with at least 7,340,032
bytes of effective UDP receive and send buffering on both sides. Build and run
from an authoritative current-source checkout with a real Git `HEAD`. A data
port other than 8443 is valid if it is opened for TCP and UDP and used
consistently for a fresh campaign; do not disturb the existing 8443 service.
Then run the mandatory live network matrix and three-trial fixed sweep with host
networking and isolated resources. Produce the separate `/files` and diagnostic
manifests and run the sealed eligibility cohort in the fixed order `adaptive-1`,
`selected-fixed-1`, through `adaptive-5`, `selected-fixed-5`, with before/after
UDP counters on both sides. Preserve the aggregate, individual run manifests,
exact commands, binaries, environment, wordlist, origin state, resource
provenance, recorded cohort order, and outer `SHA256SUMS` just as the Darwin
diagnostic did.

A defensible 100k claim requires all of the following:

- median of five adaptive runs at or above 100,000 successful logical responses
  per main-request-span second, plus five selected-fixed validation runs
- at least 30 seconds of measured main-request span per run
- less than 0.1 percent terminal logical and surfaced attempt errors, with
  application and internally recovered retries reported separately
- no increasing UDP drop counter
- marked application-attempt and route-handler ledgers reconciling within 0.1
  percent, with pre-handler attempts tracked separately
- automatic-mode median reaching at least 90 percent of the selected clean
  fixed median
- recorded host, network, software, wordlist, and command metadata

All three retained Darwin performance cohorts pass the numerical, duration,
reconciliation, and drain items in this list; A42 additionally proves exact
runtime use of the production prepared-header path, while A45 proves the
current telemetry-on build preserves exact latency sampling. The `/files/FUZZ`
bundle supplies separate local correctness evidence. These local bundles remain
certification-ineligible because they cannot satisfy the native-Linux and UDP
gates. Do not present the constant 204 result as filesystem discovery
throughput, and do not update the README until the full native-Linux
certification bundle passes.

## Engineering status before claiming parity

The implementation-ready
[100k RPS action plan](100k-rps-action-plan.md) defines task ownership, exact
files, tests, performance gates, and the retained certification bundle. Its
measurement foundation, disabled-autocalibration fast exit, adaptive worker
supervisor, explicit generation recovery, acknowledged high-rate policy,
deterministic fixed sweep, resource evidence, `/files` and diagnostic producers,
alternating certification order, and the prepared-header production seam are
complete at the implementation and portable-test level. PERF-203 is closed as
`hypothesis-disproved/skipped`; PERF-202 passed its six-allocation gate, and
PERF-205 passed without another product optimization. The remaining
highest-value steps are:

1. Obtain the second same-region native-Linux origin host, operator-set
   7,340,032-byte-or-larger effective UDP buffers on both hosts, and a real
   authoritative current-source Git checkout/`HEAD`; the measured co-located
   CPU budget and the present export cannot support eligible certification.
2. Run the live native-Linux matrix, fixed sweep, discovery/diagnostic
   prerequisites, and alternating 5+5 cohort on separated resources.
3. Reconsider minimal headers, dynamic QPACK reuse, or a lower-level direct H3
   path only if new isolated profiles still justify them. The measured direct
   RoundTripper bypass is rejected and does not satisfy this condition.

The A41 seam is no longer pending. A42 integrates it in a pinned, reproducible
quic-go v0.63.0 fork with a default-off request-context API; the runner opts in
only for selected-H3, non-replay, reliable-HEAD requests, independently of the
high-rate policy. Unsafe shapes fail closed to the generic writer, both runtime
fallback branches use generic contexts, schema-v2 telemetry accounts for every
attempt and fallback reason, and bounded state is cleared with request
lifecycle. The root full/vet suite, focused root race tests, nested HTTP/3
race/vet tests, 112 harness tests, offline provenance reconstruction,
qlog/httptrace coverage, exact byte parity, and semantic fuzzing pass. The
five-run final local cohort then produced a
114,578.588 RPS median with 18,686,560 exact hits and zero fallbacks. This
closes the integration step, not the separated-host certification step.

PERF-202 has passed. It preserves exact tracing for telemetry,
generic, rejected, and fallback requests, and uses the trace-free timing
interval only for telemetry-off eligible automatic HEAD/Range. The A45
telemetry-on regression cohort then produced a 127,008.228 RPS median with
25,694,020 exact successes, latency samples, prepared hits, and origin-handler
requests. That confirms the current local default path, but neither its
descriptive difference from A42 nor any Darwin cohort closes separated-host
certification.

These changes are more important than increasing the default stream count.
PortSwigger's own measurements show that excessive stream concurrency can
reduce throughput through loss and retransmission.

## Attribution and primary sources

- Tom Stacey and PortSwigger,
  [HTTP/3 in Burp Suite: it's time to find a bigger wordlist](https://portswigger.net/research/http3-in-burp-suite)
- PortSwigger,
  [Turbo Intruder](https://github.com/portswigger/turbo-intruder)
- Tom Stacey,
  [HTTP3-Adapter](https://github.com/t0xodile-swig/HTTP3-Adapter)
- quic-go,
  [Optimizations](https://quic-go.net/docs/quic/optimizations/)

The implementation comparison is an independent audit. It does not imply that
PortSwigger has certified h3fuzz or its performance.
