package repository

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbupstreamconfig "github.com/Wei-Shaw/sub2api/ent/upstreamconfig"
	dbupstreamkey "github.com/Wei-Shaw/sub2api/ent/upstreamkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const upstreamConfigSyncAdvisoryLockNamespace int64 = 0x73756232

type upstreamConfigRepository struct {
	client *dbent.Client
}

const (
	upstreamNameBackfillMissingConfigBinding = "missing_upstream_config_binding"
	upstreamNameBackfillMissingKeyBinding    = "missing_upstream_key_binding"
	upstreamNameBackfillConfigNotFound       = "upstream_config_not_found"
	upstreamNameBackfillConfigDeleted        = "upstream_config_deleted"
	upstreamNameBackfillKeyNotFound          = "upstream_key_not_found"
	upstreamNameBackfillKeyDeleted           = "upstream_key_deleted"
	upstreamNameBackfillKeyConfigMismatch    = "upstream_key_config_mismatch"
	upstreamNameBackfillEmptyName            = "upstream_name_empty"
)

func NewUpstreamConfigRepository(client *dbent.Client) service.UpstreamConfigRepository {
	return &upstreamConfigRepository{client: client}
}

func (r *upstreamConfigRepository) WithUpstreamConfigSyncLock(ctx context.Context, configID int64, fn func(context.Context) error) error {
	driver, ok := r.client.Driver().(*entsql.Driver)
	if !ok || driver.Dialect() != dialect.Postgres {
		return fn(ctx)
	}
	conn, err := driver.DB().Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	lockID := (upstreamConfigSyncAdvisoryLockNamespace << 32) | (configID & 0xffffffff)
	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return infraerrors.New(http.StatusConflict, "UPSTREAM_SYNC_IN_PROGRESS", "upstream config synchronization is already in progress")
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", lockID) }()
	return fn(ctx)
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
		WithKeys().
		Offset(params.Offset()).
		Limit(params.Limit()).
		Order(dbent.Desc(dbupstreamconfig.FieldUpdatedAt)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]service.UpstreamConfig, 0, len(rows))
	for _, row := range rows {
		item := upstreamConfigEntityToService(row)
		for _, key := range row.Edges.Keys {
			item.Keys = append(item.Keys, upstreamKeyEntityToService(key))
		}
		out = append(out, *item)
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
		SetRechargeRate(config.RechargeRate).
		SetStatus(config.Status)
	if config.ProxyID != nil {
		builder.SetProxyID(*config.ProxyID)
	}
	if config.BalanceToCNYRate != nil {
		builder.SetBalanceToCnyRate(*config.BalanceToCNYRate)
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
	return r.withTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		previous, err := client.UpstreamConfig.Query().
			Where(dbupstreamconfig.IDEQ(config.ID)).
			ForUpdate().
			Only(txCtx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return service.ErrUpstreamConfigNotFound
			}
			return err
		}

		builder := client.UpstreamConfig.UpdateOneID(config.ID).
			SetName(config.Name).
			SetProvider(config.Provider).
			SetBaseURL(config.BaseURL).
			SetAuthMode(config.AuthMode).
			SetCredentials(normalizeJSONMap(config.Credentials)).
			SetExtra(normalizeJSONMap(config.Extra)).
			SetRechargeRate(config.RechargeRate).
			SetStatus(config.Status)
		if config.ProxyID != nil {
			builder.SetProxyID(*config.ProxyID)
		} else {
			builder.ClearProxyID()
		}
		if config.BalanceToCNYRate != nil {
			builder.SetBalanceToCnyRate(*config.BalanceToCNYRate)
		} else {
			builder.ClearBalanceToCnyRate()
		}
		updatedConfig, err := builder.Save(txCtx)
		if err != nil {
			return err
		}

		changedIDs, err := renameAccountsForUpstreamConfig(txCtx, client, config.ID, config.Name)
		if err != nil {
			return err
		}
		if previous.RechargeRate != updatedConfig.RechargeRate {
			costChangedIDs, err := recalculateUpstreamAccounts(txCtx, client, updatedConfig, time.Now().UTC())
			if err != nil {
				return err
			}
			changedIDs = append(changedIDs, costChangedIDs...)
			if err := createRechargeRateChangedEvent(txCtx, client, updatedConfig, previous.RechargeRate, updatedConfig.RechargeRate, time.Now().UTC()); err != nil {
				return err
			}
		}
		return enqueueUpstreamAccountChanges(txCtx, client, changedIDs)
	})
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
	return createUpstreamKey(ctx, clientFromContext(ctx, r.client), key)
}

func (r *upstreamConfigRepository) UpsertKey(ctx context.Context, key *service.UpstreamKey) error {
	client := clientFromContext(ctx, r.client)
	existing, err := findUpstreamKeyForUpsert(ctx, client, key)
	if err != nil {
		return err
	}
	if existing == nil {
		return createUpstreamKey(ctx, client, key)
	}
	key.ID = existing.ID
	return updateUpstreamKey(ctx, client, key)
}

func findUpstreamKeyForUpsert(ctx context.Context, client *dbent.Client, key *service.UpstreamKey) (*dbent.UpstreamKey, error) {
	if key.RemoteKeyID != nil {
		existing, err := client.UpstreamKey.Query().Where(
			dbupstreamkey.UpstreamConfigIDEQ(key.UpstreamConfigID),
			dbupstreamkey.RemoteKeyIDEQ(*key.RemoteKeyID),
		).Only(ctx)
		if err == nil {
			return existing, nil
		}
		if !dbent.IsNotFound(err) {
			return nil, err
		}
	}
	if strings.TrimSpace(key.KeyHash) == "" {
		return nil, nil
	}
	existing, err := client.UpstreamKey.Query().Where(
		dbupstreamkey.UpstreamConfigIDEQ(key.UpstreamConfigID),
		dbupstreamkey.KeyHashEQ(key.KeyHash),
	).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	return existing, err
}

func (r *upstreamConfigRepository) UpdateKey(ctx context.Context, key *service.UpstreamKey) error {
	return updateUpstreamKey(ctx, clientFromContext(ctx, r.client), key)
}

func createUpstreamKey(ctx context.Context, client *dbent.Client, key *service.UpstreamKey) error {
	builder := client.UpstreamKey.Create().
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

func updateUpstreamKey(ctx context.Context, client *dbent.Client, key *service.UpstreamKey) error {
	builder := client.UpstreamKey.UpdateOneID(key.ID).
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

func (r *upstreamConfigRepository) ApplySyncSnapshot(ctx context.Context, configID, runID int64, keys []service.UpstreamKey, extraUpdates map[string]any, checkedAt time.Time) ([]service.UpstreamKey, int, error) {
	var (
		localKeys []service.UpstreamKey
		updated   int
	)
	err := r.withTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		config, err := client.UpstreamConfig.Query().
			Where(dbupstreamconfig.IDEQ(configID)).
			ForUpdate().
			Only(txCtx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return service.ErrUpstreamConfigNotFound
			}
			return err
		}

		oldRates := make(map[int64]*float64)
		for i := range keys {
			keys[i].UpstreamConfigID = configID
			existing, queryErr := findUpstreamKeyForUpsert(txCtx, client, &keys[i])
			if queryErr != nil {
				return queryErr
			}
			if existing == nil {
				if err := createUpstreamKey(txCtx, client, &keys[i]); err != nil {
					return err
				}
				continue
			}
			oldRates[existing.ID] = existing.RateMultiplier
			keys[i].ID = existing.ID
			if err := updateUpstreamKey(txCtx, client, &keys[i]); err != nil {
				return err
			}
		}

		keyRows, err := client.UpstreamKey.Query().
			Where(dbupstreamkey.UpstreamConfigIDEQ(configID)).
			Order(dbent.Asc(dbupstreamkey.FieldName), dbent.Asc(dbupstreamkey.FieldID)).
			All(txCtx)
		if err != nil {
			return err
		}
		localKeys = make([]service.UpstreamKey, 0, len(keyRows))
		keyByID := make(map[int64]*dbent.UpstreamKey, len(keyRows))
		for _, row := range keyRows {
			keyByID[row.ID] = row
			localKeys = append(localKeys, *upstreamKeyEntityToService(row))
			if old, ok := oldRates[row.ID]; ok && !sameOptionalRate(old, row.RateMultiplier) {
				if err := createUpstreamRateChangeEvent(txCtx, client, config, row, runID, old, row.RateMultiplier, checkedAt); err != nil {
					return err
				}
			}
		}

		accounts, err := client.Account.Query().
			Where(
				dbaccount.UpstreamConfigIDEQ(configID),
				dbaccount.UpstreamKeyIDNotNil(),
			).
			Order(dbent.Asc(dbaccount.FieldID)).
			ForUpdate().
			All(txCtx)
		if err != nil {
			return err
		}
		changedIDs := make([]int64, 0, len(accounts))
		for _, account := range accounts {
			key := keyByID[*account.UpstreamKeyID]
			if key == nil || key.UpstreamConfigID != configID {
				continue
			}
			changed, err := syncUpstreamAccount(txCtx, client, account, config, key, extraUpdates, checkedAt)
			if err != nil {
				return err
			}
			if changed {
				changedIDs = append(changedIDs, account.ID)
			}
		}

		if err := persistUpstreamBalanceState(txCtx, client, config, runID, extraUpdates, checkedAt); err != nil {
			return err
		}
		mergedExtra := copyJSONMap(config.Extra)
		if mergedExtra == nil {
			mergedExtra = map[string]any{}
		}
		for key, value := range extraUpdates {
			if value == nil {
				delete(mergedExtra, key)
			} else {
				mergedExtra[key] = value
			}
		}
		if config.LastError != nil && strings.TrimSpace(*config.LastError) != "" {
			event := client.UpstreamEvent.Create().SetUpstreamConfigID(configID).SetEventType("sync_recovered").SetSeverity("info").SetSource("sync").SetMessage("Upstream synchronization recovered").SetOccurredAt(checkedAt)
			if runID > 0 {
				event.SetSyncRunID(runID)
			}
			if err := event.Exec(txCtx); err != nil {
				return err
			}
		}
		if _, err := client.UpstreamConfig.UpdateOneID(configID).
			SetExtra(mergedExtra).
			SetLastCheckedAt(checkedAt).
			SetLastSuccessAt(checkedAt).
			ClearLastError().
			Save(txCtx); err != nil {
			return err
		}
		if err := enqueueUpstreamAccountChanges(txCtx, client, changedIDs); err != nil {
			return err
		}
		updated = len(changedIDs)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return localKeys, updated, nil
}

func (r *upstreamConfigRepository) PreviewAccountNameBackfill(ctx context.Context) ([]service.UpstreamAccountNameBackfillItem, error) {
	return loadUpstreamAccountNameBackfill(ctx, r.client, false)
}

func (r *upstreamConfigRepository) ApplyAccountNameBackfill(ctx context.Context) ([]service.UpstreamAccountNameBackfillItem, error) {
	var items []service.UpstreamAccountNameBackfillItem
	err := r.withTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		var err error
		items, err = loadUpstreamAccountNameBackfill(txCtx, client, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *upstreamConfigRepository) RecordCheckResult(ctx context.Context, id int64, success bool, safeErr string) error {
	return r.withTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		now := time.Now().UTC()
		config, err := client.UpstreamConfig.Query().Where(dbupstreamconfig.IDEQ(id)).ForUpdate().Only(txCtx)
		if err != nil {
			return err
		}
		previousFailed := config.LastError != nil && strings.TrimSpace(*config.LastError) != ""
		builder := client.UpstreamConfig.UpdateOneID(id).SetLastCheckedAt(now)
		if success {
			builder.SetLastSuccessAt(now).ClearLastError()
		} else {
			builder.SetLastError(safeErr)
		}
		if _, err := builder.Save(txCtx); err != nil {
			return err
		}
		if !success && !previousFailed {
			return client.UpstreamEvent.Create().SetUpstreamConfigID(id).SetEventType("sync_failed").SetSeverity("error").SetSource("sync").SetMessage("Upstream synchronization failed").SetPayload(map[string]any{"error": safeErr}).SetOccurredAt(now).Exec(txCtx)
		}
		if success && previousFailed {
			return client.UpstreamEvent.Create().SetUpstreamConfigID(id).SetEventType("sync_recovered").SetSeverity("info").SetSource("sync").SetMessage("Upstream synchronization recovered").SetOccurredAt(now).Exec(txCtx)
		}
		return nil
	})
}

func (r *upstreamConfigRepository) SaveRefreshedTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiresAt *time.Time) error {
	updates := map[string]any{
		service.AccountCredentialSub2APIAccessToken:  accessToken,
		service.AccountCredentialSub2APIRefreshToken: refreshToken,
	}
	query := "UPDATE upstream_configs SET credentials = (COALESCE(credentials, '{}'::jsonb) || $1::jsonb) - $3, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL"
	removeKey := service.AccountCredentialSub2APITokenExpiresAt
	if expiresAt != nil {
		updates[service.AccountCredentialSub2APITokenExpiresAt] = expiresAt.UTC().Format(time.RFC3339)
		query = "UPDATE upstream_configs SET credentials = COALESCE(credentials, '{}'::jsonb) || $1::jsonb, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL"
		removeKey = ""
	}
	payload, err := json.Marshal(updates)
	if err != nil {
		return err
	}
	client := clientFromContext(ctx, r.client)
	var result interface {
		RowsAffected() (int64, error)
	}
	if expiresAt != nil {
		result, err = client.ExecContext(ctx, query, string(payload), id)
	} else {
		result, err = client.ExecContext(ctx, query, string(payload), id, removeKey)
	}
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

func (r *upstreamConfigRepository) withTx(ctx context.Context, fn func(context.Context, *dbent.Client) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return fn(ctx, tx.Client())
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		if errors.Is(err, dbent.ErrTxStarted) {
			return fn(ctx, r.client)
		}
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx, tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}

func renameAccountsForUpstreamConfig(ctx context.Context, client *dbent.Client, configID int64, configName string) ([]int64, error) {
	keys, err := client.UpstreamKey.Query().
		Where(dbupstreamkey.UpstreamConfigIDEQ(configID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	keyByID := make(map[int64]*dbent.UpstreamKey, len(keys))
	for _, key := range keys {
		keyByID[key.ID] = key
	}
	accounts, err := client.Account.Query().
		Where(
			dbaccount.UpstreamConfigIDEQ(configID),
			dbaccount.UpstreamKeyIDNotNil(),
		).
		Order(dbent.Asc(dbaccount.FieldID)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, err
	}
	changedIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		key := keyByID[*account.UpstreamKeyID]
		if key == nil || key.UpstreamConfigID != configID {
			continue
		}
		name, err := service.BuildUpstreamAccountName(configName, key.Name)
		if err != nil || name == account.Name {
			continue
		}
		if _, err := client.Account.UpdateOneID(account.ID).SetName(name).Save(ctx); err != nil {
			return nil, err
		}
		changedIDs = append(changedIDs, account.ID)
	}
	return changedIDs, nil
}

func syncUpstreamAccount(ctx context.Context, client *dbent.Client, account *dbent.Account, config *dbent.UpstreamConfig, key *dbent.UpstreamKey, balanceExtra map[string]any, checkedAt time.Time) (bool, error) {
	builder := client.Account.UpdateOneID(account.ID)
	changed := false
	if name, err := service.BuildUpstreamAccountName(config.Name, key.Name); err == nil && name != account.Name {
		builder.SetName(name)
		changed = true
	}
	if key.RateMultiplier != nil && validUpstreamRateMultiplier(*key.RateMultiplier) {
		rechargeRate := config.RechargeRate
		if rechargeRate <= 0 {
			rechargeRate = 1
		}
		multiplier := *key.RateMultiplier * rechargeRate
		priority := service.Sub2APIUpstreamPriority(multiplier)
		loadFactor := service.AutoUpstreamLoadFactor(priority, account.Concurrency)
		extra := copyJSONMap(account.Extra)
		if extra == nil {
			extra = map[string]any{}
		}
		for extraKey, value := range upstreamRateSyncExtra(config, key, balanceExtra, checkedAt) {
			extra[extraKey] = value
		}
		builder.
			SetRateMultiplier(multiplier).
			SetPriority(priority).
			SetLoadFactor(loadFactor).
			SetExtra(extra)
		changed = true
	}
	if !changed {
		return false, nil
	}
	if _, err := builder.Save(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func validUpstreamRateMultiplier(multiplier float64) bool {
	return multiplier >= 0 && !math.IsNaN(multiplier) && !math.IsInf(multiplier, 0)
}

func upstreamRateSyncExtra(config *dbent.UpstreamConfig, key *dbent.UpstreamKey, balanceExtra map[string]any, checkedAt time.Time) map[string]any {
	checkedAtText := checkedAt.UTC().Format(time.RFC3339)
	extra := map[string]any{
		"upstream_rate_sync_last_success_at": checkedAtText,
		"upstream_rate_sync_last_error":      "",
		"upstream_provider":                  config.Provider,
		"upstream_platform":                  key.Platform,
		"upstream_rate_multiplier":           *key.RateMultiplier,
		"upstream_recharge_rate":             config.RechargeRate,
		"upstream_effective_cost_multiplier": *key.RateMultiplier * config.RechargeRate,
	}
	if key.UpstreamGroupID != nil {
		extra["upstream_group_id"] = *key.UpstreamGroupID
	}
	if strings.TrimSpace(key.UpstreamGroupName) != "" {
		extra["upstream_group_name"] = key.UpstreamGroupName
	}
	if rate, ok := numberFromMap(balanceExtra, "currency_to_cny_rate"); ok && rate > 0 {
		extra["upstream_cost_currency"] = "CNY"
		extra["upstream_cost_to_cny_rate"] = rate
	}
	if config.Provider == service.UpstreamProviderSub2API {
		extra["sub2api_rate_sync_last_success_at"] = checkedAtText
		extra["sub2api_rate_sync_last_error"] = ""
		extra["sub2api_upstream_platform"] = key.Platform
		extra["sub2api_upstream_rate_multiplier"] = *key.RateMultiplier
		if key.UpstreamGroupID != nil {
			extra["sub2api_upstream_group_id"] = *key.UpstreamGroupID
		}
		if strings.TrimSpace(key.UpstreamGroupName) != "" {
			extra["sub2api_upstream_group_name"] = key.UpstreamGroupName
		}
	}
	return extra
}

func enqueueUpstreamAccountChanges(ctx context.Context, exec sqlExecutor, accountIDs []int64) error {
	if len(accountIDs) == 0 {
		return nil
	}
	accountIDs = uniqueSortedInt64s(accountIDs)
	return enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventAccountBulkChanged, nil, nil, map[string]any{
		"account_ids": accountIDs,
	})
}

func loadUpstreamAccountNameBackfill(ctx context.Context, client *dbent.Client, apply bool) ([]service.UpstreamAccountNameBackfillItem, error) {
	accounts, err := queryUpstreamBoundAccounts(ctx, client, nil, false)
	if err != nil || len(accounts) == 0 {
		return nil, err
	}
	if apply {
		if _, _, err := loadUpstreamBindingReferences(ctx, client, accounts, true); err != nil {
			return nil, err
		}
		accountIDs := make([]int64, 0, len(accounts))
		for _, account := range accounts {
			accountIDs = append(accountIDs, account.ID)
		}
		accounts, err = queryUpstreamBoundAccounts(ctx, client, accountIDs, true)
		if err != nil || len(accounts) == 0 {
			return nil, err
		}
	}
	configs, keys, err := loadUpstreamBindingReferences(ctx, client, accounts, apply)
	if err != nil {
		return nil, err
	}
	items := make([]service.UpstreamAccountNameBackfillItem, 0, len(accounts))
	changedIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		item := upstreamAccountNameBackfillItem(account, configs, keys)
		items = append(items, item)
		if !apply || item.SkipReason != "" || item.OldName == item.NewName {
			continue
		}
		if _, err := client.Account.UpdateOneID(account.ID).SetName(item.NewName).Save(ctx); err != nil {
			return nil, err
		}
		changedIDs = append(changedIDs, account.ID)
	}
	if apply {
		if err := enqueueUpstreamAccountChanges(ctx, client, changedIDs); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func queryUpstreamBoundAccounts(ctx context.Context, client *dbent.Client, accountIDs []int64, lock bool) ([]*dbent.Account, error) {
	query := client.Account.Query().Where(dbaccount.Or(
		dbaccount.UpstreamConfigIDNotNil(),
		dbaccount.UpstreamKeyIDNotNil(),
	))
	if accountIDs != nil {
		if len(accountIDs) == 0 {
			return nil, nil
		}
		query = query.Where(dbaccount.IDIn(accountIDs...))
	}
	query = query.Order(dbent.Asc(dbaccount.FieldID))
	if lock {
		query = query.ForUpdate()
	}
	return query.Select(
		dbaccount.FieldID,
		dbaccount.FieldName,
		dbaccount.FieldUpstreamConfigID,
		dbaccount.FieldUpstreamKeyID,
	).All(ctx)
}

func loadUpstreamBindingReferences(ctx context.Context, client *dbent.Client, accounts []*dbent.Account, lock bool) (map[int64]*dbent.UpstreamConfig, map[int64]*dbent.UpstreamKey, error) {
	configIDs := make([]int64, 0, len(accounts))
	keyIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account.UpstreamConfigID != nil {
			configIDs = append(configIDs, *account.UpstreamConfigID)
		}
		if account.UpstreamKeyID != nil {
			keyIDs = append(keyIDs, *account.UpstreamKeyID)
		}
	}
	configIDs = uniqueSortedInt64s(configIDs)
	keyIDs = uniqueSortedInt64s(keyIDs)
	configs := make(map[int64]*dbent.UpstreamConfig, len(configIDs))
	keys := make(map[int64]*dbent.UpstreamKey, len(keyIDs))
	queryCtx := mixins.SkipSoftDelete(ctx)
	if len(configIDs) > 0 {
		query := client.UpstreamConfig.Query().
			Where(dbupstreamconfig.IDIn(configIDs...)).
			Order(dbent.Asc(dbupstreamconfig.FieldID))
		if lock {
			query = query.ForUpdate()
		}
		rows, err := query.Select(
			dbupstreamconfig.FieldID,
			dbupstreamconfig.FieldName,
			dbupstreamconfig.FieldDeletedAt,
		).All(queryCtx)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			configs[row.ID] = row
		}
	}
	if len(keyIDs) > 0 {
		query := client.UpstreamKey.Query().
			Where(dbupstreamkey.IDIn(keyIDs...)).
			Order(dbent.Asc(dbupstreamkey.FieldID))
		if lock {
			query = query.ForUpdate()
		}
		rows, err := query.Select(
			dbupstreamkey.FieldID,
			dbupstreamkey.FieldUpstreamConfigID,
			dbupstreamkey.FieldName,
			dbupstreamkey.FieldDeletedAt,
		).All(queryCtx)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			keys[row.ID] = row
		}
	}
	return configs, keys, nil
}

func upstreamAccountNameBackfillItem(account *dbent.Account, configs map[int64]*dbent.UpstreamConfig, keys map[int64]*dbent.UpstreamKey) service.UpstreamAccountNameBackfillItem {
	item := service.UpstreamAccountNameBackfillItem{
		AccountID:        account.ID,
		OldName:          account.Name,
		UpstreamConfigID: account.UpstreamConfigID,
		UpstreamKeyID:    account.UpstreamKeyID,
	}
	if account.UpstreamConfigID == nil {
		item.SkipReason = upstreamNameBackfillMissingConfigBinding
		return item
	}
	if account.UpstreamKeyID == nil {
		item.SkipReason = upstreamNameBackfillMissingKeyBinding
		return item
	}
	config := configs[*account.UpstreamConfigID]
	if config == nil {
		item.SkipReason = upstreamNameBackfillConfigNotFound
		return item
	}
	if config.DeletedAt != nil {
		item.SkipReason = upstreamNameBackfillConfigDeleted
		return item
	}
	key := keys[*account.UpstreamKeyID]
	if key == nil {
		item.SkipReason = upstreamNameBackfillKeyNotFound
		return item
	}
	if key.DeletedAt != nil {
		item.SkipReason = upstreamNameBackfillKeyDeleted
		return item
	}
	if key.UpstreamConfigID != config.ID {
		item.SkipReason = upstreamNameBackfillKeyConfigMismatch
		return item
	}
	name, err := service.BuildUpstreamAccountName(config.Name, key.Name)
	if err != nil {
		item.SkipReason = upstreamNameBackfillEmptyName
		return item
	}
	item.NewName = name
	return item
}

func uniqueSortedInt64s(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func upstreamConfigEntityToService(row *dbent.UpstreamConfig) *service.UpstreamConfig {
	if row == nil {
		return nil
	}
	return &service.UpstreamConfig{
		ID:               row.ID,
		Name:             row.Name,
		Provider:         row.Provider,
		BaseURL:          row.BaseURL,
		AuthMode:         row.AuthMode,
		Credentials:      copyJSONMap(row.Credentials),
		Extra:            copyJSONMap(row.Extra),
		ProxyID:          row.ProxyID,
		RechargeRate:     row.RechargeRate,
		BalanceToCNYRate: row.BalanceToCnyRate,
		Status:           row.Status,
		LastError:        row.LastError,
		LastCheckedAt:    row.LastCheckedAt,
		LastSuccessAt:    row.LastSuccessAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
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
