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
 // Mirror the actual v1 schema, including sources used by v5 migration.
 for _,stmt:=range []string{
  "CREATE TABLE sources (id TEXT PRIMARY KEY, kind TEXT NOT NULL, display_name TEXT NOT NULL, endpoint TEXT NOT NULL, secret_ref TEXT, settings_json TEXT NOT NULL, created_at TEXT NOT NULL)",
  "CREATE TABLE datasets (id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES sources(id), display_name TEXT NOT NULL, locator TEXT NOT NULL)",
  "CREATE TABLE jobs (id TEXT PRIMARY KEY, dataset_id TEXT NOT NULL REFERENCES datasets(id), schedule_json TEXT NOT NULL, created_at TEXT NOT NULL)",
  "CREATE TABLE job_runs (id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id), state TEXT NOT NULL, started_at TEXT, finished_at TEXT, failure_code TEXT)",
  "CREATE TABLE snapshots (id TEXT PRIMARY KEY, dataset_id TEXT NOT NULL REFERENCES datasets(id), repository_id TEXT NOT NULL, state TEXT NOT NULL, manifest_ref TEXT NOT NULL, created_at TEXT NOT NULL)",
 } {if _,err=raw.Exec(stmt);err!=nil{t.Fatal(err)}}

 if _,err=raw.Exec("CREATE TABLE audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, occurred_at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, resource TEXT NOT NULL, metadata_json TEXT NOT NULL)");err!=nil{t.Fatal(err)}
 if _,err=raw.Exec("INSERT INTO sources(id,kind,display_name,endpoint,settings_json,created_at) VALUES(?,?,?,?,?,?)","legacy-source","local","Old source","","{}","2026-11-01T00:00:00Z");err!=nil{t.Fatal(err)}
 original:="retained-old-instance-id"
 if _,err=raw.Exec("INSERT INTO instance VALUES(1,?,?)",original,"2026-11-01T00:00:00Z");err!=nil{t.Fatal(err)}
 if _,err=raw.Exec("PRAGMA user_version=1");err!=nil{t.Fatal(err)}
 if err=raw.Close();err!=nil{t.Fatal(err)}
 upgraded,err:=Open(dir);if err!=nil{t.Fatal(err)}
 defer upgraded.Close()
 if upgraded.InstanceID!=original{t.Fatal("instance UUID lost during migration")}
 var storageID string
 if err=upgraded.DB.QueryRow("SELECT repository_id FROM source_repositories WHERE source_id='legacy-source'").Scan(&storageID);err!=nil||storageID!="primary"{t.Fatalf("legacy source destination lost: id=%s err=%v",storageID,err)}
 var count int
 if err=upgraded.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='vault_secrets'").Scan(&count);err!=nil||count!=1{t.Fatalf("v2 migration missing: %v",err)}
}
