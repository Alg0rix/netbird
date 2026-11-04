package store

import (
	"context"

	"github.com/netbirdio/netbird/management/server/types"
	sharedrelay "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// RelayPolicyStore extends SQL-backed stores without requiring legacy stores to claim policy support.
type RelayPolicyStore interface {
	LockRelayConfiguration(context.Context, types.AccountID) error
	GetRelayCatalog(context.Context) (types.RelayCatalog, error)
	InitializeRelayCatalog(context.Context, types.RelayCatalog) (types.RelayCatalog, error)
	PreviewRelayCatalog(context.Context, types.RelayCatalog, uint64) (*RelayCatalogImpact, error)
	PublishRelayCatalog(context.Context, types.RelayCatalog, uint64) (types.RelayCatalog, error)
	GetRelaySnapshot(context.Context, types.AccountID) (sharedrelay.Snapshot, error)
	SaveRelayConfiguration(context.Context, types.AccountID, uint64, uint64, sharedrelay.Configuration) (sharedrelay.Configuration, error)
	AdvanceRelayReselection(context.Context, types.AccountID, types.PeerID, types.PeerID, uint64) (uint64, error)
}

var _ RelayPolicyStore = (*SqlStore)(nil)
