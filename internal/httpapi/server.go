package httpapi

import (
 "encoding/json"
 "net/http"
 "time"

 "github.com/mirivlad/BackupHub/internal/catalog"
)

const Version = "0.0.0-dev"

type Server struct {catalog *catalog.Catalog}

func New(c *catalog.Catalog) http.Handler {
 s:=&Server{catalog:c}
 mux:=http.NewServeMux()
 mux.HandleFunc("/api/v1/health",s.health)
 mux.HandleFunc("/api/v1/system/identity",s.identity)
 mux.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){
  http.Error(w,"BackupHub API foundation: UI and backup operations are not yet implemented",http.StatusNotFound)
 })
 return headers(mux)
}
func headers(next http.Handler)http.Handler {
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("X-Content-Type-Options","nosniff")
  w.Header().Set("X-Frame-Options","DENY")
  w.Header().Set("Referrer-Policy","no-referrer")
  w.Header().Set("Cache-Control","no-store")
  next.ServeHTTP(w,r)
 })
}
func jsonResponse(w http.ResponseWriter,status int,object any){
 w.Header().Set("Content-Type","application/json; charset=utf-8")
 w.WriteHeader(status)
 _=json.NewEncoder(w).Encode(object)
}
func getOnly(w http.ResponseWriter,r *http.Request) bool {
 if r.Method==http.MethodGet{return true}
 w.Header().Set("Allow","GET")
 jsonResponse(w,http.StatusMethodNotAllowed,map[string]string{"error":"method not allowed"})
 return false
}
func (s *Server) health(w http.ResponseWriter,r *http.Request){
 if !getOnly(w,r){return}
 ctx:=r.Context()
 if err:=s.catalog.DB.PingContext(ctx);err!=nil{
  jsonResponse(w,http.StatusServiceUnavailable,map[string]string{"service":"unhealthy"})
  return
 }
 jsonResponse(w,http.StatusOK,struct{
  Service string `json:"service"`
  Version string `json:"version"`
  BackupsAvailable bool `json:"backups_available"`
  Timestamp string `json:"timestamp"`
 }{"running",Version,false,time.Now().UTC().Format(time.RFC3339)})
}
// Identity is not a secret, but the endpoint contains no credentials or data.
func (s *Server) identity(w http.ResponseWriter,r *http.Request){
 if !getOnly(w,r){return}
 jsonResponse(w,http.StatusOK,map[string]any{
  "instance_uuid":s.catalog.InstanceID,
  "schema_version":catalog.SchemaVersion,
  "production_ready":false,
 })
}
