package client

import (
	"context"
	"errors"
	"sync"

	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// Fork relay policy state. Upstream structs embed these types in one line each, so the
// fork never edits upstream fields. Every field is unused unless WithRelayPolicy is set.

// policyState is embedded in Manager.
type policyState struct {
	// policyEnabled switches the Manager to the relay policy mode.
	policyEnabled bool

	lifecycleMu       sync.Mutex
	homeUpdates       chan struct{}
	homePolicy        *rp.ResolvedRelayPolicy
	homeCancel        context.CancelFunc
	aliases           map[rp.RelayEndpoint]rp.RelayEndpoint
	approved          map[rp.RelayEndpoint]rp.RelayCatalogEntry
	pickerLeases      map[*Client][]*Registration
	pickerLeaseMu     sync.Mutex
	stopped           bool
	catalogGeneration uint64
	catalogPublished  bool
}

// trackPolicy is embedded in RelayTrack.
type trackPolicy struct {
	refs       int
	dialCancel context.CancelFunc
}

// clientPolicy is embedded in Client.
type clientPolicy struct {
	// onDisconnect is called once after a session ends. Callers clear it before an intentional close.
	onDisconnect func(string)
	listenerMu   sync.Mutex
}

// WithRelayPolicy turns on the relay policy mode: a policy-aware home relay, pooled registrations
// for foreign relays, and per-pair relay choice. Without it the manager behaves like upstream.
func WithRelayPolicy() ManagerOption {
	return func(m *Manager) { m.policyEnabled = true }
}

// initPolicy prepares the policy state. Upstream NewManager calls it after the options are applied.
func (m *Manager) initPolicy() {
	// The maps exist in both modes so policy calls on a plain manager cannot panic.
	m.homeUpdates = make(chan struct{}, 1)
	m.aliases = make(map[rp.RelayEndpoint]rp.RelayEndpoint)
	m.approved = make(map[rp.RelayEndpoint]rp.RelayCatalogEntry)
	m.pickerLeases = make(map[*Client][]*Registration)
	if !m.policyEnabled {
		return
	}
	m.serverPicker.acquire = m.acquirePickerClient
	m.serverPicker.release = m.releasePickerClient
}

// requirePolicyMode makes a policy call on a plain manager fail loudly. The engine must create the
// manager with WithRelayPolicy; without it the policy would be ignored without any sign.
func (m *Manager) requirePolicyMode() error {
	if !m.policyEnabled {
		return errors.New("relay policy mode is off: create the manager with WithRelayPolicy")
	}
	return nil
}
