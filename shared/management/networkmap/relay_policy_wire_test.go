package networkmap_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goproto "google.golang.org/protobuf/proto"

	mgmtgrpc "github.com/netbirdio/netbird/management/internals/shared/grpc"
	nbnetworkmap "github.com/netbirdio/netbird/shared/management/networkmap"
	"github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func wireRelayPolicy() *rp.ResolvedRelayPolicy {
	return &rp.ResolvedRelayPolicy{
		State: rp.RelayPolicyReady, Revision: 4, CatalogGeneration: 2, ReselectionGeneration: 3,
		Digest: rp.Digest(strings.Repeat("a", 64)), ForceRelay: true,
		Tiers: [][]rp.RelayCatalogEntry{
			{{ID: "a", Endpoint: "rels://a.example:443", InstanceEndpoint: "rels://a.example:443", Active: true}},
			{{ID: "b", Endpoint: "rels://b.example:443", InstanceEndpoint: "rels://b.example:443", Active: true}},
		},
		Contributors: []rp.Contribution{{ID: "pair", Revision: 4}}, PreferenceRule: "pair",
	}
}

func TestRelayPolicyProtocolPresenceAndValidation(t *testing.T) {
	absent, err := nbnetworkmap.FromProtocolRelayPolicy(nil)
	require.NoError(t, err)
	assert.Nil(t, absent, "absent instructions retain legacy relay selection")
	policy := wireRelayPolicy()
	wire := nbnetworkmap.ToProtocolRelayPolicy(policy)
	decoded, err := nbnetworkmap.FromProtocolRelayPolicy(wire)
	require.NoError(t, err)
	assert.True(t, goproto.Equal(wire, nbnetworkmap.ToProtocolRelayPolicy(decoded)), "priority tiers and every generation survive conversion")
	blocked := policy.Copy()
	blocked.State, blocked.Tiers = rp.RelayPolicyBlocked, nil
	decoded, err = nbnetworkmap.FromProtocolRelayPolicy(nbnetworkmap.ToProtocolRelayPolicy(blocked))
	require.NoError(t, err)
	require.NotNil(t, decoded)
	assert.Equal(t, rp.RelayPolicyBlocked, decoded.State, "an empty restriction must not become absent policy")
	for _, mutate := range []func(*proto.RelayPolicy){
		func(p *proto.RelayPolicy) { p.State = proto.RelayPolicyState(999) },
		func(p *proto.RelayPolicy) { p.Digest = "" },
		func(p *proto.RelayPolicy) { p.Tiers = nil },
		func(p *proto.RelayPolicy) { p.Tiers[0].Relays[0].Endpoint = "https://foreign.example:443" },
		func(p *proto.RelayPolicy) { p.Tiers[1].Relays[0].Id = "a" },
		func(p *proto.RelayPolicy) { p.Tiers[0].Relays[0].Legacy = true },
	} {
		invalid := goproto.Clone(wire).(*proto.RelayPolicy)
		mutate(invalid)
		_, err := nbnetworkmap.FromProtocolRelayPolicy(invalid)
		assert.Error(t, err)
	}
}

func TestRelayPolicyLegacyAndComponentParityAndClear(t *testing.T) {
	ctx := context.Background()
	components, localKey := buildSmokeComponents(t)
	policy := wireRelayPolicy()
	components.HomeRelayPolicy = policy.Copy()
	components.PairRelayPolicies = map[rp.PeerID]*rp.ResolvedRelayPolicy{"peer-B": policy.Copy()}
	components.Peers["peer-A"].Meta.Capabilities = append(components.Peers["peer-A"].Meta.Capabilities, rp.Capability)
	components.Peers["peer-B"].Meta.Capabilities = append(components.Peers["peer-B"].Meta.Capabilities, rp.Capability)
	legacy := mgmtgrpc.ToSyncResponse(ctx, nil, nil, nil, components.Peers["peer-A"], nil, nil,
		components.Calculate(ctx), "netbird.cloud", nil, nil, components.AccountSettings, nil, nil, 0)
	envelope := mgmtgrpc.EncodeNetworkMapEnvelope(mgmtgrpc.ComponentsEnvelopeInput{
		Components: components, DNSDomain: "netbird.cloud", PeerConfig: legacy.PeerConfig,
	})
	encoded, err := goproto.Marshal(envelope)
	require.NoError(t, err)
	var received proto.NetworkMapEnvelope
	require.NoError(t, goproto.Unmarshal(encoded, &received))
	result, err := nbnetworkmap.EnvelopeToNetworkMap(ctx, &received, localKey, "netbird.cloud", false)
	require.NoError(t, err)
	require.Len(t, result.NetworkMap.RemotePeers, 1)
	require.Len(t, legacy.NetworkMap.RemotePeers, 1)
	assert.True(t, goproto.Equal(legacy.NetworkMap.RemotePeers[0].RelayPolicy, result.NetworkMap.RemotePeers[0].RelayPolicy), "both network-map forms deliver identical pair instructions")
	assert.True(t, goproto.Equal(legacy.PeerConfig.HomeRelayPolicy, result.NetworkMap.PeerConfig.HomeRelayPolicy), "home instructions retain independent presence")
	remoteKey := components.Peers["peer-B"].Key
	require.Contains(t, result.Components.PairRelayPolicies, rp.PeerID(remoteKey))
	assert.Equal(t, policy.Digest, result.Components.PairRelayPolicies[rp.PeerID(remoteKey)].Digest, "component IDs remap to the decoder's stable public keys")
	assert.True(t, result.Components.Peers[remoteKey].HasCapability(rp.Capability), "component decoding retains relay-policy capability")

	components.HomeRelayPolicy, components.PairRelayPolicies = nil, nil
	cleared := mgmtgrpc.EncodeNetworkMapEnvelope(mgmtgrpc.ComponentsEnvelopeInput{
		Components: components, DNSDomain: "netbird.cloud", PeerConfig: &proto.PeerConfig{},
	})
	afterClear, err := nbnetworkmap.EnvelopeToNetworkMap(ctx, cleared, localKey, "netbird.cloud", false)
	require.NoError(t, err)
	assert.Nil(t, afterClear.NetworkMap.PeerConfig.HomeRelayPolicy, "explicit replacement snapshot clears home instructions")
	assert.Nil(t, afterClear.NetworkMap.RemotePeers[0].RelayPolicy, "explicit replacement snapshot clears pair instructions")
	assert.NotNil(t, result.NetworkMap.RemotePeers[0].RelayPolicy, "decoding a new snapshot cannot mutate the previous snapshot")
}

func TestRelayPolicyComponentMalformedPresenceFailsClosed(t *testing.T) {
	components, localKey := buildSmokeComponents(t)
	components.PairRelayPolicies = map[rp.PeerID]*rp.ResolvedRelayPolicy{"peer-B": wireRelayPolicy()}
	envelope := mgmtgrpc.EncodeNetworkMapEnvelope(mgmtgrpc.ComponentsEnvelopeInput{Components: components})
	remoteKey := components.Peers["peer-B"].Key
	envelope.GetFull().PairRelayPolicies[remoteKey].State = proto.RelayPolicyState(999)
	_, err := nbnetworkmap.EnvelopeToNetworkMap(context.Background(), envelope, localKey, "netbird.cloud", false)
	assert.ErrorIs(t, err, nbnetworkmap.ErrInvalidRelayPolicy)
}

func TestCatalogAliasPublicationAndWireLimitsAgree(t *testing.T) {
	policy := wireRelayPolicy()
	for i := range rp.MaxRelayAliases {
		policy.Tiers[0][0].Aliases = append(policy.Tiers[0][0].Aliases, rp.RelayEndpoint(fmt.Sprintf("rels://alias%d.example:443", i)))
	}
	_, err := rp.ValidateCatalog(rp.RelayCatalog{Entries: []rp.RelayCatalogEntry{policy.Tiers[0][0]}})
	require.NoError(t, err)
	decoded, err := nbnetworkmap.FromProtocolRelayPolicy(nbnetworkmap.ToProtocolRelayPolicy(policy))
	require.NoError(t, err)
	assert.Len(t, decoded.Tiers[0][0].Aliases, rp.MaxRelayAliases, "largest publishable alias set must decode intact")
	policy.Tiers[0][0].Aliases = append(policy.Tiers[0][0].Aliases, "rels://overflow.example:443")
	_, err = rp.ValidateCatalog(rp.RelayCatalog{Entries: []rp.RelayCatalogEntry{policy.Tiers[0][0]}})
	require.Error(t, err)
	_, err = nbnetworkmap.FromProtocolRelayPolicy(nbnetworkmap.ToProtocolRelayPolicy(policy))
	assert.Error(t, err)
}
