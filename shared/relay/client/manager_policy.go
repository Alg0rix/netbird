package client

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	log "github.com/sirupsen/logrus"
)

// Fork relay policy mode of the Manager. The upstream manager methods call these when WithRelayPolicy is set.
// The upstream code path is untouched when the option is off.

// servePolicy replaces Manager.Serve: it picks the home relay by policy and runs the home loop.
func (m *Manager) servePolicy() error {
	m.lifecycleMu.Lock()
	if m.running {
		m.lifecycleMu.Unlock()
		return fmt.Errorf("manager already serving")
	}
	m.running = true
	ctx, cancel := context.WithCancel(m.ctx)
	m.homeCancel = cancel
	m.lifecycleMu.Unlock()
	client, err := m.pickHome(ctx)
	m.lifecycleMu.Lock()
	if err == nil && ctx.Err() == nil {
		m.storeClient(client)
	} else if client != nil {
		m.releasePickerClient(client)
	}
	m.lifecycleMu.Unlock()
	m.reconnectGuard.setLastError(err)
	go m.homeLoop()
	go m.startCleanupLoop()
	return err
}

// openConnPolicy replaces Manager.OpenConn: every connection goes through a pooled registration.
func (m *Manager) openConnPolicy(ctx context.Context, serverAddress, peerKey string, serverIP netip.Addr) (*Conn, error) {
	reg, err := m.acquire(ctx, serverAddress, serverIP, nil)
	if err != nil {
		return nil, err
	}
	defer reg.Release()
	return reg.OpenConn(ctx, peerKey)
}

// relayStatesPolicy replaces Manager.RelayStates.
func (m *Manager) relayStatesPolicy() []RelayConnState {
	var states []RelayConnState

	m.relayClientMu.RLock()
	home := m.relayClient
	m.relayClientMu.RUnlock()
	if home != nil {
		st := relayConnState(home)
		// The home relay reconnects through the guard, so the real failure
		// reason lives there rather than on the (stale) client.
		if st.Err != nil {
			if gErr := m.reconnectGuard.LastError(); gErr != nil {
				st.Err = gErr
			}
		}
		states = append(states, st)
	}

	// Snapshot the tracks, then query each outside the map lock: a track can be
	// held by an in-progress Connect, and blocking on it must not stall other
	// relay operations.
	m.relayClientsMutex.RLock()
	tracks := make([]*RelayTrack, 0, len(m.relayClients))
	for _, rt := range m.relayClients {
		tracks = append(tracks, rt)
	}
	m.relayClientsMutex.RUnlock()

	// Only connected foreign relays carry state; a failed connect is evicted
	// immediately (openConnVia), so there is no error state to surface.
	for _, rt := range tracks {
		rt.RLock()
		rc := rt.relayClient
		rt.RUnlock()
		if rc != nil && rc != home {
			states = append(states, relayConnState(rc))
		}
	}

	return states
}

// cleanUpPolicy replaces Manager.cleanUpUnusedRelays and honors registration references.
func (m *Manager) cleanUpPolicy() {
	var closing []*Client
	m.relayClientsMutex.Lock()
	for addr, rt := range m.relayClients {
		rt.Lock()
		c := rt.relayClient
		if c != nil && rt.refs == 0 && time.Since(rt.created) > m.keepUnusedServerTime && !c.HasConns() {
			delete(m.relayClients, addr)
			closing = append(closing, c)
		}
		rt.Unlock()
	}
	m.relayClientsMutex.Unlock()
	for _, c := range closing {
		c.SetOnDisconnectListener(nil)
		if err := c.Close(); err != nil {
			log.Debugf("close unused relay: %v", err)
		}
	}
}

// onServerDisconnected handles relay disconnect events. For the home server it wakes the home loop.
// For foreign servers it evicts the now-dead client so the next OpenConn builds a fresh one.
func (m *Manager) onServerDisconnected(serverAddress string) {
	m.relayClientMu.RLock()
	isHome := m.relayClient != nil && serverAddress == m.relayClient.connectionURL
	m.relayClientMu.RUnlock()
	m.evictForeignRelay(serverAddress)
	if isHome {
		m.wakeHome()
	}
}

func (m *Manager) evictForeignRelay(serverAddress string) {
	m.relayClientsMutex.Lock()
	defer m.relayClientsMutex.Unlock()

	rt, ok := m.relayClients[serverAddress]
	if !ok {
		return
	}

	rt.RLock()
	client := rt.relayClient
	rt.RUnlock()
	if client == nil {
		log.Debugf("keeping foreign relay track with a dial in progress: %s", serverAddress)
		return
	}
	if client.Ready() {
		log.Debugf("keeping reconnected foreign relay client: %s", serverAddress)
		return
	}

	delete(m.relayClients, serverAddress)
	log.Debugf("evicted disconnected foreign relay client: %s", serverAddress)
}

func (m *Manager) storeClient(client *Client) {
	m.relayClientMu.Lock()
	old := m.relayClient
	m.relayClient = client
	m.relayClientMu.Unlock()
	if old != nil {
		m.releasePickerClient(old)
	}
}
