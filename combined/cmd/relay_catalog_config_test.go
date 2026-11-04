package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func TestCombinedRelayCatalogReachesManagement(t *testing.T) {
	var config CombinedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`server:
  exposedAddress: "http://management.example:8080"
  authSecret: "disposable-test-secret"
  relays:
    addresses: ["rel://relay-a:33080"]
    catalog:
      - id: relay-a
        endpoint: "rel://relay-a:33080"
        instanceEndpoint: "rel://relay-a-instance:33080"
        aliases: ["rel://relay-a-alias:33080"]
        active: true
`), &config))
	config.ApplySimplifiedDefaults()
	result, err := buildRelayConfig(config.Management.Relays)
	require.NoError(t, err)
	require.Len(t, result.Catalog, 1)
	assert.Equal(t, rp.RelayID("relay-a"), result.Catalog[0].ID, "stable operator identity reaches management bootstrap")
	assert.Equal(t, rp.RelayEndpoint("rel://relay-a-instance:33080"), result.Catalog[0].InstanceEndpoint, "concrete instance mapping survives combined config adaptation")
	assert.Equal(t, []rp.RelayEndpoint{"rel://relay-a-alias:33080"}, result.Catalog[0].Aliases, "approved aliases survive combined config adaptation")
	assert.True(t, result.Catalog[0].Active, "explicit active relay remains selectable")
	config.Management.Relays.Catalog[0].Aliases[0] = "rel://mutated:33080"
	assert.Equal(t, rp.RelayEndpoint("rel://relay-a-alias:33080"), result.Catalog[0].Aliases[0], "management catalog must not share mutable config aliases")
}

func TestCombinedRelayCatalogRejectsAmbiguousInstances(t *testing.T) {
	_, err := buildRelayConfig(RelaysConfig{Catalog: []RelayCatalogEntryConfig{
		{ID: "relay-a", Endpoint: "rel://relay-a:33080", Active: true},
		{ID: "relay-b", Endpoint: "rel://relay-b:33080", Aliases: []rp.RelayEndpoint{"rel://relay-a:33080"}, Active: true},
	}})
	assert.ErrorContains(t, err, "ambiguous", "combined config must not widen an ambiguous approved relay mapping")
}
