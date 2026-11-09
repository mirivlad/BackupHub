# BackupHub — Security Notes

**v0.1.0 Preview 1 provides real backup and restore operations but is not production-hardened.** Use a dedicated internal test host, keep an independent established backup for critical systems, and verify restored content before reliance.

Controls implemented: Argon2id local passwords, one-time admin bootstrap, hashed database sessions, Secure/HttpOnly/SameSite=Strict cookies, CSRF protection, basic RBAC on source/job/export/restore APIs, authentication rate limits and audit records. Source credentials reside in AES-256-GCM vault outside the SQLite plaintext schema. Restic data is encrypted with a separate private key. Backend includes built-in TLS (initial cert is self-signed) and can sit behind Nginx.

**Unresolved threats/limitations:** local Restic repository and vault keys share the host by default; a compromised host can delete backups or read available plaintext; no off-host immutable copy or protected garbage collection; no full SQLite catalog disaster-recovery kit; remote SMB/SFTP ACL/xattr preservation incomplete; file-tree restore drills and protocol-specific security review are not complete. Backup data verification is not an application restore test. User-supplied local source path checks restrict BackupHub state overlap but the operator must restrict who can create sources. Credentials can be present in process memory during capture; PostgreSQL uses PGPASSWORD in child environment.

Operate the service only on trusted networks, configure firewall/TLS, preserve `master.key`, `restic.key` and state separately, keep repository storage on an independent disk/mount, and do not run ad hoc `restic forget/prune`.

To report vulnerabilities, use GitHub private vulnerability reporting if enabled or contact the maintainers privately. Never paste secrets or database contents into public issues.

See [current preview release notes](docs/releases/v0.1.0-preview.1.md) and [full requirements](docs/specification-v0.1.md).
