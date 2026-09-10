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

// Smoke: native accounts/contacts/tags/notes handlers reject unauthenticated
// requests without needing a database (auth helpers short-circuit first).
func TestAccountsContactsTagsNotes_NativeHTTP_Unauthorized(t *testing.T) {
	t.Parallel()
	app := &handlers.App{}

	cases := []struct {
		name, method, path string
		h                  http.HandlerFunc
	}{
		{"list_accounts", http.MethodGet, "/api/accounts", app.ListAccounts},
		{"create_account", http.MethodPost, "/api/accounts", app.CreateAccount},
		{"get_account", http.MethodGet, "/api/accounts/x", app.GetAccount},
		{"list_contacts", http.MethodGet, "/api/contacts", app.ListContacts},
		{"create_contact", http.MethodPost, "/api/contacts", app.CreateContact},
		{"list_tags", http.MethodGet, "/api/tags", app.ListTags},
		{"create_tag", http.MethodPost, "/api/tags", app.CreateTag},
		{"list_notes", http.MethodGet, "/api/contacts/x/notes", app.ListConversationNotes},
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
