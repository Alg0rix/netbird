package client

import (
	"context"
	"fmt"
	"reflect"
	"time"

	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	log "github.com/sirupsen/logrus"
)

// SetHomePolicy replaces the immutable home registration instructions. Pair
// registrations retain their independent references during home reselection.
func (m *Manager) SetHomePolicy(policy *rp.ResolvedRelayPolicy) error {
	copyPolicy := policy.Copy()
	if copyPolicy != nil {
		var entries []rp.RelayCatalogEntry
		seen := make(map[rp.RelayID]bool)
		for _, tier := range copyPolicy.Tiers {
			for _, entry := range tier {
				if !seen[entry.ID] {
					entries = append(entries, entry)
					seen[entry.ID] = true
				}
			}
		}
		if err := m.SetCatalog(entries); err != nil {
			return err
		}
	}
	m.lifecycleMu.Lock()
	old := m.homePolicy
	if sameHomeInstructions(old, copyPolicy) {
		m.homePolicy = copyPolicy
		m.lifecycleMu.Unlock()
		return nil
	}
	m.homePolicy = copyPolicy
	running := m.running
	if m.homeCancel != nil {
		m.homeCancel()
	}
	// Restriction changes revoke the home owner immediately, without closing
	// the same concrete registration if an active pair still owns it.
	m.relayClientMu.Lock()
	previous := m.relayClient
	m.relayClient = nil
	m.relayClientMu.Unlock()
	if previous != nil {
		m.releasePickerClient(previous)
	}
	m.lifecycleMu.Unlock()
	if running {
		m.wakeHome()
	}
	return nil
}

func sameHomeInstructions(a, b *rp.ResolvedRelayPolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Digest == b.Digest && a.CatalogGeneration == b.CatalogGeneration && a.ReselectionGeneration == b.ReselectionGeneration && a.State == b.State && reflect.DeepEqual(a.Tiers, b.Tiers)
}

func (m *Manager) pickHome(ctx context.Context) (*Client, error) {
	m.lifecycleMu.Lock()
	policy := m.homePolicy.Copy()
	m.lifecycleMu.Unlock()
	if policy == nil {
		return m.serverPicker.PickServer(ctx)
	}
	if policy.State != rp.RelayPolicyReady {
		return nil, fmt.Errorf("home relay policy is %s", policy.State)
	}
	var lastErr error
	for _, tier := range policy.Tiers {
		addresses := make([]string, 0, len(tier))
		for _, entry := range tier {
			if entry.Active && !entry.Legacy {
				addresses = append(addresses, string(entry.Endpoint))
			}
		}
		c, err := m.serverPicker.pickURLs(ctx, addresses)
		if err == nil {
			return c, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("home relay policy has no available instances")
	}
	return nil, lastErr
}

func (m *Manager) wakeHome() {
	select {
	case m.homeUpdates <- struct{}{}:
	default:
	}
}

func (m *Manager) homeLoop() {
	defer m.closeRegistrations()
	interval := 2 * time.Second
	maxInterval := m.maxBackoffInterval
	if maxInterval <= 0 {
		maxInterval = defaultMaxBackoffInterval
	}
	retry := time.NewTimer(interval)
	defer retry.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.homeUpdates:
		case <-retry.C:
		}
		if m.Ready() {
			retry.Reset(2 * time.Second)
			continue
		}
		m.lifecycleMu.Lock()
		ctx, cancel := context.WithCancel(m.ctx)
		m.homeCancel = cancel
		m.lifecycleMu.Unlock()
		if m.netEvents != nil {
			if _, err := m.netEvents.Wait(ctx); err != nil {
				cancel()
				retry.Reset(interval)
				continue
			}
		}
		c, err := m.pickHome(ctx)
		m.lifecycleMu.Lock()
		if ctx.Err() != nil {
			m.lifecycleMu.Unlock()
			if c != nil {
				m.releasePickerClient(c)
			}
			cancel()
			retry.Reset(2 * time.Second)
			continue
		}
		if err == nil {
			m.storeClient(c)
		}
		m.reconnectGuard.setLastError(err)
		m.lifecycleMu.Unlock()
		cancel()
		if err == nil {
			m.onServerConnected()
		}
		if err == nil {
			interval = 2 * time.Second
		} else {
			interval *= 2
			if interval > maxInterval {
				interval = maxInterval
			}
		}
		retry.Reset(interval)
	}
}

func (m *Manager) closeRegistrations() {
	m.relayClientsMutex.Lock()
	m.stopped = true
	tracks := m.relayClients
	m.relayClients = make(map[string]*RelayTrack)
	m.aliases = make(map[rp.RelayEndpoint]rp.RelayEndpoint)
	m.approved = make(map[rp.RelayEndpoint]rp.RelayCatalogEntry)
	m.relayClientsMutex.Unlock()
	m.relayClientMu.Lock()
	m.relayClient = nil
	m.relayClientMu.Unlock()
	m.pickerLeaseMu.Lock()
	m.pickerLeases = make(map[*Client][]*Registration)
	m.pickerLeaseMu.Unlock()
	for _, track := range tracks {
		<-track.ready
		track.RLock()
		c := track.relayClient
		track.RUnlock()
		if c != nil {
			c.SetOnDisconnectListener(nil)
			if err := c.Close(); err != nil {
				log.Debugf("close relay registration: %v", err)
			}
		}
	}
}
