package client

import (
	"context"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/client/iface"
	"github.com/netbirdio/netbird/client/netevents/sweep"
	"github.com/netbirdio/netbird/relay/server"
)

const lifecycleTestTimeout = 15 * time.Second

// stubNetEvents lets a test step the guard: quick reconnects pass without the
// production settle delay, or park until the manager context ends.
type stubNetEvents struct {
	dials atomic.Int32

	mu      sync.Mutex
	hold    bool
	entered chan struct{}
}

// relayProxy forwards TCP connections to a relay server and counts them, so a
// test can see every connection a manager holds, including ones it lost track of.
// While paused it accepts connections without forwarding them, which keeps a
// relay handshake in flight for as long as the test needs.
type relayProxy struct {
	addr   string
	target string

	mu       sync.Mutex
	live     map[net.Conn]struct{}
	accepted int
	resumed  chan struct{}
}

func newStubNetEvents() *stubNetEvents {
	return &stubNetEvents{entered: make(chan struct{}, 8)}
}

func startRelayProxy(t *testing.T, target string) *relayProxy {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listen for the relay proxy")

	resumed := make(chan struct{})
	close(resumed)
	p := &relayProxy{
		addr:    ln.Addr().String(),
		target:  target,
		live:    make(map[net.Conn]struct{}),
		resumed: resumed,
	}
	go p.serve(ln)
	t.Cleanup(func() {
		_ = ln.Close()
		p.closeAll()
	})
	return p
}

func (n *stubNetEvents) Wait(context.Context) (bool, error) {
	return false, nil
}

func (n *stubNetEvents) IsOnline() bool {
	return true
}

func (n *stubNetEvents) WaitSettled(ctx context.Context, _, _ time.Duration) bool {
	n.mu.Lock()
	hold := n.hold
	n.mu.Unlock()
	if !hold {
		return ctx.Err() == nil
	}

	n.entered <- struct{}{}
	<-ctx.Done()
	return false
}

func (n *stubNetEvents) StartDial(ctx context.Context) *sweep.Dial {
	n.dials.Add(1)
	return (*sweep.Sweeper)(nil).StartDial(ctx)
}

func (n *stubNetEvents) holdQuickReconnect() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hold = true
}

func (p *relayProxy) serve(ln net.Listener) {
	for {
		client, err := ln.Accept()
		if err != nil {
			return
		}

		p.mu.Lock()
		p.accepted++
		p.live[client] = struct{}{}
		resumed := p.resumed
		p.mu.Unlock()

		go p.pipe(client, resumed)
	}
}

func (p *relayProxy) pipe(client net.Conn, resumed <-chan struct{}) {
	defer func() {
		_ = client.Close()
		p.mu.Lock()
		delete(p.live, client)
		p.mu.Unlock()
	}()

	<-resumed
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		return
	}
	defer func() {
		_ = upstream.Close()
	}()

	done := make(chan struct{}, 2)
	forward := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go forward(upstream, client)
	go forward(client, upstream)
	<-done
}

func (p *relayProxy) pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resumed = make(chan struct{})
}

func (p *relayProxy) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	close(p.resumed)
}

func (p *relayProxy) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for conn := range p.live {
		_ = conn.Close()
	}
}

func (p *relayProxy) liveConns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.live)
}

func (p *relayProxy) acceptedConns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepted
}

func TestHomeRecoversAfterSessionDrop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	relay := startRelayServer(t)
	bob := serveManager(t, ctx, relay, "bob")
	alice := serveManager(t, ctx, relay, "alice", WithNetEvents(newStubNetEvents()))

	first := homeSession(t, alice)
	dropSession(t, first)

	require.NoError(t, waitForReady(ctx, alice, lifecycleTestTimeout), "the manager must recover a dropped home session")
	nextHomeSession(t, alice, first)

	relayAddr, _, err := alice.RelayInstanceAddress()
	require.NoError(t, err, "home relay address after the recovery")
	assertRelayedTraffic(t, ctx, alice, bob, relayAddr)
}

func TestHomeRecoversOnNewServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	oldRelay := startRelayServer(t)
	newRelay := startRelayServer(t)
	bob := serveManager(t, ctx, newRelay, "bob")
	alice := serveManager(t, ctx, oldRelay, "alice", WithNetEvents(newStubNetEvents()))

	first := homeSession(t, alice)
	// The old server is no longer a valid home, so the manager has to pick again.
	alice.UpdateServerURLs(toURL(newRelay))
	dropSession(t, first)

	require.NoError(t, waitForReady(ctx, alice, lifecycleTestTimeout), "the manager must recover on a newly picked server")
	recovered := nextHomeSession(t, alice, first)
	assert.Equal(t, toURL(newRelay)[0], recovered.client.connectionURL, "the recovered home must be the new server")

	relayAddr, _, err := alice.RelayInstanceAddress()
	require.NoError(t, err, "home relay address after the recovery")
	assertRelayedTraffic(t, ctx, alice, bob, relayAddr)
}

func TestForeignSessionEndRemovesTrack(t *testing.T) {
	tests := []struct {
		name string
		drop func(t *testing.T, ctx context.Context, alice *Manager, bobsRelay string)
	}{
		{
			name: "session ends after the track is published",
			drop: func(t *testing.T, ctx context.Context, alice *Manager, bobsRelay string) {
				_, err := alice.OpenConn(ctx, bobsRelay, "bob", netip.Addr{})
				require.NoError(t, err, "alice must reach bob over bob's relay")

				dropSession(t, trackSession(t, foreignTrack(t, alice, bobsRelay)))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			bob := serveManager(t, ctx, startRelayServer(t), "bob")
			alice := NewManager(ctx, toURL(startRelayServer(t)), "alice", iface.DefaultMTU)
			// Keep the idle cleanup out of the way: the track must go on its own.
			alice.cleanupInterval = time.Hour
			require.NoError(t, alice.Serve(), "serve manager of alice")

			bobsRelay, _, err := bob.RelayInstanceAddress()
			require.NoError(t, err, "bob's relay address")

			tt.drop(t, ctx, alice, bobsRelay)

			require.Eventually(t, func() bool {
				alice.relayClientsMutex.RLock()
				defer alice.relayClientsMutex.RUnlock()
				_, tracked := alice.relayClients[bobsRelay]
				return !tracked
			}, lifecycleTestTimeout, 5*time.Millisecond, "the track of an ended foreign session must be removed without the idle cleanup")

			assertRelayedTraffic(t, ctx, alice, bob, bobsRelay)
			assert.True(t, trackSession(t, foreignTrack(t, alice, bobsRelay)).client.Ready(), "the next OpenConn must run on a fresh foreign connection")
		})
	}
}

func TestOpenConnReplacesEndedForeignTrack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	bob := serveManager(t, ctx, startRelayServer(t), "bob")
	alice := NewManager(ctx, toURL(startRelayServer(t)), "alice", iface.DefaultMTU)
	alice.cleanupInterval = time.Hour
	require.NoError(t, alice.Serve(), "serve manager of alice")

	bobsRelay, _, err := bob.RelayInstanceAddress()
	require.NoError(t, err, "bob's relay address")

	foreign := NewClient(bobsRelay, alice.tokenStore, alice.peerID, alice.mtu)
	s, err := foreign.ConnectSession(ctx)
	require.NoError(t, err, "connect to the foreign relay")
	dropSession(t, s)

	// Published without its watcher, the way OpenConn finds a track whose
	// session ended but whose eviction has not run yet.
	stale := NewRelayTrack()
	stale.relayClient = foreign
	close(stale.ready)
	alice.relayClientsMutex.Lock()
	alice.relayClients[bobsRelay] = stale
	alice.relayClientsMutex.Unlock()

	_, err = alice.OpenConn(ctx, bobsRelay, "bob", netip.Addr{})
	require.NoError(t, err, "OpenConn must redial instead of failing on the ended track")

	fresh := foreignTrack(t, alice, bobsRelay)
	assert.NotSame(t, stale, fresh, "the ended track must be replaced")
	assert.True(t, trackSession(t, fresh).client.Ready(), "the replacement track must hold a live session")
}

func startRelayServer(t *testing.T) server.ListenerConfig {
	t.Helper()

	address, _ := freeAddr(t)
	cfg := server.ListenerConfig{Address: address}
	srv, err := server.NewServer(newManagerTestServerConfig(cfg.Address))
	require.NoError(t, err, "create relay server")

	errChan := make(chan error, 1)
	go func() {
		if err := srv.Listen(cfg); err != nil {
			errChan <- err
		}
	}()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
	})
	require.NoError(t, waitForServerToStart(errChan), "start relay server")
	return cfg
}

func serveManager(t *testing.T, ctx context.Context, relay server.ListenerConfig, peerID string, opts ...ManagerOption) *Manager {
	t.Helper()

	m := NewManager(ctx, toURL(relay), peerID, iface.DefaultMTU, opts...)
	require.NoError(t, m.Serve(), "serve manager of %s", peerID)
	return m
}

func homeSession(t *testing.T, m *Manager) *session {
	t.Helper()

	m.relayClientMu.RLock()
	home := m.relayClient
	m.relayClientMu.RUnlock()
	require.NotNil(t, home, "the manager has no home relay client")

	s := home.CurrentSession()
	require.NotNil(t, s, "the home relay client has no session")
	return s
}

func nextHomeSession(t *testing.T, m *Manager, prev *session) *session {
	t.Helper()

	var next *session
	require.Eventually(t, func() bool {
		m.relayClientMu.RLock()
		home := m.relayClient
		m.relayClientMu.RUnlock()

		next = home.CurrentSession()
		return next != nil && next != prev
	}, lifecycleTestTimeout, 5*time.Millisecond, "the manager did not establish a new home session")
	return next
}

func foreignTrack(t *testing.T, m *Manager, serverAddress string) *RelayTrack {
	t.Helper()

	m.relayClientsMutex.RLock()
	rt, tracked := m.relayClients[serverAddress]
	m.relayClientsMutex.RUnlock()
	require.True(t, tracked, "the foreign relay %s must be tracked", serverAddress)
	return rt
}

func trackSession(t *testing.T, rt *RelayTrack) *session {
	t.Helper()

	rt.RLock()
	relayClient := rt.relayClient
	rt.RUnlock()
	require.NotNil(t, relayClient, "the foreign relay track has no client")

	s := relayClient.CurrentSession()
	require.NotNil(t, s, "the foreign relay client has no session")
	return s
}

func assertRelayedTraffic(t *testing.T, ctx context.Context, sender, receiver *Manager, relayAddr string) {
	t.Helper()

	out, err := sender.OpenConn(ctx, relayAddr, receiver.peerID, netip.Addr{})
	require.NoError(t, err, "open the relayed connection from %s", sender.peerID)
	in, err := receiver.OpenConn(ctx, relayAddr, sender.peerID, netip.Addr{})
	require.NoError(t, err, "open the relayed connection from %s", receiver.peerID)

	payload := []byte("ping over the relay")
	_, err = out.Write(payload)
	require.NoError(t, err, "write to the relayed connection")

	received := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 2*len(payload))
		n, err := in.Read(buf)
		if err != nil {
			received <- nil
			return
		}
		received <- buf[:n]
	}()

	select {
	case got := <-received:
		assert.Equal(t, payload, got, "the payload must arrive over the relay")
	case <-time.After(lifecycleTestTimeout):
		t.Fatal("no payload arrived over the relay")
	}
}
