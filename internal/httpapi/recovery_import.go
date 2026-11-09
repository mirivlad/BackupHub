package httpapi

import (
 "errors"
 "io"
 "log/slog"
 "mime"
 "os"
 "net/http"
 "strings"
 )

// Only an authenticated administrator of an EMPTY fresh installation may
// import a Recovery Kit. This endpoint does not mutate live SQLite or Vault;
// it validates and stages a private copy for a clean supervised restart.
func (s *Server) recoveryKitImport(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodPost){return}
 user,_,csrf,ok:=s.current(w,r)
 if !ok{return}
 if user.Role!="administrator"{jsonResponse(w,403,map[string]string{"error":"administrator required"});return}
 if !csrfValid(r,csrf){jsonResponse(w,403,map[string]string{"error":"invalid CSRF token"});return}
 if s.backup==nil{jsonResponse(w,503,map[string]string{"error":"backup engine unavailable"});return}
 if !s.limits.allow(r,"recovery-import",3){
  w.Header().Set("Retry-After","60")
  jsonResponse(w,429,map[string]string{"error":"too many recovery attempts"});return
 }
 if r.Header.Get("X-BackupHub-Recovery-Confirm")!="REPLACE_EMPTY_INSTALL"{
  jsonResponse(w,400,map[string]string{"error":"explicit recovery confirmation required"});return
 }
 mediaType,_,err:=mime.ParseMediaType(r.Header.Get("Content-Type"))
 if err!=nil||!strings.EqualFold(mediaType,"multipart/form-data"){
  jsonResponse(w,415,map[string]string{"error":"multipart ZIP upload required"});return
 }
 // Cap the entire request, including multipart headers. A Restic repository
 // is NOT included in the kit and must never be uploaded over this endpoint.
 r.Body=http.MaxBytesReader(w,r.Body,(256<<20)+(1<<20))
 multipart,err:=r.MultipartReader()
 if err!=nil{jsonResponse(w,400,map[string]string{"error":"invalid multipart upload"});return}
 part,err:=multipart.NextPart()
 if err!=nil||part.FormName()!="kit"||part.FileName()==""{
  jsonResponse(w,400,map[string]string{"error":"select a Recovery Kit ZIP file"});return
 }
 // First validate the *entire* multipart body, including the absence of
 // additional fields, before staging anything that can replace state on boot.
 // Spool the upload to a private file, never to memory.
 uploaded,err:=os.CreateTemp(s.catalog.DataDir,".recovery-http-*.zip")
 if err!=nil{jsonResponse(w,500,map[string]string{"error":"cannot prepare recovery upload"});return}
 defer func(){_ =uploaded.Close();_ =os.Remove(uploaded.Name())}()
 amount,copyErr:=io.Copy(uploaded,io.LimitReader(part,(256<<20)+1))
 _=part.Close()
 if copyErr!=nil||amount<=0||amount>256<<20{
  jsonResponse(w,413,map[string]string{"error":"Recovery Kit upload failed or exceeds 256 MiB"});return
 }
 if next,nextErr:=multipart.NextPart();nextErr==nil{
  _=next.Close()
  jsonResponse(w,400,map[string]string{"error":"only one ZIP file is allowed"});return
 }else if !errors.Is(nextErr,io.EOF){
  jsonResponse(w,400,map[string]string{"error":"invalid multipart termination"});return
 }
 if err=uploaded.Sync();err!=nil{jsonResponse(w,500,map[string]string{"error":"cannot flush recovery upload"});return}
 if _,err=uploaded.Seek(0,0);err!=nil{jsonResponse(w,500,map[string]string{"error":"cannot read recovery upload"});return}
 info,err:=s.backup.PrepareRecoveryKit(r.Context(),uploaded)
 if err!=nil{
  slog.Warn("browser Recovery Kit import rejected","error",publicError(err))
  var maxErr *http.MaxBytesError
  if errors.As(err,&maxErr){jsonResponse(w,413,map[string]string{"error":"Recovery Kit exceeds 256 MiB"});return}
  jsonResponse(w,400,map[string]string{"error":"Recovery Kit rejected: invalid archive, incompatible data or non-empty installation. Check service journal."})
  return
 }
 s.backup.Audit(r.Context(),user.ID,"system.recovery_kit_import_prepared",info.InstanceID)
 jsonResponse(w,202,map[string]any{
  "staged":true,
  "restored_instance_uuid":info.InstanceID,
  "restart_requested":s.onRecoveryRestart!=nil,
  "message":"validated Recovery Kit staged; restoring previous installation on service restart",
 })
 if s.onRecoveryRestart!=nil{s.onRecoveryRestart()}
}
