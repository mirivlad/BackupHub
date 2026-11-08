package auth

import (
 "crypto/rand"
 "crypto/subtle"
 "encoding/base64"
 "errors"
 "fmt"
 "strings"

 "golang.org/x/crypto/argon2"
)

const (
 passwordMemory = 64 * 1024 // KiB
 passwordPasses = 3
 passwordThreads = 2
 passwordSaltBytes = 16
 passwordHashBytes = 32
)
var ErrInvalidCredentials = errors.New("invalid credentials")

func validPassword(password string)error{
 if len(password)<12{return errors.New("password must have at least 12 UTF-8 bytes")}
 if len(password)>1024{return errors.New("password is too long")}
 return nil
}
func passwordHash(password string)(string,error){
 if err:=validPassword(password);err!=nil{return "",err}
 salt:=make([]byte,passwordSaltBytes)
 if _,err:=rand.Read(salt);err!=nil{return "",err}
 derived:=argon2.IDKey([]byte(password),salt,passwordPasses,passwordMemory,passwordThreads,passwordHashBytes)
 return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",passwordMemory,passwordPasses,passwordThreads,
  base64.RawStdEncoding.EncodeToString(salt),base64.RawStdEncoding.EncodeToString(derived)),nil
}
func passwordMatches(encoded,password string)bool {
 parts:=strings.Split(encoded,"$")
 if len(parts)!=6||parts[0]!=""||parts[1]!="argon2id"||parts[2]!="v=19"{return false}
 var mem,passes,threads uint32
 if _,err:=fmt.Sscanf(parts[3],"m=%d,t=%d,p=%d",&mem,&passes,&threads);err!=nil{return false}
 if mem!=passwordMemory||passes!=passwordPasses||threads!=passwordThreads{return false}
 salt,err:=base64.RawStdEncoding.DecodeString(parts[4]);if err!=nil||len(salt)!=passwordSaltBytes{return false}
 expected,err:=base64.RawStdEncoding.DecodeString(parts[5]);if err!=nil||len(expected)!=passwordHashBytes{return false}
 actual:=argon2.IDKey([]byte(password),salt,passes,mem,uint8(threads),passwordHashBytes)
 return subtle.ConstantTimeCompare(actual,expected)==1
}
