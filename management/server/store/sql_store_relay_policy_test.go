package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/netbirdio/netbird/management/internals/relaypolicydb"
	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/types"
	sharedrelay "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func newRelayStore(t *testing.T) (*SqlStore, *types.Account) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SQLite store is not supported on Windows")
	}
	ctx := context.Background()
	base, cleanup, err := NewTestStoreFromSQL(ctx, "", t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	sqlStore, ok := base.(*SqlStore)
	require.True(t, ok, "relay persistence tests require the real SQL store")
	account := newAccountWithId(ctx, "relay-account", "relay-owner", "")
	account.Peers = map[string]*nbpeer.Peer{
		"x": {ID: "x", DNSLabel: "x", IP: netip.MustParseAddr("100.64.0.1"), AccountID: account.Id, Key: "relay-x"},
		"y": {ID: "y", DNSLabel: "y", IP: netip.MustParseAddr("100.64.0.2"), AccountID: account.Id, Key: "relay-y"},
	}
	require.NoError(t, base.SaveAccount(ctx, account))
	_, err = sqlStore.InitializeRelayCatalog(ctx, relayTestCatalog())
	require.NoError(t, err)
	return sqlStore, account
}

func relayTestCatalog() types.RelayCatalog {
	return types.RelayCatalog{Entries: []types.RelayCatalogEntry{
		{ID: "a", Endpoint: "rels://a.example:443", Active: true},
		{ID: "b", Endpoint: "rels://b.example:443", Active: true},
	}}
}

func relayTestConfiguration() sharedrelay.Configuration {
	return sharedrelay.Configuration{
		Groups: []types.RelayGroup{{ID: "region", AccountID: "relay-account", Name: "Region", RelayIDs: []types.RelayID{"a", "b"}}},
		Profiles: []types.RelayHomeProfile{{ID: "home", AccountID: "relay-account", Enabled: true,
			Selector:    types.RelaySelector{PeerIDs: []types.PeerID{"x"}},
			Constraints: types.RelayConstraints{Allow: &types.RelaySelection{RelayIDs: []types.RelayID{"a"}}}}},
		Policies: []types.RelayPairPolicy{{ID: "pair", AccountID: "relay-account", Enabled: true,
			Source: types.RelaySelector{PeerIDs: []types.PeerID{"x"}}, Destination: types.RelaySelector{PeerIDs: []types.PeerID{"y"}},
			Constraints: types.RelayConstraints{Allow: &types.RelaySelection{GroupIDs: []types.RelayGroupID{"region"}}, ForceRelay: true}}},
	}
}

func TestRelayConfigurationPersistenceAndAccountCopy(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	empty, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Zero(t, empty.Revision, "existing accounts receive no implicit relay configuration")
	assert.Empty(t, empty.Policies, "legacy pairs remain unconfigured")
	saved, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), saved.Revision, "first mutation advances account revision")
	loaded, err := s.GetAccount(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, saved, loaded.RelaySnapshot().Configuration, "account loader must retain all relay relations")
	copied := loaded.Copy()
	copied.RelayGroups[0].RelayIDs[0] = "other"
	copied.RelayPairPolicies[0].Constraints.Allow.GroupIDs[0] = "other"
	assert.Equal(t, types.RelayID("a"), loaded.RelayGroups[0].RelayIDs[0], "account copy owns its relay membership slice")
	assert.Equal(t, types.RelayGroupID("region"), loaded.RelayPairPolicies[0].Constraints.Allow.GroupIDs[0], "account copy owns nested selectors")
	assert.Equal(t, empty.NetworkSerial+1, loaded.Network.Serial, "policy relation and network serial advance in one commit")
}

func TestRelayConfigurationRollbackAndRevisionConflict(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	saved, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	before, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	_, err = s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, saved)
	assert.ErrorIs(t, err, ErrRelayRevisionConflict)
	bad := saved.Copy()
	bad.Groups = nil
	_, err = s.SaveRelayConfiguration(ctx, "relay-account", 1, 1, bad)
	assert.Error(t, err)
	after, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, before, after, "validation and stale writes roll back both configuration and network serial")
	rollback := errors.New("interrupt transaction")
	err = s.ExecuteInTransaction(ctx, func(tx Store) error {
		_, err := tx.(RelayPolicyStore).SaveRelayConfiguration(ctx, "relay-account", 1, 1, saved)
		require.NoError(t, err)
		return rollback
	})
	assert.ErrorIs(t, err, rollback)
	after, err = s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, before, after, "outer transaction failure rolls back the successful relay mutation")
}

func TestRelayRetryAndPeerLifecycle(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	_, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	first, err := s.AdvanceRelayReselection(ctx, "relay-account", "y", "x", 1)
	require.NoError(t, err)
	_, err = s.AdvanceRelayReselection(ctx, "relay-account", "x", "y", 1)
	assert.Error(t, err, "opposite pair orientation must share the persisted retry rate limit")
	require.NoError(t, s.db.Model(&types.RelayReselection{}).Where("account_id = ?", "relay-account").
		Update("last_requested_at", time.Now().Add(-2*RelayRetryMinimumInterval)).Error)
	second, err := s.AdvanceRelayReselection(ctx, "relay-account", "x", "y", 1)
	require.NoError(t, err)
	assert.Equal(t, first+1, second, "both pair orientations share one monotonic retry directive")
	_, err = s.AdvanceRelayReselection(ctx, "relay-account", "x", "foreign", 1)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, s.DeletePeer(ctx, "relay-account", "x"))
	snapshot, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Reselections, "peer deletion removes targeted retry state")
	assert.Empty(t, snapshot.Policies, "explicit pair policies cannot retain dangling peer references")
	assert.Empty(t, snapshot.Profiles, "single-peer home profiles are removed with their peer")
}

func TestRelayAccountDeletionIsScoped(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	_, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	other := newAccountWithId(ctx, "other-account", "other-owner", "")
	require.NoError(t, s.SaveAccount(ctx, other))
	otherConfig := sharedrelay.Configuration{Groups: []types.RelayGroup{{ID: "other-region", AccountID: "other-account", RelayIDs: []types.RelayID{"b"}}}}
	_, err = s.SaveRelayConfiguration(ctx, "other-account", 0, 1, otherConfig)
	require.NoError(t, err)
	require.NoError(t, s.DeleteAccount(ctx, &types.Account{Id: "relay-account"}))
	var count int64
	require.NoError(t, s.db.Model(&types.RelayGroup{}).Where("account_id = ?", "relay-account").Count(&count).Error)
	assert.Zero(t, count, "account deletion cleans unloaded relay relations")
	remaining, err := s.GetRelaySnapshot(ctx, "other-account")
	require.NoError(t, err)
	assert.Len(t, remaining.Groups, 1, "another account's configuration remains intact")
	catalog, err := s.GetRelayCatalog(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), catalog.Generation, "account deletion cannot delete shared operator publications")
}

type relaySQLRows struct{ *sql.Rows }

func (rows relaySQLRows) Close() { _ = rows.Rows.Close() }

func TestRelaySqliteSnapshotReaderParity(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	_, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	gormSnapshot, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	db, err := s.db.DB()
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	snapshot, err := relaypolicydb.ReadSnapshot(ctx, func(ctx context.Context, query string, args ...any) (relaypolicydb.Rows, error) {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		return relaySQLRows{rows}, nil
	}, "relay-account")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	assert.Equal(t, gormSnapshot, snapshot, "store and network-map transaction readers must decode identical policy state")
}

func TestRelayConcurrentConfigurationWriters(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var successful, conflicted int
	for err := range results {
		if err == nil {
			successful++
			continue
		}
		if errors.Is(err, ErrRelayRevisionConflict) {
			conflicted++
			continue
		}
		t.Errorf("unexpected concurrent write failure: %v", err)
	}
	assert.Equal(t, 1, successful, "exactly one writer wins an expected revision")
	assert.Equal(t, 1, conflicted, "the losing writer receives an optimistic concurrency conflict")
	snapshot, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), snapshot.Revision, "the rejected transaction cannot advance the revision")
}

func TestPostgresqlRelayAccountAndSnapshotParity(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("PostgreSQL integration requires Docker")
	}
	t.Setenv("NETBIRD_STORE_ENGINE", string(types.PostgresStoreEngine))
	s, _ := newRelayStore(t)
	ctx := context.Background()
	_, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	native, err := s.getRelaySnapshotForAccount(ctx, "relay-account", true)
	require.NoError(t, err)
	gormSnapshot, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, gormSnapshot, native, "pgx and GORM snapshots preserve all serialized relay relations")
	account, err := s.GetAccount(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, native, account.RelaySnapshot(), "pgx account loading must attach the same catalog generation and rules")
}
