# BackupHub Web UI

The first Svelte 5 + Vite web interface supports administrator bootstrap, login/logout, diagnostic status and authorized audit reading. It is **not** a backup management interface yet: native capture connectors, real job orchestration, storage configuration and restore screens remain unimplemented.

Requirements: Node.js 22+.

```bash
cd web
npm ci
npm run check
npm run build
cd ..
go run ./cmd/backuphub serve -data-dir /path/to/private/state -ui-dir ./web/dist
```

First initialize secrets and issue a one-time admin token with `backuphub init` / `backuphub bootstrap`; see `docs/security-bootstrap.md`.

The compiled `web/dist` folder is served from the Go API origin (not from a publicly exposed Vite dev server). BackupHub remains loopback-only HTTP; for browser access on another machine, configure HTTPS with Nginx. Session cookies are Secure and SameSite=Strict. The CSRF cookie is scoped to `/` so the UI can read and submit its value; the session cookie remains HttpOnly.

Production packaging must bundle the compiled assets with the binary. A missing build causes the `-ui-dir` option to fail closed rather than silently show a blank page.
