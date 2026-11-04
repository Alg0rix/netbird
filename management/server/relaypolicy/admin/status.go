package admin

import (
	"context"
	"math"
	"slices"

	"github.com/netbirdio/netbird/management/server/permissions/operations"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

// Report records authenticated endpoint observations after checking current sync ownership and policy.
// The caller obtains local and session from the authenticated sync protocol, never RPC identity fields.
func (m *Manager) Report(ctx context.Context, accountID rp.AccountID, local rp.PeerID, session int64, reports []rp.PathReport) error {
	if len(reports) == 0 || len(reports) > rp.MaxReports || local == "" || session <= 0 {
		return status.Errorf(status.InvalidArgument, "invalid relay observation batch")
	}
	observations, ok := m.store.(store.RelayObservationStore)
	if !ok {
		return status.Errorf(status.PreconditionFailed, "store does not support relay observations")
	}
	if err := observations.PruneRelayObservations(ctx, 256); err != nil {
		return err
	}
	return m.store.ExecuteInTransaction(ctx, func(tx store.Store) error {
		repository, err := repository(tx)
		if err != nil {
			return err
		}
		if err := repository.LockRelayConfiguration(ctx, accountID); err != nil {
			return err
		}
		account, err := tx.GetAccount(ctx, string(accountID))
		if err != nil {
			return err
		}
		if account.Peers[string(local)] == nil {
			return status.Errorf(status.PermissionDenied, "reporting peer does not own this account endpoint")
		}
		resolver, err := newEndpointPolicyResolver(account, local)
		if err != nil {
			return err
		}
		seen := make(map[rp.PeerID]struct{}, len(reports))
		for _, report := range reports {
			if _, exists := seen[report.RemotePeerID]; exists {
				return status.Errorf(status.InvalidArgument, "duplicate relay report target")
			}
			seen[report.RemotePeerID] = struct{}{}
			policy, err := resolver.resolve(ctx, report.RemotePeerID)
			if err != nil {
				return err
			}
			if err := validateReport(report, policy); err != nil {
				return err
			}
		}
		repositoryObservations, ok := tx.(store.RelayObservationStore)
		if !ok {
			return status.Errorf(status.PreconditionFailed, "transaction does not support relay observations")
		}
		return repositoryObservations.PutRelayObservations(ctx, accountID, local, session, reports)
	})
}

func validateReport(report rp.PathReport, policy *rp.ResolvedRelayPolicy) error {
	if report.Sequence == 0 || report.Sequence > math.MaxInt64 || report.Attempt > math.MaxInt64 || !rp.ValidReason(report.Reason) {
		return status.Errorf(status.InvalidArgument, "invalid relay report sequence, attempt or reason")
	}
	if policy == nil || report.Digest != policy.Digest || report.Revision != policy.Revision ||
		report.CatalogGeneration != policy.CatalogGeneration || report.ReselectionGeneration != policy.ReselectionGeneration {
		return status.Errorf(status.PreconditionFailed, "relay report does not match current effective policy")
	}
	switch report.State {
	case rp.StatePending, rp.StateApplied, rp.StateBlocked, rp.StateUnsupported:
	default:
		return status.Errorf(status.InvalidArgument, "unknown relay application state")
	}
	switch report.Transport {
	case rp.TransportUnknown:
		if report.RelayID != "" {
			return status.Errorf(status.InvalidArgument, "unknown transport cannot claim a relay")
		}
	case rp.TransportDirect:
		if policy.ForceRelay || report.RemotePeerID == "" || report.RelayID != "" {
			return status.Errorf(status.InvalidArgument, "direct transport is not permitted by this policy")
		}
	case rp.TransportNativeRelay:
		if report.RelayID == "" || !permittedRelay(policy, report.RelayID) {
			return status.Errorf(status.InvalidArgument, "reported relay is outside the effective permitted set")
		}
		if report.RemotePeerID != "" && report.Attempt == 0 {
			return status.Errorf(status.InvalidArgument, "native relay pair report requires an attempt generation")
		}
	default:
		return status.Errorf(status.InvalidArgument, "unknown relay report transport")
	}
	return nil
}

func permittedRelay(policy *rp.ResolvedRelayPolicy, id rp.RelayID) bool {
	for _, tier := range policy.Tiers {
		if slices.ContainsFunc(tier, func(entry rp.RelayCatalogEntry) bool { return entry.ID == id && entry.Active && !entry.Legacy }) {
			return true
		}
	}
	return false
}

// Status reads desired and observed home or pair status through the same account authorization as configuration.
func (m *Manager) Status(ctx context.Context, accountID rp.AccountID, userID rp.UserID, a, b rp.PeerID) (*rp.EffectiveStatus, error) {
	if err := m.authorize(ctx, accountID, userID, operations.Read); err != nil {
		return nil, err
	}
	var result *rp.EffectiveStatus
	err := m.store.ExecuteInTransaction(ctx, func(tx store.Store) error {
		repository, err := repository(tx)
		if err != nil {
			return err
		}
		if _, err := repository.GetRelaySnapshot(ctx, accountID); err != nil {
			return err
		}
		account, err := tx.GetAccount(ctx, string(accountID))
		if err != nil {
			return err
		}
		policy, err := effective(ctx, account, a, b)
		if err != nil {
			return err
		}
		observations, ok := tx.(store.RelayObservationStore)
		if !ok {
			return status.Errorf(status.PreconditionFailed, "store does not support relay observations")
		}
		endpoints, err := observations.GetRelayObservations(ctx, accountID, a, b)
		if err != nil {
			return err
		}
		result = aggregateStatus(account, a, b, policy, endpoints)
		return nil
	})
	return result, err
}

func aggregateStatus(account *types.Account, a, b rp.PeerID, policy *rp.ResolvedRelayPolicy, endpoints []rp.EndpointObservation) *rp.EffectiveStatus {
	if b != "" && a > b {
		a, b = b, a
	}
	result := &rp.EffectiveStatus{PeerA: a, PeerB: b, Policy: policy, Application: rp.ApplicationPending, Endpoints: endpoints, Transport: rp.TransportUnknown}
	for i := range result.Endpoints {
		if validateReport(result.Endpoints[i].Report, policy) != nil {
			result.Endpoints[i].Current = false
		}
	}
	if policy == nil {
		result.Application = rp.ApplicationUnconfigured
		return result
	}
	if !supports(account, a) || (b != "" && !supports(account, b)) {
		result.Application = rp.ApplicationUnsupported
		return result
	}
	expected := 2
	if b == "" {
		expected = 1
	}
	if len(endpoints) == 0 {
		return result
	}
	stale, unsupported := false, false
	for i := range result.Endpoints {
		e := &result.Endpoints[i]
		stale = stale || !e.Current
		unsupported = unsupported || e.Report.State == rp.StateUnsupported
	}
	if stale {
		result.Application = rp.ApplicationStale
		return result
	}
	if unsupported {
		result.Application = rp.ApplicationUnsupported
		return result
	}
	if len(endpoints) != expected {
		return result
	}
	for _, e := range endpoints {
		if e.Report.State == rp.StatePending {
			return result
		}
	}
	first := endpoints[0].Report
	for _, e := range endpoints[1:] {
		if e.Report.Transport != first.Transport || e.Report.RelayID != first.RelayID || (first.Transport == rp.TransportNativeRelay && e.Report.Attempt != first.Attempt) {
			result.Application = rp.ApplicationDisagreement
			return result
		}
	}
	result.Application = rp.ApplicationApplied
	result.Transport, result.RelayID = first.Transport, first.RelayID
	result.Healthy = first.State == rp.StateApplied && first.Transport != rp.TransportUnknown
	for _, e := range endpoints[1:] {
		result.Healthy = result.Healthy && e.Report.State == rp.StateApplied
	}
	return result
}
