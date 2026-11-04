package migration

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// MigrateRelayPolicySchema adds relay relations without creating configuration for existing accounts.
// An absent account-state row preserves the legacy, unconfigured behavior.
func MigrateRelayPolicySchema(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).AutoMigrate(
		&relaypolicy.RelayGroup{}, &relaypolicy.RelayHomeProfile{}, &relaypolicy.RelayPairPolicy{},
		&relaypolicy.RelayAccountState{}, &relaypolicy.RelayReselection{},
		&relaypolicy.CatalogHead{}, &relaypolicy.CatalogPublication{},
	); err != nil {
		return fmt.Errorf("migrate relay policy relations: %w", err)
	}
	return nil
}
