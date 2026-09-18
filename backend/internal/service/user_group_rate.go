package service

import "context"

// UserGroupRateEntry 分组下用户专属倍率/RPM 条目。
// RatePercent 是持久化真值，RateMultiplier 是按当前分组普通倍率计算的兼容有效值。
type UserGroupRateEntry struct {
	UserID         int64    `json:"user_id"`
	UserName       string   `json:"user_name"`
	UserEmail      string   `json:"user_email"`
	UserNotes      string   `json:"user_notes"`
	UserStatus     string   `json:"user_status"`
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`
	RatePercent    *float64 `json:"rate_percent,omitempty"`
	RPMOverride    *int     `json:"rpm_override,omitempty"`
}

// GroupRateMultiplierInput 批量设置分组专属倍率的输入条目。
// 新客户端提交 RatePercent；RateMultiplier 仅用于兼容旧客户端。两者不可同时设置。
type GroupRateMultiplierInput struct {
	UserID         int64    `json:"user_id"`
	RatePercent    *float64 `json:"rate_percent,omitempty"`
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`
}

// UserGroupRatePercentReader 暴露专属比例真值，供运行时缓存和管理端展示使用。
// 保持为窄接口，避免破坏仍只实现旧有效倍率接口的测试或扩展仓储。
type UserGroupRatePercentReader interface {
	GetPercentByUserID(ctx context.Context, userID int64) (map[int64]float64, error)
	GetPercentByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error)
}

// UserGroupRatePercentWriter 使用百分比语义同步一个用户的专属倍率。
type UserGroupRatePercentWriter interface {
	SyncUserGroupRatePercents(ctx context.Context, userID int64, percents map[int64]*float64) error
}

// GroupRPMOverrideInput 批量设置分组 RPM override 的输入条目。
// RPMOverride 为 *int 以支持清除（nil）语义。
type GroupRPMOverrideInput struct {
	UserID      int64 `json:"user_id"`
	RPMOverride *int  `json:"rpm_override"`
}

// UserGroupRateRepository 用户专属分组倍率/RPM 仓储接口。
// 允许管理员为特定用户设置分组的专属计费倍率与 RPM 上限，覆盖分组默认值。
type UserGroupRateRepository interface {
	// GetByUserID 获取用户所有专属分组 rate_multiplier（仅返回非 NULL 的条目）
	GetByUserID(ctx context.Context, userID int64) (map[int64]float64, error)

	// GetByUserAndGroup 获取用户在特定分组的专属 rate_multiplier（NULL 返回 nil）
	GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error)

	// GetRPMOverrideByUserAndGroup 获取用户在特定分组的 rpm_override（NULL 返回 nil）
	GetRPMOverrideByUserAndGroup(ctx context.Context, userID, groupID int64) (*int, error)

	// GetByGroupID 获取指定分组下所有用户的专属配置（rate 与 rpm_override 任一非 NULL 即返回）
	GetByGroupID(ctx context.Context, groupID int64) ([]UserGroupRateEntry, error)

	// SyncUserGroupRates 同步用户的分组专属倍率；nil 表示清空该分组的 rate_multiplier
	SyncUserGroupRates(ctx context.Context, userID int64, rates map[int64]*float64) error

	// SyncGroupRateMultipliers 批量同步分组的用户专属倍率（替换整组 rate 部分）
	SyncGroupRateMultipliers(ctx context.Context, groupID int64, entries []GroupRateMultiplierInput) error

	// SyncGroupRPMOverrides 批量同步分组的用户专属 RPM（替换整组 rpm_override 部分）。
	// 条目中 RPMOverride 为 nil 时清空对应行的 rpm_override；非 nil 时 upsert。
	SyncGroupRPMOverrides(ctx context.Context, groupID int64, entries []GroupRPMOverrideInput) error

	// ClearGroupRPMOverrides 清空指定分组的所有 rpm_override（整组 rpm 部分归 NULL）
	ClearGroupRPMOverrides(ctx context.Context, groupID int64) error

	// DeleteByGroupID 删除指定分组的所有用户专属条目（分组删除时调用）
	DeleteByGroupID(ctx context.Context, groupID int64) error

	// DeleteByUserID 删除指定用户的所有专属条目（用户删除时调用）
	DeleteByUserID(ctx context.Context, userID int64) error
}
