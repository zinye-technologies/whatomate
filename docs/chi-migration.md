# HTTP stack migration: fastglue/fasthttp → net/http + chi

**License:** AGPL-3.0 (unchanged). Module path remains `github.com/shridarpatil/whatomate` for now; rename to `zinye-technologies/whatomate` is a later follow-up.

## Goal

Replace `github.com/zerodha/fastglue` + `github.com/valyala/fasthttp` with Go's standard `net/http` and `github.com/go-chi/chi/v5`, without a big-bang rewrite of every handler.

## Inventory of fastglue / fasthttp touchpoints

| Area | Paths | Role today |
|------|--------|------------|
| Server bootstrap | `cmd/whatomate/main.go` | `fastglue.NewGlue()`, `fasthttp.Server`, CORS wrapper, route table, auth/rate-limit `Before` hooks |
| Handlers | `internal/handlers/*.go` (~60+ files) | `func(*fastglue.Request) error`; path params / auth via `RequestCtx.UserValue`; envelopes via `SendEnvelope` / `SendErrorEnvelope` |
| Middleware | `internal/middleware/{middleware,csrf,ratelimit}.go` | `fastglue.FastMiddleware` (`func(*Request) *Request`); Auth, CSRF, CORS, Recovery, SecurityHeaders, RateLimit |
| Tests | `test/testutil/http.go`, `internal/handlers/*_test.go`, `internal/middleware/*_test.go` | Build `*fastglue.Request` on a bare `fasthttp.RequestCtx` |
| Frontend | `internal/frontend/embed.go` | Already implements `http.Handler`; wraps with `fasthttpadaptor` for the fasthttp server |
| WebSocket | `internal/handlers/websocket.go`, `internal/websocket/client.go` | `github.com/fasthttp/websocket` — package also exports net/http `Upgrader` (same `Conn`) |
| Other | `pkg/whatsapp`, calling media paths | Occasional fasthttp types; not the HTTP server surface |

Calling / IVR features are **not** in scope for deletion; they keep working through the same handlers behind the adapter.

## Strategy chosen: **adapter layer** (not big-bang)

We introduce chi + `net/http` at the edge and keep existing fastglue handlers compiling behind a temporary shim:

1. **`internal/httpapi`** owns the chi router, global stdlib middleware, and route mounting.
2. **`httpapi.Wrap`** adapts `fastglue.FastRequestHandler` → `http.Handler` by projecting `*http.Request` into a `fasthttp.RequestCtx`, copying chi URL params and auth context into `UserValue`, invoking the handler, then copying status/headers/body (including `Set-Cookie`) back to `http.ResponseWriter`.
3. **Middleware** gains parallel `func(http.Handler) http.Handler` implementations used by chi. Legacy `fastglue.FastMiddleware` variants remain so unit tests and any leftover fasthttp paths keep compiling.
4. **Thin native slices** (no shim): health/readiness are trivial; WebSocket upgrades via net/http `Upgrader`; frontend serves via `frontend.HTTPHandler`.

**Why not big-bang?** Hundreds of handler/test call sites depend on `*fastglue.Request` and envelope helpers. Converting them all in one PR is high risk and blocks shipping a working server. The adapter is honest tech debt with a clear deletion criteria (see phases).

## Phased plan

### Phase 0 — this PR (`feat/chi-migration`)

- Document inventory + approach (this file).
- Add chi; introduce `internal/httpapi`.
- Switch `cmd/whatomate` server bootstrap to `net/http` + chi.
- Stdlib middleware: RequestID, Recover, SecurityHeaders, CORS, CSRF, Auth, rate limits.
- Shim all existing API handlers; convert WebSocket + frontend to native net/http.
- Keep `fastglue` / `fasthttp` in `go.mod` while handlers still reference them.
- `go test ./...` green (or failures documented).

### Phase 1 — helpers & auth surface

- Introduce `internal/httpapi` request helpers (`DecodeJSON`, `SendEnvelope`, path UUID, pagination) on `net/http`.
- Migrate `helpers.go` / `app.go` auth helpers to accept either adapter context or `*http.Request`.
- Update `test/testutil` with net/http / `httptest` helpers.

### Phase 2 — handler batches (suggested order)

1. **Auth + health + org/me** — `auth.go`, `app.go`, `organization.go`, `sso.go`
2. **Users / roles / API keys** — `users.go`, `roles.go`, `apikeys.go`
3. **Accounts / contacts / tags / notes** — `accounts.go`, `contacts.go`, `tags.go`, `conversation_notes.go`
4. **Messages / media / templates / flows** — `messages.go`, `media.go`, `templates.go`, `flows.go`
5. **Campaigns / chatbot / transfers** — `campaigns.go`, `chatbot*.go`, `agent_transfers.go`
6. **Analytics / widgets / webhooks / custom actions** — remaining CRUD
7. **Calling / IVR / call logs / transfers / outgoing** — keep feature-complete; migrate last among APIs so regressions are obvious

Each batch: convert signatures to `http.HandlerFunc` (or chi-style), drop `Wrap`, extend tests to use `httptest`.

### Phase 3 — delete the shim

- Remove `httpapi.Wrap` and fasthttp projections.
- Remove `fastglue` from `go.mod`; drop fasthttp where unused (may remain briefly for websocket fork if still imported — prefer consolidating on one websocket stack).
- Delete legacy `fastglue.FastMiddleware` implementations once tests are ported.
- Optional: module path rename to `github.com/zinye-technologies/whatomate`.

## Non-goals / constraints

- Do **not** change LICENSE away from AGPL-3.0.
- Do **not** delete calling/IVR.
- Prefer boring incremental PRs over a rewrite.
- Prefer chi idioms (`r.Route`, middleware groups) over global path-string auth checks once handlers are native.

## Deletion criteria for the adapter

The shim may be removed when:

1. No production handler still has signature `func(*fastglue.Request) error`.
2. `test/testutil` no longer constructs `fasthttp.RequestCtx` for HTTP tests.
3. `go mod why github.com/zerodha/fastglue` reports nothing (or only transitive leftovers we intentionally drop).
