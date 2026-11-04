package internal

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"

	"github.com/netbirdio/netbird/client/internal/peer"
	"github.com/netbirdio/netbird/client/internal/peerstore"
	mgmt "github.com/netbirdio/netbird/shared/management/client"
	mgmProto "github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

type cancelAwareRelayReporter struct {
	mgmt.MockClient
	entered  chan struct{}
	returned chan struct{}
}

// ReportRelayPolicyStatus holds an in-flight report until its stream context ends.
func (r *cancelAwareRelayReporter) ReportRelayPolicyStatus(ctx context.Context, _ []*mgmProto.RelayPathReport) error {
	close(r.entered)
	<-ctx.Done()
	close(r.returned)
	return ctx.Err()
}

func TestRelayPolicyReporterStopsWithManagementStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engineCtx := context.Background()
		streamCtx, cancelStream := context.WithCancel(engineCtx)
		defer cancelStream()
		reporter := &cancelAwareRelayReporter{entered: make(chan struct{}), returned: make(chan struct{})}
		status := peer.NewRecorder("management.invalid")
		engine := &Engine{ctx: engineCtx, syncMsgMux: &sync.Mutex{}, peerStore: peerstore.NewConnStore(), statusRecorder: status, mgmClient: reporter, relayPolicyDesired: map[rp.PeerID]*rp.ResolvedRelayPolicy{"remote": {State: rp.RelayPolicyReady, Digest: "desired"}}}
		engine.startRelayPolicyReporter(streamCtx)
		<-reporter.entered
		assert.NotEmpty(t, status.GetFullStatus().RelayPolicies, "active reporter must publish local observations")
		cancelStream()
		engine.shutdownWg.Wait()
		select {
		case <-reporter.returned:
		default:
			t.Fatal("management stream cancellation did not cancel the in-flight report")
		}
		assert.NoError(t, engineCtx.Err(), "normal stream completion need not cancel the entire engine")
		assert.Empty(t, status.GetFullStatus().RelayPolicies, "reporter shutdown must clear retained observations")
	})
}
