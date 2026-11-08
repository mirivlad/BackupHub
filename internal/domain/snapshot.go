package domain

import (
 "errors"
 "fmt"
 "regexp"
 "strings"
 "time"
)

// Snapshot manifests are portable metadata; SQLite is not the source of truth.
const SnapshotSchemaVersion = 1

type CaptureState string
const (
 CaptureStaged CaptureState = "staged"
 CaptureCommitted CaptureState = "committed"
 CaptureFailed CaptureState = "failed"
)
type Consistency string
const (
 ConsistencyBestEffort Consistency = "best_effort"
 ConsistencyCrash Consistency = "crash_consistent"
 ConsistencyApplication Consistency = "application_consistent"
)
type MetadataFidelity string
const (
 MetadataContent MetadataFidelity = "content_only"
 MetadataBasic MetadataFidelity = "basic_metadata"
 MetadataFull MetadataFidelity = "full_metadata_verified"
)
type Verification string
const (
 VerificationNotRun Verification = "not_run"
 VerificationPassed Verification = "passed"
 VerificationFailed Verification = "failed"
)

// A required component must exist and have a digest before a snapshot can commit.
type Component struct {
 Name string `json:"name"`
 Required bool `json:"required"`
 Captured bool `json:"captured"`
 SizeBytes int64 `json:"size_bytes"`
 SHA256 string `json:"sha256,omitempty"`
}
type Snapshot struct {
 SchemaVersion int `json:"schema_version"`
 ID string `json:"id"`
 SourceID string `json:"source_id"`
 DatasetID string `json:"dataset_id"`
 StartedAt time.Time `json:"started_at"`
 FinishedAt time.Time `json:"finished_at"`
 State CaptureState `json:"state"`
 Consistency Consistency `json:"consistency"`
 MetadataFidelity MetadataFidelity `json:"metadata_fidelity"`
 DataVerification Verification `json:"data_verification"`
 RestoreVerification Verification `json:"restore_verification"`
 RestoreRecipe string `json:"restore_recipe,omitempty"`
 Components []Component `json:"components"`
}
var sha256Pattern = regexp.MustCompile("^[0-9a-f]{64}$")

func (s Snapshot) Validate() error {
 if s.SchemaVersion != SnapshotSchemaVersion { return fmt.Errorf("unsupported snapshot schema: %d", s.SchemaVersion) }
 if strings.TrimSpace(s.ID)=="" || strings.TrimSpace(s.SourceID)=="" || strings.TrimSpace(s.DatasetID)=="" { return errors.New("snapshot identity is incomplete") }
 if s.StartedAt.IsZero() || s.FinishedAt.IsZero() || s.FinishedAt.Before(s.StartedAt) { return errors.New("invalid snapshot times") }
 switch s.State { case CaptureStaged, CaptureCommitted, CaptureFailed: default: return errors.New("invalid capture state") }
 switch s.Consistency { case ConsistencyBestEffort, ConsistencyCrash, ConsistencyApplication: default: return errors.New("consistency level must be explicit") }
 switch s.MetadataFidelity { case MetadataContent, MetadataBasic, MetadataFull: default: return errors.New("metadata fidelity must be explicit") }
 switch s.DataVerification { case VerificationNotRun, VerificationPassed, VerificationFailed: default: return errors.New("invalid data verification") }
 switch s.RestoreVerification { case VerificationNotRun, VerificationPassed, VerificationFailed: default: return errors.New("invalid restore verification") }
 if len(s.Components)==0 { return errors.New("snapshot has no components") }
 seen:=make(map[string]bool,len(s.Components))
 for _,c:=range s.Components {
  if strings.TrimSpace(c.Name)=="" || strings.Contains(c.Name,"/") || strings.Contains(c.Name,"\\") || c.Name=="." || c.Name==".." { return errors.New("invalid component name") }
  if seen[c.Name] { return fmt.Errorf("duplicate component %q",c.Name) }
  seen[c.Name]=true
  if c.SizeBytes<0 { return errors.New("negative component size") }
  if c.Captured && !sha256Pattern.MatchString(c.SHA256) { return fmt.Errorf("missing or invalid digest for %q",c.Name) }
  if s.State==CaptureCommitted && c.Required && !c.Captured { return fmt.Errorf("required component missing: %q",c.Name) }
 }
 if s.State==CaptureCommitted && s.DataVerification==VerificationFailed { return errors.New("failed verification cannot be committed") }
 if s.State!=CaptureCommitted && s.RestoreVerification==VerificationPassed { return errors.New("uncommitted snapshot cannot be restore-verified") }
 return nil
}

// Usable means a complete committed set; independent restore testing is tracked separately.
func (s Snapshot) Usable() bool { return s.State==CaptureCommitted && s.Validate()==nil }
