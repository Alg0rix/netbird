package store

import (
	"context"
	"database/sql"
	"net/netip"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/types"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func TestRelaySchemaUpgradePreservesUnconfiguredLegacyAccounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SQLite seed harness is not supported on Windows")
	}
	for _, engine := range []types.Engine{types.SqliteStoreEngine, types.PostgresStoreEngine} {
		t.Run(string(engine), func(t *testing.T) {
			if engine == types.PostgresStoreEngine && testing.Short() {
				t.Skip("PostgreSQL integration requires Docker")
			}
			t.Setenv("NETBIRD_STORE_ENGINE", string(engine))
			ctx := context.Background()
			base, cleanup, err := NewTestStoreFromSQL(ctx, "", t.TempDir())
			require.NoError(t, err)
			t.Cleanup(cleanup)
			s := base.(*SqlStore)
			legacy := newAccountWithId(ctx, "legacy-account", "legacy-owner", "legacy.example")
			legacy.Network.Serial = 17
			legacy.Peers["legacy-peer"] = &nbpeer.Peer{ID: "legacy-peer", AccountID: legacy.Id, Key: "legacy-peer-key", Name: "Legacy peer", DNSLabel: "legacy-peer", IP: netip.MustParseAddr("100.64.0.3"), Status: &nbpeer.PeerStatus{}}
			for _, group := range legacy.Groups {
				group.Peers = []string{"legacy-peer"}
				group.PublicID = group.ID
			}
			for _, policy := range legacy.Policies {
				policy.PublicID = policy.ID
			}
			require.NoError(t, s.SaveAccount(ctx, legacy))
			before, err := s.GetAccount(ctx, legacy.Id)
			require.NoError(t, err)
			nullable := newAccountWithId(ctx, "nullable-account", "nullable-owner", "")
			require.NoError(t, s.SaveAccount(ctx, nullable))
			require.NoError(t, s.db.Model(&types.Account{}).Where("id = ?", nullable.Id).Update("network_serial", nil).Error)

			// The pre-feature account schema is unchanged; removing the additive tables
			// recreates its absence of relay relations before the normal store migration.
			models := []any{&rp.ObservationRecord{}, &rp.RelayReselection{}, &rp.RelayPairPolicy{}, &rp.RelayHomeProfile{}, &rp.RelayGroup{}, &rp.RelayAccountState{}, &rp.CatalogHead{}, &rp.CatalogPublication{}}
			for _, model := range models {
				require.NoError(t, s.db.Migrator().DropTable(model))
				assert.False(t, s.db.Migrator().HasTable(model), "legacy database must not contain relay tables before upgrade")
			}
			upgraded, err := NewSqlStore(ctx, s.conn, nil, false)
			require.NoError(t, err)
			// Reopening an already upgraded store must be idempotent too.
			upgraded, err = NewSqlStore(ctx, upgraded.conn, nil, false)
			require.NoError(t, err)
			for _, model := range models {
				require.True(t, upgraded.db.Migrator().HasTable(model), "upgrade must create every additive relay relation")
				var count int64
				require.NoError(t, upgraded.db.Model(model).Count(&count).Error)
				assert.Zero(t, count, "schema migration must not provision relay instructions, catalog, or observations")
			}
			var accounts, peers int64
			require.NoError(t, upgraded.db.Model(&types.Account{}).Count(&accounts).Error)
			require.NoError(t, upgraded.db.Model(&nbpeer.Peer{}).Count(&peers).Error)
			assert.Equal(t, int64(2), accounts, "upgrade must preserve both legacy accounts")
			assert.Equal(t, int64(1), peers, "upgrade must preserve legacy peers")
			after, err := upgraded.GetAccount(ctx, legacy.Id)
			require.NoError(t, err)
			assert.Equal(t, before.Network, after.Network, "migration must preserve subnet, network identity, and serial")
			assert.Equal(t, before.Peers, after.Peers, "migration must preserve peer identity and addressing")
			assert.Equal(t, before.Users, after.Users, "migration must preserve ownership")
			assert.Equal(t, before.Groups, after.Groups, "migration must preserve group memberships")
			assert.Equal(t, before.Policies, after.Policies, "migration must preserve existing access policies")

			for accountID, serial := range map[types.AccountID]uint64{types.AccountID(legacy.Id): 17, types.AccountID(nullable.Id): 0} {
				snapshot, err := upgraded.GetRelaySnapshot(ctx, accountID)
				require.NoError(t, err)
				assert.Equal(t, serial, snapshot.NetworkSerial, "relay snapshot must preserve legacy serial semantics")
				assert.Equal(t, rp.Configuration{}, snapshot.Configuration, "legacy accounts must remain unconfigured")
				assert.Empty(t, snapshot.Reselections, "upgrade must not enqueue retries")
				assert.Zero(t, snapshot.Catalog.Generation, "upgrade must not initialize an operator catalog")
				loaded, err := upgraded.GetAccount(ctx, string(accountID))
				require.NoError(t, err)
				assert.Nil(t, loaded.RelayState, "legacy account must retain absent relay account state")
				assert.Nil(t, loaded.RelayCatalog, "legacy account must not receive catalog instructions")
				if engine == types.PostgresStoreEngine {
					native, err := upgraded.getRelaySnapshotForAccount(ctx, accountID, true)
					require.NoError(t, err)
					assert.Equal(t, snapshot, native, "pgx and GORM must agree on upgraded legacy snapshots")
				}
			}
			var nullableSerial sql.NullInt64
			require.NoError(t, upgraded.db.Raw("SELECT network_serial FROM accounts WHERE id = ?", nullable.Id).Row().Scan(&nullableSerial))
			assert.False(t, nullableSerial.Valid, "reading legacy NULL as zero must not rewrite persisted account state")
		})
	}
}
