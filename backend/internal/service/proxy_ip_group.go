package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrProxyIPGroupNotFound = infraerrors.NotFound("PROXY_IP_GROUP_NOT_FOUND", "proxy IP group not found")
	ErrProxyIPGroupInUse    = infraerrors.Conflict("PROXY_IP_GROUP_IN_USE", "proxy IP group is in use by accounts")
	ErrProxyIPGroupExists   = infraerrors.Conflict("PROXY_IP_GROUP_EXISTS", "a proxy IP group with this name already exists")
)

// ProxyIPGroup is the resolver-facing summary of one ordered proxy pool.
// ProxyIDs preserves the configured member order.
type ProxyIPGroup struct {
	ID               int64
	Name             string
	PerIPConcurrency int
	ProxyIDs         []int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ProxyIPGroupRepository is intentionally narrow so gateway resolvers can
// depend on group lookup without importing administrative mutation methods.
type ProxyIPGroupRepository interface {
	GetByID(ctx context.Context, id int64) (*ProxyIPGroup, error)
	List(ctx context.Context) ([]ProxyIPGroup, error)
	Create(ctx context.Context, group *ProxyIPGroup) error
	Update(ctx context.Context, group *ProxyIPGroup) error
	Delete(ctx context.Context, id int64) error
	CountAccounts(ctx context.Context, id int64) (int64, error)
}

type CreateProxyIPGroupInput struct {
	Name             string
	PerIPConcurrency int
	ProxyIDs         []int64
}

type UpdateProxyIPGroupInput struct {
	Name             *string
	PerIPConcurrency *int
	ProxyIDs         *[]int64
}

type ProxyIPGroupAdminService struct {
	repo      ProxyIPGroupRepository
	proxyRepo ProxyRepository
}

func NewProxyIPGroupAdminService(repo ProxyIPGroupRepository, proxyRepo ProxyRepository) *ProxyIPGroupAdminService {
	return &ProxyIPGroupAdminService{repo: repo, proxyRepo: proxyRepo}
}

func (s *ProxyIPGroupAdminService) List(ctx context.Context) ([]ProxyIPGroup, error) {
	return s.repo.List(ctx)
}

func (s *ProxyIPGroupAdminService) GetByID(ctx context.Context, id int64) (*ProxyIPGroup, error) {
	return s.repo.GetByID(ctx, id)
}

func uniqueProxyIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *ProxyIPGroupAdminService) validate(ctx context.Context, name string, concurrency int, proxyIDs []int64) (string, []int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return "", nil, infraerrors.BadRequest("PROXY_IP_GROUP_NAME_INVALID", "proxy IP group name is required and must not exceed 100 characters")
	}
	if concurrency < 1 || concurrency > 1000 {
		return "", nil, infraerrors.BadRequest("PROXY_IP_GROUP_CONCURRENCY_INVALID", "per-IP concurrency must be between 1 and 1000")
	}
	proxyIDs = uniqueProxyIDs(proxyIDs)
	if len(proxyIDs) > 0 {
		proxies, err := s.proxyRepo.ListByIDs(ctx, proxyIDs)
		if err != nil {
			return "", nil, err
		}
		if len(proxies) != len(proxyIDs) {
			return "", nil, infraerrors.BadRequest("PROXY_IP_GROUP_MEMBER_INVALID", "one or more proxy members do not exist")
		}
	}
	return name, proxyIDs, nil
}

func (s *ProxyIPGroupAdminService) Create(ctx context.Context, input CreateProxyIPGroupInput) (*ProxyIPGroup, error) {
	name, proxyIDs, err := s.validate(ctx, input.Name, input.PerIPConcurrency, input.ProxyIDs)
	if err != nil {
		return nil, err
	}
	group := &ProxyIPGroup{Name: name, PerIPConcurrency: input.PerIPConcurrency, ProxyIDs: proxyIDs}
	if err := s.repo.Create(ctx, group); err != nil {
		return nil, err
	}
	return group, nil
}

func (s *ProxyIPGroupAdminService) Update(ctx context.Context, id int64, input UpdateProxyIPGroupInput) (*ProxyIPGroup, error) {
	group, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if input.Name != nil {
		group.Name = *input.Name
	}
	if input.PerIPConcurrency != nil {
		group.PerIPConcurrency = *input.PerIPConcurrency
	}
	if input.ProxyIDs != nil {
		group.ProxyIDs = append([]int64(nil), (*input.ProxyIDs)...)
	}
	name, proxyIDs, err := s.validate(ctx, group.Name, group.PerIPConcurrency, group.ProxyIDs)
	if err != nil {
		return nil, err
	}
	group.Name, group.ProxyIDs = name, proxyIDs
	if err := s.repo.Update(ctx, group); err != nil {
		return nil, err
	}
	return group, nil
}

func (s *ProxyIPGroupAdminService) Delete(ctx context.Context, id int64) error {
	count, err := s.repo.CountAccounts(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrProxyIPGroupInUse
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return nil
}

func proxyIPGroupAccountTypeError() error {
	return infraerrors.New(http.StatusBadRequest, "PROXY_IP_GROUP_ACCOUNT_TYPE_UNSUPPORTED", "proxy IP groups are only supported for OpenAI OAuth and setup-token accounts")
}
