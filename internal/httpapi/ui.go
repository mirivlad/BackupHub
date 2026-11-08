package httpapi

import (
 "errors"
 "fmt"
 "net/http"
 "os"
 "path/filepath"
 "strings"
)

const uiPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// WithUI attaches compiled, immutable Svelte assets at the same origin as the
// authenticated API. It serves no directory indexes, templates or source files.
// Enable it explicitly with the -ui-dir flag after the Vite production build.
func WithUI(api http.Handler, dir string)(http.Handler,error){
 if dir==""{return nil,errors.New("compiled UI directory required")}
 root,err:=filepath.Abs(dir);if err!=nil{return nil,err}
 index:=filepath.Join(root,"index.html")
 info,err:=os.Stat(index);if err!=nil{return nil,fmt.Errorf("compiled UI not found: %w",err)}
 if !info.Mode().IsRegular(){return nil,errors.New("UI index must be a regular file")}
 if assets,err:=os.Stat(filepath.Join(root,"assets"));err!=nil||!assets.IsDir(){return nil,errors.New("compiled UI assets directory missing")}
 files:=http.FileServer(http.Dir(root))
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if strings.HasPrefix(r.URL.Path,"/api/"){api.ServeHTTP(w,r);return}
  if r.Method!=http.MethodGet && r.Method!=http.MethodHead{
   w.Header().Set("Allow","GET, HEAD");http.Error(w,"method not allowed",http.StatusMethodNotAllowed);return
  }
  w.Header().Set("Content-Security-Policy",uiPolicy)
  w.Header().Set("X-Content-Type-Options","nosniff")
  w.Header().Set("X-Frame-Options","DENY")
  w.Header().Set("Referrer-Policy","no-referrer")
  if r.URL.Path=="/"{
   w.Header().Set("Cache-Control","no-store")
   http.ServeFile(w,r,index)
   return
  }
  if strings.HasPrefix(r.URL.Path,"/assets/")&&!strings.HasSuffix(r.URL.Path,"/"){
   // http.FileServer rejects traversal and is only reachable under /assets/.
   files.ServeHTTP(w,r)
   return
  }
  http.NotFound(w,r)
 }),nil
}
