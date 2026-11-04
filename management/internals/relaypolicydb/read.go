// Package relaypolicydb reads relay snapshots through the SQL and pgx transaction adapters.
package relaypolicydb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// Rows is the common read-only cursor surface of database/sql and pgx.
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

// Query reads rows on the caller's existing transaction or connection.
type Query func(context.Context, string, ...any) (Rows, error)

// ReadSnapshot loads catalog, rules and targeted retries from one caller-owned snapshot.
func ReadSnapshot(ctx context.Context, query Query, accountID relaypolicy.AccountID) (relaypolicy.Snapshot, error) {
	snapshot := relaypolicy.Snapshot{AccountID: accountID}
	if err := each(ctx, query, `SELECT p.generation, p.entries FROM relay_catalog_publications p
		JOIN relay_catalog_heads h ON h.generation = p.generation WHERE h.id = 1`, nil, func(rows Rows) error {
		var entries []byte
		if err := rows.Scan(&snapshot.Catalog.Generation, &entries); err != nil {
			return err
		}
		return decode(entries, &snapshot.Catalog.Entries)
	}); err != nil {
		return snapshot, fmt.Errorf("read active relay catalog: %w", err)
	}
	args := []any{string(accountID)}
	foundAccount := false
	if err := each(ctx, query, `SELECT network_serial FROM accounts WHERE id = $1`, args, func(rows Rows) error {
		foundAccount = true
		return rows.Scan(&snapshot.NetworkSerial)
	}); err != nil {
		return snapshot, err
	}
	if !foundAccount {
		return snapshot, fmt.Errorf("relay snapshot account not found")
	}
	if err := each(ctx, query, `SELECT revision FROM relay_account_states WHERE account_id = $1`, args, func(rows Rows) error {
		return rows.Scan(&snapshot.Revision)
	}); err != nil {
		return snapshot, err
	}
	if err := each(ctx, query, `SELECT id, account_id, name, revision, relay_ids FROM relay_groups WHERE account_id = $1 ORDER BY id`, args, func(rows Rows) error {
		var group relaypolicy.RelayGroup
		var ids []byte
		if err := rows.Scan(&group.ID, &group.AccountID, &group.Name, &group.Revision, &ids); err != nil {
			return err
		}
		if err := decode(ids, &group.RelayIDs); err != nil {
			return err
		}
		snapshot.Groups = append(snapshot.Groups, group)
		return nil
	}); err != nil {
		return snapshot, err
	}
	if err := each(ctx, query, `SELECT id, account_id, name, revision, priority, enabled, selector, constraints FROM relay_home_profiles WHERE account_id = $1 ORDER BY id`, args, func(rows Rows) error {
		var profile relaypolicy.RelayHomeProfile
		var selector, constraints []byte
		if err := rows.Scan(&profile.ID, &profile.AccountID, &profile.Name, &profile.Revision, &profile.Priority,
			&profile.Enabled, &selector, &constraints); err != nil {
			return err
		}
		if err := decode(selector, &profile.Selector); err != nil {
			return err
		}
		if err := decode(constraints, &profile.Constraints); err != nil {
			return err
		}
		snapshot.Profiles = append(snapshot.Profiles, profile)
		return nil
	}); err != nil {
		return snapshot, err
	}
	if err := each(ctx, query, `SELECT id, account_id, name, revision, priority, enabled, source, destination, constraints FROM relay_pair_policies WHERE account_id = $1 ORDER BY id`, args, func(rows Rows) error {
		var policy relaypolicy.RelayPairPolicy
		var source, destination, constraints []byte
		if err := rows.Scan(&policy.ID, &policy.AccountID, &policy.Name, &policy.Revision, &policy.Priority,
			&policy.Enabled, &source, &destination, &constraints); err != nil {
			return err
		}
		if err := decode(source, &policy.Source); err != nil {
			return err
		}
		if err := decode(destination, &policy.Destination); err != nil {
			return err
		}
		if err := decode(constraints, &policy.Constraints); err != nil {
			return err
		}
		snapshot.Policies = append(snapshot.Policies, policy)
		return nil
	}); err != nil {
		return snapshot, err
	}
	if err := each(ctx, query, `SELECT account_id, peer_a, peer_b, generation, last_requested_at FROM relay_reselections WHERE account_id = $1 ORDER BY peer_a, peer_b`, args, func(rows Rows) error {
		var directive relaypolicy.RelayReselection
		var requestedAt *time.Time
		if err := rows.Scan(&directive.AccountID, &directive.PeerA, &directive.PeerB, &directive.Generation, &requestedAt); err != nil {
			return err
		}
		if requestedAt != nil {
			directive.LastRequestedAt = *requestedAt
		}
		snapshot.Reselections = append(snapshot.Reselections, directive)
		return nil
	}); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func each(ctx context.Context, query Query, sql string, args []any, scan func(Rows) error) error {
	rows, err := query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func decode(data []byte, target any) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, target)
}
