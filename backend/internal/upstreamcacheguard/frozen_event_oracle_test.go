package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

// The golden was produced by frozen pre-optimization source (see its adjacent
// provenance note). This test has no update mode: expectations remain external
// to the current engine implementation.
func TestFrozenCacheEventOracle(t *testing.T) {
	e, _ := testEngine(t)
	settings := e.Settings()
	settings.MinValidRequests, settings.CooldownMinutes, settings.TrialPercent = 2, 1, 100
	if err := e.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	at := testNow
	e.now = func() time.Time { return at }
	type result struct {
		Label     string       `json:"label"`
		Account   AccountState `json:"account"`
		Available bool         `json:"available"`
		Fresh     bool         `json:"fresh"`
		Session   SessionRead  `json:"session"`
		Group     GroupState   `json:"group"`
		Admission *Admission   `json:"admission,omitempty"`
	}
	var transcript []result
	var last Request
	capture := func(label string, admission *Admission, refresh bool) {
		if refresh {
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		a, _ := e.CachedAccount(2, 3)
		// UpdatedAt intentionally changes only when business data changes in the
		// optimized implementation; the judgement fields remain the contract.
		a.UpdatedAt = time.Time{}
		for key, p := range a.Policies {
			p.State.UpdatedAt = time.Time{}
			a.Policies[key] = p
		}
		var session SessionRead
		if last.ID != "" {
			readRequest := last
			readRequest.StartedAt = at
			session = e.CachedSessionRead(readRequest)
		}
		e.mu.RLock()
		group := e.groups[1]
		e.mu.RUnlock()
		transcript = append(transcript, result{label, a.AccountState, a.Available, a.Fresh, session, GroupState{NaturalNewCount: group.NaturalNewCount, TrialCount: group.TrialCount}, admission})
	}
	event := func(label, session string, offset time.Duration, read int64) {
		at = testNow.Add(offset)
		last = request(label, session, at)
		a := admit(t, e, last)
		at = at.Add(time.Second)
		complete(t, e, last, a, read, true, "known")
		capture(label, &a, true)
	}
	event("natural", "warm", 0, 90)
	event("warm-first", "warm", 20*time.Second, 90)
	event("warm-second", "warm", 40*time.Second, 90)
	event("low-first", "warm", 60*time.Second, 0)
	event("low-second", "warm", 80*time.Second, 0)
	event("low-third", "warm", 100*time.Second, 0)
	event("low-fourth", "warm", 120*time.Second, 0)
	cooldown := transcript[len(transcript)-1].Account.CooldownUntil
	if !cooldown.IsZero() {
		for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			at = cooldown.Add(delta)
			capture(fmt.Sprintf("cooldown-boundary-%d", delta), nil, true)
		}
		event("trial-natural", "recovery", cooldown.Sub(testNow)+time.Second, 90)
		event("trial-warm", "recovery", cooldown.Sub(testNow)+21*time.Second, 90)
		event("trial-second", "recovery", cooldown.Sub(testNow)+41*time.Second, 90)
		event("trial-session-b", "recovery-b", cooldown.Sub(testNow)+61*time.Second, 90)
		event("trial-warm-b", "recovery-b", cooldown.Sub(testNow)+81*time.Second, 90)
		event("trial-session-c", "recovery-c", cooldown.Sub(testNow)+101*time.Second, 90)
		event("trial-warm-c", "recovery-c", cooldown.Sub(testNow)+121*time.Second, 90)
		firstTrial := transcript[len(transcript)-1].Account.FirstTrialAt
		if !firstTrial.IsZero() {
			for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
				at = firstTrial.Add(RecoveryLifetime).Add(delta)
				capture(fmt.Sprintf("recovery-boundary-%d", delta), nil, true)
			}
			event("recovery-evidence", "recovery", firstTrial.Add(RecoveryLifetime).Sub(testNow)+time.Second, 90)
		}
	}
	lastCompleted := last.StartedAt.Add(time.Second)
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		at = lastCompleted.Add(WarmLifetime).Add(delta)
		capture(fmt.Sprintf("warm-boundary-%d", delta), nil, true)
	}
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		at = last.StartedAt.Add(ResidenceLifetime).Add(delta)
		capture(fmt.Sprintf("residence-boundary-%d", delta), nil, true)
	}
	// Window membership preserves the original inclusive cutoff comparison.
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		at = testNow.Add(21*time.Second + time.Duration(settings.WindowMinutes)*time.Minute).Add(delta)
		capture(fmt.Sprintf("window-boundary-%d", delta), nil, true)
	}
	loaded := at
	for _, age := range []time.Duration{5 * time.Second, 5*time.Second + time.Nanosecond, 30 * time.Second, 30*time.Second + time.Nanosecond} {
		at = loaded.Add(age)
		capture(fmt.Sprintf("snapshot-age-%d", age), nil, false)
	}
	actual, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/cache_events_preoptimization.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if json.Unmarshal(expected, &want) != nil || json.Unmarshal(actual, &got) != nil || !reflect.DeepEqual(want, got) {
		t.Fatalf("cache decisions differ from frozen pre-optimization transcript; actual=%s", actual)
	}
}
