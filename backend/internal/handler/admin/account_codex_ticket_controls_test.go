package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexTicketControlStub struct {
	updated        int
	input          service.CodexAccountTicketUpdate
	harvested      int
	harvestAccount int64
	harvestModels  []string
	err            error
}

func (s *codexTicketControlStub) GetCodexAccountTicketStatus(context.Context, int64) (*service.CodexAccountTicketStatus, error) {
	return &service.CodexAccountTicketStatus{}, s.err
}
func (s *codexTicketControlStub) ConfigureCodexAccountTicket(_ context.Context, _ int64, in service.CodexAccountTicketUpdate) (*service.CodexAccountTicketStatus, error) {
	s.updated++
	s.input = in
	return &service.CodexAccountTicketStatus{}, s.err
}
func (s *codexTicketControlStub) HarvestCodexAccountTicket(_ context.Context, accountID int64, models ...string) (*service.CodexAccountTicketStatus, error) {
	s.harvested++
	s.harvestAccount = accountID
	s.harvestModels = append([]string(nil), models...)
	return &service.CodexAccountTicketStatus{}, s.err
}

func codexTicketControlRequest(t *testing.T, stub *codexTicketControlStub, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &AccountHandler{codexAccountTickets: stub}
	r := gin.New()
	r.PUT("/accounts/:id/codex-ticket", h.UpdateCodexAccountTicket)
	q := httptest.NewRequest(http.MethodPut, "/accounts/4/codex-ticket", strings.NewReader(body))
	q.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	return w
}

func codexTicketHarvestRequest(t *testing.T, stub *codexTicketControlStub, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &AccountHandler{codexAccountTickets: stub}
	r := gin.New()
	r.POST("/accounts/:id/codex-ticket/harvest", h.HarvestCodexAccountTicket)
	q := httptest.NewRequest(http.MethodPost, "/accounts/4/codex-ticket/harvest", strings.NewReader(body))
	q.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	return w
}

func TestCodexTicketControlsRejectMalformedSettingsWithoutEchoingCredentials(t *testing.T) {
	for _, body := range []string{`{"proxy_url":"socks5://user:canary-secret@host:1080"}`, `{"enabled":"canary-secret"}`, `{"enabled":true,"proxy_url":"` + strings.Repeat("canary-secret", 2048) + `"}`} {
		s := &codexTicketControlStub{}
		w := codexTicketControlRequest(t, s, body)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.NotContains(t, w.Body.String(), "canary-secret")
		require.Zero(t, s.updated)
	}
}

func TestCodexTicketControlsDoNotExposeInternalErrors(t *testing.T) {
	s := &codexTicketControlStub{err: errors.New("database update socks5://user:canary-secret@host:1080")}
	w := codexTicketControlRequest(t, s, `{"enabled":false}`)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), "canary-secret")
}

func TestCodexTicketControlsPreserveBlankProxyIntent(t *testing.T) {
	s := &codexTicketControlStub{}
	w := codexTicketControlRequest(t, s, `{"enabled":false,"proxy_url":""}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, s.updated)
	require.Empty(t, s.input.ProxyURL)
	require.False(t, s.input.ClearProxy)
}

func TestCodexTicketControlsPassManualPlan(t *testing.T) {
	s := &codexTicketControlStub{}
	w := codexTicketControlRequest(t, s, `{"enabled":false,"ticket_plan":"team"}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "team", s.input.TicketPlan)
	w = codexTicketControlRequest(t, s, `{"enabled":false,"ticket_plan":332}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, 1, s.updated)
}

func TestCodexTicketControlsPassModelsToService(t *testing.T) {
	s := &codexTicketControlStub{}
	w := codexTicketControlRequest(t, s, "{\"models\":{\"gpt-6-astra\":{\"enabled\":true,\"ticket_plan\":\"pro\"},\"gpt-5.6-sol\":{\"enabled\":false,\"ticket_plan\":\"team\"},\"gpt-5.6-terra\":{\"enabled\":true,\"ticket_plan\":\"team\"}}}")

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, s.updated)
	require.Equal(t, map[string]service.CodexTicketModelUpdate{
		"gpt-6-astra":   {Enabled: true, TicketPlan: "pro"},
		"gpt-5.6-sol":   {Enabled: false, TicketPlan: "team"},
		"gpt-5.6-terra": {Enabled: true, TicketPlan: "team"},
	}, s.input.Models)
}

func TestCodexTicketControlsHarvestRequiresModel(t *testing.T) {
	s := &codexTicketControlStub{}
	w := codexTicketHarvestRequest(t, s, "{}")

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, s.harvested)
}

func TestCodexTicketControlsHarvestPassesModelToService(t *testing.T) {
	s := &codexTicketControlStub{}
	w := codexTicketHarvestRequest(t, s, "{\"model\":\"gpt-5.6-terra\"}")

	require.Equal(t, http.StatusAccepted, w.Code)
	require.Equal(t, 1, s.harvested)
	require.Equal(t, int64(4), s.harvestAccount)
	require.Equal(t, []string{"gpt-5.6-terra"}, s.harvestModels)
}
