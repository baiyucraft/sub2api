//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pkgerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cachedZeroRateRepo struct {
	service.UserGroupRateRepository
}

func (*cachedZeroRateRepo) GetPercentByUserID(context.Context, int64) (map[int64]float64, error) {
	return map[int64]float64{42: 0}, nil
}

func (*cachedZeroRateRepo) GetPercentByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	zero := 0.0
	return &zero, nil
}

func TestGeminiCachedContentsExplicitZeroRateStillChecksKeyQuota(t *testing.T) {
	for _, mutation := range []string{"create", "patch"} {
		for _, sufficient := range []bool{true, false} {
			t.Run(mutation+map[bool]string{true: "/quota_sufficient", false: "/quota_exhausted"}[sufficient], func(t *testing.T) {
				env := newCachedBillingEnvWithRate(t, 0, &cachedZeroRateRepo{}, 900)
				env.apiKey.Quota = 200
				if !sufficient {
					env.apiKey.Quota = 1
				}
				env.upstream.createBody = cachedBillingCreateBody(time.Now().Add(24 * time.Hour))
				if mutation == "patch" {
					env.seed(time.Now().Add(time.Hour))
				}
				var rec *httptest.ResponseRecorder
				if mutation == "patch" {
					// PATCH 在调用上游前即可知道存储计量金额。
					rec = env.patch()
				} else {
					rec = env.create()
				}
				if sufficient {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					env.waitForUsage(t, 1)
					require.Zero(t, env.usage.first().ActualCost, "零余额、显式零倍率用户的缓存费用实付为零")
					require.Positive(t, env.apiKey.QuotaUsed, "显式零倍率仍按原分组价格实际计量 Key 额度")
					return
				}
				require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				var failure struct {
					Error struct {
						Status  string `json:"status"`
						Message string `json:"message"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &failure))
				require.Equal(t, "PERMISSION_DENIED", failure.Error.Status)
				require.Equal(t, pkgerrors.Message(service.ErrAPIKeyQuotaExhausted), failure.Error.Message)
				if mutation == "patch" {
					require.Zero(t, env.upstream.methodCalls(http.MethodPatch), "Key 存储计量不足时延期不能调用上游")
				} else {
					require.Equal(t, 1, env.upstream.methodCalls(http.MethodDelete), "创建后发现 Key 计量不足时删除缓存")
					env.waitForUsage(t, 1)
					require.Zero(t, env.usage.first().ActualCost, "输入费落账仍保持零实付")
				}
			})
		}
	}
}
