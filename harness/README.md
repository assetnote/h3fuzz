# h3fuzz harness

The complete harness and certification guide is maintained in
[docs/performance/harness.md](../docs/performance/harness.md).

Run these commands from the repository root for a quick local check:

```sh
go build -o ./h3fuzz .
docker compose -f harness/compose.yaml up --build --wait
harness/scripts/smoke.sh
harness/scripts/correctness.sh
harness/scripts/benchmark.sh --repeat 1 --mode auto --connections 0
docker compose -f harness/compose.yaml down
```

The quick run validates local behavior. It is not a remote-performance or
release-certification result.
