package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type upstreamAuthSessionRepositoryAdapter struct{ repo *upstreamConfigRepository }

func NewUpstreamAuthSessionRepository(client *dbent.Client) service.UpstreamAuthSessionRepository {
	return &upstreamAuthSessionRepositoryAdapter{repo: &upstreamConfigRepository{client: client}}
}

func (a *upstreamAuthSessionRepositoryAdapter) Get(ctx context.Context, id int64) (*service.UpstreamAuthSessionRecord, error) {
	return a.repo.GetAuthSession(ctx, id)
}
func (a *upstreamAuthSessionRepositoryAdapter) Save(ctx context.Context, record *service.UpstreamAuthSessionRecord) error {
	return a.repo.SaveAuthSession(ctx, record)
}
func (a *upstreamAuthSessionRepositoryAdapter) Delete(ctx context.Context, id int64) error {
	return a.repo.DeleteAuthSession(ctx, id)
}
func (a *upstreamAuthSessionRepositoryAdapter) ClearCooldown(ctx context.Context, id int64) error {
	return a.repo.ClearAuthSessionCooldown(ctx, id)
}
