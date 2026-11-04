package networkmap

import (
	"maps"
	"slices"

	log "github.com/sirupsen/logrus"

	"github.com/netbirdio/netbird/shared/management/networkmap/nmdata"
	"github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/types"
)

func (nmd *NetworkMapData) policyResolver() (*relaypolicy.Resolver, error) {
	nmd.relayResolverOnce.Do(func() {
		snapshot := nmd.RelayPolicies
		input := relaypolicy.RelayPolicyInput{AccountID: snapshot.AccountID, Catalog: snapshot.Catalog,
			Groups: snapshot.Groups, Profiles: snapshot.Profiles, Policies: snapshot.Policies, Revision: snapshot.Revision}
		memberships := make(map[string][]relaypolicy.PeerGroupID, len(nmd.Peers))
		for id, group := range nmd.Groups {
			input.PeerGroups = append(input.PeerGroups, relaypolicy.PeerGroupID(id))
			if group == nil {
				continue
			}
			for _, peerID := range group.Peers {
				memberships[peerID] = append(memberships[peerID], relaypolicy.PeerGroupID(id))
			}
		}
		for id, peer := range nmd.Peers {
			if peer != nil {
				input.Peers = append(input.Peers, relaypolicy.RelayPeer{ID: relaypolicy.PeerID(id), AccountID: snapshot.AccountID, GroupIDs: memberships[id]})
			}
		}
		nmd.relayResolver, nmd.relayResolverErr = relaypolicy.NewResolver(input)
		if nmd.relayResolverErr != nil {
			log.Warnf("invalid relay policy snapshot; withholding affected connectivity: %v", nmd.relayResolverErr)
		}
	})
	return nmd.relayResolver, nmd.relayResolverErr
}

func (nmd *NetworkMapData) applyRelayPolicies(components *types.NetworkMapComponents) {
	if len(nmd.RelayPolicies.Profiles) == 0 && len(nmd.RelayPolicies.Policies) == 0 {
		return
	}
	resolver, err := nmd.policyResolver()
	if err != nil {
		nmd.withholdRelayPeers(components, nil)
		return
	}
	local := components.Peers[components.PeerID]
	if local == nil {
		return
	}
	home, err := resolver.ResolveHome(relaypolicy.PeerID(local.ID), relaypolicy.ResolveOptions{
		ReselectionGeneration: nmd.relayReselectionGeneration(local.ID, ""),
	})
	if err != nil {
		nmd.withholdRelayPeers(components, nil)
		return
	}
	if home != nil && !local.HasCapability(nmdata.PeerCapabilityRelayPolicy) {
		home.State, home.Tiers = relaypolicy.RelayPolicyUnsupported, nil
	}
	components.HomeRelayPolicy = home
	blocked := make(map[string]struct{})
	for id, peer := range relayTransportPeers(components) {
		if id == local.ID || peer == nil {
			continue
		}
		policy, err := resolver.ResolvePair(relaypolicy.PeerID(local.ID), relaypolicy.PeerID(id), relaypolicy.ResolveOptions{
			ReselectionGeneration: nmd.relayReselectionGeneration(local.ID, id),
		})
		if err != nil {
			blocked[id] = struct{}{}
			continue
		}
		if policy == nil {
			continue
		}
		if !local.HasCapability(nmdata.PeerCapabilityRelayPolicy) || !peer.HasCapability(nmdata.PeerCapabilityRelayPolicy) {
			policy.State, policy.Tiers = relaypolicy.RelayPolicyUnsupported, nil
			blocked[id] = struct{}{}
		}
		if components.PairRelayPolicies == nil {
			components.PairRelayPolicies = make(map[relaypolicy.PeerID]*relaypolicy.ResolvedRelayPolicy)
		}
		components.PairRelayPolicies[relaypolicy.PeerID(id)] = policy
	}
	if len(blocked) > 0 {
		nmd.withholdRelayPeers(components, blocked)
	}
}

func (nmd *NetworkMapData) relayReselectionGeneration(a, b string) uint64 {
	if b != "" && a > b {
		a, b = b, a
	}
	for _, directive := range nmd.RelayPolicies.Reselections {
		if string(directive.PeerA) == a && string(directive.PeerB) == b {
			return directive.Generation
		}
	}
	return 0
}

func (nmd *NetworkMapData) withholdRelayPeers(components *types.NetworkMapComponents, blocked map[string]struct{}) {
	if blocked == nil {
		blocked = make(map[string]struct{}, len(components.Peers)+len(components.RouterPeers))
		for id := range relayTransportPeers(components) {
			if id != components.PeerID {
				blocked[id] = struct{}{}
			}
		}
	}
	blockedReferences := maps.Clone(blocked)
	for id := range blocked {
		if peer := nmd.Peers[id]; peer != nil && peer.Key != "" {
			blockedReferences[peer.Key] = struct{}{}
		}
		delete(components.Peers, id)
		delete(components.RouterPeers, id)
	}
	components.Routes = slices.DeleteFunc(slices.Clone(components.Routes), func(route *nmdata.Route) bool {
		if route == nil {
			return false
		}
		if _, denied := blockedReferences[route.Peer]; denied {
			return true
		}
		_, denied := blockedReferences[route.PeerID]
		return denied
	})
	components.RoutersMap = maps.Clone(components.RoutersMap)
	for networkID, routers := range components.RoutersMap {
		detached := maps.Clone(routers)
		for id := range blocked {
			delete(detached, id)
		}
		components.RoutersMap[networkID] = detached
	}
}

func relayTransportPeers(components *types.NetworkMapComponents) map[string]*nmdata.Peer {
	peers := maps.Clone(components.Peers)
	if peers == nil {
		peers = make(map[string]*nmdata.Peer, len(components.RouterPeers))
	}
	for id, peer := range components.RouterPeers {
		peers[id] = peer
	}
	return peers
}
