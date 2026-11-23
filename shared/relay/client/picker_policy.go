package client

import (
	"context"
	"fmt"

	log "github.com/sirupsen/logrus"
)

// pickerPolicy holds the fork hooks of the ServerPicker. It is embedded in ServerPicker.
// acquire and release let the manager pool clients behind relay registrations.
type pickerPolicy struct {
	acquire func(context.Context, string) (*Client, error)
	release func(*Client)
}

// PickServer races the configured servers within a bounded attempt.
func (sp *ServerPicker) PickServer(parentCtx context.Context) (*Client, error) {
	return sp.pickURLs(parentCtx, sp.ServerURLs.Load().([]string))
}

func (sp *ServerPicker) pickURLs(parentCtx context.Context, urls []string) (*Client, error) {
	ctx, cancel := context.WithTimeout(parentCtx, sp.ConnectionTimeout)
	defer cancel()
	results := make(chan pickResult, len(urls))
	limiter := make(chan struct{}, maxConcurrentServers)
	for _, address := range urls {
		go func(address string) {
			select {
			case limiter <- struct{}{}:
			case <-ctx.Done():
				results <- pickResult{Err: ctx.Err()}
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
				c.authKey = sp.AuthKey
				err = c.connectWithLifetime(ctx, parentCtx)
			}
			results <- pickResult{RelayClient: c, Url: address, Err: err}
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

func (sp *ServerPicker) drainResults(results <-chan pickResult, remaining int) {
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

type pickResult struct {
	RelayClient *Client
	Url         string
	Err         error
}
