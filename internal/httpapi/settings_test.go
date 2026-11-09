package httpapi

import (
 "context"
 "archive/zip"
 "bytes"
 "net/http"
 "net/http/httptest"
 "os"
 "strings"
 "testing"

 "github.com/mirivlad/BackupHub/internal/auth"
 "github.com/mirivlad/BackupHub/internal/backup"
 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/vault"
)

func TestSettingsAuthenticationCSRFAndPersistence(t *testing.T){
 dir:=t.TempDir()
 if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 cat,err:=catalog.Open(dir);if err!=nil{t.Fatal(err)}
 defer cat.Close()
 if err=vault.Initialize(cat);err!=nil{t.Fatal(err)}
 v,err:=vault.Open(cat);if err!=nil{t.Fatal(err)}
 // Restic-backed service is needed to exercise the real HTTP handler.
 svc,err:=backup.New(context.Background(),cat,v,"","")
 if err!=nil{
  if os.Getenv("BACKUPHUB_RESTIC_REQUIRED")=="1"{t.Fatal(err)}
  t.Skipf("restic unavailable: %v",err)
 }
 h:=NewWithBackup(cat,v,svc)
 path:="/api/v1/system/settings"
 if got:=request(t,h,"GET",path,"").Code;got!=401{t.Fatalf("unauthenticated GET: %d",got)}
 token,_,err:=auth.New(cat).IssueBootstrap(context.Background());if err!=nil{t.Fatal(err)}
 if _,err=auth.New(cat).CreateAdmin(context.Background(),token,"admin","a-long-correct-test-password");err!=nil{t.Fatal(err)}
 login:=request(t,h,"POST","/api/v1/auth/login",`{"username":"admin","password":"a-long-correct-test-password"}`)
 if login.Code!=200{t.Fatalf("login: %d %s",login.Code,login.Body.String())}
 var session,csrf *http.Cookie
 for _,cookie:=range login.Result().Cookies(){
  if cookie.Name==sessionCookie{session=cookie}
  if cookie.Name==csrfCookie{csrf=cookie}
 }
 if session==nil||csrf==nil{t.Fatal("missing cookies")}
 if got:=request(t,h,"GET",path,"",session).Code;got!=200{t.Fatalf("admin settings GET: %d",got)}
 if got:=request(t,h,"GET","/api/v1/system/recovery-kit","").Code;got!=401{t.Fatalf("unprivileged kit download: %d",got)}
 kitResponse:=request(t,h,"GET","/api/v1/system/recovery-kit","",session)
 if kitResponse.Code!=200{t.Fatalf("recovery kit download failed: %d %s",kitResponse.Code,kitResponse.Body.String())}
 kit,err:=zip.NewReader(bytes.NewReader(kitResponse.Body.Bytes()),int64(kitResponse.Body.Len()))
 if err!=nil{t.Fatal(err)}
 found:=false
 for _,f:=range kit.File{if f.Name=="recovery-manifest.json"{found=true}}
 if !found{t.Fatal("recovery kit manifest missing")}

 if got:=request(t,h,"POST",path,`{"default_repository_password_mode":"no_password"}`,session).Code;got!=403{t.Fatalf("missing CSRF allowed: %d",got)}
 send:=func(body string)*httptest.ResponseRecorder{
  req:=httptest.NewRequest(http.MethodPost,path,strings.NewReader(body))
  req.Header.Set("Content-Type","application/json")
  req.Header.Set("X-BackupHub-CSRF",csrf.Value)
  req.AddCookie(session);req.AddCookie(csrf)
  result:=httptest.NewRecorder()
  h.ServeHTTP(result,req)
  return result
 }
 if got:=send(`{"default_repository_password_mode":"plaintext"}`).Code;got!=400{t.Fatalf("invalid setting accepted: %d",got)}
 if got:=send(`{"default_repository_password_mode":"no_password","other":"x"}`).Code;got!=400{t.Fatalf("unexpected JSON accepted: %d",got)}
 if res:=send(`{"default_repository_password_mode":"no_password"}`);res.Code!=200{t.Fatalf("save: %d %s",res.Code,res.Body.String())}
 got:=request(t,h,"GET",path,"",session)
 if got.Code!=200||!strings.Contains(got.Body.String(),`"no_password"`){t.Fatalf("default not reflected: %s",got.Body.String())}
 if mode,err:=svc.DefaultRepositoryPasswordMode(context.Background());err!=nil||mode!=backup.NoPasswordMode{t.Fatalf("default not stored: %s %v",mode,err)}
}
