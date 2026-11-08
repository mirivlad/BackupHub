// Package vault encrypts source credentials independently of the SQLite
// catalog. A lost master key is NOT regenerated while encrypted secrets exist.
package vault

import (
 "context"
 "crypto/aes"
 "crypto/cipher"
 "crypto/rand"
 "database/sql"
 "errors"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "strings"
 "time"

 "github.com/mirivlad/BackupHub/internal/catalog"
)

const keyName = "master.key"
const formatVersion = 1
var ErrNotInitialized = errors.New("vault master key missing: run backuphub init and keep an offline recovery copy")

type Vault struct {
 db *sql.DB
 key [32]byte
 instanceID string
}

// Initialize creates a random master key only once. It will never replace a
// missing key on an existing vault containing secrets.
func Initialize(c *catalog.Catalog) error {
 var secrets int
 if err:=c.DB.QueryRow("SELECT COUNT(*) FROM vault_secrets").Scan(&secrets);err!=nil{return err}
 if secrets!=0{return errors.New("encrypted secrets exist; refusing to create a replacement master key")}
 keyFile:=filepath.Join(c.DataDir,keyName)
 if _,err:=os.Lstat(keyFile);err==nil{return errors.New("master key already exists")}else if !os.IsNotExist(err){return err}
 var key [32]byte
 if _,err:=io.ReadFull(rand.Reader,key[:]);err!=nil{return err}
 file,err:=os.OpenFile(keyFile,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600)
 if err!=nil{return err}
 created:=false
 defer func(){
  _=file.Close()
  if !created {_=os.Remove(keyFile)}
 }()
 if _,err=file.Write(key[:]);err!=nil{return err}
 if err=file.Sync();err!=nil{return err}
 if err=file.Close();err!=nil{return err}
 created=true
 return nil
}

func Open(c *catalog.Catalog)(*Vault,error){
 keyFile:=filepath.Join(c.DataDir,keyName)
 info,err:=os.Lstat(keyFile)
 if os.IsNotExist(err){return nil,ErrNotInitialized}
 if err!=nil{return nil,err}
 if !info.Mode().IsRegular()||info.Mode().Perm()&0077!=0 {
  return nil,errors.New("master key must be a regular file with permissions 0600")
 }
 data,err:=os.ReadFile(keyFile);if err!=nil{return nil,err}
 if len(data)!=32{return nil,errors.New("master key must be exactly 32 bytes")}
 v:=&Vault{db:c.DB,instanceID:c.InstanceID}
 copy(v.key[:],data)
 return v,nil
}

func (v *Vault) AEAD()(cipher.AEAD,error){
 block,err:=aes.NewCipher(v.key[:]);if err!=nil{return nil,err}
 return cipher.NewGCM(block)
}
func (v *Vault) additionalData(name string) []byte {
 return []byte("BackupHub/vault/v1/"+v.instanceID+"/"+name)
}
func validName(name string)bool {
 if len(name)<1||len(name)>180{return false}
 for _,segment:=range strings.Split(name,"/"){if segment=="."||segment==".."||segment==""{return false}}
 for _,r:=range name {
  if !(r>='a'&&r<='z'||r>='A'&&r<='Z'||r>='0'&&r<='9'||r=='-'||r=='_'||r=='.'||r=='/') {return false}
 }
 return true
}

// Put replaces a named ciphertext. It never persists or logs plaintext.
func (v *Vault) Put(ctx context.Context,name string,plaintext []byte)error{
 if !validName(name){return errors.New("invalid vault secret name")}
 if len(plaintext)==0||len(plaintext)>1048576{return errors.New("secret must be 1 byte to 1 MiB")}
 aead,err:=v.AEAD();if err!=nil{return err}
 nonce:=make([]byte,aead.NonceSize())
 if _,err=io.ReadFull(rand.Reader,nonce);err!=nil{return err}
 ciphertext:=aead.Seal(nil,nonce,plaintext,v.additionalData(name))
 _,err=v.db.ExecContext(ctx,`INSERT INTO vault_secrets(name,version,nonce,ciphertext,updated_at)
 VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET version=excluded.version,nonce=excluded.nonce,
 ciphertext=excluded.ciphertext,updated_at=excluded.updated_at`,
 name,formatVersion,nonce,ciphertext,time.Now().UTC().Format(time.RFC3339Nano))
 return err
}

func (v *Vault) Get(ctx context.Context,name string)([]byte,error){
 if !validName(name){return nil,errors.New("invalid vault secret name")}
 var nonce,ciphertext []byte
 var version int
 err:=v.db.QueryRowContext(ctx,"SELECT version,nonce,ciphertext FROM vault_secrets WHERE name=?",name).Scan(&version,&nonce,&ciphertext)
 if err!=nil{return nil,err}
 if version!=formatVersion{return nil,fmt.Errorf("unsupported vault entry version %d",version)}
 aead,err:=v.AEAD();if err!=nil{return nil,err}
 if len(nonce)!=aead.NonceSize(){return nil,errors.New("invalid vault nonce")}
 plain,err:=aead.Open(nil,nonce,ciphertext,v.additionalData(name))
 if err!=nil{return nil,errors.New("vault decryption failed: wrong key or tampered data")}
 return plain,nil
}

func (v *Vault) Delete(ctx context.Context,name string)error{
 if !validName(name){return errors.New("invalid vault secret name")}
 result,err:=v.db.ExecContext(ctx,"DELETE FROM vault_secrets WHERE name=?",name)
 if err!=nil{return err}
 n,err:=result.RowsAffected()
 if err!=nil{return err}
 if n==0{return sql.ErrNoRows}
 return nil
}
