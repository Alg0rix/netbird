package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/netbirdio/netbird/management/internals/relaypolicydb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/relaypolicy"
	"github.com/netbirdio/netbird/management/server/types"
	sharedrelay "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// GetRelaySnapshot reads catalog and account configuration under one consistent transaction.
func (s *SqlStore) GetRelaySnapshot(ctx context.Context, accountID types.AccountID) (sharedrelay.Snapshot, error) {
	var snapshot sharedrelay.Snapshot
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		// Publication and account mutations take these locks in the same order.
		var head relayCatalogHead
		err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", 1).Take(&head).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var account types.Account
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").
			Where("id = ?", accountID).Take(&account).Error; err != nil {
			return err
		}
		var readErr error
		snapshot, readErr = readRelaySnapshot(tx, accountID)
		return readErr
	})
	return snapshot, err
}

func readRelaySnapshot(tx *gorm.DB, accountID types.AccountID) (sharedrelay.Snapshot, error) {
	snapshot := sharedrelay.Snapshot{AccountID: accountID}
	catalog, err := getRelayCatalog(tx)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return snapshot, err
	}
	snapshot.Catalog = catalog
	var serial uint64
	if err := tx.Model(&types.Account{}).Select("network_serial").Where("id = ?", accountID).Scan(&serial).Error; err != nil {
		return snapshot, err
	}
	snapshot.NetworkSerial = serial
	state := types.RelayAccountState{}
	if err := tx.Where("account_id = ?", accountID).Find(&state).Error; err != nil {
		return snapshot, err
	}
	snapshot.Revision = state.Revision
	for _, model := range []any{&snapshot.Groups, &snapshot.Profiles, &snapshot.Policies, &snapshot.Reselections} {
		if err := tx.Where("account_id = ?", accountID).Find(model).Error; err != nil {
			return snapshot, fmt.Errorf("read relay account relations: %w", err)
		}
	}
	slices.SortFunc(snapshot.Groups, func(a, b types.RelayGroup) int { return compareRelayID(a.ID, b.ID) })
	slices.SortFunc(snapshot.Profiles, func(a, b types.RelayHomeProfile) int { return compareRelayID(a.ID, b.ID) })
	slices.SortFunc(snapshot.Policies, func(a, b types.RelayPairPolicy) int { return compareRelayID(a.ID, b.ID) })
	slices.SortFunc(snapshot.Reselections, func(a, b types.RelayReselection) int {
		if a.PeerA != b.PeerA {
			return compareRelayID(a.PeerA, b.PeerA)
		}
		return compareRelayID(a.PeerB, b.PeerB)
	})
	if len(snapshot.Groups) == 0 {
		snapshot.Groups = nil
	}
	if len(snapshot.Profiles) == 0 {
		snapshot.Profiles = nil
	}
	if len(snapshot.Policies) == 0 {
		snapshot.Policies = nil
	}
	if len(snapshot.Reselections) == 0 {
		snapshot.Reselections = nil
	}
	return snapshot, nil
}

func compareRelayID[T ~string](a, b T) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// SaveRelayConfiguration validates and replaces account relations with optimistic revision and catalog fencing.
// Catalog publication, the account revision, and the network serial commit atomically with the relations.
func (s *SqlStore) SaveRelayConfiguration(ctx context.Context, accountID types.AccountID, expectedRevision, catalogGeneration uint64, candidate sharedrelay.Configuration) (sharedrelay.Configuration, error) {
	candidate = candidate.Copy()
	if expectedRevision >= math.MaxInt64 {
		return sharedrelay.Configuration{}, ErrRelayRevisionConflict
	}
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockRelayCatalog(tx, catalogGeneration); err != nil {
			return err
		}
		result := tx.Model(&types.Account{}).Where("id = ?", accountID).
			Update("network_serial", gorm.Expr("network_serial + 1"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&types.RelayAccountState{AccountID: accountID}).Error; err != nil {
			return err
		}
		current, err := readRelaySnapshot(tx, accountID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrRelayRevisionConflict
		}
		candidate.Revision = expectedRevision + 1
		advanceRelayEntityRevisions(&candidate, current.Configuration)
		input, err := relayInputForConfiguration(tx, accountID, current.Catalog, candidate)
		if err != nil {
			return err
		}
		if err := relaypolicy.Validate(input); err != nil {
			return err
		}
		result = tx.Model(&types.RelayAccountState{}).Where("account_id = ? AND revision = ?", accountID, expectedRevision).
			Update("revision", candidate.Revision)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayRevisionConflict
		}
		return replaceRelayRelations(tx, accountID, candidate)
	})
	if err != nil {
		return sharedrelay.Configuration{}, err
	}
	return candidate, nil
}

func lockRelayCatalog(tx *gorm.DB, generation uint64) error {
	result := tx.Model(&relayCatalogHead{}).Where("id = ? AND generation = ?", 1, generation).
		Update("generation", gorm.Expr("generation"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRelayRevisionConflict
	}
	return nil
}

func relayInputForConfiguration(tx *gorm.DB, accountID types.AccountID, catalog types.RelayCatalog, config sharedrelay.Configuration) (types.RelayPolicyInput, error) {
	input := types.RelayPolicyInput{AccountID: accountID, Catalog: catalog, Groups: config.Groups,
		Profiles: config.Profiles, Policies: config.Policies, Revision: config.Revision}
	var peers []nbpeer.Peer
	if err := tx.Select("id", "account_id").Where("account_id = ?", accountID).Find(&peers).Error; err != nil {
		return input, err
	}
	var groups []types.Group
	if err := tx.Select("id").Where("account_id = ?", accountID).Find(&groups).Error; err != nil {
		return input, err
	}
	var memberships []types.GroupPeer
	if err := tx.Where("account_id = ?", accountID).Find(&memberships).Error; err != nil {
		return input, err
	}
	byPeer := make(map[types.PeerID][]types.PeerGroupID)
	for _, membership := range memberships {
		id := types.PeerID(membership.PeerID)
		byPeer[id] = append(byPeer[id], types.PeerGroupID(membership.GroupID))
	}
	for _, group := range groups {
		input.PeerGroups = append(input.PeerGroups, types.PeerGroupID(group.ID))
	}
	for _, peer := range peers {
		input.Peers = append(input.Peers, types.RelayPeer{ID: types.PeerID(peer.ID), AccountID: accountID, GroupIDs: byPeer[types.PeerID(peer.ID)]})
	}
	return input, nil
}

func replaceRelayRelations(tx *gorm.DB, accountID types.AccountID, candidate sharedrelay.Configuration) error {
	for _, model := range []any{&types.RelayGroup{}, &types.RelayHomeProfile{}, &types.RelayPairPolicy{}} {
		if err := tx.Where("account_id = ?", accountID).Delete(model).Error; err != nil {
			return err
		}
	}
	if len(candidate.Groups) > 0 {
		if err := tx.Create(&candidate.Groups).Error; err != nil {
			return err
		}
	}
	if len(candidate.Profiles) > 0 {
		if err := tx.Create(&candidate.Profiles).Error; err != nil {
			return err
		}
	}
	if len(candidate.Policies) > 0 {
		return tx.Create(&candidate.Policies).Error
	}
	return nil
}

func advanceRelayEntityRevisions(candidate *sharedrelay.Configuration, current sharedrelay.Configuration) {
	groups := make(map[types.RelayGroupID]types.RelayGroup, len(current.Groups))
	for _, group := range current.Groups {
		groups[group.ID] = group
	}
	for i, group := range candidate.Groups {
		old, exists := groups[group.ID]
		group.Revision = old.Revision
		if !exists || !reflect.DeepEqual(group, old) {
			group.Revision++
		}
		candidate.Groups[i] = group
	}
	profiles := make(map[types.RelayProfileID]types.RelayHomeProfile, len(current.Profiles))
	for _, profile := range current.Profiles {
		profiles[profile.ID] = profile
	}
	for i, profile := range candidate.Profiles {
		old, exists := profiles[profile.ID]
		profile.Revision = old.Revision
		if !exists || !reflect.DeepEqual(profile, old) {
			profile.Revision++
		}
		candidate.Profiles[i] = profile
	}
	policies := make(map[types.RelayPolicyID]types.RelayPairPolicy, len(current.Policies))
	for _, policy := range current.Policies {
		policies[policy.ID] = policy
	}
	for i, policy := range candidate.Policies {
		old, exists := policies[policy.ID]
		policy.Revision = old.Revision
		if !exists || !reflect.DeepEqual(policy, old) {
			policy.Revision++
		}
		candidate.Policies[i] = policy
	}
}

// AdvanceRelayReselection increments only the selected home or unordered-pair directive and network serial.
// Manager callers additionally validate authorization, effective policy revision, capability and rate limits.
func (s *SqlStore) AdvanceRelayReselection(ctx context.Context, accountID types.AccountID, a, b types.PeerID, expectedRevision uint64) (uint64, error) {
	if a == "" || a == b {
		return 0, fmt.Errorf("invalid relay reselection target")
	}
	if b != "" && a > b {
		a, b = b, a
	}
	var generation uint64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		// Lock publication before account to keep a consistent lock order with policy writes.
		var head relayCatalogHead
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", 1).Take(&head).Error; err != nil {
			return err
		}
		result := tx.Model(&types.Account{}).Where("id = ?", accountID).
			Update("network_serial", gorm.Expr("network_serial + 1"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		var state types.RelayAccountState
		if err := tx.Where("account_id = ?", accountID).Take(&state).Error; err != nil {
			return err
		}
		if state.Revision != expectedRevision {
			return ErrRelayRevisionConflict
		}
		for _, id := range []types.PeerID{a, b} {
			if id == "" {
				continue
			}
			var count int64
			if err := tx.Model(&nbpeer.Peer{}).Where("account_id = ? AND id = ?", accountID, id).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return gorm.ErrRecordNotFound
			}
		}
		directive := types.RelayReselection{AccountID: accountID, PeerA: a, PeerB: b}
		query := tx.Where("account_id = ? AND peer_a = ? AND peer_b = ?", accountID, a, b)
		if err := query.Find(&directive).Error; err != nil {
			return err
		}
		if directive.Generation >= math.MaxInt64 {
			return ErrRelayRevisionConflict
		}
		directive.Generation++
		generation = directive.Generation
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&directive).Error
	})
	return generation, err
}

func (s *SqlStore) getRelaySnapshotForAccount(ctx context.Context, accountID types.AccountID, native bool) (sharedrelay.Snapshot, error) {
	if !native {
		return s.GetRelaySnapshot(ctx, accountID)
	}
	tx, err := s.pgxPool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return sharedrelay.Snapshot{}, err
	}
	snapshot, err := relaypolicydb.ReadSnapshot(ctx, func(ctx context.Context, sql string, args ...any) (relaypolicydb.Rows, error) {
		return tx.Query(ctx, sql, args...)
	}, accountID)
	if err != nil {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			return sharedrelay.Snapshot{}, errors.Join(err, fmt.Errorf("rollback relay snapshot read: %w", rollbackErr))
		}
		return sharedrelay.Snapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sharedrelay.Snapshot{}, err
	}
	return snapshot, nil
}

func deleteRelayAccountConfig(tx *gorm.DB, accountID string) error {
	for _, model := range []any{&types.RelayGroup{}, &types.RelayHomeProfile{}, &types.RelayPairPolicy{}, &types.RelayReselection{}, &types.RelayAccountState{}} {
		if err := tx.Where("account_id = ?", accountID).Delete(model).Error; err != nil {
			return fmt.Errorf("delete account relay configuration: %w", err)
		}
	}
	return nil
}

func deleteRelayPeerConfig(tx *gorm.DB, accountID, peerID string) error {
	if err := tx.Model(&types.Account{}).Where("id = ?", accountID).
		Update("network_serial", gorm.Expr("network_serial")).Error; err != nil {
		return err
	}
	result := tx.Where("account_id = ? AND (peer_a = ? OR peer_b = ?)", accountID, peerID, peerID).
		Delete(&types.RelayReselection{})
	if result.Error != nil {
		return result.Error
	}
	changed := result.RowsAffected > 0
	var profiles []types.RelayHomeProfile
	if err := tx.Where("account_id = ?", accountID).Find(&profiles).Error; err != nil {
		return err
	}
	for _, profile := range profiles {
		before := len(profile.Selector.PeerIDs)
		profile.Selector.PeerIDs = slices.DeleteFunc(profile.Selector.PeerIDs, func(id types.PeerID) bool { return id == types.PeerID(peerID) })
		if len(profile.Selector.PeerIDs) == before {
			continue
		}
		changed = true
		if len(profile.Selector.PeerIDs)+len(profile.Selector.GroupIDs) == 0 {
			if err := tx.Delete(&profile).Error; err != nil {
				return err
			}
			continue
		}
		profile.Revision++
		if err := tx.Save(&profile).Error; err != nil {
			return err
		}
	}
	var policies []types.RelayPairPolicy
	if err := tx.Where("account_id = ?", accountID).Find(&policies).Error; err != nil {
		return err
	}
	for _, policy := range policies {
		before := len(policy.Source.PeerIDs) + len(policy.Destination.PeerIDs)
		policy.Source.PeerIDs = slices.DeleteFunc(policy.Source.PeerIDs, func(id types.PeerID) bool { return id == types.PeerID(peerID) })
		policy.Destination.PeerIDs = slices.DeleteFunc(policy.Destination.PeerIDs, func(id types.PeerID) bool { return id == types.PeerID(peerID) })
		if len(policy.Source.PeerIDs)+len(policy.Destination.PeerIDs) == before {
			continue
		}
		changed = true
		if len(policy.Source.PeerIDs)+len(policy.Source.GroupIDs) == 0 || len(policy.Destination.PeerIDs)+len(policy.Destination.GroupIDs) == 0 {
			if err := tx.Delete(&policy).Error; err != nil {
				return err
			}
			continue
		}
		policy.Revision++
		if err := tx.Save(&policy).Error; err != nil {
			return err
		}
	}
	if !changed {
		return nil
	}
	if err := tx.Model(&types.RelayAccountState{}).Where("account_id = ?", accountID).
		Update("revision", gorm.Expr("revision + 1")).Error; err != nil {
		return err
	}
	return tx.Model(&types.Account{}).Where("id = ?", accountID).
		Update("network_serial", gorm.Expr("network_serial + 1")).Error
}
