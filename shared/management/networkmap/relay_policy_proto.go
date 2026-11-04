package networkmap

import (
	"encoding/hex"
	"fmt"

	"github.com/netbirdio/netbird/shared/management/networkmap/nmdata"
	"github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// ToProtocolRelayPolicy preserves presence and resolved transport fences on the wire.
func ToProtocolRelayPolicy(policy *rp.ResolvedRelayPolicy) *proto.RelayPolicy {
	if policy == nil {
		return nil
	}
	result := &proto.RelayPolicy{
		State: toRelayPolicyState(policy.State), Revision: policy.Revision,
		CatalogGeneration: policy.CatalogGeneration, ReselectionGeneration: policy.ReselectionGeneration,
		Digest: string(policy.Digest), ForceRelay: policy.ForceRelay, PreferenceRule: string(policy.PreferenceRule),
		Eliminated: relayIDsToStrings(policy.Eliminated),
	}
	for _, tier := range policy.Tiers {
		converted := &proto.RelayPolicyTier{}
		for _, entry := range tier {
			aliases := make([]string, len(entry.Aliases))
			for i, alias := range entry.Aliases {
				aliases[i] = string(alias)
			}
			converted.Relays = append(converted.Relays, &proto.RelayCatalogEntry{
				Id: string(entry.ID), Endpoint: string(entry.Endpoint), InstanceEndpoint: string(entry.InstanceEndpoint),
				Aliases: aliases, Active: entry.Active, Legacy: entry.Legacy,
			})
		}
		result.Tiers = append(result.Tiers, converted)
	}
	for _, contributor := range policy.Contributors {
		result.Contributors = append(result.Contributors, &proto.RelayPolicyContribution{
			Id: string(contributor.ID), Revision: contributor.Revision, Eliminated: relayIDsToStrings(contributor.Eliminated),
		})
	}
	return result
}

func toRelayPolicyState(state rp.State) proto.RelayPolicyState {
	switch state {
	case rp.RelayPolicyReady:
		return proto.RelayPolicyState_RelayPolicyStateReady
	case rp.RelayPolicyBlocked:
		return proto.RelayPolicyState_RelayPolicyStateBlocked
	case rp.RelayPolicyUnsupported:
		return proto.RelayPolicyState_RelayPolicyStateUnsupported
	default:
		return proto.RelayPolicyState_RelayPolicyStateUnknown
	}
}

// FromProtocolRelayPolicy validates managed instructions without widening malformed restrictions.
// Callers must reject the affected connection when a present message returns an error.
func FromProtocolRelayPolicy(policy *proto.RelayPolicy) (*rp.ResolvedRelayPolicy, error) {
	if policy == nil {
		return nil, nil //nolint:nilnil // Absence preserves unmanaged connection behavior.
	}
	state, err := fromRelayPolicyState(policy.State)
	if err != nil {
		return nil, err
	}
	if len(policy.Tiers) > 65 || len(policy.Contributors) > 1000 || len(policy.Eliminated) > 4096 {
		return nil, fmt.Errorf("relay policy exceeds instruction limits")
	}
	if policy.Revision == 0 || policy.CatalogGeneration == 0 {
		return nil, fmt.Errorf("relay policy is missing revision fences")
	}
	if digest, err := hex.DecodeString(policy.Digest); err != nil || len(digest) != 32 {
		return nil, fmt.Errorf("invalid relay policy digest")
	}
	result := &rp.ResolvedRelayPolicy{
		State: state, Revision: policy.Revision, CatalogGeneration: policy.CatalogGeneration,
		ReselectionGeneration: policy.ReselectionGeneration, Digest: rp.Digest(policy.Digest),
		ForceRelay: policy.ForceRelay, PreferenceRule: rp.RelayRuleID(policy.PreferenceRule),
		Eliminated: stringsToRelayIDs(policy.Eliminated),
	}
	var entries []rp.RelayCatalogEntry
	for _, tier := range policy.Tiers {
		if tier == nil || len(tier.Relays) == 0 || len(tier.Relays) > 4096-len(entries) {
			return nil, fmt.Errorf("invalid relay priority tier")
		}
		converted := make([]rp.RelayCatalogEntry, 0, len(tier.Relays))
		for _, entry := range tier.Relays {
			if entry == nil || !entry.Active || entry.Legacy || len(entry.Aliases) > 128 {
				return nil, fmt.Errorf("relay policy contains an unapproved relay")
			}
			aliases := make([]rp.RelayEndpoint, len(entry.Aliases))
			for i, alias := range entry.Aliases {
				aliases[i] = rp.RelayEndpoint(alias)
			}
			converted = append(converted, rp.RelayCatalogEntry{ID: rp.RelayID(entry.Id), Endpoint: rp.RelayEndpoint(entry.Endpoint),
				InstanceEndpoint: rp.RelayEndpoint(entry.InstanceEndpoint), Aliases: aliases, Active: true})
		}
		entries = append(entries, converted...)
		result.Tiers = append(result.Tiers, converted)
	}
	if state == rp.RelayPolicyReady && len(entries) == 0 {
		return nil, fmt.Errorf("ready relay policy has no permitted relays")
	}
	catalog, err := rp.ValidateCatalog(rp.RelayCatalog{Entries: entries})
	if err != nil {
		return nil, fmt.Errorf("invalid relay policy catalog: %w", err)
	}
	byID := make(map[rp.RelayID]rp.RelayCatalogEntry, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		byID[entry.ID] = entry
	}
	for _, tier := range result.Tiers {
		for i, entry := range tier {
			tier[i] = byID[entry.ID]
		}
	}
	for _, contributor := range policy.Contributors {
		if contributor == nil || len(contributor.Eliminated) > 4096 {
			return nil, fmt.Errorf("invalid relay policy contributor")
		}
		result.Contributors = append(result.Contributors, rp.Contribution{
			ID: rp.RelayRuleID(contributor.Id), Revision: contributor.Revision, Eliminated: stringsToRelayIDs(contributor.Eliminated),
		})
	}
	return result, nil
}

func fromRelayPolicyState(state proto.RelayPolicyState) (rp.State, error) {
	switch state {
	case proto.RelayPolicyState_RelayPolicyStateReady:
		return rp.RelayPolicyReady, nil
	case proto.RelayPolicyState_RelayPolicyStateBlocked:
		return rp.RelayPolicyBlocked, nil
	case proto.RelayPolicyState_RelayPolicyStateUnsupported:
		return rp.RelayPolicyUnsupported, nil
	default:
		return "", fmt.Errorf("unknown relay policy state")
	}
}

func relayIDsToStrings(ids []rp.RelayID) []string {
	if ids == nil {
		return nil
	}
	result := make([]string, len(ids))
	for i, id := range ids {
		result[i] = string(id)
	}
	return result
}

func stringsToRelayIDs(ids []string) []rp.RelayID {
	if ids == nil {
		return nil
	}
	result := make([]rp.RelayID, len(ids))
	for i, id := range ids {
		result[i] = rp.RelayID(id)
	}
	return result
}

type relayPeerKey string

// ApplyRemoteRelayPolicies attaches pair-specific instructions without changing peer authorization.
func ApplyRemoteRelayPolicies(configs []*proto.RemotePeerConfig, peers []*nmdata.Peer, policies map[rp.PeerID]*rp.ResolvedRelayPolicy) {
	byKey := make(map[relayPeerKey]*rp.ResolvedRelayPolicy, len(peers))
	for _, peer := range peers {
		if policy := policies[rp.PeerID(peer.ID)]; policy != nil {
			byKey[relayPeerKey(peer.Key)] = policy
		}
	}
	for _, config := range configs {
		config.RelayPolicy = ToProtocolRelayPolicy(byKey[relayPeerKey(config.WgPubKey)])
	}
}

// EncodePairRelayPolicies rekeys account-local peer IDs into stable public keys for component maps.
func EncodePairRelayPolicies(peers map[string]*nmdata.Peer, policies map[rp.PeerID]*rp.ResolvedRelayPolicy, routerPeers ...map[string]*nmdata.Peer) map[string]*proto.RelayPolicy {
	if len(policies) == 0 {
		return nil
	}
	result := make(map[string]*proto.RelayPolicy, len(policies))
	for id, policy := range policies {
		peer := peers[string(id)]
		if peer == nil {
			for _, routers := range routerPeers {
				if peer = routers[string(id)]; peer != nil {
					break
				}
			}
		}
		if peer == nil || policy == nil {
			continue
		}
		result[canonicalizeWgKey(peer.Key)] = ToProtocolRelayPolicy(policy)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func decodePairRelayPolicies(policies map[string]*proto.RelayPolicy, peers map[string]*nmdata.Peer) (map[rp.PeerID]*rp.ResolvedRelayPolicy, error) {
	if len(policies) == 0 {
		return nil, nil //nolint:nilnil // An absent component has no managed pair instructions.
	}
	result := make(map[rp.PeerID]*rp.ResolvedRelayPolicy, len(policies))
	for key, policy := range policies {
		if policy == nil || peers[key] == nil {
			return nil, fmt.Errorf("relay policy references an absent peer")
		}
		decoded, err := FromProtocolRelayPolicy(policy)
		if err != nil {
			return nil, err
		}
		result[rp.PeerID(key)] = decoded
	}
	return result, nil
}
