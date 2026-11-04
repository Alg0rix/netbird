package admin

import (
	"context"
	"fmt"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

func TestEndpointPolicyResolverMatchesTargetGenerationsAndACL(t *testing.T) {
	_, s, _ := newManagerFixture(t)
	account, err := s.GetAccount(context.Background(), "account")
	require.NoError(t, err)
	config := pairConfiguration()
	config.Policies[0].Destination.PeerIDs = []rp.PeerID{"y", "z"}
	config.Policies[0].Source.PeerIDs = []rp.PeerID{"x", "y"}
	config.Profiles = []rp.RelayHomeProfile{{ID: "home", AccountID: "account", Name: "Home", Enabled: true, Selector: rp.RelaySelector{PeerIDs: []rp.PeerID{"y"}}}}
	applyConfiguration(account, config)
	account.RelayReselections = []rp.RelayReselection{
		{AccountID: "account", PeerA: "y", Generation: 3},
		{AccountID: "account", PeerA: "x", PeerB: "y", Generation: 7},
		{AccountID: "account", PeerA: "y", PeerB: "z", Generation: 11},
	}
	allowedResolver, err := newEndpointPolicyResolver(account, "y")
	require.NoError(t, err)
	for target, wantGeneration := range map[rp.PeerID]uint64{"": 3, "x": 7, "z": 11} {
		policy, err := allowedResolver.resolve(context.Background(), target)
		require.NoError(t, err)
		require.NotNil(t, policy)
		assert.Equal(t, wantGeneration, policy.ReselectionGeneration, "home and both unordered pair orientations must retain their own generations")
	}
	// Only x-y remains ACL-authorized; the independent home profile still applies.
	for _, group := range account.Groups {
		group.Peers = []string{"x", "y"}
	}
	resolver, err := newEndpointPolicyResolver(account, "y")
	require.NoError(t, err)
	for _, target := range []rp.PeerID{"", "x", "z", "y", "foreign"} {
		t.Run(string(target), func(t *testing.T) {
			expected, expectedErr := effective(context.Background(), account, "y", target)
			actual, actualErr := resolver.resolve(context.Background(), target)
			if expectedErr != nil {
				require.Error(t, actualErr)
				var expectedStatus, actualStatus *status.Error
				require.ErrorAs(t, expectedErr, &expectedStatus)
				require.ErrorAs(t, actualErr, &actualStatus)
				assert.Equal(t, expectedStatus.Type(), actualStatus.Type(), "cached resolution must preserve ACL and account denial semantics")
				return
			}
			require.NoError(t, actualErr)
			assert.Equal(t, expected, actual, "home and unordered pair instructions retain their own retry generations")
		})
	}
}

func TestReportBatchKeepsHomeAndPairsSeparate(t *testing.T) {
	m, s, _ := newManagerFixture(t)
	ctx := context.Background()
	config := pairConfiguration()
	config.Policies[0].Destination.PeerIDs = []rp.PeerID{"y", "z"}
	config.Profiles = []rp.RelayHomeProfile{{ID: "home", AccountID: "account", Name: "Home", Enabled: true, Selector: rp.RelaySelector{PeerIDs: []rp.PeerID{"x"}}}}
	_, err := m.Save(ctx, "account", "owner", 0, 1, config)
	require.NoError(t, err)
	require.NoError(t, s.RegisterInstance(ctx, "instance-x", "test"))
	connected, err := s.ConnectPeer(ctx, "account", "x", "instance-x", 100)
	require.NoError(t, err)
	require.True(t, connected, "reporter must own an authenticated sync session")
	var reports []rp.PathReport
	for _, target := range []rp.PeerID{"", "y", "z"} {
		policy, err := m.Effective(ctx, "account", "owner", "x", target)
		require.NoError(t, err)
		reports = append(reports, currentReport(target, policy))
	}
	require.NoError(t, m.Report(ctx, "account", "x", 100, reports))
	observations := s.(store.RelayObservationStore)
	for _, report := range reports {
		stored, err := observations.GetRelayObservations(ctx, "account", "x", report.RemotePeerID)
		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.Equal(t, report, stored[0].Report, "each persisted target must retain its own home or pair instructions")
	}
}

func batchResolutionAccount(b *testing.B, count int) (*types.Account, []rp.PeerID) {
	b.Helper()
	account := &types.Account{Id: "account", Network: types.NewNetwork(), Settings: &types.Settings{}, Peers: make(map[string]*nbpeer.Peer)}
	targets := make([]rp.PeerID, 0, count)
	for i := 0; i <= count; i++ {
		id := fmt.Sprintf("peer-%03d", i)
		account.Peers[id] = &nbpeer.Peer{ID: id, AccountID: account.Id, IP: netip.AddrFrom4([4]byte{100, 64, byte(i / 256), byte(i % 256)}), Status: &nbpeer.PeerStatus{}}
		if i > 0 {
			targets = append(targets, rp.PeerID(id))
		}
	}
	require.NoError(b, account.AddAllGroup(false))
	account.RelayCatalog = &rp.RelayCatalog{Generation: 1, Entries: []rp.RelayCatalogEntry{{ID: "a", Endpoint: "rels://a.example:443", Active: true}}}
	account.RelayPairPolicies = []rp.RelayPairPolicy{{ID: "pair", AccountID: "account", Enabled: true, Source: rp.RelaySelector{PeerIDs: []rp.PeerID{"peer-000"}}, Destination: rp.RelaySelector{PeerIDs: targets}}}
	return account, targets
}

func BenchmarkReportPolicyResolution(b *testing.B) {
	account, targets := batchResolutionAccount(b, 128)
	ctx := context.Background()
	b.Run("per-target-indexes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, target := range targets {
				_, err := effective(ctx, account, "peer-000", target)
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("per-batch-indexes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			resolver, err := newEndpointPolicyResolver(account, "peer-000")
			if err != nil {
				b.Fatal(err)
			}
			for _, target := range targets {
				_, err := resolver.resolve(ctx, target)
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
