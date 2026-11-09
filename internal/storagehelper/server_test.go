package storagehelper

import (
 "net/http"
 "net/http/httptest"
 "os"
 "path/filepath"
 "strings"
 "testing"
)

func TestPathValidationAndNoFollow(t *testing.T){
 for _,p:=range []string{"","/", "/etc/../backup","/mnt//disk","/mnt/./disk","mnt/disk"}{
  if literalPath(p){t.Errorf("unsafe path accepted: %q",p)}
 }
 for _,p:=range []string{"/etc","/root","/var/lib","/tmp"}{
  if allowed(p){t.Errorf("system directory allowed: %q",p)}
 }
 for _,p:=range []string{"/mnt/disk1","/backup","/backup2","/srv/backups/nightly"}{
  if !allowed(p){t.Errorf("backup mount disallowed: %q",p)}
 }
 for _,name:=range []string{".","..","../etc","a/b","a b",".hidden",""}{
  if validFolder(name){t.Errorf("unsafe folder accepted %q",name)}
 }
 if !validFolder("BackupHub_2026-01")||!validFolder("РезервныеКопии-2026"){t.Fatal("valid name rejected")}
 dir:=t.TempDir()
 safe:=filepath.Join(dir,"dir")
 if err:=os.Mkdir(safe,0700);err!=nil{t.Fatal(err)}
 fd,err:=openPathNoFollow(safe)
 if err!=nil{t.Fatal(err)}
 _=os.NewFile(uintptr(fd),"dir").Close()
 if err:=os.Symlink("/etc",filepath.Join(dir,"redirect"));err!=nil{t.Fatal(err)}
 if _,err:=openPathNoFollow(filepath.Join(dir,"redirect"));err==nil{t.Fatal("followed symlink")}
}
func TestHelperCannotProvisionSystemDirectories(t *testing.T){
 handler:=NewHandler()
 for _,request:=range []string{
  `{"parent_path":"/etc","folder":"backuphub"}`,
  `{"parent_path":"/mnt/../etc","folder":"backuphub"}`,
  `{"parent_path":"/mnt/disk1","folder":".."}`,
 }{
  r:=httptest.NewRequest(http.MethodPost,"/provision",strings.NewReader(request))
  r.Header.Set("Content-Type","application/json")
  w:=httptest.NewRecorder()
  handler.ServeHTTP(w,r)
  if w.Code==200||w.Code==201{t.Fatalf("unsafe request succeeded: %s",request)}
 }
}
