package service

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsModelAllowlistPersistRepo struct{ OpsRepository }

func TestOpsModelAllowlistRejectionSnapshotsBoundedAdmissionMetadata(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	key := &APIKey{ID: 11, UserID: 12, Group: &Group{ID: 21, Platform: PlatformOpenAI}}
	MarkOpsModelAllowlistRejection(c, key, " "+strings.Repeat("中", 99)+"\nextra", true, true)
	key.Group.ID = 99
	key.ID = 98
	MarkOpsModelAllowlistRejection(c, key, "must-not-replace-first-rejection", false, false)
	got, ok := GetOpsModelAllowlistRejection(c)
	require.True(t, ok)
	require.Equal(t, int64(21), got.GroupID)
	require.Equal(t, int64(11), got.APIKeyID)
	require.Equal(t, int64(12), got.UserID)
	require.Equal(t, 100, utf8.RuneCountInString(got.Model))
	require.NotContains(t, got.Model, "\n")
	require.True(t, got.Stream)
	require.True(t, got.WebSocket)
	require.False(t, got.RejectedAt.IsZero())
}

func TestOpsModelAllowlistRejectionUserViewIsInvalidRequest(t *testing.T) {
	row := ToUserErrorRequest(&OpsErrorLog{Phase: "request", Type: OpsErrorReasonModelNotAllowed, RequestedModel: "blocked-model"})
	require.Equal(t, "invalid_request", row.Category)
	require.Equal(t, "blocked-model", row.Model)
}

func TestOpsModelAllowlistRejectionPersistenceKeepsClientClassification(t *testing.T) {
	svc := NewOpsService(&opsModelAllowlistPersistRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	entry, keep, err := svc.prepareErrorLogInput(context.Background(), &OpsInsertErrorLogInput{
		ErrorPhase: "request", ErrorType: OpsErrorReasonModelNotAllowed,
		ErrorOwner: "client", ErrorSource: "client_request", StatusCode: 404, IsBusinessLimited: true,
		Model: "blocked", RequestedModel: "blocked",
	})
	require.NoError(t, err)
	require.True(t, keep)
	require.Equal(t, OpsErrorReasonModelNotAllowed, entry.ErrorType)
	require.Equal(t, "request", entry.ErrorPhase)
	require.Equal(t, "client", entry.ErrorOwner)
	require.True(t, entry.IsBusinessLimited)
	require.Empty(t, entry.ErrorBody)
	require.Nil(t, entry.UpstreamStatusCode)
	require.Empty(t, entry.UpstreamErrors)
}
