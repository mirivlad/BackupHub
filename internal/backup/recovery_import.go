package backup

import (
 "context"
 "database/sql"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "strings"
 "time"

 "github.com/mirivlad/BackupHub/internal/storagehelper"
)

// Existing repositories are never initialized or modified by emergency import.
type ImportStorageRequest struct {
 Name string `json:"name"`
 ParentPath string `json:"parent_path"`
 PasswordMode string `json:"password_mode"`
 Password string `json:"password,omitempty"`
}
type ImportStorageResult struct {
 Storage Storage `json:"storage"`
 Snapshots int `json:"snapshots"`
 Sources int `json:"sources"`
}

// ImportStorage rebuilds the read-only snapshot index from durable Restic
// commit markers. It does not fabricate source credentials or runnable jobs.
func (s *Service) ImportStorage(ctx context.Context,in ImportStorageRequest)(ImportStorageResult,error){
 var empty ImportStorageResult
 if strings.TrimSpace(in.Name)==""||len(in.Name)>120||strings.ContainsAny(in.Name,"\x00\r\n"){return empty,errors.New("invalid storage name")}
 if !validPasswordMode(in.PasswordMode){return empty,errors.New("invalid repository password mode")}
 if in.PasswordMode==NoPasswordMode && in.Password!=""{return empty,errors.New("passwordless storage cannot receive a key")}
 keyContent:=""
 if in.PasswordMode==PasswordMode {
  secret:=strings.TrimSuffix(in.Password,"\n")
  if len(secret)!=96{return empty,errors.New("original BackupHub Restic key required")}
  if _,err:=hex.DecodeString(secret);err!=nil{return empty,errors.New("invalid BackupHub Restic key")}
  keyContent=secret+"\n"
 }
 helper:=storagehelper.NewClient("/run/backuphub-storage.sock")
 directory,err:=helper.Provision(ctx,in.ParentPath,"")
 if err!=nil{return empty,err}
 mounts,err:=helper.Mounts(ctx)
 if err!=nil{return empty,err}
 mountPath:=""
 for _,m:=range mounts {
  if (directory==m.Path||strings.HasPrefix(directory,m.Path+"/"))&&len(m.Path)>len(mountPath){mountPath=m.Path}
 }
 if mountPath==""||!storagehelper.MountPresent(mountPath){return empty,errors.New("repository volume must be mounted")}
 path:=filepath.Join(directory,"repository")
 if pathOverlaps(path,s.catalog.DataDir)||pathOverlaps(path,s.stageDir){return empty,errors.New("recovery repository overlaps service state")}
 if err=validateRealParents(path);err!=nil{return empty,err}
 info,err:=os.Lstat(path)
 if err!=nil{return empty,errors.New("existing repository directory missing")}
 if !info.IsDir()||info.Mode()&os.ModeSymlink!=0{return empty,errors.New("repository must be a real directory")}
 cfg,err:=os.Lstat(filepath.Join(path,"config"))
 if err!=nil||!cfg.Mode().IsRegular(){return empty,errors.New("selected folder does not contain an existing Restic repository")}
 s.repoMu.Lock()
 defer s.repoMu.Unlock()
 rows,err:=s.catalog.DB.QueryContext(ctx,"SELECT path,display_name FROM storage_repositories")
 if err!=nil{return empty,err}
 for rows.Next(){
  var oldPath,oldName string
  if err=rows.Scan(&oldPath,&oldName);err!=nil{_ =rows.Close();return empty,err}
  if pathOverlaps(path,oldPath)||in.Name==oldName{_ =rows.Close();return empty,errors.New("repository is already registered")}
 }
 if err=rows.Err();err!=nil{_ =rows.Close();return empty,err}
 _=rows.Close()
 rows,err=s.catalog.DB.QueryContext(ctx,"SELECT settings_json FROM sources WHERE kind='local'")
 if err!=nil{return empty,err}
 for rows.Next(){
  var encoded string
  if err=rows.Scan(&encoded);err!=nil{_ =rows.Close();return empty,err}
  var cfg Settings
  if err=json.Unmarshal([]byte(encoded),&cfg);err!=nil{_ =rows.Close();return empty,err}
  if cfg.Path!=""&&pathOverlaps(path,cfg.Path){_ =rows.Close();return empty,errors.New("repository overlaps a configured source")}
 }
 if err=rows.Err();err!=nil{_ =rows.Close();return empty,err}
 _=rows.Close()

 id,err:=randomID();if err!=nil{return empty,err}
 keyPath:=filepath.Join(s.catalog.DataDir,"restic-"+id+".key")
 keyCreated:=false
 if in.PasswordMode==PasswordMode{
  f,err:=os.OpenFile(keyPath,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600)
  if err!=nil{return empty,err}
  _,writeErr:=f.WriteString(keyContent)
  syncErr:=f.Sync()
  closeErr:=f.Close()
  if writeErr!=nil||syncErr!=nil||closeErr!=nil{
   _=os.Remove(keyPath)
   return empty,errors.New("cannot store imported repository key")
  }
  keyCreated=true
 }
 success:=false
 defer func(){if !success&&keyCreated{_ =os.Remove(keyPath)}}()
 if !storagehelper.MountPresent(mountPath){return empty,errors.New("volume disconnected during import")}
 r,err:=openRepositoryNamedMode(ctx,s.catalog.DataDir,path,keyPath,in.PasswordMode)
 if err!=nil{return empty,fmt.Errorf("cannot open existing Restic archive: %w",err)}
 if err=r.check(ctx,false);err!=nil{return empty,fmt.Errorf("restic metadata integrity check failed: %w",err)}
 entries,err:=r.recoverManifestIndex(ctx)
 if err!=nil{return empty,err}
 if len(entries)==0{return empty,errors.New("no completed BackupHub snapshots found")}
 tx,err:=s.catalog.DB.BeginTx(ctx,nil)
 if err!=nil{return empty,err}
 defer tx.Rollback()
 now:=time.Now().UTC().Format(time.RFC3339Nano)
 if _,err=tx.ExecContext(ctx,"INSERT INTO storage_repositories(id,display_name,path,mount_path,password_mode,created_at) VALUES(?,?,?,?,?,?)",id,in.Name,path,mountPath,in.PasswordMode,now);err!=nil{return empty,err}
 addedSources:=0
 for _,e:=range entries{
  rec:=e.Record
  var existingKind string
  err=tx.QueryRowContext(ctx,"SELECT kind FROM sources WHERE id=?",rec.SourceID).Scan(&existingKind)
  if errors.Is(err,sql.ErrNoRows){
   if _,err=tx.ExecContext(ctx,"INSERT INTO sources(id,kind,display_name,endpoint,settings_json,created_at) VALUES(?,?,?,?,?,?)",rec.SourceID,rec.Kind,rec.SourceName,"","{}",now);err!=nil{return empty,err}
   if _,err=tx.ExecContext(ctx,"INSERT INTO datasets(id,source_id,display_name,locator) VALUES(?,?,?,?)",rec.SourceID,rec.SourceID,rec.SourceName,"");err!=nil{return empty,err}
   addedSources++
  } else if err!=nil{return empty,err} else if existingKind!=rec.Kind{return empty,errors.New("source identifier conflicts with a different source kind")}
  var previousRepo string
  err=tx.QueryRowContext(ctx,"SELECT repository_id FROM snapshots WHERE id=?",rec.ID).Scan(&previousRepo)
  if err==nil{return empty,fmt.Errorf("snapshot identifier already exists in repository %s",previousRepo)}
  if !errors.Is(err,sql.ErrNoRows){return empty,err}
  if _,err=tx.ExecContext(ctx,"INSERT INTO snapshots(id,dataset_id,repository_id,state,manifest_ref,created_at) VALUES(?,?,?,?,?,?)",rec.ID,rec.SourceID,id,"committed",e.MarkerID,rec.At);err!=nil{return empty,err}
  if _,err=tx.ExecContext(ctx,"INSERT INTO file_snapshots(snapshot_id,source_id,restic_id,root_path,committed_at,verification,fidelity,consistency) VALUES(?,?,?,?,?,?,?,?)",rec.ID,rec.SourceID,rec.ResticID,rec.Root,rec.At,"verified_when_created",rec.Fidelity,rec.Consistency);err!=nil{return empty,err}
 }
 if !storagehelper.MountPresent(mountPath){return empty,errors.New("mounted volume disconnected during index reconstruction")}
 if err=tx.Commit();err!=nil{return empty,err}
 success=true
 return ImportStorageResult{Storage:Storage{ID:id,Name:in.Name,Path:path,MountPath:mountPath,PasswordMode:in.PasswordMode,Status:"available"},Snapshots:len(entries),Sources:addedSources},nil
}
