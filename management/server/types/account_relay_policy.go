package types

import "github.com/netbirdio/netbird/shared/management/relaypolicy"

// RelaySnapshot returns the account's relay policy data without copying its slices.
// Call Copy before mutating a snapshot taken from a shared account.
func (a *Account) RelaySnapshot() relaypolicy.Snapshot {
	snapshot := relaypolicy.Snapshot{AccountID: AccountID(a.Id),
		Configuration: relaypolicy.Configuration{Groups: a.RelayGroups, Profiles: a.RelayHomeProfiles, Policies: a.RelayPairPolicies},
		Reselections:  a.RelayReselections,
	}
	if a.Network != nil {
		snapshot.NetworkSerial = a.Network.Serial
	}
	if a.RelayState != nil {
		snapshot.Revision = a.RelayState.Revision
	}
	if a.RelayCatalog != nil {
		snapshot.Catalog = *a.RelayCatalog
	}
	return snapshot
}

// RelayPolicyInput builds the resolver input from the account's peer and relay relations.
func (a *Account) RelayPolicyInput() RelayPolicyInput {
	snapshot := a.RelaySnapshot()
	input := RelayPolicyInput{AccountID: AccountID(a.Id), Catalog: snapshot.Catalog,
		Groups: snapshot.Groups, Profiles: snapshot.Profiles, Policies: snapshot.Policies, Revision: snapshot.Revision}
	memberships := make(map[PeerID][]PeerGroupID)
	for _, group := range a.Groups {
		input.PeerGroups = append(input.PeerGroups, PeerGroupID(group.ID))
		for _, id := range group.Peers {
			memberships[PeerID(id)] = append(memberships[PeerID(id)], PeerGroupID(group.ID))
		}
	}
	for id := range a.Peers {
		input.Peers = append(input.Peers, RelayPeer{ID: PeerID(id), AccountID: AccountID(a.Id), GroupIDs: memberships[PeerID(id)]})
	}
	return input
}
