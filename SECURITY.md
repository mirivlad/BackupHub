# Security

BackupHub is in **early development**, with no supported backup capability. Do **not** use it to protect production data.

The HTTP server is currently bound to loopback only, with no user authentication; it exposes only diagnostic endpoints. Do not expose it directly to untrusted networks. Future data and configuration endpoints require completed authentication, RBAC, CSRF protection, audit logging, and a separate secret vault before being enabled.

Do not post credentials, repository keys, database dumps, mailbox contents, or exploitable details with secrets in public issues. Contact the maintainers privately through GitHub's security reporting options where available.

Main security requirements and acceptance tests are in `docs/specification-v0.1.md`.
