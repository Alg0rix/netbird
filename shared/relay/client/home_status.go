package client

import rp "github.com/netbirdio/netbird/shared/management/relaypolicy"

// HomePolicyStatus returns the current instructions and a validated, ready home identity.
// An absent or unready registration never implies successful application.
func (m *Manager) HomePolicyStatus() (*rp.ResolvedRelayPolicy, rp.RelayID, rp.FailureReason) {
	m.lifecycleMu.Lock()
	policy := m.homePolicy.Copy()
	m.relayClientMu.RLock()
	client := m.relayClient
	m.relayClientMu.RUnlock()
	m.lifecycleMu.Unlock()
	if policy == nil {
		return nil, "", rp.ReasonNone
	}
	if client == nil || !client.Ready() {
		if m.reconnectGuard != nil && m.RelayConnectError() != nil {
			return policy, "", rp.ReasonRelayUnavailable
		}
		return policy, "", rp.ReasonConnecting
	}
	instance, _, err := client.serverInstanceAddress()
	if err != nil {
		return policy, "", rp.ReasonInstanceMismatch
	}
	for _, tier := range policy.Tiers {
		for _, entry := range tier {
			if rp.ValidateInstance(entry, rp.RelayEndpoint(instance)) == nil {
				return policy, entry.ID, rp.ReasonNone
			}
		}
	}
	return policy, "", rp.ReasonInstanceMismatch
}
