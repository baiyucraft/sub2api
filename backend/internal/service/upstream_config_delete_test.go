package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type upstreamConfigDeleteSummaryRepo struct {
	UpstreamConfigRepository
	summary      UpstreamConfigBindingSummary
	deleteCalled bool
}

func (r *upstreamConfigDeleteSummaryRepo) GetBindingSummary(context.Context, int64) (UpstreamConfigBindingSummary, error) {
	return r.summary, nil
}

func (r *upstreamConfigDeleteSummaryRepo) CountAccounts(context.Context, int64) (int64, error) {
	return int64(r.summary.BoundAccountCount), nil
}

func (r *upstreamConfigDeleteSummaryRepo) Delete(context.Context, int64) error {
	r.deleteCalled = true
	return nil
}

func TestUpstreamConfigDeleteWithoutConfirmationReturnsRedactedBindingMetadata(t *testing.T) {
	repo := &upstreamConfigDeleteSummaryRepo{summary: UpstreamConfigBindingSummary{
		BoundAccountCount:       3,
		ManualAccountCount:      1,
		MissingKeyAccountCount:  1,
		SyncManagedAccountCount: 2,
	}}
	service := NewUpstreamConfigService(repo, nil, nil)

	_, err := service.DeleteWithOptions(context.Background(), 42, UpstreamConfigDeleteOptions{})
	require.Error(t, err)
	appErr := infraerrors.FromError(err)
	require.Equal(t, "UPSTREAM_CONFIG_IN_USE", appErr.Reason)
	require.Equal(t, map[string]string{
		"bound_account_count":        "3",
		"manual_account_count":       "1",
		"missing_key_account_count":  "1",
		"sync_managed_account_count": "2",
	}, appErr.Metadata)
	require.False(t, repo.deleteCalled)
}
