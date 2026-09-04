package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	ExtraCostCategoryAccount = "account"
	ExtraCostCategoryProxy   = "proxy"
	ExtraCostCategoryServer  = "server"
	ExtraCostCategoryOther   = "other"
	ExtraCostCategoryAdjust  = "adjustment"
	ExtraCostRuleVersion     = "extra-cost-v1"
	ExtraCostMaxAmount       = 1_000_000_000.0
	ExtraCostMaxNoteLength   = 500
	ExtraCostMaxPageSize     = 100
)

var (
	ErrExtraCostInvalidDate     = errors.New("额外成本日期无效")
	ErrExtraCostInvalidAmount   = errors.New("额外成本金额无效")
	ErrExtraCostInvalidCategory = errors.New("额外成本类型无效")
	ErrExtraCostInvalidNote     = errors.New("额外成本备注过长")
	ErrExtraCostNotFound        = errors.New("额外成本记录不存在")
	ErrExtraCostAlreadyReversed = errors.New("额外成本记录已冲正")
)

var ExtraCostCategories = []string{
	ExtraCostCategoryAccount,
	ExtraCostCategoryProxy,
	ExtraCostCategoryServer,
	ExtraCostCategoryOther,
	ExtraCostCategoryAdjust,
}

type ExtraCostEntry struct {
	ID             int64     `json:"id"`
	CostDate       string    `json:"cost_date"`
	Amount         float64   `json:"amount"`
	Category       string    `json:"category"`
	Notes          string    `json:"notes"`
	CreatedBy      *int64    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	ReversalOf     *int64    `json:"reversal_of,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	RuleVersion    string    `json:"rule_version"`
}

type ExtraCostFilter struct {
	StartDate *time.Time
	EndDate   *time.Time
	Category  string
	Page      int
	PageSize  int
}

type ExtraCostRepository interface {
	List(ctx context.Context, filter ExtraCostFilter) ([]ExtraCostEntry, int64, error)
	Create(ctx context.Context, entry ExtraCostEntry) (*ExtraCostEntry, error)
	GetByID(ctx context.Context, id int64) (*ExtraCostEntry, error)
	Reverse(ctx context.Context, id int64, createdBy *int64, reason, idempotencyKey string) (*ExtraCostEntry, error)
	Sum(ctx context.Context, start, end *time.Time) (float64, error)
}

type extraCostDailyRepository interface {
	DailySums(ctx context.Context, start, end *time.Time) (map[string]float64, error)
}

type ExtraCostService struct {
	repo           ExtraCostRepository
	invalidateDash []func()
}

func NewExtraCostService(repo ExtraCostRepository, invalidateDashboard ...func()) *ExtraCostService {
	svc := &ExtraCostService{repo: repo, invalidateDash: invalidateDashboard}
	return svc
}

func (s *ExtraCostService) invalidateDashboardCache() {
	if s == nil {
		return
	}
	for _, invalidate := range s.invalidateDash {
		if invalidate != nil {
			invalidate()
		}
	}
}

func (s *ExtraCostService) List(ctx context.Context, filter ExtraCostFilter) ([]ExtraCostEntry, int64, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > ExtraCostMaxPageSize {
		filter.PageSize = 20
	}
	if filter.Category != "" && !IsValidExtraCostCategory(filter.Category) {
		return nil, 0, ErrExtraCostInvalidCategory
	}
	return s.repo.List(ctx, filter)
}

func (s *ExtraCostService) Create(ctx context.Context, entry ExtraCostEntry) (*ExtraCostEntry, error) {
	if _, err := time.Parse("2006-01-02", entry.CostDate); err != nil {
		return nil, ErrExtraCostInvalidDate
	}
	if math.IsNaN(entry.Amount) || math.IsInf(entry.Amount, 0) || entry.Amount < 0 || entry.Amount > ExtraCostMaxAmount {
		return nil, ErrExtraCostInvalidAmount
	}
	entry.Category = strings.TrimSpace(entry.Category)
	if !IsValidExtraCostCategory(entry.Category) {
		return nil, ErrExtraCostInvalidCategory
	}
	entry.Notes = strings.TrimSpace(entry.Notes)
	if len([]rune(entry.Notes)) > ExtraCostMaxNoteLength {
		return nil, ErrExtraCostInvalidNote
	}
	if entry.RuleVersion == "" {
		entry.RuleVersion = ExtraCostRuleVersion
	}
	created, err := s.repo.Create(ctx, entry)
	if err == nil {
		s.invalidateDashboardCache()
	}
	return created, err
}

func (s *ExtraCostService) Reverse(ctx context.Context, id int64, createdBy *int64, reason, idempotencyKey string) (*ExtraCostEntry, error) {
	if id <= 0 {
		return nil, ErrExtraCostNotFound
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("冲正原因不能为空")
	}
	if len([]rune(reason)) > ExtraCostMaxNoteLength {
		return nil, ErrExtraCostInvalidNote
	}
	entry, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, ErrExtraCostNotFound
	}
	if entry.ReversalOf != nil {
		return nil, ErrExtraCostAlreadyReversed
	}
	created, err := s.repo.Reverse(ctx, id, createdBy, reason, idempotencyKey)
	if err == nil {
		s.invalidateDashboardCache()
	}
	return created, err
}

func (s *ExtraCostService) Sum(ctx context.Context, start, end *time.Time) (float64, error) {
	return s.repo.Sum(ctx, start, end)
}

func (s *ExtraCostService) DailySums(ctx context.Context, start, end *time.Time) (map[string]float64, error) {
	if repo, ok := s.repo.(extraCostDailyRepository); ok {
		return repo.DailySums(ctx, start, end)
	}
	return map[string]float64{}, nil
}

func IsValidExtraCostCategory(category string) bool {
	for _, allowed := range ExtraCostCategories {
		if category == allowed {
			return true
		}
	}
	return false
}

func (e *ExtraCostEntry) Validate() error {
	if e == nil {
		return fmt.Errorf("额外成本记录为空")
	}
	_, err := time.Parse("2006-01-02", e.CostDate)
	if err != nil {
		return ErrExtraCostInvalidDate
	}
	return nil
}
