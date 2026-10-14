package admin

import (
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *GroupHandler) SetSchedulingProtectionPolicyService(manager service.GroupSchedulingProtectionPolicyManager) {
	if h != nil {
		h.schedulingProtectionPolicies = manager
	}
}

func (h *GroupHandler) protectionPoliciesAvailable(c *gin.Context) bool {
	if h.rejectUnsupportedSimpleModeOperation(c, "advanced") {
		return false
	}
	if h.schedulingProtectionPolicies == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("GROUP_PROTECTION_UNAVAILABLE", "group scheduling protection policies are unavailable"))
		return false
	}
	return true
}

func (h *GroupHandler) ListSchedulingProtectionPolicies(c *gin.Context) {
	if !h.protectionPoliciesAvailable(c) {
		return
	}
	views, err := h.schedulingProtectionPolicies.ListProtectionPolicies(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, views)
}

func (h *GroupHandler) GetSchedulingProtectionPolicy(c *gin.Context) {
	if !h.protectionPoliciesAvailable(c) {
		return
	}
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	view, err := h.schedulingProtectionPolicies.GetProtectionPolicy(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

func (h *GroupHandler) PutSchedulingProtectionPolicy(c *gin.Context) {
	if !h.protectionPoliciesAvailable(c) {
		return
	}
	id, ok := parsePositiveIDParam(c, "id")
	if !ok {
		return
	}
	var input service.GroupSchedulingProtectionPolicyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "invalid group protection policy: "+err.Error())
		return
	}
	view, err := h.schedulingProtectionPolicies.PutProtectionPolicy(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}
