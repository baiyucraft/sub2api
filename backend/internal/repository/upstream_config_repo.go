package repository

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbupstreamconfig "github.com/Wei-Shaw/sub2api/ent/upstreamconfig"
	dbupstreamkey "github.com/Wei-Shaw/sub2api/ent/upstreamkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type upstreamConfigRepository struct {
	client *dbent.Client
}

func NewUpstreamConfigRepository(client *dbent.Client) service.UpstreamConfigRepository {
	return &upstreamConfigRepository{client: client}
}

func (r *upstreamConfigRepository) List(ctx context.Context, params pagination.PaginationParams, provider, status, search string) ([]service.UpstreamConfig, *pagination.PaginationResult, error) {
	q := r.client.UpstreamConfig.Query()
	if provider = strings.TrimSpace(provider); provider != "" {
		q = q.Where(dbupstreamconfig.ProviderEQ(provider))
	}
	if status = strings.TrimSpace(status); status != "" {
		q = q.Where(dbupstreamconfig.StatusEQ(status))
	}
	if search = strings.TrimSpace(search); search != "" {
		q = q.Where(dbupstreamconfig.Or(
			dbupstreamconfig.NameContainsFold(search),
			dbupstreamconfig.BaseURLContainsFold(search),
		))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.
		Offset(params.Offset()).
		Limit(params.Limit()).
		Order(dbent.Desc(dbupstreamconfig.FieldUpdatedAt)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]service.UpstreamConfig, 0, len(rows))
	for _, row := range rows {
		out = append(out, *upstreamConfigEntityToService(row))
	}
	return out, paginationResultFromTotal(int64(total), params), nil
}

func (r *upstreamConfigRepository) GetByID(ctx context.Context, id int64) (*service.UpstreamConfig, error) {
	row, err := r.client.UpstreamConfig.Query().
		Where(dbupstreamconfig.IDEQ(id)).
		WithKeys().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrUpstreamConfigNotFound
		}
		return nil, err
	}
	out := upstreamConfigEntityToService(row)
	for _, key := range row.Edges.Keys {
		out.Keys = append(out.Keys, upstreamKeyEntityToService(key))
	}
	return out, nil
}

func (r *upstreamConfigRepository) Create(ctx context.Context, config *service.UpstreamConfig) error {
	builder := r.client.UpstreamConfig.Create().
		SetName(config.Name).
		SetProvider(config.Provider).
		SetBaseURL(config.BaseURL).
		SetAuthMode(config.AuthMode).
		SetCredentials(normalizeJSONMap(config.Credentials)).
		SetExtra(normalizeJSONMap(config.Extra)).
		SetStatus(config.Status)
	if config.ProxyID != nil {
		builder.SetProxyID(*config.ProxyID)
	}
	row, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	config.ID = row.ID
	config.CreatedAt = row.CreatedAt
	config.UpdatedAt = row.UpdatedAt
	return nil
}

func (r *upstreamConfigRepository) Update(ctx context.Context, config *service.UpstreamConfig) error {
	builder := r.client.UpstreamConfig.UpdateOneID(config.ID).
		SetName(config.Name).
		SetProvider(config.Provider).
		SetBaseURL(config.BaseURL).
		SetAuthMode(config.AuthMode).
		SetCredentials(normalizeJSONMap(config.Credentials)).
		SetExtra(normalizeJSONMap(config.Extra)).
		SetStatus(config.Status)
	if config.ProxyID != nil {
		builder.SetProxyID(*config.ProxyID)
	} else {
		builder.ClearProxyID()
	}
	_, err := builder.Save(ctx)
	return err
}

func (r *upstreamConfigRepository) Delete(ctx context.Context, id int64) error {
	return r.client.UpstreamConfig.DeleteOneID(id).Exec(ctx)
}

func (r *upstreamConfigRepository) CountAccounts(ctx context.Context, id int64) (int64, error) {
	count, err := r.client.Account.Query().Where(dbaccount.UpstreamConfigIDEQ(id)).Count(ctx)
	return int64(count), err
}

func (r *upstreamConfigRepository) ListKeys(ctx context.Context, upstreamConfigID int64) ([]service.UpstreamKey, error) {
	rows, err := r.client.UpstreamKey.Query().
		Where(dbupstreamkey.UpstreamConfigIDEQ(upstreamConfigID)).
		Order(dbent.Asc(dbupstreamkey.FieldName), dbent.Asc(dbupstreamkey.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.UpstreamKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, *upstreamKeyEntityToService(row))
	}
	return out, nil
}

func (r *upstreamConfigRepository) GetKeyByID(ctx context.Context, id int64) (*service.UpstreamKey, error) {
	row, err := r.client.UpstreamKey.Query().Where(dbupstreamkey.IDEQ(id)).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrUpstreamKeyNotFound
		}
		return nil, err
	}
	return upstreamKeyEntityToService(row), nil
}

func (r *upstreamConfigRepository) CreateKey(ctx context.Context, key *service.UpstreamKey) error {
	builder := r.client.UpstreamKey.Create().
		SetUpstreamConfigID(key.UpstreamConfigID).
		SetName(key.Name).
		SetKey(key.Key).
		SetKeyHash(key.KeyHash).
		SetUpstreamGroupName(key.UpstreamGroupName).
		SetPlatform(key.Platform).
		SetStatus(key.Status).
		SetExtra(normalizeJSONMap(key.Extra))
	if key.RemoteKeyID != nil {
		builder.SetRemoteKeyID(*key.RemoteKeyID)
	}
	if key.UpstreamGroupID != nil {
		builder.SetUpstreamGroupID(*key.UpstreamGroupID)
	}
	if key.RateMultiplier != nil {
		builder.SetRateMultiplier(*key.RateMultiplier)
	}
	if key.LastSeenAt != nil {
		builder.SetLastSeenAt(*key.LastSeenAt)
	}
	row, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	key.ID = row.ID
	key.CreatedAt = row.CreatedAt
	key.UpdatedAt = row.UpdatedAt
	return nil
}

func (r *upstreamConfigRepository) UpsertKey(ctx context.Context, key *service.UpstreamKey) error {
	existing, err := r.client.UpstreamKey.Query().
		Where(
			dbupstreamkey.UpstreamConfigIDEQ(key.UpstreamConfigID),
			dbupstreamkey.KeyHashEQ(key.KeyHash),
		).
		Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return err
	}
	if existing == nil {
		return r.CreateKey(ctx, key)
	}
	key.ID = existing.ID
	return r.UpdateKey(ctx, key)
}

func (r *upstreamConfigRepository) UpdateKey(ctx context.Context, key *service.UpstreamKey) error {
	builder := r.client.UpstreamKey.UpdateOneID(key.ID).
		SetUpstreamConfigID(key.UpstreamConfigID).
		SetName(key.Name).
		SetKey(key.Key).
		SetKeyHash(key.KeyHash).
		SetUpstreamGroupName(key.UpstreamGroupName).
		SetPlatform(key.Platform).
		SetStatus(key.Status).
		SetExtra(normalizeJSONMap(key.Extra))
	if key.RemoteKeyID != nil {
		builder.SetRemoteKeyID(*key.RemoteKeyID)
	} else {
		builder.ClearRemoteKeyID()
	}
	if key.UpstreamGroupID != nil {
		builder.SetUpstreamGroupID(*key.UpstreamGroupID)
	} else {
		builder.ClearUpstreamGroupID()
	}
	if key.RateMultiplier != nil {
		builder.SetRateMultiplier(*key.RateMultiplier)
	} else {
		builder.ClearRateMultiplier()
	}
	if key.LastSeenAt != nil {
		builder.SetLastSeenAt(*key.LastSeenAt)
	} else {
		builder.ClearLastSeenAt()
	}
	_, err := builder.Save(ctx)
	return err
}

func (r *upstreamConfigRepository) DeleteKey(ctx context.Context, id int64) error {
	count, err := r.client.Account.Query().Where(dbaccount.UpstreamKeyIDEQ(id)).Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return infraerrors.New(http.StatusBadRequest, "UPSTREAM_KEY_IN_USE", "upstream key is used by accounts")
	}
	return r.client.UpstreamKey.DeleteOneID(id).Exec(ctx)
}

func (r *upstreamConfigRepository) RecordCheckResult(ctx context.Context, id int64, success bool, safeErr string) error {
	now := time.Now()
	builder := r.client.UpstreamConfig.UpdateOneID(id).
		SetLastCheckedAt(now)
	if success {
		builder.SetLastSuccessAt(now).ClearLastError()
	} else {
		builder.SetLastError(safeErr)
	}
	_, err := builder.Save(ctx)
	return err
}

func (r *upstreamConfigRepository) SaveRefreshedTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiresAt *time.Time) error {
	cfg, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	credentials := normalizeJSONMap(cfg.Credentials)
	credentials[service.AccountCredentialSub2APIAccessToken] = accessToken
	credentials[service.AccountCredentialSub2APIRefreshToken] = refreshToken
	if expiresAt != nil {
		credentials[service.AccountCredentialSub2APITokenExpiresAt] = expiresAt.UTC().Format(time.RFC3339)
	}
	_, err = r.client.UpstreamConfig.UpdateOneID(id).
		SetCredentials(credentials).
		Save(ctx)
	return err
}

func (r *upstreamConfigRepository) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	payload, err := json.Marshal(updates)
	if err != nil {
		return err
	}
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(
		ctx,
		"UPDATE upstream_configs SET extra = COALESCE(extra, '{}'::jsonb) || $1::jsonb, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL",
		string(payload), id,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrUpstreamConfigNotFound
	}
	return nil
}

func upstreamConfigEntityToService(row *dbent.UpstreamConfig) *service.UpstreamConfig {
	if row == nil {
		return nil
	}
	return &service.UpstreamConfig{
		ID:            row.ID,
		Name:          row.Name,
		Provider:      row.Provider,
		BaseURL:       row.BaseURL,
		AuthMode:      row.AuthMode,
		Credentials:   copyJSONMap(row.Credentials),
		Extra:         copyJSONMap(row.Extra),
		ProxyID:       row.ProxyID,
		Status:        row.Status,
		LastError:     row.LastError,
		LastCheckedAt: row.LastCheckedAt,
		LastSuccessAt: row.LastSuccessAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

func upstreamKeyEntityToService(row *dbent.UpstreamKey) *service.UpstreamKey {
	if row == nil {
		return nil
	}
	return &service.UpstreamKey{
		ID:                row.ID,
		UpstreamConfigID:  row.UpstreamConfigID,
		Name:              row.Name,
		Key:               row.Key,
		KeyHash:           row.KeyHash,
		RemoteKeyID:       row.RemoteKeyID,
		UpstreamGroupID:   row.UpstreamGroupID,
		UpstreamGroupName: row.UpstreamGroupName,
		Platform:          row.Platform,
		RateMultiplier:    row.RateMultiplier,
		Status:            row.Status,
		LastSeenAt:        row.LastSeenAt,
		Extra:             copyJSONMap(row.Extra),
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}
