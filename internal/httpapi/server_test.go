package httpapi

import (
 "bytes"
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "os"
 "testing"

 "github.com/mirivlad/BackupHub/internal/auth"
 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/vault"
)

func testServer(t *testing.T)(http.Handler,*catalog.Catalog){
 t.Helper()
 dir:=t.TempDir();if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 db,err:=catalog.Open(dir);if err!=nil{t.Fatal(err)}
 if err=vault.Initialize(db);err!=nil{t.Fatal(err)}
 v,err:=vault.Open(db);if err!=nil{t.Fatal(err)}
 return New(db,v),db
}
func request(t *testing.T,h http.Handler,method,url,body string,cookies ...*http.Cookie)*httptest.ResponseRecorder{
 t.Helper()
 r:=httptest.NewRequest(method,url,bytes.NewBufferString(body))
 if body!=""{r.Header.Set("Content-Type","application/json")}
 for _,cookie:=range cookies{r.AddCookie(cookie)}
 w:=httptest.NewRecorder();h.ServeHTTP(w,r);return w
}
func TestHealthShowsNotReady(t *testing.T){
 h,db:=testServer(t);defer db.Close()
 res:=request(t,h,http.MethodGet,"/api/v1/health","")
 if res.Code!=200{t.Fatalf("status %d",res.Code)}
 var body struct{BackupsAvailable bool `json:"backups_available"`}
 if err:=json.Unmarshal(res.Body.Bytes(),&body);err!=nil{t.Fatal(err)}
 if body.BackupsAvailable{t.Fatal("unimplemented backups reported available")}
}
func TestHealthReadOnly(t *testing.T){
 h,db:=testServer(t);defer db.Close()
 res:=request(t,h,http.MethodPost,"/api/v1/health","")
 if res.Code!=405{t.Fatalf("expected 405, got %d",res.Code)}
}
func TestBootstrapLoginCSRFAndLogout(t *testing.T){
 h,db:=testServer(t);defer db.Close()
 if status:=request(t,h,"GET","/api/v1/system/identity","").Code;status!=401{t.Fatalf("identity leaked: %d",status)}
 key,_,err:=auth.New(db).IssueBootstrap(context.Background());if err!=nil{t.Fatal(err)}
 body,_:=json.Marshal(map[string]string{"bootstrap_token":key,"username":"admin","password":"correct horse battery staple"})
 if status:=request(t,h,"POST","/api/v1/setup/admin",string(body)).Code;status!=201{t.Fatalf("setup: %d",status)}
 login,_:=json.Marshal(map[string]string{"username":"admin","password":"correct horse battery staple"})
 response:=request(t,h,"POST","/api/v1/auth/login",string(login))
 if response.Code!=200{t.Fatalf("login: %d %s",response.Code,response.Body.String())}
 var session,csrf *http.Cookie
 for _,cookie:=range response.Result().Cookies(){
  if cookie.Name==sessionCookie {session=cookie}
  if cookie.Name==csrfCookie {csrf=cookie}
 }
 if session==nil||csrf==nil||!session.HttpOnly||!session.Secure||session.SameSite!=http.SameSiteStrictMode{t.Fatal("insecure session cookies")}
 if status:=request(t,h,"GET","/api/v1/auth/me","",session).Code;status!=200{t.Fatalf("me: %d",status)}
 if status:=request(t,h,"GET","/api/v1/audit","",session).Code;status!=200{t.Fatalf("audit: %d",status)}
 if status:=request(t,h,"POST","/api/v1/auth/logout","",session,csrf).Code;status!=403{t.Fatalf("missing CSRF allowed: %d",status)}
 r:=httptest.NewRequest("POST","/api/v1/auth/logout",nil);r.AddCookie(session);r.AddCookie(csrf)
 r.Header.Set("X-BackupHub-CSRF",csrf.Value)
 w:=httptest.NewRecorder();h.ServeHTTP(w,r)
 if w.Code!=204{t.Fatalf("logout: %d %s",w.Code,w.Body.String())}
 if status:=request(t,h,"GET","/api/v1/auth/me","",session).Code;status!=401{t.Fatalf("revoked session alive: %d",status)}
}
func TestJSONAndRateLimit(t *testing.T){
 h,db:=testServer(t);defer db.Close()
 if status:=request(t,h,"POST","/api/v1/auth/login","{bad}").Code;status!=400{t.Fatalf("bad JSON: %d",status)}
 for i:=0;i<5;i++{_ =request(t,h,"POST","/api/v1/auth/login",`{"username":"x","password":"y"}`)}
 if status:=request(t,h,"POST","/api/v1/auth/login",`{"username":"x","password":"y"}`).Code;status!=429{t.Fatalf("rate limit: %d",status)}
}
