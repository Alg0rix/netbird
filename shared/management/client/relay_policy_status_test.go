package client

import (
	"context"
	"testing"

	"github.com/netbirdio/netbird/encryption"
	"github.com/netbirdio/netbird/shared/management/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc"
)

type reportRPC struct {
	proto.ManagementServiceClient
	report func(*proto.EncryptedMessage) error
}

func (r reportRPC) ReportRelayPolicyStatus(_ context.Context, message *proto.EncryptedMessage, _ ...grpc.CallOption) (*proto.Empty, error) {
	return &proto.Empty{}, r.report(message)
}

func TestRelayReportSessionAndSequenceFences(t *testing.T) {
	clientKey, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	serverKey, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	c := &GrpcClient{key: clientKey, relayReportSession: 41, relayReportServerKey: serverKey.PublicKey()}
	var received []*proto.RelayPolicyStatusRequest
	c.realClient = reportRPC{report: func(message *proto.EncryptedMessage) error {
		decoded := &proto.RelayPolicyStatusRequest{}
		require.NoError(t, encryption.DecryptMessage(clientKey.PublicKey(), serverKey, message.Body, decoded))
		received = append(received, decoded)
		return nil
	}}
	report := &proto.RelayPathReport{RemotePeerKey: "remote", Digest: "current"}
	require.NoError(t, c.ReportRelayPolicyStatus(context.Background(), []*proto.RelayPathReport{report}))
	assert.Equal(t, int64(41), received[0].Session, "wire reports must carry the current authenticated sync owner")
	assert.Equal(t, uint64(1), received[0].Reports[0].Sequence, "first observation must have a monotonic sequence")
	assert.Zero(t, report.Sequence, "sending must not mutate caller snapshots")
	c.setSyncStreamDisconnected(context.Canceled)
	assert.Error(t, c.ReportRelayPolicyStatus(context.Background(), []*proto.RelayPathReport{report}), "disconnected clients cannot reuse a stale session")
	c.relayReportSession = 42
	require.NoError(t, c.ReportRelayPolicyStatus(context.Background(), []*proto.RelayPathReport{report}))
	assert.Equal(t, int64(42), received[1].Session, "reconnected reports must use the new sync owner")
	assert.Greater(t, received[1].Reports[0].Sequence, received[0].Reports[0].Sequence, "sequence must not regress across reconnects")
	c.realClient = reportRPC{report: func(*proto.EncryptedMessage) error { c.setSyncStreamDisconnected(context.Canceled); return nil }}
	assert.Error(t, c.ReportRelayPolicyStatus(context.Background(), []*proto.RelayPathReport{report}), "late RPC success after disconnect cannot be treated as current")
}
