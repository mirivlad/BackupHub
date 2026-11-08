package catalog

import (
 "context"
 "os"
 "path/filepath"
 "testing"
)

func TestOpenPersistsIdentity(t *testing.T) {
 dir:=t.TempDir()
 first,err:=Open(dir);if err!=nil{t.Fatal(err)}
 id:=first.InstanceID
 if id=="" {t.Fatal("empty instance ID")}
 if err=first.Close();err!=nil{t.Fatal(err)}
 second,err:=Open(dir);if err!=nil{t.Fatal(err)}
 defer second.Close()
 if second.InstanceID!=id {t.Fatalf("identity changed: %s vs %s",id,second.InstanceID)}
 var ver int
 if err=second.DB.QueryRowContext(context.Background(),"PRAGMA user_version").Scan(&ver);err!=nil{t.Fatal(err)}
 if ver!=SchemaVersion {t.Fatalf("schema version: %d",ver)}
 for _,table:=range []string{"users","sessions","bootstrap","vault_secrets"}{
  var name string
  if err=second.DB.QueryRowContext(context.Background(),"SELECT name FROM sqlite_master WHERE type='table' AND name=?",table).Scan(&name);err!=nil{t.Errorf("%s missing: %v",table,err)}
 }
}
func TestRejectsMissingDir(t *testing.T) {
 if _,err:=Open("");err==nil{t.Fatal("missing directory accepted")}
}
func TestRejectsLooseDir(t *testing.T) {
 dir:=filepath.Join(t.TempDir(),"loose")
 if err:=os.Mkdir(dir,0755);err!=nil{t.Fatal(err)}
 if _,err:=Open(dir);err==nil{t.Fatal("accepted public state directory")}
}
func TestRejectsCatalogSymlink(t *testing.T){
 dir:=t.TempDir()
 target:=filepath.Join(t.TempDir(),"target")
 if err:=os.WriteFile(target,[]byte("data"),0600);err!=nil{t.Fatal(err)}
 if err:=os.Symlink(target,filepath.Join(dir,"catalog.db"));err!=nil{t.Fatal(err)}
 if _,err:=Open(dir);err==nil{t.Fatal("accepted symlink")}
}
