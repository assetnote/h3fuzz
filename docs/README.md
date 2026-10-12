# h3fuzz documentation

Start with the repository [README](../README.md) for building h3fuzz, starting
the web console, creating a scan, and using the CLI.

## Operator guides

- [Protocol selection and fallback](protocol-selection.md) explains automatic
  HTTP/3, HTTP/2, and HTTP/1.1 selection, forced modes, proxies, and downgrade
  behavior.
- [Performance and certification](performance/README.md) separates measured
  results from throughput targets and indexes the benchmark and harness
  material.
- [Harness reference](performance/harness.md) documents the Docker origin,
  correctness routes, benchmark reports, and certification workflow.

## Engineering references

- [HTTP/3 implementation and 100k RPS analysis](performance/http3-100k-analysis.md)
- [100k RPS implementation action plan](performance/100k-rps-action-plan.md)
- [Performance approach registry](performance/100k-rps-approach-registry.md)
- [Adaptive worker capacity evidence](performance/perf-102-worker-capacity-evidence.md)

The engineering references retain implementation history and detailed
measurement contracts. They are not required for ordinary web-console use.
