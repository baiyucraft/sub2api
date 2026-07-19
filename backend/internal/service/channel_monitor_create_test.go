//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type createManagedMonitorRepoStub struct {
	ChannelMonitorRepository
	created *ChannelMonitor
}

func (r *createManagedMonitorRepoStub) CreateManaged(_ context.Context, monitor *ChannelMonitor, _ string) error {
	copy := *monitor
	r.created = &copy
	monitor.ID = 99
	return nil
}

func (r *createManagedMonitorRepoStub) UpdateManaged(context.Context, *ChannelMonitor) (string, error) {
	return "", nil
}

func (r *createManagedMonitorRepoStub) DeleteManaged(context.Context, int64) (string, error) {
	return "", nil
}

type createManagedMonitorKeyStub struct{}

func (createManagedMonitorKeyStub) GenerateKey() (string, error)                     { return "managed-test-key", nil }
func (createManagedMonitorKeyStub) InvalidateAuthCacheByKey(context.Context, string) {}

type createManagedMonitorSettingsStub struct{}

func (createManagedMonitorSettingsStub) GetAPIBaseURL(context.Context) string {
	return "https://example.com"
}

func TestResolveCreateShowGroupRate(t *testing.T) {
	trueValue := true
	falseValue := false

	tests := []struct {
		name           string
		credentialMode string
		configured     *bool
		want           bool
	}{
		{
			name:           "managed defaults to visible",
			credentialMode: ChannelMonitorCredentialManagedLocal,
			want:           true,
		},
		{
			name:           "managed can explicitly disable",
			credentialMode: ChannelMonitorCredentialManagedLocal,
			configured:     &falseValue,
			want:           false,
		},
		{
			name:           "manual keeps hidden default",
			credentialMode: ChannelMonitorCredentialManual,
			want:           false,
		},
		{
			name:           "manual preserves explicit enable",
			credentialMode: ChannelMonitorCredentialManual,
			configured:     &trueValue,
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, resolveCreateShowGroupRate(tt.credentialMode, tt.configured))
		})
	}
}

func TestCreateManagedMonitorPersistsResolvedShowGroupRate(t *testing.T) {
	falseValue := false
	tests := []struct {
		name       string
		configured *bool
		want       bool
	}{
		{name: "omitted defaults visible", want: true},
		{name: "explicit false stays hidden", configured: &falseValue, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupID := int64(4)
			repo := &createManagedMonitorRepoStub{}
			svc := NewChannelMonitorService(repo, &duplicateChannelMonitorEncryptor{})
			svc.SetManagedMonitorDependencies(createManagedMonitorKeyStub{}, createManagedMonitorSettingsStub{})

			created, err := svc.Create(context.Background(), ChannelMonitorCreateParams{
				Name:             "managed",
				Provider:         MonitorProviderAnthropic,
				Endpoint:         "",
				PrimaryModel:     "claude-sonnet-4-5",
				GroupID:          &groupID,
				ShowGroupRate:    tt.configured,
				CredentialMode:   ChannelMonitorCredentialManagedLocal,
				Enabled:          true,
				IntervalSeconds:  60,
				MaxProbeAttempts: 3,
				CreatedBy:        1,
			})

			require.NoError(t, err)
			require.NotNil(t, repo.created)
			require.Equal(t, tt.want, repo.created.ShowGroupRate)
			require.Equal(t, tt.want, created.ShowGroupRate)
		})
	}
}
