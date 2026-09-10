package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthCheck_NativeHTTP(t *testing.T) {
	t.Parallel()
	app := &handlers.App{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	app.HealthCheck(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Status string            `json:"status"`
		Data   map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "success", resp.Status)
	assert.Equal(t, "ok", resp.Data["status"])
	assert.Equal(t, "whatomate", resp.Data["service"])
}
