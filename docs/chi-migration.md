# HTTP stack migration: fastglue/fasthttp → net/http + chi

**License:** AGPL-3.0 (unchanged). Module path remains `github.com/shridarpatil/whatomate` for now; rename to `zinye-technologies/whatomate` is a later follow-up.

## Goal

Replace `github.com/zerodha/fastglue` + `github.com/valyala/fasthttp` with Go's standard `net/http` and `github.com/go-chi/chi/v5`, without a big-bang rewrite of every handler.

## Inventory of fastglue / fasthttp touchpoints

| Area | Paths | Role today |
|------|--------|------------|
| Server bootstrap | `cmd/whatomate/main.go` | Uses `internal/httpapi` (`net/http` + chi) |
| Handlers | `internal/handlers/*.go` (~60+ files) | Mix of native `http.HandlerFunc` and legacy `func(*fastglue.Request) error` behind `httpapi.Wrap` |
| Middleware | `internal/middleware/{middleware,csrf,ratelimit,http}.go` | Parallel fasthttp + stdlib middleware |
| Tests | `test/testutil/http.go`, `internal/handlers/*_test.go` | Legacy fasthttp builders; native handlers via `testutil.InvokeHTTP` |
| Frontend | `internal/frontend/embed.go` | Native `http.Handler` |
| WebSocket | `internal/handlers/websocket.go` | Native `WebSocketHTTP` on chi |
| Other | `pkg/whatsapp`, calling media paths | Occasional fasthttp types; not the HTTP server surface |

Calling / IVR features are **not** in scope for deletion; they keep working through wrapped handlers.

## Strategy: adapter layer (not big-bang)

1. **`internal/httpapi`** owns the chi router and route mounting.
2. **`httpapi.Wrap`** adapts remaining fastglue handlers → `http.Handler`.
3. **Native slices** (no Wrap): health/ready, auth session, `/api/me*`, current org, users CRUD, roles, API keys, accounts, contacts (+ tags/notes/messages), media serve, templates, WhatsApp flows, WebSocket, SPA.

## Progress

### Phase 0 — done

Chi edge, Wrap shim, stdlib middleware, native WebSocket + SPA.

### Phase 1 — helpers (partial)

- `internal/handlers/http.go`: `SendEnvelope`, `SendErrorEnvelope`, `DecodeJSON`, `getOrgIDHTTP`, `requireAuthHTTP`, …
- Middleware: `UserIDFromContext`, `OrganizationIDFromContext`, `WithUserID`, …
- Cookies: `setAuthCookiesHTTP` / `clearAuthCookiesHTTP` (fasthttp variants kept for SSO).
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

### Leftovers (still Wrap)

- **SSO**: `GetPublicSSOProviders`, `InitSSO`, `CallbackSSO` (still use fasthttp `setAuthCookies`).
- **Org admin CRUD**: `ListOrganizations`, `CreateOrganization`, members, settings, audio upload.
- Campaigns, chatbot (settings/keywords/flows/AI/transfers/sessions), teams, audit logs, canned responses.
- Analytics (dashboard/agents/meta), widgets, webhooks, custom actions, catalog.
- Import/export, Meta webhook verify/handler, embedded signup config.
- Calling / IVR / call-logs / call-transfers / outgoing calls (last).

### Counts (batch 4)

- Native `http.HandlerFunc` handlers: **87**
- Remaining `func(*fastglue.Request) error` handlers: **141**
- Chi routes without Wrap: **88**
- Chi routes still using Wrap: **138**

### Next

Batch 5: campaigns / chatbot / teams → org admin + SSO → calling/IVR last → Phase 3 delete Wrap.

## Non-goals / constraints

- Do **not** change LICENSE away from AGPL-3.0.
- Do **not** delete calling/IVR.
- Prefer incremental PRs; prefer chi route groups once handlers are native.

## Deletion criteria for the adapter

1. No production handler still has signature `func(*fastglue.Request) error`.
2. `test/testutil` no longer constructs `fasthttp.RequestCtx` for HTTP tests.
3. `go mod why github.com/zerodha/fastglue` reports nothing (or only intentional leftovers).
