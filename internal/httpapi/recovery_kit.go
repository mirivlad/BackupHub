package httpapi

import (
 "fmt"
 "net/http"
 "os"
 "time"
)

// A recovery kit contains live service credentials. Only the administrator may
// download it, over the same authenticated HTTPS session as other APIs.
func (s *Server) recoveryKit(w http.ResponseWriter,r *http.Request){
 user,_,_,ok:=s.current(w,r)
 if !ok{return}
 if user.Role!="administrator"{jsonResponse(w,403,map[string]string{"error":"administrator required"});return}
 if !method(w,r,http.MethodGet){return}
 if s.backup==nil{jsonResponse(w,503,map[string]string{"error":"backup engine unavailable"});return}
 file,err:=os.CreateTemp(s.catalog.DataDir,".recovery-kit-download-*.zip")
 if err!=nil{jsonResponse(w,500,map[string]string{"error":"cannot create recovery kit"});return}
 defer func(){_ =file.Close();_ =os.Remove(file.Name())}()
 if err=s.backup.ExportRecoveryKit(r.Context(),file);err!=nil{
  jsonResponse(w,500,map[string]string{"error":"cannot create consistent recovery kit"})
  return
 }
 if _,err=file.Seek(0,0);err!=nil{jsonResponse(w,500,map[string]string{"error":"cannot read recovery kit"});return}
 s.backup.Audit(r.Context(),user.ID,"system.recovery_kit_exported",s.catalog.InstanceID)
 w.Header().Set("Content-Type","application/zip")
 w.Header().Set("Content-Disposition",fmt.Sprintf("attachment; filename=%q","backuphub-recovery-"+s.catalog.InstanceID+".zip"))
 w.Header().Set("X-Content-Type-Options","nosniff")
 http.ServeContent(w,r,"recovery-kit.zip",time.Now(),file)
}
