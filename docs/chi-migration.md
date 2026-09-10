# HTTP stack migration: fastglue/fasthttp → net/http + chi

**License:** AGPL-3.0 (unchanged). Module path remains `github.com/shridarpatil/whatomate` for now; rename to `zinye-technologies/whatomate` is a later follow-up.

## Goal

Replace `github.com/zerodha/fastglue` + `github.com/valyala/fasthttp` with Go's standard `net/http` and `github.com/go-chi/chi/v5`, without a big-bang rewrite of every handler.

## Inventory of fastglue / fasthttp touchpoints

| Area | Paths | Role today |
|------|--------|------------|
| Server bootstrap | `cmd/whatomate/main.go` | Uses `internal/httpapi` (`net/http` + chi) |
| Handlers | `internal/handlers/*.go` (~60+ files) | Essentially all route handlers are native `http.HandlerFunc`; Wrap unused on chi mounts |
| Middleware | `internal/middleware/{middleware,csrf,ratelimit,http}.go` | Parallel fasthttp + stdlib middleware |
| Tests | `test/testutil/http.go`, `internal/handlers/*_test.go` | Legacy fasthttp builders; native handlers via `testutil.InvokeHTTP` |
| Frontend | `internal/frontend/embed.go` | Native `http.Handler` |
| WebSocket | `internal/handlers/websocket.go` | Native `WebSocketHTTP` on chi |
| Other | `pkg/whatsapp`, calling media paths | Occasional fasthttp types; not the HTTP server surface |

Calling / IVR features remain fully supported and are now mounted as **native** chi handlers (no Wrap).

## Strategy: adapter layer (not big-bang)

1. **`internal/httpapi`** owns the chi router and route mounting.
2. **`httpapi.Wrap`** adapts remaining fastglue handlers → `http.Handler`.
3. **Native slices** (no Wrap): essentially the full API surface (auth, CRUD, chatbot, campaigns, calling/IVR, org/SSO/analytics/widgets/catalog/import-export, WebSocket, SPA).

## Progress

### Phase 0 — done

Chi edge, Wrap shim, stdlib middleware, native WebSocket + SPA.

### Phase 1 — helpers (partial)

- `internal/handlers/http.go`: `SendEnvelope`, `SendErrorEnvelope`, `DecodeJSON`, `getOrgIDHTTP`, `requireAuthHTTP`, …
- Middleware: `UserIDFromContext`, `OrganizationIDFromContext`, `WithUserID`, …
- Cookies: `setAuthCookiesHTTP` / `clearAuthCookiesHTTP` (SSO uses HTTP variants; fasthttp cookie helpers are unused leftovers).
- `testutil.InvokeHTTP` for existing unit tests.

### Phase 2 batch 1 — native (no Wrap)

| Route(s) | Handler | Status |
|----------|---------|--------|
| `GET /health` | `HealthCheck` | **native** |
| `GET /ready` | `ReadyCheck` | **native** |
| `POST /api/auth/login` | `Login` | **native** |
| `POST /api/auth/register` | `Register` | **native** |
| `POST /api/auth/refresh` | `RefreshToken` | **native** |
| `POST /api/auth/logout` | `Logout` | **native** |
| `POST /api/auth/switch-org` | `SwitchOrg` | **native** |
| `GET /api/auth/ws-token` | `GetWSToken` | **native** |
| `GET /api/me` | `GetCurrentUser` | **native** |
| `PUT /api/me/settings` | `UpdateCurrentUserSettings` | **native** |
| `PUT /api/me/password` | `ChangePassword` | **native** |
| `PUT /api/me/availability` | `UpdateAvailability` | **native** |
| `GET /api/me/organizations` | `ListMyOrganizations` | **native** |
| `GET /api/organizations/current` | `GetCurrentOrganization` | **native** |
| `GET /ws` | `WebSocketHTTP` | **native** (phase 0) |
| SPA `/`, `/*` | `frontend.HTTPHandler` | **native** (phase 0) |


### Phase 2 batch 2 — native (no Wrap)

| Route(s) | Handler | Status |
|----------|---------|--------|
| `GET/POST /api/users`, `GET/PUT/DELETE /api/users/{id}` | `ListUsers`, `CreateUser`, `GetUser`, `UpdateUser`, `DeleteUser` | **native** |
| `GET/POST /api/roles`, `GET/PUT/DELETE /api/roles/{id}` | `ListRoles`, `CreateRole`, `GetRole`, `UpdateRole`, `DeleteRole` | **native** |
| `GET /api/permissions` | `ListPermissions` | **native** |
| `GET/POST /api/api-keys`, `GET/PUT/DELETE /api/api-keys/{id}` | `ListAPIKeys`, `CreateAPIKey`, `GetAPIKey`, `UpdateAPIKey`, `DeleteAPIKey` | **native** |

### Phase 2 batch 3 — native (no Wrap)

| Route(s) | Handler | Status |
|----------|---------|--------|
| `GET/POST /api/accounts`, `GET/PUT/DELETE /api/accounts/{id}` | `ListAccounts`, `CreateAccount`, `GetAccount`, `UpdateAccount`, `DeleteAccount` | **native** |
| `POST /api/accounts/exchange-token` | `ExchangeToken` | **native** |
| `POST /api/accounts/{id}/register` | `RegisterPhoneNumber` | **native** |
| `POST /api/accounts/{id}/test` | `TestAccountConnection` | **native** |
| `POST /api/accounts/{id}/subscribe` | `SubscribeApp` | **native** |
| `GET/PUT /api/accounts/{id}/business_profile`, `POST .../photo` | `GetBusinessProfile`, `UpdateBusinessProfile`, `UpdateProfilePicture` | **native** |
| `GET/POST /api/contacts`, `GET/PUT/DELETE /api/contacts/{id}` | `ListContacts`, `CreateContact`, `GetContact`, `UpdateContact`, `DeleteContact` | **native** |
| `PUT /api/contacts/{id}/assign` | `AssignContact` | **native** |
| `PUT /api/contacts/{id}/tags` | `UpdateContactTags` | **native** |
| `GET /api/contacts/{id}/session-data` | `GetContactSessionData` | **native** |
| `GET/POST /api/tags`, `PUT/DELETE /api/tags/{name}` | `ListTags`, `CreateTag`, `UpdateTag`, `DeleteTag` | **native** |
| `GET/POST /api/contacts/{id}/notes`, `PUT/DELETE .../notes/{note_id}` | `ListConversationNotes`, `CreateConversationNote`, `UpdateConversationNote`, `DeleteConversationNote` | **native** |

### Phase 2 batch 4 — native (no Wrap)

| Route(s) | Handler | Status |
|----------|---------|--------|
| `GET/POST /api/contacts/{id}/messages`, `POST .../reaction` | `GetMessages`, `SendMessage`, `SendReaction` | **native** |
| `POST /api/contacts/{id}/mark-read` | `MarkContactRead` | **native** |
| `POST /api/messages`, `POST /api/messages/template`, `POST /api/messages/media` | `SendMessage`, `SendTemplateMessage`, `SendMediaMessage` | **native** |
| `PUT /api/messages/{id}/read` | `MarkMessageRead` (stub) | **native** |
| `GET /api/media/{message_id}` | `ServeMedia` | **native** |
| `GET/POST /api/templates`, `GET/PUT/DELETE /api/templates/{id}` | `ListTemplates`, `CreateTemplate`, `GetTemplate`, `UpdateTemplate`, `DeleteTemplate` | **native** |
| `POST /api/templates/sync`, `POST .../{id}/publish`, `POST .../upload-media` | `SyncTemplates`, `SubmitTemplate`, `UploadTemplateMedia` | **native** |
| `GET/POST /api/flows`, `GET/PUT/DELETE /api/flows/{id}` | `ListFlows`, `CreateFlow`, `GetFlow`, `UpdateFlow`, `DeleteFlow` | **native** |
| `POST /api/flows/{id}/{save-to-meta,publish,deprecate,duplicate}`, `POST /api/flows/sync` | `SaveFlowToMeta`, `PublishFlow`, `DeprecateFlow`, `DuplicateFlow`, `SyncFlows` | **native** |
| `GET /api/analytics/messages`, `GET /api/analytics/chatbot` | `GetMessageAnalytics`, `GetChatbotAnalytics` (stubs) | **native** |


### Phase 2 batch 5 — native (no Wrap)

| Route(s) | Handler | Status |
|----------|---------|--------|
| `GET/POST /api/campaigns`, `GET/PUT/DELETE /api/campaigns/{id}` | `ListCampaigns`, `CreateCampaign`, `GetCampaign`, `UpdateCampaign`, `DeleteCampaign` | **native** |
| `POST /api/campaigns/{id}/{start,pause,cancel,retry-failed}` | `StartCampaign`, `PauseCampaign`, `CancelCampaign`, `RetryFailed` | **native** |
| `GET /api/campaigns/{id}/progress` | `GetCampaign` | **native** |
| `POST/GET .../recipients`, `DELETE .../recipients/{recipientId}` | `ImportRecipients`, `GetCampaignRecipients`, `DeleteCampaignRecipient` | **native** |
| `POST/GET /api/campaigns/{id}/media` | `UploadCampaignMedia`, `ServeCampaignMedia` | **native** |
| `GET/PUT /api/chatbot/settings` | `GetChatbotSettings`, `UpdateChatbotSettings` | **native** |
| `GET/POST /api/chatbot/keywords`, `GET/PUT/DELETE .../{id}` | keyword rule CRUD | **native** |
| `GET/POST /api/chatbot/flows`, `GET/PUT/DELETE .../{id}` | chatbot flow CRUD | **native** |
| `GET/POST /api/chatbot/ai-contexts`, `GET/PUT/DELETE .../{id}` | AI context CRUD | **native** |
| `GET/POST /api/chatbot/transfers`, pick/resume/assign | agent transfer handlers | **native** |
| `GET /api/chatbot/sessions`, `GET .../{id}` | `ListChatbotSessions`, `GetChatbotSession` | **native** |
| `GET/POST /api/webhook` | `WebhookVerify`, `WebhookHandler` (Meta) | **native** |
| `GET/POST /api/webhooks`, `GET/PUT/DELETE .../{id}`, `POST .../test` | outbound webhook mgmt | **native** |
| `GET /api/custom-actions/redirect/{token}` | `CustomActionRedirect` | **native** |
| `GET/POST /api/custom-actions`, CRUD + execute | custom action handlers | **native** |


### Phase 2 batch 6 — native (no Wrap)

| Route(s) | Handler | Status |
|----------|---------|--------|
| `GET/POST /api/ivr-flows`, CRUD + audio | IVR flow handlers + `UploadIVRAudio` / `ServeIVRAudio` | **native** |
| `GET /api/call-logs`, `GET .../{id}`, recording | `ListCallLogs`, `GetCallLog`, `GetCallRecording` | **native** |
| `GET/POST /api/call-transfers*`, hold/resume | call transfer + hold/resume handlers | **native** |
| `POST /api/calls/outgoing*`, permission, ICE | outgoing call handlers | **native** |
| `POST /api/org/audio` | `UploadOrgAudio` | **native** |

### Phase 2 batch 7 — native (no Wrap)

| Route(s) | Handler | Status |
|----------|---------|--------|
| Teams CRUD + members | `ListTeams` … `RemoveTeamMember` | **native** |
| Audit logs | `ListAuditLogs`, `GetAuditLog` | **native** |
| Canned responses | full CRUD + use | **native** |
| Analytics dashboard/agents/meta | `GetDashboardStats`, agent + meta analytics | **native** |
| Widgets | list/CRUD/layout/data sources/data | **native** |
| Org settings / orgs / members | settings + org admin CRUD | **native** |
| SSO public + admin settings | `GetPublicSSOProviders`, `InitSSO`, `CallbackSSO`, settings CRUD | **native** |
| Import/export | `ExportData`, `ImportData`, configs | **native** |
| Catalogs / products | catalog + product CRUD + sync | **native** |
| `GET /api/embedded-signup/config` | `GetEmbeddedSignupConfig` | **native** |

### Phase 3 cleanup (partial) — after batch 7

Deleted unused fasthttp production leftovers (tests still bridge via `testutil`):

- **`httpapi.Wrap`** + `adapt.go` / `adapt_test.go` — removed (0 mounts).
- **fasthttp cookie helpers** (`setAuthCookies` / `clearAuthCookies`) — removed; HTTP variants remain.
- **`WebSocketHandler`** + fasthttp upgrader — removed; routes use `WebSocketHTTP`.
- **`getOrgID` / `getOrgAndUserID` / `requirePermission` / `requireAuth` / `decodeRequest`** (fasthttp) — removed from `app.go`.
- **`CopyHTTPContextToUserValues`** — removed (only used by Wrap).

Still present (test / parallel middleware):

- **`helpers.go` fasthttp parsers** + `helpers_test.go` (used by unit tests).
- **fasthttp middleware** in `middleware.go` / `csrf.go` / `ratelimit.go` + tests.
- **`testutil` fasthttp request builders** + `InvokeHTTP` bridge.
- **`fastglue` / `fasthttp` in `go.mod`** — still required by the above.

### Counts (post Phase 3 partial)

- Chi routes using Wrap: **0**
- Exported `func(*fastglue.Request) error` handlers: **0**
- Usage meter: `GET /api/usage` and `GET /api/billing/usage` (native)

### Next

Migrate `testutil` + fasthttp middleware/helpers off fasthttp; then `go mod tidy` can drop `fastglue`.

## Non-goals / constraints

- Do **not** change LICENSE away from AGPL-3.0.
- Do **not** delete calling/IVR.
- Prefer incremental PRs; prefer chi route groups once handlers are native.

## Deletion criteria for the adapter

1. No production handler still has signature `func(*fastglue.Request) error`.
2. `test/testutil` no longer constructs `fasthttp.RequestCtx` for HTTP tests.
3. `go mod why github.com/zerodha/fastglue` reports nothing (or only intentional leftovers).
