package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/management/server/types"
)

func TestRelayCatalogInitializationDoesNotRepublishReplicaConfig(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	replica, err := NewSqlStore(ctx, s.Conn(), nil, true)
	require.NoError(t, err)
	staleCandidate := types.RelayCatalog{Entries: []types.RelayCatalogEntry{{ID: "old", Endpoint: "rels://old.example:443", Active: true}}}
	current, err := replica.InitializeRelayCatalog(ctx, staleCandidate)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), current.Generation, "startup can initialize only a previously absent publication")
	assert.Equal(t, types.RelayID("a"), current.Entries[0].ID, "a replica's stale local file cannot replace the shared catalog")
	primary, err := s.GetRelayCatalog(ctx)
	require.NoError(t, err)
	assert.Equal(t, primary, current, "replicas resolve against the same active immutable snapshot")
}

func TestRelayCatalogPublicationPreviewCASAndRetirement(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	_, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	before, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	candidate := relayTestCatalog()
	candidate.Entries[0].Active = false
	impact, err := s.PreviewRelayCatalog(ctx, candidate, 1)
	require.NoError(t, err)
	assert.Equal(t, []types.AccountID{"relay-account"}, impact.AccountIDs, "preview lists configured accounts affected by the catalog universe")
	assert.Equal(t, []types.RelayID{"a"}, impact.Retired, "preview distinguishes explicit retirement")
	afterPreview, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, before, afterPreview, "preview must not advance publication, account revision, or serial")
	published, err := s.PublishRelayCatalog(ctx, candidate, 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), published.Generation, "successful CAS publishes exactly one new generation")
	after, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, before.NetworkSerial+1, after.NetworkSerial, "catalog generation and affected serial commit atomically")
	assert.Equal(t, before.Configuration, after.Configuration, "retirement preserves referenced identities and account intent")
	_, err = s.PublishRelayCatalog(ctx, relayTestCatalog(), 1)
	assert.ErrorIs(t, err, ErrRelayRevisionConflict)
	stillCurrent, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, after, stillCurrent, "stale publication changes neither catalog nor network serial")
	var publications int64
	require.NoError(t, s.db.Model(&relayCatalogPublication{}).Count(&publications).Error)
	assert.Equal(t, int64(2), publications, "published snapshots remain immutable history")
}

func TestRelayCatalogReferencedRemovalRollsBack(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	_, err := s.SaveRelayConfiguration(ctx, "relay-account", 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	before, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	candidate := relayTestCatalog()
	candidate.Entries = candidate.Entries[1:]
	_, err = s.PreviewRelayCatalog(ctx, candidate, 1)
	assert.Error(t, err)
	_, err = s.PublishRelayCatalog(ctx, candidate, 1)
	assert.Error(t, err)
	after, err := s.GetRelaySnapshot(ctx, "relay-account")
	require.NoError(t, err)
	assert.Equal(t, before, after, "referenced removal must roll back head generation and serial changes")
}
