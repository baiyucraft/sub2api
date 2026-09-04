package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ExtraCostHandler exposes the administrator's append-only cost ledger.
type ExtraCostHandler struct {
	service *service.ExtraCostService
}

func NewExtraCostHandler(svc *service.ExtraCostService) *ExtraCostHandler {
	return &ExtraCostHandler{service: svc}
}

// List returns extra cost entries, newest first.
// GET /api/v1/admin/extra-costs
func (h *ExtraCostHandler) List(c *gin.Context) {
	filter := service.ExtraCostFilter{Page: 1, PageSize: 20, Category: strings.TrimSpace(c.Query("category"))}
	filter.Page, filter.PageSize = response.ParsePagination(c)
	var err error
	filter.StartDate, err = parseExtraCostDateQuery(c.Query("start_date"))
	if err != nil {
		response.BadRequest(c, "Invalid start_date")
		return
	}
	filter.EndDate, err = parseExtraCostDateQuery(c.Query("end_date"))
	if err != nil {
		response.BadRequest(c, "Invalid end_date")
		return
	}
	if filter.EndDate != nil {
		end := filter.EndDate.AddDate(0, 0, 1)
		filter.EndDate = &end
	}
	items, total, err := h.service.List(c.Request.Context(), filter)
	if err != nil {
		h.writeError(c, err)
		return
	}
	rangeTotal, err := h.service.Sum(c.Request.Context(), filter.StartDate, filter.EndDate)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	today := timezone.Today()
	tomorrow := today.AddDate(0, 0, 1)
	dailyTotal, err := h.service.Sum(c.Request.Context(), &today, &tomorrow)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	pages := int((total + int64(filter.PageSize) - 1) / int64(filter.PageSize))
	if pages < 1 {
		pages = 1
	}
	response.Success(c, gin.H{
		"items":       items,
		"total":       total,
		"page":        filter.Page,
		"page_size":   filter.PageSize,
		"pages":       pages,
		"daily_total": dailyTotal,
		"range_total": rangeTotal,
	})
}

type CreateExtraCostRequest struct {
	CostDate       string  `json:"cost_date" binding:"required"`
	Amount         float64 `json:"amount"`
	Category       string  `json:"category" binding:"required"`
	Notes          string  `json:"notes"`
	IdempotencyKey string  `json:"idempotency_key"`
}

// Create appends an extra cost entry.
// POST /api/v1/admin/extra-costs
func (h *ExtraCostHandler) Create(c *gin.Context) {
	var req CreateExtraCostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	var createdBy *int64
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		createdBy = &subject.UserID
	}
	entry, err := h.service.Create(c.Request.Context(), service.ExtraCostEntry{
		CostDate: req.CostDate, Amount: req.Amount, Category: req.Category, Notes: req.Notes,
		CreatedBy: createdBy, IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
	})
	if err != nil {
		h.writeError(c, err)
		return
	}
	response.Created(c, entry)
}

type ReverseExtraCostRequest struct {
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Reverse appends an equal and opposite adjustment; the original remains immutable.
// POST /api/v1/admin/extra-costs/:id/reverse
func (h *ExtraCostHandler) Reverse(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	var req ReverseExtraCostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	var createdBy *int64
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		createdBy = &subject.UserID
	}
	entry, err := h.service.Reverse(c.Request.Context(), id, createdBy, req.Reason, strings.TrimSpace(req.IdempotencyKey))
	if err != nil {
		h.writeError(c, err)
		return
	}
	response.Created(c, entry)
}

func parseExtraCostDateQuery(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (h *ExtraCostHandler) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrExtraCostInvalidDate), errors.Is(err, service.ErrExtraCostInvalidAmount), errors.Is(err, service.ErrExtraCostInvalidCategory), errors.Is(err, service.ErrExtraCostInvalidNote):
		response.BadRequest(c, err.Error())
	case errors.Is(err, service.ErrExtraCostNotFound):
		response.NotFound(c, err.Error())
	case errors.Is(err, service.ErrExtraCostAlreadyReversed):
		response.Error(c, http.StatusConflict, err.Error())
	default:
		response.ErrorFrom(c, err)
	}
}
