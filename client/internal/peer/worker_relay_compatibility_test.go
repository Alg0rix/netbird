package peer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	sproto "github.com/netbirdio/netbird/shared/signal/proto"
)

func TestRelayPolicyWithoutWorkerOnlyAcceptsLegacySignals(t *testing.T) {
	for name, worker := range map[string]*WorkerRelay{"absent": nil, "unmanaged": {}} {
		t.Run(name, func(t *testing.T) {
			assert.True(t, worker.acceptPolicy(&OfferAnswer{}), "legacy signaling must remain available without managed relay state")
			assert.False(t, worker.acceptPolicy(&OfferAnswer{RelayPolicyAttempt: &sproto.RelayPolicyAttempt{Sequence: 1}}), "a managed attempt must not bypass missing policy state")
		})
	}
}
