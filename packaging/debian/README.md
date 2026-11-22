# BackupHub Debian package (v0.4.0)

`sudo apt install ./backuphub_0.4.0_amd64.deb` creates the
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

Create an [offline recovery kit](../../docs/recovery.md) to preserve the
SQLite catalog and all required keys. Store it separately from the
Restic repositories. The recovery runbook covers restoring fresh service
state and importing snapshots written after the catalog checkpoint.

## Source limitations

SMB and SFTP captures stage the **entire remote dataset** on the **selected backup volume** (under its private `staging/` folder) before Restic can deduplicate and encrypt it. The legacy `primary` storage stages under `/var/lib/backuphub/staging`.
Ensure enough temporary space. Do not select BackupHub's own state/repository
as a source. SSH/SFTP accepts password authentication and private keys in
OpenSSH or PuTTY PPK v2/v3 format. A passphrase can be supplied in the web interface.
The first SSH server public key is pinned automatically (TOFU) in SQLite and
displayed as an SHA-256 fingerprint. A changed server key blocks subsequent
connections. The first handshake is vulnerable to active MITM; administrators
should verify the displayed fingerprint using an independent channel.
Private keys and passphrases are encrypted using BackupHub's master key.

Local backups run under the unprivileged `backuphub` account with the
`CAP_DAC_READ_SEARCH` *read-only* capability to access root-owned and private
user folders. `ProtectHome=read-only` and `ProtectSystem=strict` prevent writes.
This capability can read sensitive local files: keep the admin account secure,
restrict access to HTTPS, and audit backup destinations.

SMB1 is a separate source type. Its Samba client runs without the daemon's
ambient capabilities and requires signing and NTLMv2. The Debian package
recommends `smbclient` for SMB1 and `putty-tools` for PPK import. Install
those packages explicitly if you install BackupHub without recommendations.

Ordinary PostgreSQL uses custom `pg_dump` with TOC validation. For Postgres Pro
for 1C, the [runbook](../../docs/postgres-1c.md) covers split-config capture and
restoration into a separate compatible database.

BackupHub restores snapshot files to a **new** server-side directory and
offers ZIP download. Protected retention, external immutable replication,
Windows VSS/AD and mailbox connectors are on the roadmap.

See [detailed storage guide](../../docs/multi-storage.md).

Build locally:
```bash
npm ci --prefix web
npm run build --prefix web
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/backuphub ./cmd/backuphub
sh packaging/debian/build.sh
```
