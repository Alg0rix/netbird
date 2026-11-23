package peer

import (
	"sync/atomic"

	"github.com/netbirdio/netbird/client/internal/peer/conntype"
	"github.com/netbirdio/netbird/client/internal/peer/worker"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// connPolicy is embedded in Conn.
type connPolicy struct {
	policyRevision atomic.Uint64
}

// RelayPolicy returns a detached snapshot of the effective pair instructions.
func (conn *Conn) RelayPolicy() *rp.ResolvedRelayPolicy {
	p := conn.config.RelayPolicy.Copy()
	if p != nil {
		if revision := conn.policyRevision.Load(); revision != 0 {
			p.Revision = revision
		}
	}
	return p
}

// UpdateRelayPolicyRevision refreshes reporting metadata without changing transport.
func (conn *Conn) UpdateRelayPolicyRevision(revision uint64) {
	conn.policyRevision.Store(revision)
}

// RelayPolicySelection returns the acknowledged native relay and attempt sequence.
func (conn *Conn) RelayPolicySelection() (rp.RelayID, uint64) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	relayWorker := conn.workerRelay
	if relayWorker == nil || relayWorker.managed == nil || conn.currentConnPriority != conntype.Relay || conn.statusRelay.Get() != worker.StatusConnected {
		return "", 0
	}
	m := relayWorker.managed
	m.mu.Lock()
	defer m.mu.Unlock()
	relayWorker.relayLock.Lock()
	selectedConn := relayWorker.relayedConn
	relayWorker.relayLock.Unlock()
	if m.stopped || conn.relayedConnRef == nil || selectedConn != conn.relayedConnRef || conn.relayedConnRef.Context() == nil || conn.relayedConnRef.Context().Err() != nil {
		return "", 0
	}
	return m.selected, m.selectedSequence
}
