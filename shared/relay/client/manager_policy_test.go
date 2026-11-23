package client

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/client/iface"
)

// Tests of the relay policy mode of the Manager. They reuse the helpers of manager_lifecycle_test.go.

func servePolicyManager(t *testing.T, ctx context.Context, relayURLs []string, peerID string, opts ...ManagerOption) *Manager {
	t.Helper()

	m := NewManager(ctx, relayURLs, peerID, iface.DefaultMTU, append(opts, WithRelayPolicy())...)
	require.NoError(t, m.Serve(), "serve manager of %s", peerID)
	return m
}

func TestPolicyServerPickerUnavailable(t *testing.T) {
	sp := ServerPicker{
		TokenStore:        nil,
		PeerID:            "test",
		ConnectionTimeout: 500 * time.Millisecond,
	}
	// Refused local ports; the picker must give up inside its own timeout.
	sp.ServerURLs.Store([]string{"rel://127.0.0.1:1", "rel://127.0.0.1:2"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := sp.PickServer(ctx)
	require.Error(t, err, "unavailable servers must return a connection failure")
	require.NoError(t, ctx.Err(), "the picker must finish within its own timeout before the caller deadline")
}

func TestPolicyHomeRecoversAfterSessionDrop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	relay := startRelayServer(t)
	bob := serveManager(t, ctx, relay, "bob")
	alice := servePolicyManager(t, ctx, toURL(relay), "alice", WithNetEvents(newStubNetEvents()))

	first := homeSession(t, alice)
	dropSession(t, first)

	require.NoError(t, waitForReady(ctx, alice, lifecycleTestTimeout), "the manager must recover a dropped home session")
	nextHomeSession(t, alice, first)

	relayAddr, _, err := alice.RelayInstanceAddress()
	require.NoError(t, err, "home relay address after the recovery")
	assertRelayedTraffic(t, ctx, alice, bob, relayAddr)
}

func TestPolicyHomeRecoversOnNewServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	oldRelay := startRelayServer(t)
	newRelay := startRelayServer(t)
	bob := serveManager(t, ctx, newRelay, "bob")
	alice := servePolicyManager(t, ctx, toURL(oldRelay), "alice", WithNetEvents(newStubNetEvents()))

	first := homeSession(t, alice)
	// The old server is no longer a valid home, so the manager has to pick again.
	alice.UpdateServerURLs(toURL(newRelay))
	dropSession(t, first)

	require.NoError(t, waitForReady(ctx, alice, lifecycleTestTimeout), "the manager must recover on a newly picked server")
	recovered := nextHomeSession(t, alice, first)
	assert.Equal(t, toURL(newRelay)[0], recovered.client.connectionURL, "the recovered home must be the new server")

	relayAddr, _, err := alice.RelayInstanceAddress()
	require.NoError(t, err, "home relay address after the recovery")
	assertRelayedTraffic(t, ctx, alice, bob, relayAddr)
}

func TestPolicyForeignSessionEndRemovesTrack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	bob := serveManager(t, ctx, startRelayServer(t), "bob")
	alice := NewManager(ctx, toURL(startRelayServer(t)), "alice", iface.DefaultMTU, WithRelayPolicy())
	// Keep the idle cleanup out of the way: the track must go on its own.
	alice.cleanupInterval = time.Hour
	require.NoError(t, alice.Serve(), "serve manager of alice")

	bobsRelay, _, err := bob.RelayInstanceAddress()
	require.NoError(t, err, "bob's relay address")

	_, err = alice.OpenConn(ctx, bobsRelay, "bob", netip.Addr{})
	require.NoError(t, err, "alice must reach bob over bob's relay")
	dropSession(t, trackSession(t, foreignTrack(t, alice, bobsRelay)))

	require.Eventually(t, func() bool {
		alice.relayClientsMutex.RLock()
		defer alice.relayClientsMutex.RUnlock()
		_, tracked := alice.relayClients[bobsRelay]
		return !tracked
	}, lifecycleTestTimeout, 5*time.Millisecond, "the track of an ended foreign session must be removed without the idle cleanup")

	assertRelayedTraffic(t, ctx, alice, bob, bobsRelay)
	assert.True(t, trackSession(t, foreignTrack(t, alice, bobsRelay)).client.Ready(), "the next OpenConn must run on a fresh foreign connection")
}

func TestPolicyOpenConnReplacesEndedForeignTrack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	bob := serveManager(t, ctx, startRelayServer(t), "bob")
	alice := NewManager(ctx, toURL(startRelayServer(t)), "alice", iface.DefaultMTU, WithRelayPolicy())
	alice.cleanupInterval = time.Hour
	require.NoError(t, alice.Serve(), "serve manager of alice")

	bobsRelay, _, err := bob.RelayInstanceAddress()
	require.NoError(t, err, "bob's relay address")

	foreign := NewClient(bobsRelay, alice.tokenStore, alice.peerID, alice.mtu)
	s, err := foreign.ConnectSession(ctx)
	require.NoError(t, err, "connect to the foreign relay")
	dropSession(t, s)

	// Published without its watcher, the way OpenConn finds a track whose
	// session ended but whose eviction has not run yet.
	stale := NewRelayTrack()
	stale.relayClient = foreign
	close(stale.ready)
	alice.relayClientsMutex.Lock()
	alice.relayClients[bobsRelay] = stale
	alice.relayClientsMutex.Unlock()

	_, err = alice.OpenConn(ctx, bobsRelay, "bob", netip.Addr{})
	require.NoError(t, err, "OpenConn must redial instead of failing on the ended track")

	fresh := foreignTrack(t, alice, bobsRelay)
	assert.NotSame(t, stale, fresh, "the ended track must be replaced")
	assert.True(t, trackSession(t, fresh).client.Ready(), "the replacement track must hold a live session")
}
