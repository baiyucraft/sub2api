package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type effectiveUserRatesRepoStub struct {
	UserGroupRateRepository
	rates map[int64]float64
	err   error
	calls int
}

func (r *effectiveUserRatesRepoStub) GetEffectiveByUserID(context.Context, int64) (map[int64]float64, error) {
	r.calls++
	return r.rates, r.err
}

func TestAPIKeyServiceUserGroupRatesUsesEffectiveDefaultsWithoutExplicitMap(t *testing.T) {
	repo := &effectiveUserRatesRepoStub{rates: map[int64]float64{8: .19, 21: 0}}
	svc := &APIKeyService{userGroupRateRepo: repo}
	got, err := svc.GetUserGroupRates(context.Background(), 135)
	require.NoError(t, err)
	require.Equal(t, repo.rates, got)
	require.Equal(t, 1, repo.calls)
	repo.err = errors.New("storage unavailable")
	_, err = svc.GetUserGroupRates(context.Background(), 135)
	require.ErrorIs(t, err, repo.err)
}
