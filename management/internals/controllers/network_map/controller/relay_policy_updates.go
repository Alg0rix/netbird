package controller

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/netbirdio/netbird/management/server/types"
)

const relayPolicyPollInterval = 5 * time.Second
const relayPolicyPollTimeout = 3 * time.Second

type relayPolicySerialReader interface {
	GetRelayPolicyAccountSerials(context.Context) (map[types.AccountID]uint64, error)
}

// StartRelayPolicyUpdates observes shared policy invalidation fences until stopped.
// The returned idempotent callback cancels in-flight reads and waits for the watcher.
func (c *Controller) StartRelayPolicyUpdates(parent context.Context) func() {
	reader := c.relayPolicySerials
	if reader == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(relayPolicyPollInterval)
		defer ticker.Stop()
		observed := make(map[types.AccountID]uint64)
		for {
			if ctx.Err() != nil {
				return
			}
			c.refreshRelayPolicyUpdates(ctx, reader, observed)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	var stop sync.Once
	return func() { stop.Do(func() { cancel(); <-done }) }
}

func (c *Controller) refreshRelayPolicyUpdates(ctx context.Context, reader relayPolicySerialReader, observed map[types.AccountID]uint64) {
	readCtx, cancel := context.WithTimeout(ctx, relayPolicyPollTimeout)
	serials, err := reader.GetRelayPolicyAccountSerials(readCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			log.WithContext(ctx).Debugf("read relay policy update fences: %v", err)
		}
		return
	}
	for accountID := range observed {
		if _, exists := serials[accountID]; !exists {
			delete(observed, accountID)
		}
	}
	for accountID, serial := range serials {
		if previous, exists := observed[accountID]; exists && previous == serial {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		updateCtx, cancel := context.WithTimeout(ctx, relayPolicyPollInterval)
		err := c.sendUpdateAccountPeers(updateCtx, string(accountID), types.UpdateReason{Resource: types.UpdateResourcePolicy, Operation: types.UpdateOperationUpdate})
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.WithContext(ctx).Debugf("distribute relay policy update: %v", err)
			}
			continue
		}
		observed[accountID] = serial
	}
}
