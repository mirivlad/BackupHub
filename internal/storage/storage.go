// Package storage separates snapshot-producing connectors from the encrypted
// repository engine. Restic is the preferred backend, not a UI/API dependency.
package storage

import (
 "context"
 "io"
 "github.com/mirivlad/BackupHub/internal/domain"
)

// Transaction MUST be abortable. No incomplete dataset may be published.
type Transaction interface {
 Put(ctx context.Context,name string,data io.Reader)(domain.Component,error)
 Commit(ctx context.Context,manifest domain.Snapshot)(string,error)
 Abort(ctx context.Context)error
}
type Engine interface {
 Begin(ctx context.Context,datasetID string)(Transaction,error)
 Read(ctx context.Context,snapshotID string,component string)(io.ReadCloser,error)
 ReadManifest(ctx context.Context,snapshotID string)(domain.Snapshot,error)
 Verify(ctx context.Context,snapshotID string)error
}

// Retention and destructive pruning are intentionally NOT exposed here.
