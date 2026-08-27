//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestGroupRepositorySetPreferredAccountIDsReplacesAndClearsAtomically(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newGroupRepositoryWithSQL(client, integrationDB)
	suffix := time.Now().UnixNano()

	group, err := client.Group.Create().
		SetName(fmt.Sprintf("preferred-pool-group-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		Save(ctx)
	require.NoError(t, err)
	otherGroup, err := client.Group.Create().
		SetName(fmt.Sprintf("preferred-pool-other-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		Save(ctx)
	require.NoError(t, err)
	boundA := createPreferredPoolTestAccount(t, ctx, fmt.Sprintf("preferred-bound-a-%d", suffix))
	boundB := createPreferredPoolTestAccount(t, ctx, fmt.Sprintf("preferred-bound-b-%d", suffix))
	unbound := createPreferredPoolTestAccount(t, ctx, fmt.Sprintf("preferred-unbound-%d", suffix))

	for idx, accountID := range []int64{boundA, boundB} {
		_, err = client.AccountGroup.Create().
			SetAccountID(accountID).
			SetGroupID(group.ID).
			SetPriority(idx + 1).
			Save(ctx)
		require.NoError(t, err)
	}
	_, err = client.AccountGroup.Create().
		SetAccountID(unbound).
		SetGroupID(otherGroup.ID).
		SetPriority(1).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE group_id = ANY($1)", pq.Array([]int64{group.ID, otherGroup.ID}))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = ANY($1)", pq.Array([]int64{boundA, boundB, unbound}))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = ANY($1)", pq.Array([]int64{boundA, boundB, unbound}))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = ANY($1)", pq.Array([]int64{group.ID, otherGroup.ID}))
	})

	require.NoError(t, repo.SetPreferredAccountIDs(ctx, group.ID, []int64{boundA, boundB, boundA}))
	assertPreferredAccountIDs(t, ctx, group.ID, []int64{boundA, boundB})

	_, err = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	require.NoError(t, err)
	err = repo.SetPreferredAccountIDs(ctx, group.ID, []int64{boundA, unbound})
	require.Error(t, err)
	assertPreferredAccountIDs(t, ctx, group.ID, []int64{boundA, boundB})
	assertSchedulerOutboxCount(t, ctx, group.ID, 0)

	require.NoError(t, repo.SetPreferredAccountIDs(ctx, group.ID, nil))
	assertPreferredAccountIDs(t, ctx, group.ID, nil)
	assertSchedulerOutboxCount(t, ctx, group.ID, 1)
}

func createPreferredPoolTestAccount(t *testing.T, ctx context.Context, name string) int64 {
	t.Helper()
	account, err := integrationEntClient.Account.Create().
		SetName(name).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		Save(ctx)
	require.NoError(t, err)
	return account.ID
}

func assertPreferredAccountIDs(t *testing.T, ctx context.Context, groupID int64, want []int64) {
	t.Helper()
	rows, err := integrationDB.QueryContext(ctx, `
		SELECT account_id
		FROM account_groups
		WHERE group_id = $1 AND scheduler_preferred = TRUE
		ORDER BY account_id`, groupID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	got := make([]int64, 0)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		got = append(got, id)
	}
	require.NoError(t, rows.Err())
	require.ElementsMatch(t, want, got)
}

func assertSchedulerOutboxCount(t *testing.T, ctx context.Context, groupID int64, want int) {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE group_id = $1", groupID).Scan(&count))
	require.Equal(t, want, count)
}
