package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// NewJSONRequest creates a fastglue request with a JSON body for testing.
func NewJSONRequest(t *testing.T, body any) *fastglue.Request {
	t.Helper()

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.Header.SetMethod("POST")

	if body != nil {
		jsonData, err := json.Marshal(body)
		require.NoError(t, err, "failed to marshal request body")
		ctx.Request.SetBody(jsonData)
	}

	return &fastglue.Request{RequestCtx: ctx}
}

// NewGETRequest creates a fastglue GET request for testing.
func NewGETRequest(t *testing.T) *fastglue.Request {
	t.Helper()

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")

	return &fastglue.Request{RequestCtx: ctx}
}

// NewRequest creates an empty fastglue request for testing.
func NewRequest(t *testing.T) *fastglue.Request {
	t.Helper()

	ctx := &fasthttp.RequestCtx{}
	return &fastglue.Request{RequestCtx: ctx}
}

// SetAuthHeader sets a Bearer token Authorization header on the request.
func SetAuthHeader(req *fastglue.Request, token string) {
	req.RequestCtx.Request.Header.Set("Authorization", "Bearer "+token)
}

// SetHeader sets a header on the request.
func SetHeader(req *fastglue.Request, key, value string) {
	req.RequestCtx.Request.Header.Set(key, value)
}

// SetQueryParam sets a query parameter on the request.
func SetQueryParam(req *fastglue.Request, key string, value any) {
	req.RequestCtx.QueryArgs().Set(key, fmt.Sprintf("%v", value))
}

// SetPathParam sets a path parameter (user value) on the request.
func SetPathParam(req *fastglue.Request, key string, value any) {
	req.RequestCtx.SetUserValue(key, value)
}

// GetResponseBody returns the response body as bytes.
func GetResponseBody(req *fastglue.Request) []byte {
	return req.RequestCtx.Response.Body()
}

// GetResponseStatusCode returns the response status code.
func GetResponseStatusCode(req *fastglue.Request) int {
	return req.RequestCtx.Response.StatusCode()
}

// ParseJSONResponse parses the response body as JSON into the given target.
func ParseJSONResponse(t *testing.T, req *fastglue.Request, target any) {
	t.Helper()

	body := GetResponseBody(req)
	err := json.Unmarshal(body, target)
	require.NoError(t, err, "failed to parse JSON response: %s", string(body))
}

// APIEnvelope represents the standard fastglue API response envelope.
type APIEnvelope struct {
	Status  string          `json:"status"`
	Message *string         `json:"message,omitempty"`
	Data    json.RawMessage `json:"data"`
}

// ParseEnvelopeResponse parses the response as an API envelope and returns the data.
func ParseEnvelopeResponse(t *testing.T, req *fastglue.Request, target any) {
	t.Helper()

	var envelope APIEnvelope
	ParseJSONResponse(t, req, &envelope)

	if target != nil && envelope.Data != nil {
		err := json.Unmarshal(envelope.Data, target)
		require.NoError(t, err, "failed to parse envelope data")
	}
}

// GetResponseCookie reads a Set-Cookie value from the response by name.
func GetResponseCookie(req *fastglue.Request, name string) string {
	var value string
	req.RequestCtx.Response.Header.VisitAllCookie(func(key, val []byte) {
		c := fasthttp.AcquireCookie()
		defer fasthttp.ReleaseCookie(c)
		if err := c.ParseBytes(val); err == nil && string(c.Key()) == name {
			value = string(c.Value())
		}
	})
	return value
}

// AssertErrorResponse asserts that the response is an error with the expected message.
func AssertErrorResponse(t *testing.T, req *fastglue.Request, expectedStatus int, expectedMessage string) {
	t.Helper()

	require.Equal(t, expectedStatus, GetResponseStatusCode(req), "unexpected status code")

	var envelope APIEnvelope
	ParseJSONResponse(t, req, &envelope)

	require.Equal(t, "error", envelope.Status, "expected error status")
	require.NotNil(t, envelope.Message, "expected message in envelope")
	require.Contains(t, *envelope.Message, expectedMessage, "error message mismatch")
}

// --- net/http / httptest helpers (chi migration) ---

// InvokeHTTP runs a native http.HandlerFunc against a fasthttp-backed
// *fastglue.Request used by existing unit tests, then copies the response
// (status, headers including Set-Cookie, body) back onto that request so
// GetResponseBody / GetResponseCookie / AssertErrorResponse keep working.
func InvokeHTTP(t *testing.T, h http.HandlerFunc, req *fastglue.Request) {
	t.Helper()

	method := string(req.RequestCtx.Method())
	if method == "" {
		method = http.MethodGet
	}
	uri := string(req.RequestCtx.RequestURI())
	if uri == "" || uri == "*" {
		uri = "/"
	}

	httpReq := httptest.NewRequest(method, uri, bytes.NewReader(req.RequestCtx.PostBody()))
	req.RequestCtx.Request.Header.VisitAll(func(k, v []byte) {
		key := string(k)
		// Skip fasthttp pseudo / hop headers; Cookie is rebuilt below.
		if key == "Content-Length" {
			return
		}
		httpReq.Header.Add(key, string(v))
	})
	// Rebuild Cookie header from fasthttp cookie jar (VisitAll may miss SetCookie-only).
	req.RequestCtx.Request.Header.VisitAllCookie(func(key, value []byte) {
		httpReq.AddCookie(&http.Cookie{Name: string(key), Value: string(value)})
	})
	if ct := string(req.RequestCtx.Request.Header.ContentType()); ct != "" {
		httpReq.Header.Set("Content-Type", ct)
	}
	// fasthttp QueryArgs may not be reflected in RequestURI(); copy explicitly.
	if qa := req.RequestCtx.QueryArgs(); qa.Len() > 0 {
		httpReq.URL.RawQuery = qa.String()
	}

	ctx := httpReq.Context()
	if v := req.RequestCtx.UserValue("user_id"); v != nil {
		if id, ok := v.(uuid.UUID); ok {
			ctx = middleware.WithUserID(ctx, id)
		}
	}
	if v := req.RequestCtx.UserValue("organization_id"); v != nil {
		if id, ok := v.(uuid.UUID); ok {
			ctx = middleware.WithOrganizationID(ctx, id)
		}
	}
	if v := req.RequestCtx.UserValue("role_id"); v != nil {
		if id, ok := v.(uuid.UUID); ok {
			ctx = middleware.WithRoleID(ctx, id)
		}
	}
	if v := req.RequestCtx.UserValue("is_super_admin"); v != nil {
		if b, ok := v.(bool); ok {
			ctx = middleware.WithIsSuperAdmin(ctx, b)
		}
	}
	// Bridge fasthttp path params (SetPathParam / SetUserValue) into chi URLParams.
	rctx := chi.NewRouteContext()
	authKeys := map[string]struct{}{
		"user_id": {}, "organization_id": {}, "role_id": {}, "is_super_admin": {},
	}
	req.RequestCtx.VisitUserValues(func(key []byte, value any) {
		k := string(key)
		if _, skip := authKeys[k]; skip {
			return
		}
		switch v := value.(type) {
		case string:
			rctx.URLParams.Add(k, v)
		case []byte:
			rctx.URLParams.Add(k, string(v))
		case uuid.UUID:
			rctx.URLParams.Add(k, v.String())
		default:
			rctx.URLParams.Add(k, fmt.Sprintf("%v", v))
		}
	})
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	httpReq = httpReq.WithContext(ctx)

	rec := httptest.NewRecorder()
	h(rec, httpReq)

	req.RequestCtx.Response.Reset()
	req.RequestCtx.Response.SetStatusCode(rec.Code)
	for k, vals := range rec.Header() {
		for _, v := range vals {
			req.RequestCtx.Response.Header.Add(k, v)
		}
	}
	req.RequestCtx.Response.SetBody(rec.Body.Bytes())
}
