package repository

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

var _ upstreamcacheguard.RecoveryFactsTx = (*upstreamCacheGuardTx)(nil)

// AccountRecoveryFacts uses one statement snapshot for the bounded processed
// history and this identity's delayed completion backlog. Active admissions and
// recently queued completions do not constitute a sampling gap. InsertFact has
// already acknowledged the completion being consumed in this transaction.
func (t *upstreamCacheGuardTx) AccountRecoveryFacts(accountID, keyID int64, since, at time.Time) (upstreamcacheguard.AccountRecoveryFacts, error) {
	var view upstreamcacheguard.AccountRecoveryFacts
	if accountID != t.accountID || keyID != t.keyID {
		return view, errors.New("cache guard account identity changed in transaction")
	}
	rows, err := t.tx.QueryContext(t.ctx, `WITH history AS (
		SELECT evidence_json,completed_at,group_id,evidence_id
		FROM upstream_cache_guard_facts
		WHERE account_id=$1 AND upstream_key_id=$2 AND processed=TRUE AND completed_at>=$3
		ORDER BY completed_at,group_id,evidence_id LIMIT $4
	), health AS (
		SELECT EXISTS(SELECT 1 FROM upstream_cache_guard_facts
			WHERE account_id=$1 AND upstream_key_id=$2 AND processed=FALSE AND completed_at<$5) AS pending_gap
	)
	SELECT history.evidence_json,health.pending_gap
	FROM health LEFT JOIN history ON TRUE
	ORDER BY history.completed_at,history.group_id,history.evidence_id`, accountID, keyID, since.UTC(), upstreamcacheguard.MaxFacts+1, at.UTC().Add(-upstreamcacheguard.RefreshLifetime))
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw, &view.HasPendingGap); err != nil {
			return upstreamcacheguard.AccountRecoveryFacts{}, err
		}
		if raw == nil {
			continue // The single health row still reports gaps for empty history.
		}
		var fact upstreamcacheguard.Evidence
		if err = json.Unmarshal(raw, &fact); err != nil {
			return upstreamcacheguard.AccountRecoveryFacts{}, fmt.Errorf("cache guard account fact decode: %w", err)
		}
		view.Facts = append(view.Facts, fact)
		if len(view.Facts) > upstreamcacheguard.MaxFacts {
			return upstreamcacheguard.AccountRecoveryFacts{}, errors.New("cache guard account fact bound exceeded")
		}
	}
	if err = rows.Err(); err != nil {
		return upstreamcacheguard.AccountRecoveryFacts{}, err
	}
	return view, nil
}
