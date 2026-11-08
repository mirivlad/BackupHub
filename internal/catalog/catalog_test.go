package catalog

import (
 "context"
 "database/sql"
 "os"
 "path/filepath"
 "testing"
)

func TestOpenPersistsIdentity(t *testing.T) {
 dir:=t.TempDir()
 if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
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
 if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 target:=filepath.Join(t.TempDir(),"target")
 if err:=os.WriteFile(target,[]byte("data"),0600);err!=nil{t.Fatal(err)}
 if err:=os.Symlink(target,filepath.Join(dir,"catalog.db"));err!=nil{t.Fatal(err)}
 if _,err:=Open(dir);err==nil{t.Fatal("accepted symlink")}
}

func TestMigrateFromV1PreservesIdentity(t *testing.T){
 dir:=t.TempDir()
 if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 raw,err:=sql.Open("sqlite",filepath.Join(dir,"catalog.db"));if err!=nil{t.Fatal(err)}
 if _,err=raw.Exec("CREATE TABLE instance (singleton INTEGER PRIMARY KEY, instance_id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL)");err!=nil{t.Fatal(err)}
 // Mirror the actual v1 schema needed by the v2 audit index.
 if _,err=raw.Exec("CREATE TABLE audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, occurred_at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, resource TEXT NOT NULL, metadata_json TEXT NOT NULL)");err!=nil{t.Fatal(err)}
 original:="retained-old-instance-id"
 if _,err=raw.Exec("INSERT INTO instance VALUES(1,?,?)",original,"2026-11-01T00:00:00Z");err!=nil{t.Fatal(err)}
 if _,err=raw.Exec("PRAGMA user_version=1");err!=nil{t.Fatal(err)}
 if err=raw.Close();err!=nil{t.Fatal(err)}
 upgraded,err:=Open(dir);if err!=nil{t.Fatal(err)}
 defer upgraded.Close()
 if upgraded.InstanceID!=original{t.Fatal("instance UUID lost during migration")}
 var count int
 if err=upgraded.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='vault_secrets'").Scan(&count);err!=nil||count!=1{t.Fatalf("v2 migration missing: %v",err)}
}
