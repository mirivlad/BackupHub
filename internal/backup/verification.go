package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mirivlad/BackupHub/internal/scheduler"
)

const verificationHistoryLimit = 50
const defaultCheckFreshness = 30 * 24 * time.Hour

var ErrVerificationBusy = errors.New("для этого хранилища уже есть активная проверка")
var ErrRestoreCheckUnsupported = errors.New("пробное восстановление доступно только для файловых снимков")

type VerificationRun struct {
	ID           string     `json:"id"`
	RepositoryID string     `json:"repository_id"`
	SnapshotID   string     `json:"snapshot_id,omitempty"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	QueuedAt     time.Time  `json:"queued_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	DurationMS   *int64     `json:"duration_ms,omitempty"`
	Selection    string     `json:"selection,omitempty"`
	Detail       string     `json:"detail,omitempty"`
	Progress     *float64   `json:"progress,omitempty"`
	Files        *int64     `json:"files,omitempty"`
	Bytes        *int64     `json:"bytes,omitempty"`
}

type CheckSchedule struct {
	Mode          string     `json:"mode"`
	IntervalHours int        `json:"interval_hours"`
	NextRun       *time.Time `json:"next_run,omitempty"`
}

type CheckStatus struct {
	State      string     `json:"state"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type StorageChecks struct {
	Schedule CheckSchedule     `json:"schedule"`
	Runs     []VerificationRun `json:"runs"`
}

const verificationColumns = `id,repository_id,COALESCE(snapshot_id,''),kind,state,queued_at,started_at,finished_at,detail,progress,files,bytes,selection`

func scanVerification(row interface{ Scan(...any) error }) (VerificationRun, error) {
	var run VerificationRun
	var queued int64
	var started, finished sql.NullInt64
	if err := row.Scan(&run.ID, &run.RepositoryID, &run.SnapshotID, &run.Kind, &run.State, &queued,
		&started, &finished, &run.Detail, &run.Progress, &run.Files, &run.Bytes, &run.Selection); err != nil {
		return run, err
	}
	run.QueuedAt = time.UnixMilli(queued).UTC()
	if started.Valid {
		at := time.UnixMilli(started.Int64).UTC()
		run.StartedAt = &at
	}
	if finished.Valid {
		at := time.UnixMilli(finished.Int64).UTC()
		run.FinishedAt = &at
		if started.Valid {
			elapsed := max(int64(0), finished.Int64-started.Int64)
			run.DurationMS = &elapsed
		}
	}
	return run, nil
}

func (s *Service) verificationHistory(ctx context.Context, condition, id string) ([]VerificationRun, error) {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT `+verificationColumns+` FROM verification_runs WHERE `+condition+
		` ORDER BY queued_at DESC,id DESC LIMIT ?`, id, verificationHistoryLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []VerificationRun{}
	for rows.Next() {
		run, err := scanVerification(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Service) StorageChecks(ctx context.Context, repoID string) (StorageChecks, error) {
	checks := StorageChecks{}
	var next sql.NullInt64
	if err := s.catalog.DB.QueryRowContext(ctx, `SELECT COALESCE(c.mode,'metadata'),COALESCE(c.interval_hours,0),c.next_run
	 FROM storage_repositories s LEFT JOIN repository_check_schedules c ON c.repository_id=s.id WHERE s.id=?`, repoID).
		Scan(&checks.Schedule.Mode, &checks.Schedule.IntervalHours, &next); err != nil {
		return checks, err
	}
	if next.Valid {
		at := time.UnixMilli(next.Int64).UTC()
		checks.Schedule.NextRun = &at
	}
	var err error
	checks.Runs, err = s.verificationHistory(ctx, "repository_id=? AND snapshot_id IS NULL", repoID)
	return checks, err
}

func (s *Service) RestoreChecks(ctx context.Context, id string) ([]VerificationRun, error) {
	if _, err := s.Snapshot(ctx, id); err != nil {
		return nil, err
	}
	return s.verificationHistory(ctx, "snapshot_id=?", id)
}

func checkStatus(state string, finished sql.NullInt64, freshness time.Duration, now time.Time) CheckStatus {
	if state == "" {
		return CheckStatus{State: "unknown"}
	}
	status := CheckStatus{State: state}
	if finished.Valid {
		at := time.UnixMilli(finished.Int64).UTC()
		status.FinishedAt = &at
		if state == "passed" && now.Sub(at) > freshness {
			status.State = "stale"
		}
	}
	return status
}

func repositoryCheckMode(mode string) bool { return mode == "metadata" || mode == "data" }

func (s *Service) SetCheckSchedule(ctx context.Context, repoID, mode string, hours int) (CheckSchedule, error) {
	var result CheckSchedule
	if !repositoryCheckMode(mode) || hours < 0 || hours > 8760 {
		return result, errors.New("выберите режим проверки и период от 0 до 8760 часов")
	}
	result.Mode, result.IntervalHours = mode, hours
	var next any
	if hours > 0 {
		at := time.Now().UTC().Add(time.Duration(hours) * time.Hour).Truncate(time.Millisecond)
		result.NextRun, next = &at, at.UnixMilli()
	}
	row, err := s.catalog.DB.ExecContext(ctx, `INSERT INTO repository_check_schedules(repository_id,mode,interval_hours,next_run)
	 SELECT id,?,?,? FROM storage_repositories WHERE id=?
	 ON CONFLICT(repository_id) DO UPDATE SET mode=excluded.mode,interval_hours=excluded.interval_hours,next_run=excluded.next_run`,
		mode, hours, next, repoID)
	if err != nil {
		return CheckSchedule{}, err
	}
	if n, err := row.RowsAffected(); err != nil || n == 0 {
		return CheckSchedule{}, errors.Join(sql.ErrNoRows, err)
	}
	s.notifyVerification()
	return result, nil
}

func (s *Service) QueueRepositoryCheck(ctx context.Context, repoID, mode string) (VerificationRun, error) {
	if !repositoryCheckMode(mode) {
		return VerificationRun{}, errors.New("неизвестный режим проверки хранилища")
	}
	return s.queueVerification(ctx, repoID, "", mode, "")
}

func (s *Service) QueueRestoreCheck(ctx context.Context, id, selection string) (VerificationRun, error) {
	snap, err := s.Snapshot(ctx, id)
	if err != nil {
		return VerificationRun{}, err
	}
	if !snap.CanTestRestore {
		return VerificationRun{}, ErrRestoreCheckUnsupported
	}
	if selection != "" && (!validRestoreCheckPath(selection) || len(selection) > 4096) {
		return VerificationRun{}, ErrRestoreCheckPath
	}
	return s.queueVerification(ctx, snap.RepositoryID, id, "restore", selection)
}

func (s *Service) queueVerification(ctx context.Context, repoID, snapshotID, kind, selection string) (VerificationRun, error) {
	id, err := randomID()
	if err != nil {
		return VerificationRun{}, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	row, err := s.catalog.DB.ExecContext(ctx, `INSERT INTO verification_runs(id,repository_id,snapshot_id,kind,state,queued_at,selection)
	 SELECT ?,id,NULLIF(?,''),?,'queued',?,? FROM storage_repositories WHERE id=?
	 ON CONFLICT(repository_id) WHERE state IN ('queued','running') DO NOTHING`, id, snapshotID, kind, now.UnixMilli(), selection, repoID)
	if err != nil {
		return VerificationRun{}, err
	}
	n, err := row.RowsAffected()
	if err != nil {
		return VerificationRun{}, err
	}
	if n == 0 {
		var exists bool
		if err = s.catalog.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM storage_repositories WHERE id=?)`, repoID).Scan(&exists); err != nil {
			return VerificationRun{}, err
		}
		if !exists {
			return VerificationRun{}, sql.ErrNoRows
		}
		return VerificationRun{}, ErrVerificationBusy
	}
	s.notifyVerification()
	return VerificationRun{ID: id, RepositoryID: repoID, SnapshotID: snapshotID, Kind: kind, Selection: selection, QueuedAt: now, State: "queued"}, nil
}

func (s *Service) notifyVerification() {
	select {
	case s.verificationWake <- struct{}{}:
	default:
	}
}

func (s *Service) TryReserve(task scheduler.Task) (func(), bool) {
	return s.tryRepositoryWork(task.RepositoryID)
}

func (s *Service) tryRepositoryWork(id string) (func(), bool) {
	if _, held := s.repositoryWork.LoadOrStore(id, struct{}{}); held {
		return nil, false
	}
	return func() {
		s.repositoryWork.Delete(id)
		s.notify()
		s.notifyVerification()
	}, true
}

func (s *Service) acquireServiceLease() (*os.File, error) {
	path := filepath.Join(s.catalog.DataDir, "service.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		err = errors.New("service lock must be a private regular file")
	}
	if err == nil {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot acquire the BackupHub service lock: %w", err)
	}
	return f, nil
}

func (s *Service) recoverVerification(ctx context.Context) error {
	if _, err := s.catalog.DB.ExecContext(ctx, `UPDATE verification_runs SET state='interrupted',finished_at=?,
	 detail='Служба прервала проверку при перезапуске.' WHERE state='running'`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if err := s.cleanRestoreChecks(); err != nil {
		slog.Warn("cannot remove old restore check workspaces", "error", err)
	}
	return nil
}

// One transaction queues one occurrence per eligible repository and advances
// its own schedule. A missed occurrence coalesces into the next check.
func (s *Service) enqueueDueChecks(ctx context.Context) error {
	now := time.Now().UnixMilli()
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `INSERT INTO verification_runs(id,repository_id,kind,state,queued_at)
	 SELECT lower(hex(randomblob(16))),c.repository_id,c.mode,'queued',?
	 FROM repository_check_schedules c WHERE c.next_run<=? AND c.interval_hours>0
	 AND NOT EXISTS(SELECT 1 FROM verification_runs r WHERE r.repository_id=c.repository_id AND r.state IN ('queued','running'))
	 RETURNING repository_id`, now, now)
	if err != nil {
		return err
	}
	var repositories []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		repositories = append(repositories, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range repositories {
		if _, err = tx.ExecContext(ctx, `UPDATE repository_check_schedules SET next_run=?+interval_hours*3600000
		 WHERE repository_id=?`, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) queuedChecks(ctx context.Context) ([]VerificationRun, error) {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT `+verificationColumns+` FROM verification_runs
	 WHERE state='queued' AND NOT EXISTS (
	  SELECT 1 FROM schedule_repositories dest JOIN schedule_entries task ON task.id=dest.schedule_id
	  WHERE dest.repository_id=verification_runs.repository_id AND task.enabled=1 AND task.active_run_id IS NULL
	  AND (
	   EXISTS(SELECT 1 FROM backup_operations o WHERE o.schedule_id=task.id AND o.state='waiting' AND o.ready_unix<=?)
	   OR ((task.manual_pending=1 OR task.next_run_unix<=?) AND NOT EXISTS(
	     SELECT 1 FROM backup_operations o WHERE o.schedule_id=task.id AND o.state IN ('running','waiting')))
	  )
	 ) ORDER BY queued_at,id`, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var queued []VerificationRun
	for rows.Next() {
		run, err := scanVerification(rows)
		if err != nil {
			return nil, err
		}
		queued = append(queued, run)
	}
	return queued, rows.Err()
}

func (s *Service) runVerification(ctx context.Context) {
	const parallelChecks = 2
	finished := make(chan string, parallelChecks)
	var workers sync.WaitGroup
	defer workers.Wait()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	active, restoring := 0, false
	for ctx.Err() == nil {
		if err := s.enqueueDueChecks(ctx); err != nil && ctx.Err() == nil {
			slog.Error("cannot schedule repository checks", "error", err)
		}
		if active < parallelChecks {
			queued, err := s.queuedChecks(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("cannot read verification queue", "error", err)
			}
			for _, run := range queued {
				if active == parallelChecks || ctx.Err() != nil {
					break
				}
				if restoring && run.Kind == "restore" {
					continue
				}
				release, ok := s.tryRepositoryWork(run.RepositoryID)
				if !ok {
					continue
				}
				active++
				restoring = restoring || run.Kind == "restore"
				workers.Go(func() {
					defer func() { finished <- run.Kind }()
					defer release()
					s.executeVerification(ctx, run)
				})
			}
		}
		select {
		case <-ctx.Done():
			return
		case kind := <-finished:
			active--
			if kind == "restore" {
				restoring = false
			}
		case <-ticker.C:
		case <-s.verificationWake:
		}
	}
}

func (s *Service) executeVerification(parent context.Context, run VerificationRun) {
	started := time.Now()
	result, err := s.catalog.DB.ExecContext(parent, `UPDATE verification_runs SET state='running',started_at=? WHERE id=? AND state='queued'`, started.UnixMilli(), run.ID)
	if err != nil {
		if parent.Err() == nil {
			slog.Error("cannot start verification", "run_id", run.ID, "error", err)
		}
		return
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return
	}
	timeout := 30 * time.Minute
	switch run.Kind {
	case "data":
		timeout = 24 * time.Hour
	case "restore":
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	progress := func(fraction float64) {
		if _, err := s.catalog.DB.ExecContext(ctx, `UPDATE verification_runs SET progress=? WHERE id=? AND state='running'`, fraction, run.ID); err != nil && ctx.Err() == nil {
			slog.Warn("cannot save verification progress", "run_id", run.ID, "error", err)
		}
	}

	repo, err := s.repositoryFor(ctx, run.RepositoryID)
	var restoredFiles, restoredBytes *int64
	detail := "Restic завершил проверку без ошибок."
	if err == nil && run.Kind == "restore" {
		var files, bytes int64
		files, bytes, err = s.runRestoreCheck(ctx, repo, run, progress)
		if err == nil {
			restoredFiles, restoredBytes = &files, &bytes
			detail = "Размеры и SHA-256 файлов автоматической выборки совпали."
			if run.Selection != "" {
				detail = "Размеры и SHA-256 обычных файлов по выбранному пути совпали."
			}
		}
	} else if err == nil {
		err = repo.checkWithProgress(ctx, run.Kind == "data", progress)
	} else {
		err = fmt.Errorf("хранилище недоступно: %w", err)
	}
	state := "passed"
	if err = resticLockError(err); err != nil {
		state, detail = "failed", err.Error()
		switch {
		case parent.Err() != nil:
			state, detail = "interrupted", "Служба остановлена во время проверки."
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			detail = "Превышено время проверки. " + detail
		case errors.Is(err, errResticRepositoryLocked):
			detail = "Репозиторий заблокирован. Проверьте выполняющиеся процессы Restic. " + detail
		}
	}
	const diagnosticLimit = 6144
	detail = strings.ToValidUTF8(detail[:min(len(detail), diagnosticLimit)], "�")
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finishCancel()
	if _, err = s.catalog.DB.ExecContext(finishCtx, `UPDATE verification_runs SET state=?,finished_at=?,detail=?,files=?,bytes=?
	 WHERE id=? AND state='running'`, state, time.Now().UnixMilli(), detail, restoredFiles, restoredBytes, run.ID); err != nil {
		slog.Error("cannot save verification result", "run_id", run.ID, "error", err)
		return
	}
	slog.Info("BackupHub verification finished", "run_id", run.ID, "repository_id", run.RepositoryID, "kind", run.Kind, "state", state)
}
