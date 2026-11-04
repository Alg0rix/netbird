package client

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	sigProto "github.com/netbirdio/netbird/shared/signal/proto"
)

type blockedSendServer struct {
	sigProto.UnimplementedSignalExchangeServer
	entered  chan struct{}
	attempts atomic.Int32
}

func (s *blockedSendServer) Send(ctx context.Context, _ *sigProto.EncryptedMessage) (*sigProto.EncryptedMessage, error) {
	if s.attempts.Add(1) == 1 {
		close(s.entered)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSendContextCancelsBlockedRPC(t *testing.T) {
	for _, lifetime := range []string{"attempt", "client"} {
		t.Run(lifetime, func(t *testing.T) {
			server := &blockedSendServer{entered: make(chan struct{})}
			address := serveTestSignalExchange(t, server)
			key, err := wgtypes.GeneratePrivateKey()
			require.NoError(t, err)
			remoteKey, err := wgtypes.GeneratePrivateKey()
			require.NoError(t, err)
			clientCtx, cancelClient := context.WithCancel(context.Background())
			t.Cleanup(cancelClient)
			client, err := NewClient(clientCtx, address, key, false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() {
				done <- client.SendContext(ctx, &sigProto.Message{RemoteKey: remoteKey.PublicKey().String(), Body: &sigProto.Body{Type: sigProto.Body_ANSWER}})
			}()
			select {
			case <-server.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("send never reached Signal server")
			}
			if lifetime == "attempt" {
				cancel()
			} else {
				cancelClient()
			}
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt blocked Signal RPC")
			}
			assert.Equal(t, int32(1), server.attempts.Load(), "canceled sends must not retry")
		})
	}
}
