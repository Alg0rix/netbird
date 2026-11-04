package networkmap_pgsql

import (
	"context"

	"github.com/netbirdio/netbird/management/internals/relaypolicydb"
	"github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// GetRelayPolicySnapshot reads account rules and the active catalog in the current network-map transaction.
func (c *PgStoreConn) GetRelayPolicySnapshot(ctx context.Context, accountID string) (relaypolicy.Snapshot, error) {
	return relaypolicydb.ReadSnapshot(ctx, func(ctx context.Context, sql string, args ...any) (relaypolicydb.Rows, error) {
		return c.Conn.Query(ctx, sql, args...)
	}, relaypolicy.AccountID(accountID))
}
