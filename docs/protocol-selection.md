# Protocol selection and fallback

h3fuzz selects the fastest protocol it can prove is usable for the target. For
a direct HTTPS target in automatic mode, the preference order is:

1. HTTP/3 over QUIC
2. HTTP/2 over TLS and TCP
3. HTTP/1.1 over TLS and TCP

The selection happens during bounded preflight work before the wordlist scan
starts. The selected protocol shown in the CLI, web console, and preflight
report is based on a real negotiated response.

## Modes

| Mode | Flags | HTTP/3 probe | HTTP/2 offered | HTTP/1.1 fallback | Behavior when the preferred protocol is unavailable |
| --- | --- | --- | --- | --- | --- |
| Automatic | none | yes, for an eligible direct HTTPS target | yes | yes | Continue with the best protocol that negotiated successfully. |
| Require HTTP/3 | `-http3` | yes | no | no | Stop before scanning if HTTP/3 cannot be proven and initialized. |
| TCP with HTTP/2 preference | `-http2` | no | yes | yes | Continue with HTTP/1.1 if TLS ALPN does not select HTTP/2. |
| Disable HTTP/3 | `-no-http3` | no | yes | yes | Use the negotiated TCP protocol. |
| Require HTTP/1.1 | `-http1` | no | no | forced | Use HTTP/1.1 even when the server supports HTTP/2. |

Plain `http://` targets do not perform the QUIC or TLS ALPN probes and use
HTTP/1.1. h3fuzz does not automatically use cleartext HTTP/2, also called
`h2c`.

The `-http2` flag is a preference, not a strict requirement. It preserves the
standard TLS behavior of falling back to HTTP/1.1 when the target does not
negotiate `h2`. Use `-http1` when the scan must not negotiate HTTP/2.

## How automatic fallback is decided

For an eligible HTTPS target, h3fuzz first performs its direct HTTP/3
fingerprint. The probe must negotiate QUIC with ALPN `h3`, receive HTTP/3
SETTINGS, complete a response, reuse the connection, and pass the bounded
concurrent-stream check. A successful probe allows the HTTP/3 pool to be
initialized.

If HTTP/3 qualification or pool initialization fails in automatic mode,
h3fuzz records the downgrade reason and uses its TCP transport. The TCP
transport offers `h2` through TLS ALPN and observes the protocol version on an
actual response. It records HTTP/2 only when the response is HTTP/2. Otherwise,
it records HTTP/1.1.

This fallback is a preflight decision. h3fuzz does not silently move an active
HTTP/3 wordlist scan onto TCP after the scan starts. Transport failures during
an HTTP/3 run are reported as errors and handled by the HTTP/3 pool's recovery
logic.

Automatic HTTP/3 probing is skipped when a configured or environment proxy
applies to the target because a direct QUIC probe would bypass the proxy. The
TCP transport can still negotiate HTTP/2 or HTTP/1.1 through the configured
route. Requiring HTTP/3 with `-http3` rejects a configuration where the proxy
would be bypassed.

## Examples

Use automatic selection:

```sh
./h3fuzz -w final.txt -D -e php -u https://target.example/FUZZ
```

Require HTTP/3 and stop if it is unavailable:

```sh
./h3fuzz -w final.txt -D -e php -u https://target.example/FUZZ -http3
```

Skip HTTP/3, prefer HTTP/2, and allow HTTP/1.1 fallback:

```sh
./h3fuzz -w final.txt -D -e php -u https://target.example/FUZZ -http2
```

Disable only the automatic HTTP/3 probe:

```sh
./h3fuzz -w final.txt -D -e php -u https://target.example/FUZZ -no-http3
```

Require HTTP/1.1:

```sh
./h3fuzz -w final.txt -D -e php -u https://target.example/FUZZ -http1
```

## Invalid combinations

The following combinations are rejected before network traffic begins:

- `-http1` with `-http2`
- `-http1` with `-http3`
- `-http1` with `-no-http3`
- `-http3` with `-http2`
- `-http3` with `-no-http3`

## Local regression coverage

The protocol selection integration tests use local TLS servers only. They
cover these cases:

- automatic mode fails its local QUIC attempt and negotiates HTTP/2 with an
  HTTP/2-capable TLS server
- automatic mode fails its local QUIC attempt, cannot negotiate HTTP/2, and
  uses HTTP/1.1
- forced HTTP/1.1 connects to an HTTP/2-capable server without advertising or
  negotiating HTTP/2
- `-http2` skips HTTP/3 and falls back to HTTP/1.1 when the TLS server does not
  negotiate HTTP/2
- `-no-http3` skips HTTP/3 and records the actual negotiated TCP protocol

Run the focused tests with:

```sh
go test ./pkg/runner -run 'TestAutomaticProtocolSelection|TestForcedHTTP1|TestTCPProtocolModes'
```
