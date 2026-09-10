package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestLogout_NativeHTTP_NoToken(t *testing.T) {
	t.Parallel()
	app := &handlers.App{
		Config: &config.Config{
			JWT:    config.JWTConfig{AccessExpiryMins: 15, RefreshExpiryDays: 7},
			Cookie: config.CookieConfig{},
			Server: config.ServerConfig{},
		},
	}

	// Direct httptest path
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	app.Logout(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	cookies := rec.Result().Cookies()
	names := map[string]bool{}
	for _, c := range cookies {
		names[c.Name] = true
		assert.Equal(t, -1, c.MaxAge)
	}
	assert.True(t, names["whm_access"])
	assert.True(t, names["whm_refresh"])
	assert.True(t, names["whm_csrf"])

	// InvokeHTTP bridge path (fasthttp test harness)
	fg := testutil.NewRequest(t)
	testutil.InvokeHTTP(t, app.Logout, fg)
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(fg))
	// Cleared cookies should appear as Set-Cookie (value empty / Max-Age=-1)
	assert.Contains(t, string(testutil.GetResponseBody(fg)), "logged_out")
}
