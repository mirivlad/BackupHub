package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/vault"
)

func privateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func serviceFixture(t *testing.T) *Service {
	t.Helper()
	state := privateDir(t)
	c, err := catalog.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err = vault.Initialize(c); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(context.Background(), c, v, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
func TestFileBackupVersionHistoryAndZipRestore(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		if os.Getenv("BACKUPHUB_RESTIC_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skip("restic not installed")
	}
	svc := serviceFixture(t)
	src := privateDir(t)
	if err := os.Mkdir(filepath.Join(src, "Video"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Video", "clip.txt"), []byte("first version"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	entry, err := svc.Create(ctx, CreateSource{Name: "Home test", Kind: "local", Path: src, IntervalMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Manual(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	ran, err := svc.scheduler.RunDue(ctx)
	if err != nil || !ran {
		t.Fatalf("first run %v ran=%v", err, ran)
	}
	snapshots, err := svc.ListSnapshots(ctx, entry.ID)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots %v count=%d", err, len(snapshots))
	}
	original := snapshots[0]
	files, err := svc.Files(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no file inventory")
	}
	var zipBytes bytes.Buffer
	recorder := httptest.NewRecorder()
	if err = svc.Download(ctx, original.ID, []string{"Video"}, false, recorder); err != nil {
		t.Fatal(err)
	}
	zipBytes.Write(recorder.Body.Bytes())
	zr, err := zip.NewReader(bytes.NewReader(zipBytes.Bytes()), int64(zipBytes.Len()))
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, member := range zr.File {
		if member.Name == "Video/clip.txt" {
			reader, err := member.Open()
			if err != nil {
				t.Fatal(err)
			}
			bytes, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(bytes) != "first version" {
				t.Fatalf("bad backup %q", bytes)
			}
			ok = true
		}
	}
	if !ok {
		t.Fatal("selected folder missing from ZIP")
	}
	if err = os.Remove(filepath.Join(src, "Video", "clip.txt")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(src, "Video", "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = svc.Manual(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	ran, err = svc.scheduler.RunDue(ctx)
	if err != nil || !ran {
		t.Fatalf("second run %v ran=%v", err, ran)
	}
	snapshots, err = svc.ListSnapshots(ctx, entry.ID)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("expected historical snapshots %v len=%d", err, len(snapshots))
	}
	destination, err := svc.RestoreToNewDirectory(ctx, original.ID, []string{"Video/clip.txt"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(destination, "Video", "clip.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "first version" {
		t.Fatalf("restore differs: %q", restored)
	}
	// There is NO in-place overwrite of the source.
	if _, err = os.Stat(filepath.Join(src, "Video", "clip.txt")); !os.IsNotExist(err) {
		t.Fatalf("source modified during restore: %v", err)
	}
}
func TestSourceValidationNoEscape(t *testing.T) {
	for _, val := range []string{"../etc", "/etc/passwd", "a/../b", ".\\x"} {
		if _, err := cleanRel(val); err == nil {
			t.Errorf("accepted invalid path %q", val)
		}
	}
	root := privateDir(t)
	if _, err := safeLocal(root, "../../oops"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := validateInput(CreateSource{Kind: "local", Path: root}, filepath.Join(root, "repo"), root, filepath.Join(root, "stage")); err == nil {
		t.Fatal("source includes state allowed")
	}
}
func TestRestoreRejectsNonexistent(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic required")
	}
	svc := serviceFixture(t)
	_, err := svc.RestoreToNewDirectory(context.Background(), "no-such-snapshot", []string{".."})
	if err == nil {
		t.Fatal("accepted invalid snapshot")
	}
}
func TestNoInsecureSourceConfig(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil { t.Skip("restic not installed") }
	svc := serviceFixture(t)
	source, err := svc.Create(context.Background(), CreateSource{Name: "Postgres", Kind: "postgres", Host: "db.example", Username: "readonly", Password: "test-secret", Database: "db", IntervalMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := svc.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != source.ID {
		t.Fatal("source not saved")
	}
	var config string
	if err = svc.catalog.DB.QueryRow("SELECT settings_json FROM sources WHERE id=?", source.ID).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "test-secret") {
		t.Fatal("secret stored in settings")
	}
}

func TestMultipleRepositoriesAndHistoricalRestore(t *testing.T){
 if _,err:=exec.LookPath("restic");err!=nil{
  if os.Getenv("BACKUPHUB_RESTIC_REQUIRED")=="1"{t.Fatal(err)}
  t.Skip("restic unavailable")
 }
 svc:=serviceFixture(t)
 ctx,cancel:=context.WithTimeout(context.Background(),4*time.Minute)
 defer cancel()
 storagePath:=filepath.Join(privateDir(t),"repo")
 alt,err:=svc.RegisterStorage(ctx,"Archive disk 2",storagePath)
 if err!=nil{t.Fatal(err)}
 if alt.ID=="primary"||alt.Path!=storagePath{t.Fatalf("incorrect alternate storage %+v",alt)}
 storages,err:=svc.ListStorages(ctx)
 if err!=nil||len(storages)!=2{t.Fatalf("expected 2 storages, got %v: %v",len(storages),err)}
 dir:=privateDir(t)
 if err=os.WriteFile(filepath.Join(dir,"save.txt"),[]byte("old-storage"),0600);err!=nil{t.Fatal(err)}
 first,err:=svc.Create(ctx,CreateSource{Name:"History test",Kind:"local",Path:dir,RepositoryID:"primary",IntervalMinutes:60})
 if err!=nil{t.Fatal(err)}
 if err=svc.Manual(ctx,first.ID);err!=nil{t.Fatal(err)}
 if ran,err:=svc.scheduler.RunDue(ctx);err!=nil||!ran{t.Fatalf("first run failed %v, %v",ran,err)}
 if err=svc.SelectSourceStorage(ctx,first.ID,alt.ID);err!=nil{t.Fatal(err)}
 if err=os.WriteFile(filepath.Join(dir,"save.txt"),[]byte("new-storage"),0600);err!=nil{t.Fatal(err)}
 if err=svc.Manual(ctx,first.ID);err!=nil{t.Fatal(err)}
 if ran,err:=svc.scheduler.RunDue(ctx);err!=nil||!ran{t.Fatalf("second run failed %v, %v",ran,err)}
 history,err:=svc.ListSnapshots(ctx,first.ID)
 if err!=nil||len(history)!=2{t.Fatalf("history: %v, %v",history,err)}
 if history[0].RepositoryID!=alt.ID||history[1].RepositoryID!="primary"{t.Fatalf("repository references swapped: %+v",history)}
 for _,snapshot:=range history{
  recorder:=httptest.NewRecorder()
  if err=svc.Download(ctx,snapshot.ID,[]string{"save.txt"},true,recorder);err!=nil{t.Fatal(err)}
  expected:="new-storage"
  if snapshot.RepositoryID=="primary"{expected="old-storage"}
  if recorder.Body.String()!=expected{t.Fatalf("bad snapshot on %s: %s",snapshot.RepositoryID,recorder.Body.String())}
 }
 // Removal of secondary mount must not create a replacement on the root disk.
 moved:=storagePath+".detached"
 if err=os.Rename(storagePath,moved);err!=nil{t.Fatal(err)}
 if _,err=svc.Files(ctx,history[0].ID);err==nil{t.Fatal("storage outage silently used an empty fallback")}
 if _,err=os.Stat(storagePath);!os.IsNotExist(err){t.Fatalf("detached repo path re-created unexpectedly: %v",err)}
 // Historical original snapshot from primary remains independently restorable.
 if _,err=svc.RestoreToNewDirectory(ctx,history[1].ID,[]string{"save.txt"});err!=nil{t.Fatal(err)}
}

func TestPasswordlessStorageSurvivesLossOfBackupHubState(t *testing.T){
 if _,err:=exec.LookPath("restic");err!=nil{
  if os.Getenv("BACKUPHUB_RESTIC_REQUIRED")=="1"{t.Fatal(err)}
  t.Skip("restic unavailable")
 }
 svc:=serviceFixture(t)
 ctx,cancel:=context.WithTimeout(context.Background(),4*time.Minute)
 defer cancel()
 if mode,err:=svc.DefaultRepositoryPasswordMode(ctx);err!=nil||mode!=PasswordMode{t.Fatalf("default should protect repositories: %q %v",mode,err)}
 if err:=svc.SetDefaultRepositoryPasswordMode(ctx,NoPasswordMode);err!=nil{t.Fatal(err)}
 if err:=svc.SetDefaultRepositoryPasswordMode(ctx,"unknown");err==nil{t.Fatal("accepted invalid mode")}
 if mode,err:=svc.DefaultRepositoryPasswordMode(ctx);err!=nil||mode!=NoPasswordMode{t.Fatalf("default not persisted: %q %v",mode,err)}
 repoPath:=filepath.Join(privateDir(t),"repository")
 open,err:=svc.registerStorage(ctx,"Portable disk",repoPath,"",NoPasswordMode)
 if err!=nil{t.Fatal(err)}
 if open.PasswordMode!=NoPasswordMode{t.Fatalf("mode incorrect: %+v",open)}
 keyPath:=filepath.Join(svc.catalog.DataDir,"restic-"+open.ID+".key")
 if _,err=os.Stat(keyPath);!os.IsNotExist(err){t.Fatalf("passwordless storage unexpectedly has a key file: %v",err)}
 items,err:=svc.ListStorages(ctx);if err!=nil{t.Fatal(err)}
 if len(items)!=2||items[1].PasswordMode!=NoPasswordMode{t.Fatalf("incorrect mode in storage list: %+v",items)}
 sourcePath:=privateDir(t)
 if err=os.WriteFile(filepath.Join(sourcePath,"recovery.txt"),[]byte("survived disaster"),0600);err!=nil{t.Fatal(err)}
 src,err:=svc.Create(ctx,CreateSource{Name:"Emergency recovery",Kind:"local",Path:sourcePath,IntervalMinutes:60,RepositoryID:open.ID})
 if err!=nil{t.Fatal(err)}
 if err=svc.Manual(ctx,src.ID);err!=nil{t.Fatal(err)}
 if ran,err:=svc.scheduler.RunDue(ctx);err!=nil||!ran{t.Fatalf("run failed %v %v",ran,err)}
 snaps,err:=svc.ListSnapshots(ctx,src.ID);if err!=nil||len(snaps)!=1{t.Fatalf("snapshots: %v %v",snaps,err)}
 // The repo can be read with only Restic and the disk; not even the
 // BackupHub catalog or any master/repository key is available here.
 read:=exec.CommandContext(ctx,"restic","--repo",repoPath,"--insecure-no-password","snapshots","--json")
 if output,err:=read.CombinedOutput();err!=nil{t.Fatalf("passwordless repo unreadable independently: %v %s",err,output)}
 target:=privateDir(t)
 restore:=exec.CommandContext(ctx,"restic","--repo",repoPath,"--insecure-no-password","restore",snaps[0].ResticID,"--target",target)
 if output,err:=restore.CombinedOutput();err!=nil{t.Fatalf("disaster restore failed: %v %s",err,output)}
 var recovered bool
 err=filepath.WalkDir(target,func(p string,d os.DirEntry,err error)error{
  if err!=nil{return err}
  if !d.IsDir()&&d.Name()=="recovery.txt"{
   data,err:=os.ReadFile(p)
   if err!=nil{return err}
   recovered=string(data)=="survived disaster"
  }
  return nil
 })
 if err!=nil||!recovered{t.Fatalf("original file not restored without BackupHub state: %v",err)}
 // The old primary is still password-protected, and the new repo cannot be
 // accidentally opened using its unrelated encryption key.
 wrong:=exec.CommandContext(ctx,"restic","--repo",repoPath,"--password-file",filepath.Join(svc.catalog.DataDir,"restic.key"),"snapshots")
 if err:=wrong.Run();err==nil{t.Fatal("passwordless repository accepted unrelated key")}
 if _,err:=svc.repositoryFor(ctx,open.ID);err!=nil{t.Fatalf("BackupHub cannot reopen passwordless repo: %v",err)}
 if _,err:=svc.repo.run(ctx,nil,"snapshots","--json");err!=nil{t.Fatalf("legacy encrypted primary broken: %v",err)}
}
