package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

func runnerAccountKey(id, keyID int64) string { return fmt.Sprintf("global-account:%d:%d", id, keyID) }
func (s *runnerTestStore) TransactAccount(ctx context.Context, group, id, keyID int64, fn func(upstreamcacheguard.Tx) error) error {
	return s.Transact(ctx, group, fn)
}
func (t *runnerTestTx) ReadAccount(id, keyID int64, out any) (int64, error) {
	raw := t.s.rows[runnerAccountKey(id, keyID)]
	if raw == nil {
		return 0, nil
	}
	return 1, json.Unmarshal(raw, out)
}
func (t *runnerTestTx) WriteAccount(id, keyID int64, value any) (int64, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	t.s.rows[runnerAccountKey(id, keyID)] = raw
	return 1, nil
}
func (t *runnerTestTx) AccountFacts(id, keyID int64, since time.Time) ([]upstreamcacheguard.Evidence, error) {
	facts, err := t.Facts(id, since)
	var out []upstreamcacheguard.Evidence
	for _, v := range facts {
		if v.Request.UpstreamKeyID == keyID {
			out = append(out, v)
		}
	}
	return out, err
}
func (t *runnerTestTx) LegacyAccounts(id, keyID int64) ([]upstreamcacheguard.LegacyAccountState, error) {
	return nil, nil
}
func (s *runnerTestStore) AccountRows(ctx context.Context) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []upstreamcacheguard.AccountRuntimeRow
	for key, raw := range s.rows {
		if !strings.HasPrefix(key, "global-account:") {
			continue
		}
		var a upstreamcacheguard.AccountState
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		out = append(out, upstreamcacheguard.AccountRuntimeRow{AccountID: a.AccountID, UpstreamKeyID: a.UpstreamKeyID, Version: 1, Payload: raw})
	}
	return out, ctx.Err()
}

func (s *runnerTestStore) AccountWindows(ctx context.Context, since, until time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	rows, err := s.AccountRows(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]upstreamcacheguard.AccountWindow{}
	for _, row := range rows {
		var a upstreamcacheguard.AccountState
		if err := json.Unmarshal(row.Payload, &a); err != nil {
			return nil, err
		}
		w := upstreamcacheguard.AccountWindow{AccountID: a.AccountID, UpstreamKeyID: a.UpstreamKeyID, Version: row.Version, State: a}
		for id, f := range s.events {
			if !s.processed[id] || f.Request.AccountID != a.AccountID || f.Request.UpstreamKeyID != a.UpstreamKeyID || !f.ValidSample() || f.CompletedAt.Before(since) || f.CompletedAt.After(until) {
				continue
			}
			if !a.QualitySince.IsZero() && !f.CompletedAt.After(a.QualitySince) {
				continue
			}
			w.ValidRequests++
			w.TotalInputTokens += f.RawUsage.TotalInputTokens
			w.CacheReadTokens += f.RawUsage.CacheReadTokens
			if f.CompletedAt.After(w.LastEvidenceAt) {
				w.LastEvidenceAt = f.CompletedAt
			}
		}
		out[fmt.Sprintf("%d:%d", a.AccountID, a.UpstreamKeyID)] = w
	}
	return out, ctx.Err()
}
