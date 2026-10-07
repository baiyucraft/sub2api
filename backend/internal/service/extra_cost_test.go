package service

import (
	"context"
	"testing"
	"time"
)

type extraCostRepositoryStub struct {
	created  ExtraCostEntry
	original *ExtraCostEntry
	reversed ExtraCostEntry
}

func (r *extraCostRepositoryStub) List(context.Context, ExtraCostFilter) ([]ExtraCostEntry, int64, error) {
	return nil, 0, nil
}

func (r *extraCostRepositoryStub) Create(_ context.Context, entry ExtraCostEntry) (*ExtraCostEntry, error) {
	r.created = entry
	return &entry, nil
}

func (r *extraCostRepositoryStub) GetByID(context.Context, int64) (*ExtraCostEntry, error) {
	return r.original, nil
}

func (r *extraCostRepositoryStub) Reverse(_ context.Context, _ int64, adjustment ExtraCostEntry) (*ExtraCostEntry, error) {
	r.reversed = adjustment
	return &adjustment, nil
}

func (r *extraCostRepositoryStub) Sum(context.Context, *time.Time, *time.Time) (float64, error) {
	return 0, nil
}

func TestExtraCostServiceCreateUsesSingleServerOccurrenceTime(t *testing.T) {
	repo := &extraCostRepositoryStub{}
	svc := NewExtraCostService(repo)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	fixedUTC := time.Date(2026, 9, 17, 16, 0, 11, 0, time.UTC)
	nowCalls := 0
	svc.now = func() time.Time {
		nowCalls++
		return fixedUTC
	}
	svc.location = func() *time.Location { return shanghai }

	_, err = svc.Create(context.Background(), ExtraCostEntry{
		CostDate: "2000-01-01",
		Amount:   12.5,
		Category: ExtraCostCategoryAccount,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	wantTime := fixedUTC.In(shanghai)
	if nowCalls != 1 {
		t.Fatalf("now calls = %d, want 1", nowCalls)
	}
	if repo.created.CostDate != "2026-09-18" {
		t.Fatalf("CostDate = %q, want 2026-09-18", repo.created.CostDate)
	}
	if !repo.created.CreatedAt.Equal(wantTime) {
		t.Fatalf("CreatedAt = %v, want %v", repo.created.CreatedAt, wantTime)
	}
}

func TestExtraCostServiceCreateQuantizesAmountToDatabaseScale(t *testing.T) {
	repo := &extraCostRepositoryStub{}
	svc := NewExtraCostService(repo)

	_, err := svc.Create(context.Background(), ExtraCostEntry{
		Amount:   1.000000009,
		Category: ExtraCostCategoryAccount,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if repo.created.Amount != 1.00000001 {
		t.Fatalf("Amount = %.12f, want database-scale value %.8f", repo.created.Amount, 1.00000001)
	}
}

func TestExtraCostServiceReverseUsesReversalOccurrenceTime(t *testing.T) {
	original := &ExtraCostEntry{ID: 42, CostDate: "2026-08-01", Amount: 8, Category: ExtraCostCategoryAccount}
	repo := &extraCostRepositoryStub{original: original}
	svc := NewExtraCostService(repo)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 9, 18, 9, 10, 11, 0, shanghai)
	svc.now = func() time.Time { return fixed }
	svc.location = func() *time.Location { return shanghai }
	createdBy := int64(7)

	_, err = svc.Reverse(context.Background(), original.ID, &createdBy, " correct entry ", "reverse-key")
	if err != nil {
		t.Fatalf("Reverse() error = %v", err)
	}

	if repo.reversed.CostDate != "2026-09-18" {
		t.Fatalf("CostDate = %q, want reversal day", repo.reversed.CostDate)
	}
	if !repo.reversed.CreatedAt.Equal(fixed) {
		t.Fatalf("CreatedAt = %v, want %v", repo.reversed.CreatedAt, fixed)
	}
	if repo.reversed.Notes != "correct entry" || repo.reversed.IdempotencyKey != "reverse-key" {
		t.Fatalf("unexpected adjustment metadata: %+v", repo.reversed)
	}
	if repo.reversed.ReversalOf == nil || *repo.reversed.ReversalOf != original.ID {
		t.Fatalf("ReversalOf = %v, want %d", repo.reversed.ReversalOf, original.ID)
	}
}

func TestExtraCostServiceRejectsAutomaticRewardReversal(t *testing.T) {
	repo := &extraCostRepositoryStub{original: &ExtraCostEntry{ID: 42, Category: "activity_reward", Amount: .25}}
	svc := NewExtraCostService(repo)
	_, err := svc.Reverse(context.Background(), 42, nil, "manual reversal", "")
	if err == nil {
		t.Fatal("automatic reward cost accepted a manual reversal")
	}
	if repo.reversed.Category != "" {
		t.Fatal("manual reversal reached repository")
	}
}

func TestExtraCostServiceRejectsForgedActivityCostsAndReservedKeys(t *testing.T) {
	sourceID := int64(81)
	userID := int64(7)
	for _, entry := range []ExtraCostEntry{
		{Category: ExtraCostCategoryActivityReward},
		{Category: ExtraCostCategoryAccount, ActivityRewardID: &sourceID},
		{Category: ExtraCostCategoryAccount, RelatedUserID: &userID},
		{Category: ExtraCostCategoryAccount, ActivityType: "daily_gift"},
		{Category: ExtraCostCategoryAccount, IdempotencyKey: " activity-reward:81 "},
		{Category: ExtraCostCategoryAccount, RuleVersion: ActivityRewardCostRuleVersion},
	} {
		repo := &extraCostRepositoryStub{}
		svc := NewExtraCostService(repo)
		if _, err := svc.Create(context.Background(), entry); err != ErrExtraCostAutomaticEntry {
			t.Fatalf("entry=%+v error=%v, want automatic entry protection", entry, err)
		}
		if repo.created.Category != "" {
			t.Fatal("forged create reached repository")
		}
	}
	svc := NewExtraCostService(&extraCostRepositoryStub{})
	if _, err := svc.Reverse(context.Background(), 7, nil, "reserved", "activity-reward:81"); err != ErrExtraCostAutomaticEntry {
		t.Fatalf("reserved reversal key error=%v", err)
	}
}

func TestExtraCostServiceAllowsActivityRewardListFilter(t *testing.T) {
	svc := NewExtraCostService(&extraCostRepositoryStub{})
	if _, _, err := svc.List(context.Background(), ExtraCostFilter{Category: ExtraCostCategoryActivityReward}); err != nil {
		t.Fatalf("activity reward list filter rejected: %v", err)
	}
}
