package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbupstreamconfig "github.com/Wei-Shaw/sub2api/ent/upstreamconfig"
	dbupstreamevent "github.com/Wei-Shaw/sub2api/ent/upstreamevent"
	dbupstreamkey "github.com/Wei-Shaw/sub2api/ent/upstreamkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const upstreamConfigSyncAdvisoryLockNamespace int64 = 0x73756232

const (
	upstreamKeyMissingThreshold = 3
	upstreamKeyMissingGrace     = 30 * time.Minute
)

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
			dbupstreamconfig.SiteURLContainsFold(search),
			dbupstreamconfig.APIURLContainsFold(search),
		))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.
		WithKeys(func(q *dbent.UpstreamKeyQuery) {
			q.WithAccounts(func(aq *dbent.AccountQuery) { aq.Select(dbaccount.FieldID) })
		}).
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
		WithKeys(func(q *dbent.UpstreamKeyQuery) {
			q.WithAccounts(func(aq *dbent.AccountQuery) { aq.Select(dbaccount.FieldID) })
		}).
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
		SetSiteURL(config.SiteURL).
		SetAuthMode(config.AuthMode).
		SetCredentials(normalizeJSONMap(config.Credentials)).
		SetExtra(normalizeJSONMap(config.Extra)).
		SetRechargeRate(config.RechargeRate).
		SetStatus(config.Status)
	if config.APIURL != nil {
		builder.SetAPIURL(*config.APIURL)
	}
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
			SetSiteURL(config.SiteURL).
			SetAuthMode(config.AuthMode).
			SetCredentials(normalizeJSONMap(config.Credentials)).
			SetExtra(normalizeJSONMap(config.Extra)).
			SetRechargeRate(config.RechargeRate).
			SetStatus(config.Status)
		if config.APIURL != nil {
			builder.SetAPIURL(*config.APIURL)
		} else {
			builder.ClearAPIURL()
		}
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

		nameChanged := previous.Name != updatedConfig.Name
		urlChanged := previous.SiteURL != updatedConfig.SiteURL || stringPointerValue(previous.APIURL) != stringPointerValue(updatedConfig.APIURL)
		rechargeRateChanged := previous.RechargeRate != updatedConfig.RechargeRate
		if !nameChanged && !urlChanged && !rechargeRateChanged {
			return nil
		}

		keys, err := client.UpstreamKey.Query().
			Where(dbupstreamkey.UpstreamConfigIDEQ(config.ID)).
			Order(dbent.Asc(dbupstreamkey.FieldID)).
			ForUpdate().
			All(txCtx)
		if err != nil {
			return err
		}
		accounts, err := client.Account.Query().
			Where(dbaccount.UpstreamConfigIDEQ(config.ID)).
			Order(dbent.Asc(dbaccount.FieldID)).
			ForUpdate().
			All(txCtx)
		if err != nil {
			return err
		}

		changedIDs := make([]int64, 0, len(accounts))
		if nameChanged {
			renamedIDs, err := renameLockedAccountsForUpstreamConfig(txCtx, client, config.ID, config.Name, keys, accounts)
			if err != nil {
				return err
			}
			changedIDs = append(changedIDs, renamedIDs...)
		}
		if urlChanged {
			for _, account := range accounts {
				changedIDs = append(changedIDs, account.ID)
			}
		}
		if rechargeRateChanged {
			changedAt := time.Now().UTC()
			costChangedIDs, err := recalculateLockedUpstreamAccounts(txCtx, client, updatedConfig, keys, accounts, changedAt)
			if err != nil {
				return err
			}
			changedIDs = append(changedIDs, costChangedIDs...)
			if err := createRechargeRateChangedEvent(txCtx, client, updatedConfig, previous.RechargeRate, updatedConfig.RechargeRate, changedAt); err != nil {
				return err
			}
			if err := recalculateLockedUpstreamBalance(txCtx, client, previous, previous.RechargeRate, updatedConfig.RechargeRate, changedAt); err != nil {
				return err
			}
		}
		return enqueueUpstreamAccountChanges(txCtx, client, changedIDs)
	})
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
		WithAccounts(func(q *dbent.AccountQuery) { q.Select(dbaccount.FieldID) }).
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

func (r *upstreamConfigRepository) ListKeysForMaskedFallback(ctx context.Context, upstreamConfigID int64, remoteKeyIDs []int64) ([]service.UpstreamKey, error) {
	remoteKeyIDs = uniqueSortedInt64s(remoteKeyIDs)
	if len(remoteKeyIDs) == 0 {
		return nil, nil
	}
	rows, err := r.client.UpstreamKey.Query().
		Where(
			dbupstreamkey.UpstreamConfigIDEQ(upstreamConfigID),
			dbupstreamkey.RemoteKeyIDIn(remoteKeyIDs...),
		).
		Order(dbent.Desc(dbupstreamkey.FieldDeletedAt), dbent.Desc(dbupstreamkey.FieldID)).
		All(mixins.SkipSoftDelete(ctx))
	if err != nil {
		return nil, err
	}
	byRemoteID := make(map[int64]*dbent.UpstreamKey, len(remoteKeyIDs))
	for _, row := range rows {
		if row.RemoteKeyID == nil || strings.TrimSpace(row.Key) == "" {
			continue
		}
		current := byRemoteID[*row.RemoteKeyID]
		if current == nil || (current.DeletedAt != nil && row.DeletedAt == nil) {
			byRemoteID[*row.RemoteKeyID] = row
		}
	}
	out := make([]service.UpstreamKey, 0, len(byRemoteID))
	for _, remoteID := range remoteKeyIDs {
		if row := byRemoteID[remoteID]; row != nil {
			out = append(out, *upstreamKeyEntityToService(row))
		}
	}
	return out, nil
}

func (r *upstreamConfigRepository) GetKeyByID(ctx context.Context, id int64) (*service.UpstreamKey, error) {
	row, err := r.client.UpstreamKey.Query().Where(dbupstreamkey.IDEQ(id)).WithAccounts(func(q *dbent.AccountQuery) { q.Select(dbaccount.FieldID) }).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrUpstreamKeyNotFound
		}
		return nil, err
	}
	return upstreamKeyEntityToService(row), nil
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
		query := client.UpstreamKey.Query().Where(
			dbupstreamkey.UpstreamConfigIDEQ(key.UpstreamConfigID),
			dbupstreamkey.RemoteKeyIDEQ(*key.RemoteKeyID),
		)
		existing, err := query.Clone().Only(ctx)
		if err == nil {
			return existing, nil
		}
		if !dbent.IsNotFound(err) {
			return nil, err
		}
		existing, err = query.Order(dbent.Desc(dbupstreamkey.FieldDeletedAt), dbent.Desc(dbupstreamkey.FieldID)).First(mixins.SkipSoftDelete(ctx))
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
	query := client.UpstreamKey.Query().Where(
		dbupstreamkey.UpstreamConfigIDEQ(key.UpstreamConfigID),
		dbupstreamkey.KeyHashEQ(key.KeyHash),
	)
	existing, err := query.Clone().Only(ctx)
	if err == nil {
		return existing, nil
	}
	if !dbent.IsNotFound(err) {
		return nil, err
	}
	existing, err = query.Order(dbent.Desc(dbupstreamkey.FieldDeletedAt), dbent.Desc(dbupstreamkey.FieldID)).First(mixins.SkipSoftDelete(ctx))
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
		SetNillablePlatform(key.Platform).
		SetPlatformSource(key.PlatformSource).
		SetNillableDetectedPlatform(key.DetectedPlatform).
		SetPlatformDetectionStatus(key.PlatformDetectionStatus).
		SetNillablePlatformDetectedAt(key.PlatformDetectedAt).
		SetStatus(key.Status).
		SetMissingCount(key.MissingCount).
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
	if key.SourceRateMultiplier != nil {
		builder.SetSourceRateMultiplier(*key.SourceRateMultiplier)
	}
	if key.LastSeenAt != nil {
		builder.SetLastSeenAt(*key.LastSeenAt)
	}
	if key.MissingSince != nil {
		builder.SetMissingSince(*key.MissingSince)
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
		ClearDeletedAt().
		SetUpstreamConfigID(key.UpstreamConfigID).
		SetName(key.Name).
		SetKey(key.Key).
		SetKeyHash(key.KeyHash).
		SetUpstreamGroupName(key.UpstreamGroupName).
		SetNillablePlatform(key.Platform).
		SetPlatformSource(key.PlatformSource).
		SetPlatformDetectionStatus(key.PlatformDetectionStatus).
		SetStatus(key.Status).
		SetMissingCount(key.MissingCount).
		SetExtra(normalizeJSONMap(key.Extra))
	if key.Platform == nil {
		builder.ClearPlatform()
	}
	if key.DetectedPlatform != nil {
		builder.SetDetectedPlatform(*key.DetectedPlatform)
	} else {
		builder.ClearDetectedPlatform()
	}
	if key.PlatformDetectedAt != nil {
		builder.SetPlatformDetectedAt(*key.PlatformDetectedAt)
	} else {
		builder.ClearPlatformDetectedAt()
	}
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
	if key.SourceRateMultiplier != nil {
		builder.SetSourceRateMultiplier(*key.SourceRateMultiplier)
	} else {
		builder.ClearSourceRateMultiplier()
	}
	if key.LastSeenAt != nil {
		builder.SetLastSeenAt(*key.LastSeenAt)
	} else {
		builder.ClearLastSeenAt()
	}
	if key.MissingSince != nil {
		builder.SetMissingSince(*key.MissingSince)
	} else {
		builder.ClearMissingSince()
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

func (r *upstreamConfigRepository) UpdateKeyPlatform(ctx context.Context, configID, keyID int64, platform string, expectedUpdatedAt time.Time, disableBoundAccounts bool) (*service.UpstreamKey, error) {
	var result *service.UpstreamKey
	err := r.withTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		if _, err := client.UpstreamConfig.Query().Where(dbupstreamconfig.IDEQ(configID)).ForUpdate().Only(txCtx); err != nil {
			if dbent.IsNotFound(err) {
				return service.ErrUpstreamConfigNotFound
			}
			return err
		}
		key, err := client.UpstreamKey.Query().Where(dbupstreamkey.IDEQ(keyID), dbupstreamkey.UpstreamConfigIDEQ(configID)).ForUpdate().Only(txCtx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return service.ErrUpstreamKeyNotFound
			}
			return err
		}
		if !key.UpdatedAt.Equal(expectedUpdatedAt) {
			return infraerrors.Conflict("UPSTREAM_KEY_PLATFORM_STALE", "upstream key changed; reload before updating its platform").WithMetadata(map[string]string{
				"current_updated_at": key.UpdatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
		accounts, err := client.Account.Query().Where(dbaccount.UpstreamKeyIDEQ(keyID)).Order(dbent.Asc(dbaccount.FieldID)).ForUpdate().All(txCtx)
		if err != nil {
			return err
		}
		mismatched := make([]*dbent.Account, 0)
		for _, account := range accounts {
			if !strings.EqualFold(strings.TrimSpace(account.Platform), platform) {
				mismatched = append(mismatched, account)
			}
		}
		if len(mismatched) > 0 && !disableBoundAccounts {
			summaries := make([]string, 0, len(mismatched))
			for _, account := range mismatched {
				summaries = append(summaries, fmt.Sprintf("%d:%s:%s", account.ID, sanitizeAccountSummaryName(account.Name), account.Platform))
			}
			return infraerrors.Conflict("UPSTREAM_KEY_PLATFORM_BOUND_ACCOUNT_CONFLICT", "changing platform requires disabling incompatible bound accounts").WithMetadata(map[string]string{
				"bound_account_count": strconv.Itoa(len(mismatched)),
				"bound_accounts":      strings.Join(summaries, ","),
			})
		}
		changedIDs := make([]int64, 0, len(mismatched))
		for _, account := range mismatched {
			if account.Status != service.StatusDisabled || account.Schedulable {
				if _, err := client.Account.UpdateOneID(account.ID).SetStatus(service.StatusDisabled).SetSchedulable(false).Save(txCtx); err != nil {
					return err
				}
				changedIDs = append(changedIDs, account.ID)
			}
		}
		status := key.PlatformDetectionStatus
		if key.DetectedPlatform != nil && !strings.EqualFold(strings.TrimSpace(*key.DetectedPlatform), platform) {
			status = service.UpstreamKeyPlatformDetectionConflict
		} else if key.DetectedPlatform != nil {
			status = service.UpstreamKeyPlatformDetectionDetected
		}
		updated, err := client.UpstreamKey.UpdateOneID(keyID).
			SetPlatform(platform).
			SetPlatformSource(service.UpstreamKeyPlatformSourceManual).
			SetPlatformDetectionStatus(status).
			Save(txCtx)
		if err != nil {
			return err
		}
		event := client.UpstreamEvent.Create().SetUpstreamConfigID(configID).SetUpstreamKeyID(keyID).SetEventType("key_platform_changed").SetSeverity("info").SetSource("admin").SetMessage("Upstream key platform was assigned by an administrator").SetPayload(map[string]any{
			"old_platform": key.Platform, "new_platform": platform, "disabled_account_count": len(changedIDs),
		}).SetOccurredAt(time.Now().UTC())
		if err := event.Exec(txCtx); err != nil {
			return err
		}
		if err := enqueueUpstreamAccountChanges(txCtx, client, changedIDs); err != nil {
			return err
		}
		updated.Edges.Accounts = accounts
		result = upstreamKeyEntityToService(updated)
		return nil
	})
	return result, err
}

func sanitizeAccountSummaryName(name string) string {
	name = strings.TrimSpace(name)
	if len([]rune(name)) <= 32 {
		return name
	}
	return string([]rune(name)[:32])
}

func (r *upstreamConfigRepository) ApplySyncSnapshot(ctx context.Context, configID, runID int64, keys []service.UpstreamKey, extraUpdates map[string]any, checkedAt time.Time, complete bool) ([]service.UpstreamKey, service.UpstreamKeyReconcileResult, int, error) {
	var (
		localKeys  []service.UpstreamKey
		reconciled service.UpstreamKeyReconcileResult
		updated    int
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
		// Keep lock order stable across sync and manual platform updates.
		lockedKeys, err := client.UpstreamKey.Query().
			Where(dbupstreamkey.UpstreamConfigIDEQ(configID)).
			Order(dbent.Asc(dbupstreamkey.FieldID)).
			ForUpdate().
			All(mixins.SkipSoftDelete(txCtx))
		if err != nil {
			return err
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
		lockedKeyByID := make(map[int64]*dbent.UpstreamKey, len(lockedKeys))
		accountsByKeyID := make(map[int64][]*dbent.Account)
		for _, key := range lockedKeys {
			lockedKeyByID[key.ID] = key
		}
		for _, account := range accounts {
			if account.UpstreamKeyID != nil {
				accountsByKeyID[*account.UpstreamKeyID] = append(accountsByKeyID[*account.UpstreamKeyID], account)
			}
		}

		seenKeyIDs := make(map[int64]struct{}, len(keys))
		reconciledAccountIDs := make([]int64, 0)
		for i := range keys {
			keys[i].UpstreamConfigID = configID
			if keys[i].SourceRateMultiplier != nil {
				actualRate, rateErr := service.NormalizeUpstreamActualRate(*keys[i].SourceRateMultiplier, config.RechargeRate)
				if rateErr != nil {
					return rateErr
				}
				keys[i].RateMultiplier = &actualRate
			} else {
				keys[i].RateMultiplier = nil
			}
			keys[i].MissingCount = 0
			keys[i].MissingSince = nil
			if !complete && config.Provider == service.UpstreamProviderNewAPI {
				// Partial key snapshots are not authoritative platform evidence. Keep
				// the raw evidence in Extra for diagnostics, but never change a
				// platform assignment or disable accounts from an incomplete view.
				keys[i].Platform = nil
				keys[i].PlatformSource = service.UpstreamKeyPlatformSourceUnassigned
				keys[i].DetectedPlatform = nil
				keys[i].PlatformDetectionStatus = service.UpstreamKeyPlatformDetectionLegacy
				keys[i].PlatformDetectedAt = nil
			}
			existing, queryErr := findUpstreamKeyForUpsert(txCtx, client, &keys[i])
			if queryErr != nil {
				return queryErr
			}
			if existing == nil {
				mergeNewUpstreamKeyPlatform(&keys[i])
				if err := createUpstreamKey(txCtx, client, &keys[i]); err != nil {
					return err
				}
				seenKeyIDs[keys[i].ID] = struct{}{}
				continue
			}
			existing = lockedKeyByID[existing.ID]
			if existing == nil {
				return fmt.Errorf("upstream key %d was not locked", keys[i].ID)
			}
			wasMissing := existing.DeletedAt != nil || existing.MissingCount > 0 || existing.Status == service.UpstreamKeyStatusStale
			keys[i].ID = existing.ID
			platformChangedAccountIDs, err := mergeSyncedUpstreamKeyPlatform(txCtx, client, configID, runID, &keys[i], existing, accountsByKeyID[existing.ID], checkedAt)
			if err != nil {
				return err
			}
			reconciledAccountIDs = append(reconciledAccountIDs, platformChangedAccountIDs...)
			if err := updateUpstreamKey(txCtx, client, &keys[i]); err != nil {
				return err
			}
			seenKeyIDs[existing.ID] = struct{}{}
			if wasMissing {
				reconciled.Restored++
				if err := createUpstreamKeyLifecycleEvent(txCtx, client, configID, existing.ID, runID, "key_restored", "info", "Upstream key reappeared and was restored", checkedAt, nil); err != nil {
					return err
				}
			}
			if keys[i].Status == service.StatusActive && (wasMissing || existing.Status != service.StatusActive) {
				restoredAccountIDs, err := restoreAccountsPausedForStaleKey(txCtx, client, existing.ID)
				if err != nil {
					return err
				}
				reconciledAccountIDs = append(reconciledAccountIDs, restoredAccountIDs...)
			}
		}
		if complete {
			var err error
			reconciledMissing, pausedAccountIDs, err := reconcileMissingUpstreamKeys(txCtx, client, configID, runID, seenKeyIDs, checkedAt)
			if err != nil {
				return err
			}
			reconciled.Missing += reconciledMissing.Missing
			reconciled.Stale += reconciledMissing.Stale
			reconciled.Deleted += reconciledMissing.Deleted
			reconciledAccountIDs = append(reconciledAccountIDs, pausedAccountIDs...)
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
		}
		if err := persistUpstreamKeyRateSnapshots(txCtx, client, config, runID, keys, checkedAt); err != nil {
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
		if err := createNewAPIGroupStructureEvents(txCtx, client, config, runID, extraUpdates, checkedAt); err != nil {
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
		allChangedIDs := append(changedIDs, reconciledAccountIDs...)
		allChangedIDs = uniqueSortedInt64s(allChangedIDs)
		if err := enqueueUpstreamAccountChanges(txCtx, client, allChangedIDs); err != nil {
			return err
		}
		updated = len(allChangedIDs)
		return nil
	})
	if err != nil {
		return nil, service.UpstreamKeyReconcileResult{}, 0, err
	}
	return localKeys, reconciled, updated, nil
}

func mergeNewUpstreamKeyPlatform(key *service.UpstreamKey) {
	if key == nil {
		return
	}
	if key.PlatformDetectionStatus == service.UpstreamKeyPlatformDetectionDetected && key.DetectedPlatform != nil {
		key.Platform = copyStringPointer(key.DetectedPlatform)
		key.PlatformSource = service.UpstreamKeyPlatformSourceAuto
		return
	}
	key.Platform = nil
	key.PlatformSource = service.UpstreamKeyPlatformSourceUnassigned
}

func mergeSyncedUpstreamKeyPlatform(ctx context.Context, client *dbent.Client, configID, runID int64, key *service.UpstreamKey, existing *dbent.UpstreamKey, accounts []*dbent.Account, checkedAt time.Time) ([]int64, error) {
	if key == nil || existing == nil {
		return nil, nil
	}
	// A partial provider snapshot carries no authoritative platform decision.
	if key.PlatformDetectionStatus == "" || key.PlatformDetectionStatus == service.UpstreamKeyPlatformDetectionLegacy {
		copyExistingUpstreamKeyPlatform(key, existing)
		return nil, nil
	}
	key.PlatformDetectedAt = &checkedAt
	if existing.PlatformSource == service.UpstreamKeyPlatformSourceManual {
		key.Platform = copyStringPointer(existing.Platform)
		key.PlatformSource = service.UpstreamKeyPlatformSourceManual
		if key.DetectedPlatform != nil && !sameOptionalPlatform(existing.Platform, key.DetectedPlatform) {
			key.PlatformDetectionStatus = service.UpstreamKeyPlatformDetectionConflict
		}
		return nil, nil
	}
	if key.PlatformDetectionStatus != service.UpstreamKeyPlatformDetectionDetected || key.DetectedPlatform == nil {
		if len(accounts) > 0 {
			key.Platform = copyStringPointer(existing.Platform)
			key.PlatformSource = existing.PlatformSource
			return nil, nil
		}
		key.Platform = nil
		key.PlatformSource = service.UpstreamKeyPlatformSourceUnassigned
		return nil, nil
	}

	candidate := strings.ToLower(strings.TrimSpace(*key.DetectedPlatform))
	mismatched := make([]*dbent.Account, 0)
	for _, account := range accounts {
		if !strings.EqualFold(strings.TrimSpace(account.Platform), candidate) {
			mismatched = append(mismatched, account)
		}
	}
	if len(mismatched) == 0 {
		key.Platform = &candidate
		key.PlatformSource = service.UpstreamKeyPlatformSourceAuto
		return nil, nil
	}

	// Existing bindings make an automatic platform switch destructive. Preserve the
	// current assignment, disable every incompatible account, and require review.
	key.Platform = copyStringPointer(existing.Platform)
	key.PlatformSource = existing.PlatformSource
	key.PlatformDetectionStatus = service.UpstreamKeyPlatformDetectionConflict
	disabledIDs := make([]int64, 0, len(mismatched))
	for _, account := range mismatched {
		if account.Status != service.StatusDisabled || account.Schedulable {
			if _, err := client.Account.UpdateOneID(account.ID).SetStatus(service.StatusDisabled).SetSchedulable(false).Save(ctx); err != nil {
				return nil, err
			}
			disabledIDs = append(disabledIDs, account.ID)
		}
	}
	if len(disabledIDs) > 0 {
		payload := map[string]any{
			"detected_platform": candidate,
			"account_count":     len(disabledIDs),
		}
		if err := createUpstreamKeyLifecycleEvent(ctx, client, configID, existing.ID, runID, "key_platform_conflict", "warning", "Upstream key platform conflicts with bound accounts", checkedAt, payload); err != nil {
			return nil, err
		}
	}
	return disabledIDs, nil
}

func copyExistingUpstreamKeyPlatform(key *service.UpstreamKey, existing *dbent.UpstreamKey) {
	key.Platform = copyStringPointer(existing.Platform)
	key.PlatformSource = existing.PlatformSource
	key.DetectedPlatform = copyStringPointer(existing.DetectedPlatform)
	key.PlatformDetectionStatus = existing.PlatformDetectionStatus
	key.PlatformDetectedAt = existing.PlatformDetectedAt
}

func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func sameOptionalPlatform(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(strings.TrimSpace(*left), strings.TrimSpace(*right))
}

func createNewAPIGroupStructureEvents(ctx context.Context, client *dbent.Client, config *dbent.UpstreamConfig, runID int64, updates map[string]any, at time.Time) error {
	if config == nil || config.Provider != service.UpstreamProviderNewAPI {
		return nil
	}
	oldSnapshot, _ := config.Extra["upstream_provider_snapshot"].(map[string]any)
	newSnapshot, _ := updates["upstream_provider_snapshot"].(map[string]any)
	oldGroupsComplete, _ := oldSnapshot["groups_complete"].(bool)
	newGroupsComplete, _ := newSnapshot["groups_complete"].(bool)
	if !oldGroupsComplete || !newGroupsComplete {
		return nil
	}
	oldGroups := jsonObjectMap(oldSnapshot["groups"])
	newGroups := jsonObjectMap(newSnapshot["groups"])
	for _, name := range sortedMapKeys(newGroups) {
		newEntry := jsonObjectMap(newGroups[name])
		oldRaw, existed := oldGroups[name]
		if !existed {
			if err := createNewAPIGroupEvent(ctx, client, config.ID, runID, "group_added", name, nil, newEntry, at); err != nil {
				return err
			}
			continue
		}
		oldEntry := jsonObjectMap(oldRaw)
		oldRatio, oldOK := numberFromMap(oldEntry, "ratio")
		newRatio, newOK := numberFromMap(newEntry, "ratio")
		if oldOK && newOK && oldRatio != newRatio {
			if err := createNewAPIGroupEvent(ctx, client, config.ID, runID, "group_rate_changed", name, oldRatio, newRatio, at); err != nil {
				return err
			}
		}
	}
	for _, name := range sortedMapKeys(oldGroups) {
		if _, exists := newGroups[name]; exists {
			continue
		}
		if err := createNewAPIGroupEvent(ctx, client, config.ID, runID, "group_removed", name, jsonObjectMap(oldGroups[name]), nil, at); err != nil {
			return err
		}
	}
	return nil
}

func createNewAPIGroupEvent(ctx context.Context, client *dbent.Client, configID, runID int64, eventType, group string, oldValue, newValue any, at time.Time) error {
	eventKey := fmt.Sprintf("newapi_group:%s:%s:%d", group, eventType, runID)
	b := client.UpstreamEvent.Create().SetUpstreamConfigID(configID).SetEventType(eventType).SetSeverity("info").SetSource("sync").SetMessage("NewAPI group structure changed").SetPayload(map[string]any{"group": group, "old_value": oldValue, "new_value": newValue}).SetOccurredAt(at)
	if runID > 0 {
		b.SetSyncRunID(runID).SetEventKey(eventKey)
		return b.OnConflict(
			entsql.ConflictColumns(dbupstreamevent.FieldUpstreamConfigID, dbupstreamevent.FieldEventKey),
			entsql.ConflictWhere(entsql.NotNull(dbupstreamevent.FieldEventKey)),
			entsql.DoNothing(),
		).Exec(ctx)
	}
	return b.Exec(ctx)
}

func jsonObjectMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func reconcileMissingUpstreamKeys(ctx context.Context, client *dbent.Client, configID, runID int64, seenKeyIDs map[int64]struct{}, checkedAt time.Time) (service.UpstreamKeyReconcileResult, []int64, error) {
	result := service.UpstreamKeyReconcileResult{}
	changedAccountIDs := make([]int64, 0)
	rows, err := client.UpstreamKey.Query().
		Where(dbupstreamkey.UpstreamConfigIDEQ(configID)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return result, nil, err
	}
	for _, row := range rows {
		if _, seen := seenKeyIDs[row.ID]; seen {
			continue
		}
		missingCount := row.MissingCount + 1
		missingSince := row.MissingSince
		if missingSince == nil {
			firstMissing := checkedAt
			missingSince = &firstMissing
			if err := createUpstreamKeyLifecycleEvent(ctx, client, configID, row.ID, runID, "key_missing_detected", "warning", "Upstream key was absent from a complete synchronization snapshot", checkedAt, map[string]any{"missing_count": missingCount}); err != nil {
				return result, nil, err
			}
		}
		result.Missing++
		eligible := upstreamKeyMissingEligible(missingCount, missingSince, checkedAt)
		if !eligible {
			if _, err := client.UpstreamKey.UpdateOneID(row.ID).SetMissingCount(missingCount).SetMissingSince(*missingSince).Save(ctx); err != nil {
				return result, nil, err
			}
			continue
		}
		accountCount, err := client.Account.Query().Where(dbaccount.UpstreamKeyIDEQ(row.ID)).Count(ctx)
		if err != nil {
			return result, nil, err
		}
		if accountCount == 0 {
			if err := createUpstreamKeyLifecycleEvent(ctx, client, configID, row.ID, runID, "key_deleted", "info", "Missing upstream key was soft-deleted after the grace period", checkedAt, map[string]any{"missing_count": missingCount}); err != nil {
				return result, nil, err
			}
			if err := client.UpstreamKey.DeleteOneID(row.ID).Exec(ctx); err != nil {
				return result, nil, err
			}
			result.Deleted++
			continue
		}
		if row.Status != service.UpstreamKeyStatusStale {
			if _, err := client.UpstreamKey.UpdateOneID(row.ID).SetStatus(service.UpstreamKeyStatusStale).SetMissingCount(missingCount).SetMissingSince(*missingSince).Save(ctx); err != nil {
				return result, nil, err
			}
			if err := createUpstreamKeyLifecycleEvent(ctx, client, configID, row.ID, runID, "key_marked_stale", "warning", "Missing upstream key was marked stale and bound accounts were paused", checkedAt, map[string]any{"missing_count": missingCount, "account_count": accountCount}); err != nil {
				return result, nil, err
			}
			pausedIDs, err := pauseAccountsForStaleKey(ctx, client, row.ID)
			if err != nil {
				return result, nil, err
			}
			changedAccountIDs = append(changedAccountIDs, pausedIDs...)
			result.Stale++
		} else if _, err := client.UpstreamKey.UpdateOneID(row.ID).SetMissingCount(missingCount).Save(ctx); err != nil {
			return result, nil, err
		}
	}
	return result, changedAccountIDs, nil
}

func upstreamKeyMissingEligible(missingCount int, missingSince *time.Time, checkedAt time.Time) bool {
	return missingSince != nil && missingCount >= upstreamKeyMissingThreshold && !checkedAt.Before(missingSince.Add(upstreamKeyMissingGrace))
}

func createUpstreamKeyLifecycleEvent(ctx context.Context, client *dbent.Client, configID, keyID, runID int64, eventType, severity, message string, occurredAt time.Time, payload map[string]any) error {
	builder := client.UpstreamEvent.Create().
		SetUpstreamConfigID(configID).
		SetUpstreamKeyID(keyID).
		SetEventType(eventType).
		SetSeverity(severity).
		SetSource("sync").
		SetMessage(message).
		SetOccurredAt(occurredAt)
	if runID > 0 {
		builder.SetSyncRunID(runID)
		builder.SetEventKey(fmt.Sprintf("key_lifecycle:%d:%s:%d", keyID, eventType, runID))
	}
	if len(payload) > 0 {
		builder.SetPayload(payload)
	}
	return builder.Exec(ctx)
}

func pauseAccountsForStaleKey(ctx context.Context, client *dbent.Client, keyID int64) ([]int64, error) {
	accounts, err := client.Account.Query().Where(dbaccount.UpstreamKeyIDEQ(keyID)).ForUpdate().All(ctx)
	if err != nil {
		return nil, err
	}
	changedIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account.Schedulable {
			if _, err := client.Account.UpdateOneID(account.ID).
				SetSchedulable(false).
				SetUpstreamStalePauseKeyID(keyID).
				SetUpstreamStalePausedAt(time.Now().UTC()).
				Save(ctx); err != nil {
				return nil, err
			}
			changedIDs = append(changedIDs, account.ID)
		}
	}
	return changedIDs, nil
}

func restoreAccountsPausedForStaleKey(ctx context.Context, client *dbent.Client, keyID int64) ([]int64, error) {
	accounts, err := client.Account.Query().Where(dbaccount.UpstreamKeyIDEQ(keyID)).ForUpdate().All(ctx)
	if err != nil {
		return nil, err
	}
	changedIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account.UpstreamStalePauseKeyID == nil || *account.UpstreamStalePauseKeyID != keyID || account.Status != service.StatusActive {
			continue
		}
		if _, err := client.Account.UpdateOneID(account.ID).
			SetSchedulable(true).
			ClearUpstreamStalePauseKeyID().
			ClearUpstreamStalePausedAt().
			Save(ctx); err != nil {
			return nil, err
		}
		changedIDs = append(changedIDs, account.ID)
	}
	return changedIDs, nil
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

func renameLockedAccountsForUpstreamConfig(ctx context.Context, client *dbent.Client, configID int64, configName string, keys []*dbent.UpstreamKey, accounts []*dbent.Account) ([]int64, error) {
	keyByID := make(map[int64]*dbent.UpstreamKey, len(keys))
	for _, key := range keys {
		keyByID[key.ID] = key
	}
	changedIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account.UpstreamKeyID == nil {
			continue
		}
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
		multiplier := *key.RateMultiplier
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
		SiteURL:          row.SiteURL,
		APIURL:           row.APIURL,
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
		ID:                      row.ID,
		UpstreamConfigID:        row.UpstreamConfigID,
		Name:                    row.Name,
		Key:                     row.Key,
		KeyHash:                 row.KeyHash,
		RemoteKeyID:             row.RemoteKeyID,
		UpstreamGroupID:         row.UpstreamGroupID,
		UpstreamGroupName:       row.UpstreamGroupName,
		Platform:                row.Platform,
		PlatformSource:          row.PlatformSource,
		DetectedPlatform:        row.DetectedPlatform,
		PlatformDetectionStatus: row.PlatformDetectionStatus,
		PlatformDetectedAt:      row.PlatformDetectedAt,
		BoundAccountCount:       len(row.Edges.Accounts),
		RateMultiplier:          row.RateMultiplier,
		SourceRateMultiplier:    row.SourceRateMultiplier,
		Status:                  row.Status,
		LastSeenAt:              row.LastSeenAt,
		MissingCount:            row.MissingCount,
		MissingSince:            row.MissingSince,
		Extra:                   copyJSONMap(row.Extra),
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
	}
}
