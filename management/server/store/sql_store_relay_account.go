package store

import (
	"context"
	"errors"

	"github.com/netbirdio/netbird/management/server/types"
)

// GetAccount reads the account together with its relay policy snapshot. The two reads are separate, so a
// concurrent network serial bump can split them. The read repeats a few times until both agree.
func (s *SqlStore) GetAccount(ctx context.Context, accountID string) (*types.Account, error) {
	for range 3 {
		account, err := s.getAccountOnce(ctx, accountID)
		if !errors.Is(err, errRelayAccountSnapshotChanged) {
			return account, err
		}
	}
	return nil, errRelayAccountSnapshotChanged
}

var errRelayAccountSnapshotChanged = errors.New("account serial changed while reading relay policy snapshot")

func (s *SqlStore) attachRelayAccountSnapshot(ctx context.Context, account *types.Account, native bool) error {
	snapshot, err := s.getRelaySnapshotForAccount(ctx, types.AccountID(account.Id), native)
	if err != nil {
		return err
	}
	if account.Network != nil && account.Network.Serial != snapshot.NetworkSerial {
		return errRelayAccountSnapshotChanged
	}
	account.RelayGroups, account.RelayHomeProfiles, account.RelayPairPolicies = snapshot.Groups, snapshot.Profiles, snapshot.Policies
	account.RelayReselections = snapshot.Reselections
	account.RelayState = nil
	if snapshot.Revision != 0 {
		account.RelayState = &types.RelayAccountState{AccountID: types.AccountID(account.Id), Revision: snapshot.Revision}
	}
	account.RelayCatalog = nil
	if snapshot.Catalog.Generation != 0 {
		account.RelayCatalog = &snapshot.Catalog
	}
	return nil
}
