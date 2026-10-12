# Performance and certification

This directory contains h3fuzz's HTTP/3 performance evidence, benchmark
contract, and implementation history. It distinguishes a requested rate from a
measured result and a diagnostic run from release certification.

## Current evidence

PortSwigger reported more than 100,000 requests per second from a laptop to a
remote host over Wi-Fi, and about 180,000 requests per second from a cloud host
in the same region as the target. Those are PortSwigger research results, not
h3fuzz guarantees.

h3fuzz has independently exceeded 100,000 completed responses per second on a
controlled local Darwin workload. The privately retained five-run
current-source cohort recorded a median of 127,008.228 completed-response RPS.
Every run exceeded 100,000 for more than 40 seconds and reconciled client
completions with origin requests. The raw benchmark bundles are not distributed
in this repository. This is local diagnostic evidence. It is not the pending
native-Linux separated-host release certification and does not predict the rate
of an arbitrary remote target.

The current co-located native-Linux profiles measured about 60,000 RPS. Client
and origin CPU accounting showed that the shared 4-core, 8-thread host could
not supply the combined CPU budget for a valid 100k attempt. Certification
therefore still requires separate same-region client and origin hosts, adequate
UDP buffers, retained network evidence, clean error and loss gates, and the
specified repeated-run duration.

The detailed analysis records exact cohorts, hashes, denominators, caveats,
rejected optimizations, and remaining certification gates.

## Documents

| Document | Purpose |
| --- | --- |
| [HTTP/3 implementation and 100k RPS analysis](http3-100k-analysis.md) | Source comparison, architecture, measured cohorts, limitations, and current verdict |
| [Harness reference](harness.md) | Docker topology, correctness checks, benchmark commands, schemas, and certification workflow |
| [100k RPS implementation action plan](100k-rps-action-plan.md) | Detailed engineering work packages, acceptance gates, and retained implementation status |
| [Performance approach registry](100k-rps-approach-registry.md) | Accepted, rejected, completed, and queued performance hypotheses |
| [Adaptive worker capacity evidence](perf-102-worker-capacity-evidence.md) | Focused evidence for HTTP/3 worker growth and explicit `-t` behavior |

## Quick harness run

Run commands from the repository root. The quick harness needs Docker Compose
v2, Bash, Python 3, and a built `./h3fuzz` binary.

```sh
docker compose -f harness/compose.yaml up --build --wait
harness/scripts/smoke.sh
harness/scripts/correctness.sh
harness/scripts/benchmark.sh --repeat 1 --mode auto --connections 0
docker compose -f harness/compose.yaml down
```

This run validates the selected protocol, response strategy, request
accounting, and drain behavior. It is not certification merely because the
reported rate crosses a particular threshold. Follow the complete
[harness reference](harness.md) for measurement and eligibility requirements.

## Interpreting rates

Use completed successful logical responses divided by the monotonic main
request interval. Do not substitute queued requests, processed wordlist items,
the volatile one-second UI rate, or server request starts.

At minimum, retain:

- the exact binary and configuration
- protocol and response-strategy evidence
- logical, transport, retry, and error accounting after drain
- client and origin request correlation
- elapsed interval and repeated-run aggregation
- client and server CPU, UDP loss, socket-buffer, and network evidence
- the input count and enough work for the required measurement duration

A target value such as `-http3-target-rps 100000` sizes and validates capacity.
It does not claim that the target delivered that rate.
