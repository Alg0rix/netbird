package relaypolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/management/server/types"
)

func fixture() types.RelayPolicyInput {
	return types.RelayPolicyInput{
		AccountID: "account", Revision: 7,
		Catalog: types.RelayCatalog{Generation: 3, Entries: []types.RelayCatalogEntry{
			{ID: "a", Endpoint: "rels://a.example:443", Active: true},
			{ID: "b", Endpoint: "rels://b.example:443", Active: true},
		}},
		Groups:     []types.RelayGroup{{ID: "regional", AccountID: "account", RelayIDs: []types.RelayID{"a", "b"}}},
		PeerGroups: []types.PeerGroupID{"customers"},
		Peers: []types.RelayPeer{
			{ID: "x", AccountID: "account", GroupIDs: []types.PeerGroupID{"customers"}},
			{ID: "y", AccountID: "account"},
		},
		Policies: []types.RelayPairPolicy{{
			ID: "pair", AccountID: "account", Enabled: true, Revision: 2,
			Source:      types.RelaySelector{GroupIDs: []types.PeerGroupID{"customers"}},
			Destination: types.RelaySelector{PeerIDs: []types.PeerID{"y"}},
			Constraints: types.RelayConstraints{
				Allow: &types.RelaySelection{GroupIDs: []types.RelayGroupID{"regional"}},
			},
		}},
	}
}

func TestSymmetricResolutionAndExclusions(t *testing.T) {
	input := fixture()
	input.Policies[0].Constraints.Exclude.RelayIDs = []types.RelayID{"a"}
	forward, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	reverse, err := ResolvePair(input, "y", "x", ResolveOptions{})
	require.NoError(t, err)
	assert.Equal(t, forward, reverse, "pair orientation must not change instructions or digest")
	require.Len(t, forward.Tiers, 1, "one remaining tier should contain only relay B")
	require.Len(t, forward.Tiers[0], 1, "excluded relay A must not remain dialable")
	assert.Equal(t, types.RelayID("b"), forward.Tiers[0][0].ID, "relay B remains permitted")
	assert.Equal(t, []types.RelayID{"a"}, forward.Eliminated, "preview explains relay A's removal")
}

func TestIntersectionCannotBeOverridden(t *testing.T) {
	input := fixture()
	deny := *input.Policies[0].Copy()
	deny.ID = "deny"
	deny.Constraints.Exclude.RelayIDs = []types.RelayID{"a"}
	input.Policies[0].Constraints.Preferences = []types.RelaySelection{{RelayIDs: []types.RelayID{"a"}}}
	input.Policies = append(input.Policies, deny)
	first, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	input.Policies[0], input.Policies[1] = input.Policies[1], input.Policies[0]
	second, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	assert.Equal(t, first, second, "input order must not weaken intersection or alter digest")
	require.Len(t, first.Tiers, 1, "a preference cannot restore an excluded relay")
	assert.Equal(t, types.RelayID("b"), first.Tiers[0][0].ID, "exclusion wins over preference")
}

func TestPreferencePriorityAndStableTieBreak(t *testing.T) {
	for _, tc := range []struct {
		name     string
		priority uint32
		id       types.RelayPolicyID
		want     types.RelayID
	}{
		{name: "lower numeric priority", priority: 0, id: "z", want: "b"},
		{name: "stable ID tie break", priority: 1, id: "a", want: "b"},
		{name: "later ID loses tie", priority: 1, id: "z", want: "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := fixture()
			input.Policies[0].Priority = 1
			input.Policies[0].Constraints.Preferences = []types.RelaySelection{{RelayIDs: []types.RelayID{"a"}}}
			other := *input.Policies[0].Copy()
			other.ID, other.Priority = tc.id, tc.priority
			other.Constraints.Preferences[0].RelayIDs = []types.RelayID{"b"}
			input.Policies = append(input.Policies, other)
			resolved, err := ResolvePair(input, "x", "y", ResolveOptions{})
			require.NoError(t, err)
			require.Len(t, resolved.Tiers, 2, "unranked permitted relay belongs to a final tier")
			assert.Equal(t, tc.want, resolved.Tiers[0][0].ID, "preference winner follows priority then stable ID")
		})
	}
}

func TestAbsentEmptyAndBlocked(t *testing.T) {
	input := fixture()
	input.Policies = nil
	absent, err := ResolvePair(input, "x", "y", ResolveOptions{ForceRelay: true})
	require.NoError(t, err)
	assert.Nil(t, absent, "unconfigured pairs retain legacy transport including global settings")
	input = fixture()
	input.Policies[0].Constraints.Allow = &types.RelaySelection{}
	_, err = ResolvePair(input, "x", "y", ResolveOptions{})
	require.Error(t, err)
	input.Policies[0].Constraints.Allow = nil
	all, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	require.Len(t, all.Tiers, 1, "omitted allow selects all approved active relays")
	assert.Len(t, all.Tiers[0], 2, "omitted allow is distinct from explicit empty")
	other := *input.Policies[0].Copy()
	input.Policies[0].Constraints.Allow = &types.RelaySelection{RelayIDs: []types.RelayID{"a"}}
	other.ID = "other"
	other.Constraints.Allow = &types.RelaySelection{RelayIDs: []types.RelayID{"b"}}
	input.Policies = append(input.Policies, other)
	blocked, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.RelayPolicyBlocked, blocked.State, "disjoint restrictions must remain present and blocked")
	assert.Empty(t, blocked.Tiers, "conflicting rules cannot fall back to an unrestricted relay")
}

func TestHomeScopeAndForce(t *testing.T) {
	input := fixture()
	input.Policies = nil
	input.Profiles = []types.RelayHomeProfile{{
		ID: "home", AccountID: "account", Enabled: true,
		Selector:    types.RelaySelector{PeerIDs: []types.PeerID{"x"}},
		Constraints: types.RelayConstraints{Allow: &types.RelaySelection{RelayIDs: []types.RelayID{"a"}}},
	}}
	home, err := ResolveHome(input, "x", ResolveOptions{})
	require.NoError(t, err)
	require.NotNil(t, home)
	assert.Equal(t, types.RelayID("a"), home.Tiers[0][0].ID, "home registration is restricted to region A")
	pair, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	assert.Nil(t, pair, "a home profile cannot restrict foreign pair connections")
	input = fixture()
	forced, err := ResolvePair(input, "x", "y", ResolveOptions{ForceRelay: true})
	require.NoError(t, err)
	assert.True(t, forced.ForceRelay, "a pair rule cannot relax a global force-relay setting")
	input.Policies[0].Constraints.ForceRelay = true
	forced, err = ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	assert.True(t, forced.ForceRelay, "a matching rule can force relay for only its pair")
}

func TestReferenceValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*types.RelayPolicyInput)
	}{
		{"foreign policy", func(i *types.RelayPolicyInput) { i.Policies[0].AccountID = "other" }},
		{"foreign group", func(i *types.RelayPolicyInput) { i.Groups[0].AccountID = "other" }},
		{"foreign peer", func(i *types.RelayPolicyInput) { i.Peers[0].AccountID = "other" }},
		{"unknown relay", func(i *types.RelayPolicyInput) { i.Groups[0].RelayIDs = []types.RelayID{"unknown"} }},
		{"unknown peer group", func(i *types.RelayPolicyInput) { i.Policies[0].Source.GroupIDs = []types.PeerGroupID{"unknown"} }},
		{"self pair", func(i *types.RelayPolicyInput) { i.Policies[0].Source = i.Policies[0].Destination }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := fixture()
			tc.mutate(&input)
			assert.Error(t, Validate(input), "invalid references must be rejected before activation")
		})
	}
}

func TestDigestFencesAndCopies(t *testing.T) {
	input := fixture()
	before, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	input.Revision++
	unchanged, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	assert.Equal(t, before.Digest, unchanged.Digest, "unrelated account revisions require acknowledgment without reconnecting this pair")
	assert.NotEqual(t, before.Revision, unchanged.Revision, "desired configuration revision still advances independently")
	after, err := ResolvePair(input, "x", "y", ResolveOptions{ReselectionGeneration: 1})
	require.NoError(t, err)
	assert.NotEqual(t, before.Digest, after.Digest, "targeted retry must invalidate prior negotiation")
	input.Catalog.Generation++
	catalog, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	assert.NotEqual(t, before.Digest, catalog.Digest, "catalog publication must invalidate prior instructions")
	input.Catalog.Entries[0].Active = false
	retired, err := ResolvePair(input, "x", "y", ResolveOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.RelayID("b"), retired.Tiers[0][0].ID, "retirement removes A without invalidating stored references")
	copied := input.Policies[0].Copy()
	copied.Constraints.Allow.GroupIDs[0] = "other"
	assert.Equal(t, types.RelayGroupID("regional"), input.Policies[0].Constraints.Allow.GroupIDs[0], "copies must not alias account state")
}
