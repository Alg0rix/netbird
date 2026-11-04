package status

import (
	"testing"

	"github.com/netbirdio/netbird/client/internal/peer"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayPolicyDiagnosticsSurviveDaemonAndOutputConversion(t *testing.T) {
	home := peer.RelayPolicyObservation{DesiredRevision: 8, AppliedRevision: 7, Digest: "home", CatalogGeneration: 3, ReselectionGeneration: 2, Transport: rp.TransportUnknown, State: rp.StatePending, Reason: rp.ReasonConnecting}
	pair := peer.RelayPolicyObservation{RemotePeerKey: "withheld-peer", DesiredRevision: 8, AppliedRevision: 8, Digest: "pair", CatalogGeneration: 3, Transport: rp.TransportUnknown, State: rp.StateBlocked, Reason: rp.ReasonNoPermittedRelay}
	daemon := ToProtoFullStatus(peer.FullStatus{RelayPolicies: []peer.RelayPolicyObservation{home, pair}})
	require.Len(t, daemon.RelayPolicyStatuses, 2, "home and withheld peer diagnostics must remain visible without active peers")
	output := ConvertToStatusOutputOverview(daemon, ConvertOptions{})
	require.Len(t, output.RelayPolicies, 2, "JSON output must preserve all effective scopes")
	assert.Equal(t, uint64(7), output.RelayPolicies[0].AppliedRevision, "pending desired revision must not replace the last applied revision")
	assert.Equal(t, "blocked", output.RelayPolicies[1].State, "withheld peers must remain diagnosable as blocked")
	text := output.FullDetailSummary()
	assert.Contains(t, text, "Home registration: pending", "CLI must distinguish home registration")
	assert.Contains(t, text, "Pair withheld-peer: blocked", "CLI must retain blocked pair instructions")
	encoded, err := output.JSON()
	require.NoError(t, err)
	assert.Contains(t, encoded, `"desiredRevision":8`, "machine output must contain desired revision")
	assert.Contains(t, encoded, `"no_permitted_relay"`, "machine output must expose the bounded failure cause")
}
