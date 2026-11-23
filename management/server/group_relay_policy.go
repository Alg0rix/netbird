package server

import (
	"context"

	relayadmin "github.com/netbirdio/netbird/management/server/relaypolicy/admin"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
)

// executeGroupTransaction runs a group mutation in a transaction that also serializes with relay policy
// changes. It locks the relay configuration first and validates the resulting account last, so a group
// change can never leave a relay rule without peers or relays. Any failure rolls back the whole mutation.
func (am *DefaultAccountManager) executeGroupTransaction(ctx context.Context, accountID string, fn func(store.Store) error) error {
	return am.Store.ExecuteInTransaction(ctx, func(transaction store.Store) error {
		if err := lockRelayGroupMutation(ctx, transaction, accountID); err != nil {
			return err
		}
		if err := fn(transaction); err != nil {
			return err
		}
		return validateRelayGroupMutation(ctx, transaction, accountID)
	})
}

func lockRelayGroupMutation(ctx context.Context, transaction store.Store, accountID string) error {
	repository, ok := transaction.(store.RelayPolicyStore)
	if !ok {
		return nil
	}
	return repository.LockRelayConfiguration(ctx, types.AccountID(accountID))
}

func validateRelayGroupMutation(ctx context.Context, transaction store.Store, accountID string) error {
	repository, ok := transaction.(store.RelayPolicyStore)
	if !ok {
		return nil
	}
	snapshot, err := repository.GetRelaySnapshot(ctx, types.AccountID(accountID))
	if err != nil {
		return err
	}
	if len(snapshot.Profiles) == 0 && len(snapshot.Policies) == 0 {
		return nil
	}
	account, err := transaction.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	return relayadmin.ValidateAccount(ctx, account)
}
