package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/netbirdio/netbird/management/server/relaypolicy"
	"github.com/netbirdio/netbird/management/server/types"
	sharedrelay "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// ErrRelayRevisionConflict indicates a stale configuration or catalog publication.
var ErrRelayRevisionConflict = errors.New("relay revision conflict")

type relayCatalogHead = sharedrelay.CatalogHead

type relayCatalogPublication = sharedrelay.CatalogPublication

// RelayCatalogImpact identifies accounts and relay identities affected by publication.
type RelayCatalogImpact struct {
	Generation     uint64
	NextGeneration uint64
	AccountIDs     []types.AccountID
	Retired        []types.RelayID
	Replaced       []types.RelayID
}

// GetRelayCatalog reads the active immutable catalog, returning gorm.ErrRecordNotFound before initialization.
func (s *SqlStore) GetRelayCatalog(ctx context.Context) (types.RelayCatalog, error) {
	return getRelayCatalog(s.db.WithContext(ctx))
}

func getRelayCatalog(tx *gorm.DB) (types.RelayCatalog, error) {
	var publication relayCatalogPublication
	err := tx.Table("relay_catalog_publications").
		Select("relay_catalog_publications.*").
		Joins("JOIN relay_catalog_heads ON relay_catalog_heads.generation = relay_catalog_publications.generation").
		Where("relay_catalog_heads.id = ?", 1).Take(&publication).Error
	if err != nil {
		return types.RelayCatalog{}, err
	}
	return types.RelayCatalog(publication), nil
}

// InitializeRelayCatalog creates only an absent catalog and never republishes local configuration.
func (s *SqlStore) InitializeRelayCatalog(ctx context.Context, candidate types.RelayCatalog) (types.RelayCatalog, error) {
	catalog, err := relaypolicy.ValidateCatalog(candidate)
	if err != nil {
		return types.RelayCatalog{}, err
	}
	catalog.Generation = 1
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&relayCatalogHead{ID: 1, Generation: 1})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var err error
			catalog, err = getRelayCatalog(tx)
			return err
		}
		return tx.Create(&relayCatalogPublication{Generation: 1, Entries: catalog.Entries}).Error
	})
	return catalog, err
}

// PreviewRelayCatalog computes publication impact without mutating catalog or account serials.
func (s *SqlStore) PreviewRelayCatalog(ctx context.Context, candidate types.RelayCatalog, expectedGeneration uint64) (*RelayCatalogImpact, error) {
	canonical, err := relaypolicy.ValidateCatalog(candidate)
	if err != nil {
		return nil, err
	}
	var impact *RelayCatalogImpact
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var err error
		impact, err = relayCatalogImpact(tx, canonical, expectedGeneration)
		return err
	})
	return impact, err
}

func relayCatalogImpact(tx *gorm.DB, candidate types.RelayCatalog, expected uint64) (*RelayCatalogImpact, error) {
	current, err := getRelayCatalog(tx)
	if err != nil {
		return nil, err
	}
	if current.Generation != expected || expected >= math.MaxInt64 {
		return nil, ErrRelayRevisionConflict
	}
	if err := validateRelayCatalogReferences(tx, candidate); err != nil {
		return nil, err
	}
	impact := &RelayCatalogImpact{Generation: expected, NextGeneration: expected + 1}
	next := make(map[types.RelayID]types.RelayCatalogEntry, len(candidate.Entries))
	for _, entry := range candidate.Entries {
		next[entry.ID] = entry
	}
	for _, entry := range current.Entries {
		updated, exists := next[entry.ID]
		if !exists || (!updated.Active && entry.Active) {
			impact.Retired = append(impact.Retired, entry.ID)
		}
		if exists && !reflect.DeepEqual(entry, updated) {
			impact.Replaced = append(impact.Replaced, entry.ID)
		}
	}
	// Every configured account depends on the catalog's active universe, including omitted allow sets.
	err = tx.Model(&types.RelayAccountState{}).Order("account_id").Pluck("account_id", &impact.AccountIDs).Error
	if err != nil {
		return nil, fmt.Errorf("read relay catalog affected accounts: %w", err)
	}
	slices.Sort(impact.Retired)
	slices.Sort(impact.Replaced)
	return impact, nil
}

// PublishRelayCatalog atomically publishes a new immutable generation and advances affected account serials.
func (s *SqlStore) PublishRelayCatalog(ctx context.Context, candidate types.RelayCatalog, expectedGeneration uint64) (types.RelayCatalog, error) {
	catalog, err := relaypolicy.ValidateCatalog(candidate)
	if err != nil {
		return types.RelayCatalog{}, err
	}
	if expectedGeneration >= math.MaxInt64 {
		return types.RelayCatalog{}, ErrRelayRevisionConflict
	}
	catalog.Generation = expectedGeneration + 1
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		result := tx.Model(&relayCatalogHead{}).Where("id = ? AND generation = ?", 1, expectedGeneration).
			Update("generation", catalog.Generation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayRevisionConflict
		}
		if err := validateRelayCatalogReferences(tx, catalog); err != nil {
			return err
		}
		if err := tx.Create(&relayCatalogPublication{Generation: catalog.Generation, Entries: catalog.Entries}).Error; err != nil {
			return fmt.Errorf("save relay catalog publication: %w", err)
		}
		return tx.Model(&types.Account{}).
			Where("id IN (?)", tx.Model(&types.RelayAccountState{}).Select("account_id")).
			Update("network_serial", gorm.Expr("COALESCE(network_serial, 0) + 1")).Error
	})
	if err != nil {
		return types.RelayCatalog{}, err
	}
	return catalog, nil
}

func validateRelayCatalogReferences(tx *gorm.DB, candidate types.RelayCatalog) error {
	available := make(map[types.RelayID]struct{}, len(candidate.Entries))
	for _, entry := range candidate.Entries {
		if !entry.Legacy {
			available[entry.ID] = struct{}{}
		}
	}
	validate := func(ids []types.RelayID) error {
		for _, id := range ids {
			if _, exists := available[id]; !exists {
				return fmt.Errorf("relay %s is referenced; retire it without removing its identity", id)
			}
		}
		return nil
	}
	var groups []types.RelayGroup
	if err := tx.Find(&groups).Error; err != nil {
		return err
	}
	for _, group := range groups {
		if err := validate(group.RelayIDs); err != nil {
			return err
		}
	}
	validateConstraints := func(c types.RelayConstraints) error {
		if c.Allow != nil {
			if err := validate(c.Allow.RelayIDs); err != nil {
				return err
			}
		}
		if err := validate(c.Exclude.RelayIDs); err != nil {
			return err
		}
		for _, tier := range c.Preferences {
			if err := validate(tier.RelayIDs); err != nil {
				return err
			}
		}
		return nil
	}
	var profiles []types.RelayHomeProfile
	if err := tx.Find(&profiles).Error; err != nil {
		return err
	}
	for _, profile := range profiles {
		if err := validateConstraints(profile.Constraints); err != nil {
			return err
		}
	}
	var policies []types.RelayPairPolicy
	if err := tx.Find(&policies).Error; err != nil {
		return err
	}
	for _, policy := range policies {
		if err := validateConstraints(policy.Constraints); err != nil {
			return err
		}
	}
	return nil
}
