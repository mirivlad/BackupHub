package vault

import (
 "bytes"
 "context"
 "errors"
 "os"
 "path/filepath"
 "testing"

 "github.com/mirivlad/BackupHub/internal/catalog"
)

func TestVaultRoundTripAndTamper(t *testing.T){
 c,err:=catalog.Open(t.TempDir());if err!=nil{t.Fatal(err)};defer c.Close()
 if _,err=Open(c);!errors.Is(err,ErrNotInitialized){t.Fatalf("expected uninitialized, got %v",err)}
 if err=Initialize(c);err!=nil{t.Fatal(err)}
 if err=Initialize(c);err==nil{t.Fatal("master key overwritten")}
 v,err:=Open(c);if err!=nil{t.Fatal(err)}
 secret:=[]byte("smb-password-123")
 if err=v.Put(context.Background(),"smb/domain-user",secret);err!=nil{t.Fatal(err)}
 stored,err:=v.Get(context.Background(),"smb/domain-user")
 if err!=nil || !bytes.Equal(stored,secret){t.Fatalf("roundtrip: %v",err)}
 var blob []byte
 if err=c.DB.QueryRow("SELECT ciphertext FROM vault_secrets WHERE name=?","smb/domain-user").Scan(&blob);err!=nil{t.Fatal(err)}
 if bytes.Contains(blob,secret){t.Fatal("plaintext in database")}
 if _,err=c.DB.Exec("UPDATE vault_secrets SET ciphertext=? WHERE name=?",[]byte("corrupted"),"smb/domain-user");err!=nil{t.Fatal(err)}
 if _,err=v.Get(context.Background(),"smb/domain-user");err==nil{t.Fatal("accepted tampered ciphertext")}
}
func TestMissingKeyFailsClosed(t *testing.T){
 c,err:=catalog.Open(t.TempDir());if err!=nil{t.Fatal(err)};defer c.Close()
 if err=Initialize(c);err!=nil{t.Fatal(err)}
 v,err:=Open(c);if err!=nil{t.Fatal(err)}
 if err=v.Put(context.Background(),"postgre/sql",[]byte("password"));err!=nil{t.Fatal(err)}
 if err=os.Remove(filepath.Join(c.DataDir,keyName));err!=nil{t.Fatal(err)}
 if err=Initialize(c);err==nil{t.Fatal("replaced missing key despite encrypted entries")}
 if _,err=Open(c);!errors.Is(err,ErrNotInitialized){t.Fatalf("wanted key missing: %v",err)}
}
func TestRejectsLooseKeyAndInvalidName(t *testing.T){
 c,err:=catalog.Open(t.TempDir());if err!=nil{t.Fatal(err)};defer c.Close()
 if err=Initialize(c);err!=nil{t.Fatal(err)}
 keyfile:=filepath.Join(c.DataDir,keyName)
 if err=os.Chmod(keyfile,0644);err!=nil{t.Fatal(err)}
 if _,err=Open(c);err==nil{t.Fatal("accepted world readable key")}
 if err=os.Chmod(keyfile,0600);err!=nil{t.Fatal(err)}
 v,err:=Open(c);if err!=nil{t.Fatal(err)}
 if err=v.Put(context.Background(),"../etc/passwd",[]byte("secret"));err==nil{t.Fatal("accepted invalid traversal-like name")}
}
