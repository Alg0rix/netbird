package internal

import (
	"fmt"
	"reflect"

	log "github.com/sirupsen/logrus"

	nbnetworkmap "github.com/netbirdio/netbird/shared/management/networkmap"
	mgmProto "github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func (e *Engine) applyRelayPolicies(networkMap *mgmProto.NetworkMap) error {
	e.captureRelayPolicyIntent(networkMap)
	if e.relayManager == nil {
		return nil
	}
	entries := make(map[rp.RelayID]rp.RelayCatalogEntry)
	var catalogErr error
	var generation uint64
	collect := func(policy *rp.ResolvedRelayPolicy) {
		if policy == nil {
			return
		}
		if generation != 0 && generation != policy.CatalogGeneration {
			catalogErr = fmt.Errorf("inconsistent relay catalog generation")
			return
		}
		generation = policy.CatalogGeneration
		for _, tier := range policy.Tiers {
			for _, entry := range tier {
				if prior, ok := entries[entry.ID]; ok && !reflect.DeepEqual(prior, entry) {
					catalogErr = fmt.Errorf("inconsistent relay identity")
					return
				}
				entries[entry.ID] = entry
			}
		}
	}
	var home *rp.ResolvedRelayPolicy
	if config := networkMap.GetPeerConfig(); config != nil {
		var err error
		home, err = nbnetworkmap.FromProtocolRelayPolicy(config.GetHomeRelayPolicy())
		if err != nil {
			home = &rp.ResolvedRelayPolicy{State: rp.RelayPolicyBlocked}
		}
		collect(home)
	}
	for _, remote := range networkMap.GetRemotePeers() {
		policy, err := nbnetworkmap.FromProtocolRelayPolicy(remote.GetRelayPolicy())
		if err == nil {
			collect(policy)
		}
	}
	if catalogErr != nil {
		return catalogErr
	}
	catalog := make([]rp.RelayCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		catalog = append(catalog, entry)
	}
	var catalogApplyErr error
	if generation != 0 {
		catalogApplyErr = e.relayManager.SetCatalogGeneration(generation, catalog)
	} else {
		catalogApplyErr = e.relayManager.SetCatalog(catalog)
	}
	if err := catalogApplyErr; err != nil {
		return fmt.Errorf("apply relay identities: %w", err)
	}
	if networkMap.GetPeerConfig() != nil {
		if err := e.relayManager.SetHomePolicy(home); err != nil {
			return fmt.Errorf("apply home relay policy: %w", err)
		}
	}
	return nil
}

// failRelayPolicies blocks the home relay and drops every peer after an invalid policy, then returns err.
func (e *Engine) failRelayPolicies(err error) error {
	if e.relayManager != nil {
		if homeErr := e.relayManager.SetHomePolicy(&rp.ResolvedRelayPolicy{State: rp.RelayPolicyBlocked}); homeErr != nil {
			log.Debugf("block home relay after invalid policy: %v", homeErr)
		}
	}
	if closeErr := e.removeAllPeers(); closeErr != nil {
		return fmt.Errorf("close peers after invalid relay identities: %v: %w", closeErr, err)
	}
	return err
}

// relayPolicyUnusable reports whether a remote peer must be withheld because its relay policy cannot be used.
func relayPolicyUnusable(p *mgmProto.RemotePeerConfig) bool {
	policy, err := nbnetworkmap.FromProtocolRelayPolicy(p.GetRelayPolicy())
	return err != nil || (policy != nil && (policy.State == rp.RelayPolicyUnsupported || (policy.State == rp.RelayPolicyBlocked && policy.ForceRelay)))
}

// relayPolicyChanged reports whether the relay policy of a known peer changed enough to rebuild its connection.
// It also refreshes the reporting revision of an unchanged policy.
func relayPolicyChanged(current interface {
	RelayPolicy() *rp.ResolvedRelayPolicy
	UpdateRelayPolicyRevision(uint64)
}, p *mgmProto.RemotePeerConfig) (bool, error) {
	policy, err := nbnetworkmap.FromProtocolRelayPolicy(p.GetRelayPolicy())
	if err != nil {
		return false, err
	}
	existing := current.RelayPolicy()
	if (existing == nil) != (policy == nil) || (existing != nil && policy != nil && (existing.Digest != policy.Digest || existing.State != policy.State)) {
		return true, nil
	}
	if policy != nil {
		current.UpdateRelayPolicyRevision(policy.Revision)
	}
	return false, nil
}
