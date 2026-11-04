package admin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func currentReport(remote rp.PeerID, policy *rp.ResolvedRelayPolicy) rp.PathReport {
	return rp.PathReport{RemotePeerID: remote, Revision: policy.Revision, Digest: policy.Digest, CatalogGeneration: policy.CatalogGeneration,
		ReselectionGeneration: policy.ReselectionGeneration, Sequence: 1, Attempt: 1, Transport: rp.TransportNativeRelay, RelayID: "a", State: rp.StateApplied}
}

func TestAuthenticatedReportsRequireBothEndpointsAndCurrentPolicy(t *testing.T) {
	m, s, _ := newManagerFixture(t)
	ctx := context.Background()
	_, err := m.Save(ctx, "account", "owner", 0, 1, pairConfiguration())
	require.NoError(t, err)
	policy, err := m.Effective(ctx, "account", "owner", "x", "y")
	require.NoError(t, err)
	for id, session := range map[string]int64{"x": 100, "y": 200} {
		require.NoError(t, s.RegisterInstance(ctx, "instance-"+id, "test"))
		connected, err := s.ConnectPeer(ctx, "account", id, "instance-"+id, session)
		require.NoError(t, err)
		require.True(t, connected, "reporting peer needs current authenticated sync ownership")
	}
	state, err := m.Status(ctx, "account", "owner", "x", "y")
	require.NoError(t, err)
	assert.Equal(t, rp.ApplicationPending, state.Application, "missing reports never mean applied")
	require.NoError(t, m.Report(ctx, "account", "x", 100, []rp.PathReport{currentReport("y", policy)}))
	state, err = m.Status(ctx, "account", "owner", "x", "y")
	require.NoError(t, err)
	assert.Equal(t, rp.ApplicationPending, state.Application, "one endpoint cannot confirm symmetric policy application")
	require.NoError(t, m.Report(ctx, "account", "y", 200, []rp.PathReport{currentReport("x", policy)}))
	state, err = m.Status(ctx, "account", "auditor", "y", "x")
	require.NoError(t, err)
	assert.Equal(t, rp.ApplicationApplied, state.Application, "matching current endpoint reports establish application")
	assert.True(t, state.Healthy, "both endpoints must observe the same active native relay attempt")
	assert.Equal(t, rp.RelayID("a"), state.RelayID, "status reports the actual pair relay")
	_, err = m.Status(ctx, "account", "service-user", "x", "y")
	assert.Error(t, err, "ordinary service users cannot read administrative telemetry")
	bad := currentReport("y", policy)
	bad.RelayID = "foreign"
	assert.Error(t, m.Report(ctx, "account", "x", 100, []rp.PathReport{bad}), "a report cannot invent an approved relay identity")
	bad = currentReport("z", policy)
	assert.Error(t, m.Report(ctx, "account", "x", 100, []rp.PathReport{bad}), "an unconfigured pair cannot borrow another pair's digest")
	bad = currentReport("y", policy)
	bad.Revision++
	assert.Error(t, m.Report(ctx, "account", "x", 100, []rp.PathReport{bad}), "an outdated or fabricated revision cannot overwrite current telemetry")
	assert.Error(t, m.Report(ctx, "account", "x", 99, []rp.PathReport{currentReport("y", policy)}), "an expired sync session cannot own a report")
}

func TestRelayStatusDisagreementBlockedApplicationAndHomeScope(t *testing.T) {
	m, s, _ := newManagerFixture(t)
	ctx := context.Background()
	_, err := m.Save(ctx, "account", "owner", 0, 1, pairConfiguration())
	require.NoError(t, err)
	account, err := s.GetAccount(ctx, "account")
	require.NoError(t, err)
	policy, err := effective(ctx, account, "x", "y")
	require.NoError(t, err)
	a, b := currentReport("y", policy), currentReport("x", policy)
	b.RelayID = "b"
	state := aggregateStatus(account, "x", "y", policy, []rp.EndpointObservation{{LocalPeerID: "x", Report: a, Current: true}, {LocalPeerID: "y", Report: b, Current: true}})
	assert.Equal(t, rp.ApplicationDisagreement, state.Application, "different permitted relays are not a healthy rendezvous")
	assert.False(t, state.Healthy, "relay authentication alone cannot prove both endpoints share a path")
	b.RelayID = "a"
	b.Attempt = 2
	state = aggregateStatus(account, "x", "y", policy, []rp.EndpointObservation{{LocalPeerID: "x", Report: a, Current: true}, {LocalPeerID: "y", Report: b, Current: true}})
	assert.Equal(t, rp.ApplicationDisagreement, state.Application, "different attempt generations cannot claim a shared native path")
	a.State, a.Transport, a.RelayID = rp.StateBlocked, rp.TransportUnknown, ""
	b = a
	b.RemotePeerID = "x"
	state = aggregateStatus(account, "x", "y", policy, []rp.EndpointObservation{{LocalPeerID: "x", Report: a, Current: true}, {LocalPeerID: "y", Report: b, Current: true}})
	assert.Equal(t, rp.ApplicationApplied, state.Application, "both endpoints may apply a policy whose transport is blocked")
	assert.False(t, state.Healthy, "applied configuration and working transport remain separate")
	a = currentReport("", policy)
	state = aggregateStatus(account, "x", "", policy, []rp.EndpointObservation{{LocalPeerID: "x", Report: a, Current: true}})
	assert.Equal(t, rp.ApplicationApplied, state.Application, "home registration needs its own single-endpoint observation")
	assert.True(t, state.Healthy, "home health is distinct from any pair status")
	state = aggregateStatus(account, "x", "y", nil, []rp.EndpointObservation{{LocalPeerID: "x", Report: a, Current: true}})
	assert.Equal(t, rp.ApplicationUnconfigured, state.Application, "clearing policy returns unconfigured status")
	assert.False(t, state.Endpoints[0].Current, "a cleared policy invalidates previous endpoint observations")
}
