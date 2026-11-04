package store

import (
	"context"

	"github.com/netbirdio/netbird/management/server/types"
)

// GetRelayPolicyAccountSerials reads invalidation fences, including accounts whose policies were cleared.
func (s *SqlStore) GetRelayPolicyAccountSerials(ctx context.Context) (map[types.AccountID]uint64, error) {
	var rows []struct {
		AccountID types.AccountID
		Serial    uint64
	}
	if err := s.db.WithContext(ctx).Table("relay_account_states").
		Select("relay_account_states.account_id, accounts.network_serial AS serial").
		Joins("JOIN accounts ON accounts.id = relay_account_states.account_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	serials := make(map[types.AccountID]uint64, len(rows))
	for _, row := range rows {
		serials[row.AccountID] = row.Serial
	}
	return serials, nil
}
