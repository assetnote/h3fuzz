# Web console storage

h3fuzz uses one SQLite database for durable web-console state. The database
keeps sessions available across restarts and lets the results explorer filter,
sort, group, and page findings without loading an entire scan into the browser.

## What is stored

The database stores:

- session configuration and lifecycle state
- matched result metadata, including URL, status, response size, word and line
  counts, content type, redirect location, duration, and fuzz input
- optional scraper output and a compact response fingerprint
- terminal events, saved result views, presets, and web-console settings
- small aggregate tables and indexes used by the session rail and results
  explorer

Raw HTTP response bodies are not stored. Request headers, cookies, request
bodies, and scraper output can still contain sensitive data, so protect the
database and its backups.

Disk use is driven mainly by the number of retained findings, not by the number
of requests sent. A scan that filters the target's normal not-found response
may retain only a small number of rows. A scan that matches every wildcard or
soft-404 response may retain hundreds of thousands of rows, each with metadata
and index entries.

## Storage behavior

New databases use SQLite WAL mode and incremental auto-vacuum. Result writes
are batched. The indexes needed for browsing remain online while scans run, so
finishing a scan does not rebuild indexes across all historical results.

The default results explorer reads per-session status, content-type, and range
summaries. It does not scan all retained rows to render its initial facets.
Internal `FFUFHASH` values are not persisted, and response fingerprints use a
compact binary encoding. These choices reduce write amplification and the
amount of duplicated data per finding.

The storage warning appears when the database and WAL reach 1 GiB. It reports
estimated usage by session so the largest scans can be selected for cleanup.
The estimates attribute result and index payloads but do not attempt to assign
shared schema pages exactly.

An upgraded database can also show a one-time maintenance notice below 1 GiB.
This asks the operator to run Compact database so legacy directory-tree
metadata and result summaries are upgraded outside the startup and scan paths.

## Cleanup and compaction

Use this sequence when a warning appears:

1. Review the largest sessions in the storage warning.
2. Select scans that are no longer required and delete them in bulk.
3. Wait for all running, starting, paused, and stopping scans to finish or be
   stopped.
4. Select **Compact database** if disk space must be returned to the operating
   system.

Deleting a session removes its results and related console state. New
databases also perform a small, rate-limited incremental vacuum after an idle
deletion. SQLite can reuse free pages for later scans before the file becomes
smaller, so immediate file-size reduction is not guaranteed.

Explicit compaction performs maintenance that is intentionally kept off the
scan path. It validates and truncates the WAL, upgrades legacy result payloads,
rebuilds missing aggregate summaries, enables incremental auto-vacuum for a
legacy database, and runs `VACUUM`. Compaction is unavailable while a scan is
active. It also checks available disk space because SQLite needs working space
while rewriting the database.

For regular operation:

- configure matchers and filters so wildcard or soft-404 responses are not
  retained as discoveries
- delete obsolete scans instead of using one database as an indefinite archive
- keep the database on local storage with adequate free space
- back up the database before maintenance when its history is important
- use a separate database path for an unrelated engagement or retention domain

## Why SQLite remains the current store

SQLite fits h3fuzz's local, single-process console: it is embedded, requires no
database service, provides transactions for session state and results, and
supports the indexed queries used by the explorer. WAL mode permits readers to
continue while the writer commits batches, although SQLite still has one
writer at a time. See the SQLite guidance on
[appropriate uses](https://www.sqlite.org/whentouse.html) and
[WAL concurrency](https://www.sqlite.org/wal.html).

Moving to DuckDB would not remove file locking and is optimized for a different
analytical workload. See DuckDB's
[concurrency documentation](https://duckdb.org/docs/stable/connect/concurrency).
Running a client-server database would add deployment and authentication costs
that do not match the default local-console model.

A future storage format could isolate result payloads per session while keeping
a small catalog database. That becomes worthwhile if h3fuzz needs independent
retention, movement, or deletion of very large scans, or multiple writer
processes. It is a schema migration rather than a drop-in performance switch:
session creation, result cursors, terminal history, comparison, export,
backup, and crash recovery all need an atomic cross-store design. The current
implementation therefore fixes row and index amplification without introducing
a partial dual-store migration.
