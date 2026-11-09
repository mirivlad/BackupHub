package storagehelper

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net"
 "net/http"
 "os"
 "os/user"
 "path/filepath"
 "sort"
 "strconv"
 "strings"
 "time"

 "golang.org/x/sys/unix"
)

var allowedPrefixes=[]string{"/mnt","/media","/backup","/backup2","/srv/backups"}
var forbiddenFS=map[string]bool{"proc":true,"sysfs":true,"devtmpfs":true,"tmpfs":true,"ramfs":true,"overlay":true,"autofs":true,"cgroup2":true,"cgroup":true,"securityfs":true,"debugfs":true,"tracefs":true,"fusectl":true,"mqueue":true}

func within(path,base string)bool{return path==base||strings.HasPrefix(path,base+"/")}
func allowed(p string)bool{
 for _,root:=range allowedPrefixes {if within(p,root){return true}}
 return false
}
func literalPath(p string)bool{
 if !filepath.IsAbs(p)||filepath.Clean(p)!=p||strings.ContainsRune(p,0){return false}
 for _,v:=range strings.Split(strings.TrimPrefix(p,"/"),"/"){
  if v==""||v=="."||v==".."{return false}
 }
 return true
}
func decodeMountPath(p string)string{
 r:=strings.NewReplacer(`\040`," ",`\011`,"\t",`\012`,"\n",`\134`,"\\")
 return r.Replace(p)
}
func (h *Handler) mounts()([]Mount,error){
 b,err:=os.ReadFile(h.mountinfo)
 if err!=nil{return nil,err}
 found:=make(map[string]Mount)
 for _,line:=range strings.Split(string(b),"\n"){
  halves:=strings.SplitN(line," - ",2)
  if len(halves)!=2{continue}
  left,right:=strings.Fields(halves[0]),strings.Fields(halves[1])
  if len(left)<5||len(right)<1{continue}
  loc:=decodeMountPath(left[4])
  if !literalPath(loc)||!allowed(loc)||forbiddenFS[right[0]]{continue}
  var stats unix.Statfs_t
  if err=unix.Statfs(loc,&stats);err!=nil{continue}
  found[loc]=Mount{Path:loc,Filesystem:right[0],FreeBytes:stats.Bavail*uint64(stats.Bsize)}
 }
 out:=make([]Mount,0,len(found))
 for _,v:=range found{out=append(out,v)}
 sort.Slice(out,func(i,j int)bool{return out[i].Path<out[j].Path})
 return out,nil
}
func(h *Handler) validateStoragePath(p string)error{
 if !literalPath(p)||!allowed(p){return errors.New("invalid storage location")}
 mounts,err:=h.mounts()
 if err!=nil{return err}
 for _,m:=range mounts {if within(p,m.Path){return nil}}
 return errors.New("select a mounted data volume under /mnt, /media, /backup, /backup2 or /srv/backups")
}
// openPathNoFollow walks all components using directory file descriptors.
// Replaced symlinks cannot redirect the privileged helper to /etc or /root.
func openPathNoFollow(p string)(int,error){
 if !literalPath(p){return -1,errors.New("unsafe path")}
 fd,err:=unix.Open("/",unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC,0)
 if err!=nil{return -1,err}
 for _,part:=range strings.Split(strings.TrimPrefix(p,"/"),"/"){
  next,e:=unix.Openat(fd,part,unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,0)
  _=unix.Close(fd)
  if e!=nil{return -1,fmt.Errorf("cannot open directory: %w",e)}
  fd=next
 }
 return fd,nil
}

func validFolder(name string)bool{
 if len(name)<1||len(name)>64||name[0]=='.'||name=="."||name==".."{return false}
 for _,c:=range name{
  if !(c>='a'&&c<='z'||c>='A'&&c<='Z'||c>='0'&&c<='9'||c=='_'||c=='-'||c=='.'){return false}
 }
 return true
}
func(h *Handler) provision(parent,folder string)(string,error){
 if err:=h.validateStoragePath(parent);err!=nil{return "",err}
 if !validFolder(folder){return "",errors.New("folder must contain only letters, digits, dash, dot or underscore (1-64 chars)")}
 fd,err:=openPathNoFollow(parent)
 if err!=nil{return "",err}
 defer unix.Close(fd)
 acct,err:=user.Lookup("backuphub")
 if err!=nil{return "",err}
 uid,err:=strconv.Atoi(acct.Uid);if err!=nil{return "",err}
 gid,err:=strconv.Atoi(acct.Gid);if err!=nil{return "",err}
 // Existing directories are never chmodded or chowned.
 if err=unix.Mkdirat(fd,folder,0700);err!=nil{
  if errors.Is(err,unix.EEXIST){return "",errors.New("destination folder already exists; choose a fresh folder name")}
  return "",err
 }
 success:=false
 defer func(){if !success{_ =unix.Unlinkat(fd,folder,unix.AT_REMOVEDIR)}}()
 dst,err:=unix.Openat(fd,folder,unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,0)
 if err!=nil{return "",err}
 defer unix.Close(dst)
 if err=unix.Fchown(dst,uid,gid);err!=nil{return "",err}
 if err=unix.Fchmod(dst,0700);err!=nil{return "",err}
 success=true
 return filepath.Join(parent,folder),nil
}
func (h *Handler) browse(p string)([]Entry,error){
 if err:=h.validateStoragePath(p);err!=nil{return nil,err}
 fd,err:=openPathNoFollow(p)
 if err!=nil{return nil,err}
 file:=os.NewFile(uintptr(fd),"directory")
 defer file.Close()
 infos,err:=file.Readdir(-1)
 if err!=nil{return nil,err}
 out:=make([]Entry,0)
 for _,info:=range infos{
  if !info.IsDir()||info.Mode()&os.ModeSymlink!=0{continue}
  name:=info.Name()
  if name=="."||name==".."||strings.ContainsAny(name,"/\\\x00"){continue}
  out=append(out,Entry{Name:name,Path:filepath.Join(p,name)})
  if len(out)>2000{return nil,errors.New("too many directories to display")}
 }
 sort.Slice(out,func(i,j int)bool{return out[i].Name<out[j].Name})
 return out,nil
}

// Handler has no API to delete, rename, read files, change arbitrary ownership
// or write files. Its only mutation is mkdir + chown of a NEW directory.
type Handler struct{mountinfo string}
func NewHandler()http.Handler{return NewHandlerWithMountinfo("/proc/self/mountinfo")}
func NewHandlerWithMountinfo(path string)http.Handler{
 h:=&Handler{mountinfo:path}
 mux:=http.NewServeMux()
 write:=func(w http.ResponseWriter,code int,v any){
  w.Header().Set("Content-Type","application/json")
  w.Header().Set("Cache-Control","no-store")
  w.WriteHeader(code)
  _=json.NewEncoder(w).Encode(v)
 }
 mux.HandleFunc("/mounts",func(w http.ResponseWriter,r *http.Request){
  if r.Method!=http.MethodGet{write(w,405,map[string]string{"error":"method not allowed"});return}
  mounts,err:=h.mounts();if err!=nil{write(w,500,map[string]string{"error":"mount discovery failed"});return}
  write(w,200,map[string]any{"mounts":mounts})
 })
 mux.HandleFunc("/browse",func(w http.ResponseWriter,r *http.Request){
  if r.Method!=http.MethodGet{write(w,405,map[string]string{"error":"method not allowed"});return}
  entries,err:=h.browse(r.URL.Query().Get("path"))
  if err!=nil{write(w,400,map[string]string{"error":err.Error()});return}
  write(w,200,map[string]any{"entries":entries})
 })
 mux.HandleFunc("/provision",func(w http.ResponseWriter,r *http.Request){
  if r.Method!=http.MethodPost||r.Header.Get("Content-Type")!="application/json"{write(w,405,map[string]string{"error":"method or content type rejected"});return}
  var in struct{Parent string `json:"parent_path"`;Folder string `json:"folder"`}
  body:=json.NewDecoder(io.LimitReader(r.Body,4097));body.DisallowUnknownFields()
  if err:=body.Decode(&in);err!=nil{write(w,400,map[string]string{"error":"invalid request"});return}
  var extra any
  if err:=body.Decode(&extra);err!=io.EOF{write(w,400,map[string]string{"error":"invalid request size"});return}
  created,err:=h.provision(in.Parent,in.Folder)
  if err!=nil{write(w,400,map[string]string{"error":err.Error()});return}
  write(w,201,map[string]string{"path":created})
 })
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  r.Body=http.MaxBytesReader(w,r.Body,4096)
  mux.ServeHTTP(w,r)
 })
}

// ServeActivated accepts ONLY an already privileged systemd Unix socket.
// There is deliberately no TCP listener or way to start as the web user.
func ServeActivated(ctx context.Context)error{
 if os.Geteuid()!=0{return errors.New("provisioning helper must run as root")}
 if os.Getenv("LISTEN_FDS")!="1"||os.Getenv("LISTEN_PID")!=strconv.Itoa(os.Getpid()){
  return errors.New("provisioning helper requires a systemd-activated Unix socket")
 }
 f:=os.NewFile(3,"systemd-storage-socket")
 listener,err:=net.FileListener(f)
 _=f.Close()
 if err!=nil{return err}
 defer listener.Close()
 if listener.Addr().Network()!="unix"{return errors.New("provisioning helper requires Unix socket")}
 server:=&http.Server{Handler:NewHandler(),ReadHeaderTimeout:5*time.Second,ReadTimeout:15*time.Second,WriteTimeout:30*time.Second,MaxHeaderBytes:8192}
 go func(){<-ctx.Done();timeout,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel();_ =server.Shutdown(timeout)}()
 err=server.Serve(listener)
 if errors.Is(err,http.ErrServerClosed){return nil}
 return err
}
