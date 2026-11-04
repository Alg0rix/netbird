package relaypolicy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogEnforcesWireIdentityBounds(t *testing.T) {
	entry := RelayCatalogEntry{ID: "relay", Endpoint: "rels://relay.example:443", Active: true}
	for i := range MaxRelayAliases {
		entry.Aliases = append(entry.Aliases, RelayEndpoint(fmt.Sprintf("rels://alias%d.example:443", i)))
	}
	accepted, err := ValidateCatalog(RelayCatalog{Entries: []RelayCatalogEntry{entry}})
	require.NoError(t, err)
	assert.Len(t, accepted.Entries[0].Aliases, MaxRelayAliases, "maximum supported aliases must remain publishable")
	entry.Aliases = append(entry.Aliases, "rels://overflow.example:443")
	_, err = ValidateCatalog(RelayCatalog{Entries: []RelayCatalogEntry{entry}})
	assert.ErrorContains(t, err, "alias limit", "operator publication must reject instructions clients cannot decode")
	_, err = ValidateCatalog(RelayCatalog{Entries: make([]RelayCatalogEntry, MaxCatalogEntries+1)})
	assert.ErrorContains(t, err, "entry limit", "catalog size and wire instruction limit must agree")
	_, err = CanonicalEndpoint(RelayEndpoint(strings.Repeat("x", MaxRelayEndpointLength+1)))
	assert.ErrorContains(t, err, "length limit", "oversized endpoints must be rejected before URL parsing")
	entry.Aliases = nil
	entry.ID = RelayID(strings.Repeat("x", MaxRelayIDLength+1))
	_, err = ValidateCatalog(RelayCatalog{Entries: []RelayCatalogEntry{entry}})
	assert.Error(t, err)
}
