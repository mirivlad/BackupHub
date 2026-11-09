package backup

import (
 "bytes"
 "context"
 "os"
 "os/exec"
 "path/filepath"
 "testing"
 "time"

 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/vault"
)

func TestBrowserKitStagingAndAtomicServiceStartupSwap(t *testing.T){
 if _,err:=exec.LookPath("restic");err!=nil{
  if os.Getenv("BACKUPHUB_RESTIC_REQUIRED")=="1"{t.Fatal(err)}
  t.Skip("restic not installed")
 }
 ctx,cancel:=context.WithTimeout(context.Background(),3*time.Minute)
 defer cancel()
 original:=serviceFixture(t)
 sourceDir:=privateDir(t)
 if err:=os.WriteFile(filepath.Join(sourceDir,"important.txt"),[]byte("restore-me"),0600);err!=nil{t.Fatal(err)}
 source,err:=original.Create(ctx,CreateSource{Name:"Legacy office",Kind:"local",Path:sourceDir,IntervalMinutes:60})
 if err!=nil{t.Fatal(err)}
 if err=original.Manual(ctx,source.ID);err!=nil{t.Fatal(err)}
 if ran,err:=original.scheduler.RunDue(ctx);err!=nil||!ran{t.Fatalf("original backup failed: %v %v",ran,err)}
 var data bytes.Buffer
 if err=original.ExportRecoveryKit(ctx,&data);err!=nil{t.Fatal(err)}
 fresh:=serviceFixture(t)
 freshRoot:=fresh.catalog.DataDir
 freshID:=fresh.catalog.InstanceID
 if _,err=ApplyPreparedRecovery(ctx,freshRoot);err!=nil{t.Fatal(err)}
 info,err:=fresh.PrepareRecoveryKit(ctx,bytes.NewReader(data.Bytes()))
 if err!=nil{t.Fatal(err)}
 if info.InstanceID!=original.catalog.InstanceID{t.Fatal("restored identity mismatch")}
 if _,err=fresh.PrepareRecoveryKit(ctx,bytes.NewReader(data.Bytes()));err==nil{t.Fatal("second pending upload accepted")}
 if _,err=os.Stat(filepath.Join(freshRoot,".recovery-kit-apply.json"));err!=nil{t.Fatal("pending restart request missing",err)}
 if err=fresh.catalog.Close();err!=nil{t.Fatal(err)}
 applied,err:=ApplyPreparedRecovery(ctx,freshRoot)
 if err!=nil||!applied{t.Fatalf("atomic apply failed: %v (applied=%v)",err,applied)}
 if applied,err=ApplyPreparedRecovery(ctx,freshRoot);err!=nil||applied{t.Fatalf("request repeated: applied %v err %v",applied,err)}
 rolledBack,err:=catalog.Open(freshRoot+".before-recovery")
 if err!=nil{t.Fatal(err)}
 if rolledBack.InstanceID!=freshID{t.Fatal("original empty installation was not retained")}
 _=rolledBack.Close()
 restored,err:=catalog.Open(freshRoot)
 if err!=nil{t.Fatal(err)}
 defer restored.Close()
 if restored.InstanceID!=original.catalog.InstanceID{t.Fatal("did not activate recovered catalog")}
 var count int
 if err=restored.DB.QueryRow("SELECT COUNT(*) FROM file_snapshots").Scan(&count);err!=nil||count!=1{t.Fatalf("snapshot catalog lost: %d %v",count,err)}
 var active int
 if err=restored.DB.QueryRow("SELECT enabled FROM schedule_entries WHERE id=?",source.ID).Scan(&active);err!=nil||active!=0{t.Fatalf("dangerous restored schedule resumed automatically: %d %v",active,err)}
 recoveredVault,err:=vault.Open(restored);if err!=nil{t.Fatalf("cannot use old Vault key: %v",err)}
 recoveredSvc,err:=New(ctx,restored,recoveredVault,"","")
 if err!=nil{t.Fatalf("cannot open new instance with missing original primary disk: %v",err)}
 if _,err=recoveredSvc.repositoryFor(ctx,PrimaryStorageID);err==nil{t.Fatal("old primary unexpectedly regenerated")}
 if _,err=os.Stat(filepath.Join(freshRoot,"repository"));!os.IsNotExist(err){t.Fatalf("primary repository incorrectly created: %v",err)}
}

func TestBrowserKitRejectsExistingBackupInstallation(t *testing.T){
 if _,err:=exec.LookPath("restic");err!=nil{t.Skip("restic unavailable")}
 svc:=serviceFixture(t)
 ctx:=context.Background()
 dir:=privateDir(t)
 if _,err:=svc.Create(ctx,CreateSource{Name:"Existing source",Kind:"local",Path:dir,IntervalMinutes:60});err!=nil{t.Fatal(err)}
 original:=serviceFixture(t)
 var kit bytes.Buffer
 if err:=original.ExportRecoveryKit(ctx,&kit);err!=nil{t.Fatal(err)}
 if _,err:=svc.PrepareRecoveryKit(ctx,bytes.NewReader(kit.Bytes()));err==nil{t.Fatal("recovery upload overwritten configured instance")}
 if _,err:=os.Stat(filepath.Join(svc.catalog.DataDir,".recovery-kit-apply.json"));!os.IsNotExist(err){t.Fatalf("dangerous pending swap created: %v",err)}
}
