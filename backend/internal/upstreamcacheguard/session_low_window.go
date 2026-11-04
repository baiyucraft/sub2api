package upstreamcacheguard

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

func sessionLowWindowFingerprint(a AccountState, s Settings, generation string) string {
	return digest(fmt.Sprintf("session-low-window-v1:%d:%d:%s:%d:%g:%s:%s:%s", a.AccountID, a.UpstreamKeyID,
		PolicySignature(s), s.WindowMinutes, EffectiveSessionWindowMigrationThreshold(s),
		a.QualitySince.UTC().Format(time.RFC3339Nano), a.DegradedAt.UTC().Format(time.RFC3339Nano), generation))
}

func sessionLowWindowValid(proof *SessionLowWindow, a AccountState, s Settings, generation string, at time.Time) bool {
	return proof != nil && generation != "" && s.Mode == Protect && protectedPhase(a.Phase) &&
		!proof.EvaluatedAt.IsZero() && !proof.ValidUntil.IsZero() && !at.Before(proof.EvaluatedAt) && !at.After(proof.ValidUntil) &&
		proof.RuleFingerprint == sessionLowWindowFingerprint(a, s, generation)
}

func sessionLowWindowFactEligible(f Evidence, a AccountState, s Settings, sessionKey, routeKey string, at time.Time) bool {
	return f.ValidSample() && !f.Admission.CaptureOnly && !f.ProtectionPaused && f.Admission.ProtectedAtStart &&
		f.Request.AccountID == a.AccountID && f.Request.UpstreamKeyID == a.UpstreamKeyID &&
		f.Admission.SessionKey == sessionKey && WarmRouteKey(f.Request) == routeKey &&
		(f.Admission.PolicySignature == "" || f.Admission.PolicySignature == PolicySignature(s)) &&
		!f.CompletedAt.Before(at.Add(-time.Duration(s.WindowMinutes)*time.Minute)) && !f.CompletedAt.After(at) &&
		(a.QualitySince.IsZero() || (f.CompletedAt.After(a.QualitySince) && !f.Request.StartedAt.Before(a.QualitySince))) &&
		(a.DegradedAt.IsZero() || (!f.CompletedAt.Before(a.DegradedAt) && !f.Request.StartedAt.Before(a.DegradedAt)))
}

// sessionLowWindow reuses the account completion's bounded facts. Only this
// route's completed, protected warm samples can establish a new low-rate proof.
func sessionLowWindow(a AccountState, s Settings, generation, sessionKey, routeKey string, facts []Evidence, at time.Time) (*SessionLowWindow, error) {
	if generation == "" || s.Mode != Protect || !protectedPhase(a.Phase) {
		return nil, nil
	}
	var samples []Evidence
	var input, read int64
	for _, f := range facts {
		if !sessionLowWindowFactEligible(f, a, s, sessionKey, routeKey, at) {
			continue
		}
		if math.MaxInt64-input < f.RawUsage.TotalInputTokens || math.MaxInt64-read < f.RawUsage.CacheReadTokens {
			return nil, errors.New("cache guard session token sum overflow")
		}
		input += f.RawUsage.TotalInputTokens
		read += f.RawUsage.CacheReadTokens
		samples = append(samples, f)
	}
	threshold := EffectiveSessionWindowMigrationThreshold(s)
	low := func(count int, input, read int64) bool {
		return count > s.MinValidRequests && input > 0 && float64(read)/float64(input)*100 < threshold
	}
	if !low(len(samples), input, read) {
		return nil, nil
	}
	// Pure passage of time may revoke a proof, never create one. Find the first
	// loss of the currently proven condition, grouping equal-time expirations.
	sort.Slice(samples, func(i, j int) bool { return samples[i].CompletedAt.Before(samples[j].CompletedAt) })
	count := len(samples)
	for index := 0; index < len(samples); {
		expiresAt := samples[index].CompletedAt.Add(time.Duration(s.WindowMinutes) * time.Minute)
		end := index
		for end < len(samples) && samples[end].CompletedAt.Equal(samples[index].CompletedAt) {
			input -= samples[end].RawUsage.TotalInputTokens
			read -= samples[end].RawUsage.CacheReadTokens
			count--
			end++
		}
		if !low(count, input, read) {
			return &SessionLowWindow{RuleFingerprint: sessionLowWindowFingerprint(a, s, generation), EvaluatedAt: at, ValidUntil: expiresAt}, nil
		}
		index = end
	}
	return nil, nil
}
