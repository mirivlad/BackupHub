package catalog

import (
 "context"
 "crypto/rand"
 "database/sql"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "time"
 "net/url"

 _ "modernc.org/sqlite"
)

const SchemaVersion = 1

// Catalog contains operational state, never the sole copy of snapshot metadata.
type Catalog struct {
 DB *sql.DB
 InstanceID string
}

func Open(dataDir string) (_ *Catalog, err error) {
 if dataDir=="" {return nil,errors.New("data directory required")}
 if err=os.MkdirAll(dataDir,0700);err!=nil{return nil,err}
 info,err:=os.Lstat(dataDir)
 if err!=nil{return nil,err}
 if !info.IsDir() || info.Mode()&os.ModeSymlink!=0 {return nil,errors.New("data directory must not be a symlink")}
 dbPath,err:=filepath.Abs(filepath.Join(dataDir,"catalog.db"))
 if err!=nil{return nil,err}
 u:=url.URL{Scheme:"file",Path:filepath.ToSlash(dbPath)}
 db,err:=sql.Open("sqlite",u.String())
 if err!=nil{return nil,err}
 defer func(){if err!=nil{_ =db.Close()}}()
 db.SetMaxOpenConns(1)
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
 defer cancel()
 for _,statement:=range []string{
  "PRAGMA busy_timeout=5000",
  "PRAGMA foreign_keys=ON",
  "PRAGMA journal_mode=WAL",
 } {
  if _,err=db.ExecContext(ctx,statement);err!=nil{return nil,fmt.Errorf("sqlite setup: %w",err)}
 }
 if err=migrate(ctx,db);err!=nil{return nil,err}
 var id string
 if err=db.QueryRowContext(ctx,"SELECT instance_id FROM instance WHERE singleton = 1").Scan(&id);err!=nil{return nil,err}
 return &Catalog{DB:db,InstanceID:id},nil
}

func (c *Catalog) Close() error {return c.DB.Close()}

func migrate(ctx context.Context, db *sql.DB) error {
 var version int
 if err:=db.QueryRowContext(ctx,"PRAGMA user_version").Scan(&version);err!=nil{return err}
 if version>SchemaVersion {return fmt.Errorf("catalog schema %d newer than supported %d",version,SchemaVersion)}
 if version==SchemaVersion{return nil}
 tx,err:=db.BeginTx(ctx,nil);if err!=nil{return err}
 defer tx.Rollback()
 // v1 includes the persistent entity skeleton; no source passwords are stored here.
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
 for _,statement:=range statements {
  if _,err=tx.ExecContext(ctx,statement);err!=nil{return fmt.Errorf("catalog migration: %w",err)}
 }
 id,err:=newUUID();if err!=nil{return err}
 if _,err=tx.ExecContext(ctx,"INSERT OR IGNORE INTO instance(singleton,instance_id,created_at) VALUES(1,?,?)",id,time.Now().UTC().Format(time.RFC3339Nano));err!=nil{return err}
 if _,err=tx.ExecContext(ctx,"PRAGMA user_version=1");err!=nil{return err}
 return tx.Commit()
}

func newUUID()(string,error){
 b:=make([]byte,16)
 if _,err:=rand.Read(b);err!=nil{return "",err}
 b[6]=(b[6]&0x0f)|0x40
 b[8]=(b[8]&0x3f)|0x80
 return fmt.Sprintf("%x-%x-%x-%x-%x",b[0:4],b[4:6],b[6:8],b[8:10],b[10:16]),nil
}
