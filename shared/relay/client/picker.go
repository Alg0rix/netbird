package client

import (
	"context"
	"crypto/ed25519"
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
	Session *session
	Url     string
	Err     error
}

type ServerPicker struct {
	TokenStore        *auth.TokenStore
	ServerURLs        atomic.Value
	PeerID            string
	MTU               uint16
	ConnectionTimeout time.Duration
	TransportFallback *transportFallback
	NetEvents         NetEvents
	AuthKey           ed25519.PrivateKey

	// acquire and release let the manager pool clients behind relay registrations.
	acquire func(context.Context, string) (*Client, error)
	release func(*Client)
}

func (sp *ServerPicker) Pick(parentCtx context.Context) (*session, error) {
	ctx, cancel := context.WithTimeout(parentCtx, sp.ConnectionTimeout)
	defer cancel()

	serverURLs := sp.ServerURLs.Load().([]string)

	connResultChan := make(chan connResult, len(serverURLs))
	pickedChan := make(chan connResult)
	concurrentLimiter := make(chan struct{}, maxConcurrentServers)

	log.Debugf("pick server from list: %v", serverURLs)
	for _, url := range serverURLs {
		// todo check if we have a successful connection so we do not need to connect to other servers
		concurrentLimiter <- struct{}{}
		go func(url string) {
			defer func() {
				<-concurrentLimiter
			}()
			sp.startConnection(parentCtx, connResultChan, url)
		}(url)
	}

	go sp.processConnResults(ctx, connResultChan, pickedChan)

	select {
	case cr := <-pickedChan:
		if cr.Err != nil {
			return nil, cr.Err
		}
		log.Infof("chosen home Relay server: %s, instance URL: %s, server IP: %s", cr.Url, cr.Session.instanceURL, connectedIP(cr.Session.conn))
		return cr.Session, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("connect to relay server: %w", ctx.Err())
	}
}

func (sp *ServerPicker) startConnection(ctx context.Context, resultChan chan connResult, url string) {
	log.Infof("try to connecting to relay server: %s", url)
	relayClient := NewClient(url, sp.TokenStore, sp.PeerID, sp.MTU)
	relayClient.SetTransportFallback(sp.TransportFallback)
	relayClient.netEvents = sp.NetEvents
	relayClient.authKey = sp.AuthKey
	s, err := relayClient.ConnectSession(ctx)
	resultChan <- connResult{
		Session: s,
		Url:     url,
		Err:     err,
	}
}

func (sp *ServerPicker) processConnResults(ctx context.Context, resultChan chan connResult, pickedChan chan connResult) {
	var hasSuccess bool
	var errs []error
	for numOfResults := 0; numOfResults < cap(resultChan); numOfResults++ {
		cr := <-resultChan
		if cr.Err != nil {
			log.Tracef("failed to connect to Relay server: %s: %v", cr.Url, cr.Err)
			errs = append(errs, cr.Err)
			continue
		}
		log.Infof("connected to Relay server: %s", cr.Url)

		first := !hasSuccess
		hasSuccess = true
		if first && handOver(ctx, pickedChan, cr) {
			continue
		}

		log.Infof("closing unnecessary Relay connection to: %s", cr.Url)
		if err := cr.Session.Close(true); err != nil {
			log.Errorf("failed to close connection to %s: %v", cr.Url, err)
		}
	}
	if !hasSuccess {
		handOver(ctx, pickedChan, connResult{Err: pickErr(errs)})
	}
}

func handOver(ctx context.Context, pickedChan chan connResult, cr connResult) bool {
	select {
	case pickedChan <- cr:
		return true
	case <-ctx.Done():
		return false
	}
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

// pickErr combines per-server connection failures into a single error.
func pickErr(errs []error) error {
	if len(errs) == 0 {
		return errors.New("no relay server available")
	}
	return errors.Join(errs...)
}
