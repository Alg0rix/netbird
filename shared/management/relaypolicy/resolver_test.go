package relaypolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolverResultsDoNotAliasSnapshot(t *testing.T) {
	input := RelayPolicyInput{AccountID: "account", Catalog: RelayCatalog{Entries: []RelayCatalogEntry{{ID: "a", Endpoint: "rels://a.example:443", Aliases: []RelayEndpoint{"rels://alias.example:443"}, Active: true}}},
		Peers:    []RelayPeer{{ID: "x", AccountID: "account"}, {ID: "y", AccountID: "account"}},
		Policies: []RelayPairPolicy{{ID: "pair", AccountID: "account", Enabled: true, Source: RelaySelector{PeerIDs: []PeerID{"x"}}, Destination: RelaySelector{PeerIDs: []PeerID{"y"}}, Constraints: RelayConstraints{Allow: &RelaySelection{RelayIDs: []RelayID{"a"}}}}}}
	resolver, err := NewResolver(input)
	require.NoError(t, err)
	first, err := resolver.ResolvePair("x", "y", ResolveOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, first.Tiers, "configured pair has permitted tiers")
	require.NotEmpty(t, first.Tiers[0][0].Aliases, "catalog alias is retained")
	original := first.Copy()
	first.Tiers[0][0].Aliases[0] = "rels://changed.example:443"
	input.Policies[0].Constraints.Allow.RelayIDs[0] = "missing"
	again, err := resolver.ResolvePair("x", "y", ResolveOptions{})
	require.NoError(t, err)
	assert.Equal(t, original, again, "mutation of caller input and previous result cannot change reusable resolver")
}
