package httpapi

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shridarpatil/whatomate/internal/frontend"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
)

// mountRoutes registers public and authenticated routes on the chi router.
// All mounted handlers are native net/http (+ chi).
func mountRoutes(r chi.Router, d Deps) {
	app := d.App
	lo := d.Log
	cfg := d.Config
	rdb := d.Redis

	// --- Public routes (no auth) ---
	r.Get("/health", app.HealthCheck)
	r.Get("/ready", app.ReadyCheck)
	r.Get("/api/embedded-signup/config", app.GetEmbeddedSignupConfig)

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
		})).Post("/api/auth/login", app.Login)
		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.RegisterMaxAttempts, Window: window, KeyPrefix: "register", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Post("/api/auth/register", app.Register)
		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.RefreshMaxAttempts, Window: window, KeyPrefix: "refresh", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Post("/api/auth/refresh", app.RefreshToken)
	} else {
		r.Post("/api/auth/login", app.Login)
		r.Post("/api/auth/register", app.Register)
		r.Post("/api/auth/refresh", app.RefreshToken)
	}
	r.Post("/api/auth/logout", app.Logout)

	// SSO routes
	r.Get("/api/auth/sso/providers", app.GetPublicSSOProviders)
	if cfg.RateLimit.Enabled {
		window := time.Duration(cfg.RateLimit.WindowSeconds) * time.Second
		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.SSOMaxAttempts, Window: window, KeyPrefix: "sso_init", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Get("/api/auth/sso/{provider}/init", app.InitSSO)
		r.With(middleware.RateLimitHTTP(middleware.RateLimitOpts{
			Redis: rdb, Log: lo, Max: cfg.RateLimit.SSOMaxAttempts, Window: window, KeyPrefix: "sso_callback", TrustProxy: cfg.RateLimit.TrustProxy,
		})).Get("/api/auth/sso/{provider}/callback", app.CallbackSSO)
	} else {
		r.Get("/api/auth/sso/{provider}/init", app.InitSSO)
		r.Get("/api/auth/sso/{provider}/callback", app.CallbackSSO)
	}

	// Meta webhook + WebSocket (public)
	r.Get("/api/webhook", app.WebhookVerify)
	r.Post("/api/webhook", app.WebhookHandler)
	r.Get("/ws", app.WebSocketHTTP)

	// Custom action redirect uses one-time token (public)
	r.Get("/api/custom-actions/redirect/{token}", app.CustomActionRedirect)

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
	r.Post("/api/auth/switch-org", app.SwitchOrg)
	r.Get("/api/auth/ws-token", app.GetWSToken)
	r.Get("/api/me", app.GetCurrentUser)
	r.Put("/api/me/settings", app.UpdateCurrentUserSettings)
	r.Put("/api/me/password", app.ChangePassword)
	r.Put("/api/me/availability", app.UpdateAvailability)
	r.Get("/api/me/organizations", app.ListMyOrganizations)
	r.Get("/api/users", app.ListUsers)
	r.Post("/api/users", app.CreateUser)
	r.Get("/api/users/{id}", app.GetUser)
	r.Put("/api/users/{id}", app.UpdateUser)
	r.Delete("/api/users/{id}", app.DeleteUser)
	r.Get("/api/roles", app.ListRoles)
	r.Post("/api/roles", app.CreateRole)
	r.Get("/api/roles/{id}", app.GetRole)
	r.Put("/api/roles/{id}", app.UpdateRole)
	r.Delete("/api/roles/{id}", app.DeleteRole)
	r.Get("/api/permissions", app.ListPermissions)
	r.Get("/api/api-keys", app.ListAPIKeys)
	r.Get("/api/api-keys/{id}", app.GetAPIKey)
	r.Post("/api/api-keys", app.CreateAPIKey)
	r.Put("/api/api-keys/{id}", app.UpdateAPIKey)
	r.Delete("/api/api-keys/{id}", app.DeleteAPIKey)
	r.Get("/api/accounts", app.ListAccounts)
	r.Post("/api/accounts", app.CreateAccount)
	r.Post("/api/accounts/exchange-token", app.ExchangeToken)
	r.Get("/api/accounts/{id}", app.GetAccount)
	r.Put("/api/accounts/{id}", app.UpdateAccount)
	r.Delete("/api/accounts/{id}", app.DeleteAccount)
	r.Post("/api/accounts/{id}/register", app.RegisterPhoneNumber)
	r.Post("/api/accounts/{id}/test", app.TestAccountConnection)
	r.Post("/api/accounts/{id}/subscribe", app.SubscribeApp)
	r.Get("/api/accounts/{id}/business_profile", app.GetBusinessProfile)
	r.Put("/api/accounts/{id}/business_profile", app.UpdateBusinessProfile)
	r.Post("/api/accounts/{id}/business_profile/photo", app.UpdateProfilePicture)
	r.Get("/api/contacts", app.ListContacts)
	r.Post("/api/contacts", app.CreateContact)
	r.Get("/api/contacts/{id}", app.GetContact)
	r.Put("/api/contacts/{id}", app.UpdateContact)
	r.Delete("/api/contacts/{id}", app.DeleteContact)
	r.Put("/api/contacts/{id}/assign", app.AssignContact)
	r.Put("/api/contacts/{id}/tags", app.UpdateContactTags)
	r.Get("/api/contacts/{id}/session-data", app.GetContactSessionData)
	r.Post("/api/export", app.ExportData)
	r.Post("/api/import", app.ImportData)
	r.Get("/api/export/{table}/config", app.GetExportConfig)
	r.Get("/api/import/{table}/config", app.GetImportConfig)
	r.Get("/api/tags", app.ListTags)
	r.Post("/api/tags", app.CreateTag)
	r.Put("/api/tags/{name}", app.UpdateTag)
	r.Delete("/api/tags/{name}", app.DeleteTag)
	r.Get("/api/contacts/{id}/messages", app.GetMessages)
	r.Post("/api/contacts/{id}/messages", app.SendMessage)
	r.Post("/api/contacts/{id}/mark-read", app.MarkContactRead)
	r.Post("/api/contacts/{id}/messages/{message_id}/reaction", app.SendReaction)
	r.Post("/api/messages", app.SendMessage)
	r.Post("/api/messages/template", app.SendTemplateMessage)
	r.Post("/api/messages/media", app.SendMediaMessage)
	r.Put("/api/messages/{id}/read", app.MarkMessageRead)
	r.Get("/api/contacts/{id}/notes", app.ListConversationNotes)
	r.Post("/api/contacts/{id}/notes", app.CreateConversationNote)
	r.Put("/api/contacts/{id}/notes/{note_id}", app.UpdateConversationNote)
	r.Delete("/api/contacts/{id}/notes/{note_id}", app.DeleteConversationNote)
	r.Get("/api/media/{message_id}", app.ServeMedia)
	r.Get("/api/templates", app.ListTemplates)
	r.Post("/api/templates", app.CreateTemplate)
	r.Get("/api/templates/{id}", app.GetTemplate)
	r.Put("/api/templates/{id}", app.UpdateTemplate)
	r.Delete("/api/templates/{id}", app.DeleteTemplate)
	r.Post("/api/templates/sync", app.SyncTemplates)
	r.Post("/api/templates/{id}/publish", app.SubmitTemplate)
	r.Post("/api/templates/upload-media", app.UploadTemplateMedia)
	r.Get("/api/flows", app.ListFlows)
	r.Post("/api/flows", app.CreateFlow)
	r.Get("/api/flows/{id}", app.GetFlow)
	r.Put("/api/flows/{id}", app.UpdateFlow)
	r.Delete("/api/flows/{id}", app.DeleteFlow)
	r.Post("/api/flows/{id}/save-to-meta", app.SaveFlowToMeta)
	r.Post("/api/flows/{id}/publish", app.PublishFlow)
	r.Post("/api/flows/{id}/deprecate", app.DeprecateFlow)
	r.Post("/api/flows/{id}/duplicate", app.DuplicateFlow)
	r.Post("/api/flows/sync", app.SyncFlows)
	r.Get("/api/campaigns", app.ListCampaigns)
	r.Post("/api/campaigns", app.CreateCampaign)
	r.Get("/api/campaigns/{id}", app.GetCampaign)
	r.Put("/api/campaigns/{id}", app.UpdateCampaign)
	r.Delete("/api/campaigns/{id}", app.DeleteCampaign)
	r.Post("/api/campaigns/{id}/start", app.StartCampaign)
	r.Post("/api/campaigns/{id}/pause", app.PauseCampaign)
	r.Post("/api/campaigns/{id}/cancel", app.CancelCampaign)
	r.Post("/api/campaigns/{id}/retry-failed", app.RetryFailed)
	r.Get("/api/campaigns/{id}/progress", app.GetCampaign)
	r.Post("/api/campaigns/{id}/recipients/import", app.ImportRecipients)
	r.Get("/api/campaigns/{id}/recipients", app.GetCampaignRecipients)
	r.Delete("/api/campaigns/{id}/recipients/{recipientId}", app.DeleteCampaignRecipient)
	r.Post("/api/campaigns/{id}/media", app.UploadCampaignMedia)
	r.Get("/api/campaigns/{id}/media", app.ServeCampaignMedia)
	r.Get("/api/chatbot/settings", app.GetChatbotSettings)
	r.Put("/api/chatbot/settings", app.UpdateChatbotSettings)
	r.Get("/api/chatbot/keywords", app.ListKeywordRules)
	r.Post("/api/chatbot/keywords", app.CreateKeywordRule)
	r.Get("/api/chatbot/keywords/{id}", app.GetKeywordRule)
	r.Put("/api/chatbot/keywords/{id}", app.UpdateKeywordRule)
	r.Delete("/api/chatbot/keywords/{id}", app.DeleteKeywordRule)
	r.Get("/api/chatbot/flows", app.ListChatbotFlows)
	r.Post("/api/chatbot/flows", app.CreateChatbotFlow)
	r.Get("/api/chatbot/flows/{id}", app.GetChatbotFlow)
	r.Put("/api/chatbot/flows/{id}", app.UpdateChatbotFlow)
	r.Delete("/api/chatbot/flows/{id}", app.DeleteChatbotFlow)
	r.Get("/api/chatbot/ai-contexts", app.ListAIContexts)
	r.Post("/api/chatbot/ai-contexts", app.CreateAIContext)
	r.Get("/api/chatbot/ai-contexts/{id}", app.GetAIContext)
	r.Put("/api/chatbot/ai-contexts/{id}", app.UpdateAIContext)
	r.Delete("/api/chatbot/ai-contexts/{id}", app.DeleteAIContext)
	r.Get("/api/chatbot/transfers", app.ListAgentTransfers)
	r.Post("/api/chatbot/transfers", app.CreateAgentTransfer)
	r.Post("/api/chatbot/transfers/pick", app.PickNextTransfer)
	r.Put("/api/chatbot/transfers/{id}/resume", app.ResumeFromTransfer)
	r.Put("/api/chatbot/transfers/{id}/assign", app.AssignAgentTransfer)
	r.Get("/api/teams", app.ListTeams)
	r.Post("/api/teams", app.CreateTeam)
	r.Get("/api/teams/{id}", app.GetTeam)
	r.Put("/api/teams/{id}", app.UpdateTeam)
	r.Delete("/api/teams/{id}", app.DeleteTeam)
	r.Get("/api/teams/{id}/members", app.ListTeamMembers)
	r.Post("/api/teams/{id}/members", app.AddTeamMember)
	r.Delete("/api/teams/{id}/members/{member_user_id}", app.RemoveTeamMember)
	r.Get("/api/audit-logs", app.ListAuditLogs)
	r.Get("/api/audit-logs/{id}", app.GetAuditLog)
	r.Get("/api/canned-responses", app.ListCannedResponses)
	r.Post("/api/canned-responses", app.CreateCannedResponse)
	r.Get("/api/canned-responses/{id}", app.GetCannedResponse)
	r.Put("/api/canned-responses/{id}", app.UpdateCannedResponse)
	r.Delete("/api/canned-responses/{id}", app.DeleteCannedResponse)
	r.Post("/api/canned-responses/{id}/use", app.IncrementCannedResponseUsage)
	r.Get("/api/chatbot/sessions", app.ListChatbotSessions)
	r.Get("/api/chatbot/sessions/{id}", app.GetChatbotSession)
	r.Get("/api/usage", app.GetUsage)
	r.Get("/api/billing/usage", app.GetUsage)
	r.Get("/api/analytics/dashboard", app.GetDashboardStats)
	r.Get("/api/analytics/messages", app.GetMessageAnalytics)
	r.Get("/api/analytics/chatbot", app.GetChatbotAnalytics)
	r.Get("/api/analytics/agents", app.GetAgentAnalytics)
	r.Get("/api/analytics/agents/{id}", app.GetAgentDetails)
	r.Get("/api/analytics/agents/comparison", app.GetAgentComparison)
	r.Get("/api/analytics/meta", app.GetMetaAnalytics)
	r.Get("/api/analytics/meta/accounts", app.ListMetaAccountsForAnalytics)
	r.Post("/api/analytics/meta/refresh", app.RefreshMetaAnalyticsCache)
	r.Get("/api/widgets", app.ListWidgets)
	r.Post("/api/widgets", app.CreateWidget)
	r.Get("/api/widgets/data-sources", app.GetWidgetDataSources)
	r.Get("/api/widgets/data", app.GetAllWidgetsData)
	r.Get("/api/widgets/{id}", app.GetWidget)
	r.Put("/api/widgets/{id}", app.UpdateWidget)
	r.Delete("/api/widgets/{id}", app.DeleteWidget)
	r.Get("/api/widgets/{id}/data", app.GetWidgetData)
	r.Post("/api/widgets/layout", app.SaveWidgetLayout)
	r.Get("/api/org/settings", app.GetOrganizationSettings)
	r.Put("/api/org/settings", app.UpdateOrganizationSettings)
	r.Post("/api/org/audio", app.UploadOrgAudio)
	r.Get("/api/organizations", app.ListOrganizations)
	r.Post("/api/organizations", app.CreateOrganization)
	r.Get("/api/organizations/current", app.GetCurrentOrganization)
	r.Get("/api/organizations/members", app.ListOrganizationMembers)
	r.Post("/api/organizations/members", app.AddOrganizationMember)
	r.Put("/api/organizations/members/{member_id}", app.UpdateOrganizationMemberRole)
	r.Delete("/api/organizations/members/{member_id}", app.RemoveOrganizationMember)
	r.Get("/api/settings/sso", app.GetSSOSettings)
	r.Put("/api/settings/sso/{provider}", app.UpdateSSOProvider)
	r.Delete("/api/settings/sso/{provider}", app.DeleteSSOProvider)
	r.Get("/api/webhooks", app.ListWebhooks)
	r.Post("/api/webhooks", app.CreateWebhook)
	r.Get("/api/webhooks/{id}", app.GetWebhook)
	r.Put("/api/webhooks/{id}", app.UpdateWebhook)
	r.Delete("/api/webhooks/{id}", app.DeleteWebhook)
	r.Post("/api/webhooks/{id}/test", app.TestWebhook)
	r.Get("/api/custom-actions", app.ListCustomActions)
	r.Post("/api/custom-actions", app.CreateCustomAction)
	r.Get("/api/custom-actions/{id}", app.GetCustomAction)
	r.Put("/api/custom-actions/{id}", app.UpdateCustomAction)
	r.Delete("/api/custom-actions/{id}", app.DeleteCustomAction)
	r.Post("/api/custom-actions/{id}/execute", app.ExecuteCustomAction)
	r.Get("/api/ivr-flows", app.ListIVRFlows)
	r.Get("/api/ivr-flows/{id}", app.GetIVRFlow)
	r.Post("/api/ivr-flows", app.CreateIVRFlow)
	r.Put("/api/ivr-flows/{id}", app.UpdateIVRFlow)
	r.Delete("/api/ivr-flows/{id}", app.DeleteIVRFlow)
	r.Post("/api/ivr-flows/audio", app.UploadIVRAudio)
	r.Get("/api/ivr-flows/audio/{filename}", app.ServeIVRAudio)
	r.Get("/api/call-logs", app.ListCallLogs)
	r.Get("/api/call-logs/{id}", app.GetCallLog)
	r.Get("/api/call-logs/{id}/recording", app.GetCallRecording)
	r.Get("/api/call-transfers", app.ListCallTransfers)
	r.Get("/api/call-transfers/{id}", app.GetCallTransfer)
	r.Post("/api/call-transfers/{id}/connect", app.ConnectCallTransfer)
	r.Post("/api/call-transfers/{id}/hangup", app.HangupCallTransfer)
	r.Post("/api/call-transfers/initiate", app.InitiateAgentTransfer)
	r.Post("/api/call-logs/{id}/hold", app.HoldCall)
	r.Post("/api/call-logs/{id}/resume", app.ResumeCall)
	r.Post("/api/calls/outgoing", app.InitiateOutgoingCall)
	r.Post("/api/calls/outgoing/{id}/hangup", app.HangupOutgoingCall)
	r.Post("/api/calls/permission-request", app.SendCallPermissionRequest)
	r.Get("/api/calls/permission/{contactId}", app.GetCallPermission)
	r.Get("/api/calls/ice-servers", app.GetICEServers)
	r.Get("/api/catalogs", app.ListCatalogs)
	r.Post("/api/catalogs", app.CreateCatalog)
	r.Get("/api/catalogs/{id}", app.GetCatalog)
	r.Delete("/api/catalogs/{id}", app.DeleteCatalog)
	r.Post("/api/catalogs/sync", app.SyncCatalogs)
	r.Get("/api/catalogs/{id}/products", app.ListCatalogProducts)
	r.Post("/api/catalogs/{id}/products", app.CreateCatalogProduct)
	r.Get("/api/products/{id}", app.GetCatalogProduct)
	r.Put("/api/products/{id}", app.UpdateCatalogProduct)
	r.Delete("/api/products/{id}", app.DeleteCatalogProduct)
}
