package service

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// AccountQualityCandidatesQuery shares the ordinary list predicates and order.
// Scoring remains in AccountQualityFilter; the repository only returns IDs.
type AccountQualityCandidatesQuery struct {
	Platform, Type, Status, Search, PrivacyMode, SortBy, SortOrder string
	GroupID                                                        int64
	Scope                                                          AccountListScope
}

type accountQualityCandidateRepository interface {
	AccountQualityCandidateIDs(context.Context, AccountQualityCandidatesQuery) ([]int64, error)
	AccountQualityCandidatePage(context.Context, AccountQualityCandidatesQuery, []int64) ([]Account, error)
}

func (s *adminServiceImpl) AccountQualityCandidateIDs(ctx context.Context, query AccountQualityCandidatesQuery) ([]int64, error) {
	if !query.Scope.Valid() {
		return nil, fmt.Errorf("invalid account list scope %q", query.Scope)
	}
	if query.GroupID > 0 {
		if err := s.ValidateAccountGroupBindings(ctx, []int64{query.GroupID}); err != nil {
			return nil, err
		}
	}
	reader, ok := s.accountRepo.(accountQualityCandidateRepository)
	if !ok {
		return nil, fmt.Errorf("lean account quality listing is not supported")
	}
	ids, err := accountListSharedRead(ctx, fmt.Sprintf("%p", s), "quality_candidate_ids", query, func(shared context.Context) ([]int64, error) {
		return reader.AccountQualityCandidateIDs(shared, query)
	})
	return append([]int64{}, ids...), err
}

func (s *adminServiceImpl) AccountQualityCandidatePage(ctx context.Context, query AccountQualityCandidatesQuery, ids []int64) ([]Account, error) {
	reader, ok := s.accountRepo.(accountQualityCandidateRepository)
	if !ok {
		return nil, fmt.Errorf("lean account quality listing is not supported")
	}
	return reader.AccountQualityCandidatePage(ctx, query, ids)
}

func (query AccountQualityCandidatesQuery) Pagination() pagination.PaginationParams {
	return pagination.PaginationParams{SortBy: query.SortBy, SortOrder: query.SortOrder}
}
