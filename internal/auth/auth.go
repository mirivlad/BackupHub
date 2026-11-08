// Package auth implements local administrator bootstrap, Argon2id passwords,
// hashed session tokens, CSRF secrets and persisted audit events.
package auth

import (
 "context"
 "crypto/rand"
 "crypto/sha256"
 "crypto/subtle"
 "database/sql"
 "encoding/base64"
 "encoding/json"
 "errors"
 "fmt"
 "regexp"
 "strings"
 "time"

 "github.com/mirivlad/BackupHub/internal/catalog"
)

const BootstrapLifetime=20*time.Minute
const SessionLifetime=7*24*time.Hour
var usernamePattern=regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{2,63}$`)
var ErrAlreadyInitialized=errors.New("administrator already exists")

type Store struct {c *catalog.Catalog}
type User struct {
 ID string `json:"id"`
 Username string `json:"username"`
 Role string `json:"role"`
}
type Session struct {
 User User
 Token string
 CSRF string
 Expires time.Time
}
type AuditEvent struct {
 ID int64 `json:"id"`
 At string `json:"at"`
 Actor string `json:"actor"`
 Action string `json:"action"`
 Resource string `json:"resource"`
 Details json.RawMessage `json:"details"`
}

func New(c *catalog.Catalog)*Store{return &Store{c:c}}
func token() (string,error) {
 b:=make([]byte,32)
 if _,err:=rand.Read(b);err!=nil{return "",err}
 return base64.RawURLEncoding.EncodeToString(b),nil
}
func hashToken(value string)[32]byte{return sha256.Sum256([]byte(value))}
func userID()(string,error){return token()}

func (a *Store) HasUsers(ctx context.Context)(bool,error) {
 var count int
 err:=a.c.DB.QueryRowContext(ctx,"SELECT COUNT(*) FROM users").Scan(&count)
 return count>0,err
}

// IssueBootstrap may be invoked only from the local CLI. The raw token is
// returned exactly once, printed to the invoking operator, and never persisted.
func (a *Store) IssueBootstrap(ctx context.Context)(string,time.Time,error){
 var count int
 err:=a.c.DB.QueryRowContext(ctx,"SELECT COUNT(*) FROM users").Scan(&count)
 if err!=nil{return "",time.Time{},err}
 if count>0{return "",time.Time{},ErrAlreadyInitialized}
 secret,err:=token();if err!=nil{return "",time.Time{},err}
 digest:=hashToken(secret)
 expires:=time.Now().UTC().Add(BootstrapLifetime)
 _,err=a.c.DB.ExecContext(ctx,`INSERT INTO bootstrap(singleton,token_sha256,expires_unix) VALUES(1,?,?)
 ON CONFLICT(singleton) DO UPDATE SET token_sha256=excluded.token_sha256,expires_unix=excluded.expires_unix`,digest[:],expires.Unix())
 if err!=nil{return "",time.Time{},err}
 return secret,expires,nil
}

func (a *Store) CreateAdmin(ctx context.Context,bootstrapToken,username,password string)(User,error){
 var empty User
 if !usernamePattern.MatchString(username){return empty,errors.New("username must have 3-64 ASCII letters, numbers, dots, underscores or hyphens, starting with a letter")}
 if err:=validPassword(password);err!=nil{return empty,err}
 storedHash,err:=passwordHash(password);if err!=nil{return empty,err}
 tx,err:=a.c.DB.BeginTx(ctx,nil);if err!=nil{return empty,err}
 defer tx.Rollback()
 var dbToken []byte
 var expires int64
 err=tx.QueryRowContext(ctx,"SELECT token_sha256,expires_unix FROM bootstrap WHERE singleton=1").Scan(&dbToken,&expires)
 if errors.Is(err,sql.ErrNoRows){return empty,ErrInvalidCredentials}
 if err!=nil{return empty,err}
 digest:=hashToken(bootstrapToken)
 if subtle.ConstantTimeCompare(dbToken,digest[:])!=1||time.Now().UTC().Unix()>=expires{return empty,ErrInvalidCredentials}
 id,err:=userID();if err!=nil{return empty,err}
 user:=User{ID:id,Username:username,Role:"administrator"}
 result,err:=tx.ExecContext(ctx,`INSERT INTO users(id,username,password_hash,role,created_at)
 SELECT ?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM users)`,id,username,storedHash,user.Role,time.Now().UTC().Format(time.RFC3339Nano))
 if err!=nil{return empty,err}
 n,err:=result.RowsAffected();if err!=nil{return empty,err}
 if n!=1{return empty,ErrAlreadyInitialized}
 if _,err=tx.ExecContext(ctx,"DELETE FROM bootstrap WHERE singleton=1");err!=nil{return empty,err}
 if err=a.auditTx(ctx,tx,user.ID,"admin.created","user:"+user.ID);err!=nil{return empty,err}
 if err=tx.Commit();err!=nil{return empty,err}
 return user,nil
}

func (a *Store) Login(ctx context.Context,username,password string)(Session,error){
 var s Session
 if len(username)>64||len(password)>1024{return s,ErrInvalidCredentials}
 var user User
 var passwordHash string
 var disabled int
 err:=a.c.DB.QueryRowContext(ctx,`SELECT id,username,role,password_hash,disabled FROM users WHERE username=?`,username).Scan(&user.ID,&user.Username,&user.Role,&passwordHash,&disabled)
 if err!=nil{return s,ErrInvalidCredentials}
 if disabled!=0||!passwordMatches(passwordHash,password){return s,ErrInvalidCredentials}
 raw,err:=token();if err!=nil{return s,err}
 csrf,err:=token();if err!=nil{return s,err}
 tokenDigest:=hashToken(raw);csrfDigest:=hashToken(csrf)
 s=Session{User:user,Token:raw,CSRF:csrf,Expires:time.Now().UTC().Add(SessionLifetime)}
 tx,err:=a.c.DB.BeginTx(ctx,nil);if err!=nil{return Session{},err}
 defer tx.Rollback()
 if _,err=tx.ExecContext(ctx,`INSERT INTO sessions(token_sha256,user_id,csrf_sha256,expires_unix,created_at)
 VALUES(?,?,?,?,?)`,tokenDigest[:],user.ID,csrfDigest[:],s.Expires.Unix(),time.Now().UTC().Format(time.RFC3339Nano));err!=nil{return Session{},err}
 if err=a.auditTx(ctx,tx,user.ID,"auth.login","session");err!=nil{return Session{},err}
 if err=tx.Commit();err!=nil{return Session{},err}
 return s,nil
}

func (a *Store) Authenticate(ctx context.Context,raw string)(User,[]byte,error){
 var u User
 if len(raw)!=43 {return u,nil,ErrInvalidCredentials}
 digest:=hashToken(raw)
 var csrfDigest []byte
 var expires int64
 var disabled int
 err:=a.c.DB.QueryRowContext(ctx,`SELECT u.id,u.username,u.role,s.csrf_sha256,s.expires_unix,u.disabled
 FROM sessions s JOIN users u ON s.user_id=u.id WHERE s.token_sha256=?`,digest[:]).
 Scan(&u.ID,&u.Username,&u.Role,&csrfDigest,&expires,&disabled)
 if err!=nil||disabled!=0||time.Now().UTC().Unix()>=expires{return User{},nil,ErrInvalidCredentials}
 return u,csrfDigest,nil
}

func (a *Store) Logout(ctx context.Context,raw,userID string)error{
 digest:=hashToken(raw)
 tx,err:=a.c.DB.BeginTx(ctx,nil);if err!=nil{return err}
 defer tx.Rollback()
 result,err:=tx.ExecContext(ctx,"DELETE FROM sessions WHERE token_sha256=? AND user_id=?",digest[:],userID)
 if err!=nil{return err}
 n,err:=result.RowsAffected();if err!=nil{return err}
 if n==0{return ErrInvalidCredentials}
 if err=a.auditTx(ctx,tx,userID,"auth.logout","session");err!=nil{return err}
 return tx.Commit()
}
func (a *Store) auditTx(ctx context.Context,tx *sql.Tx,actor,action,resource string)error{
 _,err:=tx.ExecContext(ctx,`INSERT INTO audit_events(occurred_at,actor,action,resource,metadata_json)
 VALUES(?,?,?,?,?)`,time.Now().UTC().Format(time.RFC3339Nano),actor,action,resource,"{}")
 return err
}
func (a *Store) Audit(ctx context.Context,limit int)([]AuditEvent,error){
 if limit<1||limit>100{return nil,fmt.Errorf("invalid audit page size")}
 rows,err:=a.c.DB.QueryContext(ctx,`SELECT id,occurred_at,actor,action,resource,metadata_json
 FROM audit_events ORDER BY id DESC LIMIT ?`,limit)
 if err!=nil{return nil,err}
 defer rows.Close()
 events:=make([]AuditEvent,0)
 for rows.Next() {
  var event AuditEvent
  var details string
  if err:=rows.Scan(&event.ID,&event.At,&event.Actor,&event.Action,&event.Resource,&details);err!=nil{return nil,err}
  event.Details=json.RawMessage(details)
  events=append(events,event)
 }
 return events,rows.Err()
}
func (a *Store) DisableUser(ctx context.Context,id string)error{
 if strings.TrimSpace(id)==""{return errors.New("user id required")}
 // Will be exposed in a future admin-only user management API. Disabled
 // users immediately lose session privileges during authentication.
 _,err:=a.c.DB.ExecContext(ctx,"UPDATE users SET disabled=1 WHERE id=?",id)
 return err
}
