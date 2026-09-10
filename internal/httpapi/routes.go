package httpapi

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shridarpatil/whatomate/internal/frontend"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
)

// mountRoutes registers public and authenticated routes on the chi router.
// Most handlers remain fastglue.FastRequestHandler values wrapped by Wrap;
// WebSocket and the embedded SPA are native net/http handlers.
func mountRoutes(r chi.Router, d Deps) {
	app := d.App
	lo := d.Log
	cfg := d.Config
	rdb := d.Redis

	// --- Public routes (no auth) ---
	r.Get("/health", Wrap(app.HealthCheck))
	r.Get("/ready", Wrap(app.ReadyCheck))
	r.Get("/api/embedded-signup/config", Wrap(app.GetEmbeddedSignupConfig))

	// Auth routes (public, optionally rate-limited)
	if cfg.RateLimit.Enabled {
		window := time.Duration(cfg.RateLimit.WindowSeconds) * time.Second
		lo.Info("Rate limiting enabled on auth endpoints",
			"login_max", cfg.RateLimit.LoginMaxAttempts,
			"register_max", cfg.RateLimit.RegisterMaxAttempts,
			"refresh_max", cfg.RateLimit.RefreshMaxAttempts,
			"sso_max", cfg.RateLimit.SSOMaxAttempts,
			"window_seconds", cfg.RateLimit.WindowSeconds)

		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.LoginMaxAttempts, Window: window, KeyPrefix: "login", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Post("/api/auth/login", Wrap(app.Login))
		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.RegisterMaxAttempts, Window: window, KeyPrefix: "register", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Post("/api/auth/register", Wrap(app.Register))
		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.RefreshMaxAttempts, Window: window, KeyPrefix: "refresh", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Post("/api/auth/refresh", Wrap(app.RefreshToken))
	} else {
		r.Post("/api/auth/login", Wrap(app.Login))
		r.Post("/api/auth/register", Wrap(app.Register))
		r.Post("/api/auth/refresh", Wrap(app.RefreshToken))
	}
	r.Post("/api/auth/logout", Wrap(app.Logout))

	// SSO routes
	r.Get("/api/auth/sso/providers", Wrap(app.GetPublicSSOProviders))
	if cfg.RateLimit.Enabled {
		window := time.Duration(cfg.RateLimit.WindowSeconds) * time.Second
		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.SSOMaxAttempts, Window: window, KeyPrefix: "sso_init", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Get("/api/auth/sso/{provider}/init", Wrap(app.InitSSO))
		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.SSOMaxAttempts, Window: window, KeyPrefix: "sso_callback", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Get("/api/auth/sso/{provider}/callback", Wrap(app.CallbackSSO))
	} else {
		r.Get("/api/auth/sso/{provider}/init", Wrap(app.InitSSO))
		r.Get("/api/auth/sso/{provider}/callback", Wrap(app.CallbackSSO))
	}

	// Meta webhook + WebSocket (public)
	r.Get("/api/webhook", Wrap(app.WebhookVerify))
	r.Post("/api/webhook", Wrap(app.WebhookHandler))
	r.Get("/ws", app.WebSocketHTTP)

	// Custom action redirect uses one-time token (public)
	r.Get("/api/custom-actions/redirect/{token}", Wrap(app.CustomActionRedirect))

	// --- Authenticated /api routes ---
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthHTTP(app.Config.JWT.Secret, app.DB))
		if cfg.RateLimit.Enabled {
			apiMax := cfg.RateLimit.APIMaxRequests
			if apiMax == 0 {
				apiMax = 200
			}
			apiWindow := cfg.RateLimit.APIWindowSeconds
			if apiWindow == 0 {
				apiWindow = 60
			}
			r.Use(middleware.UserAwareRateLimitHTTP(middleware.RateLimitOpts{
				Redis:      rdb,
				Log:        lo,
				Max:        apiMax,
				Window:     time.Duration(apiWindow) * time.Second,
				KeyPrefix:  "api_global",
				TrustProxy: cfg.RateLimit.TrustProxy,
			}))
		}

		mountAuthenticatedAPI(r, app)
	})

	// Embedded SPA (native net/http)
	if frontend.IsEmbedded() {
		lo.Info("Serving embedded frontend", "base_path", cfg.Server.BasePath)
		spa := frontend.HTTPHandler(cfg.Server.BasePath)
		r.Get("/", spa.ServeHTTP)
		r.Get("/*", spa.ServeHTTP)
	} else {
		lo.Info("Frontend not embedded, API-only mode")
	}

}

func mountAuthenticatedAPI(r chi.Router, app *handlers.App) {
	r.Post("/api/auth/switch-org", Wrap(app.SwitchOrg))
	r.Get("/api/auth/ws-token", Wrap(app.GetWSToken))
	r.Get("/api/me", Wrap(app.GetCurrentUser))
	r.Put("/api/me/settings", Wrap(app.UpdateCurrentUserSettings))
	r.Put("/api/me/password", Wrap(app.ChangePassword))
	r.Put("/api/me/availability", Wrap(app.UpdateAvailability))
	r.Get("/api/me/organizations", Wrap(app.ListMyOrganizations))
	r.Get("/api/users", Wrap(app.ListUsers))
	r.Post("/api/users", Wrap(app.CreateUser))
	r.Get("/api/users/{id}", Wrap(app.GetUser))
	r.Put("/api/users/{id}", Wrap(app.UpdateUser))
	r.Delete("/api/users/{id}", Wrap(app.DeleteUser))
	r.Get("/api/roles", Wrap(app.ListRoles))
	r.Post("/api/roles", Wrap(app.CreateRole))
	r.Get("/api/roles/{id}", Wrap(app.GetRole))
	r.Put("/api/roles/{id}", Wrap(app.UpdateRole))
	r.Delete("/api/roles/{id}", Wrap(app.DeleteRole))
	r.Get("/api/permissions", Wrap(app.ListPermissions))
	r.Get("/api/api-keys", Wrap(app.ListAPIKeys))
	r.Get("/api/api-keys/{id}", Wrap(app.GetAPIKey))
	r.Post("/api/api-keys", Wrap(app.CreateAPIKey))
	r.Put("/api/api-keys/{id}", Wrap(app.UpdateAPIKey))
	r.Delete("/api/api-keys/{id}", Wrap(app.DeleteAPIKey))
	r.Get("/api/accounts", Wrap(app.ListAccounts))
	r.Post("/api/accounts", Wrap(app.CreateAccount))
	r.Post("/api/accounts/exchange-token", Wrap(app.ExchangeToken))
	r.Get("/api/accounts/{id}", Wrap(app.GetAccount))
	r.Put("/api/accounts/{id}", Wrap(app.UpdateAccount))
	r.Delete("/api/accounts/{id}", Wrap(app.DeleteAccount))
	r.Post("/api/accounts/{id}/register", Wrap(app.RegisterPhoneNumber))
	r.Post("/api/accounts/{id}/test", Wrap(app.TestAccountConnection))
	r.Post("/api/accounts/{id}/subscribe", Wrap(app.SubscribeApp))
	r.Get("/api/accounts/{id}/business_profile", Wrap(app.GetBusinessProfile))
	r.Put("/api/accounts/{id}/business_profile", Wrap(app.UpdateBusinessProfile))
	r.Post("/api/accounts/{id}/business_profile/photo", Wrap(app.UpdateProfilePicture))
	r.Get("/api/contacts", Wrap(app.ListContacts))
	r.Post("/api/contacts", Wrap(app.CreateContact))
	r.Get("/api/contacts/{id}", Wrap(app.GetContact))
	r.Put("/api/contacts/{id}", Wrap(app.UpdateContact))
	r.Delete("/api/contacts/{id}", Wrap(app.DeleteContact))
	r.Put("/api/contacts/{id}/assign", Wrap(app.AssignContact))
	r.Put("/api/contacts/{id}/tags", Wrap(app.UpdateContactTags))
	r.Get("/api/contacts/{id}/session-data", Wrap(app.GetContactSessionData))
	r.Post("/api/export", Wrap(app.ExportData))
	r.Post("/api/import", Wrap(app.ImportData))
	r.Get("/api/export/{table}/config", Wrap(app.GetExportConfig))
	r.Get("/api/import/{table}/config", Wrap(app.GetImportConfig))
	r.Get("/api/tags", Wrap(app.ListTags))
	r.Post("/api/tags", Wrap(app.CreateTag))
	r.Put("/api/tags/{name}", Wrap(app.UpdateTag))
	r.Delete("/api/tags/{name}", Wrap(app.DeleteTag))
	r.Get("/api/contacts/{id}/messages", Wrap(app.GetMessages))
	r.Post("/api/contacts/{id}/messages", Wrap(app.SendMessage))
	r.Post("/api/contacts/{id}/mark-read", Wrap(app.MarkContactRead))
	r.Post("/api/contacts/{id}/messages/{message_id}/reaction", Wrap(app.SendReaction))
	r.Post("/api/messages", Wrap(app.SendMessage))
	r.Post("/api/messages/template", Wrap(app.SendTemplateMessage))
	r.Post("/api/messages/media", Wrap(app.SendMediaMessage))
	r.Put("/api/messages/{id}/read", Wrap(app.MarkMessageRead))
	r.Get("/api/contacts/{id}/notes", Wrap(app.ListConversationNotes))
	r.Post("/api/contacts/{id}/notes", Wrap(app.CreateConversationNote))
	r.Put("/api/contacts/{id}/notes/{note_id}", Wrap(app.UpdateConversationNote))
	r.Delete("/api/contacts/{id}/notes/{note_id}", Wrap(app.DeleteConversationNote))
	r.Get("/api/media/{message_id}", Wrap(app.ServeMedia))
	r.Get("/api/templates", Wrap(app.ListTemplates))
	r.Post("/api/templates", Wrap(app.CreateTemplate))
	r.Get("/api/templates/{id}", Wrap(app.GetTemplate))
	r.Put("/api/templates/{id}", Wrap(app.UpdateTemplate))
	r.Delete("/api/templates/{id}", Wrap(app.DeleteTemplate))
	r.Post("/api/templates/sync", Wrap(app.SyncTemplates))
	r.Post("/api/templates/{id}/publish", Wrap(app.SubmitTemplate))
	r.Post("/api/templates/upload-media", Wrap(app.UploadTemplateMedia))
	r.Get("/api/flows", Wrap(app.ListFlows))
	r.Post("/api/flows", Wrap(app.CreateFlow))
	r.Get("/api/flows/{id}", Wrap(app.GetFlow))
	r.Put("/api/flows/{id}", Wrap(app.UpdateFlow))
	r.Delete("/api/flows/{id}", Wrap(app.DeleteFlow))
	r.Post("/api/flows/{id}/save-to-meta", Wrap(app.SaveFlowToMeta))
	r.Post("/api/flows/{id}/publish", Wrap(app.PublishFlow))
	r.Post("/api/flows/{id}/deprecate", Wrap(app.DeprecateFlow))
	r.Post("/api/flows/{id}/duplicate", Wrap(app.DuplicateFlow))
	r.Post("/api/flows/sync", Wrap(app.SyncFlows))
	r.Get("/api/campaigns", Wrap(app.ListCampaigns))
	r.Post("/api/campaigns", Wrap(app.CreateCampaign))
	r.Get("/api/campaigns/{id}", Wrap(app.GetCampaign))
	r.Put("/api/campaigns/{id}", Wrap(app.UpdateCampaign))
	r.Delete("/api/campaigns/{id}", Wrap(app.DeleteCampaign))
	r.Post("/api/campaigns/{id}/start", Wrap(app.StartCampaign))
	r.Post("/api/campaigns/{id}/pause", Wrap(app.PauseCampaign))
	r.Post("/api/campaigns/{id}/cancel", Wrap(app.CancelCampaign))
	r.Post("/api/campaigns/{id}/retry-failed", Wrap(app.RetryFailed))
	r.Get("/api/campaigns/{id}/progress", Wrap(app.GetCampaign))
	r.Post("/api/campaigns/{id}/recipients/import", Wrap(app.ImportRecipients))
	r.Get("/api/campaigns/{id}/recipients", Wrap(app.GetCampaignRecipients))
	r.Delete("/api/campaigns/{id}/recipients/{recipientId}", Wrap(app.DeleteCampaignRecipient))
	r.Post("/api/campaigns/{id}/media", Wrap(app.UploadCampaignMedia))
	r.Get("/api/campaigns/{id}/media", Wrap(app.ServeCampaignMedia))
	r.Get("/api/chatbot/settings", Wrap(app.GetChatbotSettings))
	r.Put("/api/chatbot/settings", Wrap(app.UpdateChatbotSettings))
	r.Get("/api/chatbot/keywords", Wrap(app.ListKeywordRules))
	r.Post("/api/chatbot/keywords", Wrap(app.CreateKeywordRule))
	r.Get("/api/chatbot/keywords/{id}", Wrap(app.GetKeywordRule))
	r.Put("/api/chatbot/keywords/{id}", Wrap(app.UpdateKeywordRule))
	r.Delete("/api/chatbot/keywords/{id}", Wrap(app.DeleteKeywordRule))
	r.Get("/api/chatbot/flows", Wrap(app.ListChatbotFlows))
	r.Post("/api/chatbot/flows", Wrap(app.CreateChatbotFlow))
	r.Get("/api/chatbot/flows/{id}", Wrap(app.GetChatbotFlow))
	r.Put("/api/chatbot/flows/{id}", Wrap(app.UpdateChatbotFlow))
	r.Delete("/api/chatbot/flows/{id}", Wrap(app.DeleteChatbotFlow))
	r.Get("/api/chatbot/ai-contexts", Wrap(app.ListAIContexts))
	r.Post("/api/chatbot/ai-contexts", Wrap(app.CreateAIContext))
	r.Get("/api/chatbot/ai-contexts/{id}", Wrap(app.GetAIContext))
	r.Put("/api/chatbot/ai-contexts/{id}", Wrap(app.UpdateAIContext))
	r.Delete("/api/chatbot/ai-contexts/{id}", Wrap(app.DeleteAIContext))
	r.Get("/api/chatbot/transfers", Wrap(app.ListAgentTransfers))
	r.Post("/api/chatbot/transfers", Wrap(app.CreateAgentTransfer))
	r.Post("/api/chatbot/transfers/pick", Wrap(app.PickNextTransfer))
	r.Put("/api/chatbot/transfers/{id}/resume", Wrap(app.ResumeFromTransfer))
	r.Put("/api/chatbot/transfers/{id}/assign", Wrap(app.AssignAgentTransfer))
	r.Get("/api/teams", Wrap(app.ListTeams))
	r.Post("/api/teams", Wrap(app.CreateTeam))
	r.Get("/api/teams/{id}", Wrap(app.GetTeam))
	r.Put("/api/teams/{id}", Wrap(app.UpdateTeam))
	r.Delete("/api/teams/{id}", Wrap(app.DeleteTeam))
	r.Get("/api/teams/{id}/members", Wrap(app.ListTeamMembers))
	r.Post("/api/teams/{id}/members", Wrap(app.AddTeamMember))
	r.Delete("/api/teams/{id}/members/{member_user_id}", Wrap(app.RemoveTeamMember))
	r.Get("/api/audit-logs", Wrap(app.ListAuditLogs))
	r.Get("/api/audit-logs/{id}", Wrap(app.GetAuditLog))
	r.Get("/api/canned-responses", Wrap(app.ListCannedResponses))
	r.Post("/api/canned-responses", Wrap(app.CreateCannedResponse))
	r.Get("/api/canned-responses/{id}", Wrap(app.GetCannedResponse))
	r.Put("/api/canned-responses/{id}", Wrap(app.UpdateCannedResponse))
	r.Delete("/api/canned-responses/{id}", Wrap(app.DeleteCannedResponse))
	r.Post("/api/canned-responses/{id}/use", Wrap(app.IncrementCannedResponseUsage))
	r.Get("/api/chatbot/sessions", Wrap(app.ListChatbotSessions))
	r.Get("/api/chatbot/sessions/{id}", Wrap(app.GetChatbotSession))
	r.Get("/api/analytics/dashboard", Wrap(app.GetDashboardStats))
	r.Get("/api/analytics/messages", Wrap(app.GetMessageAnalytics))
	r.Get("/api/analytics/chatbot", Wrap(app.GetChatbotAnalytics))
	r.Get("/api/analytics/agents", Wrap(app.GetAgentAnalytics))
	r.Get("/api/analytics/agents/{id}", Wrap(app.GetAgentDetails))
	r.Get("/api/analytics/agents/comparison", Wrap(app.GetAgentComparison))
	r.Get("/api/analytics/meta", Wrap(app.GetMetaAnalytics))
	r.Get("/api/analytics/meta/accounts", Wrap(app.ListMetaAccountsForAnalytics))
	r.Post("/api/analytics/meta/refresh", Wrap(app.RefreshMetaAnalyticsCache))
	r.Get("/api/widgets", Wrap(app.ListWidgets))
	r.Post("/api/widgets", Wrap(app.CreateWidget))
	r.Get("/api/widgets/data-sources", Wrap(app.GetWidgetDataSources))
	r.Get("/api/widgets/data", Wrap(app.GetAllWidgetsData))
	r.Get("/api/widgets/{id}", Wrap(app.GetWidget))
	r.Put("/api/widgets/{id}", Wrap(app.UpdateWidget))
	r.Delete("/api/widgets/{id}", Wrap(app.DeleteWidget))
	r.Get("/api/widgets/{id}/data", Wrap(app.GetWidgetData))
	r.Post("/api/widgets/layout", Wrap(app.SaveWidgetLayout))
	r.Get("/api/org/settings", Wrap(app.GetOrganizationSettings))
	r.Put("/api/org/settings", Wrap(app.UpdateOrganizationSettings))
	r.Post("/api/org/audio", Wrap(app.UploadOrgAudio))
	r.Get("/api/organizations", Wrap(app.ListOrganizations))
	r.Post("/api/organizations", Wrap(app.CreateOrganization))
	r.Get("/api/organizations/current", Wrap(app.GetCurrentOrganization))
	r.Get("/api/organizations/members", Wrap(app.ListOrganizationMembers))
	r.Post("/api/organizations/members", Wrap(app.AddOrganizationMember))
	r.Put("/api/organizations/members/{member_id}", Wrap(app.UpdateOrganizationMemberRole))
	r.Delete("/api/organizations/members/{member_id}", Wrap(app.RemoveOrganizationMember))
	r.Get("/api/settings/sso", Wrap(app.GetSSOSettings))
	r.Put("/api/settings/sso/{provider}", Wrap(app.UpdateSSOProvider))
	r.Delete("/api/settings/sso/{provider}", Wrap(app.DeleteSSOProvider))
	r.Get("/api/webhooks", Wrap(app.ListWebhooks))
	r.Post("/api/webhooks", Wrap(app.CreateWebhook))
	r.Get("/api/webhooks/{id}", Wrap(app.GetWebhook))
	r.Put("/api/webhooks/{id}", Wrap(app.UpdateWebhook))
	r.Delete("/api/webhooks/{id}", Wrap(app.DeleteWebhook))
	r.Post("/api/webhooks/{id}/test", Wrap(app.TestWebhook))
	r.Get("/api/custom-actions", Wrap(app.ListCustomActions))
	r.Post("/api/custom-actions", Wrap(app.CreateCustomAction))
	r.Get("/api/custom-actions/{id}", Wrap(app.GetCustomAction))
	r.Put("/api/custom-actions/{id}", Wrap(app.UpdateCustomAction))
	r.Delete("/api/custom-actions/{id}", Wrap(app.DeleteCustomAction))
	r.Post("/api/custom-actions/{id}/execute", Wrap(app.ExecuteCustomAction))
	r.Get("/api/ivr-flows", Wrap(app.ListIVRFlows))
	r.Get("/api/ivr-flows/{id}", Wrap(app.GetIVRFlow))
	r.Post("/api/ivr-flows", Wrap(app.CreateIVRFlow))
	r.Put("/api/ivr-flows/{id}", Wrap(app.UpdateIVRFlow))
	r.Delete("/api/ivr-flows/{id}", Wrap(app.DeleteIVRFlow))
	r.Post("/api/ivr-flows/audio", Wrap(app.UploadIVRAudio))
	r.Get("/api/ivr-flows/audio/{filename}", Wrap(app.ServeIVRAudio))
	r.Get("/api/call-logs", Wrap(app.ListCallLogs))
	r.Get("/api/call-logs/{id}", Wrap(app.GetCallLog))
	r.Get("/api/call-logs/{id}/recording", Wrap(app.GetCallRecording))
	r.Get("/api/call-transfers", Wrap(app.ListCallTransfers))
	r.Get("/api/call-transfers/{id}", Wrap(app.GetCallTransfer))
	r.Post("/api/call-transfers/{id}/connect", Wrap(app.ConnectCallTransfer))
	r.Post("/api/call-transfers/{id}/hangup", Wrap(app.HangupCallTransfer))
	r.Post("/api/call-transfers/initiate", Wrap(app.InitiateAgentTransfer))
	r.Post("/api/call-logs/{id}/hold", Wrap(app.HoldCall))
	r.Post("/api/call-logs/{id}/resume", Wrap(app.ResumeCall))
	r.Post("/api/calls/outgoing", Wrap(app.InitiateOutgoingCall))
	r.Post("/api/calls/outgoing/{id}/hangup", Wrap(app.HangupOutgoingCall))
	r.Post("/api/calls/permission-request", Wrap(app.SendCallPermissionRequest))
	r.Get("/api/calls/permission/{contactId}", Wrap(app.GetCallPermission))
	r.Get("/api/calls/ice-servers", Wrap(app.GetICEServers))
	r.Get("/api/catalogs", Wrap(app.ListCatalogs))
	r.Post("/api/catalogs", Wrap(app.CreateCatalog))
	r.Get("/api/catalogs/{id}", Wrap(app.GetCatalog))
	r.Delete("/api/catalogs/{id}", Wrap(app.DeleteCatalog))
	r.Post("/api/catalogs/sync", Wrap(app.SyncCatalogs))
	r.Get("/api/catalogs/{id}/products", Wrap(app.ListCatalogProducts))
	r.Post("/api/catalogs/{id}/products", Wrap(app.CreateCatalogProduct))
	r.Get("/api/products/{id}", Wrap(app.GetCatalogProduct))
	r.Put("/api/products/{id}", Wrap(app.UpdateCatalogProduct))
	r.Delete("/api/products/{id}", Wrap(app.DeleteCatalogProduct))
}
