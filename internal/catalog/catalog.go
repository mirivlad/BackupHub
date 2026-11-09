package catalog

import (
 "context"
 "crypto/rand"
 "database/sql"
 "errors"
 "fmt"
 "net/url"
 "os"
 "path/filepath"
 "time"

 _ "modernc.org/sqlite"
)

const SchemaVersion = 5

// Catalog stores operational state; the portable repository manifest must
// remain sufficient for rebuilding the snapshot index after database loss.
type Catalog struct {
 DB *sql.DB
 InstanceID string
 DataDir string
}

func Open(dataDir string) (_ *Catalog, err error) {
 if dataDir=="" {return nil,errors.New("data directory required")}
 if err=os.MkdirAll(dataDir,0700);err!=nil{return nil,err}
 info,err:=os.Lstat(dataDir)
 if err!=nil{return nil,err}
 if !info.IsDir() || info.Mode()&os.ModeSymlink!=0 {
  return nil,errors.New("data directory must be a real directory")
 }
 if info.Mode().Perm()&0077!=0 {
  return nil,errors.New("data directory must have mode 0700")
 }
 dir,err:=filepath.Abs(dataDir)
 if err!=nil{return nil,err}
 dbPath:=filepath.Join(dir,"catalog.db")
 if dbInfo,statErr:=os.Lstat(dbPath);statErr==nil && !dbInfo.Mode().IsRegular(){
  return nil,errors.New("catalog.db must be a regular file")
 }else if statErr!=nil && !os.IsNotExist(statErr) {return nil,statErr}
 u:=url.URL{Scheme:"file",Path:filepath.ToSlash(dbPath)}
 db,err:=sql.Open("sqlite",u.String())
 if err!=nil{return nil,err}
 defer func(){if err!=nil{_ =db.Close()}}()
 db.SetMaxOpenConns(1)
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
 defer cancel()
 for _,statement:=range []string{"PRAGMA busy_timeout=5000","PRAGMA foreign_keys=ON","PRAGMA journal_mode=WAL"} {
  if _,err=db.ExecContext(ctx,statement);err!=nil{return nil,fmt.Errorf("sqlite setup: %w",err)}
 }
 if err=migrate(ctx,db);err!=nil{return nil,err}
 if err=os.Chmod(dbPath,0600);err!=nil{return nil,err}
 var id string
 if err=db.QueryRowContext(ctx,"SELECT instance_id FROM instance WHERE singleton = 1").Scan(&id);err!=nil{return nil,err}
 return &Catalog{DB:db,InstanceID:id,DataDir:dir},nil
}

func (c *Catalog) Close() error {return c.DB.Close()}

func migrate(ctx context.Context, db *sql.DB) error {
 var version int
 if err:=db.QueryRowContext(ctx,"PRAGMA user_version").Scan(&version);err!=nil{return err}
 if version>SchemaVersion {return fmt.Errorf("catalog schema %d newer than supported %d",version,SchemaVersion)}
 if version==SchemaVersion{return nil}
 tx,err:=db.BeginTx(ctx,nil);if err!=nil{return err}
 defer tx.Rollback()

 if version<1 {
  statements:=[]string{
   "CREATE TABLE IF NOT EXISTS instance (singleton INTEGER PRIMARY KEY CHECK (singleton=1), instance_id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL)",
   "CREATE TABLE IF NOT EXISTS sources (id TEXT PRIMARY KEY, kind TEXT NOT NULL, display_name TEXT NOT NULL, endpoint TEXT NOT NULL, secret_ref TEXT, settings_json TEXT NOT NULL, created_at TEXT NOT NULL)",
   "CREATE TABLE IF NOT EXISTS datasets (id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES sources(id), display_name TEXT NOT NULL, locator TEXT NOT NULL)",
   "CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, dataset_id TEXT NOT NULL REFERENCES datasets(id), schedule_json TEXT NOT NULL, created_at TEXT NOT NULL)",
   "CREATE TABLE IF NOT EXISTS job_runs (id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id), state TEXT NOT NULL, started_at TEXT, finished_at TEXT, failure_code TEXT)",
   "CREATE TABLE IF NOT EXISTS snapshots (id TEXT PRIMARY KEY, dataset_id TEXT NOT NULL REFERENCES datasets(id), repository_id TEXT NOT NULL, state TEXT NOT NULL, manifest_ref TEXT NOT NULL, created_at TEXT NOT NULL)",
   "CREATE TABLE IF NOT EXISTS audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, occurred_at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, resource TEXT NOT NULL, metadata_json TEXT NOT NULL)",
   "CREATE INDEX IF NOT EXISTS snapshots_by_dataset_time ON snapshots(dataset_id,created_at DESC)",
   "CREATE INDEX IF NOT EXISTS runs_by_job ON job_runs(job_id,started_at DESC)",
  }
  for _,statement:=range statements {if _,err=tx.ExecContext(ctx,statement);err!=nil{return fmt.Errorf("v1 migration: %w",err)}}
  id,idErr:=newUUID();if idErr!=nil{return idErr}
  if _,err=tx.ExecContext(ctx,"INSERT OR IGNORE INTO instance(singleton,instance_id,created_at) VALUES(1,?,?)",id,time.Now().UTC().Format(time.RFC3339Nano));err!=nil{return err}
 }

 if version<2 {
  statements:=[]string{
   "CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('administrator','backup_operator','restore_operator','auditor','viewer')), disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)), created_at TEXT NOT NULL)",
   "CREATE TABLE bootstrap (singleton INTEGER PRIMARY KEY CHECK(singleton=1), token_sha256 BLOB NOT NULL, expires_unix INTEGER NOT NULL)",
   "CREATE TABLE sessions (token_sha256 BLOB PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, csrf_sha256 BLOB NOT NULL, expires_unix INTEGER NOT NULL, created_at TEXT NOT NULL)",
   "CREATE INDEX sessions_by_expiry ON sessions(expires_unix)",
   "CREATE TABLE vault_secrets (name TEXT PRIMARY KEY, version INTEGER NOT NULL, nonce BLOB NOT NULL, ciphertext BLOB NOT NULL, updated_at TEXT NOT NULL)",
   "CREATE INDEX audit_recent ON audit_events(id DESC)",
  }
  for _,statement:=range statements {if _,err=tx.ExecContext(ctx,statement);err!=nil{return fmt.Errorf("v2 migration: %w",err)}}
 }
 if version<3 {
  if err=migrateSchedulerV3(ctx,tx);err!=nil{return err}
 }
 if version<4 {
  statements:=[]string{
   `CREATE TABLE file_snapshots (
    snapshot_id TEXT PRIMARY KEY REFERENCES snapshots(id) ON DELETE RESTRICT,
    source_id TEXT NOT NULL REFERENCES sources(id),
    restic_id TEXT NOT NULL,
    root_path TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    verification TEXT NOT NULL,
    fidelity TEXT NOT NULL,
    consistency TEXT NOT NULL
   )`,
   "CREATE INDEX file_snapshots_by_source ON file_snapshots(source_id,committed_at DESC)",
  }
  for _,statement:=range statements {if _,err=tx.ExecContext(ctx,statement);err!=nil{return fmt.Errorf("v4 migration: %w",err)}}
 }
 if version<5 {
  statements:=[]string{
   `CREATE TABLE storage_repositories (
      id TEXT PRIMARY KEY,
      display_name TEXT NOT NULL UNIQUE,
      path TEXT NOT NULL UNIQUE,
      mount_path TEXT NOT NULL DEFAULT '',
      created_at TEXT NOT NULL
   )`,
   `INSERT INTO storage_repositories(id,display_name,path,created_at)
     VALUES('primary','Встроенное хранилище','',CURRENT_TIMESTAMP)`,
   `CREATE TABLE schedule_repositories (
      schedule_id TEXT PRIMARY KEY REFERENCES schedule_entries(id) ON DELETE RESTRICT,
      repository_id TEXT NOT NULL REFERENCES storage_repositories(id) ON DELETE RESTRICT
   )`,
   `INSERT INTO schedule_repositories(schedule_id,repository_id) SELECT id,'primary' FROM schedule_entries WHERE kind='backup'`,
  }
  for _,statement:=range statements {if _,err=tx.ExecContext(ctx,statement);err!=nil{return fmt.Errorf("v5 storage migration: %w",err)}}
 }
 if _,err=tx.ExecContext(ctx,fmt.Sprintf("PRAGMA user_version=%d",SchemaVersion));err!=nil{return err}
 return tx.Commit()
}

func newUUID()(string,error){
 b:=make([]byte,16)
 if _,err:=rand.Read(b);err!=nil{return "",err}
 b[6]=(b[6]&0x0f)|0x40
 b[8]=(b[8]&0x3f)|0x80
 return fmt.Sprintf("%x-%x-%x-%x-%x",b[0:4],b[4:6],b[6:8],b[8:10],b[10:16]),nil
}
