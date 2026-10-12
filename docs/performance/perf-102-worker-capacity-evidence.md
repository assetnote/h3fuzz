# PERF-102 adaptive request-worker capacity evidence

[Performance documentation](README.md)

Date: 2026-10-11

PERF-102 is implemented with a transport-neutral proposal/acknowledgement
protocol and an engine-owned worker supervisor. This note records the local
correctness and resource evidence. It is not a 100k-RPS certification result.

## Retained behavior

- Each queue execution has a monotonically increasing generation. Pool
  proposals and engine acknowledgements carry the exact generation/epoch tuple,
  proposed and live counts, remaining-input cap, lifecycle state, and timestamp.
- Proposal and acknowledgement publication is asynchronous, one-element, and
  latest-value coalescing. A subscriber receives an immediate ready replay.
  Stale, superseded, timed-out, late-after-timeout, missing-listener,
  unsubscribe, and cancellation outcomes cannot reset the pool measurement
  baseline.
- One supervisor owns every worker `Add`, spawn, between-task retirement, and
  drain. It is joined before the next queue generation. Growth is bounded by
  remaining input, `-http3-max-workers`, and the 65,536 automatic safety cap.
- Omitted `-t` enables adaptive sizing. Explicit `-t`, including `-t 40`, is a
  fixed legacy worker count and hard cap. CLI visits, TOML/`.ffufrc`, JSON
  history, web settings/session creation, and frontend CLI import retain that
  provenance. The same rule applies to an explicitly supplied
  `-http3-max-workers` value.
- `peak_main_in_flight` counts simultaneous marked main-work transport calls,
  including optimization fallback and engine retry roles. It excludes probes,
  warmup, qualification, preflight, and postflight traffic.

The local diagnostic data in the approach registry is deliberately
non-monotonic: explicit 80 workers was slower than the automatic/fixed-4 point.
The retained pool tuner therefore measures, backtracks, and obeys exact bounds;
the supervisor does not assume that more workers improve throughput.

## Correctness evidence

The following commands used Go 1.27.1 with
`GOROOT=/opt/homebrew/opt/go/libexec`:

```text
go test ./...
go test -race ./pkg/engine ./pkg/runner ./pkg/ffuf ./pkg/webserver
```

Both passed. Focused deterministic coverage includes:

- 5,000 simultaneously live workers without network traffic, followed by a
  zero-live/zero-retiring drain;
- 200-to-20 shrink and concurrent regrowth with no lost or duplicate task IDs;
- input-exhaustion capping, old generation/epoch rejection, coalescing, repeated
  timeout/late acknowledgement classification, republish/recovery, missing
  listeners, cancellation, and bounded subscriber publication;
- stop while paused, growth racing drain, and fixed-versus-adaptive subscription
  selection;
- pool capacity transitions for 10 to 20 to 40 connections and shrink, with the
  acknowledged worker ceiling changing only for the matching tuple;
- explicit-default provenance across Go config/web paths and frontend command
  import, plus the frontend wizard's automatic/fixed toggle.

Frontend focused tests (57 tests), production build, and lint also passed.

## Idle-worker resource measurement

Command:

```text
go test ./pkg/engine -run '^$' -bench '^BenchmarkWorkerSupervisorIdle5000$' -benchtime=1x -count=5
```

Environment: Darwin/arm64, Apple M2 Max, Go 1.27.1. All samples reached exactly
5,000 live workers and reported `1.000 goroutines/worker`; every iteration then
fully drained. The first cold sample reported 894.2 heap bytes, 2,182 stack
bytes, and 3,077 combined live bytes per worker. Four warm samples reported:

| Metric | Warm range per worker |
| --- | ---: |
| Heap | 373.0-428.4 bytes |
| Stack | 1,507-1,652 bytes |
| Combined live memory | 1,883-2,054 bytes |

These are Go runtime deltas for idle supervised goroutines, not process RSS and
not a native-Linux performance point.

## Deferred gate

The mandatory 50 ms, zero-loss, peer-limit-256 end-to-end capacity gate remains
deferred to PERF-301, exactly as specified. Its native-Linux network-matrix
controller and bounded fixture now exist and pass fake-command lifecycle and
cleanup tests, but no live namespace/`tc` profile has run. PERF-102 therefore
makes no end-to-end 100k claim; its required no-network 5,000-worker acceptance
gate is green.
