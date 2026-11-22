# Scheduler: durable execution ledger

BackupHub starts the scheduler with the service. Each backup job owns its interval or cron configuration, first occurrence and time zone. Manual requests queue a run and preserve an upcoming planned occurrence.

## Model

- `schedule_entries` — typed jobs `backup`, `verify`, `replicate`, a dataset, enabled flag, interval (60 seconds to one year), next due UTC timestamp and an active run ID.
- `schedule_runs` — durable per-run ID, phase, start/finish times, non-secret error code.
- `Scheduler.Add(ctx,Task)` — explicit interval schedule. No arbitrary Bash/PowerShell job type.
- `Scheduler.RunDue(ctx)` — atomically claims at most one due task, then invokes a registered, trusted Go executor. A second poll of that task cannot overlap.
- `Executor.Execute(ctx,task,report)` — reports legal domain job-phase transitions. Return success is accepted **only after** `verifying`; skipping phases cannot produce a false green job.
- `RecoverInterrupted(ctx)` — marks unfinalized work `interrupted`, unlocks schedules and allows one catch-up execution. It does not replay every missed interval at boot.
- `RecentRuns` — paged read-only history, no plaintext passwords or raw application output.

When preflight returns `ErrSourceUnavailable` during a planned run, a task records `waiting_for_source` and retries after the smaller of its normal interval and five minutes. A manual request preserves a planned occurrence that is still in the future. Other failures remain failed with a later scheduled opportunity.

## Crash and concurrency guarantees

The due-task claim and new-run creation are one SQLite transaction; the active run ID prevents overlap even when polls race. Phase changes are persisted before continuing. The final state and next run are committed together. On restart, an explicit `RecoverInterrupted` reconciles abandoned active runs and avoids false successes.

The service processes one backup at a time. Each backup reads its new snapshot before publishing it. Repository-wide checks use an independent queue and reserve only the target repository. Repository admission occurs before the transaction claims a due backup. An occupied repository leaves its due time and manual flag intact. The scheduler can claim a job for a different repository, and repository release wakes the backup dispatcher. The existing task transaction and repository admission gate also support multiple workers bounded by a future dispatcher. Production currently runs one backup worker. See the [verification runbook](verification.md). Each catalog supports one running instance. Recovered state pauses restored schedules until the operator enables them.

## Tests

Tests cover successful phases and history, two concurrent polls against the same due job, interrupted-run recovery, offline sources, errors and attempted success without required phases. SQLite migration v2→v3 is exercised with repository CI. No test uses a fake executor to claim an actual backup was performed.
