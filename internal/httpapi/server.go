package httpapi

import (
 "crypto/sha256"
 "crypto/subtle"
 "encoding/json"
 "errors"
 "io"
 "net"
 "net/http"
 "strconv"
 "sync"
 "time"

 "github.com/mirivlad/BackupHub/internal/auth"
 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/vault"
)

const Version = "0.0.0-dev"
const sessionCookie = "backuphub_session"
const csrfCookie = "backuphub_csrf"

type Server struct {
 catalog *catalog.Catalog
 auth *auth.Store
 vault *vault.Vault // Not exposed over the public API.
 limits *rateLimiter
}
type rateBucket struct {
 count int
 expires time.Time
}
type rateLimiter struct {
 mu sync.Mutex
 buckets map[string]rateBucket
}
func (l *rateLimiter) allow(request *http.Request,action string,max int)bool{
 ip,_,err:=net.SplitHostPort(request.RemoteAddr)
 if err!=nil{ip=request.RemoteAddr}
 key:=action+":"+ip
 now:=time.Now()
 l.mu.Lock()
 defer l.mu.Unlock()
 if len(l.buckets)>=2048 {
  for k,v:=range l.buckets {if now.After(v.expires){delete(l.buckets,k)}}
  if len(l.buckets)>=2048 {if _,ok:=l.buckets[key];!ok{return false}}
 }
 b:=l.buckets[key]
 if now.After(b.expires){b=rateBucket{expires:now.Add(time.Minute)}}
 b.count++
 l.buckets[key]=b
 return b.count<=max
}

func New(c *catalog.Catalog,v *vault.Vault) http.Handler {
 s:=&Server{catalog:c,auth:auth.New(c),vault:v,limits:&rateLimiter{buckets:make(map[string]rateBucket)}}
 mux:=http.NewServeMux()
 mux.HandleFunc("/api/v1/health",s.health)
 mux.HandleFunc("/api/v1/setup/status",s.setupStatus)
 mux.HandleFunc("/api/v1/setup/admin",s.setupAdmin)
 mux.HandleFunc("/api/v1/auth/login",s.login)
 mux.HandleFunc("/api/v1/auth/logout",s.logout)
 mux.HandleFunc("/api/v1/auth/me",s.me)
 mux.HandleFunc("/api/v1/system/identity",s.identity)
 mux.HandleFunc("/api/v1/audit",s.audit)
 mux.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){
  jsonResponse(w,http.StatusNotFound,map[string]string{"error":"endpoint unavailable; backup engine and UI are under development"})
 })
 return headers(mux)
}
func headers(next http.Handler)http.Handler {
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("X-Content-Type-Options","nosniff")
  w.Header().Set("X-Frame-Options","DENY")
  w.Header().Set("Referrer-Policy","no-referrer")
  w.Header().Set("Cache-Control","no-store")
  w.Header().Set("Content-Security-Policy","default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
  next.ServeHTTP(w,r)
 })
}
func jsonResponse(w http.ResponseWriter,status int,object any){
 w.Header().Set("Content-Type","application/json; charset=utf-8")
 w.WriteHeader(status)
 _=json.NewEncoder(w).Encode(object)
}
func method(w http.ResponseWriter,r *http.Request,required string)bool{
 if r.Method==required{return true}
 w.Header().Set("Allow",required)
 jsonResponse(w,http.StatusMethodNotAllowed,map[string]string{"error":"method not allowed"})
 return false
}
func decode(w http.ResponseWriter,r *http.Request,target any)bool{
 if r.Header.Get("Content-Type")!="application/json"{
  // An explicit JSON content type prevents form-based CSRF attempts.
  jsonResponse(w,http.StatusUnsupportedMediaType,map[string]string{"error":"Content-Type must be application/json"})
  return false
 }
 d:=json.NewDecoder(io.LimitReader(r.Body,4097))
 d.DisallowUnknownFields()
 if err:=d.Decode(target);err!=nil{
  jsonResponse(w,http.StatusBadRequest,map[string]string{"error":"invalid JSON body"})
  return false
 }
 var extra any
 if err:=d.Decode(&extra);err!=io.EOF{
  jsonResponse(w,http.StatusBadRequest,map[string]string{"error":"only one JSON object allowed"})
  return false
 }
 return true
}
func (s *Server) health(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodGet){return}
 if err:=s.catalog.DB.PingContext(r.Context());err!=nil{
  jsonResponse(w,http.StatusServiceUnavailable,map[string]string{"service":"unhealthy"});return
 }
 jsonResponse(w,http.StatusOK,struct{
  Service string `json:"service"`
  Version string `json:"version"`
  BackupsAvailable bool `json:"backups_available"`
  Timestamp string `json:"timestamp"`
 }{"running",Version,false,time.Now().UTC().Format(time.RFC3339)})
}
func (s *Server) setupStatus(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodGet){return}
 users,err:=s.auth.HasUsers(r.Context())
 if err!=nil{jsonResponse(w,500,map[string]string{"error":"catalog unavailable"});return}
 jsonResponse(w,200,map[string]bool{"setup_required":!users})
}
func (s *Server) setupAdmin(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodPost){return}
 if !s.limits.allow(r,"setup",6){w.Header().Set("Retry-After","60");jsonResponse(w,429,map[string]string{"error":"too many attempts"});return}
 var req struct {
  Token string `json:"bootstrap_token"`
  Username string `json:"username"`
  Password string `json:"password"`
 }
 if !decode(w,r,&req){return}
 if len(req.Token)>128||len(req.Username)>64||len(req.Password)>1024{
  jsonResponse(w,400,map[string]string{"error":"invalid input size"});return
 }
 user,err:=s.auth.CreateAdmin(r.Context(),req.Token,req.Username,req.Password)
 if err!=nil {
  status:=http.StatusBadRequest
  if errors.Is(err,auth.ErrInvalidCredentials){status=http.StatusUnauthorized}
  if errors.Is(err,auth.ErrAlreadyInitialized){status=http.StatusConflict}
  jsonResponse(w,status,map[string]string{"error":err.Error()});return
 }
 jsonResponse(w,http.StatusCreated,user)
}
func (s *Server) login(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodPost){return}
 if !s.limits.allow(r,"login",6){w.Header().Set("Retry-After","60");jsonResponse(w,429,map[string]string{"error":"too many attempts"});return}
 var req struct {
  Username string `json:"username"`
  Password string `json:"password"`
 }
 if !decode(w,r,&req){return}
 session,err:=s.auth.Login(r.Context(),req.Username,req.Password)
 if err!=nil {
  if errors.Is(err,auth.ErrInvalidCredentials){jsonResponse(w,http.StatusUnauthorized,map[string]string{"error":"invalid credentials"});return}
  jsonResponse(w,500,map[string]string{"error":"authentication unavailable"});return
 }
 http.SetCookie(w,&http.Cookie{Name:sessionCookie,Value:session.Token,Path:"/api/v1",HttpOnly:true,Secure:true,SameSite:http.SameSiteStrictMode,Expires:session.Expires})
 http.SetCookie(w,&http.Cookie{Name:csrfCookie,Value:session.CSRF,Path:"/",HttpOnly:false,Secure:true,SameSite:http.SameSiteStrictMode,Expires:session.Expires})
 jsonResponse(w,200,map[string]any{"user":session.User,"expires_at":session.Expires.UTC().Format(time.RFC3339)})
}
func (s *Server) current(w http.ResponseWriter,r *http.Request)(auth.User,string,[]byte,bool){
 c,err:=r.Cookie(sessionCookie)
 if err!=nil {jsonResponse(w,http.StatusUnauthorized,map[string]string{"error":"login required"});return auth.User{}, "",nil,false}
 u,csrf,err:=s.auth.Authenticate(r.Context(),c.Value)
 if err!=nil{jsonResponse(w,http.StatusUnauthorized,map[string]string{"error":"login required"});return auth.User{},"",nil,false}
 return u,c.Value,csrf,true
}
func csrfValid(r *http.Request,expected []byte)bool{
 header:=r.Header.Get("X-BackupHub-CSRF")
 cookie,err:=r.Cookie(csrfCookie)
 if err!=nil||header==""||len(header)>128||header!=cookie.Value{return false}
 digest:=sha256.Sum256([]byte(header))
 return subtle.ConstantTimeCompare(digest[:],expected)==1
}
func (s *Server) logout(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodPost){return}
 u,raw,csrf,ok:=s.current(w,r);if !ok{return}
 if !csrfValid(r,csrf){jsonResponse(w,http.StatusForbidden,map[string]string{"error":"invalid CSRF token"});return}
 if err:=s.auth.Logout(r.Context(),raw,u.ID);err!=nil{jsonResponse(w,500,map[string]string{"error":"logout failed"});return}
 http.SetCookie(w,&http.Cookie{Name:sessionCookie,Value:"",Path:"/api/v1",MaxAge:-1,HttpOnly:true,Secure:true,SameSite:http.SameSiteStrictMode})
 http.SetCookie(w,&http.Cookie{Name:csrfCookie,Value:"",Path:"/",MaxAge:-1,Secure:true,SameSite:http.SameSiteStrictMode})
 w.WriteHeader(http.StatusNoContent)
}
func (s *Server) me(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodGet){return}
 u,_,_,ok:=s.current(w,r);if !ok{return}
 jsonResponse(w,200,u)
}
func (s *Server) identity(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodGet){return}
 _,_,_,ok:=s.current(w,r);if !ok{return}
 jsonResponse(w,http.StatusOK,map[string]any{"instance_uuid":s.catalog.InstanceID,"schema_version":catalog.SchemaVersion,"production_ready":false})
}
func (s *Server) audit(w http.ResponseWriter,r *http.Request){
 if !method(w,r,http.MethodGet){return}
 u,_,_,ok:=s.current(w,r);if !ok{return}
 if u.Role!="administrator"&&u.Role!="auditor"{jsonResponse(w,403,map[string]string{"error":"permission denied"});return}
 limit:=50
 if r.URL.Query().Has("limit"){
  n,err:=strconv.Atoi(r.URL.Query().Get("limit"))
  if err!=nil||n<1||n>100{jsonResponse(w,400,map[string]string{"error":"limit must be 1-100"});return}
  limit=n
 }
 events,err:=s.auth.Audit(r.Context(),limit)
 if err!=nil{jsonResponse(w,500,map[string]string{"error":"audit unavailable"});return}
 jsonResponse(w,200,map[string]any{"events":events})
}
