package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGroupHandlerOptionalImageToolsMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	h := NewGroupHandler(svc, nil, nil)
	r := gin.New()
	r.POST("/groups", h.Create)
	r.PUT("/groups/:id", h.Update)
	request := func(method, url, body string) {
		req := httptest.NewRequest(method, url, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		r.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	}
	request(http.MethodPost, "/groups", `{"name":"image-tools","platform":"openai","strip_optional_image_tools":true}`)
	require.Len(t, svc.createdGroups, 1)
	require.True(t, svc.createdGroups[0].StripOptionalImageTools)

	request(http.MethodPut, "/groups/2", `{"strip_optional_image_tools":false}`)
	require.Len(t, svc.updatedGroups, 1)
	require.NotNil(t, svc.updatedGroups[0].StripOptionalImageTools)
	require.False(t, *svc.updatedGroups[0].StripOptionalImageTools)

	request(http.MethodPut, "/groups/2", `{}`)
	require.Len(t, svc.updatedGroups, 2)
	require.Nil(t, svc.updatedGroups[1].StripOptionalImageTools)
}
