# Restic adapter: portable snapshots (Phase A)

**Development component; not yet a production backup engine.** This is a native Go adapter to Restic, **not** a wrapper for arbitrary shell backup scripts. No server API exposes repository creation or modification yet.

## Format v1

Each `Transaction.Put(name, stream)` stores one provisional encrypted Restic snapshot via `restic backup --stdin` and records a SHA-256 checksum and object size. On `Commit`, all components in a validated application manifest must match the captured objects. Only after this does BackupHub write `backuphub-manifest.json` as a separate, tagged Restic snapshot.

The manifest includes the application snapshot identity, consistency and metadata-fidelity declarations, verification state, optional restore recipe, required component list, SHA-256 digests, sizes, and Restic snapshot IDs. Every snapshot is logically identified by its stable BackupHub ID, not a SQLite row or an internal Restic UUID.

**Only the manifest tag `bh-commit-<BackupHub snapshot ID>` constitutes a published BackupHub checkpoint.** Staging objects use `bh-pending` and remain **unpublished** if the operation fails or the process crashes. The current adapter intentionally does not delete abandoned objects. A future privileged, reference-aware garbage collector must guard against deleting a component used by any committed manifest. **Do not run restic forget/prune against such repositories using ad hoc tag policies.**

This format is an interim testable v1. It must be signed off for compatibility and disaster recovery before declaring P0 ready.

## Core API

- `New(Config)`: absolute local repo path, absolute private (0600) password file, Restic binary.
- `Init(ctx)`: explicit creation of a new encrypted repository, never silent.
- `Begin(ctx, datasetID)`, `Put`, `Commit`, `Abort`: streaming content, atomic publication marker, fail-closed incomplete sets.
- `ReadManifest(ctx,id)`, `Read(ctx,id,component)`, `Verify(ctx,id)`: restore by ID and compare SHA-256/size.
- `ListCommitted(ctx)`: reconstruct logical catalog from portable manifests with no SQLite or original daemon.
- `Check(ctx,true)`: Restic full data verification; this is distinct from a verified application restore.

The adapter uses fixed `exec.CommandContext` arguments and **no shell**, bounded diagnostic capture, explicit password-file ownership permissions, local repositories only, and no inherited `RESTIC_*` environment overrides. It passes no raw passwords on CLI argv.

## Restic-specific caveats

Restic returns exit **3** when a backup is partial (some files unreadable). BackupHub treats any nonzero status as failure, never publication. Restic `check` alone does not prove application restoration; the separate `Verify` method currently checks only component bytes, not a test application's startup.

Per-component `--stdin` snapshots do not preserve full file trees, filesystem ACLs or meaningful directory metadata. They are designed for capturing **logical application objects** through the connector/restore strategy abstraction. A future native file-tree adapter must use Restic's file-tree backup capabilities (or an equivalent storage mechanism), without repacking entire shares into single large archives. The current adapter must not be used to promise faithful SMB/SSH file restores.

## Integration tests

GitHub CI installs the Restic executable and runs an actual isolated local repository test: init → store components → refuse incomplete commit → commit manifest → `restic check --read-data` → discover without SQLite → verify SHA-256 → restore exact bytes. A separate aborted transaction test proves it cannot appear as a committed BackupHub snapshot.

No repository lifecycle API or retention executor is exposed while object reference tracking and privilege separation remain incomplete.
