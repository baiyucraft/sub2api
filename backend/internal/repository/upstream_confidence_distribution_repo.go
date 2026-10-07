package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const confidenceDistributionCASAttempts = 32

var _ service.UpstreamConfidenceDistributionRepository = (*upstreamConfigRepository)(nil)

type confidenceDistributionSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type confidenceDistributionWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Use the caller's Ent driver, including its transaction, for retention writes.
// Taking a standalone *sql.DB here would bypass the health cleanup transaction.
type confidenceDistributionDriverWriter struct{ driver dialect.Driver }

func (w confidenceDistributionDriverWriter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := w.driver.Exec(ctx, query, args, &result)
	return result, err
}

func (r *upstreamConfigRepository) confidenceDistributionDB() (*sql.DB, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("confidence distribution database unavailable")
	}
	driver, ok := r.client.Driver().(*entsql.Driver)
	if !ok {
		return nil, errors.New("confidence distribution SQL driver unavailable")
	}
	return driver.DB(), nil
}

func readConfidenceDistributionState(ctx context.Context, db confidenceDistributionSQL, keyID int64) (*service.ConfidenceDistributionState, int64, error) {
	var raw []byte
	var revision int64
	err := db.QueryRowContext(ctx, `SELECT state_json, revision FROM upstream_confidence_distribution_states WHERE upstream_key_id=$1`, keyID).Scan(&raw, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	state := new(service.ConfidenceDistributionState)
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, 0, fmt.Errorf("decode confidence distribution state: %w", err)
	}
	if err := state.Validate(); err != nil {
		return nil, 0, err
	}
	return state, revision, nil
}

func saveConfidenceDistributionState(ctx context.Context, db confidenceDistributionWriter, keyID, revision int64, state *service.ConfidenceDistributionState, now time.Time) (bool, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	if revision == 0 {
		result, err := db.ExecContext(ctx, `INSERT INTO upstream_confidence_distribution_states (upstream_key_id, revision, state_json, updated_at) VALUES ($1,1,$2,$3) ON CONFLICT (upstream_key_id) DO NOTHING`, keyID, string(raw), now.UTC())
		return confidenceDistributionChanged(result, err)
	}
	result, err := db.ExecContext(ctx, `UPDATE upstream_confidence_distribution_states SET state_json=$1, revision=revision+1, updated_at=$2 WHERE upstream_key_id=$3 AND revision=$4`, string(raw), now.UTC(), keyID, revision)
	return confidenceDistributionChanged(result, err)
}

func cleanupExpiredConfidenceDistributionStates(ctx context.Context, client *dbent.Client, now time.Time) error {
	driver := client.Driver()
	var rows entsql.Rows
	query := `SELECT upstream_key_id, state_json, revision FROM upstream_confidence_distribution_states ORDER BY upstream_key_id`
	if driver.Dialect() == dialect.Postgres {
		// Health cleanup already holds its Key row. Do not wait for a Finish
		// transaction which may need that Key for its event's foreign key check.
		query += ` FOR UPDATE SKIP LOCKED`
	}
	if err := driver.Query(ctx, query, []any{}, &rows); err != nil {
		// Older SQLite unit fixtures use Ent's generated schema, which does not
		// contain this raw migration table. Never hide a missing production table.
		if driver.Dialect() == dialect.SQLite && strings.Contains(err.Error(), "no such table: upstream_confidence_distribution_states") {
			return nil
		}
		return err
	}
	type expiredState struct {
		keyID, revision int64
		state           *service.ConfidenceDistributionState
	}
	var changed []expiredState
	for rows.Next() {
		var item expiredState
		var raw []byte
		if err := rows.Scan(&item.keyID, &raw, &item.revision); err != nil {
			_ = rows.Close()
			return err
		}
		item.state = new(service.ConfidenceDistributionState)
		if err := json.Unmarshal(raw, item.state); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode confidence distribution retention state: %w", err)
		}
		if err := item.state.Validate(); err != nil {
			_ = rows.Close()
			return err
		}
		if item.state.Recover(now) {
			changed = append(changed, item)
		}
	}
	err := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	writer := confidenceDistributionDriverWriter{driver: driver}
	for _, item := range changed {
		// A newer Claim/Finish wins a revision race and applies its own recovery.
		// The cleanup snapshot must never erase that newer attempt or result.
		if _, err := saveConfidenceDistributionState(ctx, writer, item.keyID, item.revision, item.state, now); err != nil {
			return err
		}
	}
	return nil
}

func confidenceDistributionChanged(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (r *upstreamConfigRepository) ClaimConfidenceDistribution(ctx context.Context, keyID int64, identity service.ConfidenceDistributionIdentity, now time.Time) (*service.DistributionAttempt, error) {
	if keyID <= 0 {
		return nil, errors.New("confidence distribution key is required")
	}
	db, err := r.confidenceDistributionDB()
	if err != nil {
		return nil, err
	}
	for i := 0; i < confidenceDistributionCASAttempts; i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		state, revision, err := readConfidenceDistributionState(ctx, tx, keyID)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		previousSeries := ""
		if state != nil {
			recovered := state.Recover(now)
			// Identity changes never overwrite a request which still owns its lease.
			if state.Pending != nil {
				if recovered {
					changed, err := saveConfidenceDistributionState(ctx, tx, keyID, revision, state, now)
					if err != nil {
						_ = tx.Rollback()
						return nil, err
					}
					if !changed {
						_ = tx.Rollback()
						continue
					}
					if err := tx.Commit(); err != nil {
						return nil, err
					}
				} else {
					_ = tx.Rollback()
				}
				return nil, nil
			}
			reasons, upgrade := state.IdentityChange(identity)
			if upgrade {
				state.UpgradeIdentity(identity)
			}
			if len(reasons) > 0 {
				previousSeries = state.SeriesID
				previousAttempted := len(state.Samples)
				state, err = service.NewConfidenceDistributionStateForIdentity(identity)
				if err != nil {
					_ = tx.Rollback()
					return nil, err
				}
				at := now.UTC()
				state.SeriesReset = &service.ConfidenceDistributionSeriesReset{At: &at, Reasons: reasons, PreviousAttempted: previousAttempted}
			}
		} else {
			state, err = service.NewConfidenceDistributionStateForIdentity(identity)
			if err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		}
		attempt, err := state.Claim(now)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		changed, err := saveConfidenceDistributionState(ctx, tx, keyID, revision, state, now)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if !changed {
			_ = tx.Rollback()
			continue
		}
		if previousSeries != "" {
			if err := persistConfidenceDistributionResetEvent(ctx, tx, keyID, previousSeries, state.SeriesID, state.SeriesReset, now); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return attempt, nil
	}
	return nil, errors.New("confidence distribution claim contention")
}

func (r *upstreamConfigRepository) FinishConfidenceDistribution(ctx context.Context, keyID int64, attempt *service.DistributionAttempt, sample service.DistributionSample, now time.Time) (*service.UpstreamConfidenceDistribution, bool, error) {
	db, err := r.confidenceDistributionDB()
	if err != nil {
		return nil, false, err
	}
	for i := 0; i < confidenceDistributionCASAttempts; i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, false, err
		}
		state, revision, err := readConfidenceDistributionState(ctx, tx, keyID)
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if state == nil {
			_ = tx.Rollback()
			return nil, false, service.ErrConfidenceDistributionLeaseLost
		}
		summary, alert, err := state.Finish(attempt, sample, now)
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		changed, err := saveConfidenceDistributionState(ctx, tx, keyID, revision, state, now)
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if !changed {
			_ = tx.Rollback()
			continue
		}
		if alert {
			if err := persistConfidenceDistributionEvent(ctx, tx, keyID, state.SeriesID, summary, now); err != nil {
				_ = tx.Rollback()
				return nil, false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return summary, alert, nil
	}
	return nil, false, errors.New("confidence distribution completion contention")
}

func (r *upstreamConfigRepository) LoadConfidenceDistribution(ctx context.Context, keyID int64, now time.Time) (*service.UpstreamConfidenceDistribution, error) {
	return r.loadConfidenceDistribution(ctx, keyID, nil, now)
}

func (r *upstreamConfigRepository) LoadConfidenceDistributionForSeries(ctx context.Context, keyID int64, identity service.ConfidenceDistributionIdentity, now time.Time) (*service.UpstreamConfidenceDistribution, error) {
	return r.loadConfidenceDistribution(ctx, keyID, &identity, now)
}

func (r *upstreamConfigRepository) loadConfidenceDistribution(ctx context.Context, keyID int64, identity *service.ConfidenceDistributionIdentity, now time.Time) (*service.UpstreamConfidenceDistribution, error) {
	db, err := r.confidenceDistributionDB()
	if err != nil {
		return nil, err
	}
	for i := 0; i < confidenceDistributionCASAttempts; i++ {
		state, revision, err := readConfidenceDistributionState(ctx, db, keyID)
		if err != nil || state == nil {
			return nil, err
		}
		changed := state.Recover(now)
		var reasons []string
		if identity != nil {
			var upgrade bool
			reasons, upgrade = state.IdentityChange(*identity)
			if upgrade {
				state.UpgradeIdentity(*identity)
				changed = true
			}
		} else if state.BaselineVersion != service.DistributionBaselineVersion(state.Protocol) {
			return nil, nil
		}
		if changed {
			saved, err := saveConfidenceDistributionState(ctx, db, keyID, revision, state, now)
			if err != nil {
				return nil, err
			}
			if !saved {
				continue
			}
		}
		if len(reasons) > 0 {
			return state.PendingResetSummary(*identity, reasons)
		}
		return state.Summary()
	}
	return nil, errors.New("confidence distribution read contention")
}

func persistConfidenceDistributionResetEvent(ctx context.Context, tx *sql.Tx, keyID int64, previousSeries, seriesID string, reset *service.ConfidenceDistributionSeriesReset, now time.Time) error {
	payload, err := json.Marshal(map[string]any{
		"previous_series_id": previousSeries, "series_id": seriesID,
		"reasons": reset.Reasons, "previous_attempted": reset.PreviousAttempted, "reset_at": reset.At,
	})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO upstream_events
		(upstream_config_id, upstream_key_id, event_type, severity, source, message, payload, occurred_at, created_at)
		SELECT upstream_config_id, id, 'key_confidence_distribution_reset', 'info', 'probe', $1, $2, $3, $3
		FROM upstream_keys WHERE id=$4`, "OpenAI probe distribution request identity changed", string(payload), now.UTC(), keyID)
	changed, err := confidenceDistributionChanged(result, err)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("confidence distribution reset event key unavailable")
	}
	return nil
}

func persistConfidenceDistributionEvent(ctx context.Context, tx *sql.Tx, keyID int64, seriesID string, summary *service.UpstreamConfidenceDistribution, now time.Time) error {
	payload, err := json.Marshal(map[string]any{
		"series_id": seriesID, "status": summary.Status, "closest_model": summary.ClosestModel,
		"valid_samples": summary.ValidSamples, "baseline_version": summary.BaselineVersion,
	})
	if err != nil {
		return err
	}
	severity, message := "info", "OpenAI probe distribution matches Sol"
	if summary.Status == "mismatch" {
		severity, message = "warning", "OpenAI probe distribution does not match Sol"
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO upstream_events
		(upstream_config_id, upstream_key_id, event_type, severity, source, message, payload, occurred_at, created_at)
		SELECT upstream_config_id, id, 'key_confidence_distribution_changed', $1, 'probe', $2, $3, $4, $4
		FROM upstream_keys WHERE id=$5`, severity, message, string(payload), now.UTC(), keyID)
	changed, err := confidenceDistributionChanged(result, err)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("confidence distribution event key unavailable")
	}
	return nil
}
