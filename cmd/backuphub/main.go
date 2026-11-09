package main

import (
 "context"
 "bufio"
 "strings"
 "bytes"
 "golang.org/x/term"
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
 "github.com/mirivlad/BackupHub/internal/storagehelper"
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
 if len(args)>0&&args[0]=="storage-helper"{
  if len(args)!=1{return fmt.Errorf("storage-helper takes no arguments")}
  signals,stop:=signal.NotifyContext(context.Background(),os.Interrupt,syscall.SIGTERM)
  defer stop()
  return storagehelper.ServeActivated(signals)
 }
 command:="serve"
 if len(args)>0 {
  switch args[0] {
  case "serve","init","bootstrap","setup-admin":
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
 case "setup-admin":
  if _,err:=vault.Open(c);err!=nil{return err}
  if !term.IsTerminal(int(os.Stdin.Fd())){return fmt.Errorf("setup-admin requires an interactive terminal; noninteractive installs use the web bootstrap flow")}
  authStore:=auth.New(c)
  hasUsers,err:=authStore.HasUsers(context.Background())
  if err!=nil{return err}
  if hasUsers{return fmt.Errorf("administrator already exists; setup-admin is only for the first account")}
  fmt.Print("Имя первого администратора: ")
  reader:=bufio.NewReader(os.Stdin)
  username,err:=reader.ReadString('\n')
  if err!=nil{return err}
  username=strings.TrimSpace(username)
  fmt.Print("Пароль администратора (не отображается): ")
  password,err:=term.ReadPassword(int(os.Stdin.Fd()))
  fmt.Println()
  if err!=nil{return err}
  defer func(){for i:=range password{password[i]=0}}()
  fmt.Print("Повторите пароль: ")
  confirm,err:=term.ReadPassword(int(os.Stdin.Fd()))
  fmt.Println()
  if err!=nil{return err}
  defer func(){for i:=range confirm{confirm[i]=0}}()
  if !bytes.Equal(password,confirm){return fmt.Errorf("пароли не совпадают")}
  token,_,err:=authStore.IssueBootstrap(context.Background())
  if err!=nil{return err}
  if _,err=authStore.CreateAdmin(context.Background(),token,username,string(password));err!=nil{return err}
  fmt.Println("Администратор создан. Откройте веб-интерфейс BackupHub.")
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
