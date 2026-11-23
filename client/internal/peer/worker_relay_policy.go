package peer

import (
	relayClient "github.com/netbirdio/netbird/shared/relay/client"
	sproto "github.com/netbirdio/netbird/shared/signal/proto"
)

func (w *WorkerRelay) policyAnnouncement() *sproto.RelayPolicyAttempt {
	if w.managed == nil {
		return nil
	}
	return w.managed.announcement()
}

func (w *WorkerRelay) acceptPolicy(offer *OfferAnswer) bool {
	if offer == nil {
		return false
	}
	if w == nil || w.managed == nil {
		return offer.RelayPolicyAttempt == nil
	}
	return w.managed.accept(offer.RelayPolicyAttempt)
}

func (w *WorkerRelay) isCurrentConnection(conn *relayClient.Conn) bool {
	if w.managed == nil {
		return true
	}
	m := w.managed
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped || m.current == nil || m.selectedSequence != m.current.Sequence || string(m.selected) != m.current.RelayId {
		return false
	}
	w.relayLock.Lock()
	defer w.relayLock.Unlock()
	return w.relayedConn == conn
}
