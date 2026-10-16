package repository

import (
	"context"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *accountRepository) AccountQualityCandidateIDs(ctx context.Context, query service.AccountQualityCandidatesQuery) ([]int64, error) {
	q := r.accountListFilteredQuery(ctx, query.Platform, query.Type, query.Status, query.Search, query.GroupID, query.PrivacyMode, query.Scope)
	for _, order := range accountListOrder(query.Pagination()) {
		q = q.Order(order)
	}
	return q.IDs(ctx)
}

func (r *accountRepository) AccountQualityCandidatePage(ctx context.Context, query service.AccountQualityCandidatesQuery, ids []int64) ([]service.Account, error) {
	if len(ids) == 0 {
		return []service.Account{}, nil
	}
	q := r.accountListFilteredQuery(ctx, query.Platform, query.Type, query.Status, query.Search, query.GroupID, query.PrivacyMode, query.Scope)
	rows, err := q.Where(dbaccount.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := r.accountsToService(ctx, rows)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]service.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	ordered := make([]service.Account, 0, len(accounts))
	for _, id := range ids {
		if account, ok := byID[id]; ok {
			ordered = append(ordered, account)
		}
	}
	return ordered, nil
}
