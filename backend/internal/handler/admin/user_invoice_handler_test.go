package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type invoiceHandlerService struct {
	service.AdminService
	userID, invoiceID, actorID int64
	page, pageSize             int
	input                      service.UserInvoiceInput
	reason                     string
	calls                      int
	err                        error
}

func (s *invoiceHandlerService) ListUserInvoices(_ context.Context, userID int64, page, size int) (*service.UserInvoicePage, error) {
	s.userID, s.page, s.pageSize = userID, page, size
	s.calls++
	return &service.UserInvoicePage{Items: []service.UserInvoiceAnnotation{}, Page: page, PageSize: size, Pages: 1, TotalInvoiced: 42.5}, s.err
}

func (s *invoiceHandlerService) CreateUserInvoice(_ context.Context, userID, actorID int64, input service.UserInvoiceInput) (*service.UserInvoiceAnnotation, error) {
	s.userID, s.actorID, s.input = userID, actorID, input
	s.calls++
	return &service.UserInvoiceAnnotation{ID: 9, UserID: userID, Amount: input.Amount, CreatedBy: actorID}, s.err
}

func (s *invoiceHandlerService) UpdateUserInvoice(_ context.Context, userID, invoiceID, actorID int64, input service.UserInvoiceInput) (*service.UserInvoiceAnnotation, error) {
	s.userID, s.invoiceID, s.actorID, s.input = userID, invoiceID, actorID, input
	s.calls++
	return &service.UserInvoiceAnnotation{ID: invoiceID, UserID: userID}, s.err
}

func (s *invoiceHandlerService) VoidUserInvoice(_ context.Context, userID, invoiceID, actorID int64, reason string) (*service.UserInvoiceAnnotation, error) {
	s.userID, s.invoiceID, s.actorID, s.reason = userID, invoiceID, actorID, reason
	s.calls++
	return &service.UserInvoiceAnnotation{ID: invoiceID, UserID: userID, Status: "voided"}, s.err
}

func invoiceHandlerContext(method, url, body string, authenticated bool, role string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, url, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "7"}, {Key: "invoice_id", Value: "9"}}
	if authenticated {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 3})
		c.Set(string(middleware.ContextKeyUserRole), role)
	}
	return c, w
}

func TestUserInvoiceHandlerListReturnsIndependentSummary(t *testing.T) {
	svc := &invoiceHandlerService{}
	h := &UserHandler{adminService: svc}
	c, w := invoiceHandlerContext(http.MethodGet, "/?page=2&page_size=10&type=subscription", "", true, service.RoleAdmin)
	h.ListInvoices(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(7), svc.userID)
	require.Equal(t, 2, svc.page)
	require.Equal(t, 10, svc.pageSize)
	var out struct {
		Data service.UserInvoicePage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, 42.5, out.Data.TotalInvoiced)
}

func TestUserInvoiceHandlerIgnoresBodyActorAndScopesRecord(t *testing.T) {
	svc := &invoiceHandlerService{}
	h := &UserHandler{adminService: svc}
	body := `{"amount":25.5,"invoice_date":"2026-10-12","invoice_number":"INV-1","notes":"补录","operation_id":"operation-1","expected_updated_at":"2026-10-12T00:00:00Z","created_by":999,"actor_id":999,"user_id":888}`
	c, w := invoiceHandlerContext(http.MethodPost, "/", body, true, service.RoleAdmin)
	h.CreateInvoice(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(7), svc.userID)
	require.Equal(t, int64(3), svc.actorID)
	require.Equal(t, "operation-1", svc.input.OperationID)
	c, w = invoiceHandlerContext(http.MethodPut, "/", body, true, service.RoleAdmin)
	h.UpdateInvoice(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(9), svc.invoiceID)
	require.Equal(t, int64(3), svc.actorID)
	require.NotNil(t, svc.input.ExpectedUpdatedAt)
	require.Equal(t, "2026-10-12T00:00:00Z", svc.input.ExpectedUpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	c, w = invoiceHandlerContext(http.MethodPost, "/", `{"reason":"误填","voided_by":999}`, true, service.RoleAdmin)
	h.VoidInvoice(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "误填", svc.reason)
	require.Equal(t, int64(3), svc.actorID)
}

func TestUserInvoiceHandlerRequiresAdministrator(t *testing.T) {
	for _, method := range []string{"list", "create", "update", "void"} {
		for _, role := range []string{"", service.RoleUser} {
			t.Run(method+role, func(t *testing.T) {
				svc := &invoiceHandlerService{}
				h := &UserHandler{adminService: svc}
				c, w := invoiceHandlerContext(http.MethodPost, "/", `{}`, role != "", role)
				switch method {
				case "list":
					h.ListInvoices(c)
				case "create":
					h.CreateInvoice(c)
				case "update":
					h.UpdateInvoice(c)
				case "void":
					h.VoidInvoice(c)
				}
				if role == "" {
					require.Equal(t, 401, w.Code)
				} else {
					require.Equal(t, 403, w.Code)
				}
				require.Zero(t, svc.calls)
			})
		}
	}
}

func TestUserInvoiceHandlerInvalidRequestAndServiceErrors(t *testing.T) {
	svc := &invoiceHandlerService{}
	h := &UserHandler{adminService: svc}
	c, w := invoiceHandlerContext(http.MethodPost, "/", `{"amount":`, true, service.RoleAdmin)
	h.CreateInvoice(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, svc.calls)
	c, w = invoiceHandlerContext(http.MethodPost, "/", `{}`, true, service.RoleAdmin)
	c.Params[1].Value = "-9"
	h.VoidInvoice(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, svc.calls)
	for _, body := range []string{`{"amount":0.10000000000000001}`, `{"amount":1e999999999999999999}`} {
		c, w = invoiceHandlerContext(http.MethodPost, "/", body, true, service.RoleAdmin)
		h.CreateInvoice(c)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Zero(t, svc.calls)
	}
	svc.err = infraerrors.Conflict("USER_INVOICE_VOIDED", "already voided")
	c, w = invoiceHandlerContext(http.MethodPut, "/", `{"amount":25.5}`, true, service.RoleAdmin)
	h.UpdateInvoice(c)
	require.Equal(t, http.StatusConflict, w.Code)
}
