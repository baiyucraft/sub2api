package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRetiredCodexTicketSettingsAreIgnoredAndPrivate(t *testing.T) {
	const proxyKey = "openai_codex_ticket_harvest_proxy_url"
	const enabledKey = "openai_codex_ticket_enabled"
	const oldSecret = "retired-state-stored-fixture-token-4b197e"
	const newSecret = "retired-state-updated-fixture-token-692f8a"
	const invalidSecret = "retired-state-invalid-fixture-token-f03c51"
	const oldProxy = "http://user:" + oldSecret + "@old.example.com:8080"
	assertPrivate := func(body string) {
		t.Helper()
		for _, secret := range []string{oldSecret, newSecret, invalidSecret} {
			require.NotContains(t, body, secret)
		}
		require.NotContains(t, body, "openai_codex_ticket")
	}
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{proxyKey: oldProxy, enabledKey: "true"})
	for _, body := range []map[string]any{
		{proxyKey: "socks5h://user:" + newSecret + "@new.example.com:1080", enabledKey: false},
		{proxyKey: "ftp://user:" + invalidSecret + "@proxy.example.com:21"},
		{"site_name": "updated"},
	} {
		rec := doUpdateSettings(t, h, body, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, oldProxy, repo.values[proxyKey])
		require.Equal(t, "true", repo.values[enabledKey])
		assertPrivate(rec.Body.String())
	}
	get := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(get)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, get.Code)
	assertPrivate(get.Body.String())
}
