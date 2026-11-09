# BackupHub Debian package (v0.1.0 Preview 4)

`sudo apt install ./backuphub_0.1.0~preview.4_amd64.deb` creates the
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
access to it; BackupHub initializes a separate Restic repository using the password mode selected in the browser.

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

New repositories can use **password-protected** (default) or **passwordless**
Restic; choose the system-wide default on the separate admin Settings screen
or override the mode while adding a specific storage. Passwordless repositories
require Restic **0.17 or newer** and can be accessed with
`restic --repo /mnt/disk/BackupHub/repository --insecure-no-password snapshots`
without any BackupHub master/key/database. The user must consciously accept
that anyone with a full disk copy can read these backups. Restic still uses
its own block format and technically encrypts with the empty password;
this is NOT a normal directory tree. Existing repositories retain their
original protected mode without alteration.

**Back up ALL required keys**: `/var/lib/backuphub/master.key`,
`/var/lib/backuphub/restic.key`, each `restic-<id>.key` **for password-protected storages** plus the SQLite
catalog and off-host repository data. Passwordless storage has no
`restic-<id>.key`. Keep offline copies of needed keys. Losing one of
them renders the corresponding protected backup inaccessible.

## Disaster recovery (Preview 4)

The administrator can download a **Recovery Kit** from the dedicated
**Emergency Recovery** screen. It contains a consistent SQLite snapshot and
all keys needed for the registered protected repositories, so this ZIP is
highly sensitive: keep it secure and independent of the backup server.
It does **not** contain Restic repository data.

On a newly installed, **empty** BackupHub you can upload the saved ZIP in
the Recovery screen, confirm that it replaces the empty installation and let
systemd restart the service. A narrowly scoped root-only ExecStartPre applies
the checked kit using Linux atomic directory exchange, keeping the initial
empty installation in /var/lib/backuphub.before-recovery. Old sessions are
revoked and restored jobs remain paused. Log in using the OLD administrator
credentials. Mount the backup disks and select the recovered storage in the
browser to rebind it to a different directory; all historical snapshot IDs
must match.

Without the full kit, the same browser screen attaches an **existing**
mounted disk without initialization, optionally imports its original key,
and reconstructs the historical snapshot index from portable Restic commit
markers. It does **not** fabricate working source credentials or schedules.

To restore the full configuration, stop the service and run
`backuphub recovery-kit-restore -kit /path/kit.zip -data-dir /new/empty/path`;
the target must not yet exist. Replace the state directory only after
manual review, keep a rollback copy, reattach old repository disks and restart.
When the old primary disk is absent, the server runs in degraded recovery mode
without reinitializing a replacement repository.

See [disaster recovery guide](../../docs/disaster-recovery.md) for tested
steps, limitations and warnings.

## Source limitations

SMB2/3 and SFTP captures stage the **entire remote dataset** on the **selected backup volume** (under its private `staging/` folder) before Restic can deduplicate and encrypt it. The legacy `primary` storage stages under `/var/lib/backuphub/staging`.
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
