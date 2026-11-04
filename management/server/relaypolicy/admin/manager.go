// Package admin provides authorized account relay configuration and reconciliation actions.
package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/netbirdio/netbird/management/server/activity"
	"github.com/netbirdio/netbird/management/server/permissions"
	"github.com/netbirdio/netbird/management/server/permissions/modules"
	"github.com/netbirdio/netbird/management/server/permissions/operations"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
	sharedrelay "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

const previewLimit = 4096

// Hooks deliver audit and distribution only after a successful transaction.
type Hooks struct {
	Audit  func(context.Context, string, string, string, activity.ActivityDescriber, map[string]any)
	Update func(context.Context, string)
}

// Manager administers relay policies without granting any network access.
type Manager struct {
	store       store.Store
	permissions permissions.Manager
	hooks       Hooks
}

// HomePreview describes the effective home assignment of one peer.
type HomePreview struct {
	PeerID      sharedrelay.PeerID
	Policy      *sharedrelay.ResolvedRelayPolicy
	Unsupported bool
}

// PairPreview describes one ACL-authorized unordered pair.
type PairPreview struct {
	PeerA       sharedrelay.PeerID
	PeerB       sharedrelay.PeerID
	Policy      *sharedrelay.ResolvedRelayPolicy
	Unsupported bool
}

// Preview contains bounded results and explicit truncation, never an implicit success claim.
type Preview struct {
	Revision          uint64
	CatalogGeneration uint64
	Home              []HomePreview
	Pairs             []PairPreview
	Truncated         bool
}

// RetryResult identifies the durable, targeted retry generation and resulting desired policy.
type RetryResult struct {
	Generation uint64
	Policy     *sharedrelay.ResolvedRelayPolicy
}

// NewManager creates a permission-enforcing relay administration manager.
func NewManager(s store.Store, p permissions.Manager, hooks Hooks) *Manager {
	return &Manager{store: s, permissions: p, hooks: hooks}
}

func (m *Manager) authorize(ctx context.Context, accountID, userID string, operation operations.Operation) error {
	user, err := m.store.GetUserByUserID(ctx, store.LockingStrengthNone, userID)
	if err != nil {
		return err
	}
	if user == nil || user.AccountID != accountID || user.IsBlocked() {
		return status.NewPermissionDeniedError()
	}
	switch user.Role {
	case types.UserRoleOwner, types.UserRoleAdmin, types.UserRoleNetworkAdmin:
	case types.UserRoleAuditor:
		if operation != operations.Read {
			return status.NewPermissionDeniedError()
		}
	default:
		return status.NewPermissionDeniedError()
	}
	allowed, _, err := m.permissions.ValidateUserPermissions(ctx, accountID, userID, modules.Relays, operation)
	if err != nil {
		return err
	}
	if !allowed {
		return status.NewPermissionDeniedError()
	}
	return nil
}

func repository(s store.Store) (store.RelayPolicyStore, error) {
	repo, ok := s.(store.RelayPolicyStore)
	if !ok {
		return nil, status.Errorf(status.PreconditionFailed, "store does not support relay policies")
	}
	return repo, nil
}

// Get returns account configuration and the shared approved catalog after authorization.
func (m *Manager) Get(ctx context.Context, accountID, userID string) (sharedrelay.Snapshot, error) {
	if err := m.authorize(ctx, accountID, userID, operations.Read); err != nil {
		return sharedrelay.Snapshot{}, err
	}
	repo, err := repository(m.store)
	if err != nil {
		return sharedrelay.Snapshot{}, err
	}
	return repo.GetRelaySnapshot(ctx, sharedrelay.AccountID(accountID))
}

// Save validates and atomically persists a revision-fenced configuration, then audits and pushes it.
func (m *Manager) Save(ctx context.Context, accountID, userID string, expectedRevision, catalogGeneration uint64, candidate sharedrelay.Configuration) (sharedrelay.Configuration, error) {
	if err := m.authorize(ctx, accountID, userID, operations.Update); err != nil {
		return sharedrelay.Configuration{}, err
	}
	candidate = candidate.Copy()
	var saved sharedrelay.Configuration
	err := m.store.ExecuteInTransaction(ctx, func(tx store.Store) error {
		repo, err := repository(tx)
		if err != nil {
			return err
		}
		if err := repo.LockRelayConfiguration(ctx, sharedrelay.AccountID(accountID)); err != nil {
			return err
		}
		account, err := tx.GetAccount(ctx, accountID)
		if err != nil {
			return err
		}
		current := account.RelaySnapshot()
		if current.Revision != expectedRevision || current.Catalog.Generation != catalogGeneration {
			return store.ErrRelayRevisionConflict
		}
		if err := validateNames(candidate); err != nil {
			return err
		}
		next := sharedrelay.NextConfiguration(current.Configuration, candidate)
		applyConfiguration(account, next)
		if err := ValidateAccount(ctx, account); err != nil {
			return err
		}
		saved, err = repo.SaveRelayConfiguration(ctx, sharedrelay.AccountID(accountID), expectedRevision, catalogGeneration, candidate)
		return err
	})
	if err != nil {
		return sharedrelay.Configuration{}, mapError(err)
	}
	m.committed(ctx, accountID, userID, accountID, activity.RelayConfigurationUpdated, map[string]any{
		"old_revision": expectedRevision, "new_revision": saved.Revision, "catalog_generation": catalogGeneration,
	})
	return saved, nil
}

func applyConfiguration(account *types.Account, candidate sharedrelay.Configuration) {
	account.RelayGroups, account.RelayHomeProfiles, account.RelayPairPolicies = candidate.Groups, candidate.Profiles, candidate.Policies
	account.RelayState = &sharedrelay.RelayAccountState{AccountID: sharedrelay.AccountID(account.Id), Revision: candidate.Revision}
}

func validateNames(candidate sharedrelay.Configuration) error {
	if err := validateBounds(candidate); err != nil {
		return err
	}
	names := make([]string, 0, len(candidate.Groups)+len(candidate.Profiles)+len(candidate.Policies))
	for _, group := range candidate.Groups {
		names = append(names, group.Name)
	}
	for _, profile := range candidate.Profiles {
		names = append(names, profile.Name)
	}
	for _, policy := range candidate.Policies {
		names = append(names, policy.Name)
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 255 {
			return status.Errorf(status.InvalidArgument, "relay configuration names must contain 1 to 255 characters")
		}
	}
	return nil
}

// Preview resolves a detached candidate without persisting it or granting connectivity.
func (m *Manager) Preview(ctx context.Context, accountID, userID string, candidate sharedrelay.Configuration) (*Preview, error) {
	if err := m.authorize(ctx, accountID, userID, operations.Read); err != nil {
		return nil, err
	}
	account, err := m.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if candidate.Revision != account.RelaySnapshot().Revision {
		return nil, store.ErrRelayRevisionConflict
	}
	if err := validateNames(candidate); err != nil {
		return nil, err
	}
	applyConfiguration(account, sharedrelay.NextConfiguration(account.RelaySnapshot().Configuration, candidate))
	return previewAccount(ctx, account, false)
}

// Effective returns one authorized pair's resolved policy, or the local home policy when b is empty.
func (m *Manager) Effective(ctx context.Context, accountID, userID string, a, b sharedrelay.PeerID) (*sharedrelay.ResolvedRelayPolicy, error) {
	if err := m.authorize(ctx, accountID, userID, operations.Read); err != nil {
		return nil, err
	}
	account, err := m.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return effective(ctx, account, a, b)
}

// Retry persists a rate-limited reconciliation directive without modifying relay permissions.
func (m *Manager) Retry(ctx context.Context, accountID, userID string, a, b sharedrelay.PeerID, expectedDigest sharedrelay.Digest) (*RetryResult, error) {
	if err := m.authorize(ctx, accountID, userID, operations.Update); err != nil {
		return nil, err
	}
	if expectedDigest == "" {
		return nil, status.Errorf(status.InvalidArgument, "expected effective digest is required")
	}
	var result RetryResult
	err := m.store.ExecuteInTransaction(ctx, func(tx store.Store) error {
		repo, err := repository(tx)
		if err != nil {
			return err
		}
		if err := repo.LockRelayConfiguration(ctx, sharedrelay.AccountID(accountID)); err != nil {
			return err
		}
		account, err := tx.GetAccount(ctx, accountID)
		if err != nil {
			return err
		}
		policy, err := effective(ctx, account, a, b)
		if err != nil {
			return err
		}
		if policy == nil || policy.Digest != expectedDigest {
			return store.ErrRelayRevisionConflict
		}
		if !supports(account, a) || (b != "" && !supports(account, b)) {
			return status.Errorf(status.PreconditionFailed, "relay policy target has an unsupported client")
		}
		result.Generation, err = repo.AdvanceRelayReselection(ctx, sharedrelay.AccountID(accountID), a, b, account.RelaySnapshot().Revision)
		if err != nil {
			return err
		}
		result.Policy, err = resolveWithGeneration(account, a, b, result.Generation)
		return err
	})
	if err != nil {
		return nil, mapError(err)
	}
	if b != "" && a > b {
		a, b = b, a
	}
	m.committed(ctx, accountID, userID, string(a), activity.RelayPreferredRetryRequested, map[string]any{
		"peer_a": a, "peer_b": b, "reselection_generation": result.Generation, "effective_digest": expectedDigest,
	})
	return &result, nil
}

func (m *Manager) committed(ctx context.Context, accountID, userID, targetID string, event activity.Activity, meta map[string]any) {
	if m.hooks.Audit != nil {
		m.hooks.Audit(ctx, userID, targetID, accountID, event, meta)
	}
	if m.hooks.Update != nil {
		m.hooks.Update(context.WithoutCancel(ctx), accountID)
	}
}

func mapError(err error) error {
	if errors.Is(err, store.ErrRelayRevisionConflict) {
		return fmt.Errorf("relay revision changed; reload and preview again: %w", err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Errorf(status.NotFound, "relay policy target not found")
	}
	var typed *status.Error
	if errors.As(err, &typed) {
		return err
	}
	return fmt.Errorf("relay configuration: %w", err)
}
