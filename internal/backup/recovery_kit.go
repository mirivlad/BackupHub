package backup

import (
 "archive/zip"
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

 "github.com/mirivlad/BackupHub/internal/catalog"
)

const recoveryKitFormat="backuphub.recovery-kit.v1"

type RecoveryKitInfo struct {
 Format string `json:"format"`
 InstanceID string `json:"instance_uuid"`
 SchemaVersion int `json:"schema_version"`
 CreatedAt string `json:"created_at"`
 Repositories []Storage `json:"repositories"`
}

// ExportRecoveryKit writes a consistent SQLite snapshot and required secrets
// into a ZIP. The ZIP itself is a highly sensitive plaintext artifact.
// Physical Restic repository contents are NOT included.
func (s *Service) ExportRecoveryKit(ctx context.Context,out io.Writer)error{
 s.repoMu.Lock()
 defer s.repoMu.Unlock()
 temp,err:=os.MkdirTemp(s.catalog.DataDir,".kit-")
 if err!=nil{return err}
 defer os.RemoveAll(temp)
 if err=os.Chmod(temp,0700);err!=nil{return err}
 dbCopy:=filepath.Join(temp,"catalog.db")
 if _,err=s.catalog.DB.ExecContext(ctx,"VACUUM INTO ?",dbCopy);err!=nil{return fmt.Errorf("cannot snapshot SQLite catalog: %w",err)}
 entries:=[]struct{name,path string}{{"catalog.db",dbCopy},{"master.key",filepath.Join(s.catalog.DataDir,"master.key")},{"restic.key",filepath.Join(s.catalog.DataDir,"restic.key")}}
 rows,err:=s.catalog.DB.QueryContext(ctx,"SELECT id,password_mode FROM storage_repositories")
 if err!=nil{return err}
 for rows.Next(){
  var id,mode string
  if err=rows.Scan(&id,&mode);err!=nil{_ =rows.Close();return err}
  if id=="primary"{continue}
  if !safeID(id)||!validPasswordMode(mode){_ =rows.Close();return errors.New("invalid persisted repository authentication")}
  if mode==PasswordMode{
   name:="restic-"+id+".key"
   entries=append(entries,struct{name,path string}{name,filepath.Join(s.catalog.DataDir,name)})
  }
 }
 if err=rows.Err();err!=nil{_ =rows.Close();return err}
 _=rows.Close()
 storages,err:=s.ListStorages(ctx)
 if err!=nil{return err}
 manifest:=RecoveryKitInfo{Format:recoveryKitFormat,InstanceID:s.catalog.InstanceID,SchemaVersion:catalog.SchemaVersion,CreatedAt:time.Now().UTC().Format(time.RFC3339Nano),Repositories:storages}
 writer:=zip.NewWriter(out)
 for _,entry:=range entries{
  info,err:=os.Lstat(entry.path)
  if err!=nil{return err}
  if !info.Mode().IsRegular() || (entry.name!="catalog.db"&&info.Mode().Perm()&0077!=0){return errors.New("unsafe key or catalog file")}
  input,err:=os.Open(entry.path)
  if err!=nil{return err}
  header:=&zip.FileHeader{Name:entry.name,Method:zip.Deflate}
  header.SetMode(0600)
  zipEntry,err:=writer.CreateHeader(header)
  if err!=nil{_ =input.Close();return err}
  _,copyErr:=io.Copy(zipEntry,input)
  closeErr:=input.Close()
  if copyErr!=nil{return copyErr}
  if closeErr!=nil{return closeErr}
 }
 header:=&zip.FileHeader{Name:"recovery-manifest.json",Method:zip.Deflate}
 header.SetMode(0600)
 target,err:=writer.CreateHeader(header)
 if err!=nil{return err}
 if err=json.NewEncoder(target).Encode(manifest);err!=nil{return err}
 return writer.Close()
}

// RestoreRecoveryKit safely extracts a trusted operator-selected kit into a
// completely NEW directory. Never overwrite a running BackupHub installation.
// Use CLI while the destination service is stopped.
func RestoreRecoveryKit(ctx context.Context,kitPath,targetDir string)error{
 if !filepath.IsAbs(targetDir)||filepath.Clean(targetDir)=="/"{return errors.New("destination must be an absolute non-root path")}
 if err:=validateRealParents(targetDir);err!=nil{return err}
 if _,err:=os.Lstat(targetDir);err==nil{return errors.New("recovery target exists; refusing overwrite")}else if !os.IsNotExist(err){return err}
 reader,err:=zip.OpenReader(kitPath)
 if err!=nil{return err}
 defer reader.Close()
 if len(reader.File)>1000{return errors.New("too many entries in recovery kit")}
 contents:=make(map[string]*zip.File)
 var total uint64
 for _,f:=range reader.File{
  name:=f.Name
  allowed:=name=="catalog.db"||name=="master.key"||name=="restic.key"||name=="recovery-manifest.json"
  if strings.HasPrefix(name,"restic-")&&strings.HasSuffix(name,".key"){
   id:=strings.TrimSuffix(strings.TrimPrefix(name,"restic-"),".key")
   allowed=safeID(id)
  }
  if !allowed||f.Mode()&os.ModeType!=0{return errors.New("unexpected or unsafe recovery kit entry")}
  if _,exists:=contents[name];exists{return errors.New("duplicate recovery kit entry")}
  maxSize:=uint64(16384)
  if name=="catalog.db"{maxSize=16<<30}
  if f.UncompressedSize64>maxSize{return errors.New("recovery kit file is unexpectedly large")}
  if total>20<<30-f.UncompressedSize64{return errors.New("recovery kit exceeds 20 GiB")}
  total+=f.UncompressedSize64
  contents[name]=f
 }
 for _,needed:=range []string{"catalog.db","master.key","restic.key","recovery-manifest.json"}{
  if contents[needed]==nil{return fmt.Errorf("recovery kit lacks %s",needed)}
 }
 manifestEntry,err:=contents["recovery-manifest.json"].Open()
 if err!=nil{return err}
 var manifest RecoveryKitInfo
 err=json.NewDecoder(io.LimitReader(manifestEntry,16385)).Decode(&manifest)
 _=manifestEntry.Close()
 if err!=nil||manifest.Format!=recoveryKitFormat||manifest.InstanceID==""||manifest.SchemaVersion<1||manifest.SchemaVersion>catalog.SchemaVersion{
  return errors.New("invalid or incompatible recovery kit manifest")
 }
 for _,storage:=range manifest.Repositories{
  if !safeID(storage.ID)||!validPasswordMode(storage.PasswordMode){return errors.New("invalid kit repository record")}
  if storage.ID!="primary"&&storage.PasswordMode==PasswordMode&&contents["restic-"+storage.ID+".key"]==nil{
   return errors.New("missing key for password-protected repository")
  }
  if storage.PasswordMode==NoPasswordMode&&contents["restic-"+storage.ID+".key"]!=nil{return errors.New("unexpected key for passwordless repository")}
 }
 if err=os.Mkdir(targetDir,0700);err!=nil{return err}
 success:=false
 defer func(){if !success{_ =os.RemoveAll(targetDir)}}()
 for _,f:=range reader.File{
  if err=ctx.Err();err!=nil{return err}
  source,err:=f.Open();if err!=nil{return err}
  dest,err:=os.OpenFile(filepath.Join(targetDir,f.Name),os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600)
  if err!=nil{_ =source.Close();return err}
  // Read through EOF to force archive/zip's CRC validation. CopyN
  // could stop at the expected size without checking a damaged payload.
  copied,copyErr:=io.Copy(dest,io.LimitReader(source,int64(f.UncompressedSize64)+1))
  syncErr:=dest.Sync()
  closeErr:=dest.Close()
  _=source.Close()
  if copyErr!=nil{return copyErr}
  if uint64(copied)!=f.UncompressedSize64{return errors.New("recovery kit entry size mismatch")}
  if syncErr!=nil{return syncErr}
  if closeErr!=nil{return closeErr}
 }
 // Do not use catalog.Open: that would mutate the imported database. Verify
 // the recovered identity with a separate read-only SQLite connection.
 path:=filepath.Join(targetDir,"catalog.db")
 db,err:=sql.Open("sqlite","file:"+filepath.ToSlash(path)+"?mode=ro")
 if err!=nil{return err}
 defer db.Close()
 var instanceID string
 if err=db.QueryRowContext(ctx,"SELECT instance_id FROM instance WHERE singleton=1").Scan(&instanceID);err!=nil{return err}
 if instanceID!=manifest.InstanceID{return errors.New("recovered catalog instance UUID mismatches manifest")}
 var integrity string
 if err=db.QueryRowContext(ctx,"PRAGMA quick_check").Scan(&integrity);err!=nil{return err}
 if integrity!="ok"{return errors.New("recovered SQLite catalog integrity check failed")}
 success=true
 return nil
}
