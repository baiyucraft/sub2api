//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestList_DefaultSortByNameAsc() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "z-account"})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "a-account"})

	accounts, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)
	s.Require().Equal("a-account", accounts[0].Name)
	s.Require().Equal("z-account", accounts[1].Name)
}

func (s *AccountRepoSuite) TestListWithFilters_SortByPriorityDesc() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "low-priority", Priority: 10})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "high-priority", Priority: 90})

	accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "priority",
		SortOrder: "desc",
	}, "", "", "", "", 0, "")
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)
	s.Require().Equal("high-priority", accounts[0].Name)
	s.Require().Equal("low-priority", accounts[1].Name)
}

func (s *AccountRepoSuite) TestListWithFilters_SortByLastUsedDescNullsLast() {
	early := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	late := time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)

	mustCreateAccount(s.T(), s.client, &service.Account{Name: "never-used"})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "used-earlier", LastUsedAt: &early})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "used-later", LastUsedAt: &late})

	accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "last_used_at",
		SortOrder: "desc",
	}, "", "", "", "", 0, "")
	s.Require().NoError(err)
	s.Require().Len(accounts, 3)
	s.Require().Equal("used-later", accounts[0].Name)
	s.Require().Equal("used-earlier", accounts[1].Name)
	s.Require().Equal("never-used", accounts[2].Name)
}
