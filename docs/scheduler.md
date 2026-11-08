# Scheduler: durable execution ledger (Phase A)

**Status: tested orchestration library, not yet a running backup scheduler.** This Go module will be started from the BackupHub service when native connectors, permissions, repository registration and preflight are wired. No real backup job is silently created or run by the API now.

## Model

- `schedule_entries` — typed jobs `backup`, `verify`, `replicate`, a dataset, enabled flag, interval (60 seconds to one year), next due UTC timestamp and an active run ID.
- `schedule_runs` — durable per-run ID, phase, start/finish times, non-secret error code.
- `Scheduler.Add(ctx,Task)` — explicit interval schedule. No arbitrary Bash/PowerShell job type.
- `Scheduler.RunDue(ctx)` — atomically claims at most one due task, then invokes a registered, trusted Go executor. A second poll of that task cannot overlap.
- `Executor.Execute(ctx,task,report)` — reports legal domain job-phase transitions. Return success is accepted **only after** `verifying`; skipping phases cannot produce a false green job.
- `RecoverInterrupted(ctx)` — marks unfinalized work `interrupted`, unlocks schedules and allows one catch-up execution. It does not replay every missed interval at boot.
- `RecentRuns` — paged read-only history, no plaintext passwords or raw application output.

Backoff: when preflight returns `ErrSourceUnavailable`, a task records `waiting_for_source` and retries after the smaller of its normal interval and five minutes. Other failures remain failed with a later scheduled opportunity. A completed task advances to a future due date, with no automatic deletions of data.

## Crash and concurrency guarantees

The due-task claim and new-run creation are one SQLite transaction; the active run ID prevents overlap even when polls race. Phase changes are persisted before continuing. The final state and next run are committed together. On restart, an explicit `RecoverInterrupted` reconciles abandoned active runs and avoids false successes.

**Limitations:** this release slice supports interval schedules only, and only one running instance against each catalog is supported until a lease/fencing protocol is implemented. The engine is not automatically started in `backuphub serve`; all production connectors and external storage targets are still awaiting integration. Time zones, weekly/calendar scheduling, retry limits, bounded worker pools, multi-instance leases, and pause/resume controls remain outstanding.

## Tests

Tests cover successful phases and history, two concurrent polls against the same due job, interrupted-run recovery, offline sources, errors and attempted success without required phases. SQLite migration v2→v3 is exercised with repository CI. No test uses a fake executor to claim an actual backup was performed.
