package domain

import (
 "strings"
 "testing"
 "time"
)

func completeSnapshot() Snapshot {
 at:=time.Date(2026,11,8,12,0,0,0,time.UTC)
 return Snapshot{
  SchemaVersion:SnapshotSchemaVersion, ID:"snapshot-1",SourceID:"source-1",DatasetID:"dataset-1",
  StartedAt:at,FinishedAt:at.Add(time.Minute), State:CaptureCommitted,
  Consistency:ConsistencyBestEffort, MetadataFidelity:MetadataBasic,
  DataVerification:VerificationNotRun,RestoreVerification:VerificationNotRun,
  Components:[]Component{{Name:"tree",Required:true,Captured:true,SizeBytes:5,SHA256:strings.Repeat("a",64)}},
 }
}
func TestSnapshotCompleteness(t *testing.T) {
 s:=completeSnapshot()
 if err:=s.Validate();err!=nil || !s.Usable(){t.Fatalf("valid snapshot: %v",err)}
 s.Components[0].Captured=false
 if s.Usable() {t.Fatal("incomplete snapshot marked usable")}
}
func TestSnapshotInvalidDigestAndDuplicate(t *testing.T) {
 s:=completeSnapshot();s.Components[0].SHA256="bad"
 if s.Validate()==nil {t.Fatal("bad digest accepted")}
 s=completeSnapshot();s.Components=append(s.Components,s.Components[0])
 if s.Validate()==nil {t.Fatal("duplicate component accepted")}
}
func TestRestoreVerificationNeedsCommit(t *testing.T){
 s:=completeSnapshot();s.State=CaptureFailed;s.RestoreVerification=VerificationPassed
 if s.Validate()==nil {t.Fatal("failed snapshot marked restore verified")}
}
