package client

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServerPicker_UnavailableServers(t *testing.T) {
	timeout := 5 * time.Second
	sp := ServerPicker{
		TokenStore:        nil,
		PeerID:            "test",
		ConnectionTimeout: timeout,
	}
	sp.ServerURLs.Store([]string{"rel://dummy1", "rel://dummy2"})

	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Second)
	defer cancel()

	_, err := sp.PickServer(ctx)
	require.Error(t, err, "unavailable servers must return a connection failure")
	require.NoError(t, ctx.Err(), "the picker must finish within its own timeout before the caller deadline")
}
