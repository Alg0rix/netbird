package client

import "context"

// Fork additions to the relay Client. They sit on top of the upstream session client.

// connectWithLifetime dials with dialCtx and keeps the session alive until lifetime ends.
// A pooled client must outlive the bounded context of the attempt that created it.
func (c *Client) connectWithLifetime(dialCtx, lifetime context.Context) error {
	_, err := c.connectSessionWithLifetime(dialCtx, lifetime)
	return err
}

// connectSessionWithLifetime mirrors Client.ConnectSession with a separate session lifetime.
// Keep it in sync with ConnectSession when upstream changes that function.
func (c *Client) connectSessionWithLifetime(dialCtx, lifetime context.Context) (*session, error) {
	c.log.Infof("connecting to relay server")
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	if s := c.CurrentSession(); s != nil {
		return s, nil
	}

	s, err := c.connect(dialCtx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.session = s
	c.mu.Unlock()

	s.log.Infof("relay connection established, server IP: %s", connectedIP(s.conn))

	go s.ListenForStopEvents(lifetime)
	go s.ReadLoop()
	go c.notifyDisconnected(s)

	return s, nil
}

// SetOnDisconnectListener registers fn, called with the connection URL after a session ends.
func (c *Client) SetOnDisconnectListener(fn func(string)) {
	c.listenerMu.Lock()
	defer c.listenerMu.Unlock()
	c.onDisconnect = fn
}

func (c *Client) notifyDisconnected(s *session) {
	s.Wait(context.Background())
	c.listenerMu.Lock()
	fn := c.onDisconnect
	c.listenerMu.Unlock()
	if fn != nil {
		fn(c.connectionURL)
	}
}
