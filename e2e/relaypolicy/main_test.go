//go:build e2e

// Package relaypolicy verifies observed policy enforcement with disposable Docker peers.
package relaypolicy

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/netbirdio/netbird/e2e/harness"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

var server *harness.Combined
var relays map[rp.RelayID]*harness.Relay
var signal *harness.Signal

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var err error
	server, err = harness.StartCombined(ctx, harness.WithRelayCatalog(harness.ControlledRelayCatalog()), harness.WithSignalAddress("http://signal.local:10000"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "start relay acceptance Management: %v\n", err)
		return 1
	}
	defer cleanupServer(server)
	if _, err = server.Bootstrap(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap relay acceptance Management: %v\n", err)
		return 1
	}
	signal, err = harness.StartSignal(ctx, server, "signal.local")
	if err != nil {
		fmt.Fprintf(os.Stderr, "start isolated signal: %v\n", err)
		return 1
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := signal.Terminate(cleanupCtx); err != nil {
			fmt.Fprintf(os.Stderr, "terminate signal: %v\n", err)
		}
	}()
	relays = make(map[rp.RelayID]*harness.Relay)
	defer func() {
		for name, relay := range relays {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := relay.Terminate(cleanupCtx); err != nil {
				fmt.Fprintf(os.Stderr, "terminate %s: %v\n", name, err)
			}
			cancel()
		}
	}()
	for _, name := range []string{"relay-a", "relay-b", "relay-c"} {
		relay, startErr := harness.StartRelay(ctx, server, name)
		if startErr != nil {
			fmt.Fprintf(os.Stderr, "start %s: %v\n", name, startErr)
			return 1
		}
		relays[rp.RelayID(name)] = relay
	}
	return m.Run()
}

func cleanupServer(s *harness.Combined) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.Terminate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "terminate Management: %v\n", err)
	}
}
