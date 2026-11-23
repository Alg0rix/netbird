package client

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/relay/server"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func registrationTestRelay(t *testing.T) rp.RelayCatalogEntry {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	srv, err := server.NewServer(newManagerTestServerConfig(address))
	require.NoError(t, err)
	errors := make(chan error, 1)
	go func() {
		if err := srv.Listen(server.ListenerConfig{Address: address}); err != nil {
			errors <- err
		}
	}()
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })
	require.NoError(t, waitForServerToStart(errors))
	endpoint := rp.RelayEndpoint("rel://" + address)
	return rp.RelayCatalogEntry{ID: "integration-relay", Endpoint: endpoint, InstanceEndpoint: endpoint, Active: true}
}

func TestRegistrationSharedAcrossHomeAndPairOwners(t *testing.T) {
	entry := registrationTestRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	alice := NewManager(ctx, []string{string(entry.Endpoint)}, "registration-alice", 1280, WithRelayPolicy())
	require.NoError(t, alice.SetCatalogGeneration(1, []rp.RelayCatalogEntry{entry}))
	require.NoError(t, alice.Serve())
	bob := NewManager(ctx, nil, "registration-bob", 1280, WithRelayPolicy())
	bobRegistration, err := bob.AcquireRegistration(ctx, entry)
	require.NoError(t, err)
	t.Cleanup(bobRegistration.Release)
	const owners = 12
	registrations := make([]*Registration, owners)
	failures := make([]error, owners)
	var group sync.WaitGroup
	for i := range registrations {
		group.Add(1)
		go func(i int) { defer group.Done(); registrations[i], failures[i] = alice.AcquireRegistration(ctx, entry) }(i)
	}
	group.Wait()
	for i, reg := range registrations {
		require.NoError(t, failures[i])
		t.Cleanup(reg.Release)
		assert.Same(t, alice.relayClient, reg.client, "owner %d must reuse the home registration", i)
	}
	outbound, err := registrations[0].OpenConn(ctx, "registration-bob")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, outbound.Close()) })
	inbound, err := bobRegistration.OpenConn(ctx, "registration-alice")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, inbound.Close()) })
	require.NoError(t, alice.SetHomePolicy(&rp.ResolvedRelayPolicy{State: rp.RelayPolicyBlocked}))
	assert.False(t, alice.Ready(), "blocked home policy removes only the home ownership")
	for _, reg := range registrations[1:] {
		reg.Release()
		reg.Release()
	}
	payload := []byte("pair remains usable while the home registration is revoked")
	_, err = outbound.Write(payload)
	require.NoError(t, err)
	bytes := make([]byte, len(payload))
	received, err := inbound.Read(bytes)
	require.NoError(t, err)
	assert.Equal(t, payload, bytes[:received], "active pair must survive releasing all other registration owners")
}

func TestRegistrationRejectsUnapprovedInstanceAndStaleGeneration(t *testing.T) {
	entry := registrationTestRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	manager := NewManager(ctx, nil, "registration-validation", 1280, WithRelayPolicy())
	mismatch := entry
	mismatch.InstanceEndpoint = "rel://unapproved.example:1234"
	_, err := manager.AcquireRegistration(ctx, mismatch)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unapproved instance", "handshake must match the approved concrete instance")
	require.NoError(t, manager.SetCatalogGeneration(2, []rp.RelayCatalogEntry{entry}))
	_, err = manager.AcquireRegistration(ctx, mismatch)
	assert.ErrorContains(t, err, "stale", "an old offer must not reintroduce an earlier mapping")
	err = manager.SetCatalogGeneration(1, []rp.RelayCatalogEntry{mismatch})
	assert.ErrorContains(t, err, "stale", "old catalog generations cannot replace current mappings")
	reg, err := manager.AcquireRegistration(ctx, entry)
	require.NoError(t, err)
	t.Cleanup(reg.Release)
	address, _, err := reg.InstanceAddress()
	require.NoError(t, err)
	assert.Equal(t, string(entry.InstanceEndpoint), address, "accepted handshake returns the approved concrete endpoint")
}

func TestRegistrationHomeMetadataUpdateRetainsHealthyFallback(t *testing.T) {
	entry := registrationTestRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	manager := NewManager(ctx, nil, "registration-home-update", 1280, WithRelayPolicy())
	policy := &rp.ResolvedRelayPolicy{State: rp.RelayPolicyReady, Digest: "same-instructions", CatalogGeneration: 1, Tiers: [][]rp.RelayCatalogEntry{{entry}}}
	require.NoError(t, manager.SetHomePolicy(policy))
	require.NoError(t, manager.Serve())
	previous := manager.relayClient
	policy = policy.Copy()
	policy.Revision++
	require.NoError(t, manager.SetHomePolicy(policy))
	assert.Same(t, previous, manager.relayClient, "an unrelated account revision must retain a healthy selected home")
	assert.True(t, manager.Ready(), "home remains connected after a metadata-only update")
	assert.Equal(t, policy.Revision, manager.homePolicy.Revision, "new metadata is retained")
}

func TestRegistrationPreservesDecodedEmptyAliases(t *testing.T) {
	entry := registrationTestRelay(t)
	// Protobuf decoding creates an allocated empty alias list.
	entry.Aliases = []rp.RelayEndpoint{}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	manager := NewManager(ctx, nil, "registration-decoded-aliases", 1280, WithRelayPolicy())
	require.NoError(t, manager.SetCatalogGeneration(1, []rp.RelayCatalogEntry{entry}))
	registration, err := manager.AcquireRegistration(ctx, entry)
	require.NoError(t, err, "a decoded approved selection must retain its identity through registration acquisition")
	t.Cleanup(registration.Release)
	address, _, err := registration.InstanceAddress()
	require.NoError(t, err)
	assert.Equal(t, string(entry.InstanceEndpoint), address, "the registration must reach the approved concrete relay")
}
