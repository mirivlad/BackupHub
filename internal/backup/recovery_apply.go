package backup

import (
 "context"
 "database/sql"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "strings"
 "time"

 "golang.org/x/sys/unix"
)

const (
 recoveryReadyDir=".recovery-kit-ready"
 recoveryRequestFile=".recovery-kit-apply.json"
 maxRecoveryUpload=256<<20 // 256 MiB of catalog and keys; Restic data is separate.
)
type preparedRecoveryRequest struct {
 Format string `json:"format"`
 InstanceID string `json:"instance_uuid"`
}

// A complete instance must never be replaced by a browser upload. Admin
// accounts/sessions on a new installation are permitted; sources, jobs,
// snapshots, external repositories and physical Restic snapshots are not.
func recoveryTargetIsEmpty(ctx context.Context,db *sql.DB)error{
 for _,statement:=range []string{
  "SELECT COUNT(*) FROM sources",
  "SELECT COUNT(*) FROM snapshots",
  "SELECT COUNT(*) FROM schedule_entries",
  "SELECT COUNT(*) FROM storage_repositories WHERE id<>'primary'",
  "SELECT COUNT(*) FROM vault_secrets",
 }{
  var count int
  if err:=db.QueryRowContext(ctx,statement).Scan(&count);err!=nil{return err}
  if count!=0{return errors.New("recovery is allowed only on a newly initialized BackupHub without existing data or jobs")}
 }
 return nil
}

func (s *Service) PrepareRecoveryKit(ctx context.Context,reader io.Reader)(RecoveryKitInfo,error){
 var zero RecoveryKitInfo
 s.repoMu.Lock()
 defer s.repoMu.Unlock()
 if err:=recoveryTargetIsEmpty(ctx,s.catalog.DB);err!=nil{return zero,err}
 // Never replace a standalone repository that already has a Restic history.
 entries,err:=s.repo.run(ctx,nil,"snapshots","--json")
 if err!=nil{return zero,fmt.Errorf("cannot verify empty primary repository: %w",err)}
 var snaps []json.RawMessage
 if err=json.Unmarshal(entries,&snaps);err!=nil{return zero,err}
 if len(snaps)!=0{return zero,errors.New("primary repository already contains snapshots; recovery upload is not permitted")}
 readyPath:=filepath.Join(s.catalog.DataDir,recoveryReadyDir)
 requestPath:=filepath.Join(s.catalog.DataDir,recoveryRequestFile)
 if _,err=os.Lstat(readyPath);err==nil{return zero,errors.New("a recovery upload is already staged")}else if !os.IsNotExist(err){return zero,err}
 if _,err=os.Lstat(requestPath);err==nil{return zero,errors.New("a recovery restart is already pending")}else if !os.IsNotExist(err){return zero,err}
 upload,err:=os.CreateTemp(s.catalog.DataDir,".recovery-upload-*.zip")
 if err!=nil{return zero,err}
 uploadPath:=upload.Name()
 defer os.Remove(uploadPath)
 amount,copyErr:=io.Copy(upload,io.LimitReader(reader,maxRecoveryUpload+1))
 syncErr:=upload.Sync()
 closeErr:=upload.Close()
 if copyErr!=nil{return zero,copyErr}
 if syncErr!=nil{return zero,syncErr}
 if closeErr!=nil{return zero,closeErr}
 if amount<=0||amount>maxRecoveryUpload{return zero,errors.New("recovery kit must be a non-empty ZIP not exceeding 256 MiB")}
 if err=RestoreRecoveryKit(ctx,uploadPath,readyPath);err!=nil{return zero,err}
 // Tokens from the former server must never authenticate on the recovered
 // instance. Backups are paused until the operator verifies mounted disks.
 if err=sanitizeRecoveredInstallation(ctx,readyPath);err!=nil{
  _=os.RemoveAll(readyPath)
  return zero,err
 }
 stageOK:=false
 defer func(){if !stageOK{_ =os.RemoveAll(readyPath)}}()
 info,err:=readPreparedRecovery(ctx,readyPath)
 if err!=nil{return zero,err}
 if info.InstanceID==s.catalog.InstanceID{return zero,errors.New("the recovery kit belongs to the current installation")}
 if err=recoveryTargetIsEmpty(ctx,s.catalog.DB);err!=nil{return zero,err}
 // Marker is written ONLY after successful extraction and validation.
 payload,_:=json.Marshal(preparedRecoveryRequest{Format:recoveryKitFormat,InstanceID:info.InstanceID})
 f,err:=os.OpenFile(requestPath,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600)
 if err!=nil{return zero,err}
 _,writeErr:=f.Write(append(payload,'\n'))
 fsyncErr:=f.Sync()
 closeErr=f.Close()
 if writeErr!=nil||fsyncErr!=nil||closeErr!=nil{
  _=os.Remove(requestPath)
  return zero,errors.New("unable to commit pending recovery")
 }
 stageOK=true
 return info,nil
}

// The backup catalog carries both user credentials and active sessions.
// Preserve accounts and jobs but invalidate old sessions and pause all jobs
// before bringing an unrelated recovered host online.
func sanitizeRecoveredInstallation(ctx context.Context,dir string)error{
 db,err:=sql.Open("sqlite",filepath.Join(dir,"catalog.db"))
 if err!=nil{return err}
 defer db.Close()
 if _,err=db.ExecContext(ctx,"PRAGMA busy_timeout=5000");err!=nil{return err}
 tx,err:=db.BeginTx(ctx,nil);if err!=nil{return err}
 defer tx.Rollback()
 for _,sqlText:=range []string{
  "DELETE FROM sessions",
  "DELETE FROM bootstrap",
  "UPDATE schedule_entries SET enabled=0",
 }{
  if _,err=tx.ExecContext(ctx,sqlText);err!=nil{return fmt.Errorf("cannot safely prepare restored catalog: %w",err)}
 }
 if err=tx.Commit();err!=nil{return err}
 // Flush any WAL changes into the main SQLite file before atomic swapping.
 if _,err=db.ExecContext(ctx,"PRAGMA wal_checkpoint(TRUNCATE)");err!=nil{return err}
 if err=db.Close();err!=nil{return err}
 for _,suffix:=range []string{"-wal","-shm"}{
  file:=filepath.Join(dir,"catalog.db"+suffix)
  if _,err:=os.Lstat(file);err==nil{
   if err=os.Remove(file);err!=nil{return err}
  }else if !os.IsNotExist(err){return err}
 }
 return nil
}

// No SQLite or Vault writes: safe for ExecStartPre root execution while the
// daemon is stopped. A staging directory can only have Recovery Kit files.
func readPreparedRecovery(ctx context.Context,dir string)(RecoveryKitInfo,error){
 var info RecoveryKitInfo
 stat,err:=os.Lstat(dir)
 if err!=nil{return info,err}
 if !stat.IsDir()||stat.Mode()&os.ModeSymlink!=0||stat.Mode().Perm()!=0700{return info,errors.New("unsafe staged recovery directory")}
 manifest,err:=os.ReadFile(filepath.Join(dir,"recovery-manifest.json"))
 if err!=nil{return info,err}
 if len(manifest)>16384{return info,errors.New("recovery manifest too large")}
 if err=json.Unmarshal(manifest,&info);err!=nil{return info,err}
 if info.Format!=recoveryKitFormat||info.InstanceID==""{return info,errors.New("invalid staged recovery manifest")}
 for _,file:=range []string{"catalog.db","master.key","restic.key","recovery-manifest.json"}{
  stat,err:=os.Lstat(filepath.Join(dir,file))
  if err!=nil{return info,err}
  if !stat.Mode().IsRegular()||stat.Mode().Perm()&0077!=0{return info,errors.New("unsafe staged recovery file")}
 }
 contents,err:=os.ReadDir(dir)
 if err!=nil{return info,err}
 for _,item:=range contents{
  name:=item.Name()
  if name=="catalog.db"||name=="master.key"||name=="restic.key"||name=="recovery-manifest.json"{continue}
  if strings.HasPrefix(name,"restic-")&&strings.HasSuffix(name,".key"){
   id:=strings.TrimSuffix(strings.TrimPrefix(name,"restic-"),".key")
   stat,err:=os.Lstat(filepath.Join(dir,name))
   if err!=nil{return info,err}
   if safeID(id)&&stat.Mode().IsRegular()&&stat.Mode().Perm()&0077==0{continue}
  }
  return info,errors.New("unexpected recovery staging entry")
 }
 db,err:=sql.Open("sqlite","file:"+filepath.ToSlash(filepath.Join(dir,"catalog.db"))+"?mode=ro")
 if err!=nil{return info,err}
 defer db.Close()
 var id,integrity string
 if err=db.QueryRowContext(ctx,"SELECT instance_id FROM instance WHERE singleton=1").Scan(&id);err!=nil{return info,err}
 if id!=info.InstanceID{return info,errors.New("recovery kit catalog UUID mismatch")}
 if err=db.QueryRowContext(ctx,"PRAGMA quick_check").Scan(&integrity);err!=nil{return info,err}
 if integrity!="ok"{return info,errors.New("staged catalog is corrupt")}
 var version int
 if err=db.QueryRowContext(ctx,"PRAGMA user_version").Scan(&version);err!=nil{return info,err}
 if version>info.SchemaVersion||version<1{return info,errors.New("incompatible recovered database schema")}
 return info,nil
}

// ApplyPreparedRecovery swaps the state directory ATOMICALLY using Linux
// renameat2(RENAME_EXCHANGE), leaving the initial empty installation as a
// private sibling rollback directory. This must run BEFORE the new daemon
// opens SQLite, by systemd ExecStartPre with root privileges.
func ApplyPreparedRecovery(ctx context.Context,dataDir string)(bool,error){
 if !filepath.IsAbs(dataDir)||filepath.Clean(dataDir)=="/"{return false,errors.New("invalid service state directory")}
 if err:=validateRealParents(dataDir);err!=nil{return false,err}
 requestPath:=filepath.Join(dataDir,recoveryRequestFile)
 raw,err:=os.ReadFile(requestPath)
 if os.IsNotExist(err){return false,nil}
 if err!=nil{return false,err}
 if len(raw)>4096{return false,errors.New("invalid recovery request size")}
 var req preparedRecoveryRequest
 if err=json.Unmarshal(raw,&req);err!=nil{return false,err}
 if req.Format!=recoveryKitFormat||req.InstanceID==""{return false,errors.New("invalid pending recovery request")}
 oldDB,err:=sql.Open("sqlite","file:"+filepath.ToSlash(filepath.Join(dataDir,"catalog.db"))+"?mode=ro")
 if err!=nil{return false,err}
 if err=recoveryTargetIsEmpty(ctx,oldDB);err!=nil{_ =oldDB.Close();return false,err}
 if err=oldDB.Close();err!=nil{return false,err}
 sibling:=dataDir+".recovery-staged"
 rollback:=dataDir+".before-recovery"
 if _,err=os.Lstat(rollback);err==nil{return false,errors.New("previous recovery rollback directory already exists")}else if !os.IsNotExist(err){return false,err}
 stage:=filepath.Join(dataDir,recoveryReadyDir)
 stageInfo,err:=os.Lstat(stage)
 if err==nil{
  if !stageInfo.IsDir()||stageInfo.Mode()&os.ModeSymlink!=0{return false,errors.New("invalid staged recovery directory")}
  if _,err=os.Lstat(sibling);err==nil{return false,errors.New("unexpected existing recovery staging sibling")}else if !os.IsNotExist(err){return false,err}
  if err=os.Rename(stage,sibling);err!=nil{return false,err}
 }else if os.IsNotExist(err){
  if _,err=os.Lstat(sibling);err!=nil{return false,errors.New("pending recovery staging not found")}
 }else{return false,err}
 info,err:=readPreparedRecovery(ctx,sibling)
 if err!=nil{return false,err}
 if info.InstanceID!=req.InstanceID{return false,errors.New("staged recovery UUID changed")}
 // Atomic: either the original installation is intact, or the new one is.
 // Failure on filesystems without RENAME_EXCHANGE leaves the original intact.
 if err=unix.Renameat2(unix.AT_FDCWD,dataDir,unix.AT_FDCWD,sibling,unix.RENAME_EXCHANGE);err!=nil{
  return false,fmt.Errorf("atomic recovery state swap failed (requires Linux renameat2 support): %w",err)
 }
 // The old state now resides at sibling; do not erase it on a rename error.
 if err=os.Rename(sibling,rollback);err!=nil{
  return true,fmt.Errorf("new state installed; original state retained at %s: %w",sibling,err)
 }
 return true,nil
}
