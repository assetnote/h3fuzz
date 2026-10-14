# Security policy

## Reporting a vulnerability

Use the **Report a vulnerability** control on this repository's Security page
when GitHub private vulnerability reporting is available.

Do not open a public issue or pull request for a security vulnerability.

Include the affected h3fuzz version or commit, impact, reproduction steps, and
any suggested mitigation. Remove target credentials, scan results, and other
sensitive data from the report unless they are required to reproduce the issue.

## Scope

This policy covers vulnerabilities in h3fuzz itself, including its CLI, web
console, protocol implementations, persistence layer, and bundled harness. A
vulnerability discovered in a system scanned with h3fuzz must be reported to
that system's owner instead.

Only use h3fuzz against systems you own or have explicit permission to test.

## Supported versions

Security fixes are made against the latest release and the `main` branch.
Confirm that the issue reproduces on one of those versions before reporting it.
