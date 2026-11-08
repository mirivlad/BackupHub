package auth

import (
 "context"
 "errors"
 "testing"

 "github.com/mirivlad/BackupHub/internal/catalog"
)

func TestBootstrapLoginSessions(t *testing.T) {
 c,err:=catalog.Open(t.TempDir());if err!=nil{t.Fatal(err)};defer c.Close()
 store:=New(c);ctx:=context.Background()
 has,err:=store.HasUsers(ctx);if err!=nil||has{t.Fatalf("unexpected users: %v",err)}
 token,_,err:=store.IssueBootstrap(ctx);if err!=nil{t.Fatal(err)}
 if _,err=store.CreateAdmin(ctx,"bad-token","root","correct horse battery staple");!errors.Is(err,ErrInvalidCredentials){t.Fatalf("bad token: %v",err)}
 user,err:=store.CreateAdmin(ctx,token,"root","correct horse battery staple")
 if err!=nil{t.Fatal(err)}
 if user.Role!="administrator"{t.Fatal("bootstrap not administrator")}
 if _,_,err=store.IssueBootstrap(ctx);!errors.Is(err,ErrAlreadyInitialized){t.Fatalf("second bootstrap: %v",err)}
 if _,err=store.CreateAdmin(ctx,token,"another","long-enough-password");err==nil{t.Fatal("created second admin")}
 if _,err=store.Login(ctx,"root","wrong-password");!errors.Is(err,ErrInvalidCredentials){t.Fatalf("wrong password: %v",err)}
 session,err:=store.Login(ctx,"root","correct horse battery staple");if err!=nil{t.Fatal(err)}
 got,csrfHash,err:=store.Authenticate(ctx,session.Token)
 if err!=nil||got.ID!=user.ID||len(csrfHash)!=32{t.Fatalf("session verification: %v",err)}
 if err=store.Logout(ctx,session.Token,user.ID);err!=nil{t.Fatal(err)}
 if _,_,err=store.Authenticate(ctx,session.Token);!errors.Is(err,ErrInvalidCredentials){t.Fatalf("logged-out session alive: %v",err)}
 audit,err:=store.Audit(ctx,10);if err!=nil{t.Fatal(err)}
 if len(audit)!=3{t.Fatalf("expected admin.created + login + logout, got %d",len(audit))}
}
func TestPasswordHashNotPlaintext(t *testing.T){
 h,err:=passwordHash("a long correct passphrase");if err!=nil{t.Fatal(err)}
 if h=="a long correct passphrase"||!passwordMatches(h,"a long correct passphrase"){t.Fatal("hash mismatch")}
 if passwordMatches(h,"not the password"){t.Fatal("wrong password accepted")}
 if _,err=passwordHash("short");err==nil{t.Fatal("short password accepted")}
}
func TestExpiredBootstrap(t *testing.T){
 c,err:=catalog.Open(t.TempDir());if err!=nil{t.Fatal(err)};defer c.Close()
 store:=New(c);ctx:=context.Background()
 token,_,err:=store.IssueBootstrap(ctx);if err!=nil{t.Fatal(err)}
 if _,err=c.DB.Exec("UPDATE bootstrap SET expires_unix=1");err!=nil{t.Fatal(err)}
 if _,err=store.CreateAdmin(ctx,token,"admin","long enough password");!errors.Is(err,ErrInvalidCredentials){t.Fatalf("expired bootstrap: %v",err)}
}
