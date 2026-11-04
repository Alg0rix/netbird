package types

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountCopyPreservesAbsentRelayPolicy(t *testing.T) {
	account := &Account{Network: NewNetwork(), Settings: &Settings{}}
	copied := account.Copy()
	assert.Nil(t, copied.RelayGroups, "absent relay groups remain absent")
	assert.Nil(t, copied.RelayHomeProfiles, "absent home profiles remain absent")
	assert.Nil(t, copied.RelayPairPolicies, "absent pair policies remain absent")
	assert.Nil(t, copied.RelayReselections, "absent retry directives remain absent")
	assert.Nil(t, copied.RelayState, "copying an unconfigured account cannot invent policy state")
	assert.Nil(t, copied.RelayCatalog, "copying an account cannot invent an attached catalog")
	account.RelayGroups = []RelayGroup{}
	account.RelayHomeProfiles = []RelayHomeProfile{}
	account.RelayPairPolicies = []RelayPairPolicy{}
	account.RelayReselections = []RelayReselection{}
	account.RelayCatalog = &RelayCatalog{Entries: []RelayCatalogEntry{}}
	copied = account.Copy()
	assert.NotNil(t, copied.RelayGroups, "explicit empty groups retain their shape")
	assert.NotNil(t, copied.RelayHomeProfiles, "explicit empty home profiles retain their shape")
	assert.NotNil(t, copied.RelayPairPolicies, "explicit empty pair policies retain their shape")
	assert.NotNil(t, copied.RelayReselections, "explicit empty directives retain their shape")
	require.NotNil(t, copied.RelayCatalog, "explicit attached catalog remains attached")
	assert.NotNil(t, copied.RelayCatalog.Entries, "an explicitly attached empty catalog retains its shape")
}
