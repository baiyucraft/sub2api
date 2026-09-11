package service

import "testing"

func TestPartitionPreferredAccountsForGroup(t *testing.T) {
	groupID := int64(7)
	accounts := []Account{
		{ID: 1, RateMultiplier: testFloat64Ptr(0.01), AccountGroups: []AccountGroup{{GroupID: groupID}}},
		{ID: 2, RateMultiplier: testFloat64Ptr(0.20), AccountGroups: []AccountGroup{{GroupID: groupID, SchedulerPreferred: true}}},
		{ID: 3, RateMultiplier: testFloat64Ptr(0.02), AccountGroups: []AccountGroup{{GroupID: 8, SchedulerPreferred: true}}},
		{ID: 4, RateMultiplier: testFloat64Ptr(0.30), AccountGroups: []AccountGroup{{GroupID: groupID, SchedulerPreferred: true}}},
	}

	preferred, ordinary := partitionPreferredAccountsForGroup(accounts, &groupID)
	if got := []int64{preferred[0].ID, preferred[1].ID}; len(preferred) != 2 || got[0] != 2 || got[1] != 4 {
		t.Fatalf("preferred pool = %v, want account order [2 4]", preferredAccountPoolTestIDs(preferred))
	}
	if got := preferredAccountPoolTestIDs(ordinary); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("ordinary pool = %v, want account order [1 3]", got)
	}
}

func TestPartitionPreferredAccountsWithoutGroupKeepsBaseline(t *testing.T) {
	accounts := []Account{{ID: 1}, {ID: 2}}
	preferred, ordinary := partitionPreferredAccountsForGroup(accounts, nil)
	if len(preferred) != 0 || len(ordinary) != len(accounts) || ordinary[0].ID != 1 || ordinary[1].ID != 2 {
		t.Fatalf("partition without group = preferred %v ordinary %v", preferredAccountPoolTestIDs(preferred), preferredAccountPoolTestIDs(ordinary))
	}
}

func preferredAccountPoolTestIDs(accounts []Account) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}

func testFloat64Ptr(value float64) *float64 { return &value }

func TestOpenAISelectionOrderPrefersGroupPreferredBeforeLowerRateOrdinary(t *testing.T) {
	groupID := int64(7)
	configID, keyID := int64(70), int64(700)
	ordinary := &Account{
		ID:               1,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeAPIKey,
		Priority:         1,
		RateMultiplier:   testFloat64Ptr(0.01),
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
		AccountGroups:    []AccountGroup{{GroupID: groupID}},
	}
	preferred := &Account{
		ID:               2,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeAPIKey,
		Priority:         1,
		RateMultiplier:   testFloat64Ptr(0.20),
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
		AccountGroups:    []AccountGroup{{GroupID: groupID, SchedulerPreferred: true}},
	}
	scheduler := &defaultOpenAIAccountScheduler{}
	plan := openAIAccountLoadPlan{
		candidates: []openAIAccountCandidateScore{
			{account: ordinary, loadInfo: &AccountLoadInfo{AccountID: ordinary.ID}},
			{account: preferred, loadInfo: &AccountLoadInfo{AccountID: preferred.ID}},
		},
		topK: 1,
	}

	order := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{GroupID: &groupID}, plan)

	if len(order) == 0 || order[0].account.ID != preferred.ID {
		t.Fatalf("selection order first account = %v, want preferred account %d", openAICandidateAccountIDs(order), preferred.ID)
	}
}

func TestOpenAISelectionOrderIgnoresRateInsidePreferredPool(t *testing.T) {
	groupID := int64(7)
	configID, keyID := int64(70), int64(700)
	expensive := &Account{
		ID:               1,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeAPIKey,
		Priority:         1,
		RateMultiplier:   testFloat64Ptr(0.20),
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
		AccountGroups:    []AccountGroup{{GroupID: groupID, SchedulerPreferred: true}},
	}
	cheap := &Account{
		ID:               2,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeAPIKey,
		Priority:         1,
		RateMultiplier:   testFloat64Ptr(0.01),
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
		AccountGroups:    []AccountGroup{{GroupID: groupID, SchedulerPreferred: true}},
	}
	scheduler := &defaultOpenAIAccountScheduler{}
	plan := openAIAccountLoadPlan{
		candidates: []openAIAccountCandidateScore{
			{account: cheap, score: 0.1, loadInfo: &AccountLoadInfo{AccountID: cheap.ID}},
			{account: expensive, score: 0.9, loadInfo: &AccountLoadInfo{AccountID: expensive.ID}},
		},
		topK: 1,
	}

	order := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{GroupID: &groupID}, plan)

	if len(order) == 0 || order[0].account.ID != expensive.ID {
		t.Fatalf("selection order first account = %v, want rate-neutral preferred account %d", openAICandidateAccountIDs(order), expensive.ID)
	}
}

func TestOpenAICompactSelectionKeepsPreferredAheadWithinCapabilityTier(t *testing.T) {
	groupID := int64(7)
	configID, keyID := int64(70), int64(700)
	ordinary := &Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 1,
		RateMultiplier: testFloat64Ptr(0.01), UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
		Extra:         map[string]any{"openai_compact_supported": true},
		AccountGroups: []AccountGroup{{GroupID: groupID}},
	}
	preferred := &Account{
		ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 1,
		RateMultiplier: testFloat64Ptr(0.20), UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
		Extra:         map[string]any{"openai_compact_supported": true},
		AccountGroups: []AccountGroup{{GroupID: groupID, SchedulerPreferred: true}},
	}
	unknownPreferred := &Account{
		ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 1,
		RateMultiplier: testFloat64Ptr(0.30), UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
		AccountGroups: []AccountGroup{{GroupID: groupID, SchedulerPreferred: true}},
	}

	scheduler := &defaultOpenAIAccountScheduler{}
	plan := openAIAccountLoadPlan{
		candidates: []openAIAccountCandidateScore{
			{account: ordinary, priority: 1, loadInfo: &AccountLoadInfo{AccountID: ordinary.ID}},
			{account: preferred, priority: 1, loadInfo: &AccountLoadInfo{AccountID: preferred.ID}},
			{account: unknownPreferred, priority: 1, loadInfo: &AccountLoadInfo{AccountID: unknownPreferred.ID}},
		},
		topK: 1,
	}
	order := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{GroupID: &groupID, RequireCompact: true}, plan)
	if len(order) < 3 {
		t.Fatalf("compact selection order = %v, want all compact-compatible candidates", openAICandidateAccountIDs(order))
	}
	if order[0].account.ID != preferred.ID || order[1].account.ID != ordinary.ID || order[2].account.ID != unknownPreferred.ID {
		t.Fatalf("compact selection order = %v, want [preferred supported, ordinary supported, preferred unknown]", openAICandidateAccountIDs(order))
	}
}

func openAICandidateAccountIDs(candidates []openAIAccountCandidateScore) []int64 {
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.account != nil {
			ids = append(ids, candidate.account.ID)
		}
	}
	return ids
}
