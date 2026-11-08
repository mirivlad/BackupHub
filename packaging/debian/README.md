# BackupHub Debian package

The package includes the compiled Go daemon, Svelte production assets and a
systemd unit. The service runs as the dedicated, unprivileged `backuphub` user.

After installation, browse `https://backup-host:8443/`, create the first
administrator via a token displayed by `runuser -u backuphub -- backuphub
bootstrap -data-dir /var/lib/backuphub`. The initial TLS certificate is
self-signed. Configure an existing trusted Nginx proxy or replace the
certificate in `/etc/backuphub`.

The built-in Restic repository defaults to `/var/lib/backuphub/repository`.
For a large NAS/storage mount, configure `BACKUPHUB_REPOSITORY=/mount/path` in
`/etc/backuphub/backuphub.env` **before starting the service for the first
time**; also configure the systemd sandbox write permissions for that path via
a unit override. Merely changing the env variable on an existing instance
without migrating the encrypted repository and preserving `restic.key` makes
the original backups inaccessible.

Backups of remote SMB/SFTP folders currently require temporary staging space
for the **complete remote dataset**. Ensure suitable free disk space under
`/var/lib/backuphub/staging` (or set `BACKUPHUB_STAGING` and adjust
systemd sandbox permissions). Once staged, Restic deduplicates and encrypts
on the persistent destination. Do not point source paths at BackupHub's own
state or repository. The first preview does not implement streaming remote
tree ingestion, cross-host immutable storage or retention.

SFTP host keys **must** be registered, e.g. via securely reviewed known_hosts
entries in `/etc/backuphub/known_hosts` with read permissions for backuphub.
Use no insecure known-hosts bypass. SMB2/3 connects using NTLMv2 with required
message signing; SMB1 is unsupported in this preview. PostgreSQL uses
pg_dump custom format and detects invalid dump TOC with pg_restore. It does
not yet support split-config backups for Postgres Pro 1C.

Restoration always uses a **new** server-side directory under
`/var/lib/backuphub/restores` or exports ZIP64 to the browser. No in-place
overwrite, restore to SMB share or automatic AD/system-state restore is offered.
ZIP exports stream and do not require a second full copy.

Keep independent offline copies of `master.key` and `restic.key`; losing
them means losing access to credentials and encrypted backups.

Build release locally: `npm ci --prefix web && npm run build --prefix web &&
go build -o dist/backuphub ./cmd/backuphub &&
sh packaging/debian/build.sh`.
