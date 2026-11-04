package client

import (
	"context"
	"fmt"

	pb "google.golang.org/protobuf/proto"

	"github.com/netbirdio/netbird/encryption"
	"github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// ReportRelayPolicyStatus sends observations under the current authenticated sync session.
// Callers may use this optional capability without extending the shared Client interface.
func (c *GrpcClient) ReportRelayPolicyStatus(ctx context.Context, reports []*proto.RelayPathReport) error {
	if len(reports) == 0 {
		return nil
	}
	if len(reports) > rp.MaxReports {
		return fmt.Errorf("relay status batch exceeds 128 observations")
	}
	c.syncStreamMu.Lock()
	session, key := c.relayReportSession, c.relayReportServerKey
	if session == 0 {
		c.syncStreamMu.Unlock()
		return fmt.Errorf("relay reporting sync session is unavailable")
	}
	detached := make([]*proto.RelayPathReport, 0, len(reports))
	for _, report := range reports {
		if report == nil {
			c.syncStreamMu.Unlock()
			return fmt.Errorf("nil relay observation")
		}
		clone := pb.Clone(report).(*proto.RelayPathReport)
		c.relayReportSequence++
		clone.Sequence = c.relayReportSequence
		detached = append(detached, clone)
	}
	c.syncStreamMu.Unlock()
	body, err := encryption.EncryptMessage(key, c.key, &proto.RelayPolicyStatusRequest{Session: session, Reports: detached})
	if err != nil {
		return fmt.Errorf("encrypt relay observations: %w", err)
	}
	c.syncStreamMu.RLock()
	current := c.relayReportSession
	c.syncStreamMu.RUnlock()
	if session != current {
		return fmt.Errorf("relay reporting sync session changed")
	}
	rpcCtx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()
	_, err = c.realClient.ReportRelayPolicyStatus(rpcCtx, &proto.EncryptedMessage{WgPubKey: c.key.PublicKey().String(), Body: body})
	if err != nil {
		return err
	}
	c.syncStreamMu.RLock()
	current = c.relayReportSession
	c.syncStreamMu.RUnlock()
	if session != current {
		return fmt.Errorf("relay reporting sync session changed")
	}
	return nil
}
