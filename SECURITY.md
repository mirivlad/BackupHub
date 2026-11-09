# BackupHub — Security Notes

**v0.1.0 Preview 2 provides real backup and restore operations but is not production-hardened.** Use a dedicated internal test host, keep an independent established backup for critical systems, and verify restored content before reliance.

Controls implemented: Argon2id local passwords, one-time admin bootstrap, hashed database sessions, Secure/HttpOnly/SameSite=Strict cookies, CSRF protection, basic RBAC on source/job/export/restore APIs, authentication rate limits and audit records. Source credentials reside in AES-256-GCM vault outside the SQLite plaintext schema. Restic data is encrypted with a separate private key. Backend includes built-in TLS (initial cert is self-signed) and can sit behind Nginx.

**Unresolved threats/limitations:** local Restic repository and vault keys share the host by default; a compromised host can delete backups or read available plaintext; no off-host immutable copy or protected garbage collection; no full SQLite catalog disaster-recovery kit; remote SMB/SFTP ACL/xattr preservation incomplete; file-tree restore drills and protocol-specific security review are not complete. Backup data verification is not an application restore test. User-supplied local source path checks restrict BackupHub state overlap but the operator must restrict who can create sources. Credentials can be present in process memory during capture; PostgreSQL uses PGPASSWORD in child environment.

Operate the service only on trusted networks, configure firewall/TLS, preserve `master.key`, `restic.key` and state separately, keep repository storage on an independent disk/mount, and do not run ad hoc `restic forget/prune`.

To report vulnerabilities, use GitHub private vulnerability reporting if enabled or contact the maintainers privately. Never paste secrets or database contents into public issues.

See [current preview release notes](docs/releases/v0.1.0-preview.1.md) and [full requirements](docs/specification-v0.1.md).

## Mounted storage provisioning (Preview 2)

The Go API remains an unprivileged `backuphub` process. A separate root
`backuphub-storage.service`, activated through a root:backuphub mode-0660
Unix socket, can only list directories on eligible mounted volumes and create
**new** mode-0700 subdirectories owned by `backuphub`. The helper can also
select an *already private* (mode 0700, owned by backuphub) directory
without changing its ownership. It cannot rename/remove other files, chown
existing directories, execute shell commands or listen on TCP.

The server refuses paths with symlink components for privileged traversal.
Only an authorized BackupHub administrator can use storage configuration
endpoints. Repositories are recorded in SQLite and every snapshot retains
its actual repository identifier; disabling/removing a mount fails closed.
The full threat model of arbitrary mount replacements, NAS filesystem ACLs,
cross-volume recovery, storage retention and privilege boundaries has **not**
yet been accepted on all target Debian distributions. Install Preview 2 first
on a test host and keep independent backups of Restic keys and metadata.

## Passwordless Restic (Preview 3)

The administrator can select `no_password` for a **new** repository, or
make that the default for new storages. In this mode every Restic operation
uses `--insecure-no-password` and the system intentionally creates no
external repository password file. It is **not plaintext**: Restic still uses
its encrypted object format with an empty password, which is not a security
barrier. Anyone with an intact copy of that repository can read its data.
The service never automatically downgrades an existing protected repository.
The metadata schema assigns `password` to all pre-existing storages;
unknown modes are rejected, not interpreted as passwordless.
The administrator-only settings endpoint enforces session authentication,
CSRF protection on changes and audit logging.
