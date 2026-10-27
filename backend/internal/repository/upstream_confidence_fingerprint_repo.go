package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

var _ service.ConfidenceFingerprintRepository = (*upstreamConfigRepository)(nil)

func (r *upstreamConfigRepository) fingerprintDB() (*sql.DB, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("confidence fingerprint database unavailable")
	}
	driver, ok := r.client.Driver().(*entsql.Driver)
	if !ok {
		return nil, errors.New("confidence fingerprint SQL driver unavailable")
	}
	return driver.DB(), nil
}

func readConfidenceFingerprintState(ctx context.Context, tx *sql.Tx, keyID int64) (*service.ConfidenceFingerprintState, int64, error) {
	var raw []byte
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT state_json, revision FROM upstream_confidence_fingerprint_states WHERE upstream_key_id=$1`, keyID).Scan(&raw, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	state := new(service.ConfidenceFingerprintState)
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, 0, fmt.Errorf("decode confidence fingerprint state: %w", err)
	}
	if err := state.Validate(); err != nil {
		return nil, 0, err
	}
	return state, revision, nil
}

func saveConfidenceFingerprintState(ctx context.Context, tx *sql.Tx, keyID, revision int64, state *service.ConfidenceFingerprintState, now time.Time) (bool, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	if revision == 0 {
		result, err := tx.ExecContext(ctx, `INSERT INTO upstream_confidence_fingerprint_states (upstream_key_id,revision,state_json,updated_at) VALUES ($1,1,$2,$3) ON CONFLICT (upstream_key_id) DO NOTHING`, keyID, string(raw), now.UTC())
		return confidenceDistributionChanged(result, err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE upstream_confidence_fingerprint_states SET state_json=$1,revision=revision+1,updated_at=$2 WHERE upstream_key_id=$3 AND revision=$4`, string(raw), now.UTC(), keyID, revision)
	return confidenceDistributionChanged(result, err)
}

func (r *upstreamConfigRepository) ClaimConfidenceFingerprint(ctx context.Context, keyID int64, identity service.ConfidenceFingerprintIdentity, now time.Time) (*service.ConfidenceFingerprintAttempt, error) {
	if keyID <= 0 || identity.Version != service.ConfidenceFingerprintIdentityVersion || identity.Fingerprint == "" {
		return nil, errors.New("invalid confidence fingerprint claim")
	}
	db, err := r.fingerprintDB()
	if err != nil {
		return nil, err
	}
	for range confidenceDistributionCASAttempts {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		state, revision, err := readConfidenceFingerprintState(ctx, tx, keyID)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if state != nil && !stateIdentityMatches(state, identity) && state.Pending != nil && state.LeaseExpires.After(now) {
			_ = tx.Rollback()
			return nil, nil
		}
		if state == nil || !stateIdentityMatches(state, identity) {
			nextSequence := int64(1)
			if state != nil && state.NextSequence > 0 {
				nextSequence = state.NextSequence
			}
			state, err = service.NewConfidenceFingerprintState(identity)
			if err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			state.NextSequence = nextSequence
		}
		if state.Pending != nil && state.LeaseExpires.After(now) {
			_ = tx.Rollback()
			return nil, nil
		}
		attempt, err := state.Claim(now)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		changed, err := saveConfidenceFingerprintState(ctx, tx, keyID, revision, state, now)
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
		return attempt, nil
	}
	return nil, errors.New("confidence fingerprint claim contention")
}

func stateIdentityMatches(state *service.ConfidenceFingerprintState, identity service.ConfidenceFingerprintIdentity) bool {
	if state == nil || state.Fingerprint != identity.Fingerprint || state.PolicyVersion != service.ConfidenceFingerprintPolicyVersion {
		return false
	}
	return true
}

func (r *upstreamConfigRepository) FinishConfidenceFingerprint(ctx context.Context, keyID int64, attempt *service.ConfidenceFingerprintAttempt, identity service.ConfidenceFingerprintIdentity, evidence service.ConfidenceFingerprintEvidence, now time.Time) error {
	if keyID <= 0 || attempt == nil || identity.Version != service.ConfidenceFingerprintIdentityVersion || identity.Fingerprint == "" {
		return errors.New("confidence fingerprint finish is incomplete")
	}
	db, err := r.fingerprintDB()
	if err != nil {
		return err
	}
	for range confidenceDistributionCASAttempts {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		state, revision, err := readConfidenceFingerprintState(ctx, tx, keyID)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if state == nil {
			_ = tx.Rollback()
			return errors.New("confidence fingerprint state missing")
		}
		if err := state.Finish(attempt, evidence, now); err != nil {
			_ = tx.Rollback()
			return err
		}
		if !stateIdentityMatches(state, identity) {
			nextSequence := state.NextSequence
			state, err = service.NewConfidenceFingerprintState(identity)
			if err != nil {
				_ = tx.Rollback()
				return err
			}
			state.NextSequence = nextSequence
			state.Samples = append(state.Samples, service.ConfidenceFingerprintSample{ObservedAt: attempt.ObservedAt.UTC(), Method: attempt.Method, Evidence: evidence})
		}
		changed, err := saveConfidenceFingerprintState(ctx, tx, keyID, revision, state, now)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if !changed {
			_ = tx.Rollback()
			continue
		}
		return tx.Commit()
	}
	return errors.New("confidence fingerprint completion contention")
}

func (r *upstreamConfigRepository) RecordConfidenceFingerprintSample(ctx context.Context, keyID int64, identity service.ConfidenceFingerprintIdentity, evidence service.ConfidenceFingerprintEvidence, now time.Time) error {
	if keyID <= 0 || identity.Version != service.ConfidenceFingerprintIdentityVersion || identity.Fingerprint == "" {
		return errors.New("invalid confidence fingerprint sample")
	}
	db, err := r.fingerprintDB()
	if err != nil {
		return err
	}
	for range confidenceDistributionCASAttempts {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		state, revision, err := readConfidenceFingerprintState(ctx, tx, keyID)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if state == nil || !stateIdentityMatches(state, identity) {
			state, err = service.NewConfidenceFingerprintState(identity)
			if err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if state.Pending != nil && state.LeaseExpires.After(now) {
			_ = tx.Rollback()
			return errors.New("confidence fingerprint sample conflicts with pending claim")
		}
		state.Samples = append(state.Samples, service.ConfidenceFingerprintSample{ObservedAt: now.UTC(), Method: "behavioral", Evidence: evidence})
		if len(state.Samples) > service.ConfidenceFingerprintSampleLimit {
			state.Samples = append([]service.ConfidenceFingerprintSample(nil), state.Samples[len(state.Samples)-service.ConfidenceFingerprintSampleLimit:]...)
		}
		changed, err := saveConfidenceFingerprintState(ctx, tx, keyID, revision, state, now)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if !changed {
			_ = tx.Rollback()
			continue
		}
		return tx.Commit()
	}
	return errors.New("confidence fingerprint sample contention")
}

func (r *upstreamConfigRepository) LoadConfidenceFingerprints(ctx context.Context, identities map[int64]service.ConfidenceFingerprintIdentity) (map[int64]service.ConfidenceFingerprintReadResult, error) {
	out := make(map[int64]service.ConfidenceFingerprintReadResult, len(identities))
	if len(identities) == 0 {
		return out, nil
	}
	db, err := r.fingerprintDB()
	if err != nil {
		return nil, err
	}
	keyIDs := make([]int64, 0, len(identities))
	for keyID := range identities {
		keyIDs = append(keyIDs, keyID)
	}
	query := `SELECT upstream_key_id,state_json FROM upstream_confidence_fingerprint_states WHERE upstream_key_id=ANY($1)`
	var rows *sql.Rows
	if driver, ok := r.client.Driver().(*entsql.Driver); ok && driver.Dialect() == dialect.SQLite {
		for _, keyID := range keyIDs {
			var raw []byte
			err := db.QueryRowContext(ctx, `SELECT state_json FROM upstream_confidence_fingerprint_states WHERE upstream_key_id=?`, keyID).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				out[keyID] = service.ConfidenceFingerprintReadResult{}
				continue
			}
			if err != nil {
				out[keyID] = service.ConfidenceFingerprintReadResult{Err: err}
				continue
			}
			state := new(service.ConfidenceFingerprintState)
			if err := json.Unmarshal(raw, state); err == nil {
				err = state.Validate()
			}
			if err != nil {
				out[keyID] = service.ConfidenceFingerprintReadResult{Err: err}
				continue
			}
			if !stateIdentityMatches(state, identities[keyID]) {
				out[keyID] = service.ConfidenceFingerprintReadResult{}
				continue
			}
			out[keyID] = service.ConfidenceFingerprintReadResult{State: state}
		}
		return out, nil
	}
	rows, err = db.QueryContext(ctx, query, pq.Array(keyIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var keyID int64
		var raw []byte
		if err := rows.Scan(&keyID, &raw); err != nil {
			return nil, err
		}
		state := new(service.ConfidenceFingerprintState)
		if err := json.Unmarshal(raw, state); err == nil {
			err = state.Validate()
		}
		if err != nil {
			out[keyID] = service.ConfidenceFingerprintReadResult{Err: err}
			continue
		}
		if !stateIdentityMatches(state, identities[keyID]) {
			out[keyID] = service.ConfidenceFingerprintReadResult{}
			continue
		}
		out[keyID] = service.ConfidenceFingerprintReadResult{State: state}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
