package httpapi

import (
 "net/http"
)

// systemSettings is deliberately administrator-only. The default does not
// change the password mode of any existing repository or any prior snapshot.
func (s *Server) systemSettings(w http.ResponseWriter,r *http.Request){
 user,_,csrf,ok:=s.current(w,r)
 if !ok{return}
 if user.Role!="administrator"{jsonResponse(w,403,map[string]string{"error":"administrator required"});return}
 if s.backup==nil{jsonResponse(w,503,map[string]string{"error":"backup engine unavailable"});return}
 switch r.Method{
 case http.MethodGet:
  mode,err:=s.backup.DefaultRepositoryPasswordMode(r.Context())
  if err!=nil{jsonResponse(w,500,map[string]string{"error":"settings unavailable"});return}
  jsonResponse(w,200,map[string]string{"default_repository_password_mode":mode})
 case http.MethodPost:
  if !csrfValid(r,csrf){jsonResponse(w,403,map[string]string{"error":"invalid CSRF token"});return}
  var req struct{Mode string `json:"default_repository_password_mode"`}
  if !decode(w,r,&req){return}
  if err:=s.backup.SetDefaultRepositoryPasswordMode(r.Context(),req.Mode);err!=nil{
   jsonResponse(w,400,map[string]string{"error":publicError(err)});return
  }
  s.backup.Audit(r.Context(),user.ID,"system.default_storage_mode_changed",req.Mode)
  jsonResponse(w,200,map[string]string{"default_repository_password_mode":req.Mode})
 default:
  w.Header().Set("Allow","GET, POST")
  jsonResponse(w,405,map[string]string{"error":"method not allowed"})
 }
}
