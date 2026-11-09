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
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Host            string `json:"host"`
	Share           string `json:"share"`
	Path            string `json:"path"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	Database        string `json:"database"`
	IntervalMinutes int    `json:"interval_minutes"`
	RepositoryID string `json:"repository_id"`
}
type Settings struct{ Host, Share, Path, Username, Database string }
type Source struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Host            string `json:"host"`
	Share           string `json:"share"`
	Path            string `json:"path"`
	Username        string `json:"username"`
	Database        string `json:"database"`
	IntervalMinutes int    `json:"interval_minutes"`
	RepositoryID string `json:"repository_id"`
	NextRun         string `json:"next_run,omitempty"`
	Enabled         bool   `json:"enabled"`
}
type Snapshot struct {
	RepositoryID string `json:"repository_id"`
	ID           string `json:"id"`
	SourceID     string `json:"source_id"`
	SourceName   string `json:"source_name"`
	Kind         string `json:"kind"`
	ResticID     string `json:"-"`
	Root         string `json:"-"`
	At           string `json:"created_at"`
	Verification string `json:"verification"`
	Fidelity     string `json:"fidelity"`
	Consistency  string `json:"consistency"`
}
type File struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     uint64    `json:"size"`
	Modified time.Time `json:"modified"`
}
type Service struct {
	catalog   *catalog.Catalog
	vault     *vault.Vault
	repo      *repository
	repoMu sync.Mutex
	scheduler *scheduler.Scheduler
	stageDir  string
	wake      chan struct{}
	once      sync.Once
}

func New(ctx context.Context, c *catalog.Catalog, v *vault.Vault, repoPath, stageDir string) (*Service, error) {
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
	// A newly restored catalog may refer to the old primary disk, which
 // can be physically gone. Never create a replacement Restic repository
 // using an existing recovery key: start in degraded recovery mode instead.
 var storedPrimary string
 if err:=c.DB.QueryRowContext(ctx,"SELECT path FROM storage_repositories WHERE id='primary'").Scan(&storedPrimary);err!=nil{return nil,err}
 if storedPrimary!=""{
  if repoPath!=""&&filepath.Clean(repoPath)!=storedPrimary{return nil,errors.New("configured primary repository differs from recovery catalog")}
  repoPath=storedPrimary
 }
 if repoPath==""{repoPath=filepath.Join(c.DataDir,"repository")}
 missingPrimary:=false
 if storedPrimary!="" {
  if _,err:=os.Lstat(filepath.Join(repoPath,"config"));os.IsNotExist(err){missingPrimary=true}else if err!=nil{return nil,err}
 }
 var repo *repository
 if missingPrimary {
  key:=filepath.Join(c.DataDir,"restic.key")
  keyInfo,err:=os.Lstat(key)
  if err!=nil||!keyInfo.Mode().IsRegular()||keyInfo.Mode().Perm()&0077!=0{return nil,errors.New("primary Restic key unavailable during recovery")}
  binary,err:=exec.LookPath("restic");if err!=nil{return nil,err}
  repo=&repository{binary:binary,path:repoPath,passwordFile:key,passwordMode:PasswordMode}
  slog.Warn("primary repository missing; backup server started in degraded recovery mode","path",repoPath)
 }else{
  var err error
  repo,err=initRepository(ctx,c.DataDir,repoPath)
  if err!=nil{return nil,err}
 }
 if err:=ensurePrimaryStorage(ctx,c,repo.path);err!=nil{return nil,err}
 svc := &Service{catalog: c, vault: v, repo: repo, stageDir: stageDir, wake: make(chan struct{}, 1)}
	svc.scheduler = scheduler.New(c, svc)
	return svc, nil
}

// Start is called once at service startup, not once per request. Jobs already
// running when the service died are marked interrupted, then made eligible.
func (s *Service) Start(ctx context.Context) error {
	var startErr error
	s.once.Do(func() {
		_, startErr = s.scheduler.RecoverInterrupted(ctx)
		if startErr != nil {
			return
		}
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				for {
					did, err := s.scheduler.RunDue(ctx)
					if err != nil && ctx.Err() == nil {
						slog.Warn("scheduled run failed", "error", safeFailure(err))
					}
					if err != nil || !did {
						break
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				case <-s.wake:
				}
			}
		}()
	})
	return startErr
}
func safeFailure(err error) string {
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
