package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type upstreamAccountNameBackfillRepoStub struct {
	UpstreamConfigRepository
	previewItems []UpstreamAccountNameBackfillItem
	applyItems   []UpstreamAccountNameBackfillItem
	previewCalls int
	applyCalls   int
}

func (r *upstreamAccountNameBackfillRepoStub) PreviewAccountNameBackfill(context.Context) ([]UpstreamAccountNameBackfillItem, error) {
	r.previewCalls++
	return r.previewItems, nil
}

func (r *upstreamAccountNameBackfillRepoStub) ApplyAccountNameBackfill(context.Context) ([]UpstreamAccountNameBackfillItem, error) {
	r.applyCalls++
	return r.applyItems, nil
}

func TestUpstreamConfigServiceAccountNameBackfillModes(t *testing.T) {
	change := UpstreamAccountNameBackfillItem{
		AccountID: 1,
		OldName:   "可达鸭pro",
		NewName:   "可达鸭-pro",
	}
	repo := &upstreamAccountNameBackfillRepoStub{
		previewItems: []UpstreamAccountNameBackfillItem{change},
		applyItems:   []UpstreamAccountNameBackfillItem{change},
	}
	svc := NewUpstreamConfigService(repo, nil, nil)

	preview, err := svc.PreviewAccountNameBackfill(context.Background())
	require.NoError(t, err)
	require.Equal(t, []UpstreamAccountNameBackfillItem{change}, preview)
	require.Equal(t, 1, repo.previewCalls)
	require.Zero(t, repo.applyCalls)

	applied, err := svc.ApplyAccountNameBackfill(context.Background())
	require.NoError(t, err)
	require.Equal(t, []UpstreamAccountNameBackfillItem{change}, applied)
	require.Equal(t, 1, repo.applyCalls)
}

func TestUpstreamConfigServiceAccountNameBackfillUnavailable(t *testing.T) {
	svc := NewUpstreamConfigService(&upstreamConfigServiceRepo{}, nil, nil)

	_, err := svc.PreviewAccountNameBackfill(context.Background())
	require.Error(t, err)
}
