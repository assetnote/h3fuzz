# h3fuzz web client

The web client is a dark operator console built with React, TypeScript, Vite, Radix Themes and Primitives, TanStack Query, and TanStack Virtual. It consumes the authenticated REST and WebSocket APIs under `/api/v1`.

## Development

From this directory:

```sh
pnpm install
pnpm dev
```

The Vite development server proxies `/api` and WebSocket upgrades to `http://127.0.0.1:8080`.

## Verification

```sh
pnpm lint
pnpm test
pnpm build
```

The production build is written to `../web/dist`. Those generated files are checked in because the Go server embeds them, which keeps `go build .` independent of Node.js.

The browser receives all session events from one same-origin WebSocket. Basic authentication is established by loading the protected console and is reused by the browser for the WebSocket upgrade. REST snapshots and cursor-based result reads recover missed events after a reconnect or sequence gap.
