package service

import (
	"context"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestTrimUpstreamNameWhitespace(t *testing.T) {
	allWhitespace := "\u0009\u000a\u000b\u000c\u000d\u0020\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"
	require.Equal(t, "配置 Key", trimUpstreamNameWhitespace(allWhitespace+"配置 Key"+allWhitespace))
	require.Equal(t, "内\u00a0部", trimUpstreamNameWhitespace("内\u00a0部"))
	require.Equal(t, "\u200bvalue\u200b", trimUpstreamNameWhitespace("\u200bvalue\u200b"))
}

func TestBuildUpstreamAccountName(t *testing.T) {
	tests := []struct {
		name       string
		configName string
		keyName    string
		want       string
	}{
		{name: "short", configName: "配置", keyName: "Key", want: "配置-Key"},
		{name: "edge whitespace", configName: "\t Config \r", keyName: "\u00a0Key\u3000", want: "Config-Key"},
		{name: "internal whitespace", configName: "Config Name", keyName: "Key\u00a0Name", want: "Config Name-Key\u00a0Name"},
		{name: "both long", configName: strings.Repeat("配", 60), keyName: strings.Repeat("😀", 60), want: strings.Repeat("配", 49) + "-" + strings.Repeat("😀", 50)},
		{name: "config long", configName: strings.Repeat("c", 120), keyName: "key", want: strings.Repeat("c", 96) + "-key"},
		{name: "key long", configName: "配置😀", keyName: strings.Repeat("🔑", 120), want: "配置😀-" + strings.Repeat("🔑", 96)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildUpstreamAccountName(tt.configName, tt.keyName)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len([]rune(got)), upstreamAccountNameMaxRunes)
		})
	}
}

func TestNormalizeUpstreamAccountInputNameBehavior(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, " 配置名 ", UpstreamProviderNewAPI, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: cfgID,
			Name:             "\u3000Key名\u00a0",
			Platform:         PlatformOpenAI,
		}},
	}
	svc := &adminServiceImpl{upstreamConfigRepo: repo}

	t.Run("blank name generates authoritative name", func(t *testing.T) {
		input := &CreateAccountInput{
			Name:             "\u00a0\t",
			Type:             AccountTypeAPIKey,
			Platform:         PlatformOpenAI,
			UpstreamConfigID: &cfgID,
			UpstreamKeyID:    &keyID,
		}

		require.NoError(t, svc.normalizeUpstreamAccountInput(context.Background(), input))
		require.Equal(t, "配置名-Key名", input.Name)
	})

	t.Run("nonblank name is replaced by authoritative name", func(t *testing.T) {
		input := &CreateAccountInput{
			Name:             " custom name ",
			Type:             AccountTypeAPIKey,
			Platform:         PlatformOpenAI,
			UpstreamConfigID: &cfgID,
			UpstreamKeyID:    &keyID,
		}

		require.NoError(t, svc.normalizeUpstreamAccountInput(context.Background(), input))
		require.Equal(t, "配置名-Key名", input.Name)
	})
}

func TestNormalizeUpstreamAccountUpdateUsesAuthoritativeName(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "可达鸭", UpstreamProviderSub2API, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: cfgID,
			Name:             "pro",
			Platform:         PlatformOpenAI,
		}},
	}
	svc := &adminServiceImpl{upstreamConfigRepo: repo}
	account := &Account{
		ID:               1,
		Name:             "可达鸭pro",
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
	}
	input := &UpdateAccountInput{Name: "手工名称"}

	require.NoError(t, svc.normalizeUpstreamAccountUpdate(context.Background(), account, input))
	require.Equal(t, "可达鸭-pro", account.Name)
}

func TestNormalizeUpstreamAccountInputRejectsInvalidNamesAndMismatchedKey(t *testing.T) {
	cfgID := int64(10)
	otherCfgID := int64(11)
	keyID := int64(20)

	tests := []struct {
		name       string
		configName string
		keyName    string
		keyCfgID   int64
		wantReason string
	}{
		{name: "blank config name", configName: "\u3000\t", keyName: "key", keyCfgID: cfgID, wantReason: "UPSTREAM_CONFIG_NAME_REQUIRED"},
		{name: "blank key name", configName: "config", keyName: "\u00a0\u202f", keyCfgID: cfgID, wantReason: "UPSTREAM_KEY_NAME_REQUIRED"},
		{name: "key config mismatch", configName: "config", keyName: "key", keyCfgID: otherCfgID, wantReason: "UPSTREAM_KEY_CONFIG_MISMATCH"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &upstreamConfigServiceRepo{
				configs: []UpstreamConfig{testUpstreamConfig(cfgID, tt.configName, UpstreamProviderNewAPI, StatusActive, "https://upstream.example.com")},
				keys:    []UpstreamKey{{ID: keyID, UpstreamConfigID: tt.keyCfgID, Name: tt.keyName, Platform: PlatformOpenAI}},
			}
			svc := &adminServiceImpl{upstreamConfigRepo: repo}
			input := &CreateAccountInput{
				Name:             "custom",
				Type:             AccountTypeAPIKey,
				Platform:         PlatformOpenAI,
				UpstreamConfigID: &cfgID,
				UpstreamKeyID:    &keyID,
			}

			err := svc.normalizeUpstreamAccountInput(context.Background(), input)
			require.Error(t, err)
			require.Equal(t, tt.wantReason, infraerrors.Reason(err))
			require.Equal(t, 400, infraerrors.Code(err))
		})
	}
}

func TestNormalizeUpstreamAccountInputRejectsLegacyUpstreamType(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "config", UpstreamProviderNewAPI, StatusActive, "https://upstream.example.com")},
		keys:    []UpstreamKey{{ID: keyID, UpstreamConfigID: cfgID, Name: "key", Platform: PlatformOpenAI}},
	}
	svc := &adminServiceImpl{upstreamConfigRepo: repo}

	err := svc.normalizeUpstreamAccountInput(context.Background(), &CreateAccountInput{
		Type:             AccountTypeUpstream,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
	})

	require.Error(t, err)
	require.Equal(t, "UPSTREAM_ACCOUNT_TYPE_INVALID", infraerrors.Reason(err))
}

func TestNormalizeUpstreamAccountInputRequiresCompleteBinding(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	svc := &adminServiceImpl{}

	for _, tt := range []struct {
		name     string
		configID *int64
		keyID    *int64
	}{
		{name: "config only", configID: &cfgID},
		{name: "key only", keyID: &keyID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.normalizeUpstreamAccountInput(context.Background(), &CreateAccountInput{
				Type:             AccountTypeAPIKey,
				Platform:         PlatformOpenAI,
				UpstreamConfigID: tt.configID,
				UpstreamKeyID:    tt.keyID,
			})

			require.Error(t, err)
			require.Equal(t, "UPSTREAM_ACCOUNT_BINDING_REQUIRED", infraerrors.Reason(err))
		})
	}
}

func TestNormalizeUpstreamAccountUpdateValidatesFinalTypeAndBinding(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	svc := &adminServiceImpl{}

	err := svc.normalizeUpstreamAccountUpdate(context.Background(), &Account{
		Type:             AccountTypeUpstream,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
	}, &UpdateAccountInput{})

	require.Error(t, err)
	require.Equal(t, "UPSTREAM_ACCOUNT_TYPE_INVALID", infraerrors.Reason(err))
}

func TestNormalizeUpstreamAccountRejectsAndPreservesStaleBindings(t *testing.T) {
	cfgID := int64(10)
	staleKeyID := int64(20)
	otherStaleKeyID := int64(21)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "config", UpstreamProviderSub2API, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{
			{ID: staleKeyID, UpstreamConfigID: cfgID, Name: "stale", Platform: PlatformOpenAI, Status: UpstreamKeyStatusStale},
			{ID: otherStaleKeyID, UpstreamConfigID: cfgID, Name: "other-stale", Platform: PlatformOpenAI, Status: UpstreamKeyStatusStale},
		},
	}
	svc := &adminServiceImpl{upstreamConfigRepo: repo}

	createErr := svc.normalizeUpstreamAccountInput(context.Background(), &CreateAccountInput{
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &staleKeyID,
	})
	require.Error(t, createErr)
	require.Equal(t, "UPSTREAM_KEY_INACTIVE", infraerrors.Reason(createErr))

	account := &Account{
		ID:               1,
		Name:             "config-stale",
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &staleKeyID,
	}
	require.NoError(t, svc.normalizeUpstreamAccountUpdate(context.Background(), account, &UpdateAccountInput{}))
	require.Equal(t, "config-stale", account.Name)

	switchErr := svc.normalizeUpstreamAccountUpdate(context.Background(), account, &UpdateAccountInput{
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &otherStaleKeyID,
	})
	require.Error(t, switchErr)
	require.Equal(t, "UPSTREAM_KEY_INACTIVE", infraerrors.Reason(switchErr))
}

type accountNameCreateRepo struct {
	AccountRepository
	createCalls int
}

func (r *accountNameCreateRepo) Create(_ context.Context, _ *Account) error {
	r.createCalls++
	return nil
}

func TestAdminServiceCreateAccountRejectsBlankOrdinaryNameBeforePersistence(t *testing.T) {
	repo := &accountNameCreateRepo{}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "\u00a0\u3000\t",
		Type:                 AccountTypeAPIKey,
		Platform:             PlatformOpenAI,
		SkipDefaultGroupBind: true,
	})

	require.Error(t, err)
	require.Equal(t, "ACCOUNT_NAME_REQUIRED", infraerrors.Reason(err))
	require.Equal(t, 400, infraerrors.Code(err))
	require.Zero(t, repo.createCalls)
}
