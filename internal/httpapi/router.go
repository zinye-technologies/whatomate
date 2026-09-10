package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"github.com/zerodha/logf"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/middleware"
)

// Deps bundles dependencies needed to mount HTTP routes.
type Deps struct {
	App    *handlers.App
	Log    logf.Logger
	Config *config.Config
	Redis  *redis.Client
}

// NewRouter builds the chi router with global middleware and all application routes.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	allowedOrigins := middleware.ParseAllowedOrigins(d.Config.Server.AllowedOrigins)

	r.Use(middleware.RequestID)
	r.Use(middleware.RecoverHTTP(d.Log))
	r.Use(middleware.SecurityHeadersHTTP)
	r.Use(middleware.CORSHTTP(allowedOrigins))
	r.Use(middleware.CSRFProtectionHTTP)

	mountRoutes(r, d)
	return r
}

// NewServer constructs a net/http Server with timeouts from config.
func NewServer(addr string, handler http.Handler, cfg *config.Config) *http.Server {
	read := time.Duration(cfg.Server.ReadTimeout) * time.Second
	write := time.Duration(cfg.Server.WriteTimeout) * time.Second
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       read,
		WriteTimeout:      write,
		ReadHeaderTimeout: 10 * time.Second,
		// 15 MiB matches the previous fasthttp MaxRequestBodySize.
		MaxHeaderBytes: 1 << 20,
	}
}
