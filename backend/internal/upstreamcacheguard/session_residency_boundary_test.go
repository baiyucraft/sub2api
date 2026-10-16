package upstreamcacheguard

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Unchanged active sessions need no JSON reread. Durable historical rows remain
// accessible through the real cold-loader after residency eviction.
type residencyBoundaryStore struct {
	*incrementalMemoryStore
	active []SessionIdentity
}

func (s *residencyBoundaryStore) ActiveRuntimeRows(_ context.Context, active []SessionIdentity, _ time.Time) ([]RuntimeRow, error) {
	s.active = append([]SessionIdentity(nil), active...)
	return nil, nil
}

func TestSessionResidencyThirtyMinuteStrictBoundaryAndAccessRenewal(t *testing.T) {
	for _, renew := range []bool{false, true} {
		t.Run(map[bool]string{false: "untouched", true: "access-renews"}[renew], func(t *testing.T) {
			store := &residencyBoundaryStore{incrementalMemoryStore: &incrementalMemoryStore{memoryStore: newMemoryStore()}}
			engine := NewEngine(store)
			settings := DefaultSettings()
			settings.Mode = Protect
			require.NoError(t, engine.ApplySettings(settings))
			at := testNow
			engine.now = func() time.Time { return at }
			r := request("first", "persistent", at.Add(-time.Hour))
			admit(t, engine, r)
			first := engine.sessions[sessionCacheKey(r.GroupID, SessionKey(r))].FirstSeenAt
			lastAccess := at
			if renew {
				at = at.Add(29 * time.Minute)
				require.NoError(t, engine.Refresh(context.Background()))
				read, err := engine.SessionRead(context.Background(), r)
				require.NoError(t, err)
				require.True(t, read.Known)
				lastAccess = at
			}
			for _, offset := range []time.Duration{30*time.Minute - time.Nanosecond, 30 * time.Minute, 30*time.Minute + time.Nanosecond} {
				at = lastAccess.Add(offset)
				require.NoError(t, engine.Refresh(context.Background()))
				cacheKey := sessionCacheKey(r.GroupID, SessionKey(r))
				_, resident := engine.sessions[cacheKey]
				require.Equal(t, offset <= 30*time.Minute, resident, "residency age %s", offset)
				require.Equal(t, offset <= 30*time.Minute, len(store.active) == 1)
			}
			read, err := engine.SessionRead(context.Background(), r)
			require.NoError(t, err)
			require.True(t, read.Known, "an evicted identity is cold-loaded, never a new natural session")
			require.Equal(t, 1, store.loads)
			require.Equal(t, first, engine.sessions[sessionCacheKey(r.GroupID, SessionKey(r))].FirstSeenAt)
			var group GroupState
			require.NoError(t, store.Transact(context.Background(), r.GroupID, func(tx Tx) error { _, err := tx.Read(GroupKind, "quota", &group); return err }))
			require.Equal(t, int64(1), group.NaturalNewCount)
		})
	}
}

func TestLowCacheResidenceStrictBoundary(t *testing.T) {
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			engine, _ := testEngine(t)
			r := request("resident", "low-cache", testNow)
			admit(t, engine, r)
			require.NoError(t, engine.Refresh(context.Background()))
			key := sessionCacheKey(r.GroupID, SessionKey(r))
			session := engine.sessions[key]
			session.LastAccountID, session.LastModel, session.LastProtocol = r.AccountID, r.Model, r.Protocol
			session.LastRouteKey = WarmRouteKey(r)
			session.ResidenceUntil = testNow.Add(ResidenceLifetime)
			session.Routes = map[string]SessionRoute{session.LastRouteKey: {AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, LastSuccessfulAt: testNow, LastCachePercent: 20, PendingLow: true}}
			engine.sessions[key] = session
			at := session.ResidenceUntil.Add(offset)
			// Refresh snapshot verification at the same fixed instant while keeping
			// the session's business route and migration residence unchanged.
			engine.loadedAt = at
			account := engine.accounts[accountIdentity(r.AccountID, r.UpstreamKeyID)]
			account.LoadedAt = at
			engine.accounts[accountIdentity(r.AccountID, r.UpstreamKeyID)] = account
			r.StartedAt = at
			read := engine.CachedSessionReadAt(r, at)
			require.True(t, read.Known)
			require.True(t, read.WarmAtStart)
			require.True(t, read.PendingLow)
			require.Equal(t, offset < 0, read.Protected, "residence uses strict Before, independently of high-cache affinity")
		})
	}
}
