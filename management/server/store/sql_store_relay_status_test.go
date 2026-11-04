package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func observationReport(remote rp.PeerID, sequence uint64) rp.PathReport {
	return rp.PathReport{RemotePeerID: remote, Revision: 1, Digest: "digest", CatalogGeneration: 1, Sequence: sequence, Attempt: 1,
		Transport: rp.TransportNativeRelay, RelayID: "a", State: rp.StateApplied}
}

func connectObservationPeer(t *testing.T, s *SqlStore, local, instance string, session int64) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.RegisterInstance(ctx, instance, "test"))
	connected, err := s.ConnectPeer(ctx, "relay-account", local, instance, session)
	require.NoError(t, err)
	require.True(t, connected, "report fixture must own the current sync session")
}

func TestRelayObservationSQLSharingOwnershipAndSequenceFences(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	connectObservationPeer(t, s, "x", "instance-x", 100)
	connectObservationPeer(t, s, "y", "instance-y", 200)
	report := observationReport("y", 1)
	require.NoError(t, s.PutRelayObservations(ctx, "relay-account", "x", 100, []rp.PathReport{report}))
	require.NoError(t, s.PutRelayObservations(ctx, "relay-account", "y", 200, []rp.PathReport{observationReport("x", 1)}))
	reader := &SqlStore{conn: s.conn, db: s.db}
	observations, err := reader.GetRelayObservations(ctx, "relay-account", "y", "x")
	require.NoError(t, err)
	require.Len(t, observations, 2)
	assert.True(t, observations[0].Current, "a third management receiver can read reports owned by another instance")
	assert.True(t, observations[1].Current, "both separately owned endpoints are aggregated from SQL")
	require.NoError(t, s.PutRelayObservations(ctx, "relay-account", "x", 100, []rp.PathReport{report}))
	again, err := s.GetRelayObservations(ctx, "relay-account", "x", "y")
	require.NoError(t, err)
	assert.Equal(t, observations[0].ObservedAt, again[0].ObservedAt, "duplicate delivery must not extend observation lifetime")
	changed := report
	changed.RelayID = "b"
	assert.Error(t, s.PutRelayObservations(ctx, "relay-account", "x", 100, []rp.PathReport{changed}), "same sequence cannot rewrite observation contents")
	require.NoError(t, s.RegisterInstance(ctx, "takeover-instance", "test"))
	connected, err := s.ConnectPeer(ctx, "relay-account", "x", "takeover-instance", 101)
	require.NoError(t, err)
	require.True(t, connected, "new sync session replaces old report owner")
	assert.Error(t, s.PutRelayObservations(ctx, "relay-account", "x", 100, []rp.PathReport{report}), "old session cannot refresh observations after reconnect")
	current, err := s.GetRelayObservations(ctx, "relay-account", "x", "y")
	require.NoError(t, err)
	assert.False(t, current[0].Current, "previous owner's report becomes stale immediately")
	require.NoError(t, s.PutRelayObservations(ctx, "relay-account", "x", 101, []rp.PathReport{report}))
	_, err = s.DisconnectPeer(ctx, "relay-account", "x", 101)
	require.NoError(t, err)
	current, err = s.GetRelayObservations(ctx, "relay-account", "x", "y")
	require.NoError(t, err)
	assert.False(t, current[0].Current, "disconnect invalidates telemetry without modifying liveness to manufacture health")
}

func TestRelayObservationExpiryPreservesWatermarkAndPrunesEndedSessions(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	connectObservationPeer(t, s, "x", "instance-x", 100)
	require.NoError(t, s.PutRelayObservations(ctx, "relay-account", "x", 100, []rp.PathReport{observationReport("y", 10)}))
	require.NoError(t, s.db.Model(&RelayObservation{}).Where("local_peer_id = ?", "x").Update("expires_at", time.Now().Add(-time.Hour)).Error)
	require.NoError(t, s.PruneRelayObservations(ctx, 256))
	observations, err := s.GetRelayObservations(ctx, "relay-account", "x", "y")
	require.NoError(t, err)
	require.Len(t, observations, 1)
	assert.False(t, observations[0].Current, "expiry must invalidate status while retaining the current-session sequence watermark")
	assert.Error(t, s.PutRelayObservations(ctx, "relay-account", "x", 100, []rp.PathReport{observationReport("y", 1)}), "expired reports cannot be replayed as fresh observations")
	_, err = s.DisconnectPeer(ctx, "relay-account", "x", 100)
	require.NoError(t, err)
	require.NoError(t, s.PruneRelayObservations(ctx, 256))
	observations, err = s.GetRelayObservations(ctx, "relay-account", "x", "y")
	require.NoError(t, err)
	assert.Empty(t, observations, "ended-session expired telemetry is physically pruned")
}

func TestRelayObservationStaleInstanceAndPeerCleanup(t *testing.T) {
	s, _ := newRelayStore(t)
	ctx := context.Background()
	connectObservationPeer(t, s, "x", "instance-x", 100)
	require.NoError(t, s.PutRelayObservations(ctx, "relay-account", "x", 100, []rp.PathReport{observationReport("y", 1)}))
	require.NoError(t, s.db.Model(&nbpeer.ManagementInstance{}).Where("id = ?", "instance-x").Update("last_seen", time.Now().Add(-2*nbpeer.InstanceStaleAfter)).Error)
	assert.Error(t, s.PutRelayObservations(ctx, "relay-account", "x", 100, []rp.PathReport{observationReport("y", 2)}), "stale process ownership cannot refresh a report")
	observations, err := s.GetRelayObservations(ctx, "relay-account", "x", "y")
	require.NoError(t, err)
	assert.False(t, observations[0].Current, "stale management heartbeat invalidates reported health")
	require.NoError(t, s.DeletePeer(ctx, "relay-account", "y"))
	observations, err = s.GetRelayObservations(ctx, "relay-account", "x", "y")
	require.NoError(t, err)
	assert.Empty(t, observations, "deleting either endpoint removes account-owned pair observations")
}
