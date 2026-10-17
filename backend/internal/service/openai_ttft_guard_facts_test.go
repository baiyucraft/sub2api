package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type ttftFactFixture struct {
	g        *openAITTFTGuard
	cfg      OpenAITTFTGuardConfigSnapshot
	now      time.Time
	sequence int
}

func newTTFTFactFixture() *ttftFactFixture {
	f := &ttftFactFixture{now: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), cfg: enabledOpenAITTFTGuardConfig(75*time.Second, 5)}
	f.g = newOpenAITTFTGuardWithOptions(64, 15*time.Minute, func() time.Time { return f.now })
	return f
}

func (f *ttftFactFixture) observation(ms int, session string) OpenAITTFTGuardObservation {
	f.sequence++
	sent := f.now.Add(time.Second)
	first := sent.Add(time.Duration(ms) * time.Millisecond)
	f.now = first.Add(time.Millisecond)
	return OpenAITTFTGuardObservation{GroupID: 100, AccountID: 7, UpstreamKeyID: 99, Model: "GPT-TEST", AttemptID: fmt.Sprintf("attempt-%d", f.sequence), LogicalRequestID: fmt.Sprintf("request-%d", f.sequence), SessionID: session, RuntimeID: f.g.runtimeID, AlgorithmVersion: openAITTFTGuardAlgorithmVersion, PolicyGeneration: f.g.policyGenerationLocked(), SentAt: sent, FirstTokenAt: first, CompletedAt: f.now, TTFTMs: ms, Success: true}
}

func (f *ttftFactFixture) report(t *testing.T, ms int, session string) OpenAITTFTGuardObservation {
	t.Helper()
	o := f.observation(ms, session)
	require.True(t, f.g.reportObservation(o, f.cfg, true))
	return o
}

func (f *ttftFactFixture) entry(t *testing.T) *openAITTFTGuardEntry {
	t.Helper()
	raw, _ := f.g.sampleKeyLocked(7, 99, "gpt-test")
	e := f.g.projectionAtLocked(raw, 100, f.cfg, f.now)
	require.NotNil(t, e)
	return e
}

func TestTTFTFactsOrdinaryRecoveryNeedsThreeRequestsAndTwoSessions(t *testing.T) {
	f := newTTFTFactFixture()
	f.cfg.MinSamples = 20
	f.report(t, 225000, "one")
	e := f.entry(t)
	for i := 0; i < 3; i++ {
		f.report(t, 1000, "one")
	}
	require.Equal(t, "protected", e.stateAt(f.now, f.g.ttl))
	require.Equal(t, "recovery_sessions_insufficient", e.recoveryReason)
	f.report(t, 1000, "two")
	require.Equal(t, "normal", e.stateAt(f.now, f.g.ttl))
	require.Empty(t, e.lastRecoveryBlockReason)
	require.True(t, e.lastRecoveryBlockAt.IsZero())
}

func TestTTFTFactsRecoveryBlockDiagnosticsUseTerminalEvidence(t *testing.T) {
	tests := []struct {
		reason string
		change func(*OpenAITTFTGuardObservation, *ttftFactFixture, *openAITTFTGuardEntry)
	}{
		{"failure", func(o *OpenAITTFTGuardObservation, _ *ttftFactFixture, _ *openAITTFTGuardEntry) { o.Success = false }},
		{"canceled", func(o *OpenAITTFTGuardObservation, _ *ttftFactFixture, _ *openAITTFTGuardEntry) { o.Canceled = true }},
		{"no_ttft", func(o *OpenAITTFTGuardObservation, _ *ttftFactFixture, _ *openAITTFTGuardEntry) {
			o.TTFTMs, o.FirstTokenAt = 0, time.Time{}
		}},
		{"slow_ttft", func(o *OpenAITTFTGuardObservation, _ *ttftFactFixture, _ *openAITTFTGuardEntry) { o.TTFTMs = 46000 }},
		{"old_send", func(o *OpenAITTFTGuardObservation, _ *ttftFactFixture, e *openAITTFTGuardEntry) {
			o.SentAt = e.latestBadBarrier
		}},
		{"missing_logical_request", func(o *OpenAITTFTGuardObservation, _ *ttftFactFixture, _ *openAITTFTGuardEntry) {
			o.LogicalRequestID = ""
		}},
		{"duplicate_logical_request", func(o *OpenAITTFTGuardObservation, f *ttftFactFixture, _ *openAITTFTGuardEntry) {
			o.LogicalRequestID = fmt.Sprintf("request-%d", f.sequence-1)
		}},
		{"expired_ttft", func(o *OpenAITTFTGuardObservation, f *ttftFactFixture, _ *openAITTFTGuardEntry) {
			o.CompletedAt = o.FirstTokenAt.Add(f.g.ttl)
			f.now = o.CompletedAt
		}},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			f := newTTFTFactFixture()
			f.report(t, 225000, "trigger")
			f.report(t, 1000, "one")
			e := f.entry(t)
			o := f.observation(1000, "two")
			tt.change(&o, f, e)
			// Exercise the projection's terminal evaluator directly so a long
			// stream can finish after its original latency window has expired.
			require.True(t, e.mergeFactAt(openAITTFTGuardFact{OpenAITTFTGuardObservation: o, identified: true}, f.now, f.g.ttl))
			e.evaluateFactsAt(f.now, f.g.ttl)
			require.Equal(t, tt.reason, e.lastRecoveryBlockReason)
			require.Equal(t, o.CompletedAt, e.lastRecoveryBlockAt)
			f.now = f.now.Add(time.Second)
			e.evaluateFactsAt(f.now, f.g.ttl)
			require.Equal(t, o.CompletedAt, e.lastRecoveryBlockAt, "reading later must not invent a terminal timestamp")
			e.setPolicyGeneration(e.policyGeneration + 1)
			require.Empty(t, e.lastRecoveryBlockReason)
			require.True(t, e.lastRecoveryBlockAt.IsZero())
		})
	}
}

func TestTTFTFactsFirstTokenDoesNotRecordRecoveryBlock(t *testing.T) {
	f := newTTFTFactFixture()
	f.report(t, 225000, "trigger")
	e := f.entry(t)
	reason, at := e.lastRecoveryBlockReason, e.lastRecoveryBlockAt
	o := f.observation(46000, "one")
	o.FirstTokenOnly, o.CompletedAt = true, time.Time{}
	require.True(t, f.g.reportObservation(o, f.cfg, true))
	require.Equal(t, reason, e.lastRecoveryBlockReason)
	require.Equal(t, at, e.lastRecoveryBlockAt)
}

func TestTTFTFactsSnapshotExpiryDoesNotInventRecoveryBlock(t *testing.T) {
	f := newTTFTFactFixture()
	f.report(t, 225000, "trigger")
	o := f.report(t, 1000, "one")
	e := f.entry(t)
	reason, at := e.lastRecoveryBlockReason, e.lastRecoveryBlockAt
	e.evaluateFactsAt(o.FirstTokenAt.Add(f.g.ttl), f.g.ttl)
	require.Equal(t, reason, e.lastRecoveryBlockReason)
	require.Equal(t, at, e.lastRecoveryBlockAt)
}

func TestTTFTFactsFastTrafficCannotRenewBadEvidenceOrBypassExpiredEpoch(t *testing.T) {
	f := newTTFTFactFixture()
	f.cfg.MinSamples = 20
	f.report(t, 225000, "one")
	e := f.entry(t)
	deadline, barrier := e.badExpiresAt, e.latestBadBarrier
	for i := 0; i < 24; i++ {
		f.now = f.now.Add(40 * time.Second)
		f.report(t, 1000, "one")
	}
	require.True(t, f.now.After(deadline))
	require.Equal(t, deadline, e.badExpiresAt)
	require.Equal(t, barrier, e.latestBadBarrier)
	require.Equal(t, "insufficient", e.stateAt(f.now, f.g.ttl))
	require.EqualValues(t, 20, e.sampleCount)
	require.True(t, e.needsRevalidation)
	f.report(t, 1000, "two")
	require.Equal(t, "normal", e.stateAt(f.now, f.g.ttl))
}

func TestTTFTFactsTwoHour400OrdinaryFastRequestsRequireTwoSessionsAndDoNotRenewBadEvidence(t *testing.T) {
	for _, test := range []struct {
		name        string
		twoSessions bool
	}{
		{name: "first_three_span_two_sessions", twoSessions: true},
		{name: "all_requests_share_one_session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newTTFTFactFixture()
			f.cfg.MinSamples = 20
			bad := f.report(t, 225000, "bad-session")
			e := f.entry(t)
			badBarrier, badDeadline, badEpoch := e.latestBadBarrier, e.badExpiresAt, e.recoveryEpoch
			require.True(t, e.needsRevalidation)
			require.Equal(t, "protected", e.stateAt(f.now, f.g.ttl))

			reportAt := func(completedAt time.Time, session string) {
				t.Helper()
				f.sequence++
				firstTokenAt := completedAt.Add(-time.Millisecond)
				o := OpenAITTFTGuardObservation{
					GroupID: 100, AccountID: 7, UpstreamKeyID: 99, Model: "GPT-TEST",
					AttemptID:        fmt.Sprintf("attempt-%d", f.sequence),
					LogicalRequestID: fmt.Sprintf("request-%d", f.sequence),
					SessionID:        session, RuntimeID: f.g.runtimeID,
					AlgorithmVersion: openAITTFTGuardAlgorithmVersion,
					PolicyGeneration: f.g.policyGenerationLocked(),
					SentAt:           firstTokenAt.Add(-time.Second), FirstTokenAt: firstTokenAt,
					CompletedAt: completedAt, TTFTMs: 1000, Success: true,
				}
				f.now = completedAt
				require.True(t, f.g.reportObservation(o, f.cfg, true), "ordinary physical attempt %d is accepted", f.sequence)
				require.False(t, o.Trial, "these are ordinary, not trial, requests")
				require.Equal(t, badBarrier, e.latestBadBarrier, "fast evidence cannot move the bad barrier")
				require.Equal(t, badDeadline, e.badExpiresAt, "fast evidence cannot renew bad evidence TTL")
				require.Equal(t, badEpoch, e.recoveryEpoch, "fast evidence cannot open a new bad epoch")
			}

			// Four hundred ordinary requests at 18-second intervals span exactly
			// two hours from the triggering first-token fact, without sleeping.
			for i := 0; i < 400; i++ {
				completedAt := bad.FirstTokenAt.Add(time.Duration(i+1) * 18 * time.Second)
				session := "session-one"
				if test.twoSessions && i == 2 {
					session = "session-two"
				}
				reportAt(completedAt, session)

				if test.twoSessions && i == 1 {
					require.Equal(t, "protected", e.stateAt(f.now, f.g.ttl))
					require.Equal(t, "recovery_samples_insufficient", e.recoveryReason)
				}
				if test.twoSessions && i == 2 {
					require.Equal(t, "normal", e.stateAt(f.now, f.g.ttl), "the first three qualifying requests span two sessions")
					require.False(t, e.needsRevalidation)
					require.True(t, e.recoveredThrough.After(bad.FirstTokenAt), "the third qualifying completion closes the recovery epoch")
				}
			}

			require.Equal(t, bad.FirstTokenAt.Add(2*time.Hour), f.now, "400 requests span exactly two hours")
			require.True(t, f.now.After(badDeadline), "the original bad-evidence TTL has elapsed")
			require.Equal(t, badDeadline, e.badExpiresAt, "the original expiry is never renewed")
			require.Equal(t, badBarrier, e.latestBadBarrier)
			require.Equal(t, badEpoch, e.recoveryEpoch)
			activeBadFact := false
			for _, fact := range e.facts {
				if fact.AttemptID == bad.AttemptID {
					activeBadFact = true
					break
				}
			}
			require.False(t, activeBadFact, "the expired slow fact is no longer active")

			if test.twoSessions {
				require.Equal(t, "normal", e.stateAt(f.now, f.g.ttl))
				require.False(t, e.needsRevalidation)
			} else {
				require.Equal(t, "insufficient", e.stateAt(f.now, f.g.ttl))
				require.True(t, e.needsRevalidation)
				require.Equal(t, "recovery_sessions_insufficient", e.recoveryReason)
				require.Equal(t, 1, e.recoverySessions)
				require.GreaterOrEqual(t, e.sampleCount, uint64(f.cfg.MinSamples), "the missing second session, not a lack of fast samples, blocks recovery")
				require.False(t, e.degraded, "the old evidence expired, but revalidation remains incomplete")
			}
		})
	}
}

func TestTTFTFactsLegacyCallbacksAndOldSendsCannotRecover(t *testing.T) {
	f := newTTFTFactFixture()
	oldSent := f.now
	f.report(t, 225000, "one")
	e := f.entry(t)
	for i := 0; i < 3; i++ {
		o := f.observation(1000, fmt.Sprint(i))
		o.SentAt = oldSent
		require.True(t, f.g.reportObservation(o, f.cfg, true))
	}
	fast := 1000
	for i := 0; i < 3; i++ {
		f.g.reportForKey(100, 7, 99, "gpt-test", true, &fast, f.cfg)
	}
	require.Equal(t, "protected", e.stateAt(f.now, f.g.ttl))
	require.Zero(t, e.consecutiveRecovered)
}

func TestTTFTFactsPhysicalAndLogicalDedupe(t *testing.T) {
	f := newTTFTFactFixture()
	f.report(t, 225000, "one")
	o := f.report(t, 1000, "one")
	require.False(t, f.g.reportObservation(o, f.cfg, true))
	for i := 0; i < 3; i++ {
		retry := f.observation(1000, "two")
		retry.LogicalRequestID = o.LogicalRequestID
		require.True(t, f.g.reportObservation(retry, f.cfg, true))
	}
	e := f.entry(t)
	require.Equal(t, "protected", e.stateAt(f.now, f.g.ttl))
	require.Equal(t, 1, e.consecutiveRecovered)
}

func TestTTFTFactsRejectMissingOrStaleAlgorithmVersion(t *testing.T) {
	f := newTTFTFactFixture()
	o := f.observation(225000, "one")
	o.AlgorithmVersion = ""
	require.False(t, f.g.reportObservation(o, f.cfg, true))
	o = f.observation(225000, "one")
	o.AlgorithmVersion = "ttft-window-v1"
	require.False(t, f.g.reportObservation(o, f.cfg, true))
	key, ok := f.g.sampleKeyLocked(7, 99, "gpt-test")
	raw := f.g.observations[key]
	require.True(t, ok)
	require.Nil(t, raw)
}

func TestTTFTFactsOldPolicyAndCanceledOrMissingTokenCannotRecover(t *testing.T) {
	f := newTTFTFactFixture()
	f.report(t, 225000, "one")
	f.g.policySnapshot = &GroupSchedulingProtectionPolicySnapshot{revision: 2}
	for i := 0; i < 3; i++ {
		o := f.observation(1000, fmt.Sprint(i))
		o.PolicyGeneration = 1
		require.False(t, f.g.reportObservation(o, f.cfg, true))
	}
	e := f.entry(t)
	require.Zero(t, e.consecutiveRecovered)
	f.report(t, 1000, "one")
	o := f.observation(1000, "two")
	o.Canceled = true
	require.True(t, f.g.reportObservation(o, f.cfg, true))
	require.Zero(t, e.consecutiveRecovered)
	f.report(t, 1000, "one")
	o = f.observation(1000, "two")
	o.TTFTMs = 0
	o.FirstTokenAt = time.Time{}
	o.Success = false
	require.True(t, f.g.reportObservation(o, f.cfg, true))
	require.Zero(t, e.consecutiveRecovered)
}

func TestTTFTFactsLateOldBadCannotReprotectButNewSlowFirstTokenCan(t *testing.T) {
	f := newTTFTFactFixture()
	old := f.report(t, 225000, "one")
	f.report(t, 1000, "one")
	f.report(t, 1000, "two")
	f.report(t, 1000, "one")
	e := f.entry(t)
	require.Equal(t, "normal", e.stateAt(f.now, f.g.ttl))
	late := old
	late.AttemptID = "late-old"
	late.LogicalRequestID = "late-old"
	late.CompletedAt = f.now
	require.True(t, f.g.reportObservation(late, f.cfg, true))
	require.Equal(t, "normal", e.stateAt(f.now, f.g.ttl))
	slow := f.observation(225000, "one")
	slow.SentAt = old.SentAt
	require.True(t, f.g.reportObservation(slow, f.cfg, true))
	require.Equal(t, "protected", e.stateAt(f.now, f.g.ttl))
}

func TestTTFTFactsWindowCountsTwentyButEWMAKeepsTen(t *testing.T) {
	f := newTTFTFactFixture()
	f.cfg.MinSamples = 20
	for i := 0; i < 19; i++ {
		f.report(t, 1000, "one")
	}
	e := f.entry(t)
	require.Equal(t, "insufficient", e.stateAt(f.now, f.g.ttl))
	f.report(t, 1000, "two")
	require.Equal(t, "normal", e.stateAt(f.now, f.g.ttl))
	require.Equal(t, 10, e.sampleLen)
	require.EqualValues(t, 20, e.sampleCount)
	f.now = f.now.Add(15 * time.Minute)
	require.Equal(t, "insufficient", e.stateAt(f.now, f.g.ttl))
	require.Zero(t, e.sampleCount)
	f.report(t, 1000, "one")
	e = f.entry(t)
	require.EqualValues(t, 1, e.sampleCount)
}

func TestTTFTFactsThresholdsIndependentAndSnapshotsReadOnly(t *testing.T) {
	f := newTTFTFactFixture()
	f.cfg.Threshold = 20 * time.Second
	f.report(t, 60000, "one")
	raw, _ := f.g.sampleKeyLocked(7, 99, "gpt-test")
	strict := f.entry(t)
	lenient := enabledOpenAITTFTGuardConfig(75*time.Second, 5)
	f.g.syncConfigLocked(200, lenient)
	e := f.g.projectionAtLocked(raw, 200, lenient, f.now)
	require.Equal(t, "protected", strict.stateAt(f.now, f.g.ttl))
	require.Equal(t, "insufficient", e.stateAt(f.now, f.g.ttl))
	svc := &OpenAIGatewayService{openaiTTFTGuard: f.g}
	epoch, deadline := strict.recoveryEpoch, strict.badExpiresAt
	view := svc.TTFTProtectionSnapshots(7, 99, 100, f.cfg)
	require.Equal(t, "protected", view[0].State)
	require.Equal(t, f.g.runtimeID, view[0].RuntimeID)
	f.now = deadline.Add(time.Second)
	view = svc.TTFTProtectionSnapshots(7, 99, 100, f.cfg)
	require.Equal(t, "insufficient", view[0].State)
	require.Equal(t, epoch, strict.recoveryEpoch)
	require.Equal(t, deadline, strict.badExpiresAt)
	require.True(t, strict.degraded)
}

func TestTTFTFactsLongStreamTerminalResetsWithoutRenewingLatency(t *testing.T) {
	f := newTTFTFactFixture()
	f.report(t, 225000, "one")
	long := f.observation(1000, "one")
	long.Success = false
	f.now = f.now.Add(16 * time.Minute)
	f.report(t, 1000, "one")
	f.report(t, 1000, "two")
	e := f.entry(t)
	require.Equal(t, 2, e.consecutiveRecovered)
	deadline := e.badExpiresAt
	long.CompletedAt = f.now
	require.True(t, f.g.reportObservation(long, f.cfg, true))
	require.Zero(t, e.consecutiveRecovered, "a late failed terminal breaks the live recovery streak")
	require.Equal(t, deadline, e.badExpiresAt)
	require.EqualValues(t, 2, e.sampleCount, "the old first token does not become recent latency")
}

func TestTTFTFactsRetryFailureResetsEvenAfterLogicalSuccess(t *testing.T) {
	f := newTTFTFactFixture()
	f.report(t, 225000, "one")
	success := f.report(t, 1000, "one")
	f.report(t, 1000, "two")
	failed := f.observation(1000, "two")
	failed.LogicalRequestID = success.LogicalRequestID
	failed.Success = false
	require.True(t, f.g.reportObservation(failed, f.cfg, true))
	require.Zero(t, f.entry(t).consecutiveRecovered)
}

func TestTTFTFactsOldKeyAndPolicyCompletionCannotRollCurrentIdentityBack(t *testing.T) {
	f := newTTFTFactFixture()
	old := f.observation(225000, "one")
	f.now = f.now.Add(time.Second)
	f.g.markCurrentKeyLocked(7, 100, f.now)
	require.True(t, f.g.reportObservation(old, f.cfg, true), "old key evidence remains identified separately")
	require.EqualValues(t, 100, f.g.accountKeys[7])
	require.False(t, f.g.isDegraded(100, 7, "gpt-test"), "the current key has no evidence")
	old.PolicyGeneration = 0
	old.AttemptID = "old-generation"
	f.g.policySnapshot = &GroupSchedulingProtectionPolicySnapshot{revision: 2}
	require.False(t, f.g.reportObservation(old, f.cfg, true))
	require.EqualValues(t, 100, f.g.accountKeys[7])
	raw, _ := f.g.sampleKeyLocked(7, 99, "gpt-test")
	require.Len(t, f.g.observations[raw].facts, 1, "old generation completion cannot initialize or protect a current projection")
}

func TestTTFTFactsProjectionCapacityAndNoTokenFactsAreBounded(t *testing.T) {
	f := newTTFTFactFixture()
	f.cfg.MinSamples = 20
	for i := 0; i < 20; i++ {
		f.report(t, 1000, "one")
	}
	require.EqualValues(t, 20, f.entry(t).sampleCount)
	smaller := enabledOpenAITTFTGuardConfig(75*time.Second, 2)
	f.g.syncConfigLocked(200, smaller)
	raw, _ := f.g.sampleKeyLocked(7, 99, "gpt-test")
	projected := f.g.projectionAtLocked(raw, 200, smaller, f.now)
	require.EqualValues(t, 10, projected.sampleCount)
	for i := 0; i < 24; i++ {
		o := f.observation(1000, "one")
		o.FirstTokenAt = time.Time{}
		o.TTFTMs = 0
		o.Success = false
		require.True(t, f.g.reportObservation(o, f.cfg, true))
	}
	require.Len(t, f.entry(t).facts, 20)
	require.Len(t, projected.facts, 10)
	require.Zero(t, f.entry(t).sampleCount)
	require.Equal(t, "insufficient", f.entry(t).stateAt(f.now, f.g.ttl))
}

func TestTTFTFactsUnmaterializedPolicyKeepsBadEpochAfterRawWindowEviction(t *testing.T) {
	f := newTTFTFactFixture()
	f.cfg.Threshold = 20 * time.Second
	// Register a second, more lenient policy before any observations. It has
	// never been read, so the first critical fact must materialize its
	// projection while that fact is still in the raw window.
	lenient := enabledOpenAITTFTGuardConfig(75*time.Second, 5)
	f.g.syncConfigLocked(200, lenient)
	f.report(t, 225000, "one")
	raw, ok := f.g.sampleKeyLocked(7, 99, "gpt-test")
	require.True(t, ok)
	scope, ok := openAITTFTGuardScopeFor(200, lenient)
	require.True(t, ok)
	projectionKey := raw
	projectionKey.scopeID = scope
	_, materialized := f.g.entries[projectionKey]
	require.True(t, materialized, "registered policy must retain the critical fact before raw eviction")

	for i := 0; i < 24; i++ {
		f.report(t, 1000, "one")
	}
	projection := f.g.entries[projectionKey]
	require.NotNil(t, projection)
	require.Equal(t, "protected", projection.stateAt(f.now, f.g.ttl), "raw-window eviction must not bypass an active bad epoch")
}

func TestTTFTFactsUnmanagedPolicyChangeRejectsOldInFlightAndRequiresFreshMinimum(t *testing.T) {
	f := newTTFTFactFixture()
	for i := 0; i < 10; i++ {
		f.report(t, 1000, "one")
	}
	require.Equal(t, "normal", f.entry(t).stateAt(f.now, f.g.ttl))
	oldFast := f.observation(1000, "two")
	oldSlow := f.observation(225000, "two")
	previous := f.g.policyGenerationLocked()
	f.cfg.MinSamples = 6
	f.g.clearForConfig(100, f.cfg)
	require.Greater(t, f.g.policyGenerationLocked(), previous)
	require.False(t, f.g.reportObservation(oldFast, f.cfg, true), "old success cannot initialize or recover a new epoch")
	require.False(t, f.g.reportObservation(oldSlow, f.cfg, true), "old slow callback cannot protect the new epoch")
	e := f.entry(t)
	require.Equal(t, "insufficient", e.stateAt(f.now, f.g.ttl))
	require.Zero(t, e.sampleCount)
	require.False(t, e.degraded)
	for i := 0; i < 5; i++ {
		f.report(t, 1000, "one")
	}
	require.Equal(t, "insufficient", f.entry(t).stateAt(f.now, f.g.ttl))
	f.report(t, 1000, "two")
	require.Equal(t, "normal", f.entry(t).stateAt(f.now, f.g.ttl))
}

func TestTTFTFactsPolicyChangeClearsExpiredRevalidationWithoutFacts(t *testing.T) {
	f := newTTFTFactFixture()
	f.cfg.MinSamples = 20
	f.report(t, 225000, "trigger")
	e := f.entry(t)
	f.now = f.now.Add(16 * time.Minute)
	require.Equal(t, "insufficient", e.stateAt(f.now, f.g.ttl))
	require.Empty(t, e.facts)
	require.True(t, e.needsRevalidation)
	previousEpoch := e.recoveryEpoch
	// An unrelated policy revision keeps this scope active but starts a new
	// generation for all physical attempts, including an empty tombstone.
	other := enabledOpenAITTFTGuardConfig(35*time.Second, 5)
	f.g.syncConfigLocked(200, other)
	other.MinSamples = 6
	f.g.syncConfigLocked(200, other)
	e = f.entry(t)
	require.Greater(t, e.recoveryEpoch, previousEpoch)
	require.False(t, e.needsRevalidation)
	require.True(t, e.latestBadBarrier.IsZero())
	require.True(t, e.badExpiresAt.IsZero())
	require.Zero(t, e.triggerTTFTMs)
	for _, session := range []string{"one", "two", "one"} {
		f.report(t, 1000, session)
	}
	e = f.entry(t)
	require.Equal(t, "insufficient", e.stateAt(f.now, f.g.ttl))
	require.EqualValues(t, 3, e.sampleCount)
	require.Zero(t, e.consecutiveRecovered)
	require.Equal(t, "fresh_samples_insufficient", e.recoveryReason)
	for i := 3; i < 20; i++ {
		f.report(t, 1000, "one")
	}
	require.Equal(t, "normal", f.entry(t).stateAt(f.now, f.g.ttl))
}

func TestTTFTObservationIdentitySyncsUnmanagedPolicyBeforeFreezing(t *testing.T) {
	cfg := enabledOpenAITTFTGuardConfig(75*time.Second, 5)
	svc := &OpenAIGatewayService{}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return cfg }))
	runtimeID, first := svc.OpenAITTFTGuardObservationIdentity(100)
	_, same := svc.OpenAITTFTGuardObservationIdentity(100)
	require.Equal(t, first, same, "unchanged first registration has a stable generation")
	cfg.GroupName = "renamed"
	_, renamed := svc.OpenAITTFTGuardObservationIdentity(100)
	require.Equal(t, first, renamed, "display content does not invalidate physical attempts")
	cfg.MinSamples = 6
	runtimeAgain, next := svc.OpenAITTFTGuardObservationIdentity(100)
	require.Equal(t, runtimeID, runtimeAgain)
	require.Greater(t, next, first)
	_, stable := svc.OpenAITTFTGuardObservationIdentity(100)
	require.Equal(t, next, stable)
}
