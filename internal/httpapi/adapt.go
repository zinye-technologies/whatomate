// Package httpapi hosts the chi/net/http edge for Whatomate during the
// fasthttp/fastglue → chi migration. See docs/chi-migration.md.
package httpapi

import (
	"io"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"

	"github.com/shridarpatil/whatomate/internal/middleware"
)

// Wrap adapts a legacy fastglue handler to net/http so it can be mounted on chi.
//
// It projects the incoming *http.Request into a fasthttp.RequestCtx (method, URI,
// headers, body, remote addr), copies chi URL params and stdlib auth context
// values into UserValue slots that existing handlers expect, invokes the handler,
// then copies status, headers (including Set-Cookie), and body back to the
// ResponseWriter.
//
// This is intentional temporary tech debt. Delete once handlers are native
// http.Handlers (see docs/chi-migration.md phase 3).
func Wrap(h fastglue.FastRequestHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var ctx fasthttp.RequestCtx
		if err := projectRequest(r, &ctx); err != nil {
			http.Error(w, `{"status":"error","message":"Invalid request"}`, http.StatusBadRequest)
			return
		}
		copyRouteParams(r, &ctx)
		middleware.CopyHTTPContextToUserValues(r, &ctx)

		req := &fastglue.Request{RequestCtx: &ctx}
		_ = h(req)

		writeResponse(w, &ctx)
	}
}

func projectRequest(r *http.Request, ctx *fasthttp.RequestCtx) error {
	ctx.Request.Header.SetMethod(r.Method)
	if r.URL != nil {
		ctx.Request.SetRequestURI(r.URL.RequestURI())
	}
	if r.Host != "" {
		ctx.Request.SetHost(r.Host)
	} else if r.URL != nil && r.URL.Host != "" {
		ctx.Request.SetHost(r.URL.Host)
	}

	for k, vals := range r.Header {
		for _, v := range vals {
			ctx.Request.Header.Add(k, v)
		}
	}

	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		_ = r.Body.Close()
		ctx.Request.SetBody(body)
	}

	if r.RemoteAddr != "" {
		if addr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr); err == nil {
			ctx.SetRemoteAddr(addr)
		} else if host, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			_ = port
			ctx.SetRemoteAddr(&net.TCPAddr{IP: net.ParseIP(host)})
		}
	}
	return nil
}

func copyRouteParams(r *http.Request, ctx *fasthttp.RequestCtx) {
	routeCtx := chi.RouteContext(r.Context())
	if routeCtx == nil {
		return
	}
	for i, key := range routeCtx.URLParams.Keys {
		if i < len(routeCtx.URLParams.Values) {
			ctx.SetUserValue(key, routeCtx.URLParams.Values[i])
		}
	}
}

func writeResponse(w http.ResponseWriter, ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.VisitAll(func(key, value []byte) {
		// fasthttp may emit Content-Length; let Go compute it from the body we write.
		if string(key) == "Content-Length" {
			return
		}
		w.Header().Add(string(key), string(value))
	})
	status := ctx.Response.StatusCode()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(ctx.Response.Body())
}
