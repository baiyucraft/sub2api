package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type proxyIPGroupRepoStub struct {
	groups       map[int64]ProxyIPGroup
	nextID       int64
	accountCount map[int64]int64
	deleted      []int64
}

func newProxyIPGroupRepoStub() *proxyIPGroupRepoStub {
	return &proxyIPGroupRepoStub{
		groups:       make(map[int64]ProxyIPGroup),
		nextID:       10,
		accountCount: make(map[int64]int64),
	}
}

func (r *proxyIPGroupRepoStub) GetByID(_ context.Context, id int64) (*ProxyIPGroup, error) {
	group, ok := r.groups[id]
	if !ok {
		return nil, ErrProxyIPGroupNotFound
	}
	clone := group
	clone.ProxyIDs = append([]int64(nil), group.ProxyIDs...)
	return &clone, nil
}

func (r *proxyIPGroupRepoStub) List(context.Context) ([]ProxyIPGroup, error) {
	out := make([]ProxyIPGroup, 0, len(r.groups))
	for _, group := range r.groups {
		out = append(out, group)
	}
	return out, nil
}

func (r *proxyIPGroupRepoStub) Create(_ context.Context, group *ProxyIPGroup) error {
	r.nextID++
	group.ID = r.nextID
	r.groups[group.ID] = *group
	return nil
}

func (r *proxyIPGroupRepoStub) Update(_ context.Context, group *ProxyIPGroup) error {
	if _, ok := r.groups[group.ID]; !ok {
		return ErrProxyIPGroupNotFound
	}
	r.groups[group.ID] = *group
	return nil
}

func (r *proxyIPGroupRepoStub) Delete(_ context.Context, id int64) error {
	if _, ok := r.groups[id]; !ok {
		return ErrProxyIPGroupNotFound
	}
	delete(r.groups, id)
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *proxyIPGroupRepoStub) CountAccounts(_ context.Context, id int64) (int64, error) {
	return r.accountCount[id], nil
}

type proxyIPGroupProxyRepoStub struct {
	ProxyRepository
	proxies map[int64]Proxy
}

func (r *proxyIPGroupProxyRepoStub) ListByIDs(_ context.Context, ids []int64) ([]Proxy, error) {
	out := make([]Proxy, 0, len(ids))
	for _, id := range ids {
		if proxy, ok := r.proxies[id]; ok {
			out = append(out, proxy)
		}
	}
	return out, nil
}

func TestProxyIPGroupAdminServiceCreateNormalizesMembers(t *testing.T) {
	repo := newProxyIPGroupRepoStub()
	proxyRepo := &proxyIPGroupProxyRepoStub{proxies: map[int64]Proxy{
		1: {ID: 1},
		2: {ID: 2},
	}}
	svc := NewProxyIPGroupAdminService(repo, proxyRepo)

	group, err := svc.Create(context.Background(), CreateProxyIPGroupInput{
		Name:             "  primary exits  ",
		PerIPConcurrency: 10,
		ProxyIDs:         []int64{2, 1, 2},
	})

	require.NoError(t, err)
	require.Equal(t, "primary exits", group.Name)
	require.Equal(t, []int64{2, 1}, group.ProxyIDs)
	require.Equal(t, 10, group.PerIPConcurrency)
	require.NotZero(t, group.ID)
}

func TestProxyIPGroupAdminServiceRejectsInvalidConcurrencyAndMissingMembers(t *testing.T) {
	repo := newProxyIPGroupRepoStub()
	proxyRepo := &proxyIPGroupProxyRepoStub{proxies: map[int64]Proxy{1: {ID: 1}}}
	svc := NewProxyIPGroupAdminService(repo, proxyRepo)

	_, err := svc.Create(context.Background(), CreateProxyIPGroupInput{Name: "bad", PerIPConcurrency: 0})
	require.Equal(t, "PROXY_IP_GROUP_CONCURRENCY_INVALID", infraerrors.Reason(err))

	_, err = svc.Create(context.Background(), CreateProxyIPGroupInput{
		Name:             "missing",
		PerIPConcurrency: 10,
		ProxyIDs:         []int64{1, 2},
	})
	require.Equal(t, "PROXY_IP_GROUP_MEMBER_INVALID", infraerrors.Reason(err))
}

func TestProxyIPGroupAdminServiceUpdateAndDeleteGuards(t *testing.T) {
	repo := newProxyIPGroupRepoStub()
	repo.groups[7] = ProxyIPGroup{ID: 7, Name: "old", PerIPConcurrency: 10, ProxyIDs: []int64{1}}
	proxyRepo := &proxyIPGroupProxyRepoStub{proxies: map[int64]Proxy{
		1: {ID: 1},
		2: {ID: 2},
	}}
	svc := NewProxyIPGroupAdminService(repo, proxyRepo)
	name := "new"
	limit := 25
	members := []int64{2, 1, 2}

	updated, err := svc.Update(context.Background(), 7, UpdateProxyIPGroupInput{
		Name:             &name,
		PerIPConcurrency: &limit,
		ProxyIDs:         &members,
	})
	require.NoError(t, err)
	require.Equal(t, "new", updated.Name)
	require.Equal(t, 25, updated.PerIPConcurrency)
	require.Equal(t, []int64{2, 1}, updated.ProxyIDs)

	repo.accountCount[7] = 1
	require.ErrorIs(t, svc.Delete(context.Background(), 7), ErrProxyIPGroupInUse)
	require.Empty(t, repo.deleted)

	repo.accountCount[7] = 0
	require.NoError(t, svc.Delete(context.Background(), 7))
	require.Equal(t, []int64{7}, repo.deleted)
}
