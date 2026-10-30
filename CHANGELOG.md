# Changelog

## Unreleased

## v0.2.0 - 2026-10-30

- Reduce web-console database growth with compact result payloads, aggregate
  explorer summaries, shorter session identifiers, and fewer duplicated index
  entries.
- Keep result indexes online across scans to avoid database-wide index rebuilds
  and large-history stalls at scan completion.
- Add per-session storage estimates, bulk selection of the largest scans, safe
  compaction eligibility, bounded deletion reclamation, and legacy database
  maintenance.
- Add session select-all, result-to-scan cloning, and a custom extension
  profile in the web console.

## v0.1.0 - 2026-10-13

- Prepare the initial h3fuzz release.
- Add an authenticated HTTPS web console for persistent, concurrent scan
  sessions.
- Add automatic HTTP/3, HTTP/2, and HTTP/1.1 protocol selection with explicit
  protocol overrides.
- Add HTTP/3 fingerprinting, response minimization, adaptive connection
  management, telemetry, and a reproducible performance harness.
- Retain ffuf-compatible scanning, matching, filtering, input, and output
  options.

The history before the h3fuzz fork is available in the
[ffuf repository](https://github.com/ffuf/ffuf/blob/master/CHANGELOG.md).
