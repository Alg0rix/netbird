package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/management/server/types"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func TestRelayPolicyUpdateFencesSurviveClear(t *testing.T) {
	s, account := newRelayStore(t)
	ctx := context.Background()
	serials, err := s.GetRelayPolicyAccountSerials(ctx)
	require.NoError(t, err)
	assert.NotContains(t, serials, types.AccountID(account.Id), "never-configured account needs no policy refresh")
	saved, err := s.SaveRelayConfiguration(ctx, types.AccountID(account.Id), 0, 1, relayTestConfiguration())
	require.NoError(t, err)
	before, err := s.GetRelayPolicyAccountSerials(ctx)
	require.NoError(t, err)
	require.Contains(t, before, types.AccountID(account.Id), "configured account is observed across replicas")
	_, err = s.SaveRelayConfiguration(ctx, types.AccountID(account.Id), saved.Revision, 1, rp.Configuration{})
	require.NoError(t, err)
	cleared, err := s.GetRelayPolicyAccountSerials(ctx)
	require.NoError(t, err)
	assert.Greater(t, cleared[types.AccountID(account.Id)], before[types.AccountID(account.Id)], "clear retains an advanced invalidation fence")
}
