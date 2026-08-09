package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetBatchTodayStatsEmptyIDsReturnsBothMaps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/today-stats/batch", bytes.NewBufferString(`{"account_ids":[]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&AccountHandler{}).GetBatchTodayStats(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data struct {
			Stats           map[string]any `json:"stats"`
			UpstreamBilling map[string]any `json:"upstream_billing"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Empty(t, payload.Data.Stats)
	require.Empty(t, payload.Data.UpstreamBilling)
}
