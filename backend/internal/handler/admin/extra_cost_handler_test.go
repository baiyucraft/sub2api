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
	created service.ExtraCostEntry
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
