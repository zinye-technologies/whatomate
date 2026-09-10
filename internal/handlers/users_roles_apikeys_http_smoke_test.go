package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Smoke: native users/roles/API-key handlers reject unauthenticated requests
// without needing a database (auth helpers short-circuit first).
func TestUsersRolesAPIKeys_NativeHTTP_Unauthorized(t *testing.T) {
	t.Parallel()
	app := &handlers.App{}

	cases := []struct {
		name, method, path string
		h                  http.HandlerFunc
	}{
		{"list_users", http.MethodGet, "/api/users", app.ListUsers},
		{"create_user", http.MethodPost, "/api/users", app.CreateUser},
		{"get_user", http.MethodGet, "/api/users/" + uuid.New().String(), app.GetUser},
		{"list_roles", http.MethodGet, "/api/roles", app.ListRoles},
		{"create_role", http.MethodPost, "/api/roles", app.CreateRole},
		{"list_api_keys", http.MethodGet, "/api/api-keys", app.ListAPIKeys},
		{"create_api_key", http.MethodPost, "/api/api-keys", app.CreateAPIKey},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			tc.h(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			var env map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.Equal(t, "error", env["status"])
		})
	}
}

func TestGetUser_NativeHTTP_InvalidPathUUID(t *testing.T) {
	t.Parallel()
	app := &handlers.App{}

	r := chi.NewRouter()
	r.Get("/api/users/{id}", app.GetUser)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/users/not-a-uuid", nil)
	// Auth context present so we reach path UUID parsing
	ctx := middleware.WithUserID(req.Context(), uuid.New())
	ctx = middleware.WithOrganizationID(ctx, uuid.New())
	req = req.WithContext(ctx)

	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid user ID")
}
