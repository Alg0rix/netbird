package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nbconfig "github.com/netbirdio/netbird/management/internals/server/config"
	"github.com/netbirdio/netbird/management/server/types"
)

func TestRelayCatalogPublishRequiresPreviewGeneration(t *testing.T) {
	command := newRelayCatalogCommand()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"publish"})
	err := command.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected-generation", "publication must not infer a generation and overwrite an unseen change")
}

func TestConfiguredRelayCatalogPreservesExplicitIdentity(t *testing.T) {
	entry := types.RelayCatalogEntry{ID: "region-a", Endpoint: "rels://a.example:443", Active: true}
	catalog, err := configuredRelayCatalog(&nbconfig.Relay{
		Addresses: []string{"rels://a.example:443", "rels://legacy.example:443"},
		Catalog:   []types.RelayCatalogEntry{entry},
		Secret:    "must-not-appear-in-catalog",
	})
	require.NoError(t, err)
	require.Len(t, catalog.Entries, 2)
	var approved, legacy int
	for _, relay := range catalog.Entries {
		if relay.Legacy {
			legacy++
			continue
		}
		approved++
		assert.Equal(t, entry.ID, relay.ID, "config explicitly binds the stable managed identity")
	}
	assert.Equal(t, 1, approved, "an existing advertisement must not duplicate an approved relay")
	assert.Equal(t, 1, legacy, "unapproved addresses remain available to legacy selection")
}
