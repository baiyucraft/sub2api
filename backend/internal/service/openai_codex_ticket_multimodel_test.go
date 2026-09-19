package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func multiModelTicketAccount(id int64) *Account {
	account := ticketTestAccount(id)
	account.Extra[codexAccountTicketConfigKey] = codexAccountTicketConfig{
		Revision: "account-revision",
		Models: map[string]codexTicketModelConfig{
			openAICodexTicketDefaultModel:      {Enabled: true, TicketPlan: codexTicketPlanPro, Revision: "astra-revision"},
			openAICodexTicketDefaultSolModel:   {Enabled: true, TicketPlan: codexTicketPlanTeam, Revision: "sol-revision"},
			openAICodexTicketDefaultTerraModel: {Enabled: true, TicketPlan: codexTicketPlanPro, Revision: "terra-revision"},
		},
	}
	return account
}

func verifiedModelTicket(t *testing.T, account *Account, model string) *openAICodexTicket {
	t.Helper()
	accountCfg := codexAccountTicketConfigOf(account)
	modelCfg, ok := accountCfg.modelConfig(model)
	require.True(t, ok)
	length := codexTicketTargetLength(modelCfg.TicketPlan)
	state := fakeCodexTicketState(length)
	now := time.Now()
	envelope, err := parseCodexTicketEnvelope(state, modelCfg.TicketPlan, now)
	require.NoError(t, err)
	return &openAICodexTicket{
		AccountID: account.ID, Model: model, State: state, Length: len(state),
		CapturedAt: now, IssuedAt: envelope.IssuedAt, ExpiresAt: envelope.ExpiresAt,
		Verified: true, ConfigRevision: modelCfg.Revision,
		FixedProxyFingerprint: codexTicketFixedProxyFingerprint(account), Fingerprint: envelope.Fingerprint,
	}
}

func TestCodexTicketStrictGateIsolatedAcrossThreeModels(t *testing.T) {
	account := multiModelTicketAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)

	astra := verifiedModelTicket(t, account, openAICodexTicketDefaultModel)
	svc.storeOpenAICodexTicket(context.Background(), account, astra)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, openAICodexTicketDefaultModel))
	require.True(t, svc.openAICodexTicketBlocksAccount(account, openAICodexTicketDefaultSolModel))
	require.True(t, svc.openAICodexTicketBlocksAccount(account, openAICodexTicketDefaultTerraModel))
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-5.5"))

	svc.storeOpenAICodexTicket(context.Background(), account, verifiedModelTicket(t, account, openAICodexTicketDefaultSolModel))
	svc.storeOpenAICodexTicket(context.Background(), account, verifiedModelTicket(t, account, openAICodexTicketDefaultTerraModel))
	require.False(t, svc.openAICodexTicketBlocksAccount(account, openAICodexTicketDefaultSolModel))
	require.False(t, svc.openAICodexTicketBlocksAccount(account, openAICodexTicketDefaultTerraModel))
}

func TestCodexTicketActiveReadyPromotionAndVersionGuard(t *testing.T) {
	account := multiModelTicketAccount(41)
	repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = repo

	active := verifiedModelTicket(t, account, openAICodexTicketDefaultModel)
	svc.storeOpenAICodexTicket(context.Background(), account, active)
	ready := verifiedModelTicket(t, account, openAICodexTicketDefaultModel)
	svc.storeOpenAICodexTicket(context.Background(), account, ready)
	slot := svc.lookupOpenAICodexTicketSlot(account, active.Model)
	require.NotNil(t, slot.Active)
	require.NotNil(t, slot.Ready)
	activeReceipt := receiptForCodexTicket(slot.Active)

	svc.recordCodexTicketResponse(activeReceipt, "model_mismatch")
	slot = svc.lookupOpenAICodexTicketSlot(account, active.Model)
	require.Equal(t, 1, slot.Strikes)
	require.Equal(t, active.Fingerprint, slot.Active.Fingerprint)

	svc.recordCodexTicketResponse(activeReceipt, "")
	require.Zero(t, svc.lookupOpenAICodexTicketSlot(account, active.Model).Strikes)

	svc.recordCodexTicketResponse(activeReceipt, "model_mismatch")
	svc.recordCodexTicketResponse(activeReceipt, "state_312")
	slot = svc.lookupOpenAICodexTicketSlot(account, active.Model)
	require.Zero(t, slot.Strikes)
	require.Nil(t, slot.Ready)
	require.Equal(t, ready.Fingerprint, slot.Active.Fingerprint)
	promotedVersion := slot.Active.Version

	// A response bound to the previous immutable version cannot mutate active.
	svc.recordCodexTicketResponse(activeReceipt, "model_mismatch")
	slot = svc.lookupOpenAICodexTicketSlot(account, active.Model)
	require.Zero(t, slot.Strikes)
	require.Equal(t, promotedVersion, slot.Active.Version)
}

func TestCodexTicketLegacyConfigUpgradesWithoutInvalidatingUnchangedModel(t *testing.T) {
	account := ticketTestAccount(41)
	ticket := verifiedTestTicket(account, 292)
	account.Extra[openAICodexTicketExtraKey(ticket.Model)] = ticket
	repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: false}, nil)
	svc.accountRepo = repo

	status, err := svc.ConfigureCodexAccountTicket(context.Background(), account.ID, CodexAccountTicketUpdate{Enabled: true, TicketPlan: codexTicketPlanPro})
	require.NoError(t, err)
	require.Contains(t, status.Models, openAICodexTicketDefaultTerraModel)
	live, err := repo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)
	upgraded := codexAccountTicketConfigOf(live)
	require.NotNil(t, upgraded.Models)
	astraCfg, ok := upgraded.modelConfig(openAICodexTicketDefaultModel)
	require.True(t, ok)
	require.Equal(t, ticket.ConfigRevision, astraCfg.Revision)
	require.NotNil(t, svc.lookupOpenAICodexTicket(live, ticket.Model))
}

func TestCodexTicketClientStateHasInjectionPriority(t *testing.T) {
	account := multiModelTicketAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.storeOpenAICodexTicket(context.Background(), account, verifiedModelTicket(t, account, openAICodexTicketDefaultModel))
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, "client-state")
	receipt, err := svc.applyOpenAICodexTicketWithReceipt(context.Background(), account, openAICodexTicketDefaultModel, header)
	require.NoError(t, err)
	require.Nil(t, receipt)
	require.Equal(t, "client-state", header.Get(openAICodexTurnStateHeader))
}
