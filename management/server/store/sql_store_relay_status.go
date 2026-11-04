package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/types"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

// RelayObservation stores only the latest report of one authenticated endpoint and target.
type RelayObservation = rp.ObservationRecord

// RelayObservationStore is the SQL latest-state repository used by authenticated reporting.
type RelayObservationStore interface {
	PutRelayObservations(context.Context, rp.AccountID, rp.PeerID, int64, []rp.PathReport) error
	GetRelayObservations(context.Context, rp.AccountID, rp.PeerID, rp.PeerID) ([]rp.EndpointObservation, error)
	PruneRelayObservations(context.Context, int) error
}

// PutRelayObservations fences ownership and report sequence within the same database transaction.
func (s *SqlStore) PutRelayObservations(ctx context.Context, accountID rp.AccountID, local rp.PeerID, session int64, reports []rp.PathReport) error {
	if session <= 0 || len(reports) == 0 || len(reports) > rp.MaxReports {
		return status.Errorf(status.InvalidArgument, "invalid relay report session or batch size")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var owner nbpeer.Connection
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("account_id = ? AND peer_id = ? AND session_started_at = ? AND instance_id IS NOT NULL", accountID, local, session).Take(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Errorf(status.PreconditionFailed, "relay report sync session is no longer current")
			}
			return err
		}
		threshold, arg := s.staleThreshold()
		var instance nbpeer.ManagementInstance
		if err := tx.Where("id = ? AND last_seen > "+threshold, *owner.InstanceID, arg).Take(&instance).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return status.Errorf(status.PreconditionFailed, "relay report owner is stale")
			}
			return err
		}
		now, err := s.relayDatabaseTime(tx)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&RelayObservation{}).Where("account_id = ? AND local_peer_id = ?", accountID, local).Count(&count).Error; err != nil {
			return err
		}
		seen := make(map[rp.PeerID]struct{}, len(reports))
		for _, report := range reports {
			if report.Sequence == 0 || report.Sequence > math.MaxInt64 {
				return status.Errorf(status.InvalidArgument, "invalid relay report sequence")
			}
			if _, exists := seen[report.RemotePeerID]; exists {
				return status.Errorf(status.InvalidArgument, "duplicate relay report target")
			}
			seen[report.RemotePeerID] = struct{}{}
			var previous RelayObservation
			query := tx.Where("account_id = ? AND local_peer_id = ? AND remote_peer_id = ?", accountID, local, report.RemotePeerID)
			err := query.Take(&previous).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && previous.Session == session && previous.Sequence >= report.Sequence {
				if previous.Sequence == report.Sequence && reflect.DeepEqual(previous.Report, report) {
					continue
				}
				return status.Errorf(status.PreconditionFailed, "relay report sequence is stale")
			}
			if err == nil && previous.Session == session && now.Sub(previous.ObservedAt) < time.Second {
				return status.Errorf(status.TooManyRequests, "relay reports are limited to once per target per second")
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				count++
				if count > rp.MaxObservationsPerPeer {
					return status.Errorf(status.TooManyRequests, "relay observation target limit exceeded")
				}
			}
			row := RelayObservation{AccountID: accountID, LocalPeerID: local, RemotePeerID: report.RemotePeerID,
				Session: session, InstanceID: *owner.InstanceID, Sequence: report.Sequence, Report: report,
				ObservedAt: now, ExpiresAt: now.Add(rp.ReportExpiry)}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
				return fmt.Errorf("save relay observation: %w", err)
			}
		}
		return nil
	})
}

func (s *SqlStore) relayDatabaseTime(tx *gorm.DB) (time.Time, error) {
	query := "SELECT CAST(FLOOR(EXTRACT(EPOCH FROM clock_timestamp())) AS BIGINT)"
	if s.conn.Engine() == types.SqliteStoreEngine {
		query = "SELECT CAST(strftime('%s','now') AS INTEGER)"
	}
	var epoch int64
	if err := tx.Raw(query).Scan(&epoch).Error; err != nil {
		return time.Time{}, fmt.Errorf("read relay observation clock: %w", err)
	}
	return time.Unix(epoch, 0).UTC(), nil
}

// GetRelayObservations reads both endpoints through current session and instance ownership fences.
func (s *SqlStore) GetRelayObservations(ctx context.Context, accountID rp.AccountID, a, b rp.PeerID) ([]rp.EndpointObservation, error) {
	threshold, arg := s.staleThreshold()
	type record struct {
		RelayObservation
		Current bool
	}
	var rows []record
	query := s.db.WithContext(ctx).Table("relay_observations AS ro").
		Select("ro.*, CASE WHEN ro.expires_at > CURRENT_TIMESTAMP AND pc.session_started_at = ro.session AND pc.instance_id = ro.instance_id AND mi.last_seen > "+threshold+" THEN true ELSE false END AS current", arg).
		Joins("LEFT JOIN peer_connections pc ON pc.peer_id = ro.local_peer_id AND pc.account_id = ro.account_id").
		Joins("LEFT JOIN management_instances mi ON mi.id = pc.instance_id").
		Where("ro.account_id = ?", accountID)
	if b == "" {
		query = query.Where("ro.local_peer_id = ? AND ro.remote_peer_id = ?", a, "")
	} else {
		query = query.Where("(ro.local_peer_id = ? AND ro.remote_peer_id = ?) OR (ro.local_peer_id = ? AND ro.remote_peer_id = ?)", a, b, b, a)
	}
	if err := query.Order("ro.local_peer_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("read relay observations: %w", err)
	}
	observations := make([]rp.EndpointObservation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, rp.EndpointObservation{LocalPeerID: row.LocalPeerID, Report: row.Report,
			ObservedAt: row.ObservedAt, ExpiresAt: row.ExpiresAt, Current: row.Current})
	}
	return observations, nil
}

// PruneRelayObservations removes a bounded batch of expired rows without retaining an event history.
func (s *SqlStore) PruneRelayObservations(ctx context.Context, limit int) error {
	if limit <= 0 || limit > rp.MaxObservationsPerPeer {
		return status.Errorf(status.InvalidArgument, "invalid relay observation pruning limit")
	}
	threshold, arg := s.staleThreshold()
	// Keep current-session watermarks even after expiry so old sequences cannot revive them.
	return s.db.WithContext(ctx).Where(`(account_id, local_peer_id, remote_peer_id) IN (
		SELECT ro.account_id, ro.local_peer_id, ro.remote_peer_id FROM relay_observations ro
		WHERE ro.expires_at <= CURRENT_TIMESTAMP AND NOT EXISTS (
			SELECT 1 FROM peer_connections pc JOIN management_instances mi ON mi.id = pc.instance_id
			WHERE pc.account_id = ro.account_id AND pc.peer_id = ro.local_peer_id
			AND pc.session_started_at = ro.session AND pc.instance_id = ro.instance_id
			AND mi.last_seen > `+threshold+`)
		ORDER BY ro.expires_at LIMIT ?)`, arg, limit).Delete(&RelayObservation{}).Error
}

var _ RelayObservationStore = (*SqlStore)(nil)
