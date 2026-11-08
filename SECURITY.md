# Security

BackupHub remains in **early development**: **no production backup/restore capability** is available. Do not trust this build for production data protection.

## Current controls

- Loopback-only HTTP API, intended to run behind a properly configured HTTPS reverse proxy (such as Nginx).
- Explicit one-time master-key initialization; protected state directory and 0600 master key. Vault entries use AES-256-GCM with randomized nonces and associated data; the master key is separate from SQLite.
- One-time, short-lived administrator bootstrap token issued by local CLI only.
- Argon2id password hashing, 7-day hashed database sessions, Secure/HttpOnly/SameSite=Strict session cookies, separate CSRF token cookie and request header, per-IP rate limits, basic audit events and protected API.
- Security-sensitive API endpoints require authentication; auditors and administrators can access audit records.

## Important limits

This is not a complete security review. The web UI, privileged backup operations, authorization scopes for source data, external recovery kit, independent repository protection, restore drills, and native connectors are **not implemented** yet. An attacker who gets both the SQLite data and the live server's master key can decrypt source secrets. Store the master key in an access-controlled offline recovery kit and do not expose the backend HTTP port directly to a network.

Use [the security bootstrap instructions](docs/security-bootstrap.md) for safe local setup. Full threat model and required acceptance tests: [specification](docs/specification-v0.1.md).

Please do not post credentials, repository keys, database dumps, mailboxes, or secrets in public issues. Use GitHub's private vulnerability reporting if enabled or another private maintainer channel.
