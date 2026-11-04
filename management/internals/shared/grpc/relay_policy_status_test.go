package grpc

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	grpcbase "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/netbirdio/netbird/encryption"
	"github.com/netbirdio/netbird/management/internals/controllers/network_map"
	nbconfig "github.com/netbirdio/netbird/management/internals/server/config"
	"github.com/netbirdio/netbird/management/server/account"
	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
	"github.com/netbirdio/netbird/shared/management/networkmap/nmdata"
	"github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func TestRelayPathReportsResolveOnlySameAccountKeys(t *testing.T) {
	ctx := context.Background()
	repository, err := store.NewStore(ctx, types.SqliteStoreEngine, t.TempDir(), nil, false)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, repository.Close(ctx)) })
	localKey, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	foreignKey, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	require.NoError(t, repository.SaveAccount(ctx, &types.Account{Id: "account", Peers: map[string]*nbpeer.Peer{"remote": {ID: "remote", AccountID: "account", Key: localKey.PublicKey().String()}}}))
	require.NoError(t, repository.SaveAccount(ctx, &types.Account{Id: "foreign", Peers: map[string]*nbpeer.Peer{"foreign": {ID: "foreign", AccountID: "foreign", Key: foreignKey.PublicKey().String()}}}))
	report := &proto.RelayPathReport{RemotePeerKey: localKey.PublicKey().String(), Transport: proto.RelayPathTransport_RELAY_PATH_TRANSPORT_DIRECT, State: proto.RelayPathState_RELAY_PATH_STATE_APPLIED, Revision: 4, Sequence: 9}
	reports, err := decodeRelayPathReports(ctx, repository, "account", []*proto.RelayPathReport{report})
	require.NoError(t, err)
	require.Len(t, reports, 1, "one peer observation should survive decoding")
	assert.Equal(t, rp.PeerID("remote"), reports[0].RemotePeerID, "public keys must become server-owned account peer IDs")
	assert.Equal(t, rp.TransportDirect, reports[0].Transport, "direct transport must remain distinct from native relay")
	report.RemotePeerKey = foreignKey.PublicKey().String()
	_, err = decodeRelayPathReports(ctx, repository, "account", []*proto.RelayPathReport{report})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "a valid key in another account must not be accepted")
	report.RemotePeerKey = ""
	reports, err = decodeRelayPathReports(ctx, repository, "account", []*proto.RelayPathReport{report})
	require.NoError(t, err)
	assert.Empty(t, reports[0].RemotePeerID, "an empty key must identify a home observation")
	for _, mutate := range []func(*proto.RelayPathReport){
		func(r *proto.RelayPathReport) { r.Transport = proto.RelayPathTransport(99) },
		func(r *proto.RelayPathReport) { r.State = proto.RelayPathState(99) },
		func(r *proto.RelayPathReport) { r.Reason = strings.Repeat("x", 129) },
	} {
		candidate := &proto.RelayPathReport{Transport: report.Transport, State: report.State}
		mutate(candidate)
		_, err = decodeRelayPathReports(ctx, repository, "account", []*proto.RelayPathReport{candidate})
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "malformed reports must fail closed")
	}
	_, err = decodeRelayPathReports(ctx, repository, "account", []*proto.RelayPathReport{report, report})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "duplicate home or pair observations must not be ambiguous")
}

func TestRelayPolicyReportRejectsUnauthenticatedAndUnboundedBatches(t *testing.T) {
	serverKey, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	peerKey, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	server := &Server{secretsManager: &TimeBasedAuthSecretsManager{wgKey: serverKey}}
	_, err = server.ReportRelayPolicyStatus(context.Background(), &proto.EncryptedMessage{WgPubKey: peerKey.PublicKey().String(), Body: []byte("forged")})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a public key alone must not authenticate a report")
	for _, report := range []*proto.RelayPolicyStatusRequest{
		{Session: 1, Reports: make([]*proto.RelayPathReport, rp.MaxReports+1)},
		{Session: 0, Reports: []*proto.RelayPathReport{{}}},
		{Session: 1},
	} {
		body, err := encryption.EncryptMessage(serverKey.PublicKey(), peerKey, report)
		require.NoError(t, err)
		_, err = server.ReportRelayPolicyStatus(context.Background(), &proto.EncryptedMessage{WgPubKey: peerKey.PublicKey().String(), Body: body})
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "invalid encrypted batches must be rejected before identity lookup")
	}
	_, err = server.ReportRelayPolicyStatus(context.Background(), &proto.EncryptedMessage{Body: make([]byte, maxRelayReportBytes+1)})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "oversized ciphertext must be rejected before decryption")
}

type relayStatusSyncStream struct {
	grpcbase.ServerStream
	response *proto.EncryptedMessage
}

func (s *relayStatusSyncStream) Send(response *proto.EncryptedMessage) error {
	s.response = response
	return nil
}

func TestInitialSyncReportsCurrentOwningSession(t *testing.T) {
	ctx := context.Background()
	repository, err := store.NewStore(ctx, types.SqliteStoreEngine, t.TempDir(), nil, false)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, repository.Close(ctx)) })
	serverKey, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	clientKey, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	require.NoError(t, repository.SaveAccount(ctx, &types.Account{Id: "account", Peers: map[string]*nbpeer.Peer{"local": {ID: "local", AccountID: "account", Key: clientKey.PublicKey().String(), IP: netip.MustParseAddr("100.64.0.1"), Status: &nbpeer.PeerStatus{}}}}))
	beforeConnect, err := repository.GetPeerByPeerPubKey(ctx, store.LockingStrengthNone, clientKey.PublicKey().String())
	require.NoError(t, err)
	require.Zero(t, beforeConnect.Status.SessionStartedAt, "first sync must begin without a previous session")
	require.NoError(t, repository.RegisterInstance(ctx, "owner", "test"))
	session := time.Now().UnixNano()
	connected, err := repository.ConnectPeer(ctx, "account", "local", "owner", session)
	require.NoError(t, err)
	require.True(t, connected, "current sync must own the SQL connection")
	owningPeer, err := repository.GetPeerByPeerPubKey(ctx, store.LockingStrengthNone, clientKey.PublicKey().String())
	require.NoError(t, err)
	ctrl := gomock.NewController(t)
	accounts := account.NewMockManager(ctrl)
	accounts.EXPECT().GetStore().Return(repository)
	controller := network_map.NewMockController(ctrl)
	controller.EXPECT().GetDNSDomain(gomock.Any()).Return("")
	server := &Server{accountManager: accounts, networkMapController: controller, secretsManager: &TimeBasedAuthSecretsManager{wgKey: serverKey}, config: &nbconfig.Config{}}
	stream := &relayStatusSyncStream{}
	err = server.sendInitialSync(ctx, clientKey.PublicKey(), beforeConnect, &types.NetworkMap{Network: &nmdata.Network{}}, nil, stream, 0, &types.Settings{}, session, func() {})
	require.NoError(t, err)
	require.NotNil(t, stream.response, "initial sync must send a snapshot")
	response := &proto.SyncResponse{}
	require.NoError(t, encryption.DecryptMessage(serverKey.PublicKey(), clientKey, stream.response.Body, response))
	assert.Equal(t, owningPeer.Status.SessionStartedAt, response.RelayReportSession, "first snapshot must carry the current SQL ownership fence rather than the pre-connect peer status")
}
