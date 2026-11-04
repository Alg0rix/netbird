package relaypolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/management/server/types"
)

func TestCatalogCanonicalIdentity(t *testing.T) {
	entry := types.RelayCatalogEntry{
		ID: "a", Active: true, Endpoint: "rels://A.EXAMPLE.:00443",
		InstanceEndpoint: "rels://instance.example:443",
		Aliases:          []types.RelayEndpoint{"rels://[::ffff:192.0.2.1]:443"},
	}
	catalog, err := ValidateCatalog(types.RelayCatalog{Entries: []types.RelayCatalogEntry{entry}})
	require.NoError(t, err)
	assert.Equal(t, types.RelayEndpoint("rels://a.example:443"), catalog.Entries[0].Endpoint, "normalize host casing, trailing dot and numeric port")
	assert.Equal(t, types.RelayEndpoint("rels://192.0.2.1:443"), catalog.Entries[0].Aliases[0], "mapped IPs must normalize before identity checks")
	assert.NoError(t, ValidateInstance(entry, "rels://192.0.2.1:443"))
	assert.Error(t, ValidateInstance(entry, "rels://foreign.example:443"))
	other := types.RelayCatalogEntry{ID: "b", Endpoint: "rels://192.0.2.1:443", Active: true}
	_, err = ValidateCatalog(types.RelayCatalog{Entries: []types.RelayCatalogEntry{entry, other}})
	assert.Error(t, err)
}

func TestInvalidCatalogEndpoints(t *testing.T) {
	for _, endpoint := range []types.RelayEndpoint{
		"https://a.example:443", "rel://", "rels://user:password@a.example:443",
		"rels://a.example:443/path", "rels://a.example:443?token=secret", "rels://a.example:443#fragment",
		"rels://a.example:0", "rels://a.example:65536", "rels://a.example:", "rels://[fe80::1%25eth0]:443",
	} {
		t.Run(string(endpoint), func(t *testing.T) {
			_, err := CanonicalEndpoint(endpoint)
			assert.Error(t, err)
		})
	}
}

func TestBootstrapRequiresExplicitApproval(t *testing.T) {
	first, err := BootstrapCatalog([]string{"rel://a.example:80", "rels://b.example:443"}, nil)
	require.NoError(t, err)
	second, err := BootstrapCatalog([]string{"rels://b.example:443", "rel://a.example:80"}, nil)
	require.NoError(t, err)
	assert.Equal(t, first, second, "legacy IDs remain stable across reordering and restarts")
	input := fixture()
	input.Catalog = first
	input.Groups[0].RelayIDs = []types.RelayID{first.Entries[0].ID}
	assert.Error(t, Validate(input), "bootstrapped addresses cannot silently become approved policy identities")
	explicit := types.RelayCatalogEntry{ID: "approved", Endpoint: "rels://b.example:443", Active: true}
	approved, err := BootstrapCatalog([]string{"rels://b.example:443"}, []types.RelayCatalogEntry{explicit})
	require.NoError(t, err)
	assert.Len(t, approved.Entries, 1, "explicit approval replaces rather than duplicates a legacy address")
	assert.False(t, approved.Entries[0].Legacy, "explicit stable identity is eligible for account policies")
}

func TestLegacyAdvertisementDoesNotRequireManagedCanonicalURLs(t *testing.T) {
	catalog, err := BootstrapCatalog([]string{"rels://legacy.example:443/", "rels://valid.example:443"}, nil)
	require.NoError(t, err)
	assert.Len(t, catalog.Entries, 1, "legacy noncanonical addresses remain outside managed policy without blocking startup")
	_, err = BootstrapCatalog(nil, []types.RelayCatalogEntry{{ID: "explicit", Endpoint: "rels://legacy.example:443/", Active: true}})
	assert.Error(t, err)
}
