package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

func TestLegacyTTFTQualityProbeDoesNotBecomeHealthyOrAdvanceCadence(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	g := newOpenAITTFTGuardWithOptions(16, time.Hour, func() time.Time { return now })
	runtime := legacyTTFTRuntime{guard: g}
	cfg := forkscheduling.TTFTConfig{Enabled: true, Threshold: 20 * time.Second, MinSamples: 5, Source: GroupTTFTGuardSourceGlobal}
	slow := 60000
	views := []forkscheduling.CandidateView{{ID: 1, GroupID: 100, Model: "gpt-a"}, {ID: 2, GroupID: 100, Model: "gpt-a"}}
	for _, view := range views {
		runtime.Report(forkscheduling.TTFTSample{GroupID: view.GroupID, AccountID: view.ID, Model: view.Model, Success: true, FirstTokenMs: &slow}, cfg)
	}
	for i := 0; i < openAITTFTGuardProbeEvery; i++ {
		runtime.Exclusions(views, nil, cfg)
	}
	scope, _ := openAITTFTGuardScopeFor(100, openAITTFTGuardConfigFromContract(cfg))
	if len(g.groupProbes) != 0 {
		t.Fatal("exclusions must not mint trial opportunities")
	}
	touch := g.entries[openAITTFTGuardKey{scopeID: scope, accountID: 1, model: "gpt-a"}].lastTouchedAt
	now = now.Add(time.Minute)
	for i := 0; i < 40; i++ {
		q := runtime.Quality(views, cfg)
		for _, view := range views {
			if !q[view.ID].Degraded || q[view.ID].EWMAms != 60000 || q[view.ID].SampleCount != 1 {
				t.Fatalf("probe became healthy: %+v", q)
			}
		}
	}
	if len(g.groupProbes) != 0 {
		t.Fatal("quality advanced recovery cadence")
	}
	if !g.entries[openAITTFTGuardKey{scopeID: scope, accountID: 1, model: "gpt-a"}].lastTouchedAt.Equal(touch) {
		t.Fatal("quality changed LRU touch")
	}
	q := runtime.Quality(views, cfg)
	q[1] = forkscheduling.TTFTQuality{}
	if !runtime.Quality(views, cfg)[1].Degraded {
		t.Fatal("returned map changed runtime")
	}
}

func TestLegacyTTFTQualityProjectsSharedLatencyAtIndependentThresholds(t *testing.T) {
	g := newOpenAITTFTGuard()
	runtime := legacyTTFTRuntime{guard: g}
	source := forkscheduling.TTFTConfig{Enabled: true, Threshold: 30 * time.Second, MinSamples: 2, Source: GroupTTFTGuardSourceGroup}
	slow := 90000
	runtime.Report(forkscheduling.TTFTSample{GroupID: 100, AccountID: 1, Model: "gpt-a", Success: true, FirstTokenMs: &slow}, source)
	target := source
	target.Threshold = 10 * time.Second
	fast := 1000
	runtime.Report(forkscheduling.TTFTSample{GroupID: 200, AccountID: 1, Model: "gpt-a", Success: true, FirstTokenMs: &fast}, target)
	q := runtime.Quality([]forkscheduling.CandidateView{{ID: 1, GroupID: 200, Model: "gpt-a"}}, target)[1]
	if !q.Degraded || q.EWMAms != 72200 || q.SampleCount != 2 {
		t.Fatalf("shared observation projection was lost: %+v", q)
	}
	weaker := source
	weaker.Threshold = 100 * time.Second
	if q := runtime.Quality([]forkscheduling.CandidateView{{ID: 1, GroupID: 300, Model: "gpt-a"}}, weaker)[1]; q.Degraded || q.SampleCount != 2 || q.EWMAms != 72200 {
		t.Fatalf("loose policy did not independently evaluate shared evidence: %+v", q)
	}
}

func TestLegacyTTFTQualityUnknownHealthyTTLAndConfigIsolation(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	g := newOpenAITTFTGuardWithOptions(16, time.Minute, func() time.Time { return now })
	runtime := legacyTTFTRuntime{guard: g}
	cfg := forkscheduling.TTFTConfig{Enabled: true, Threshold: 20 * time.Second, MinSamples: 5, Source: GroupTTFTGuardSourceGlobal}
	fast := 1234
	runtime.Report(forkscheduling.TTFTSample{GroupID: 100, AccountID: 1, Model: "gpt-a", Success: true, FirstTokenMs: &fast}, cfg)
	views := []forkscheduling.CandidateView{{ID: 1, GroupID: 100, Model: "gpt-a"}, {ID: 2, GroupID: 100, Model: "gpt-a"}, {ID: 1, GroupID: 100, Model: "other"}}
	q := runtime.Quality(views, cfg)
	if len(q) != 1 || q[1].Degraded || q[1].EWMAms != 1234 || q[1].SampleCount != 1 {
		t.Fatalf("quality=%+v", q)
	}
	changed := cfg
	changed.Threshold = 30 * time.Second
	if q := runtime.Quality(views, changed); len(q) != 1 || q[1].SampleCount != 0 || q[1].State != "insufficient" || q[1].Degraded {
		t.Fatalf("changed policy reused prior generation evidence: %+v", q)
	}
	disabled := cfg
	disabled.Enabled = false
	if q := runtime.Quality(views, disabled); len(q) != 0 {
		t.Fatal("disabled quality still active")
	}
	now = now.Add(time.Minute)
	if q := runtime.Quality(views, cfg); len(q) != 0 {
		t.Fatalf("expired quality=%+v", q)
	}
	if len(g.entries) != 0 || len(g.observations) != 1 {
		t.Fatal("inactive projections should retire without clearing shared observations")
	}
	if q := (legacyTTFTRuntime{}).Quality(views, cfg); q != nil {
		t.Fatal("nil runtime was known")
	}
}

func TestLegacyTTFTQualityEqualScopeAndModelIsolation(t *testing.T) {
	g := newOpenAITTFTGuard()
	runtime := legacyTTFTRuntime{guard: g}
	cfg := forkscheduling.TTFTConfig{Enabled: true, Threshold: 20 * time.Second, MinSamples: 2, Source: GroupTTFTGuardSourceGroup}
	slowA, slowB := 60000, 80000
	runtime.Report(forkscheduling.TTFTSample{GroupID: 100, AccountID: 1, Model: "gpt-a", Success: true, FirstTokenMs: &slowA}, cfg)
	runtime.Report(forkscheduling.TTFTSample{GroupID: 200, AccountID: 1, Model: "gpt-a", Success: true, FirstTokenMs: &slowB}, cfg)
	for _, group := range []int64{100, 200, 300} {
		q := runtime.Quality([]forkscheduling.CandidateView{{ID: 1, GroupID: group, Model: "gpt-a"}}, cfg)[1]
		if !q.Degraded || q.EWMAms != 64000 || q.SampleCount != 2 {
			t.Fatalf("equal owner group=%d quality=%+v", group, q)
		}
	}
	if q := runtime.Quality([]forkscheduling.CandidateView{{ID: 1, GroupID: 100, Model: "other"}}, cfg); len(q) != 0 {
		t.Fatalf("model leaked quality: %+v", q)
	}
}
