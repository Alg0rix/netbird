package client

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// Registration pins the local peer's shared registration at one relay instance.
// Release drops this owner's reference without interrupting other owners.
type Registration struct {
	track  *RelayTrack
	client *Client
	once   sync.Once
}

// InstanceAddress returns the validated concrete instance and its connected address.
func (r *Registration) InstanceAddress() (string, netip.Addr, error) {
	return r.client.serverInstanceAddress()
}

// OpenConn opens a peer channel independently of home relay readiness.
func (r *Registration) OpenConn(ctx context.Context, remoteKey string) (*Conn, error) {
	return r.client.OpenConn(ctx, remoteKey)
}

// Release relinquishes this reference exactly once.
func (r *Registration) Release() {
	r.once.Do(func() {
		r.track.Lock()
		r.track.refs--
		r.track.created = time.Now()
		if r.track.refs == 0 && r.track.relayClient == nil && r.track.dialCancel != nil {
			r.track.dialCancel()
		}
		r.track.Unlock()
	})
}

// SetCatalog adds approved identity mappings before home or pair dialing begins.
// Existing live aliases cannot be reassigned to another concrete instance.
func (m *Manager) SetCatalog(entries []rp.RelayCatalogEntry) error {
	catalog, err := rp.ValidateCatalog(rp.RelayCatalog{Entries: entries})
	if err != nil {
		return err
	}
	m.relayClientsMutex.Lock()
	defer m.relayClientsMutex.Unlock()
	if m.stopped {
		return context.Canceled
	}
	if m.catalogPublished {
		for _, entry := range catalog.Entries {
			if approved, ok := m.approved[entry.Endpoint]; !ok || !reflect.DeepEqual(approved, entry) {
				return fmt.Errorf("relay selection is stale")
			}
		}
		return nil
	}
	aliases := make(map[rp.RelayEndpoint]rp.RelayEndpoint, len(m.aliases))
	for alias, key := range m.aliases {
		aliases[alias] = key
	}
	for _, entry := range catalog.Entries {
		key := entry.InstanceEndpoint
		for _, addr := range append(append([]rp.RelayEndpoint(nil), entry.Aliases...), entry.Endpoint, key) {
			if prior, ok := aliases[addr]; ok && prior != key {
				// Learned legacy endpoints may become an explicit concrete mapping.
				if _, managed := m.approved[addr]; managed {
					return fmt.Errorf("relay alias belongs to another instance")
				}
			}
			aliases[addr] = key
		}
	}
	moves := make(map[string]*RelayTrack)
	// Validate all moves before publishing any owner or alias change.
	for _, entry := range catalog.Entries {
		key := string(entry.InstanceEndpoint)
		var owner *RelayTrack
		for _, addr := range append(append([]rp.RelayEndpoint(nil), entry.Aliases...), entry.Endpoint, entry.InstanceEndpoint) {
			if rt := m.relayClients[string(addr)]; rt != nil {
				if owner != nil && owner != rt {
					return fmt.Errorf("relay aliases already have separate registrations")
				}
				owner = rt
			}
		}
		if owner != nil {
			moves[key] = owner
		}
	}
	for key, owner := range moves {
		for addr, rt := range m.relayClients {
			if rt == owner {
				delete(m.relayClients, addr)
			}
		}
		m.relayClients[key] = owner
	}
	m.aliases = aliases
	for _, entry := range catalog.Entries {
		for _, addr := range append(append([]rp.RelayEndpoint(nil), entry.Aliases...), entry.Endpoint, entry.InstanceEndpoint) {
			m.approved[addr] = entry
		}
	}
	return nil
}

// AcquireRegistration acquires a shared registration and validates the handshake
// against the supplied operator-approved concrete identity. No remote IP is used.
func (m *Manager) AcquireRegistration(ctx context.Context, entry rp.RelayCatalogEntry) (*Registration, error) {
	if !entry.Active || entry.Legacy {
		return nil, fmt.Errorf("relay is not available for managed selection")
	}
	normalized, err := rp.ValidateCatalog(rp.RelayCatalog{Entries: []rp.RelayCatalogEntry{entry}})
	if err != nil {
		return nil, err
	}
	entry = normalized.Entries[0]
	m.relayClientsMutex.RLock()
	published := m.catalogPublished
	approved, known := m.approved[entry.Endpoint]
	m.relayClientsMutex.RUnlock()
	if published {
		if !known || !reflect.DeepEqual(approved, entry) {
			return nil, fmt.Errorf("relay selection is stale")
		}
	} else if err := m.SetCatalog([]rp.RelayCatalogEntry{entry}); err != nil {
		return nil, err
	}

	return m.acquire(ctx, string(entry.Endpoint), netip.Addr{}, &entry)
}

func (m *Manager) acquire(ctx context.Context, address string, ip netip.Addr, entry *rp.RelayCatalogEntry) (*Registration, error) {
	if entry != nil {
		copyEntry := *entry
		copyEntry.Aliases = append([]rp.RelayEndpoint(nil), entry.Aliases...)
		entry = &copyEntry
	}
	key := rp.RelayEndpoint(address)
	if canonical, err := rp.CanonicalEndpoint(key); err == nil {
		key = canonical
	}
	m.relayClientsMutex.Lock()
	if m.stopped || m.ctx.Err() != nil {
		m.relayClientsMutex.Unlock()
		return nil, context.Canceled
	}
	if err := m.validateSelectionLocked(entry); err != nil {
		m.relayClientsMutex.Unlock()
		return nil, err
	}
	if entry == nil {
		if approved, ok := m.approved[key]; ok {
			copied := approved
			entry = &copied
			ip = netip.Addr{}
		}
	}
	if canonical, ok := m.aliases[key]; ok {
		key = canonical
	}
	rt := m.relayClients[string(key)]
	if rt != nil {
		rt.RLock()
		c, finished := rt.relayClient, rt.err
		rt.RUnlock()
		if finished != nil || (c != nil && !c.Ready()) {
			delete(m.relayClients, string(key))
			rt = nil
		}
	}
	start := rt == nil
	if start {
		rt = NewRelayTrack()
		m.relayClients[string(key)] = rt
	}
	rt.Lock()
	rt.refs++
	rt.Unlock()
	m.relayClientsMutex.Unlock()
	reg := &Registration{track: rt}
	if start {
		go func() {
			// The shared lifetime belongs to the manager; the initiating attempt only
			// bounds the handshake, never the registration after it has succeeded.
			c := NewClientWithServerIP(address, ip, m.tokenStore, m.peerID, m.mtu)
			c.SetTransportFallback(m.transportFallback)
			c.netEvents = m.netEvents
			dialCtx, cancel := context.WithTimeout(m.ctx, defaultConnectionTimeout)
			rt.Lock()
			rt.dialCancel = cancel
			empty := rt.refs == 0
			rt.Unlock()
			if empty {
				cancel()
			}
			err := c.connectWithLifetime(dialCtx, m.ctx)
			cancel()
			// Catalog publication can promote an already-running legacy handshake.
			// Validate against that publication before exposing the registration.
			m.relayClientsMutex.RLock()
			selectionErr := m.validateSelectionLocked(entry)
			validationEntry := entry
			if validationEntry == nil {
				if approved, ok := m.approved[rp.RelayEndpoint(address)]; ok {
					copyEntry := approved
					validationEntry = &copyEntry
				}
			}
			m.relayClientsMutex.RUnlock()
			if err == nil {
				err = selectionErr
			}
			if err == nil && validationEntry != nil {
				var instance string
				instance, _, err = c.serverInstanceAddress()
				if err == nil {
					err = rp.ValidateInstance(*validationEntry, rp.RelayEndpoint(instance))
				}
			}
			if err != nil {
				if closeErr := c.Close(); closeErr != nil {
					log.Debugf("close rejected relay registration: %v", closeErr)
				}
			} else {
				c.SetOnDisconnectListener(m.onServerDisconnected)
			}
			rt.Lock()
			rt.err = err
			if err == nil {
				rt.relayClient = c
			}
			rt.Unlock()
			close(rt.ready)
			m.relayClientsMutex.Lock()
			if err != nil {
				for ownerKey, owner := range m.relayClients {
					if owner == rt {
						delete(m.relayClients, ownerKey)
					}
				}
			} else if instance, _, instanceErr := c.serverInstanceAddress(); instanceErr == nil && !m.stopped {
				// Learning the home instance lets a later explicit catalog reuse its owner.
				if canonical, canonicalErr := rp.CanonicalEndpoint(rp.RelayEndpoint(instance)); canonicalErr == nil {
					if _, exists := m.aliases[canonical]; !exists {
						m.aliases[canonical] = key
					}
				}
			}
			m.relayClientsMutex.Unlock()

		}()
	}
	select {
	case <-ctx.Done():
		reg.Release()
		return nil, ctx.Err()
	case <-rt.ready:
	}
	rt.RLock()
	c, err := rt.relayClient, rt.err
	rt.RUnlock()
	if err == nil && entry != nil {
		var instance string
		instance, _, err = c.serverInstanceAddress()
		if err == nil {
			err = rp.ValidateInstance(*entry, rp.RelayEndpoint(instance))
		}
	}
	if err != nil {
		reg.Release()
		return nil, err
	}
	m.relayClientsMutex.RLock()
	err = m.validateSelectionLocked(entry)
	if err == nil && (m.stopped || m.ctx.Err() != nil) {
		err = context.Canceled
	}
	m.relayClientsMutex.RUnlock()
	if err != nil {
		reg.Release()
		return nil, err
	}
	reg.client = c
	return reg, nil
}

func (m *Manager) acquirePickerClient(ctx context.Context, address string) (*Client, error) {
	reg, err := m.acquire(ctx, address, netip.Addr{}, nil)
	if err != nil {
		return nil, err
	}
	m.pickerLeaseMu.Lock()
	m.pickerLeases[reg.client] = append(m.pickerLeases[reg.client], reg)
	m.pickerLeaseMu.Unlock()
	return reg.client, nil
}

func (m *Manager) releasePickerClient(c *Client) {
	m.pickerLeaseMu.Lock()
	refs := m.pickerLeases[c]
	var reg *Registration
	if len(refs) > 0 {
		reg = refs[len(refs)-1]
		if len(refs) == 1 {
			delete(m.pickerLeases, c)
		} else {
			m.pickerLeases[c] = refs[:len(refs)-1]
		}
	}
	m.pickerLeaseMu.Unlock()
	if reg != nil {
		reg.Release()
	}
}

// SetCatalogGeneration publishes the approved union of effective home and pair
// instructions. Updates merge within a generation; a new generation replaces mappings.
// Existing leases may drain, while old offers cannot restore replaced mappings.
func (m *Manager) SetCatalogGeneration(generation uint64, entries []rp.RelayCatalogEntry) error {
	canonical, err := rp.ValidateCatalog(rp.RelayCatalog{Generation: generation, Entries: entries})
	if err != nil {
		return err
	}
	m.relayClientsMutex.Lock()
	defer m.relayClientsMutex.Unlock()
	if m.stopped {
		return context.Canceled
	}
	if m.catalogPublished && canonical.Generation < m.catalogGeneration {
		return fmt.Errorf("relay catalog generation is stale")
	}
	if m.catalogPublished && canonical.Generation == m.catalogGeneration {
		byID := make(map[rp.RelayID]rp.RelayCatalogEntry)
		for _, entry := range m.approved {
			byID[entry.ID] = entry
		}
		for _, entry := range canonical.Entries {
			if old, ok := byID[entry.ID]; ok && !reflect.DeepEqual(old, entry) {
				return fmt.Errorf("relay mapping changed without advancing catalog generation")
			}
			byID[entry.ID] = entry
		}
		combined := rp.RelayCatalog{Generation: canonical.Generation}
		for _, entry := range byID {
			combined.Entries = append(combined.Entries, entry)
		}
		var validationErr error
		canonical, validationErr = rp.ValidateCatalog(combined)
		if validationErr != nil {
			return validationErr
		}
	}
	aliases := make(map[rp.RelayEndpoint]rp.RelayEndpoint)
	approved := make(map[rp.RelayEndpoint]rp.RelayCatalogEntry)
	for _, entry := range canonical.Entries {
		key := entry.InstanceEndpoint
		for _, addr := range append(append([]rp.RelayEndpoint(nil), entry.Aliases...), entry.Endpoint, key) {
			aliases[addr] = key
			approved[addr] = entry
		}
	}

	// Map all approved aliases, including in-flight legacy owners, before
	// publishing. The handshake revalidates a promoted owner after Connect.
	moves := make(map[string]*RelayTrack)
	for _, entry := range canonical.Entries {
		key := string(entry.InstanceEndpoint)
		var owner *RelayTrack
		for _, addr := range append(append([]rp.RelayEndpoint(nil), entry.Aliases...), entry.Endpoint, entry.InstanceEndpoint) {
			rt := m.relayClients[string(addr)]
			if rt == nil {
				continue
			}
			rt.RLock()
			c := rt.relayClient
			rt.RUnlock()
			if c != nil {
				instance, _, instanceErr := c.serverInstanceAddress()
				if instanceErr != nil || rp.ValidateInstance(entry, rp.RelayEndpoint(instance)) != nil {
					continue
				}
			}
			if owner != nil && owner != rt {
				return fmt.Errorf("relay aliases already have separate registrations")
			}
			owner = rt
		}
		if owner != nil {
			moves[key] = owner
		}
	}
	for key, owner := range moves {
		for addr, rt := range m.relayClients {
			if rt == owner {
				delete(m.relayClients, addr)
			}
		}
		m.relayClients[key] = owner
	}

	m.aliases = aliases
	m.approved = approved
	m.catalogGeneration = canonical.Generation
	m.catalogPublished = true
	return nil
}

func (m *Manager) validateSelectionLocked(entry *rp.RelayCatalogEntry) error {
	if entry == nil || !m.catalogPublished {
		return nil
	}
	approved, ok := m.approved[entry.Endpoint]
	if !ok || !reflect.DeepEqual(approved, *entry) {
		return fmt.Errorf("relay selection is stale")
	}
	return nil
}
