package client

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	auth "github.com/netbirdio/netbird/shared/relay/auth/hmac"
)

const (
	maxConcurrentServers     = 7
	defaultConnectionTimeout = 30 * time.Second
)

type connResult struct {
	RelayClient *Client
	Url         string
	Err         error
}

type ServerPicker struct {
	TokenStore        *auth.TokenStore
	ServerURLs        atomic.Value
	PeerID            string
	MTU               uint16
	ConnectionTimeout time.Duration
	TransportFallback *transportFallback
	NetEvents         NetEvents
	acquire           func(context.Context, string) (*Client, error)
	release           func(*Client)
}

// PickServer races the configured servers within a bounded attempt.
func (sp *ServerPicker) PickServer(parentCtx context.Context) (*Client, error) {
	return sp.pickURLs(parentCtx, sp.ServerURLs.Load().([]string))
}

func (sp *ServerPicker) pickURLs(parentCtx context.Context, urls []string) (*Client, error) {
	ctx, cancel := context.WithTimeout(parentCtx, sp.ConnectionTimeout)
	defer cancel()
	results := make(chan connResult, len(urls))
	limiter := make(chan struct{}, maxConcurrentServers)
	for _, address := range urls {
		go func(address string) {
			select {
			case limiter <- struct{}{}:
			case <-ctx.Done():
				results <- connResult{Err: ctx.Err()}
				return
			}
			defer func() { <-limiter }()
			var c *Client
			var err error
			if sp.acquire != nil {
				c, err = sp.acquire(ctx, address)
			} else {
				c = NewClient(address, sp.TokenStore, sp.PeerID, sp.MTU)
				c.SetTransportFallback(sp.TransportFallback)
				c.netEvents = sp.NetEvents
				err = c.connectWithLifetime(ctx, parentCtx)
			}
			results <- connResult{RelayClient: c, Url: address, Err: err}
		}(address)
	}
	var errs []error
	remaining := len(urls)
	for remaining > 0 {
		select {
		case result := <-results:
			remaining--
			if result.Err != nil {
				errs = append(errs, result.Err)
				if result.RelayClient != nil {
					sp.releaseClient(result.RelayClient)
				}
				continue
			}
			// Drain late successes after cancelling the race; pooled losers release
			// only their picker ownership, preserving pair and foreign registrations.
			go sp.drainResults(results, remaining)
			return result.RelayClient, nil
		case <-ctx.Done():
			go sp.drainResults(results, remaining)
			return nil, fmt.Errorf("connect to relay server: %w", ctx.Err())
		}
	}
	return nil, pickErr(errs)
}

func (sp *ServerPicker) drainResults(results <-chan connResult, remaining int) {
	for i := 0; i < remaining; i++ {
		result := <-results
		if result.RelayClient != nil {
			sp.releaseClient(result.RelayClient)
		}
	}
}

func (sp *ServerPicker) releaseClient(c *Client) {
	if sp.release != nil {
		sp.release(c)
		return
	}
	if err := c.Close(); err != nil {
		log.Debugf("close unused relay connection: %v", err)
	}
}

// pickErr combines per-server connection failures into a single error.
func pickErr(errs []error) error {
	if len(errs) == 0 {
		return errors.New("no relay server available")
	}
	return errors.Join(errs...)
}
