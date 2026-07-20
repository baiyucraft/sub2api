package handler

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ChannelMonitorUserHandler 渠道监控用户只读 handler。
type ChannelMonitorUserHandler struct {
	monitorService *service.ChannelMonitorService
	settingService *service.SettingService
	apiKeyService  channelMonitorAvailableGroupService
}

type channelMonitorAvailableGroupService interface {
	GetAvailableGroups(ctx context.Context, userID int64) ([]service.Group, error)
}

// NewChannelMonitorUserHandler 创建 handler。
// settingService 用于每次请求前读取功能开关；关闭时 List/GetStatus 直接返回空/404。
func NewChannelMonitorUserHandler(
	monitorService *service.ChannelMonitorService,
	settingService *service.SettingService,
	apiKeyService *service.APIKeyService,
) *ChannelMonitorUserHandler {
	return &ChannelMonitorUserHandler{
		monitorService: monitorService,
		settingService: settingService,
		apiKeyService:  apiKeyService,
	}
}

// featureEnabled 返回当前渠道监控功能是否开启。
// settingService 为 nil（测试场景）视为启用。
func (h *ChannelMonitorUserHandler) featureEnabled(c *gin.Context) bool {
	if h.settingService == nil {
		return true
	}
	return h.settingService.GetChannelMonitorRuntime(c.Request.Context()).Enabled
}

// --- Response ---

type channelMonitorUserListItem struct {
	ID                   int64                                `json:"id"`
	Name                 string                               `json:"name"`
	Provider             string                               `json:"provider"`
	GroupName            string                               `json:"group_name"`
	PrimaryModel         string                               `json:"primary_model"`
	PrimaryStatus        string                               `json:"primary_status"`
	PrimaryLatencyMs     *int                                 `json:"primary_latency_ms"`
	PrimaryPingLatencyMs *int                                 `json:"primary_ping_latency_ms"`
	Availability7d       float64                              `json:"availability_7d"`
	Availability         float64                              `json:"availability"`
	ExtraModels          []dto.ChannelMonitorExtraModelStatus `json:"extra_models"`
	Timeline             []channelMonitorUserTimelinePoint    `json:"timeline"`
	ShowGroupRate        bool                                 `json:"show_group_rate"`
	CurrentPublicRate    *float64                             `json:"current_public_rate,omitempty"`
	RateObservedSince    *string                              `json:"rate_observed_since,omitempty"`
	RateTrend            []channelMonitorUserRateTrendPoint   `json:"rate_trend,omitempty"`
}

// channelMonitorUserTimelinePoint 主模型最近一次检测的 timeline 点。
// 仅用于用户视图 list 响应，admin 视图不使用。
type channelMonitorUserTimelinePoint struct {
	Status        string `json:"status"`
	LatencyMs     *int   `json:"latency_ms"`
	PingLatencyMs *int   `json:"ping_latency_ms"`
	CheckedAt     string `json:"checked_at"`
}

type channelMonitorUserRateTrendPoint struct {
	ObservedAt string  `json:"observed_at"`
	Rate       float64 `json:"rate"`
}

type channelMonitorUserDetailResponse struct {
	ID                int64                              `json:"id"`
	Name              string                             `json:"name"`
	Provider          string                             `json:"provider"`
	GroupName         string                             `json:"group_name"`
	Models            []channelMonitorUserModelStat      `json:"models"`
	ShowGroupRate     bool                               `json:"show_group_rate"`
	CurrentPublicRate *float64                           `json:"current_public_rate,omitempty"`
	RateObservedSince *string                            `json:"rate_observed_since,omitempty"`
	RateTrend         []channelMonitorUserRateTrendPoint `json:"rate_trend,omitempty"`
}

type channelMonitorUserModelStat struct {
	Model           string  `json:"model"`
	LatestStatus    string  `json:"latest_status"`
	LatestLatencyMs *int    `json:"latest_latency_ms"`
	Availability24h float64 `json:"availability_24h"`
	Availability7d  float64 `json:"availability_7d"`
	Availability15d float64 `json:"availability_15d"`
	Availability30d float64 `json:"availability_30d"`
	AvgLatency7dMs  *int    `json:"avg_latency_7d_ms"`
}

func userMonitorViewToItem(v *service.UserMonitorView) channelMonitorUserListItem {
	extras := make([]dto.ChannelMonitorExtraModelStatus, 0, len(v.ExtraModels))
	for _, e := range v.ExtraModels {
		extras = append(extras, dto.ChannelMonitorExtraModelStatus{
			Model:     e.Model,
			Status:    e.Status,
			LatencyMs: e.LatencyMs,
		})
	}
	timeline := make([]channelMonitorUserTimelinePoint, 0, len(v.Timeline))
	for _, p := range v.Timeline {
		timeline = append(timeline, channelMonitorUserTimelinePoint{
			Status:        p.Status,
			LatencyMs:     p.LatencyMs,
			PingLatencyMs: p.PingLatencyMs,
			CheckedAt:     p.CheckedAt.UTC().Format(time.RFC3339),
		})
	}
	rateTrend := userMonitorRateTrendToResponse(v.RateTrend)
	return channelMonitorUserListItem{
		ID:                   v.ID,
		Name:                 v.Name,
		Provider:             v.Provider,
		GroupName:            v.GroupName,
		PrimaryModel:         v.PrimaryModel,
		PrimaryStatus:        v.PrimaryStatus,
		PrimaryLatencyMs:     v.PrimaryLatencyMs,
		PrimaryPingLatencyMs: v.PrimaryPingLatencyMs,
		Availability7d:       v.Availability7d,
		Availability:         v.Availability,
		ExtraModels:          extras,
		Timeline:             timeline,
		ShowGroupRate:        v.ShowGroupRate,
		CurrentPublicRate:    v.CurrentPublicRate,
		RateObservedSince:    formatOptionalMonitorTime(v.RateObservedSince),
		RateTrend:            rateTrend,
	}
}

func userMonitorDetailToResponse(d *service.UserMonitorDetail) *channelMonitorUserDetailResponse {
	models := make([]channelMonitorUserModelStat, 0, len(d.Models))
	for _, m := range d.Models {
		models = append(models, channelMonitorUserModelStat{
			Model:           m.Model,
			LatestStatus:    m.LatestStatus,
			LatestLatencyMs: m.LatestLatencyMs,
			Availability24h: m.Availability24h,
			Availability7d:  m.Availability7d,
			Availability15d: m.Availability15d,
			Availability30d: m.Availability30d,
			AvgLatency7dMs:  m.AvgLatency7dMs,
		})
	}
	return &channelMonitorUserDetailResponse{
		ID:                d.ID,
		Name:              d.Name,
		Provider:          d.Provider,
		GroupName:         d.GroupName,
		Models:            models,
		ShowGroupRate:     d.ShowGroupRate,
		CurrentPublicRate: d.CurrentPublicRate,
		RateObservedSince: formatOptionalMonitorTime(d.RateObservedSince),
		RateTrend:         userMonitorRateTrendToResponse(d.RateTrend),
	}
}

func userMonitorRateTrendToResponse(points []service.PublicRateTrendPoint) []channelMonitorUserRateTrendPoint {
	if points == nil {
		return nil
	}
	out := make([]channelMonitorUserRateTrendPoint, 0, len(points))
	for _, point := range points {
		out = append(out, channelMonitorUserRateTrendPoint{
			ObservedAt: point.ObservedAt.UTC().Format(time.RFC3339),
			Rate:       point.Rate,
		})
	}
	return out
}

func formatOptionalMonitorTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

// --- Handlers ---

// List GET /api/v1/channel-monitors
func (h *ChannelMonitorUserHandler) List(c *gin.Context) {
	monitorRange, err := service.ParseMonitorRateRange(requestedMonitorRange(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.featureEnabled(c) {
		response.Success(c, gin.H{"items": []channelMonitorUserListItem{}, "range": monitorRange})
		return
	}
	allowedGroupIDs, ok := h.userAllowedGroupIDs(c, subject.UserID)
	if !ok {
		return
	}
	views, err := h.monitorService.ListUserView(c.Request.Context(), monitorRange, allowedGroupIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items := make([]channelMonitorUserListItem, 0, len(views))
	for _, v := range views {
		items = append(items, userMonitorViewToItem(v))
	}
	response.Success(c, gin.H{"items": items, "range": monitorRange})
}

// GetStatus GET /api/v1/channel-monitors/:id/status
func (h *ChannelMonitorUserHandler) GetStatus(c *gin.Context) {
	monitorRange, err := service.ParseMonitorRateRange(requestedMonitorRange(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.featureEnabled(c) {
		response.ErrorFrom(c, service.ErrChannelMonitorNotFound)
		return
	}
	allowedGroupIDs, ok := h.userAllowedGroupIDs(c, subject.UserID)
	if !ok {
		return
	}
	// 复用 admin.ParseChannelMonitorID 保持错误码与日志一致。
	id, ok := admin.ParseChannelMonitorID(c)
	if !ok {
		return
	}
	detail, err := h.monitorService.GetUserDetail(c.Request.Context(), id, monitorRange, allowedGroupIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, userMonitorDetailToResponse(detail))
}

func requestedMonitorRange(c *gin.Context) string {
	if values, exists := c.Request.URL.Query()["range"]; exists {
		if len(values) == 0 {
			return ""
		}
		return values[0]
	}
	return c.Query("rate_range")
}

func (h *ChannelMonitorUserHandler) userAllowedGroupIDs(c *gin.Context, userID int64) (map[int64]struct{}, bool) {
	groups, err := h.apiKeyService.GetAvailableGroups(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return nil, false
	}
	allowed := make(map[int64]struct{}, len(groups))
	for i := range groups {
		allowed[groups[i].ID] = struct{}{}
	}
	return allowed, true
}
