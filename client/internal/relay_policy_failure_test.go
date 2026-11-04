package internal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/client/internal/peer"
	"github.com/netbirdio/netbird/client/internal/peerstore"
	nbnetworkmap "github.com/netbirdio/netbird/shared/management/networkmap"
	mgmProto "github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	relayclient "github.com/netbirdio/netbird/shared/relay/client"
)

func TestMalformedComponentPolicyRevokesExistingManagedPaths(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		incomingManaged, home, overflow bool
	}{
		{name: "existing managed policy"},
		{name: "new restriction on direct peer", incomingManaged: true},
		{name: "malformed home component", home: true},
		{name: "oversized relay aliases", overflow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			existingPath, remotePath := net.Pipe()
			t.Cleanup(func() { assert.NoError(t, existingPath.Close()); assert.NoError(t, remotePath.Close()) })
			rawKey := make([]byte, 32)
			rawKey[0] = 1
			key := base64.StdEncoding.EncodeToString(rawKey)
			status := peer.NewRecorder("management.invalid")
			connections := peerstore.NewConnStore()
			var policy *rp.ResolvedRelayPolicy
			if !tc.incomingManaged {
				policy = &rp.ResolvedRelayPolicy{State: rp.RelayPolicyReady, Digest: "previous"}
			}
			connection, err := peer.NewConn(peer.ConnConfig{Key: key, RelayPolicy: policy, WgConfig: peer.WgConfig{AllowedIps: []netip.Prefix{netip.MustParsePrefix("100.64.0.2/32")}}}, peer.ServiceDependencies{StatusRecorder: status})
			require.NoError(t, err)
			require.True(t, connections.AddPeerConn(key, connection), "existing path must be registered")
			unrelated, err := peer.NewConn(peer.ConnConfig{Key: "unmanaged", WgConfig: peer.WgConfig{AllowedIps: []netip.Prefix{netip.MustParsePrefix("100.64.0.3/32")}}}, peer.ServiceDependencies{StatusRecorder: status})
			require.NoError(t, err)
			require.True(t, connections.AddPeerConn("unmanaged", unrelated), "unmanaged peer must be registered")
			hardware := &MockWGIface{RemovePeerFunc: func(removed string) error {
				require.Equal(t, key, removed, "only managed or newly restricted peers may lose their interface path")
				return existingPath.Close()
			}}
			engine := &Engine{ctx: ctx, config: &EngineConfig{}, peerStore: connections, statusRecorder: status, relayManager: relayclient.NewManager(ctx, nil, "local", 1280)}
			engine.connMgr = NewConnMgr(engine.config, status, connections, hardware)
			envelope := &mgmProto.NetworkMapEnvelope{Payload: &mgmProto.NetworkMapEnvelope_Full{Full: &mgmProto.NetworkMapComponentsFull{
				Peers:             []*mgmProto.PeerCompact{{WgPubKey: rawKey}},
				PairRelayPolicies: map[string]*mgmProto.RelayPolicy{key: {State: mgmProto.RelayPolicyState(999)}},
			}}}
			if tc.home {
				envelope.GetFull().HomeRelayPolicy = envelope.GetFull().PairRelayPolicies[key]
				envelope.GetFull().PairRelayPolicies = nil
			}
			if tc.overflow {
				malformed := nbnetworkmap.ToProtocolRelayPolicy(&rp.ResolvedRelayPolicy{State: rp.RelayPolicyReady, Revision: 1, CatalogGeneration: 1, Digest: rp.Digest(strings.Repeat("a", 64)), Tiers: [][]rp.RelayCatalogEntry{{{ID: "relay", Endpoint: "rel://relay.example:443", InstanceEndpoint: "rel://relay.example:443", Active: true}}}})
				for i := range rp.MaxRelayAliases + 1 {
					malformed.Tiers[0].Relays[0].Aliases = append(malformed.Tiers[0].Relays[0].Aliases, fmt.Sprintf("rel://alias%d.example:443", i))
				}
				envelope.GetFull().PairRelayPolicies[key] = malformed
			}
			_, genericErr := engine.decodeComponentNetworkMap(&mgmProto.NetworkMapEnvelope{}, "local", "netbird.cloud")
			require.Error(t, genericErr)
			assert.NotErrorIs(t, genericErr, nbnetworkmap.ErrInvalidRelayPolicy, "generic decode failures must not be labeled transport policy failures")
			_, stillRegistered := connections.PeerConn(key)
			require.True(t, stillRegistered, "unrelated envelope failures must retain the previous managed peer")
			require.NoError(t, remotePath.SetReadDeadline(time.Now().Add(time.Second)))
			_, err = engine.decodeComponentNetworkMap(envelope, "local", "netbird.cloud")
			require.ErrorIs(t, err, nbnetworkmap.ErrInvalidRelayPolicy)
			_, readErr := remotePath.Read(make([]byte, 1))
			assert.True(t, errors.Is(readErr, io.EOF), "forbidden existing interface path must close before sync rejection: %v", readErr)
			_, found := connections.PeerConn(key)
			assert.False(t, found, "managed path must be removed from the real peer store")
			_, found = connections.PeerConn("unmanaged")
			assert.True(t, found, "unrelated unmanaged peer remains usable")
			home, _, _ := engine.relayManager.HomePolicyStatus()
			require.NotNil(t, home)
			assert.Equal(t, rp.RelayPolicyBlocked, home.State, "malformed component must revoke home registration instructions")
			assert.Equal(t, rp.RelayPolicyBlocked, engine.relayPolicyDesired[rp.PeerID(key)].State, "desired state must not continue reporting stale policy applied")
		})
	}
}
