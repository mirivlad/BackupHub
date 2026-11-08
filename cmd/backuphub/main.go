package main

import (
 "context"
 "flag"
 "fmt"
 "log/slog"
 "net"
 "net/http"
 "os"
 "os/signal"
 "syscall"
 "time"

 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/httpapi"
)

func main(){
 listen:=flag.String("listen","127.0.0.1:8085","HTTP bind address (loopback only; put Nginx/TLS in front)")
 dataDir:=flag.String("data-dir","/var/lib/backuphub","state directory")
 version:=flag.Bool("version",false,"print version")
 flag.Parse()
 if *version {fmt.Println("BackupHub",httpapi.Version);return}
 host,_,err:=net.SplitHostPort(*listen)
 if err!=nil {slog.Error("invalid listen address","error",err);os.Exit(1)}
 ip:=net.ParseIP(host)
 if host!="localhost" && (ip==nil || !ip.IsLoopback()){
  slog.Error("refusing to listen on a non-loopback address without TLS; use Nginx on the same host")
  os.Exit(1)
 }
 c,err:=catalog.Open(*dataDir)
 if err!=nil{slog.Error("cannot open catalog","error",err);os.Exit(1)}
 defer c.Close()
 server:=&http.Server{
  Addr:*listen,Handler:httpapi.New(c),
  ReadHeaderTimeout:5*time.Second,ReadTimeout:15*time.Second,
  IdleTimeout:60*time.Second,
 }
 signals,stop:=signal.NotifyContext(context.Background(),os.Interrupt,syscall.SIGTERM)
 defer stop()
 go func(){
  <-signals.Done()
  ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
  defer cancel()
  if err:=server.Shutdown(ctx);err!=nil{slog.Error("shutdown error","error",err)}
 }()
 slog.Info("BackupHub API started (no backup operations implemented)","listen",*listen,"instance_uuid",c.InstanceID)
 if err:=server.ListenAndServe();err!=nil && err!=http.ErrServerClosed{
  slog.Error("HTTP server failed","error",err);os.Exit(1)
 }
}
