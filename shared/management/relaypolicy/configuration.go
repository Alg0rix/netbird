package relaypolicy

import "reflect"

// NextConfiguration assigns revisions identically for previews and committed writes.
func NextConfiguration(current, candidate Configuration) Configuration {
	candidate = candidate.Copy()
	candidate.Revision = current.Revision + 1
	advanceEntityRevisions(&candidate, current)
	return candidate
}

func advanceEntityRevisions(candidate *Configuration, current Configuration) {
	groups := make(map[RelayGroupID]RelayGroup, len(current.Groups))
	for _, group := range current.Groups {
		groups[group.ID] = group
	}
	for i, group := range candidate.Groups {
		old, exists := groups[group.ID]
		group.Revision = old.Revision
		if !exists || !reflect.DeepEqual(group, old) {
			group.Revision++
		}
		candidate.Groups[i] = group
	}
	profiles := make(map[RelayProfileID]RelayHomeProfile, len(current.Profiles))
	for _, profile := range current.Profiles {
		profiles[profile.ID] = profile
	}
	for i, profile := range candidate.Profiles {
		old, exists := profiles[profile.ID]
		profile.Revision = old.Revision
		if !exists || !reflect.DeepEqual(profile, old) {
			profile.Revision++
		}
		candidate.Profiles[i] = profile
	}
	policies := make(map[PolicyID]RelayPairPolicy, len(current.Policies))
	for _, policy := range current.Policies {
		policies[policy.ID] = policy
	}
	for i, policy := range candidate.Policies {
		old, exists := policies[policy.ID]
		policy.Revision = old.Revision
		if !exists || !reflect.DeepEqual(policy, old) {
			policy.Revision++
		}
		candidate.Policies[i] = policy
	}
}
