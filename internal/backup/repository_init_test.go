package backup

import (
 "context"
 "os"
 "os/exec"
 "path/filepath"
 "testing"
 "time"
)

// Initial Restic setup must survive a killed daemon between writing the key
// and publishing the repository. It must never reformat a lost repository.
func TestInterruptedRepositoryInitialization(t *testing.T) {
 if _,err:=exec.LookPath("restic");err!=nil{t.Skip("restic executable not installed")}
 state:=privateDir(t)
 repoPath:=filepath.Join(state,"repository")
 if err:=os.Mkdir(repoPath,0700);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(state,"restic.init-in-progress"),[]byte("initializing new repository\n"),0600);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(state,"restic.key"),[]byte("correct-test-only-password-123456789\n"),0600);err!=nil{t.Fatal(err)}
 ctx,cancel:=context.WithTimeout(context.Background(),2*time.Minute)
 defer cancel()
 repo,err:=initRepository(ctx,state,repoPath)
 if err!=nil{t.Fatal(err)}
 if _,err=os.Stat(filepath.Join(repoPath,"config"));err!=nil{t.Fatal(err)}
 if _,err=os.Stat(filepath.Join(state,"restic.init-in-progress"));!os.IsNotExist(err){t.Fatalf("initialization marker not removed: %v",err)}
 again,err:=initRepository(ctx,state,repoPath)
 if err!=nil{t.Fatalf("reopen failed: %v",err)}
 if repo.path!=again.path{t.Fatal("repository location changed after reopen")}
}
func TestLostRepositoryIsNeverFormatted(t *testing.T){
 if _,err:=exec.LookPath("restic");err!=nil{t.Skip("restic executable not installed")}
 state:=privateDir(t)
 repoPath:=filepath.Join(state,"repository")
 if err:=os.Mkdir(repoPath,0700);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(state,"restic.key"),[]byte("previous-backup-repository-key\n"),0600);err!=nil{t.Fatal(err)}
 if _,err:=initRepository(context.Background(),state,repoPath);err==nil{t.Fatal("silently initialized missing repository config with existing key")}
 if _,err:=os.Stat(filepath.Join(repoPath,"config"));!os.IsNotExist(err){t.Fatalf("new config unexpectedly created: %v",err)}
}
