// Package connectors defines native backup source connectors. It deliberately
// does not offer arbitrary shell scripts as a default connector.
package connectors

import (
 "context"
 "io"
)

type Capability struct {
 FileTree bool
 ApplicationConsistent bool
 ACL bool
 ExtendedAttributes bool
 ObjectRestore bool
 RemoteExecution bool
}
type Endpoint struct {
 Address string
 SecretRef string // Reference into a separate encrypted vault, not credentials.
}
type Dataset struct {
 ID string
 Name string
 Kind string
}
type Plan struct {
 Dataset Dataset
 CaptureStrategy string
 Consistency string
 Exclusions []string
}
type Entry struct {
 Path string
 Size int64
 Mode uint32
 ModTimeUnix int64
}
type Reader interface {
 Next(ctx context.Context) (Entry,io.ReadCloser,error)
 Close() error
}
type RestoreRequest struct {
 SnapshotID string
 Paths []string
 Destination string
 OverwritePolicy string
}
type RestorePlan struct {
 Request RestoreRequest
 Warnings []string
}

// Connector methods are operations, not shell command templates. Capture
// streams source objects to the engine, which owns encryption and durability.
type Connector interface {
 Kind() string
 Capabilities() Capability
 Discover(ctx context.Context,endpoint Endpoint)([]Dataset,error)
 Validate(ctx context.Context,endpoint Endpoint,dataset Dataset) error
 Plan(ctx context.Context,endpoint Endpoint,dataset Dataset)(Plan,error)
 Capture(ctx context.Context,plan Plan)(Reader,error)
 Verify(ctx context.Context,plan Plan) error
 RestorePlan(ctx context.Context,request RestoreRequest)(RestorePlan,error)
 Restore(ctx context.Context,plan RestorePlan,reader Reader)error
 Cleanup(ctx context.Context,plan Plan) error
}
