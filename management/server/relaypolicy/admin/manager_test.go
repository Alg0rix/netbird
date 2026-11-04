package admin

import (
	"context"
	"net/netip"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/management/server/activity"
	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/permissions"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
	sharedrelay "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

func newManagerFixture(t *testing.T) (*Manager, store.Store, *int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SQLite store is not supported on Windows")
	}
	ctx := context.Background()
	s, cleanup, err := store.NewTestStoreFromSQL(ctx, "", t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	account := &types.Account{Id: "account", Network: types.NewNetwork(), Settings: &types.Settings{}, Users: make(map[string]*types.User), Peers: make(map[string]*nbpeer.Peer)}
	for _, role := range []types.UserRole{types.UserRoleOwner, types.UserRoleAdmin, types.UserRoleNetworkAdmin, types.UserRoleAuditor, types.UserRoleUser} {
		user := types.NewOwnerUser(string(role), "", "")
		user.AccountID, user.Role = account.Id, role
		account.Users[user.Id] = user
	}
	serviceUser := types.NewOwnerUser("service-user", "", "")
	serviceUser.AccountID, serviceUser.Role, serviceUser.IsServiceUser = account.Id, types.UserRoleUser, true
	account.Users[serviceUser.Id] = serviceUser
	for i, id := range []string{"x", "y", "z"} {
		account.Peers[id] = &nbpeer.Peer{ID: id, AccountID: account.Id, Key: "key-" + id, IP: netip.AddrFrom4([4]byte{100, 64, 0, byte(i + 1)}), Status: &nbpeer.PeerStatus{}, Meta: nbpeer.PeerSystemMeta{Capabilities: []int32{sharedrelay.Capability}}}
	}
	require.NoError(t, account.AddAllGroup(false))
	for _, policy := range account.Policies {
		policy.AccountID = account.Id
	}
	require.NoError(t, s.SaveAccount(ctx, account))
	repo, ok := s.(store.RelayPolicyStore)
	require.True(t, ok, "fixture requires the real SQL relay repository")
	_, err = repo.InitializeRelayCatalog(ctx, sharedrelay.RelayCatalog{Entries: []sharedrelay.RelayCatalogEntry{
		{ID: "a", Endpoint: "rels://a.example:443", Active: true},
		{ID: "b", Endpoint: "rels://b.example:443", Active: true},
	}})
	require.NoError(t, err)
	audits := 0
	m := NewManager(s, permissions.NewManager(s), Hooks{Audit: func(context.Context, string, string, string, activity.ActivityDescriber, map[string]any) { audits++ }})
	return m, s, &audits
}

func pairConfiguration() sharedrelay.Configuration {
	return sharedrelay.Configuration{Policies: []sharedrelay.RelayPairPolicy{{
		ID: "pair", AccountID: "account", Name: "Pair", Enabled: true,
		Source:      sharedrelay.RelaySelector{PeerIDs: []sharedrelay.PeerID{"x"}},
		Destination: sharedrelay.RelaySelector{PeerIDs: []sharedrelay.PeerID{"y"}},
		Constraints: sharedrelay.RelayConstraints{Allow: &sharedrelay.RelaySelection{RelayIDs: []sharedrelay.RelayID{"a", "b"}}, ForceRelay: true, Preferences: []sharedrelay.RelaySelection{{RelayIDs: []sharedrelay.RelayID{"a"}}}},
	}}}
}

func TestManagerRoleAndAccountBoundaries(t *testing.T) {
	m, _, _ := newManagerFixture(t)
	ctx := context.Background()
	for _, user := range []string{"owner", "admin", "network_admin", "auditor"} {
		_, err := m.Get(ctx, "account", user)
		assert.NoError(t, err)
	}
	for _, user := range []string{"user", "service-user"} {
		_, err := m.Get(ctx, "account", user)
		assert.Error(t, err, "ordinary service users must not inherit administrative read access")
	}
	_, err := m.Get(ctx, "foreign", "owner")
	assert.Error(t, err, "account ownership must be checked before returning configuration")
	_, err = m.Save(ctx, "account", "auditor", 0, 1, pairConfiguration())
	assert.Error(t, err, "auditors cannot mutate relay policy")
	for _, user := range []string{"owner", "admin", "network_admin"} {
		assert.NoError(t, m.authorize(ctx, "account", user, "update"))
	}
}

func TestPreviewSaveReverseLookupAndNoFalseAudit(t *testing.T) {
	m, _, audits := newManagerFixture(t)
	ctx := context.Background()
	preview, err := m.Preview(ctx, "account", "owner", pairConfiguration())
	require.NoError(t, err)
	require.Len(t, preview.Pairs, 1)
	saved, err := m.Save(ctx, "account", "owner", 0, 1, pairConfiguration())
	require.NoError(t, err)
	assert.Equal(t, 1, *audits, "only a committed mutation is audited")
	effective, err := m.Effective(ctx, "account", "owner", "y", "x")
	require.NoError(t, err)
	assert.Equal(t, preview.Pairs[0].Policy, effective, "preview and persistence must produce identical symmetric instructions")
	_, err = m.Save(ctx, "account", "owner", 0, 1, saved)
	assert.ErrorIs(t, err, store.ErrRelayRevisionConflict)
	assert.Equal(t, 1, *audits, "a stale write must not emit success audit")
	snapshot, err := m.Get(ctx, "account", "owner")
	require.NoError(t, err)
	assert.Equal(t, saved, snapshot.Configuration, "stale writes preserve the committed configuration")
}

func TestActivationRejectsUnsupportedAndBlockedWithoutMutation(t *testing.T) {
	m, s, audits := newManagerFixture(t)
	ctx := context.Background()
	peer, err := s.GetPeerByID(ctx, store.LockingStrengthNone, "account", "y")
	require.NoError(t, err)
	peer.Meta.Capabilities = nil
	require.NoError(t, s.SavePeer(ctx, "account", peer))
	preview, err := m.Preview(ctx, "account", "owner", pairConfiguration())
	require.NoError(t, err)
	require.Len(t, preview.Pairs, 1)
	assert.True(t, preview.Pairs[0].Unsupported, "preview must expose unsupported endpoints")
	_, err = m.Save(ctx, "account", "owner", 0, 1, pairConfiguration())
	assert.Error(t, err)
	peer.Meta.Capabilities = []int32{sharedrelay.Capability}
	require.NoError(t, s.SavePeer(ctx, "account", peer))
	blocked := pairConfiguration()
	blocked.Policies[0].Constraints.Exclude.RelayIDs = []sharedrelay.RelayID{"a", "b"}
	preview, err = m.Preview(ctx, "account", "owner", blocked)
	require.NoError(t, err)
	assert.Equal(t, sharedrelay.RelayPolicyBlocked, preview.Pairs[0].Policy.State, "read-only preview explains conflicting restrictions")
	_, err = m.Save(ctx, "account", "owner", 0, 1, blocked)
	assert.Error(t, err)
	assert.Zero(t, *audits, "failed activation must not emit configuration audit")
	snapshot, err := m.Get(ctx, "account", "owner")
	require.NoError(t, err)
	assert.Zero(t, snapshot.Revision, "failed activation must roll back its policy revision")
}

func TestRetryIsTargetedFencedAndDurablyRateLimited(t *testing.T) {
	m, _, audits := newManagerFixture(t)
	ctx := context.Background()
	_, err := m.Save(ctx, "account", "owner", 0, 1, pairConfiguration())
	require.NoError(t, err)
	policy, err := m.Effective(ctx, "account", "owner", "x", "y")
	require.NoError(t, err)
	result, err := m.Retry(ctx, "account", "owner", "y", "x", policy.Digest)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), result.Generation, "the first targeted retry has a durable generation")
	assert.Equal(t, policy.Tiers, result.Policy.Tiers, "retry must preserve configured allowed choices and preference order")
	_, err = m.Retry(ctx, "account", "owner", "x", "y", policy.Digest)
	assert.ErrorIs(t, err, store.ErrRelayRevisionConflict)
	_, err = m.Retry(ctx, "account", "owner", "x", "y", result.Policy.Digest)
	statusErr, ok := status.FromError(err)
	require.True(t, ok, "rate limit must be a typed API error")
	assert.Equal(t, status.TooManyRequests, statusErr.Type(), "reversed pair retry shares the persisted rate limit")
	snapshot, err := m.Get(ctx, "account", "owner")
	require.NoError(t, err)
	require.Len(t, snapshot.Reselections, 1)
	assert.Equal(t, sharedrelay.PeerID("x"), snapshot.Reselections[0].PeerA, "retry stores one canonical unordered target")
	assert.Equal(t, 2, *audits, "only the configuration and successful retry emit audit")
}

func TestManagerBoundsApplyOutsideHTTP(t *testing.T) {
	config := pairConfiguration()
	config.Policies[0].Source.PeerIDs = make([]sharedrelay.PeerID, configurationLimit+1)
	assert.Error(t, validateNames(config), "manager calls must obey collection bounds without relying on HTTP body limits")
}
