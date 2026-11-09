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

 "github.com/mirivlad/BackupHub/internal/backup"
 "github.com/mirivlad/BackupHub/internal/auth"
 "github.com/mirivlad/BackupHub/internal/catalog"
 "github.com/mirivlad/BackupHub/internal/httpapi"
 "github.com/mirivlad/BackupHub/internal/vault"
)

func main(){
 // All generated files in a secret-bearing service must start private.
 syscall.Umask(0077)
 if err:=run(os.Args[1:]);err!=nil{
  slog.Error("backuphub command failed","error",err)
  os.Exit(1)
 }
}

func run(args []string)error{
 command:="serve"
 if len(args)>0 {
  switch args[0] {
  case "serve","init","bootstrap":
   command=args[0];args=args[1:]
  }
 }
 flags:=flag.NewFlagSet(command,flag.ContinueOnError)
 dataDir:=flags.String("data-dir","/var/lib/backuphub","private state directory")
 listen:=flags.String("listen","127.0.0.1:8085","HTTP address; external access requires TLS")
 repoPath:=flags.String("repository",os.Getenv("BACKUPHUB_REPOSITORY"),"absolute path to encrypted Restic repository")
 stagePath:=flags.String("staging",os.Getenv("BACKUPHUB_STAGING"),"private capture staging directory (must have sufficient free space for remote sources)")
 tlsCert:=flags.String("tls-cert",os.Getenv("BACKUPHUB_TLS_CERT"),"TLS certificate")
 tlsKey:=flags.String("tls-key",os.Getenv("BACKUPHUB_TLS_KEY"),"TLS key")
 uiDir:=flags.String("ui-dir","","compiled Svelte UI directory (optional)")
 version:=flags.Bool("version",false,"print version")
 if err:=flags.Parse(args);err!=nil{return err}
 if flags.NArg()!=0{return fmt.Errorf("unexpected arguments: %v",flags.Args())}
 if *version{fmt.Println("BackupHub",httpapi.Version);return nil}

 c,err:=catalog.Open(*dataDir)
 if err!=nil{return fmt.Errorf("open catalog: %w",err)}
 defer c.Close()

 switch command {
 case "init":
  if err:=vault.Initialize(c);err!=nil{return err}
  fmt.Println("BackupHub master key created; store an offline copy of",*dataDir+"/master.key")
  fmt.Println("Loss of this key means loss of access to encrypted source credentials.")
  return nil
 case "bootstrap":
  if _,err:=vault.Open(c);err!=nil{return err}
  raw,expires,err:=auth.New(c).IssueBootstrap(context.Background())
  if err!=nil{return err}
  fmt.Println("One-time administrator bootstrap token (valid until",expires.Format(time.RFC3339)+"):")
  fmt.Println(raw)
  fmt.Println("This token is displayed only now; do not paste it into logs or issues.")
  return nil
 case "serve":
  v,err:=vault.Open(c)
  if err!=nil{return err}
  ctx:=context.Background()
  svc,err:=backup.New(ctx,c,v,*repoPath,*stagePath)
  if err!=nil{return fmt.Errorf("cannot initialize backup engine: %w",err)}
  host,_,err:=net.SplitHostPort(*listen)
  if err!=nil{return fmt.Errorf("invalid listen address: %w",err)}
  ip:=net.ParseIP(host)
  if host!="localhost" && (ip==nil||!ip.IsLoopback()) && (*tlsCert==""||*tlsKey==""){
   return fmt.Errorf("refusing non-loopback HTTP address without TLS: %q",*listen)
  }
  if (*tlsCert=="")!=(*tlsKey==""){return fmt.Errorf("both TLS certificate and key are required")}
  handler:=httpapi.NewWithBackup(c,v,svc)
  if *uiDir!="" {handler,err=httpapi.WithUI(handler,*uiDir);if err!=nil{return err}}
  server:=&http.Server{
   Addr:*listen,Handler:handler,
   ReadHeaderTimeout:5*time.Second,ReadTimeout:15*time.Second,
   IdleTimeout:60*time.Second,MaxHeaderBytes:16384,
  }
  signals,stop:=signal.NotifyContext(context.Background(),os.Interrupt,syscall.SIGTERM)
  defer stop()
  go func(){
   <-signals.Done()
   ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
   defer cancel()
   if err:=server.Shutdown(ctx);err!=nil{slog.Error("shutdown error","error",err)}
  }()
  if err:=svc.Start(signals);err!=nil{return fmt.Errorf("cannot start scheduler: %w",err)}
  slog.Info("BackupHub started","listen",*listen,"instance_uuid",c.InstanceID)
  if *tlsCert!="" {
   if err:=server.ListenAndServeTLS(*tlsCert,*tlsKey);err!=nil&&err!=http.ErrServerClosed{return err}
  }else {
   if err:=server.ListenAndServe();err!=nil&&err!=http.ErrServerClosed{return err}
  }
  return nil
 }
 return fmt.Errorf("unknown command: %q",command)
}
