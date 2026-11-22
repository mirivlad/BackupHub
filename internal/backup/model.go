// Package backup joins native source adapters, Restic tree snapshots and the
// existing crash-safe scheduler. It deliberately exposes no user scripts.
package backup

import (
	"context"
	"errors"
	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/scheduler"
	"github.com/mirivlad/BackupHub/internal/vault"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type CreateSource struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Host          string `json:"host"`
	Share         string `json:"share"`
	Path          string `json:"path"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	AuthMethod    string `json:"auth_method,omitempty"`
	PrivateKey    string `json:"private_key,omitempty"`
	KeyPassphrase string `json:"key_passphrase,omitempty"`
	Database      string `json:"database"`
}
type Settings struct {
	Host, Share, Path, Username, Database, AuthMethod string
	LocalMount                                        string `json:",omitempty"`
}
type Source struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	Host               string `json:"host"`
	Share              string `json:"share"`
	Path               string `json:"path"`
	Username           string `json:"username"`
	Database           string `json:"database"`
	AccessStatus       string `json:"access_status,omitempty"`
	AccessHint         string `json:"access_hint,omitempty"`
	AuthMethod         string `json:"auth_method,omitempty"`
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
}
type Snapshot struct {
	RepositoryID   string      `json:"repository_id"`
	ID             string      `json:"id"`
	SourceID       string      `json:"source_id"`
	SourceName     string      `json:"source_name"`
	Kind           string      `json:"kind"`
	ResticID       string      `json:"-"`
	Root           string      `json:"-"`
	At             string      `json:"created_at"`
	Verification   string      `json:"verification"`
	Fidelity       string      `json:"fidelity"`
	Consistency    string      `json:"consistency"`
	DurationMS     *int64      `json:"duration_ms,omitempty"`
	RestoreCheck   CheckStatus `json:"restore_check"`
	CanTestRestore bool        `json:"can_test_restore"`
}
type File struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     uint64    `json:"size"`
	Modified time.Time `json:"modified"`
}
type Service struct {
	catalog          *catalog.Catalog
	vault            *vault.Vault
	repo             *repository
	repoMu           sync.Mutex
	repositoryWork   sync.Map
	scheduler        *scheduler.Scheduler
	stageDir         string
	wake             chan struct{}
	verificationWake chan struct{}
	stopped          chan struct{}
	once             sync.Once
	startErr         error
	timeouts         operationTimeouts
	stagingCleanup   sync.Mutex
}

func New(ctx context.Context, c *catalog.Catalog, v *vault.Vault, repoPath, stageDir string) (*Service, error) {
	timeouts, err := readOperationTimeouts()
	if err != nil {
		return nil, err
	}
	if stageDir == "" {
		stageDir = filepath.Join(c.DataDir, "staging")
	}
	if !filepath.IsAbs(stageDir) {
		return nil, errors.New("staging path must be absolute")
	}
	if err := os.MkdirAll(stageDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(stageDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("staging directory must be private and real")
	}
	var stored string
	if err = c.DB.QueryRowContext(ctx, "SELECT path FROM storage_repositories WHERE id=?", PrimaryStorageID).Scan(&stored); err != nil {
		return nil, err
	}
	var repo *repository
	if stored == "" {
		repo, err = initRepository(ctx, c.DataDir, repoPath)
		if err != nil {
			return nil, err
		}
		if err = ensurePrimaryStorage(ctx, c, repo.path); err != nil {
			return nil, err
		}
	} else {
		if repoPath != "" && filepath.Clean(repoPath) != stored {
			return nil, errors.New("configured primary repository differs from catalog; move data using explicit migration")
		}
		// An established primary can be offline while other repositories
		// and the catalog remain available. Never initialize it again.
		binary, err := exec.LookPath("restic")
		if err != nil {
			return nil, err
		}
		repo = &repository{binary: binary, path: stored, passwordFile: filepath.Join(c.DataDir, "restic.key"), passwordMode: PasswordMode}
	}

	svc := &Service{
		catalog: c, vault: v, repo: repo, stageDir: stageDir, timeouts: timeouts,
		wake: make(chan struct{}, 1), verificationWake: make(chan struct{}, 1), stopped: make(chan struct{}),
	}
	svc.scheduler = scheduler.New(c, svc)
	return svc, nil
}

// Start is called once at service startup, not once per request. Jobs already
// running when the service died are marked interrupted, then made eligible.
func (s *Service) Start(ctx context.Context) error {
	s.once.Do(func() {
		lease, err := s.acquireServiceLease()
		if err != nil {
			s.startErr = err
			close(s.stopped)
			return
		}
		_, err = s.scheduler.RecoverInterrupted(ctx)
		if err == nil {
			err = s.recoverVerification(ctx)
		}
		if err != nil {
			s.startErr = err
			_ = lease.Close()
			close(s.stopped)
			return
		}
		if err := s.cleanSnapshotIndexes(); err != nil {
			slog.Warn("cannot remove old snapshot indexes", "error", err)
		}
		ctx = context.WithValue(ctx, serviceLeaseKey{}, lease)
		go func() {
			defer close(s.stopped)
			defer lease.Close()
			var workers sync.WaitGroup
			workers.Go(func() { s.runVerification(ctx) })
			workers.Go(func() {
				if err := s.cleanInterruptedStaging(ctx, ""); err != nil && ctx.Err() == nil {
					slog.Warn("cannot clean interrupted staging", "error", err)
				}
			})
			s.dispatchBackups(ctx)
			workers.Wait()
		}()
	})
	return s.startErr
}

// WaitStopped waits for backup and verification work to stop.
func (s *Service) WaitStopped() { <-s.stopped }

func safeFailure(err error) string {
	if errors.Is(err, scheduler.ErrSSHHostKeyChanged) {
		return "ssh_host_key_changed"
	}
	if errors.Is(err, scheduler.ErrSourcePermissionDenied) {
		return "source_permission_denied"
	}
	var failure *scheduler.BackupFailure
	if errors.As(err, &failure) {
		return failure.Code
	}
	if errors.Is(err, scheduler.ErrSourceUnavailable) {
		return "source_unavailable"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "backup_failed"
}
func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) watchOperation(ctx context.Context) (context.Context, func()) {
	if s.timeouts.idle != 0 {
		ctx = context.WithValue(ctx, operationOptionsKey{}, s.timeouts)
	}
	return watchProgress(ctx)
}

func (s *Service) dispatchBackups(ctx context.Context) {
	const parallelBackups = 2
	finished := make(chan struct{}, parallelBackups)
	var workers sync.WaitGroup
	defer workers.Wait()
	active := 0
	for ctx.Err() == nil {
		cutoff := time.Now()
		recheck := s.scheduler.FlushFinishes(ctx)
		for active < parallelBackups && ctx.Err() == nil {
			run, err := s.scheduler.ClaimDue(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("cannot claim backup attempt", "error", err)
				recheck = true
			}
			if run == nil {
				break
			}
			active++
			workers.Go(func() {
				defer func() { finished <- struct{}{} }()
				if err := run(); err != nil && ctx.Err() == nil {
					slog.Warn("scheduled run failed", "error", safeFailure(err))
				}
			})
		}
		var wakeAt *time.Time
		if active < parallelBackups {
			next, err := s.scheduler.NextWake(ctx, cutoff)
			if err != nil && ctx.Err() == nil {
				slog.Error("cannot read backup queue deadline", "error", err)
				recheck = true
			}
			wakeAt = next
		}
		if recheck {
			soon := time.Now().Add(time.Second)
			if wakeAt == nil || soon.Before(*wakeAt) {
				wakeAt = &soon
			}
		}
		var timer *time.Timer
		var deadline <-chan time.Time
		if wakeAt != nil {
			timer = time.NewTimer(time.Until(*wakeAt))
			deadline = timer.C
		}
		select {
		case <-ctx.Done():
		case <-finished:
			active--
		case <-s.wake:
		case <-deadline:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
