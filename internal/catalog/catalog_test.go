package catalog

import (
 "context"
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
}
func TestRejectsMissingDir(t *testing.T) {
 if _,err:=Open("");err==nil{t.Fatal("missing directory accepted")}
}
