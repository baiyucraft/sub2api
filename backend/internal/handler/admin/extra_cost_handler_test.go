package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type extraCostHandlerRepositoryStub struct {
	created  service.ExtraCostEntry
	original *service.ExtraCostEntry
}

func (r *extraCostHandlerRepositoryStub) List(context.Context, service.ExtraCostFilter) ([]service.ExtraCostEntry, int64, error) {
	return nil, 0, nil
}

func (r *extraCostHandlerRepositoryStub) Create(_ context.Context, entry service.ExtraCostEntry) (*service.ExtraCostEntry, error) {
	r.created = entry
	entry.ID = 1
	return &entry, nil
}

func (r *extraCostHandlerRepositoryStub) GetByID(context.Context, int64) (*service.ExtraCostEntry, error) {
	if r.original != nil {
		return r.original, nil
	}
	return nil, service.ErrExtraCostNotFound
}

func (r *extraCostHandlerRepositoryStub) Reverse(context.Context, int64, service.ExtraCostEntry) (*service.ExtraCostEntry, error) {
	return nil, nil
}

func (r *extraCostHandlerRepositoryStub) Sum(context.Context, *time.Time, *time.Time) (float64, error) {
	return 0, nil
}

func TestExtraCostHandlerCreateIgnoresLegacyCostDate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &extraCostHandlerRepositoryStub{}
	handler := NewExtraCostHandler(service.NewExtraCostService(repo))
	router := gin.New()
	router.POST("/extra-costs", handler.Create)

	recorder := httptest.NewRecorder()
	body := "{\"cost_date\":\"not-a-date\",\"amount\":12.5,\"category\":\"account\",\"notes\":\"legacy client\"}"
	request := httptest.NewRequest(http.MethodPost, "/extra-costs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if repo.created.CostDate == "" || repo.created.CostDate == "not-a-date" {
		t.Fatalf("CostDate = %q, want server-generated date", repo.created.CostDate)
	}
	if repo.created.CreatedAt.IsZero() {
		t.Fatal("CreatedAt is zero, want server occurrence time")
	}
}

func TestExtraCostHandlerRejectsForgedRewardAssociation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, fields := range []string{
		`"activity_reward_id":81`,
		`"related_user_id":7`,
		`"activity_type":"daily_gift"`,
	} {
		t.Run(fields, func(t *testing.T) {
			repo := &extraCostHandlerRepositoryStub{}
			handler := NewExtraCostHandler(service.NewExtraCostService(repo))
			router := gin.New()
			router.POST("/extra-costs", handler.Create)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/extra-costs", strings.NewReader(`{"amount":1,"category":"account",`+fields+`}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
			}
			if repo.created.Category != "" {
				t.Fatal("forged request reached repository")
			}
		})
	}
}

func TestExtraCostHandlerRejectsAutomaticRewardCostMutations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`{"amount":1,"category":"activity_reward"}`,
		`{"amount":1,"category":"account","idempotency_key":"activity-reward:81"}`,
	} {
		repo := &extraCostHandlerRepositoryStub{}
		router := gin.New()
		router.POST("/extra-costs", NewExtraCostHandler(service.NewExtraCostService(repo)).Create)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/extra-costs", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest || repo.created.Category != "" {
			t.Fatalf("automatic create status=%d created=%+v body=%s", recorder.Code, repo.created, recorder.Body.String())
		}
	}
	repo := &extraCostHandlerRepositoryStub{original: &service.ExtraCostEntry{ID: 900, Category: service.ExtraCostCategoryActivityReward}}
	router := gin.New()
	router.POST("/extra-costs/:id/reverse", NewExtraCostHandler(service.NewExtraCostService(repo)).Reverse)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/extra-costs/900/reverse", strings.NewReader(`{"reason":"manual reversal"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("automatic reversal status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
