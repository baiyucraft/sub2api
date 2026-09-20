package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbproxyipgroup "github.com/Wei-Shaw/sub2api/ent/proxyipgroup"
	dbproxyipgroupmember "github.com/Wei-Shaw/sub2api/ent/proxyipgroupmember"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type proxyIPGroupRepository struct {
	client *dbent.Client
}

func NewProxyIPGroupRepository(client *dbent.Client) service.ProxyIPGroupRepository {
	return &proxyIPGroupRepository{client: client}
}

func (r *proxyIPGroupRepository) GetByID(ctx context.Context, id int64) (*service.ProxyIPGroup, error) {
	if id <= 0 {
		return nil, service.ErrProxyIPGroupNotFound
	}

	groups, err := loadProxyIPGroupsByIDs(ctx, r.client, []int64{id})
	if err != nil {
		return nil, err
	}
	group := groups[id]
	if group == nil {
		return nil, service.ErrProxyIPGroupNotFound
	}
	return group, nil
}

func (r *proxyIPGroupRepository) List(ctx context.Context) ([]service.ProxyIPGroup, error) {
	rows, err := r.client.ProxyIPGroup.Query().
		WithMembers(func(q *dbent.ProxyIPGroupMemberQuery) {
			q.Order(dbproxyipgroupmember.ByPosition(), dbproxyipgroupmember.ByProxyID())
		}).
		Order(dbproxyipgroup.ByID()).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.ProxyIPGroup, 0, len(rows))
	for _, row := range rows {
		if group := proxyIPGroupEntityToService(row); group != nil {
			out = append(out, *group)
		}
	}
	return out, nil
}

func createProxyIPGroupMembers(ctx context.Context, client *dbent.Client, groupID int64, proxyIDs []int64) error {
	if len(proxyIDs) == 0 {
		return nil
	}
	builders := make([]*dbent.ProxyIPGroupMemberCreate, 0, len(proxyIDs))
	for position, proxyID := range proxyIDs {
		builders = append(builders, client.ProxyIPGroupMember.Create().
			SetProxyIPGroupID(groupID).
			SetProxyID(proxyID).
			SetPosition(position))
	}
	_, err := client.ProxyIPGroupMember.CreateBulk(builders...).Save(ctx)
	return err
}

func (r *proxyIPGroupRepository) Create(ctx context.Context, group *service.ProxyIPGroup) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	row, err := tx.ProxyIPGroup.Create().
		SetName(group.Name).
		SetPerIPConcurrency(group.PerIPConcurrency).
		Save(ctx)
	if err != nil {
		return rollback(translatePersistenceError(err, nil, service.ErrProxyIPGroupExists))
	}
	if err := createProxyIPGroupMembers(ctx, tx.Client(), row.ID, group.ProxyIDs); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	created, err := r.GetByID(ctx, row.ID)
	if err != nil {
		return err
	}
	*group = *created
	return nil
}

func (r *proxyIPGroupRepository) Update(ctx context.Context, group *service.ProxyIPGroup) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	if _, err := tx.ProxyIPGroup.UpdateOneID(group.ID).
		SetName(group.Name).
		SetPerIPConcurrency(group.PerIPConcurrency).
		Save(ctx); err != nil {
		return rollback(translatePersistenceError(err, service.ErrProxyIPGroupNotFound, service.ErrProxyIPGroupExists))
	}
	if _, err := tx.ProxyIPGroupMember.Delete().
		Where(dbproxyipgroupmember.ProxyIPGroupIDEQ(group.ID)).
		Exec(ctx); err != nil {
		return rollback(err)
	}
	if err := createProxyIPGroupMembers(ctx, tx.Client(), group.ID, group.ProxyIDs); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	updated, err := r.GetByID(ctx, group.ID)
	if err != nil {
		return err
	}
	*group = *updated
	return nil
}

func (r *proxyIPGroupRepository) Delete(ctx context.Context, id int64) error {
	err := r.client.ProxyIPGroup.DeleteOneID(id).Exec(ctx)
	return translatePersistenceError(err, service.ErrProxyIPGroupNotFound, nil)
}

func (r *proxyIPGroupRepository) CountAccounts(ctx context.Context, id int64) (int64, error) {
	count, err := r.client.Account.Query().Where(dbaccount.ProxyIPGroupIDEQ(id)).Count(ctx)
	return int64(count), err
}

func loadProxyIPGroupsByIDs(ctx context.Context, client *dbent.Client, ids []int64) (map[int64]*service.ProxyIPGroup, error) {
	result := make(map[int64]*service.ProxyIPGroup)
	ids = uniquePositiveInt64s(ids)
	if len(ids) == 0 {
		return result, nil
	}

	for start := 0; start < len(ids); start += postgresParameterBatchSize {
		end := start + postgresParameterBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		rows, err := client.ProxyIPGroup.Query().
			Where(dbproxyipgroup.IDIn(ids[start:end]...)).
			WithMembers(func(q *dbent.ProxyIPGroupMemberQuery) {
				q.Order(
					dbproxyipgroupmember.ByPosition(),
					dbproxyipgroupmember.ByProxyID(),
				)
			}).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			result[row.ID] = proxyIPGroupEntityToService(row)
		}
	}
	return result, nil
}

func proxyIPGroupEntityToService(row *dbent.ProxyIPGroup) *service.ProxyIPGroup {
	if row == nil {
		return nil
	}
	proxyIDs := make([]int64, 0, len(row.Edges.Members))
	for _, member := range row.Edges.Members {
		if member != nil && member.ProxyID > 0 {
			proxyIDs = append(proxyIDs, member.ProxyID)
		}
	}
	return &service.ProxyIPGroup{
		ID:               row.ID,
		Name:             row.Name,
		PerIPConcurrency: row.PerIPConcurrency,
		ProxyIDs:         proxyIDs,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}
