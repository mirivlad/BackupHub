package backup

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "strings"

 "github.com/mirivlad/BackupHub/internal/storagehelper"
)

// RebindStorage connects a formerly registered Restic repository at a new
// mount point. It never creates, initializes, modifies, or formats the disk.
// ALL catalogued commit-marker and data-snapshot IDs must still exist there.
func (s *Service) RebindStorage(ctx context.Context,id,parentPath string)(Storage,error){
 var empty Storage
 if !safeID(id){return empty,errors.New("invalid repository identifier")}
 if parentPath==""{return empty,errors.New("choose the existing repository folder")}
 helper:=storagehelper.NewClient("/run/backuphub-storage.sock")
 folder,err:=helper.Provision(ctx,parentPath,"")
 if err!=nil{return empty,err}
 mounts,err:=helper.Mounts(ctx);if err!=nil{return empty,err}
 mountPath:=""
 for _,m:=range mounts{
  if (folder==m.Path||strings.HasPrefix(folder,m.Path+"/"))&&len(m.Path)>len(mountPath){mountPath=m.Path}
 }
 if mountPath==""||!storagehelper.MountPresent(mountPath){return empty,errors.New("selected data volume is not mounted")}
 newPath:=filepath.Join(folder,"repository")
 if pathOverlaps(newPath,s.catalog.DataDir)||pathOverlaps(newPath,s.stageDir){return empty,errors.New("repository overlaps BackupHub state")}
 if err=validateRealParents(newPath);err!=nil{return empty,err}
 s.repoMu.Lock()
 defer s.repoMu.Unlock()
 var oldPath,mode,name string
 if err=s.catalog.DB.QueryRowContext(ctx,"SELECT path,password_mode,display_name FROM storage_repositories WHERE id=?",id).Scan(&oldPath,&mode,&name);err!=nil{return empty,err}
 if !validPasswordMode(mode){return empty,errors.New("unknown storage protection mode")}
 if filepath.Clean(oldPath)==newPath{
  return empty,errors.New("selected repository is already connected at this path")
 }
 rows,err:=s.catalog.DB.QueryContext(ctx,"SELECT id,path FROM storage_repositories WHERE id<>?",id)
 if err!=nil{return empty,err}
 for rows.Next(){
  var otherID,otherPath string
  if err=rows.Scan(&otherID,&otherPath);err!=nil{_ =rows.Close();return empty,err}
  if pathOverlaps(newPath,otherPath){_ =rows.Close();return empty,errors.New("new repository path overlaps another configured storage")}
 }
 if err=rows.Err();err!=nil{_ =rows.Close();return empty,err}
 _=rows.Close()
 rows,err=s.catalog.DB.QueryContext(ctx,"SELECT settings_json FROM sources WHERE kind='local'")
 if err!=nil{return empty,err}
 for rows.Next(){
  var serialized string
  if err=rows.Scan(&serialized);err!=nil{_ =rows.Close();return empty,err}
  var settings Settings
  if err=json.Unmarshal([]byte(serialized),&settings);err!=nil{_ =rows.Close();return empty,err}
  if settings.Path!=""&&pathOverlaps(newPath,settings.Path){_ =rows.Close();return empty,errors.New("repository path overlaps a configured source")}
 }
 if err=rows.Err();err!=nil{_ =rows.Close();return empty,err}
 _=rows.Close()
 if _,err=os.Lstat(filepath.Join(newPath,"config"));err!=nil{return empty,fmt.Errorf("repository config missing at selected mount: %w",err)}
 key:=filepath.Join(s.catalog.DataDir,"restic-"+id+".key")
 if id==PrimaryStorageID{key=filepath.Join(s.catalog.DataDir,"restic.key")}
 candidate,err:=openRepositoryNamedMode(ctx,s.catalog.DataDir,newPath,key,mode)
 if err!=nil{return empty,fmt.Errorf("cannot open recovered repository with saved credentials: %w",err)}
 if err=candidate.check(ctx,false);err!=nil{return empty,fmt.Errorf("repository metadata check failed: %w",err)}
 output,err:=candidate.run(ctx,nil,"snapshots","--json")
 if err!=nil{return empty,err}
 var actual []portableResticSnapshot
 if err=json.Unmarshal(output,&actual);err!=nil{return empty,err}
 ids:=make(map[string]bool,len(actual))
 for _,item:=range actual{ids[item.ID]=true}
 // A repository ID cannot currently be reconstructed without an existing
 // committed snapshot on installations upgraded from Preview 3. Therefore,
 // refuse arbitrary rebinding of an empty repository.
 rows,err=s.catalog.DB.QueryContext(ctx,"SELECT sp.manifest_ref,f.restic_id FROM snapshots sp JOIN file_snapshots f ON f.snapshot_id=sp.id WHERE sp.repository_id=?",id)
 if err!=nil{return empty,err}
 count:=0
 for rows.Next(){
  var marker,dataID string
  if err=rows.Scan(&marker,&dataID);err!=nil{_ =rows.Close();return empty,err}
  count++
  if !ids[marker]||!ids[dataID]{_ =rows.Close();return empty,errors.New("selected disk does not contain all previously recorded BackupHub snapshots")}
 }
 if err=rows.Err();err!=nil{_ =rows.Close();return empty,err}
 _=rows.Close()
 if count==0{return empty,errors.New("no committed snapshots available to verify this repository; use a new storage instead")}
 // A live mount cannot disappear between metadata scan and SQLite pointer
 // update without the operation refusing to complete.
 if !storagehelper.MountPresent(mountPath){return empty,errors.New("mounted volume disappeared during verification")}
 result,err:=s.catalog.DB.ExecContext(ctx,"UPDATE storage_repositories SET path=?,mount_path=? WHERE id=?",newPath,mountPath,id)
 if err!=nil{return empty,err}
 rowsAffected,err:=result.RowsAffected();if err!=nil{return empty,err}
 if rowsAffected!=1{return empty,errors.New("repository pointer update did not affect exactly one row")}
 if id==PrimaryStorageID{s.repo.path=newPath}
 return Storage{ID:id,Name:name,Path:newPath,MountPath:mountPath,PasswordMode:mode,Status:"available"},nil
}
