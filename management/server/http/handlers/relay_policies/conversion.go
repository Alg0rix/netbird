package relaypolicies

import (
	"github.com/netbirdio/netbird/shared/management/http/api"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func stringsFromIDs[T ~string](ids []T) []string {
	result := make([]string, len(ids))
	for i, id := range ids {
		result[i] = string(id)
	}
	return result
}

func idsFromStrings[T ~string](ids []string) []T {
	result := make([]T, len(ids))
	for i, id := range ids {
		result[i] = T(id)
	}
	return result
}

func catalogEntry(entry rp.RelayCatalogEntry) api.RelayCatalogItem {
	return api.RelayCatalogItem{Id: string(entry.ID), Endpoint: string(entry.Endpoint), InstanceEndpoint: string(entry.InstanceEndpoint), Aliases: stringsFromIDs(entry.Aliases), Active: entry.Active, Legacy: entry.Legacy}
}

func selection(selection rp.RelaySelection) api.RelaySelection {
	return api.RelaySelection{RelayIds: stringsFromIDs(selection.RelayIDs), GroupIds: stringsFromIDs(selection.GroupIDs)}
}

func fromSelection(selection api.RelaySelection) rp.RelaySelection {
	return rp.RelaySelection{RelayIDs: idsFromStrings[rp.RelayID](selection.RelayIds), GroupIDs: idsFromStrings[rp.RelayGroupID](selection.GroupIds)}
}

func selector(selector rp.RelaySelector) api.RelayPeerSelector {
	return api.RelayPeerSelector{PeerIds: stringsFromIDs(selector.PeerIDs), GroupIds: stringsFromIDs(selector.GroupIDs)}
}

func fromSelector(selector api.RelayPeerSelector) rp.RelaySelector {
	return rp.RelaySelector{PeerIDs: idsFromStrings[rp.PeerID](selector.PeerIds), GroupIDs: idsFromStrings[rp.PeerGroupID](selector.GroupIds)}
}

func constraints(value rp.RelayConstraints) api.RelayConstraints {
	result := api.RelayConstraints{Exclude: selection(value.Exclude), ForceRelay: value.ForceRelay, Preferences: make([]api.RelaySelection, len(value.Preferences))}
	if value.Allow != nil {
		allow := selection(*value.Allow)
		result.Allow = &allow
	}
	for i, tier := range value.Preferences {
		result.Preferences[i] = selection(tier)
	}
	return result
}

func fromConstraints(value api.RelayConstraints) rp.RelayConstraints {
	result := rp.RelayConstraints{Exclude: fromSelection(value.Exclude), ForceRelay: value.ForceRelay, Preferences: make([]rp.RelaySelection, len(value.Preferences))}
	if value.Allow != nil {
		allow := fromSelection(*value.Allow)
		result.Allow = &allow
	}
	for i, tier := range value.Preferences {
		result.Preferences[i] = fromSelection(tier)
	}
	return result
}

func group(value rp.RelayGroup) api.RelayGroup {
	return api.RelayGroup{Id: string(value.ID), Name: value.Name, Revision: &value.Revision, RelayIds: stringsFromIDs(value.RelayIDs)}
}

func fromGroup(value api.RelayGroup, account rp.AccountID) rp.RelayGroup {
	return rp.RelayGroup{ID: rp.RelayGroupID(value.Id), AccountID: account, Name: value.Name, RelayIDs: idsFromStrings[rp.RelayID](value.RelayIds)}
}

func profile(value rp.RelayHomeProfile) api.RelayHomeProfile {
	return api.RelayHomeProfile{Id: string(value.ID), Name: value.Name, Revision: &value.Revision, Priority: value.Priority, Enabled: value.Enabled, Selector: selector(value.Selector), Constraints: constraints(value.Constraints)}
}

func fromProfile(value api.RelayHomeProfile, account rp.AccountID) rp.RelayHomeProfile {
	return rp.RelayHomeProfile{ID: rp.RelayProfileID(value.Id), AccountID: account, Name: value.Name, Priority: value.Priority, Enabled: value.Enabled, Selector: fromSelector(value.Selector), Constraints: fromConstraints(value.Constraints)}
}

func pairPolicy(value rp.RelayPairPolicy) api.RelayPairPolicy {
	return api.RelayPairPolicy{Id: string(value.ID), Name: value.Name, Revision: &value.Revision, Priority: value.Priority, Enabled: value.Enabled, Source: selector(value.Source), Destination: selector(value.Destination), Constraints: constraints(value.Constraints)}
}

func fromPairPolicy(value api.RelayPairPolicy, account rp.AccountID) rp.RelayPairPolicy {
	return rp.RelayPairPolicy{ID: rp.PolicyID(value.Id), AccountID: account, Name: value.Name, Priority: value.Priority, Enabled: value.Enabled, Source: fromSelector(value.Source), Destination: fromSelector(value.Destination), Constraints: fromConstraints(value.Constraints)}
}

func configuration(value rp.Configuration, generation uint64) api.RelayPolicyConfiguration {
	result := api.RelayPolicyConfiguration{Revision: value.Revision, CatalogGeneration: generation, Supported: true, Groups: make([]api.RelayGroup, len(value.Groups)), HomeProfiles: make([]api.RelayHomeProfile, len(value.Profiles)), PairPolicies: make([]api.RelayPairPolicy, len(value.Policies))}
	for i, value := range value.Groups {
		result.Groups[i] = group(value)
	}
	for i, value := range value.Profiles {
		result.HomeProfiles[i] = profile(value)
	}
	for i, value := range value.Policies {
		result.PairPolicies[i] = pairPolicy(value)
	}
	return result
}

func fromConfiguration(value api.RelayPolicyConfiguration, account rp.AccountID) rp.Configuration {
	result := rp.Configuration{Revision: value.Revision, Groups: make([]rp.RelayGroup, len(value.Groups)), Profiles: make([]rp.RelayHomeProfile, len(value.HomeProfiles)), Policies: make([]rp.RelayPairPolicy, len(value.PairPolicies))}
	for i, value := range value.Groups {
		result.Groups[i] = fromGroup(value, account)
	}
	for i, value := range value.HomeProfiles {
		result.Profiles[i] = fromProfile(value, account)
	}
	for i, value := range value.PairPolicies {
		result.Policies[i] = fromPairPolicy(value, account)
	}
	return result
}

func effective(value *rp.ResolvedRelayPolicy) *api.RelayEffectivePolicy {
	if value == nil {
		return nil
	}
	result := &api.RelayEffectivePolicy{State: api.RelayEffectivePolicyState(value.State), Revision: value.Revision, CatalogGeneration: value.CatalogGeneration, ReselectionGeneration: value.ReselectionGeneration, Digest: string(value.Digest), ForceRelay: value.ForceRelay, PreferenceRule: string(value.PreferenceRule), Eliminated: stringsFromIDs(value.Eliminated), Tiers: make([][]api.RelayCatalogItem, len(value.Tiers)), Contributors: make([]api.RelayPolicyContribution, len(value.Contributors))}
	for i, tier := range value.Tiers {
		result.Tiers[i] = make([]api.RelayCatalogItem, len(tier))
		for j, entry := range tier {
			result.Tiers[i][j] = catalogEntry(entry)
		}
	}
	for i, contribution := range value.Contributors {
		result.Contributors[i] = api.RelayPolicyContribution{Id: string(contribution.ID), Revision: contribution.Revision, Eliminated: stringsFromIDs(contribution.Eliminated)}
	}
	return result
}
