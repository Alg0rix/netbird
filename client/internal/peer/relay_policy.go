package peer

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	relayClient "github.com/netbirdio/netbird/shared/relay/client"
	sproto "github.com/netbirdio/netbird/shared/signal/proto"
	"google.golang.org/protobuf/proto"
)

const relayAttemptTimeout = 10 * time.Second

type negotiationNonce string

type managedRelay struct {
	worker           *WorkerRelay
	selected         rp.RelayID
	selectedSequence uint64
	remoteSequence   uint64
	retryDelay       time.Duration
	lastReason       rp.FailureReason
	exhausted        bool
	remoteNonce      []byte
	retired          map[negotiationNonce]bool
	mu               sync.Mutex
	started          bool
	stopped          bool
	nonce            []byte
	sequence         uint64
	current          *sproto.RelayPolicyAttempt
	cancel           context.CancelFunc
	lease            *relayClient.Registration
	ack              chan *OfferAnswer
}

func newManagedRelay(w *WorkerRelay) *managedRelay {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		w.log.Errorf("create relay negotiation nonce: %v", err)
		nonce = nil
	}
	return &managedRelay{worker: w, nonce: nonce, ack: make(chan *OfferAnswer, 8), retired: make(map[negotiationNonce]bool)}
}

func (m *managedRelay) readyPolicy() bool {
	p := m.worker.config.RelayPolicy
	return p.State == rp.RelayPolicyReady && len(p.Tiers) != 0 && len(m.nonce) == 16
}

func (m *managedRelay) accept(a *sproto.RelayPolicyAttempt) bool {
	p := m.worker.config.RelayPolicy
	return (m.readyPolicy() || (p.State == rp.RelayPolicyBlocked && !p.ForceRelay && a != nil && a.Sequence == 0)) && a != nil && a.Digest == string(p.Digest) && a.CatalogGeneration == p.CatalogGeneration && a.ReselectionGeneration == p.ReselectionGeneration && a.Phase <= 3
}

func (m *managedRelay) announcement() *sproto.RelayPolicyAttempt {
	p := m.worker.config.RelayPolicy
	return &sproto.RelayPolicyAttempt{Digest: string(p.Digest), CatalogGeneration: p.CatalogGeneration, ReselectionGeneration: p.ReselectionGeneration, Nonce: append([]byte(nil), m.nonce...)}
}

func cloneAttempt(a *sproto.RelayPolicyAttempt) *sproto.RelayPolicyAttempt {
	return proto.Clone(a).(*sproto.RelayPolicyAttempt)
}

func sameAttempt(a, b *sproto.RelayPolicyAttempt) bool {
	return a != nil && b != nil && a.Digest == b.Digest && a.CatalogGeneration == b.CatalogGeneration && a.ReselectionGeneration == b.ReselectionGeneration && bytes.Equal(a.Nonce, b.Nonce) && bytes.Equal(a.ResponderNonce, b.ResponderNonce) && a.Sequence == b.Sequence && a.RelayId == b.RelayId && a.Instance == b.Instance
}

func (m *managedRelay) send(a *sproto.RelayPolicyAttempt) error {
	w := m.worker
	offer := OfferAnswer{WgListenPort: w.config.LocalWgPort, RosenpassPubKey: w.config.RosenpassConfig.PubKey, RosenpassAddr: w.config.RosenpassConfig.Addr, RelayPolicyAttempt: cloneAttempt(a)}
	return w.conn.signaler.SignalAnswer(offer, w.config.Key)
}

func (m *managedRelay) receive(remote *OfferAnswer) {
	a := remote.RelayPolicyAttempt
	if !m.accept(a) {
		return
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	if a.Sequence == 0 {
		if len(a.Nonce) != 16 || m.retired[negotiationNonce(a.Nonce)] {
			m.mu.Unlock()
			return
		}
		if !bytes.Equal(a.Nonce, m.remoteNonce) {
			if len(m.remoteNonce) != 0 {
				if m.cancel != nil {
					m.cancel()
				}
				if m.worker.isController && m.selected != "" {
					m.started = false
					m.selected = ""
					m.current = nil
				}
			}
			if len(m.retired) >= 64 {
				m.mu.Unlock()
				return
			}
			if len(m.remoteNonce) != 0 {
				m.retired[negotiationNonce(m.remoteNonce)] = true
			}
			m.remoteNonce = append([]byte(nil), a.Nonce...)
			m.remoteSequence = 0
		}
		if !m.worker.isController {
			m.mu.Unlock()
			return
		}
	}
	if m.worker.isController {
		if !m.started && len(m.remoteNonce) == 16 && m.readyPolicy() {
			m.started = true
			go m.control()
		}
		m.mu.Unlock()
		if a.Sequence != 0 && len(a.Nonce) == 16 {
			select {
			case m.ack <- remote:
			default:
			}
		}
		return
	}
	if len(a.Nonce) != 16 || a.Sequence == 0 || a.RelayId == "" || !bytes.Equal(a.ResponderNonce, m.nonce) || !bytes.Equal(a.Nonce, m.remoteNonce) {
		m.mu.Unlock()
		return
	}
	if a.Phase == 2 {
		valid := sameAttempt(m.current, a)
		m.mu.Unlock()
		if valid {
			select {
			case m.ack <- remote:
			default:
			}
		}
		return
	}
	if a.Phase != 0 {
		m.mu.Unlock()
		return
	}
	if a.Sequence < m.remoteSequence {
		m.mu.Unlock()
		return
	}
	if sameAttempt(m.current, a) {
		m.mu.Unlock()
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(m.worker.peerCtx)
	m.cancel = cancel
	m.current = cloneAttempt(a)
	m.remoteSequence = a.Sequence
	m.mu.Unlock()
	go m.respond(ctx, remote)
}

func (m *managedRelay) entry(a *sproto.RelayPolicyAttempt) (rp.RelayCatalogEntry, bool) {
	for _, tier := range m.worker.config.RelayPolicy.Tiers {
		for _, entry := range tier {
			if string(entry.ID) == a.RelayId && rp.ValidateInstance(entry, rp.RelayEndpoint(a.Instance)) == nil {
				return entry, true
			}
		}
	}
	return rp.RelayCatalogEntry{}, false
}

func (m *managedRelay) respond(ctx context.Context, remote *OfferAnswer) {
	a := remote.RelayPolicyAttempt
	defer func() {
		m.mu.Lock()
		if sameAttempt(m.current, a) {
			m.current = nil
			m.selected = ""
		}
		m.mu.Unlock()
	}()
	establishment, finish := context.WithTimeout(ctx, relayAttemptTimeout)
	defer finish()
	entry, ok := m.entry(a)
	if !ok {
		return
	}
	lease, err := m.worker.relayManager.AcquireRegistration(establishment, entry)
	if err != nil {
		m.recordFailure(rp.ReasonRelayUnavailable, true)
		return
	}
	defer lease.Release()
	instance, _, err := lease.InstanceAddress()
	if err != nil || instance != a.Instance {
		m.recordFailure(rp.ReasonInstanceMismatch, true)
		return
	}
	conn, err := lease.OpenConn(establishment, m.worker.config.Key)
	if err != nil {
		m.recordFailure(relayAttemptFailure(err), true)
		return
	}
	installed := false
	defer func() {
		if !installed {
			if err := conn.Close(); err != nil {
				m.worker.log.Debugf("close rejected relay attempt: %v", err)
			}
		}
	}()
	ready := cloneAttempt(a)
	ready.Phase = 1
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := m.send(ready); err != nil {
			m.worker.log.Debugf("send relay readiness: %v", err)
		}
		select {
		case <-establishment.Done():
			m.recordFailure(rp.ReasonPairTimeout, true)
			return
		case <-ticker.C:
			continue
		case answer := <-m.ack:
			if !sameAttempt(a, answer.RelayPolicyAttempt) || answer.RelayPolicyAttempt.Phase != 2 {
				continue
			}
			if !m.install(ctx, a, lease, conn, remote) {
				return
			}
			installed = true
			finish()
			confirmed := cloneAttempt(a)
			confirmed.Phase = 3
			if err := m.send(confirmed); err != nil {
				m.worker.log.Debugf("acknowledge relay confirmation: %v", err)
			}
			for {
				select {
				case <-ctx.Done():
					if err := conn.Close(); err != nil {
						m.worker.log.Debugf("close superseded relay: %v", err)
					}
					return
				case <-conn.Context().Done():
					return
				case retry := <-m.ack:
					if sameAttempt(a, retry.RelayPolicyAttempt) && retry.RelayPolicyAttempt.Phase == 2 {
						if err := m.send(confirmed); err != nil {
							m.worker.log.Debugf("repeat relay confirmation: %v", err)
						}
					}
				}
			}
		}
	}
}

func (m *managedRelay) install(ctx context.Context, a *sproto.RelayPolicyAttempt, lease *relayClient.Registration, conn *relayClient.Conn, remote *OfferAnswer) bool {
	m.mu.Lock()
	if m.stopped || ctx.Err() != nil || !sameAttempt(m.current, a) {
		m.mu.Unlock()
		return false
	}
	m.lease = lease
	m.retryDelay = time.Second
	m.lastReason = rp.ReasonNone
	m.exhausted = false
	m.selected = rp.RelayID(a.RelayId)
	m.selectedSequence = a.Sequence
	m.worker.relayLock.Lock()
	m.worker.relayedConn = conn
	m.worker.relayLock.Unlock()
	m.mu.Unlock()
	go m.worker.watchRelayedConn(conn)
	go m.worker.conn.onRelayConnectionIsReady(RelayConnInfo{relayedConn: conn, rosenpassPubKey: remote.RosenpassPubKey, rosenpassAddr: remote.RosenpassAddr})
	return true
}

func (m *managedRelay) control() {
	for _, tier := range m.worker.config.RelayPolicy.Tiers {
		tierCtx, cancelTier := context.WithCancel(m.worker.peerCtx)
		type acquired struct {
			lease *relayClient.Registration
			entry rp.RelayCatalogEntry
		}
		results := make(chan acquired, len(tier))
		var wg sync.WaitGroup
		jobs := make(chan rp.RelayCatalogEntry, len(tier))
		for _, entry := range tier {
			jobs <- entry
		}
		close(jobs)
		for i := 0; i < min(7, len(tier)); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for entry := range jobs {
					if tierCtx.Err() != nil {
						return
					}
					ctx, cancel := context.WithTimeout(tierCtx, relayAttemptTimeout)
					lease, err := m.worker.relayManager.AcquireRegistration(ctx, entry)
					cancel()
					if err == nil {
						results <- acquired{lease, entry}
					} else if tierCtx.Err() == nil {
						m.recordFailure(rp.ReasonRelayUnavailable, false)
					}
				}
			}()
		}
		go func() { wg.Wait(); close(results) }()
		selected := false
		for result := range results {
			if selected || tierCtx.Err() != nil {
				result.lease.Release()
				continue
			}
			selected = m.attempt(result.lease, result.entry)
			if !selected {
				result.lease.Release()
			}
			if selected {
				cancelTier()
			}
		}
		cancelTier()
		if selected || m.worker.peerCtx.Err() != nil {
			return
		}
	}
	m.mu.Lock()
	m.started = false
	m.exhausted = true
	if m.lastReason == rp.ReasonNone {
		m.lastReason = rp.ReasonExhausted
	}
	if m.retryDelay == 0 {
		m.retryDelay = time.Second
	} else {
		m.retryDelay = min(30*time.Second, 2*m.retryDelay)
	}
	m.mu.Unlock()
	go m.restart()
}

func (m *managedRelay) attempt(lease *relayClient.Registration, entry rp.RelayCatalogEntry) bool {
	instance, _, err := lease.InstanceAddress()
	if err != nil {
		m.recordFailure(rp.ReasonInstanceMismatch, false)
		return false
	}
	ctx, cancel := context.WithCancel(m.worker.peerCtx)
	establishment, finish := context.WithTimeout(ctx, relayAttemptTimeout)
	defer finish()
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		cancel()
		return false
	}
	m.sequence++
	a := m.announcement()
	a.Nonce = append([]byte(nil), m.nonce...)
	a.Sequence = m.sequence
	a.ResponderNonce = append([]byte(nil), m.remoteNonce...)
	a.RelayId = string(entry.ID)
	a.Instance = instance
	m.current = a
	m.cancel = cancel
	m.mu.Unlock()
	if err := m.send(a); err != nil {
		m.worker.log.Debugf("send initial relay selection: %v", err)
	}
	openDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-establishment.Done():
				return
			case <-openDone:
				return
			case <-ticker.C:
				if err := m.send(a); err != nil {
					m.worker.log.Debugf("repeat pending relay selection: %v", err)
				}
			}
		}
	}()
	conn, err := lease.OpenConn(establishment, m.worker.config.Key)
	close(openDone)
	if err != nil {
		m.recordFailure(relayAttemptFailure(err), false)
		cancel()
		return false
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := m.send(a); err != nil {
			m.worker.log.Debugf("send relay selection: %v", err)
		}
		select {
		case <-establishment.Done():
			m.recordFailure(rp.ReasonPairTimeout, false)
			cancel()
			if err := conn.Close(); err != nil {
				m.worker.log.Debugf("close unsuccessful relay attempt: %v", err)
			}
			return false
		case <-ticker.C:
			continue
		case remote := <-m.ack:
			if !sameAttempt(a, remote.RelayPolicyAttempt) || remote.RelayPolicyAttempt.Phase != 1 {
				continue
			}
			confirmed := cloneAttempt(a)
			confirmed.Phase = 2
			if err := m.send(confirmed); err != nil {
				continue
			}
			if !m.install(ctx, a, lease, conn, remote) {
				cancel()
				if err := conn.Close(); err != nil {
					m.worker.log.Debugf("close stale relay connection: %v", err)
				}
				return false
			}
			finish()
			go m.hold(ctx, cancel, lease, conn, confirmed)

			return true
		}
	}
}

func (m *managedRelay) close() {
	m.mu.Lock()
	m.stopped = true
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
}

func (m *managedRelay) hold(ctx context.Context, cancel context.CancelFunc, lease *relayClient.Registration, conn *relayClient.Conn, confirmed *sproto.RelayPolicyAttempt) {
	defer lease.Release()
	defer cancel()
	defer func() {
		m.mu.Lock()
		owned := sameAttempt(m.current, confirmed)
		if owned {
			m.started = false
			m.lastReason = rp.ReasonDisconnected
			m.selected = ""
		}
		m.mu.Unlock()
		if owned {
			go m.restart()
		}
	}()
	retry := time.NewTicker(time.Second)
	defer retry.Stop()
	acknowledged := false
	for {
		select {
		case <-ctx.Done():
			if err := conn.Close(); err != nil {
				m.worker.log.Debugf("close canceled relay: %v", err)
			}
			return
		case <-conn.Context().Done():
			return
		case answer := <-m.ack:
			if sameAttempt(confirmed, answer.RelayPolicyAttempt) && answer.RelayPolicyAttempt.Phase == 3 {
				acknowledged = true
			}
		case <-retry.C:
			if !acknowledged {
				if err := m.send(confirmed); err != nil {
					m.worker.log.Debugf("repeat relay selection confirmation: %v", err)
				}
			}
		}
	}
}

func (m *managedRelay) restart() {
	m.mu.Lock()
	delay := m.retryDelay
	m.mu.Unlock()
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-m.worker.peerCtx.Done():
		return
	case <-timer.C:
	}
	m.mu.Lock()
	if m.stopped || m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	m.control()
}

func (m *managedRelay) recordFailure(reason rp.FailureReason, exhausted bool) {
	m.mu.Lock()
	m.lastReason = reason
	m.exhausted = exhausted
	m.mu.Unlock()
}

func relayAttemptFailure(err error) rp.FailureReason {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return rp.ReasonPairTimeout
	case errors.Is(err, context.Canceled):
		return rp.ReasonCancelled
	default:
		return rp.ReasonRelayUnavailable
	}
}
