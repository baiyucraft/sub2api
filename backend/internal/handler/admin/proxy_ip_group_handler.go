package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ProxyIPGroupHandler struct {
	service *service.ProxyIPGroupAdminService
}

func NewProxyIPGroupHandler(groupService *service.ProxyIPGroupAdminService) *ProxyIPGroupHandler {
	return &ProxyIPGroupHandler{service: groupService}
}

type proxyIPGroupRequest struct {
	Name             string  `json:"name"`
	PerIPConcurrency *int    `json:"per_ip_concurrency"`
	ProxyIDs         []int64 `json:"proxy_ids"`
}

type proxyIPGroupMembersRequest struct {
	ProxyIDs []int64 `json:"proxy_ids"`
}

type proxyIPGroupResponse struct {
	ID               int64     `json:"id"`
	Name             string    `json:"name"`
	PerIPConcurrency int       `json:"per_ip_concurrency"`
	ProxyIDs         []int64   `json:"proxy_ids"`
	MemberCount      int       `json:"member_count"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func proxyIPGroupResponseFromService(group *service.ProxyIPGroup) proxyIPGroupResponse {
	if group == nil {
		return proxyIPGroupResponse{}
	}
	proxyIDs := append([]int64(nil), group.ProxyIDs...)
	return proxyIPGroupResponse{
		ID:               group.ID,
		Name:             group.Name,
		PerIPConcurrency: group.PerIPConcurrency,
		ProxyIDs:         proxyIDs,
		MemberCount:      len(proxyIDs),
		CreatedAt:        group.CreatedAt,
		UpdatedAt:        group.UpdatedAt,
	}
}

func parseProxyIPGroupID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid proxy IP group ID")
		return 0, false
	}
	return id, true
}

func (h *ProxyIPGroupHandler) List(c *gin.Context) {
	groups, err := h.service.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]proxyIPGroupResponse, 0, len(groups))
	for i := range groups {
		out = append(out, proxyIPGroupResponseFromService(&groups[i]))
	}
	response.Success(c, out)
}

func (h *ProxyIPGroupHandler) GetByID(c *gin.Context) {
	id, ok := parseProxyIPGroupID(c)
	if !ok {
		return
	}
	group, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, proxyIPGroupResponseFromService(group))
}

func (h *ProxyIPGroupHandler) Create(c *gin.Context) {
	var req proxyIPGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	concurrency := 10
	if req.PerIPConcurrency != nil {
		concurrency = *req.PerIPConcurrency
	}
	group, err := h.service.Create(c.Request.Context(), service.CreateProxyIPGroupInput{
		Name:             req.Name,
		PerIPConcurrency: concurrency,
		ProxyIDs:         req.ProxyIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": proxyIPGroupResponseFromService(group)})
}

func (h *ProxyIPGroupHandler) Update(c *gin.Context) {
	id, ok := parseProxyIPGroupID(c)
	if !ok {
		return
	}
	var req proxyIPGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	name := req.Name
	proxyIDs := append([]int64(nil), req.ProxyIDs...)
	group, err := h.service.Update(c.Request.Context(), id, service.UpdateProxyIPGroupInput{
		Name:             &name,
		PerIPConcurrency: req.PerIPConcurrency,
		ProxyIDs:         &proxyIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, proxyIPGroupResponseFromService(group))
}

func (h *ProxyIPGroupHandler) UpdateMembers(c *gin.Context) {
	id, ok := parseProxyIPGroupID(c)
	if !ok {
		return
	}
	var req proxyIPGroupMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	proxyIDs := append([]int64(nil), req.ProxyIDs...)
	group, err := h.service.Update(c.Request.Context(), id, service.UpdateProxyIPGroupInput{
		ProxyIDs: &proxyIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, proxyIPGroupResponseFromService(group))
}

func (h *ProxyIPGroupHandler) Delete(c *gin.Context) {
	id, ok := parseProxyIPGroupID(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Proxy IP group deleted"})
}
