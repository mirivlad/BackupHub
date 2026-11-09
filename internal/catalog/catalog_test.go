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

 var count int
 if err=upgraded.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='vault_secrets'").Scan(&count);err!=nil||count!=1{t.Fatalf("v2 migration missing: %v",err)}
}

func TestV4ToV5PreservesBackupScheduleDestination(t *testing.T){
 dir:=t.TempDir()
 if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 c,err:=Open(dir)
 if err!=nil{t.Fatal(err)}
 _,err=c.DB.Exec("INSERT INTO sources(id,kind,display_name,endpoint,settings_json,created_at) VALUES('source-v4','local','Demo','','{}','2026-11-01T00:00:00Z')")
 if err!=nil{t.Fatal(err)}
 _,err=c.DB.Exec("INSERT INTO datasets(id,source_id,display_name,locator) VALUES('source-v4','source-v4','Demo','/tmp/demo')")
 if err!=nil{t.Fatal(err)}
 _,err=c.DB.Exec("INSERT INTO schedule_entries(id,dataset_id,kind,interval_seconds,next_run_unix,enabled,created_at) VALUES('schedule-v4','source-v4','backup',3600,100,1,'2026-11-01T00:00:00Z')")
 if err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("DROP TABLE system_settings");err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("DROP TABLE schedule_repositories");err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("DROP TABLE storage_repositories");err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("PRAGMA user_version=4");err!=nil{t.Fatal(err)}
 if err=c.Close();err!=nil{t.Fatal(err)}
 migrated,err:=Open(dir)
 if err!=nil{t.Fatal(err)}
 defer migrated.Close()
 var got string
 if err=migrated.DB.QueryRow("SELECT repository_id FROM schedule_repositories WHERE schedule_id='schedule-v4'").Scan(&got);err!=nil||got!="primary"{t.Fatalf("existing task repository lost: %q err=%v",got,err)}
}

func TestV5ToV6DefaultsExistingRepositoriesToProtected(t *testing.T){
 dir:=t.TempDir()
 if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 c,err:=Open(dir);if err!=nil{t.Fatal(err)}
 _,err=c.DB.Exec("INSERT INTO storage_repositories(id,display_name,path,created_at) VALUES('external','old disk','/mnt/old','2026-11-01')")
 if err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("DROP TABLE system_settings");err!=nil{t.Fatal(err)}
 // SQLite cannot remove a column on all supported versions; rebuild
 // precisely the v5 table with its existing data before re-applying v6.
 if _,err=c.DB.Exec(`CREATE TABLE old_repositories (
  id TEXT PRIMARY KEY,display_name TEXT NOT NULL UNIQUE,
  path TEXT NOT NULL UNIQUE,mount_path TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL)`);err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("INSERT INTO old_repositories(id,display_name,path,mount_path,created_at) SELECT id,display_name,path,mount_path,created_at FROM storage_repositories");err!=nil{t.Fatal(err)}
 // The FK from schedule_repositories references storage_repositories.
 // Preserve its data by disabling FK checks while doing the schema swap.
 if _,err=c.DB.Exec("PRAGMA foreign_keys=OFF");err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("DROP TABLE storage_repositories");err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("ALTER TABLE old_repositories RENAME TO storage_repositories");err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("PRAGMA foreign_keys=ON");err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("PRAGMA user_version=5");err!=nil{t.Fatal(err)}
 if err=c.Close();err!=nil{t.Fatal(err)}
 upgraded,err:=Open(dir);if err!=nil{t.Fatal(err)}
 defer upgraded.Close()
 var count int
 if err=upgraded.DB.QueryRow("SELECT COUNT(*) FROM storage_repositories WHERE password_mode='password'").Scan(&count);err!=nil||count!=2{t.Fatalf("legacy encrypted repos changed: %d %v",count,err)}
 var mode string
 if err=upgraded.DB.QueryRow("SELECT default_repository_password_mode FROM system_settings WHERE singleton=1").Scan(&mode);err!=nil||mode!="password"{t.Fatalf("unsafe default after upgrade: %q %v",mode,err)}
}
