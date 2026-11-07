package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsModelReasonRepo struct {
	service.OpsRepository
	filter *service.OpsErrorLogFilter
}

func (r *opsModelReasonRepo) ListErrorLogs(_ context.Context, filter *service.OpsErrorLogFilter) (*service.OpsErrorLogList, error) {
	r.filter = filter
	return &service.OpsErrorLogList{Errors: []*service.OpsErrorLog{}, Page: 1, PageSize: 20}, nil
}

func TestOpsModelAllowlistReasonFiltersBothRequestListEndpoints(t *testing.T) {
	for _, requestList := range []bool{false, true} {
		for _, reason := range []string{"", service.OpsErrorReasonModelNotAllowed, "invalid_api_key"} {
			t.Run(map[bool]string{true: "requests", false: "legacy"}[requestList]+"/"+reason, func(t *testing.T) {
				repo := &opsModelReasonRepo{}
				h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodGet, "/errors?reason="+reason+"&view=excluded&group_id=21&model=blocked", nil)
				if requestList {
					h.ListRequestErrors(c)
				} else {
					h.GetErrorLogs(c)
				}
				if reason == "invalid_api_key" {
					require.Equal(t, http.StatusBadRequest, w.Code)
					require.Nil(t, repo.filter)
					return
				}
				require.Equal(t, http.StatusOK, w.Code)
				require.NotNil(t, repo.filter)
				require.Equal(t, reason, repo.filter.Reason)
				require.Equal(t, "excluded", repo.filter.View)
				require.Equal(t, "blocked", repo.filter.Model)
				require.NotNil(t, repo.filter.GroupID)
				require.Equal(t, int64(21), *repo.filter.GroupID)
			})
		}
	}
}

func TestOpsIngressRejectReasonAcceptsModelNotAllowed(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/ingress-rejections?reason=model_not_allowed", nil)
	reason, err := parseIngressRejectEnum(c, "reason", ingressRejectReasons)
	require.NoError(t, err)
	require.Equal(t, service.OpsErrorReasonModelNotAllowed, reason)
}
