package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/management/server/types"
)

type blockingRelaySerialReader struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (r *blockingRelaySerialReader) GetRelayPolicyAccountSerials(ctx context.Context) (map[types.AccountID]uint64, error) {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	return nil, ctx.Err()
}

func TestRelayPolicyWatcherStopCancelsAndWaits(t *testing.T) {
	reader := &blockingRelaySerialReader{started: make(chan struct{}), cancelled: make(chan struct{})}
	controller := &Controller{relayPolicySerials: reader}
	stop := controller.StartRelayPolicyUpdates(context.Background())
	t.Cleanup(stop)
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("watcher must read on first observation without waiting for poll interval")
	}
	var callers sync.WaitGroup
	for range 2 {
		callers.Add(1)
		go func() { defer callers.Done(); stop() }()
	}
	callers.Wait()
	select {
	case <-reader.cancelled:
	default:
		t.Fatal("stop must wait for in-flight store read cancellation")
	}
	require.NotPanics(t, stop, "repeated shutdown remains safe")
}
