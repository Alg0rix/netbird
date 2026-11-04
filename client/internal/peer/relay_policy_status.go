package peer

import (
	"github.com/netbirdio/netbird/client/internal/peer/conntype"
	"github.com/netbirdio/netbird/client/internal/peer/worker"
	"github.com/netbirdio/netbird/client/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// RelayPolicyObservation contains credential-free desired and observed path diagnostics.
type RelayPolicyObservation struct {
	RemotePeerKey         rp.PeerID
	DesiredRevision       uint64
	AppliedRevision       uint64
	Digest                rp.Digest
	CatalogGeneration     uint64
	ReselectionGeneration uint64
	Attempt               uint64
	Transport             rp.PathTransport
	RelayID               rp.RelayID
	State                 rp.PathState
	Reason                rp.FailureReason
}

// Proto returns a detached daemon status representation.
func (o RelayPolicyObservation) Proto() *proto.RelayPolicyStatus {
	return &proto.RelayPolicyStatus{RemotePeerKey: string(o.RemotePeerKey), DesiredRevision: o.DesiredRevision, AppliedRevision: o.AppliedRevision, Digest: string(o.Digest), CatalogGeneration: o.CatalogGeneration, ReselectionGeneration: o.ReselectionGeneration, Attempt: o.Attempt, Transport: string(o.Transport), RelayId: string(o.RelayID), State: string(o.State), Reason: string(o.Reason)}
}

// NewRelayPolicyObservation describes intent before a transport has been observed.
func NewRelayPolicyObservation(remoteKey string, p *rp.ResolvedRelayPolicy) RelayPolicyObservation {
	o := RelayPolicyObservation{RemotePeerKey: rp.PeerID(remoteKey), DesiredRevision: p.Revision, Digest: p.Digest, CatalogGeneration: p.CatalogGeneration, ReselectionGeneration: p.ReselectionGeneration, Transport: rp.TransportUnknown, State: rp.StatePending, Reason: rp.ReasonConnecting}
	switch {
	case p.Digest == "":
		o.State = rp.StateBlocked
		o.Reason = rp.ReasonInvalidPolicy
	case p.State == rp.RelayPolicyUnsupported:
		o.State = rp.StateUnsupported
		o.Reason = rp.ReasonUnsupported
	case p.State == rp.RelayPolicyBlocked && p.ForceRelay:
		o.State = rp.StateBlocked
		o.Reason = rp.ReasonNoPermittedRelay
		o.AppliedRevision = p.Revision
	case p.State == rp.RelayPolicyBlocked:
		o.Reason = rp.ReasonNoPermittedRelay
	}
	return o
}

// RelayPolicyObservation snapshots the installed instructions and actual active path.
func (conn *Conn) RelayPolicyObservation() RelayPolicyObservation {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	p := conn.RelayPolicy()
	if p == nil {
		return RelayPolicyObservation{}
	}
	o := NewRelayPolicyObservation(conn.config.Key, p)
	o.AppliedRevision = p.Revision
	if !conn.opened {
		o.Reason = rp.ReasonLazy
		return o
	}
	if conn.workerRelay != nil && conn.workerRelay.managed != nil {
		m := conn.workerRelay.managed
		m.mu.Lock()
		if m.lastReason != rp.ReasonNone {
			o.Reason = m.lastReason
		}
		if m.exhausted && (p.ForceRelay || IsForceRelayed()) {
			o.State = rp.StateBlocked
		}
		if m.current != nil {
			o.Attempt = m.current.Sequence
		}
		conn.workerRelay.relayLock.Lock()
		selectedConn := conn.workerRelay.relayedConn
		conn.workerRelay.relayLock.Unlock()
		installed := conn.relayedConnRef != nil && selectedConn == conn.relayedConnRef && conn.relayedConnRef.Context() != nil && conn.relayedConnRef.Context().Err() == nil
		if installed && conn.currentConnPriority == conntype.Relay && conn.statusRelay.Get() == worker.StatusConnected && !m.stopped && m.selected != "" {
			o.Attempt = m.selectedSequence
			o.Transport = rp.TransportNativeRelay
			o.RelayID = m.selected
			o.State = rp.StateApplied
			o.Reason = rp.ReasonNone
			if relayTier(p, m.selected) > 0 {
				o.Reason = rp.ReasonHigherPriorityUnavailable
			}
		}
		m.mu.Unlock()
	}
	if conn.currentConnPriority == conntype.ICEP2P && conn.statusICE.Get() == worker.StatusConnected {
		o.Transport = rp.TransportDirect
		o.State = rp.StateApplied
		o.Reason = rp.ReasonNone
	}
	return o
}

func relayTier(p *rp.ResolvedRelayPolicy, id rp.RelayID) int {
	for i, tier := range p.Tiers {
		for _, entry := range tier {
			if entry.ID == id {
				return i
			}
		}
	}
	return -1
}
