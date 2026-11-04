package peer

import (
	"testing"

	"github.com/netbirdio/netbird/client/internal/peer/conntype"
	"github.com/netbirdio/netbird/client/internal/peer/worker"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/stretchr/testify/assert"
)

func observationTestConn() *Conn {
	return &Conn{config: ConnConfig{Key: "remote", RelayPolicy: &rp.ResolvedRelayPolicy{State: rp.RelayPolicyReady, Revision: 7, Digest: "d", CatalogGeneration: 2, Tiers: [][]rp.RelayCatalogEntry{{{ID: "a"}}, {{ID: "b"}}}}}, statusICE: worker.NewAtomicStatus(), statusRelay: worker.NewAtomicStatus()}
}

func TestRelayObservationIdleIsPending(t *testing.T) {
	conn := observationTestConn()
	o := conn.RelayPolicyObservation()
	assert.Equal(t, rp.StatePending, o.State, "installed instructions without a path must remain pending")
	assert.Equal(t, rp.TransportUnknown, o.Transport, "a lazy peer has no observed transport")
	assert.Equal(t, uint64(7), o.AppliedRevision, "idle instructions still record the applied revision")
	assert.Equal(t, rp.ReasonLazy, o.Reason, "idle state must explain why transport is unknown")
}

func TestRelayObservationUsesActualTransport(t *testing.T) {
	conn := observationTestConn()
	conn.opened = true
	managed := &managedRelay{selected: "b", selectedSequence: 2}
	conn.workerRelay = &WorkerRelay{managed: managed}
	registered := conn.RelayPolicyObservation()
	assert.Equal(t, rp.StatePending, registered.State, "registration alone must not produce applied transport")
	assert.Empty(t, registered.RelayID, "an uninstalled selection must not be reported as actual")
	conn.statusICE.SetConnected()
	conn.currentConnPriority = conntype.ICEP2P
	direct := conn.RelayPolicyObservation()
	assert.Equal(t, rp.TransportDirect, direct.Transport, "an active direct path must remain direct despite a relay selection")
	assert.Equal(t, rp.StateApplied, direct.State, "active direct transport applies a direct-capable policy")
	conn.statusRelay.SetConnected()
	conn.currentConnPriority = conntype.Relay
	native := conn.RelayPolicyObservation()
	assert.Equal(t, rp.StatePending, native.State, "connected status without the matching installed channel must not imply application")
	assert.Empty(t, native.RelayID, "a stale native status must not report a tentative approved identity")
}

func TestRelayObservationExhaustionIsBlockedOnlyWhenForced(t *testing.T) {
	conn := observationTestConn()
	conn.opened = true
	conn.workerRelay = &WorkerRelay{managed: &managedRelay{exhausted: true, lastReason: rp.ReasonPairTimeout}}
	o := conn.RelayPolicyObservation()
	assert.Equal(t, rp.StatePending, o.State, "direct-capable policy can still establish direct after relay exhaustion")
	conn.config.RelayPolicy.ForceRelay = true
	o = conn.RelayPolicyObservation()
	assert.Equal(t, rp.StateBlocked, o.State, "force-relay exhaustion must be visible as blocked")
	assert.Equal(t, rp.ReasonPairTimeout, o.Reason, "failure cause must survive into diagnostics")
}

func TestRelayObservationRevisionMetadataDoesNotMutateInstructions(t *testing.T) {
	conn := observationTestConn()
	conn.UpdateRelayPolicyRevision(8)
	o := conn.RelayPolicyObservation()
	assert.Equal(t, uint64(8), o.DesiredRevision, "revision-only changes must refresh diagnostics")
	assert.Equal(t, rp.RelayPolicyDigest("d"), o.Digest, "revision-only changes must preserve transport identity")
	assert.Equal(t, uint64(7), conn.config.RelayPolicy.Revision, "immutable configured snapshot must stay detached")
}
