package backup

import (
 "context"
 "database/sql"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "strings"
 "syscall"
 "time"

 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/storagehelper"
)

const PrimaryStorageID="primary"

func pathOverlaps(a,b string)bool{
 if !filepath.IsAbs(a)||!filepath.IsAbs(b){return false}
 a,b=filepath.Clean(a),filepath.Clean(b)
 return a==b||strings.HasPrefix(a,b+string(os.PathSeparator))||strings.HasPrefix(b,a+string(os.PathSeparator))
}

type Storage struct {
 ID string `json:"id"`
 Name string `json:"name"`
 Path string `json:"path"`
 MountPath string `json:"mount_path,omitempty"`
 FreeBytes uint64 `json:"free_bytes"`
 Status string `json:"status"`
}

type CreateStorage struct {
 Name string `json:"name"`
 ParentPath string `json:"parent_path"`
 Folder string `json:"folder"`
}

// Primary's location is recorded at upgrade. A changed BACKUPHUB_REPOSITORY
// cannot silently redirect existing snapshots to an empty repository.
func ensurePrimaryStorage(ctx context.Context,c *catalog.Catalog, path string) error {
 result,err:=c.DB.ExecContext(ctx,"UPDATE storage_repositories SET path=? WHERE id='primary' AND path=''",path)
 if err!=nil{return err}
 _ = result
 var stored string
 if err=c.DB.QueryRowContext(ctx,"SELECT path FROM storage_repositories WHERE id='primary'").Scan(&stored);err!=nil{return err}
 if stored!=path{return fmt.Errorf("configured primary repository differs from catalog (%s): move data using explicit migration, do not change BACKUPHUB_REPOSITORY",stored)}
 return nil
}

func (s *Service) ListStorages(ctx context.Context)([]Storage,error){
 rows,err:=s.catalog.DB.QueryContext(ctx,"SELECT id,display_name,path,mount_path FROM storage_repositories ORDER BY created_at,id")
 if err!=nil{return nil,err}
 defer rows.Close()
 out:=[]Storage{}
 for rows.Next(){
  var x Storage
  if err=rows.Scan(&x.ID,&x.Name,&x.Path,&x.MountPath);err!=nil{return nil,err}
  var stats syscall.Statfs_t
  if x.MountPath!=""&&!storagehelper.MountPresent(x.MountPath){x.Status="unavailable";out=append(out,x);continue}
  if e:=syscall.Statfs(x.Path,&stats);e==nil{
   x.FreeBytes=stats.Bavail*uint64(stats.Bsize)
   x.Status="available"
   if _,err=os.Stat(filepath.Join(x.Path,"config"));err!=nil{x.Status="unavailable";x.FreeBytes=0}
  }else{x.Status="unavailable"}
  out=append(out,x)
 }
 return out,rows.Err()
}

func (s *Service) repositoryFor(ctx context.Context,id string)(*repository,error){
 if id==""||id==PrimaryStorageID{return s.repo,nil}
 if !safeID(id){return nil,errors.New("invalid repository id")}
 var path,mountPath string
 if err:=s.catalog.DB.QueryRowContext(ctx,"SELECT path,mount_path FROM storage_repositories WHERE id=?",id).Scan(&path,&mountPath);err!=nil{return nil,err}
 if mountPath!=""&&!storagehelper.MountPresent(mountPath){return nil,errors.New("storage volume not mounted")}
 if path==s.repo.path{return nil,errors.New("repository overlaps primary storage")}
 key:=filepath.Join(s.catalog.DataDir,"restic-"+id+".key")
 return openRepositoryNamed(ctx,s.catalog.DataDir,path,key)
}

func (s *Service) taskRepository(ctx context.Context,scheduleID string)(string,error){
 var repoID string
 err:=s.catalog.DB.QueryRowContext(ctx,"SELECT repository_id FROM schedule_repositories WHERE schedule_id=?",scheduleID).Scan(&repoID)
 return repoID,err
}

// RegisterStorage is used after privileged directory provisioning, but is
// also testable with a directory owned by the backuphub service user.
func (s *Service) RegisterStorage(ctx context.Context,name,repoPath string)(Storage,error){
 return s.registerStorage(ctx,name,repoPath,"")
}
func (s *Service) registerStorage(ctx context.Context,name,repoPath,mountPath string)(Storage,error){
 var empty Storage
 if mountPath!="" && !storagehelper.MountPresent(mountPath){return empty,errors.New("storage volume is not mounted")}
 if strings.TrimSpace(name)==""||len(name)>120||strings.ContainsAny(name,"\x00\r\n"){return empty,errors.New("invalid storage name")}
 if !filepath.IsAbs(repoPath)||filepath.Clean(repoPath)=="/"{return empty,errors.New("storage path must be absolute")}
 repoPath=filepath.Clean(repoPath)
 if pathOverlaps(repoPath,s.catalog.DataDir)||pathOverlaps(repoPath,s.stageDir){return empty,errors.New("external storage cannot overlap BackupHub state")}
 // Never nest Restic repositories, even across unrelated backup tasks.
 rows,err:=s.catalog.DB.QueryContext(ctx,"SELECT path FROM storage_repositories")
 if err!=nil{return empty,err}
 var existing []string
 for rows.Next(){var p string;if err=rows.Scan(&p);err!=nil{_ =rows.Close();return empty,err};existing=append(existing,p)}
 if err=rows.Err();err!=nil{_ =rows.Close();return empty,err}
 _ =rows.Close()
 for _,p:=range existing {if pathOverlaps(repoPath,p){return empty,errors.New("repositories must not overlap")}}
 // Ensure a newly added destination is not within an existing local source
 // (otherwise that source would recursively back up its own repository).
 sourceRows,err:=s.catalog.DB.QueryContext(ctx,"SELECT settings_json FROM sources WHERE kind='local'")
 if err!=nil{return empty,err}
 for sourceRows.Next(){
  var encoded string
  if err=sourceRows.Scan(&encoded);err!=nil{_ =sourceRows.Close();return empty,err}
  var cfg Settings
  if err=json.Unmarshal([]byte(encoded),&cfg);err!=nil{_ =sourceRows.Close();return empty,err}
  if pathOverlaps(repoPath,cfg.Path){_ =sourceRows.Close();return empty,errors.New("repository overlaps an existing local backup source")}
 }
 if err=sourceRows.Err();err!=nil{_ =sourceRows.Close();return empty,err}
 _ =sourceRows.Close()
 var exists int
 if err:=s.catalog.DB.QueryRowContext(ctx,"SELECT COUNT(*) FROM storage_repositories WHERE path=?",repoPath).Scan(&exists);err!=nil{return empty,err}
 if exists!=0{return empty,errors.New("repository is already registered")}
 id,err:=randomID();if err!=nil{return empty,err}
 s.repoMu.Lock()
 defer s.repoMu.Unlock()
 key:=filepath.Join(s.catalog.DataDir,"restic-"+id+".key")
 marker:="restic."+id+".init-in-progress"
 if _,err=initRepositoryNamed(ctx,s.catalog.DataDir,repoPath,key,marker);err!=nil{return empty,err}
 _,err=s.catalog.DB.ExecContext(ctx,"INSERT INTO storage_repositories(id,display_name,path,mount_path,created_at) VALUES(?,?,?,?,?)",id,name,repoPath,mountPath,time.Now().UTC().Format(time.RFC3339Nano))
 if err!=nil{return empty,err}
 return Storage{ID:id,Name:name,Path:repoPath,MountPath:mountPath,Status:"available"},nil
}

func (s *Service) ProvisionStorage(ctx context.Context,in CreateStorage)(Storage,error){
 var empty Storage
 if strings.TrimSpace(in.Name)==""||len(in.Name)>120{return empty,errors.New("storage name required")}
 client:=storagehelper.NewClient("/run/backuphub-storage.sock")
 directory,err:=client.Provision(ctx,in.ParentPath,in.Folder)
 if err!=nil{return empty,err}
 // Require the selected location to remain on the actual mounted volume,
 // not the system filesystem exposed after an unmount.
 mounts,err:=client.Mounts(ctx)
 if err!=nil{return empty,err}
 chosen:=""
 for _,m:=range mounts{
  if (directory==m.Path||strings.HasPrefix(directory,m.Path+"/"))&&len(m.Path)>len(chosen){chosen=m.Path}
 }
 if chosen==""||!storagehelper.MountPresent(chosen){return empty,errors.New("destination volume disconnected during provisioning")}
 return s.registerStorage(ctx,in.Name,filepath.Join(directory,"repository"),chosen)
}

func (s *Service) StorageMounts(ctx context.Context)([]storagehelper.Mount,error){
 return storagehelper.NewClient("/run/backuphub-storage.sock").Mounts(ctx)
}
func (s *Service) BrowseStorage(ctx context.Context,path string)([]storagehelper.Entry,error){
 return storagehelper.NewClient("/run/backuphub-storage.sock").Browse(ctx,path)
}

func (s *Service) SelectSourceStorage(ctx context.Context,sourceID,storageID string) error {
 if !safeID(sourceID)||!safeID(storageID){return errors.New("invalid source/storage ID")}
 // Confirm the new repository opens before storing the choice; preserve
 // snapshot.repository_id for every historical copy.
 if _,err:=s.repositoryFor(ctx,storageID);err!=nil{return err}
 // Don't switch the destination while an execution is underway.
 result,err:=s.catalog.DB.ExecContext(ctx,`UPDATE schedule_repositories SET repository_id=?
 WHERE schedule_id=? AND EXISTS(
 SELECT 1 FROM schedule_entries WHERE id=? AND active_run_id IS NULL
 )`,storageID,sourceID,sourceID)
 if err!=nil{return err}
 n,err:=result.RowsAffected();if err!=nil{return err}
 if n==0{return sql.ErrNoRows}
 return nil
}
