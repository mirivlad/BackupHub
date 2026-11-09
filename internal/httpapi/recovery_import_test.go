package httpapi

import (
 "bytes"
 "context"
 "io"
 "mime/multipart"
 "net/http"
 "net/http/httptest"
 "os"
 "path/filepath"
 "testing"

 "github.com/mirivlad/BackupHub/internal/auth"
 "github.com/mirivlad/BackupHub/internal/backup"
 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/vault"
)

func TestBrowserRecoveryUploadRequiresAdminCSRFAndCleanInstallation(t *testing.T){
 ctx:=context.Background()
 origDir:=t.TempDir()
 if err:=os.Chmod(origDir,0700);err!=nil{t.Fatal(err)}
 orig,err:=catalog.Open(origDir);if err!=nil{t.Fatal(err)}
 defer orig.Close()
 if err=vault.Initialize(orig);err!=nil{t.Fatal(err)}
 originalVault,err:=vault.Open(orig);if err!=nil{t.Fatal(err)}
 originalService,err:=backup.New(ctx,orig,originalVault,"","")
 if err!=nil{
  if os.Getenv("BACKUPHUB_RESTIC_REQUIRED")=="1"{t.Fatal(err)}
  t.Skipf("Restic unavailable: %v",err)
 }
 var kit bytes.Buffer
 if err=originalService.ExportRecoveryKit(ctx,&kit);err!=nil{t.Fatal(err)}
 freshDir:=t.TempDir()
 if err=os.Chmod(freshDir,0700);err!=nil{t.Fatal(err)}
 fresh,err:=catalog.Open(freshDir);if err!=nil{t.Fatal(err)}
 defer fresh.Close()
 if err=vault.Initialize(fresh);err!=nil{t.Fatal(err)}
 v,err:=vault.Open(fresh);if err!=nil{t.Fatal(err)}
 svc,err:=backup.New(ctx,fresh,v,"","")
 if err!=nil{t.Fatal(err)}
 restarted:=false
 h:=NewWithBackupRecovery(fresh,v,svc,func(){restarted=true})
 adminToken,_,err:=auth.New(fresh).IssueBootstrap(ctx);if err!=nil{t.Fatal(err)}
 if _,err=auth.New(fresh).CreateAdmin(ctx,adminToken,"admin","proper-long-password-for-ci");err!=nil{t.Fatal(err)}
 login:=request(t,h,"POST","/api/v1/auth/login",`{"username":"admin","password":"proper-long-password-for-ci"}`)
 if login.Code!=200{t.Fatalf("login status %d",login.Code)}
 var session,csrf *http.Cookie
 for _,c:=range login.Result().Cookies(){
  if c.Name==sessionCookie{session=c}
  if c.Name==csrfCookie{csrf=c}
 }
 if session==nil||csrf==nil{t.Fatal("missing session cookies")}
 post:=func(withSession,withCSRF,extra bool)*httptest.ResponseRecorder{
  var body bytes.Buffer
  writer:=multipart.NewWriter(&body)
  part,err:=writer.CreateFormFile("kit","recovery.zip");if err!=nil{t.Fatal(err)}
  if _,err=io.Copy(part,bytes.NewReader(kit.Bytes()));err!=nil{t.Fatal(err)}
  if extra{if err=writer.WriteField("unexpected","x");err!=nil{t.Fatal(err)}}
  if err=writer.Close();err!=nil{t.Fatal(err)}
  req:=httptest.NewRequest(http.MethodPost,"/api/v1/system/recovery-kit/import",&body)
  req.Header.Set("Content-Type",writer.FormDataContentType())
  req.Header.Set("X-BackupHub-Recovery-Confirm","REPLACE_EMPTY_INSTALL")
  if withSession{req.AddCookie(session)}
  if withCSRF{req.AddCookie(csrf);req.Header.Set("X-BackupHub-CSRF",csrf.Value)}
  w:=httptest.NewRecorder()
  h.ServeHTTP(w,req)
  return w
 }
 if status:=post(false,false,false).Code;status!=401{t.Fatalf("unauthorized recovery upload accepted: %d",status)}
 if status:=post(true,false,false).Code;status!=403{t.Fatalf("missing CSRF accepted: %d",status)}
 if status:=post(true,true,true).Code;status!=400{t.Fatalf("multipart extra field accepted: %d",status)}
 if _,err=os.Stat(filepath.Join(freshDir,".recovery-kit-apply.json"));!os.IsNotExist(err){t.Fatal("invalid multipart staged recovery")}
 result:=post(true,true,false)
 if result.Code!=202{t.Fatalf("valid import: %d %s",result.Code,result.Body.String())}
 if !restarted{t.Fatal("service restart callback was not called")}
 if _,err=os.Stat(filepath.Join(freshDir,".recovery-kit-apply.json"));err!=nil{t.Fatalf("recovery restart marker missing: %v",err)}
}
