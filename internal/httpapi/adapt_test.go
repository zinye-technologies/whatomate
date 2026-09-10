package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"

	"github.com/shridarpatil/whatomate/internal/httpapi"
)

func TestWrap_ProjectsJSONBodyAndPathParam(t *testing.T) {
	t.Parallel()

	h := httpapi.Wrap(func(r *fastglue.Request) error {
		id, _ := r.RequestCtx.UserValue("id").(string)
		require.Equal(t, "abc-123", id)
		require.Equal(t, "POST", string(r.RequestCtx.Method()))
		require.Contains(t, string(r.RequestCtx.PostBody()), `"hello"`)
		return r.SendEnvelope(map[string]string{"id": id})
	})

	r := chi.NewRouter()
	r.Post("/api/items/{id}", h)

	req := httptest.NewRequest(http.MethodPost, "/api/items/abc-123", strings.NewReader(`{"hello":"world"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var env struct {
		Status string            `json:"status"`
		Data   map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, "success", env.Status)
	require.Equal(t, "abc-123", env.Data["id"])
}
