package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *UserHandler) invoiceService(c *gin.Context) (service.UserInvoiceAdminService, int64, bool) {
	subject, authenticated := middleware.GetAuthSubjectFromContext(c)
	role, hasRole := middleware.GetUserRoleFromContext(c)
	if !authenticated || subject.UserID <= 0 {
		response.Unauthorized(c, "Authenticated administrator required")
		return nil, 0, false
	}
	if !hasRole || role != service.RoleAdmin {
		response.Forbidden(c, "Admin access required")
		return nil, 0, false
	}
	invoices, ok := h.adminService.(service.UserInvoiceAdminService)
	if !ok {
		response.InternalError(c, "Invoice annotations are unavailable")
		return nil, 0, false
	}
	return invoices, subject.UserID, true
}

func invoicePathID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid "+name)
		return 0, false
	}
	return id, true
}

// ListInvoices returns all annotations, including voided ones. Its active amount
// total is independent of pagination and balance-history type selection.
func (h *UserHandler) ListInvoices(c *gin.Context) {
	invoices, _, ok := h.invoiceService(c)
	if !ok {
		return
	}
	userID, ok := invoicePathID(c, "id")
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	result, err := invoices.ListUserInvoices(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *UserHandler) CreateInvoice(c *gin.Context) {
	invoices, actorID, ok := h.invoiceService(c)
	if !ok {
		return
	}
	userID, ok := invoicePathID(c, "id")
	if !ok {
		return
	}
	var input service.UserInvoiceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid invoice annotation request")
		return
	}
	record, err := invoices.CreateUserInvoice(c.Request.Context(), userID, actorID, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Replays have the same successful response as the original create.
	response.Success(c, record)
}

func (h *UserHandler) UpdateInvoice(c *gin.Context) {
	invoices, actorID, ok := h.invoiceService(c)
	if !ok {
		return
	}
	userID, ok := invoicePathID(c, "id")
	if !ok {
		return
	}
	invoiceID, ok := invoicePathID(c, "invoice_id")
	if !ok {
		return
	}
	var input service.UserInvoiceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid invoice annotation request")
		return
	}
	record, err := invoices.UpdateUserInvoice(c.Request.Context(), userID, invoiceID, actorID, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, record)
}

func (h *UserHandler) VoidInvoice(c *gin.Context) {
	invoices, actorID, ok := h.invoiceService(c)
	if !ok {
		return
	}
	userID, ok := invoicePathID(c, "id")
	if !ok {
		return
	}
	invoiceID, ok := invoicePathID(c, "invoice_id")
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid invoice void request")
		return
	}
	record, err := invoices.VoidUserInvoice(c.Request.Context(), userID, invoiceID, actorID, input.Reason)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, record)
}
