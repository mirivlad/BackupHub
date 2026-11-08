package httpapi

import (
 "net/http"
 "net/http/httptest"
 "os"
 "path/filepath"
 "strings"
 "testing"
)

func TestUIServesCompiledAssetsAndKeepsAPI(t *testing.T){
 dir:=t.TempDir()
 if err:=os.Mkdir(filepath.Join(dir,"assets"),0700);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(dir,"index.html"),[]byte("<html>BackupHub</html>"),0600);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(dir,"assets","app.js"),[]byte("export const ready = true;"),0600);err!=nil{t.Fatal(err)}
 api:=http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)})
 handler,err:=WithUI(api,dir);if err!=nil{t.Fatal(err)}
 for _,tc:=range []struct{path string;status int;body string}{
  {"/",200,"BackupHub"},
  {"/assets/app.js",200,"ready"},
  {"/api/v1/health",204,""},
  {"/assets/",404,""},
  {"/missing",404,""},
 }{
  r:=httptest.NewRequest(http.MethodGet,tc.path,nil);w:=httptest.NewRecorder()
  handler.ServeHTTP(w,r)
  if w.Code!=tc.status{t.Errorf("%s: %d expected %d",tc.path,w.Code,tc.status)}
  if !strings.Contains(w.Body.String(),tc.body){t.Errorf("%s: missing content",tc.path)}
  if tc.path=="/"&&!strings.Contains(w.Header().Get("Content-Security-Policy"),"script-src 'self'"){t.Error("no UI CSP")}
 }
 r:=httptest.NewRequest(http.MethodPost,"/",nil);w:=httptest.NewRecorder();handler.ServeHTTP(w,r)
 if w.Code!=405{t.Fatalf("POST accepted on UI: %d",w.Code)}
}
func TestUIFailsClosedWithoutBuild(t *testing.T){
 if _,err:=WithUI(http.NotFoundHandler(),t.TempDir());err==nil{t.Fatal("missing build accepted")}
}
