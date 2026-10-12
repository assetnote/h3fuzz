# Contributing to h3fuzz

h3fuzz requires the Go version declared in `go.mod`. The web console also
requires Node.js and pnpm when its frontend is changed.

## Build and test

Build the embedded frontend after changing `frontend/`:

```sh
cd frontend
pnpm install --frozen-lockfile
pnpm lint
pnpm test
pnpm build
cd ..
```

Then validate the Go code:

```sh
gofmt -w <changed-go-files>
go vet ./...
go test -race ./... -count=1
```

The production frontend in `web/dist` is checked in because it is embedded in
the Go binary. Commit it whenever a frontend build changes it.

Frontend or Go dependency changes can alter the notices shipped with release
archives. With the frontend dependencies installed, regenerate and verify the
deterministic license bundle:

```sh
node scripts/generate-third-party-licenses.mjs
git diff --exit-code -- THIRD_PARTY_LICENSES.txt
```

The generator also validates its Go target list against `.goreleaser.yml` and
checks the pinned license overrides under `scripts/licenses/`.

If a change affects help, flags, or configuration output, regenerate the
characterization fixtures:

```sh
go test -run Characterization -update-golden .
```

## Patched quic-go module

The root module replaces `github.com/quic-go/quic-go` with the source tree in
`third_party/quic-go`. Before submitting a change that affects it, run:

```sh
scripts/verify-third-party-quic-go.sh
(cd third_party/quic-go && go vet ./http3 && go test -race ./http3 -count=1)
```

Do not edit the copied module without regenerating
`third_party/quic-go-a42.patch` and updating
`third_party/quic-go.UPSTREAM.json`. The verifier reconstructs the pinned
upstream release and checks the copied tree byte for byte.

## Pull requests

- Base changes on `main`.
- Keep each pull request focused on one logical change.
- Add or update tests for behavior changes.
- Update the relevant file under `docs/` when operator behavior changes.
- Do not commit binaries, databases, credentials, dependency directories,
  profiles, qlogs, or harness work output.

Report security issues through the private process in [SECURITY.md](SECURITY.md).
