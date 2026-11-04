package networkmap_sqlite

import (
	"context"
	"database/sql"

	"github.com/netbirdio/netbird/management/internals/relaypolicydb"
	"github.com/netbirdio/netbird/shared/management/relaypolicy"
)

type relayRows struct{ *sql.Rows }

func (r relayRows) Close() { _ = r.Rows.Close() }

// GetRelayPolicySnapshot reads account rules and the active catalog in the current network-map transaction.
func (c *SqliteStoreConn) GetRelayPolicySnapshot(ctx context.Context, accountID string) (relaypolicy.Snapshot, error) {
	return relaypolicydb.ReadSnapshot(ctx, func(ctx context.Context, query string, args ...any) (relaypolicydb.Rows, error) {
		rows, err := c.Conn.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		return relayRows{rows}, nil
	}, relaypolicy.AccountID(accountID))
}
