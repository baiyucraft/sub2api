package service

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// UserBalanceHistoryRecord is a read model spanning independent ledgers. Legacy
// fields remain compatible; RecordSource and SourceID identify the real record.
type UserBalanceHistoryRecord struct {
	ID           int64
	Code         string
	Type         string
	Value        float64
	Status       string
	UsedBy       *int64
	UsedAt       *time.Time
	CreatedAt    time.Time
	ExpiresAt    *time.Time
	GroupID      *int64
	ValidityDays int
	Notes        string
	User         *User
	Group        *Group
	RecordSource string
	SourceID     int64
	ActivityType string
	PeriodDate   *string
}

// UserBalanceHistoryPage keeps lifetime monetary totals independent of the
// selected source and page. Rewards are not part of TotalRecharged.
type UserBalanceHistoryPage struct {
	Items          []UserBalanceHistoryRecord
	Total          int64
	TotalRecharged float64
	TotalRewarded  float64
}

func userBalanceHistoryFromRedeem(code RedeemCode) UserBalanceHistoryRecord {
	source, sourceID := "redeem_code", code.ID
	if code.Type == RedeemTypeAffiliateBalance {
		source, sourceID = "affiliate_ledger", -code.ID
	}
	return UserBalanceHistoryRecord{
		ID: code.ID, Code: code.Code, Type: code.Type, Value: code.Value,
		Status: code.Status, UsedBy: code.UsedBy, UsedAt: code.UsedAt,
		CreatedAt: code.CreatedAt, ExpiresAt: code.ExpiresAt, GroupID: code.GroupID,
		ValidityDays: code.ValidityDays, Notes: code.Notes, User: code.User,
		Group: code.Group, RecordSource: source, SourceID: sourceID,
	}
}

func userBalanceHistoryTime(record UserBalanceHistoryRecord) time.Time {
	if record.UsedAt != nil {
		return *record.UsedAt
	}
	return record.CreatedAt
}

func paginateUserBalanceHistory(records []UserBalanceHistoryRecord, params pagination.PaginationParams) []UserBalanceHistoryRecord {
	ordered := append([]UserBalanceHistoryRecord{}, records...)
	sort.Slice(ordered, func(i, j int) bool {
		left, right := userBalanceHistoryTime(ordered[i]), userBalanceHistoryTime(ordered[j])
		if !left.Equal(right) {
			return left.After(right)
		}
		if ordered[i].RecordSource != ordered[j].RecordSource {
			return ordered[i].RecordSource < ordered[j].RecordSource
		}
		return ordered[i].SourceID > ordered[j].SourceID
	})
	offset := params.Offset()
	if offset < 0 || offset >= len(ordered) {
		return []UserBalanceHistoryRecord{}
	}
	end := min(offset+params.Limit(), len(ordered))
	return ordered[offset:end]
}

func (s *adminServiceImpl) activityRewardHistoryTotals(ctx context.Context, userID int64) (int64, float64, error) {
	if s.entClient == nil {
		return 0, 0, nil
	}
	rows, err := s.entClient.QueryContext(ctx, `SELECT COUNT(*), COALESCE(SUM(amount),0)
		FROM activity_reward_records WHERE user_id=$1 AND status='credited'`, userID)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var count int64
	var amount float64
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return 0, 0, err
		}
		return 0, 0, sql.ErrNoRows
	}
	if err = rows.Scan(&count, &amount); err != nil {
		return 0, 0, err
	}
	return count, amount, rows.Err()
}

func (s *adminServiceImpl) listActivityRewardHistory(ctx context.Context, userID int64, limit, offset int) ([]UserBalanceHistoryRecord, error) {
	items := []UserBalanceHistoryRecord{}
	if s.entClient == nil || limit <= 0 {
		return items, nil
	}
	rows, err := s.entClient.QueryContext(ctx, `SELECT id,activity_type,amount,period_date::text,source,created_at
		FROM activity_reward_records WHERE user_id=$1 AND status='credited'
		ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item := UserBalanceHistoryRecord{Type: "activity_reward", Status: "credited", RecordSource: "activity_reward"}
		var period sql.NullString
		var source string
		if err = rows.Scan(&item.ID, &item.ActivityType, &item.Value, &period, &source, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.SourceID = item.ID
		owner, claimedAt := userID, item.CreatedAt
		item.UsedBy, item.UsedAt = &owner, &claimedAt
		if period.Valid {
			item.PeriodDate = &period.String
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *adminServiceImpl) userBalanceHistory(ctx context.Context, userID int64, params pagination.PaginationParams, codeType string) (*UserBalanceHistoryPage, error) {
	if userID <= 0 {
		return nil, errors.New("invalid user ID")
	}
	rewardCount, totalRewarded, err := s.activityRewardHistoryTotals(ctx, userID)
	if err != nil {
		return nil, err
	}
	totalRecharged, err := s.redeemCodeRepo.SumPositiveBalanceByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := &UserBalanceHistoryPage{Items: []UserBalanceHistoryRecord{}, TotalRecharged: totalRecharged, TotalRewarded: totalRewarded}
	if codeType == "activity_reward" {
		result.Items, err = s.listActivityRewardHistory(ctx, userID, params.Limit(), params.Offset())
		result.Total = rewardCount
		return result, err
	}
	if codeType != "" {
		var codes []RedeemCode
		if codeType == RedeemTypeAffiliateBalance {
			codes, result.Total, err = s.listAffiliateBalanceHistory(ctx, userID, params)
		} else {
			var page *pagination.PaginationResult
			codes, page, err = s.redeemCodeRepo.ListByUserPaginated(ctx, userID, params, codeType)
			if err == nil {
				result.Total = page.Total
			}
		}
		for _, code := range codes {
			result.Items = append(result.Items, userBalanceHistoryFromRedeem(code))
		}
		return result, err
	}
	// Each source must supply offset+limit rows from its beginning; fetching the
	// same page per source would silently omit records after the merge.
	needed := params.Offset() + params.Limit()
	if needed < params.Limit() {
		return nil, errors.New("history page is too large")
	}
	redeems, redeemCount, err := s.listRedeemBalanceHistoryForMerge(ctx, userID, needed)
	if err != nil {
		return nil, err
	}
	affiliates, affiliateCount, err := s.listAffiliateBalanceHistoryForMerge(ctx, userID, needed)
	if err != nil {
		return nil, err
	}
	rewards, err := s.listActivityRewardHistory(ctx, userID, needed, 0)
	if err != nil {
		return nil, err
	}
	for _, code := range append(redeems, affiliates...) {
		rewards = append(rewards, userBalanceHistoryFromRedeem(code))
	}
	result.Items = paginateUserBalanceHistory(rewards, params)
	result.Total = redeemCount + affiliateCount + rewardCount
	return result, nil
}
