package peer

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"

	log "github.com/sirupsen/logrus"

	relayClient "github.com/netbirdio/netbird/shared/relay/client"
	sproto "github.com/netbirdio/netbird/shared/signal/proto"
)

type RelayConnInfo struct {
	relayedConn     *relayClient.Conn
	rosenpassPubKey []byte
	rosenpassAddr   string
}

type WorkerRelay struct {
	peerCtx      context.Context
	log          *log.Entry
	isController bool
	config       ConnConfig
	conn         *Conn
	relayManager *relayClient.Manager

	relayedConn *relayClient.Conn
	relayLock   sync.Mutex

	relaySupportedOnRemotePeer atomic.Bool
	managed                    *managedRelay
}

func NewWorkerRelay(ctx context.Context, log *log.Entry, ctrl bool, config ConnConfig, conn *Conn, relayManager *relayClient.Manager) *WorkerRelay {
	r := &WorkerRelay{
		peerCtx:      ctx,
		log:          log,
		isController: ctrl,
		config:       config,
		conn:         conn,
		relayManager: relayManager,
	}
	if config.RelayPolicy != nil {
		r.managed = newManagedRelay(r)
	}
	return r
}

func (w *WorkerRelay) OnNewOffer(remoteOfferAnswer *OfferAnswer) {
	if w.managed != nil {
		w.relaySupportedOnRemotePeer.Store(w.managed.accept(remoteOfferAnswer.RelayPolicyAttempt))
		w.managed.receive(remoteOfferAnswer)
		return
	}
	if !w.isRelaySupported(remoteOfferAnswer) {
		w.log.Infof("Relay is not supported by remote peer")
		w.relaySupportedOnRemotePeer.Store(false)
		return
	}
	w.relaySupportedOnRemotePeer.Store(true)

	// the relayManager will return with error in case if the connection has lost with relay server
	currentRelayAddress, _, err := w.relayManager.RelayInstanceAddress()
	if err != nil {
		w.log.Errorf("failed to handle new offer: %s", err)
		return
	}

	srv := w.preferredRelayServer(currentRelayAddress, remoteOfferAnswer.RelaySrvAddress)
	var serverIP netip.Addr
	if srv == remoteOfferAnswer.RelaySrvAddress {
		serverIP = remoteOfferAnswer.RelaySrvIP
	}

	relayedConn, err := w.relayManager.OpenConn(w.peerCtx, srv, w.config.Key, serverIP)
	if err != nil {
		if errors.Is(err, relayClient.ErrConnAlreadyExists) {
			w.log.Debugf("handled offer by reusing existing relay connection")
			return
		}
		w.log.Errorf("failed to open connection via Relay: %s", err)
		return
	}

	w.relayLock.Lock()
	w.relayedConn = relayedConn
	w.relayLock.Unlock()

	go w.watchRelayedConn(relayedConn)

	w.log.Debugf("peer conn opened via Relay: %s", srv)
	go w.conn.onRelayConnectionIsReady(RelayConnInfo{
		relayedConn:     relayedConn,
		rosenpassPubKey: remoteOfferAnswer.RosenpassPubKey,
		rosenpassAddr:   remoteOfferAnswer.RosenpassAddr,
	})
}

func (w *WorkerRelay) RelayInstanceAddress() (string, netip.Addr, error) {
	return w.relayManager.RelayInstanceAddress()
}

func (w *WorkerRelay) IsRelayConnectionSupportedWithPeer() bool {
	return w.relaySupportedOnRemotePeer.Load() && w.RelayIsSupportedLocally()
}

func (w *WorkerRelay) RelayIsSupportedLocally() bool {
	if w.managed != nil {
		return w.managed.readyPolicy()
	}
	return w.relayManager.HasRelayAddress()
}

func (w *WorkerRelay) IsTransportConnected() bool {
	if w.managed != nil {
		return w.managed.readyPolicy()
	}
	return w.relayManager.Ready()
}

func (w *WorkerRelay) CloseConn() {
	if w.managed != nil {
		w.managed.close()
	}
	w.relayLock.Lock()
	conn := w.relayedConn
	w.relayedConn = nil
	w.relayLock.Unlock()

	if conn == nil {
		return
	}

	if err := conn.Close(); err != nil {
		w.log.Warnf("failed to close relay connection: %v", err)
	}
}

func (w *WorkerRelay) isRelaySupported(answer *OfferAnswer) bool {
	if !w.relayManager.HasRelayAddress() {
		return false
	}
	return answer.RelaySrvAddress != ""
}

func (w *WorkerRelay) preferredRelayServer(myRelayAddress, remoteRelayAddress string) string {
	if w.isController {
		return myRelayAddress
	}
	return remoteRelayAddress
}

func (w *WorkerRelay) watchRelayedConn(relayedConn *relayClient.Conn) {
	<-relayedConn.Context().Done()

	w.conn.onRelayDisconnected(relayedConn)
}

func (w *WorkerRelay) policyAnnouncement() *sproto.RelayPolicyAttempt {
	if w.managed == nil {
		return nil
	}
	return w.managed.announcement()
}

func (w *WorkerRelay) acceptPolicy(offer *OfferAnswer) bool {
	if w.managed == nil {
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
