package backup

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "path/filepath"
 "strings"
 "time"
)

type portableResticSnapshot struct {
 ID string `json:"id"`
 Tags []string `json:"tags"`
}
type recoveredManifest struct {
 Record portableRecord
 MarkerID string
}
func resticHasTag(tags []string, tag string)bool {
 for _,v:=range tags {if v==tag{return true}}
 return false
}

// Read durable commit markers without trusting SQLite or treating uncommitted
// Restic captures as restorable BackupHub snapshots.
func (r *repository) recoverManifestIndex(ctx context.Context)([]recoveredManifest,error) {
 output,err:=r.run(ctx,nil,"snapshots","--json")
 if err!=nil{return nil,err}
 var all []portableResticSnapshot
 if err=json.Unmarshal(output,&all);err!=nil{return nil,fmt.Errorf("invalid Restic snapshot index: %w",err)}
 if len(all)>20000{return nil,errors.New("too many snapshots for one recovery operation")}
 byID:=make(map[string]portableResticSnapshot,len(all))
 for _,item:=range all {
  if !validResticID(item.ID){return nil,errors.New("invalid Restic snapshot identifier")}
  byID[item.ID]=item
 }
 recovered:=make([]recoveredManifest,0)
 seen:=make(map[string]bool)
 for _,snapshot:=range all {
  commitTag:=""
  for _,tag:=range snapshot.Tags {
   if strings.HasPrefix(tag,"bh-commit-") {
    if commitTag!=""{return nil,errors.New("ambiguous BackupHub commit marker")}
    commitTag=tag
   }
  }
  if commitTag==""{continue}
  data,err:=r.run(ctx,nil,"dump",snapshot.ID,"/backuphub-tree-manifest.json")
  if err!=nil{return nil,fmt.Errorf("cannot read BackupHub portable manifest: %w",err)}
  if len(data)<2||len(data)>16384{return nil,errors.New("invalid portable manifest length")}
  var rec portableRecord
  if err=json.Unmarshal(data,&rec);err!=nil{return nil,errors.New("invalid portable manifest JSON")}
  if rec.Format!="backuphub.restic.tree.v1"||!safeID(rec.ID)||!safeID(rec.SourceID)||
     rec.SourceName==""||len(rec.SourceName)>120||strings.ContainsAny(rec.SourceName,"\x00\r\n")||
     !validResticID(rec.ResticID)||!rec.DataVerified||!filepath.IsAbs(rec.Root)||
     commitTag!="bh-commit-"+rec.ID {
    return nil,errors.New("inconsistent BackupHub commit marker")
  }
  switch rec.Kind {case "local","smb","sftp","postgres":default:return nil,errors.New("unknown restored source kind")}
  if _,err=time.Parse(time.RFC3339Nano,rec.At);err!=nil{return nil,errors.New("invalid snapshot timestamp")}
  origin,ok:=byID[rec.ResticID]
  if !ok||!resticHasTag(origin.Tags,"bh-source-"+rec.SourceID)||!resticHasTag(origin.Tags,"bh-pending") {
   return nil,errors.New("committed manifest references an absent or unrelated data snapshot")
  }
  if seen[rec.ID]{return nil,errors.New("duplicate recovered snapshot ID")}
  seen[rec.ID]=true
  recovered=append(recovered,recoveredManifest{Record:rec,MarkerID:snapshot.ID})
 }
 return recovered,nil
}
