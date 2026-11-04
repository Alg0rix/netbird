package networkmap

import (
	"fmt"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/shared/management/networkmap/nmdata"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/types"
)

func relayComputationFixture() (*NetworkMapData, *types.NetworkMapComponents) {
	peers := map[string]*nmdata.Peer{
		"x": {ID: "x", Key: "key-x", Meta: nmdata.PeerSystemMeta{Capabilities: []int32{rp.Capability}}},
		"y": {ID: "y", Key: "key-y", Meta: nmdata.PeerSystemMeta{Capabilities: []int32{rp.Capability}}},
		"z": {ID: "z", Key: "key-z", Meta: nmdata.PeerSystemMeta{Capabilities: []int32{rp.Capability}}},
	}
	data := &NetworkMapData{Peers: peers, RelayPolicies: rp.Snapshot{
		AccountID: "account", Catalog: rp.RelayCatalog{Generation: 1, Entries: []rp.RelayCatalogEntry{{ID: "a", Endpoint: "rels://a.example:443", Active: true}}},
		Configuration: rp.Configuration{Revision: 1, Policies: []rp.RelayPairPolicy{{ID: "pair", AccountID: "account", Enabled: true, Revision: 1,
			Source: rp.RelaySelector{PeerIDs: []rp.PeerID{"x"}}, Destination: rp.RelaySelector{PeerIDs: []rp.PeerID{"y", "z"}},
			Constraints: rp.RelayConstraints{Allow: &rp.RelaySelection{RelayIDs: []rp.RelayID{"a"}}},
		}}},
	}}
	components := &types.NetworkMapComponents{PeerID: "x", Peers: map[string]*nmdata.Peer{"x": peers["x"], "y": peers["y"]}, RouterPeers: map[string]*nmdata.Peer{"z": peers["z"]}}
	return data, components
}

func TestRelayPolicyComputationSymmetryAndTargetedRetry(t *testing.T) {
	data, forward := relayComputationFixture()
	data.RelayPolicies.Reselections = []rp.RelayReselection{{PeerA: "x", PeerB: "y", Generation: 2}}
	data.applyRelayPolicies(forward)
	reverse := &types.NetworkMapComponents{PeerID: "y", Peers: map[string]*nmdata.Peer{"x": data.Peers["x"], "y": data.Peers["y"]}}
	data.applyRelayPolicies(reverse)
	require.NotNil(t, forward.PairRelayPolicies["y"])
	assert.Equal(t, forward.PairRelayPolicies["y"], reverse.PairRelayPolicies["x"], "both endpoints receive identical policy")
	assert.Equal(t, uint64(2), forward.PairRelayPolicies["y"].ReselectionGeneration, "selected pair receives retry directive")
	assert.Zero(t, forward.PairRelayPolicies["z"].ReselectionGeneration, "router-only unrelated pair retains its generation")
	compact := forward.ToCompact().ToFull()
	assert.Equal(t, forward.PairRelayPolicies, compact.PairRelayPolicies, "compact form preserves policies")
	compact.PairRelayPolicies["y"].Tiers[0][0].ID = "changed"
	assert.Equal(t, rp.RelayID("a"), forward.PairRelayPolicies["y"].Tiers[0][0].ID, "compact round trip owns nested tiers")
}

func TestRelayCapabilityLossWithholdsAllTransportReferences(t *testing.T) {
	for _, endpoint := range []string{"x", "y", "z"} {
		t.Run(endpoint, func(t *testing.T) {
			data, components := relayComputationFixture()
			data.Peers[endpoint].Meta.Capabilities = nil
			routes := []*nmdata.Route{{Peer: "key-y"}, {PeerID: "z"}}
			routers := map[string]map[string]*nmdata.NetworkRouter{"network": {"y": {}, "z": {}}}
			components.Routes, components.RoutersMap = routes, routers
			data.applyRelayPolicies(components)
			blocked := endpoint
			if endpoint == "x" {
				blocked = "y"
			}
			assert.NotContains(t, components.Peers, blocked, "unsupported endpoint cannot receive a peer key")
			assert.NotContains(t, components.RouterPeers, blocked, "unsupported router cannot remain reachable")
			assert.Equal(t, rp.RelayPolicyUnsupported, components.PairRelayPolicies[rp.PeerID(blocked)].State, "diagnostics retain unsupported state")
			assert.Nil(t, components.PairRelayPolicies[rp.PeerID(blocked)].Tiers, "unsupported policy offers no relay")
			assert.NotContains(t, components.RoutersMap["network"], blocked, "network router reference is removed")
			for _, route := range components.Routes {
				assert.NotEqual(t, data.Peers[blocked].Key, route.Peer, "route cannot retain blocked overlay key")
				assert.NotEqual(t, blocked, route.PeerID, "route cannot retain blocked peer ID")
			}
			assert.Len(t, routers["network"], 2, "withholding must not mutate shared router input")
			assert.Len(t, routes, 2, "withholding must not mutate shared route input")
		})
	}
}

func TestHomeOnlyAndClearedPoliciesPreserveLegacyReachability(t *testing.T) {
	for _, homeOnly := range []bool{false, true} {
		data, components := relayComputationFixture()
		data.RelayPolicies.Policies = nil
		data.Peers["y"].Meta.Capabilities = nil
		if homeOnly {
			data.RelayPolicies.Profiles = []rp.RelayHomeProfile{{ID: "home", AccountID: "account", Enabled: true,
				Selector: rp.RelaySelector{PeerIDs: []rp.PeerID{"x"}}, Constraints: rp.RelayConstraints{Allow: &rp.RelaySelection{RelayIDs: []rp.RelayID{"a"}}}}}
		}
		data.applyRelayPolicies(components)
		assert.Contains(t, components.Peers, "y", "home-only or cleared policy preserves legacy remote connectivity")
		assert.Empty(t, components.PairRelayPolicies, "home preference must not create restrictive pair policy")
		if homeOnly {
			require.NotNil(t, components.HomeRelayPolicy)
		}
	}
}

func BenchmarkRelayPolicyComputation(b *testing.B) {
	for _, peerCount := range []int{3, 100, 1000} {
		b.Run(fmt.Sprintf("peers-%d", peerCount), func(b *testing.B) {
			data, _ := relayComputationFixture()
			for i := 3; i < peerCount; i++ {
				id := fmt.Sprintf("peer-%d", i)
				data.Peers[id] = &nmdata.Peer{ID: id, Key: id, Meta: nmdata.PeerSystemMeta{Capabilities: []int32{rp.Capability}}}
				data.RelayPolicies.Policies[0].Destination.PeerIDs = append(data.RelayPolicies.Policies[0].Destination.PeerIDs, rp.PeerID(id))
			}
			b.ReportAllocs()
			for b.Loop() {
				components := &types.NetworkMapComponents{PeerID: "x", Peers: maps.Clone(data.Peers)}
				data.applyRelayPolicies(components)
			}
		})
	}
}
