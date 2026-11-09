// Package storagehelper isolates narrowly privileged filesystem provisioning.
// Only root-owned socket-activated systemd service may create repository
// directories. The main web daemon remains unprivileged.
package storagehelper

import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net"
 "net/http"
 "net/url"
 "time"
)

type Mount struct {
 Path string `json:"path"`
 Filesystem string `json:"filesystem"`
 FreeBytes uint64 `json:"free_bytes"`
}
type Entry struct {
 Name string `json:"name"`
 Path string `json:"path"`
}

type Client struct {http *http.Client}
func NewClient(sock string)*Client{
 transport:=&http.Transport{DialContext:func(ctx context.Context,_,_ string)(net.Conn,error){
  var dialer net.Dialer
  return dialer.DialContext(ctx,"unix",sock)
 }}
 return &Client{http:&http.Client{Transport:transport,Timeout:20*time.Second}}
}
func(c *Client) call(ctx context.Context,method,path string,req any,resp any)error{
 var body io.Reader
 if req!=nil{
  bytes,err:=json.Marshal(req);if err!=nil{return err}
  body=bytesReader(bytes)
 }
 request,err:=http.NewRequestWithContext(ctx,method,"http://unix"+path,body)
 if err!=nil{return err}
 if req!=nil{request.Header.Set("Content-Type","application/json")}
 response,err:=c.http.Do(request)
 if err!=nil{return fmt.Errorf("storage provisioning helper unavailable: %w",err)}
 defer response.Body.Close()
 if response.StatusCode!=200 && response.StatusCode!=201{
  data,_:=io.ReadAll(io.LimitReader(response.Body,512))
  return fmt.Errorf("storage provisioning rejected (%d): %s",response.StatusCode,string(data))
 }
 if err=json.NewDecoder(io.LimitReader(response.Body,128*1024)).Decode(resp);err!=nil{return err}
 return nil
}
func bytesReader(data []byte) io.Reader{return bytes.NewReader(data)}
func(c *Client) Mounts(ctx context.Context)([]Mount,error){
 var d struct{Mounts []Mount `json:"mounts"`}
 err:=c.call(ctx,"GET","/mounts",nil,&d)
 if err!=nil{return nil,err}
 return d.Mounts,nil
}
func(c *Client) Browse(ctx context.Context,p string)([]Entry,error){
 if p==""{return nil,errors.New("path required")}
 var d struct{Entries []Entry `json:"entries"`}
 err:=c.call(ctx,"GET","/browse?path="+url.QueryEscape(p),nil,&d)
 if err!=nil{return nil,err}
 return d.Entries,nil
}
func(c *Client) Provision(ctx context.Context,parent,folder string)(string,error){
 var d struct{Path string `json:"path"`}
 err:=c.call(ctx,"POST","/provision",map[string]string{"parent_path":parent,"folder":folder},&d)
 if err!=nil{return "",err}
 if d.Path==""{return "",errors.New("provisioning helper returned empty path")}
 return d.Path,nil
}
