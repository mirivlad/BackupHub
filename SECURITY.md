# BackupHub security

BackupHub v0.6.0 targets dedicated hosts on trusted networks. Configure TLS
and network access controls, keep an independent backup for critical systems,
and test restoration before relying on a backup.

## Access and credentials

The daemon uses local Argon2id passwords, one-time administrator bootstrap,
hashed sessions, Secure/HttpOnly/SameSite=Strict cookies, CSRF protection,
role checks and audit records. Administrators configure storage, schedules and
sources. Backup operators can start repository checks and restore operators
can start restore checks. Authorized readers can see results and diagnostics,
which can include repository details and paths. The service journal is
administrator-only and exposes a bounded tail of BackupHub's own messages.
The package uses the existing unprivileged account with its read-only
`CAP_DAC_READ_SEARCH` capability for local backups.

BackupHub encrypts source credentials in its AES-256-GCM vault. The master key
and each protected Restic repository password are separate private files.
During capture, credentials may be present in process memory. PostgreSQL
passes `PGPASSWORD` in the child environment. Configure SFTP host keys through
the first-use fingerprint check.

A new repository may use an empty Restic password. Anyone with an intact copy
of that repository can read its data. Existing protected repositories keep
their password mode.

## Storage and restoration

The root-owned socket-activated storage helper can browse eligible mounted
volumes and create private BackupHub subdirectories. It accepts requests on a
root:backuphub mode-0660 Unix socket. Privileged traversal rejects symlink
components. Use independent disks and store the recovery kit, catalog and
keys offline and away from the service host. A compromised host can delete
accessible backups or read plaintext. Immutable replication, protected
retention and full SMB/SFTP ACL preservation remain open work.

Integrity checks and file recovery tests use read operations and ordinary
Restic locking. A reported lock conflict requires operator review. A file
recovery test uses a private temporary directory and verifies a bounded sample.
Application recovery requires an independent, application-specific test.
See the [verification runbook](docs/verification.md) and
[offline recovery guide](docs/recovery.md).

Report vulnerabilities privately through GitHub security reporting or the
maintainers. Keep credentials, diagnostic dumps containing secrets and the
contents of repositories out of public issues.
