package backup

import (
 "archive/zip"
 "bytes"
 "context"
 "io"
 "os"
 "os/exec"
 "path/filepath"
 "testing"
 "time"

 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/vault"
)

func TestIndependentRecoveryScanAndKitRestore(t *testing.T){
 if _,err:=exec.LookPath("restic");err!=nil{
  if os.Getenv("BACKUPHUB_RESTIC_REQUIRED")=="1"{t.Fatal(err)}
  t.Skip("restic unavailable")
 }
 ctx,cancel:=context.WithTimeout(context.Background(),4*time.Minute)
 defer cancel()
 svc:=serviceFixture(t)
 src:=privateDir(t)
 if err:=os.WriteFile(filepath.Join(src,"survivor.txt"),[]byte("portable snapshot"),0600);err!=nil{t.Fatal(err)}
 job,err:=svc.Create(ctx,CreateSource{Name:"Lost host",Kind:"local",Path:src,IntervalMinutes:60})
 if err!=nil{t.Fatal(err)}
 if err=svc.Manual(ctx,job.ID);err!=nil{t.Fatal(err)}
 if did,err:=svc.scheduler.RunDue(ctx);err!=nil||!did{t.Fatalf("backup: %v, %v",did,err)}
 snapshots,err:=svc.ListSnapshots(ctx,job.ID)
 if err!=nil||len(snapshots)!=1{t.Fatalf("original backup not catalogued: %v %v",snapshots,err)}
 recovered,err:=svc.repo.recoverManifestIndex(ctx)
 if err!=nil{t.Fatal(err)}
 if len(recovered)!=1||recovered[0].Record.ID!=snapshots[0].ID||recovered[0].Record.SourceID!=job.ID {
  t.Fatalf("portable recovery mismatch: %+v",recovered)
 }
 // A Restic snapshot not accompanied by a committed BackupHub manifest
 // must never appear in the recovered file history.
 if _,err=svc.repo.run(ctx,bytes.NewBufferString("not committed"),"backup","--stdin","--stdin-filename","uncommitted.txt");err!=nil{t.Fatal(err)}
 recovered,err=svc.repo.recoverManifestIndex(ctx)
 if err!=nil||len(recovered)!=1{t.Fatalf("uncommitted snapshot mistakenly recovered: %v %v",recovered,err)}

 var kit bytes.Buffer
 if err=svc.ExportRecoveryKit(ctx,&kit);err!=nil{t.Fatal(err)}
 z,err:=zip.NewReader(bytes.NewReader(kit.Bytes()),int64(kit.Len()))
 if err!=nil{t.Fatal(err)}
 present:=map[string]bool{}
 for _,f:=range z.File{present[f.Name]=true}
 for _,name:=range []string{"catalog.db","restic.key","master.key","recovery-manifest.json"}{
  if !present[name]{t.Fatalf("kit lacks %s",name)}
 }
 file:=filepath.Join(privateDir(t),"kit.zip")
 if err=os.WriteFile(file,kit.Bytes(),0600);err!=nil{t.Fatal(err)}
 restored:=filepath.Join(privateDir(t),"restored-state")
 if err=RestoreRecoveryKit(ctx,file,restored);err!=nil{t.Fatal(err)}
 if err=RestoreRecoveryKit(ctx,file,restored);err==nil{t.Fatal("overwrote existing destination")}
 state,err:=catalog.Open(restored)
 if err!=nil{t.Fatal(err)}
 defer state.Close()
 if state.InstanceID!=svc.catalog.InstanceID{t.Fatal("original installation identity lost")}
 var count int
 if err=state.DB.QueryRow("SELECT COUNT(*) FROM file_snapshots").Scan(&count);err!=nil||count!=1{t.Fatalf("portable catalog incomplete: %d %v",count,err)}
 originalKey,err:=os.ReadFile(filepath.Join(svc.catalog.DataDir,"restic.key"));if err!=nil{t.Fatal(err)}
 kitKey,err:=os.ReadFile(filepath.Join(restored,"restic.key"));if err!=nil{t.Fatal(err)}
 if !bytes.Equal(originalKey,kitKey){t.Fatal("restic recovery key changed")}
 // Recovered installations can start with their old primary repository
 // missing. They must not silently initialize a new one with the old key.
 originalRepo:=svc.repo.path
 missingRepo:=originalRepo+".lost"
 if err=os.Rename(originalRepo,missingRepo);err!=nil{t.Fatal(err)}
 recoveredVault,err:=vault.Open(state);if err!=nil{t.Fatal(err)}
 degraded,err:=New(ctx,state,recoveredVault,"","")
 if err!=nil{t.Fatalf("cannot start with lost primary repository: %v",err)}
 if _,err=degraded.repositoryFor(ctx,PrimaryStorageID);err==nil{t.Fatal("lost primary repository unexpectedly available")}
 if _,err=os.Stat(originalRepo);!os.IsNotExist(err){t.Fatalf("recreated lost repository: %v",err)}

}

// The exporter stores exactly one SQLite snapshot; a corrupted/malicious
// archive cannot write anything outside its new destination.
func TestRecoveryKitRejectsUnsafeZIPPaths(t *testing.T){
 var buf bytes.Buffer
 writer:=zip.NewWriter(&buf)
 f,err:=writer.Create("../escape");if err!=nil{t.Fatal(err)}
 if _,err=io.WriteString(f,"tampering");err!=nil{t.Fatal(err)}
 if err=writer.Close();err!=nil{t.Fatal(err)}
 kit:=filepath.Join(privateDir(t),"malicious.zip")
 if err=os.WriteFile(kit,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
 target:=filepath.Join(privateDir(t),"imported")
 if err=RestoreRecoveryKit(context.Background(),kit,target);err==nil{t.Fatal("malicious ZIP imported")}
 if _,err=os.Lstat(target);!os.IsNotExist(err){t.Fatal("recovery wrote destination on invalid kit")}
}
