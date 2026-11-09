# BackupHub Debian package (v0.1.0 Preview 2)

`sudo apt install ./backuphub_0.1.0~preview.2_amd64.deb` creates the
dedicated unprivileged system user `backuphub`, initializes state under
`/var/lib/backuphub`, installs the Go daemon, Svelte UI, an HTTPS service and
a **separate** root-owned socket-activated `backuphub-storage` helper. Neither
reinstallation nor package removal deletes backup data or encryption keys.

When the package installer is connected to a TTY, it may offer the option to
set the initial admin username/password interactively without exposing the
password on argv or in debconf. It never blocks a noninteractive deployment.
The same safe setup is available later:

```bash
sudo runuser -u backuphub -- backuphub setup-admin -data-dir /var/lib/backuphub
```

Alternatively, use `backuphub bootstrap` and create the first administrator
in the browser. Then browse `https://backup-host:8443/`. The initial
certificate is self-signed; replace it with trusted TLS or a well-configured
Nginx reverse proxy.

## Storage configuration

**No manual systemd overrides should be required for normal mounted volumes.**
In the admin UI, open Storage, choose an existing mounted volume under
`/mnt`, `/media`, `/backup`, `/backup2` or `/srv/backups`. Browse its
directories, supply a **new subdirectory name** and a friendly storage name.
The root-only helper creates just that new directory and grants `backuphub`
access to it; BackupHub initializes a separately encrypted Restic repository.

Select the destination repository in each backup job. Different jobs can use
different disks. Switching a job only affects future backups; prior restore
points stay bound to the original repository. If an external mount disappears,
new backups fail closed instead of silently filling the system disk.

The built-in legacy repository under `/var/lib/backuphub/repository` remains
available to maintain older deployments. If `BACKUPHUB_REPOSITORY` was set
before, keep that setting unchanged: changing it is *not* an online migration.
Use the new Storage UI to add additional repositories. For complex/multiple
NAS mounts, each backing filesystem must support private directories owned
by the `backuphub` service account.

The root storage-helper has no TCP listener and only operates through the
root:backuphub 0660 Unix socket `/run/backuphub-storage.sock`. It cannot
delete paths, modify ownership of existing directories or follow symlinks
while browsing. Only admin sessions may invoke its backend API.

**Back up ALL keys**: `/var/lib/backuphub/master.key`,
`/var/lib/backuphub/restic.key`, each `restic-<id>.key` plus the SQLite
catalog and the off-host repository data. Keep offline copies. Losing a key
makes the corresponding encrypted data unrecoverable.

## Source limitations

SMB2/3 and SFTP captures stage the **entire remote dataset** in
`/var/lib/backuphub/staging` before Restic can deduplicate and encrypt it.
Ensure enough temporary space. Do not select BackupHub's own state/repository
as a source. SFTP host key verification requires a trusted
`/etc/backuphub/known_hosts`. SMB1 is unsupported. PostgreSQL uses custom
`pg_dump` with TOC validation only; native split-config for PostgreSQL Pro 1C
is **not** included.

Restoration is limited to a **new** server-side directory or ZIP download,
not overwriting live data. There is no automatic protected retention, external
immutable replication, 1C restore, Windows VSS/AD or mailbox connector yet.

See [detailed storage guide](../../docs/multi-storage.md).

Build locally:
```bash
npm ci --prefix web
npm run build --prefix web
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/backuphub ./cmd/backuphub
sh packaging/debian/build.sh
```
