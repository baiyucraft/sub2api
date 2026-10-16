package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestAccountListOwnedSnapshotPreservesTypesAndOwnsRelations(t *testing.T) {
	instant := time.Now()
	group := &Group{ID: 9, ModelRouting: map[string][]int64{"model": {1, 2}}, VideoModelPrices: map[string]map[string]float64{"video": {"720p": 2}}}
	account := Account{ID: 1, Credentials: map[string]any{"number": int64(7), "nested": map[string]any{"values": []any{"original"}}}, Extra: map[string]any{"instant": instant}, Groups: []*Group{group}, ProxyIPGroup: &ProxyIPGroup{ProxyIDs: []int64{3}}, modelMappingCacheReady: true, modelMappingCache: map[string]string{"a": "b"}}
	group.AccountGroups = []AccountGroup{{Group: group}}
	original := []Account{account}
	original[0].Groups[0].AccountGroups[0].Account = &original[0]
	cloned := cloneAccountListSnapshot(original)
	require.IsType(t, int64(0), cloned[0].Credentials["number"])
	require.Equal(t, instant, cloned[0].Extra["instant"])
	require.False(t, cloned[0].modelMappingCacheReady)
	require.Nil(t, cloned[0].modelMappingCache)
	require.Same(t, cloned[0].Groups[0], cloned[0].Groups[0].AccountGroups[0].Group)
	require.Same(t, &cloned[0], cloned[0].Groups[0].AccountGroups[0].Account)
	cloned[0].Credentials["nested"].(map[string]any)["values"].([]any)[0] = "changed"
	cloned[0].Groups[0].ModelRouting["model"][0] = 99
	cloned[0].Groups[0].VideoModelPrices["video"]["720p"] = 9
	cloned[0].ProxyIPGroup.ProxyIDs[0] = 99
	require.Equal(t, "original", original[0].Credentials["nested"].(map[string]any)["values"].([]any)[0])
	require.Equal(t, int64(1), original[0].Groups[0].ModelRouting["model"][0])
	require.Equal(t, float64(2), original[0].Groups[0].VideoModelPrices["video"]["720p"])
	require.Equal(t, int64(3), original[0].ProxyIPGroup.ProxyIDs[0])
}

type sharedAccountListRepository struct {
	AccountRepository
	calls            atomic.Int32
	started, release chan struct{}
}

func (r *sharedAccountListRepository) ListAllWithFiltersScoped(context.Context, string, string, string, string, int64, string, AccountListScope) ([]Account, error) {
	return nil, nil
}

func (r *sharedAccountListRepository) ListWithFiltersScoped(ctx context.Context, _ pagination.PaginationParams, _, _, _, _ string, _ int64, _ string, _ AccountListScope) ([]Account, *pagination.PaginationResult, error) {
	if r.calls.Add(1) == 1 {
		close(r.started)
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return []Account{{ID: 7, Credentials: map[string]any{"nested": map[string]any{"value": "original"}}, GroupIDs: []int64{2}}}, &pagination.PaginationResult{Total: 1}, nil
}
func TestAccountListCoalescesRawReadAndReturnsOwnedRows(t *testing.T) {
	repo := &sharedAccountListRepository{started: make(chan struct{}), release: make(chan struct{})}
	svc := &adminServiceImpl{accountRepo: repo}
	base := WithAccountListReadAuthorization(context.Background(), 17, "admin")
	firstCtx, cancel := context.WithCancel(base)
	type result struct {
		rows  []Account
		total int64
		err   error
	}
	list := func(ctx context.Context) result {
		rows, total, err := svc.ListAccountsScoped(ctx, 1, 20, "openai", "apikey", "active", "", 0, "", "name", "asc", AccountListScopeUpstream)
		return result{rows, total, err}
	}
	first := make(chan result, 1)
	go func() { first <- list(firstCtx) }()
	<-repo.started
	joined := make(chan struct{})
	second := make(chan result, 1)
	go func() { second <- list(&accountListJoinedContext{Context: base, joined: joined}) }()
	<-joined
	cancel()
	require.ErrorIs(t, (<-first).err, context.Canceled)
	close(repo.release)
	response := <-second
	require.NoError(t, response.err)
	require.Equal(t, int64(1), response.total)
	require.Equal(t, int64(7), response.rows[0].ID)
	require.Equal(t, int32(1), repo.calls.Load())
	response.rows[0].Credentials["nested"].(map[string]any)["value"] = "edited"
	third := list(base)
	require.NoError(t, third.err)
	require.Equal(t, int32(2), repo.calls.Load())
	require.Equal(t, "original", third.rows[0].Credentials["nested"].(map[string]any)["value"])
}

func BenchmarkAccountListOwnedSnapshot(b *testing.B) {
	for _, size := range []int{20, 100} {
		rows := make([]Account, size)
		for i := range rows {
			rows[i] = Account{ID: int64(i + 1), Name: "account", Platform: PlatformOpenAI, Credentials: map[string]any{"api_key": "benchmark-only", "model_mapping": map[string]any{"gpt": "gpt"}}, Extra: map[string]any{"enabled": true}, Groups: []*Group{{ID: 1, Name: "group"}}, GroupIDs: []int64{1}}
		}
		b.Run(fmt.Sprintf("page%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = cloneAccountListSnapshot(rows)
			}
		})
	}
}
