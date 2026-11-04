package grpc

import (
	"context"
	"unicode/utf8"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/netbirdio/netbird/management/server/relaypolicy/admin"
	"github.com/netbirdio/netbird/management/server/store"
	mgmtproto "github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

const maxRelayReportBytes = 1 << 20

// ReportRelayPolicyStatus records observations owned by the authenticated peer's Sync session.
func (s *Server) ReportRelayPolicyStatus(ctx context.Context, req *mgmtproto.EncryptedMessage) (*mgmtproto.Empty, error) {
	if req == nil || proto.Size(req) > maxRelayReportBytes {
		return nil, status.Error(codes.InvalidArgument, "relay report exceeds message limit")
	}
	report := &mgmtproto.RelayPolicyStatusRequest{}
	peerKey, err := s.parseRequest(ctx, req, report)
	if err != nil {
		return nil, err
	}
	if proto.Size(report) > maxRelayReportBytes || len(report.Reports) == 0 || len(report.Reports) > rp.MaxReports || report.Session <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid relay report batch")
	}
	repository := s.accountManager.GetStore()
	local, err := repository.GetPeerByPeerPubKey(ctx, store.LockingStrengthNone, peerKey.String())
	if err != nil || local == nil {
		return nil, status.Error(codes.PermissionDenied, "relay report peer is not registered")
	}
	accountID := rp.AccountID(local.AccountID)
	reports, err := decodeRelayPathReports(ctx, repository, accountID, report.Reports)
	if err != nil {
		return nil, err
	}
	manager := admin.NewManager(repository, nil, admin.Hooks{})
	if err := manager.Report(ctx, accountID, rp.PeerID(local.ID), report.Session, reports); err != nil {
		return nil, mapError(ctx, err)
	}
	return &mgmtproto.Empty{}, nil
}

func decodeRelayPathReports(ctx context.Context, repository store.Store, accountID rp.AccountID, wire []*mgmtproto.RelayPathReport) ([]rp.PathReport, error) {
	reports := make([]rp.PathReport, 0, len(wire))
	seen := make(map[rp.PeerID]struct{}, len(wire))
	for _, report := range wire {
		if report == nil || len(report.Reason) > 128 || !utf8.ValidString(report.Reason) {
			return nil, status.Error(codes.InvalidArgument, "invalid relay path report")
		}
		transport, err := relayPathTransport(report.Transport)
		if err != nil {
			return nil, err
		}
		state, err := relayPathState(report.State)
		if err != nil {
			return nil, err
		}
		var remoteID rp.PeerID
		if report.RemotePeerKey != "" {
			key, err := wgtypes.ParseKey(report.RemotePeerKey)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, "invalid remote peer key")
			}
			remote, err := repository.GetPeerByPeerPubKey(ctx, store.LockingStrengthNone, key.String())
			if err != nil || remote == nil || remote.AccountID != string(accountID) {
				return nil, status.Error(codes.PermissionDenied, "remote peer is unavailable")
			}
			remoteID = rp.PeerID(remote.ID)
		}
		if _, duplicate := seen[remoteID]; duplicate {
			return nil, status.Error(codes.InvalidArgument, "duplicate relay path report")
		}
		seen[remoteID] = struct{}{}
		reports = append(reports, rp.PathReport{
			RemotePeerID: remoteID, Revision: report.Revision, Digest: rp.Digest(report.Digest),
			CatalogGeneration: report.CatalogGeneration, ReselectionGeneration: report.ReselectionGeneration,
			Sequence: report.Sequence, Attempt: report.Attempt, Transport: transport,
			RelayID: rp.RelayID(report.RelayId), State: state, Reason: rp.FailureReason(report.Reason),
		})
	}
	return reports, nil
}

func relayPathTransport(value mgmtproto.RelayPathTransport) (rp.PathTransport, error) {
	switch value {
	case mgmtproto.RelayPathTransport_RELAY_PATH_TRANSPORT_UNKNOWN:
		return rp.TransportUnknown, nil
	case mgmtproto.RelayPathTransport_RELAY_PATH_TRANSPORT_DIRECT:
		return rp.TransportDirect, nil
	case mgmtproto.RelayPathTransport_RELAY_PATH_TRANSPORT_NATIVE_RELAY:
		return rp.TransportNativeRelay, nil
	default:
		return "", status.Error(codes.InvalidArgument, "unknown relay transport")
	}
}

func relayPathState(value mgmtproto.RelayPathState) (rp.PathState, error) {
	switch value {
	case mgmtproto.RelayPathState_RELAY_PATH_STATE_PENDING:
		return rp.StatePending, nil
	case mgmtproto.RelayPathState_RELAY_PATH_STATE_APPLIED:
		return rp.StateApplied, nil
	case mgmtproto.RelayPathState_RELAY_PATH_STATE_BLOCKED:
		return rp.StateBlocked, nil
	case mgmtproto.RelayPathState_RELAY_PATH_STATE_UNSUPPORTED:
		return rp.StateUnsupported, nil
	default:
		return "", status.Error(codes.InvalidArgument, "unknown relay path state")
	}
}
