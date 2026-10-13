# h3fuzz

h3fuzz is a content discovery tool with automatic HTTP/3 acceleration and a
persistent web console. It is a fork of [ffuf](https://github.com/ffuf/ffuf),
so existing ffuf targets, wordlists, matchers, filters, request files, and most
command-line workflows remain available.

The normal HTTPS protocol order is HTTP/3, then HTTP/2, then HTTP/1.1. Before a
scan, h3fuzz can also qualify `HEAD` or `Range: bytes=-1` as a smaller response
strategy. It uses either strategy only when repeated baseline requests show
that the result metadata remains reliable.

## Responsible use

Run h3fuzz only against systems that you own or have explicit permission to
test. Content discovery, especially at high request rates, can disrupt a
service, trigger defenses, consume bandwidth, and create infrastructure cost.
Start with conservative settings on an unfamiliar target and coordinate load
testing with its operator.

## Why h3fuzz exists

Tom Stacey's PortSwigger research,
[HTTP/3 in Burp Suite: it's time to find a bigger wordlist](https://portswigger.net/research/http3-in-burp-suite),
showed how response minimization, multiplexed QUIC streams, and multiple
HTTP/3 connections can raise the ceiling for request-heavy security testing.
PortSwigger reported more than 100,000 requests per second from a laptop to a
remote host over Wi-Fi, and about 180,000 requests per second from a cloud host
in the same region as the target.

h3fuzz applies those ideas to ffuf's content-discovery model. It is an
independent Go implementation informed by the research and by the open source
[Turbo Intruder](https://github.com/portswigger/turbo-intruder) and
[HTTP3-Adapter](https://github.com/t0xodile-swig/HTTP3-Adapter) projects. It
does not copy Turbo Intruder's request engine and does not claim that every
target can sustain the rates reported in the research.

For an eligible HTTPS target, an automatic scan:

1. Negotiates QUIC with ALPN `h3`, receives HTTP/3 settings, checks connection
   reuse, and runs a bounded concurrent-stream probe.
2. Uses an HTTP/3 connection pool when qualification succeeds. Otherwise it
   negotiates HTTP/2 over TLS and falls back to HTTP/1.1 when needed.
3. Compares repeated full `GET` baselines with `HEAD`, then with a final-byte
   Range request if `HEAD` is unreliable.
4. Runs the content-discovery workload with the selected protocol and response
   strategy.
5. Falls back to a full `GET` for an individual optimized request when its live
   response no longer matches the qualified behavior.

Protocol choice and response minimization are independent. A scan can use
HTTP/3 with full `GET` responses, or a qualified smaller response over HTTP/2
or HTTP/1.1.

## Install a release

Prebuilt archives and checksums are available from
[GitHub Releases](https://github.com/assetnote/h3fuzz/releases). Release
archives include the h3fuzz binary, `final.txt`, documentation, license files,
and the local performance harness.

Common archive names are:

| Platform | Archive |
| --- | --- |
| Linux x86-64 | `h3fuzz_0.1.0_linux_amd64.tar.gz` |
| Linux ARM64 | `h3fuzz_0.1.0_linux_arm64.tar.gz` |
| macOS Apple silicon | `h3fuzz_0.1.0_macOS_arm64.tar.gz` |
| macOS Intel | `h3fuzz_0.1.0_macOS_amd64.tar.gz` |
| Windows x86-64 | `h3fuzz_0.1.0_windows_amd64.zip` |

The GitHub CLI can download a specific archive and its checksum file. This
example downloads the Linux x86-64 release. Authenticate first with
`gh auth login` when the repository requires GitHub access:

```sh
version=0.1.0
asset="h3fuzz_${version}_linux_amd64.tar.gz"
gh release download "v${version}" \
  --repo assetnote/h3fuzz \
  --pattern "$asset" \
  --pattern "h3fuzz_${version}_checksums.txt"
grep "  ${asset}$" "h3fuzz_${version}_checksums.txt" | sha256sum -c -
mkdir "h3fuzz-${version}"
tar -xzf "$asset" -C "h3fuzz-${version}"
cd "h3fuzz-${version}"
./h3fuzz -V
./h3fuzz -web
```

On macOS, use the appropriate `macOS` archive and replace `sha256sum` with
`shasum -a 256`. On Windows, download the matching `.zip`, verify its SHA-256
value against the checksum file, extract it, and run `h3fuzz.exe -web` from the
extracted directory. Keeping the extracted files together makes the bundled
`final.txt` available as the default wordlist.

## Build

The module currently requires Go 1.26. The compiled web assets are checked in
and embedded in the binary, so a normal build does not require Node.js.

```sh
git clone https://github.com/assetnote/h3fuzz.git
cd h3fuzz
go build -o ./h3fuzz .
```

Verify the binary:

```sh
./h3fuzz -V
./h3fuzz -h
```

To change the web application, install pnpm, rebuild the frontend, then rebuild
the Go binary:

```sh
pnpm --dir frontend install --frozen-lockfile
pnpm --dir frontend build
go build -o ./h3fuzz .
```

## Start the web console

The web console is the recommended way to use h3fuzz for interactive content
discovery:

```sh
./h3fuzz -web
```

The process prints the console URL, username, and password. By default:

- the console listens on `https://127.0.0.1:8787`
- the username is `h3fuzz`
- TLS uses a locally generated self-signed certificate
- the password is generated once for the selected database and reused on
  later runs
- sessions, results, settings, terminal history, and saved views persist in
  SQLite

Open the printed URL, accept the local certificate on first use, and enter the
printed credentials. Basic authentication also protects the REST API and live
WebSocket connection.

Use an explicit database path when you want the state in a known location:

```sh
./h3fuzz -web -web-db ./h3fuzz-web.db
```

The generated certificate and key are stored beside that database unless
`-web-cert` and `-web-key` select an existing TLS identity. Protect the
database and its backups because the database contains the Basic Auth secret
and may contain request headers or cookies.

### Create a first scan

Select the plus button and complete the single-page scan form:

1. Enter an absolute target containing a fuzz keyword, such as
   `https://target.example/FUZZ`. h3fuzz does not add `FUZZ` automatically.
2. Keep `final.txt` or select another wordlist readable by the h3fuzz process.
3. Select the ASP Minimal, ASP / ASP.NET, PHP, JSP / Java, or Directories
   profile and review the resulting extensions.
4. Review matchers and filters. The bundled profiles match all statuses and
   filter `404` responses. Status, size, word, line, time, and regular
   expression controls are available.
5. Enable the Akamai target preset only when browser-like client hints and
   fetch metadata are needed. Its headers remain visible and editable.
6. Leave protocol mode and response minimization on Automatic unless the
   target requires an override.
7. Search Advanced options when an inherited ffuf flag is not among the common
   controls.
8. Select Create and run, or create an Idle session for later use.

`final.txt` contains DirSearch-style `%EXT%` entries. The web defaults enable
DirSearch compatibility so those entries expand using the selected profile.
The web console's regular HTTP worker baseline is 1,000. When HTTP/3 is
selected and `-t` was not explicitly set, the adaptive worker supervisor sizes
request workers from the negotiated connection and stream capacity.

The form also provides a copyable CLI preview. A compatible h3fuzz or ffuf
command can be pasted into the same form to populate its fields.

### During and after a scan

The console supports multiple concurrent, persistent sessions. Each session
provides:

- live status, progress, rolling rate, 30-second average, overall average,
  peak rate, ETA, request, response, and error counters
- a protocol preflight report with negotiation, response-strategy, capacity,
  timing, and downgrade evidence
- pause, resume, stop, clone, rerun, archive, pin, compare, and bulk session
  actions
- server-side result search, sorting, filters, facets, saved views, and
  virtualized rendering
- list and collapsible directory-tree result views
- result details with copy URL, copy path, copy cURL, and open target actions
- categorized transport errors and representative diagnostics
- searchable, virtualized terminal history with severity and timestamp
  controls
- JSON, path-only text, and terminal exports with preview, copy, and download
- inherited defaults and named presets for wordlists, profiles, headers,
  matching, filtering, and protocol settings

Filters can be added after a scan starts. They are saved with the session and
applied by the server across the complete result set, not only the rows loaded
in the browser.

### Web security and storage

| Flag | Purpose | Default |
| --- | --- | --- |
| `-web` | Start the web console | off |
| `-web-listen HOST:PORT` | Set the listen address | `127.0.0.1:8787` |
| `-web-db PATH` | Set the SQLite database path | platform XDG data path |
| `-web-cert PATH` | Use an existing PEM certificate | generated beside the database |
| `-web-key PATH` | Use an existing PEM private key | generated beside the database |
| `-web-allow-remote` | Permit a non-loopback listener | off |
| `-web-plain-http` | Disable TLS for a trusted local reverse proxy | off |
| `-web-allow-input-cmd` | Permit web sessions to execute `-input-cmd` | off |

The server refuses a non-loopback listener unless `-web-allow-remote` is also
set. Prefer an SSH tunnel instead of exposing the console directly:

```sh
ssh -L 8787:127.0.0.1:8787 user@scanner-host
```

If direct binding is required, restrict network access and provide a trusted
certificate whose subject alternative names cover the listen host:

```sh
./h3fuzz -web \
  -web-listen scanner.example.com:8787 \
  -web-allow-remote \
  -web-cert /secure/scanner.example.com.crt \
  -web-key /secure/scanner.example.com.key
```

Plain HTTP sends Basic Auth credentials without transport encryption. Use
`-web-plain-http` only on loopback behind a trusted local TLS reverse proxy.

The database stores result metadata, not raw HTTP response bodies. Large scans
can still consume substantial space when most responses are retained. Filter
the target's normal not-found response, delete sessions that are no longer
needed, and use Compact database after deletion when disk space must be
returned to the operating system.

## Command-line use

The CLI remains useful for automation, existing ffuf workflows, and controlled
throughput measurement.

### Basic discovery

```sh
./h3fuzz \
  -u https://target.example/FUZZ \
  -w final.txt \
  -D \
  -e html,js \
  -mc all \
  -fc 404
```

`-D` enables DirSearch compatibility so `%EXT%` markers in `final.txt` are
expanded. `-e` alone keeps ffuf's append-extension behavior and does not
replace those markers.

Common ffuf-compatible patterns include:

```sh
# Virtual-host discovery
./h3fuzz -u https://target.example/ -w hosts.txt -H 'Host: FUZZ.target.example'

# Query-value discovery
./h3fuzz -u 'https://target.example/search?q=FUZZ' -w values.txt

# JSON POST body
./h3fuzz -u https://target.example/api -w values.txt \
  -X POST -H 'Content-Type: application/json' -d '{"name":"FUZZ"}'

# Match every status, then remove a known wildcard response by word count
./h3fuzz -u https://target.example/FUZZ -w paths.txt -mc all -fw 42
```

Run `./h3fuzz -h` for the complete inherited and h3fuzz-specific flag list.

### Common issues

- A scan must contain an active fuzz keyword in its URL, header, body, or other
  configured input. The web form intentionally does not append `FUZZ`.
- `final.txt` requires DirSearch compatibility and at least one extension when
  its `%EXT%` entries are used. In the CLI, use `-D -e ...`.
- The Errors counter reports inputs that did not produce a usable response,
  such as malformed expansions, DNS or transport failures, and timeouts. HTTP
  `403`, `404`, and `500` responses are normal responses handled by matchers and
  filters.
- Large numbers of retained matches can make rendering or SQLite persistence
  the bottleneck. Verify the target's wildcard response and filter that known
  noise before drawing transport-performance conclusions.
- Check the Preflight tab or CLI banner for the negotiated protocol and
  downgrade reason. A server advertising HTTP/3 is not enough by itself to
  pass direct negotiation and the bounded capability checks.

### Protocol selection

Automatic mode uses the fastest protocol that it proves usable:

```text
HTTP/3 -> HTTP/2 -> HTTP/1.1
```

| Intent | Flag |
| --- | --- |
| Use automatic selection | no protocol flag |
| Print the HTTP/3 capability report and exit | `-http3-fingerprint` |
| Require HTTP/3 and fail on downgrade | `-http3` |
| Skip HTTP/3, prefer HTTP/2, allow HTTP/1.1 fallback | `-http2` |
| Disable only the automatic HTTP/3 probe | `-no-http3` |
| Require HTTP/1.1 and disable HTTP/2 negotiation | `-http1` |

Example fingerprint:

```sh
./h3fuzz -u https://target.example/FUZZ -http3-fingerprint
```

Plain `http://` targets use HTTP/1.1. h3fuzz does not automatically negotiate
cleartext HTTP/2 (`h2c`). Proxies also prevent the direct QUIC probe because it
would bypass the configured route. See
[Protocol selection and fallback](docs/protocol-selection.md) for exact mode,
fallback, conflict, and regression-test behavior.

### Response minimization

For an eligible bodyless `GET`, h3fuzz compares three full baselines with its
`HEAD` and Range probes. It keeps full `GET` when status, logical size, word or
line counts, matchers, filters, scrapers, or changing behavior make the smaller
strategy unreliable.

Disable this qualification when exact response bodies are required:

```sh
./h3fuzz -u https://target.example/FUZZ -w paths.txt \
  -no-response-optimization
```

## High-rate HTTP/3 measurement

PortSwigger's result is a research measurement, not a universal h3fuzz
guarantee. Request rate depends on latency, response strategy, input duration,
server capacity, client and server CPU, UDP buffers, packet loss, matchers,
filters, output, and result persistence.

h3fuzz has exceeded 100,000 completed responses per second in privately
retained local Darwin diagnostic cohorts. The later five-run current-source
cohort recorded a median of 127,008.228 completed-response RPS, with every run
above 100,000 for more than 40 seconds and exact client and origin accounting.
The raw benchmark bundles are not distributed in this repository. The recorded
metrics and hashes demonstrate the controlled local path, not arbitrary remote
performance. This is not the pending native-Linux separated-host release
certification. Co-located native-Linux profiles were around 60,000 RPS and
showed that the shared client and origin CPU budget was the limiting
environment.

For an authorized, controlled ceiling test, use the CLI-only high-rate policy
with a long flat wordlist and a known baseline filter:

```sh
./h3fuzz \
  -u https://target.example/FUZZ \
  -w /path/to/long-flat-wordlist.txt \
  -http3-high-rate \
  -http3-high-rate-acknowledge \
  -http3-target-rps 100000 \
  -http3-max-connections 64 \
  -http3-streams 256 \
  -http3-max-workers 16384 \
  -mc all -fc 404 \
  -run-telemetry run.json
```

The policy requires direct HTTP/3, automatic response qualification, a finite
single wordlist, structured telemetry, no explicit `-t`, and explicit operator
acknowledgement. It rejects incompatible options and fails before load when the
declared connection, stream, worker, and measured-latency bounds cannot supply
the requested target. A target RPS value is a sizing objective, not a promise.

Read the [performance and certification guide](docs/performance/README.md)
before interpreting a rate. It links the implementation analysis, retained
measurements, harness contract, action plan, and remaining certification work.

## Local harness

The repository includes a Docker-based origin with HTTP/1.1, HTTP/2, HTTP/3,
TCP-only, UDP-only, reliable and misleading response-optimization routes, and
instrumented request accounting.

With Docker Compose v2, Bash, Python 3, and a built binary:

```sh
docker compose -f harness/compose.yaml up --build --wait
harness/scripts/smoke.sh
harness/scripts/correctness.sh
harness/scripts/benchmark.sh --repeat 1 --mode auto --connections 0
docker compose -f harness/compose.yaml down
```

These checks validate protocol selection, multiplexing, response strategy,
fallback, logical and transport accounting, and clean drain behavior. A local
harness run does not prove that a remote target will sustain the same rate.
See the [complete harness guide](docs/performance/harness.md).

## Documentation

- [Documentation index](docs/README.md)
- [Protocol selection and fallback](docs/protocol-selection.md)
- [Performance and certification](docs/performance/README.md)
- [HTTP/3 implementation and 100k RPS analysis](docs/performance/http3-100k-analysis.md)
- [100k RPS implementation action plan](docs/performance/100k-rps-action-plan.md)
- [Harness reference](docs/performance/harness.md)

## Attribution

h3fuzz is built on [ffuf](https://github.com/ffuf/ffuf), created by Joona
Hoikkala and contributors, and retains ffuf's MIT license and copyright notice.

The HTTP/3 approach credits Tom Stacey and PortSwigger:

- [HTTP/3 in Burp Suite: it's time to find a bigger wordlist](https://portswigger.net/research/http3-in-burp-suite)
- [PortSwigger Turbo Intruder](https://github.com/portswigger/turbo-intruder)
- [Tom Stacey's HTTP3-Adapter](https://github.com/t0xodile-swig/HTTP3-Adapter)

HTTP/3 transport support uses a pinned fork of
[quic-go](https://github.com/quic-go/quic-go). See
[Third-party notices](THIRD_PARTY_NOTICES.md) for dependency provenance and
license details.

## License

See [LICENSE](LICENSE).
