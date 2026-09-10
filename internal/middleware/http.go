package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/logf"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/shridarpatil/whatomate/internal/models"
)

// Typed context keys for net/http middleware. String UserValue keys remain
// ContextKey* for fasthttp handlers reached through httpapi.Wrap.
type ctxKey int

const (
	ctxKeyUserID ctxKey = iota + 1
	ctxKeyOrganizationID
	ctxKeyEmail
	ctxKeyRoleID
	ctxKeyIsSuperAdmin
	ctxKeyUser
	ctxKeyOrganization
	ctxKeyRequestID
)

// RequestIDHeader is the response/request header used for request IDs.
const RequestIDHeader = "X-Request-ID"

// RequestID ensures every request has an X-Request-ID (chi-compatible pattern).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestID returns the request ID from context, if any.
func GetRequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// RecoverHTTP recovers from panics and returns a JSON 500 envelope.
func RecoverHTTP(log logf.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Error("Panic recovered", "error", err, "path", r.URL.Path)
					writeJSONError(w, http.StatusInternalServerError, "Internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeadersHTTP adds standard security headers.
func SecurityHeadersHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(self), geolocation=()")
		h.Set("X-XSS-Protection", "0")
		next.ServeHTTP(w, r)
	})
}

// CORSHTTP handles CORS with the same allow-list semantics as the fasthttp wrapper.
func CORSHTTP(allowedOrigins map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && IsOriginAllowed(origin, allowedOrigins) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			} else if len(allowedOrigins) == 0 && origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Organization-ID, X-CSRF-Token")
			w.Header().Set("Access-Control-Max-Age", "86400")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRFProtectionHTTP validates double-submit CSRF tokens for cookie-authenticated mutating requests.
func CSRFProtectionHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if method != http.MethodPost && method != http.MethodPut && method != http.MethodDelete && method != http.MethodPatch {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie("whm_access"); err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		csrfCookie, _ := r.Cookie("whm_csrf")
		csrfHeader := r.Header.Get("X-CSRF-Token")
		if csrfCookie == nil || csrfCookie.Value == "" || csrfHeader == "" || csrfCookie.Value != csrfHeader {
			writeJSONError(w, http.StatusForbidden, "CSRF token mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AuthHTTP validates JWT (Bearer or whm_access cookie) and optional API keys.
// On success it stores identity on the request context (and httpapi.Wrap copies
// those values into fasthttp UserValues for legacy handlers).
func AuthHTTP(secret string, db *gorm.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := r.Header.Get("X-API-Key")
			if apiKey != "" && db != nil {
				ctx, ok := validateAPIKeyHTTP(r.Context(), apiKey, db)
				if !ok {
					writeJSONError(w, http.StatusUnauthorized, "Invalid API key")
					return
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			tokenString := ""
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.Split(authHeader, " ")
				if len(parts) != 2 || parts[0] != "Bearer" {
					writeJSONError(w, http.StatusUnauthorized, "Invalid authorization header format")
					return
				}
				tokenString = parts[1]
			} else if c, err := r.Cookie("whm_access"); err == nil {
				tokenString = c.Value
			}

			if tokenString == "" {
				writeJSONError(w, http.StatusUnauthorized, "Missing authorization")
				return
			}

			token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (any, error) {
				return []byte(secret), nil
			})
			if err != nil || !token.Valid {
				writeJSONError(w, http.StatusUnauthorized, "Invalid or expired token")
				return
			}
			claims, ok := token.Claims.(*JWTClaims)
			if !ok {
				writeJSONError(w, http.StatusUnauthorized, "Invalid token claims")
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, ctxKeyUserID, claims.UserID)
			ctx = context.WithValue(ctx, ctxKeyOrganizationID, claims.OrganizationID)
			ctx = context.WithValue(ctx, ctxKeyEmail, claims.Email)
			if claims.RoleID != nil {
				ctx = context.WithValue(ctx, ctxKeyRoleID, *claims.RoleID)
			}
			ctx = context.WithValue(ctx, ctxKeyIsSuperAdmin, claims.IsSuperAdmin)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func validateAPIKeyHTTP(ctx context.Context, key string, db *gorm.DB) (context.Context, bool) {
	if len(key) != 36 || key[:4] != "whm_" {
		return ctx, false
	}
	newPrefix := key[4:20]
	oldPrefix := key[4:12]

	var apiKeys []models.APIKey
	if err := db.Preload("User").Where("(key_prefix = ? OR key_prefix = ?) AND is_active = ?", newPrefix, oldPrefix, true).Find(&apiKeys).Error; err != nil {
		return ctx, false
	}

	for _, apiKey := range apiKeys {
		if err := bcrypt.CompareHashAndPassword([]byte(apiKey.KeyHash), []byte(key)); err != nil {
			continue
		}
		if apiKey.ExpiresAt != nil && time.Now().After(*apiKey.ExpiresAt) {
			return ctx, false
		}
		go func(id uuid.UUID) {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			now := time.Now()
			db.WithContext(c).Model(&models.APIKey{}).Where("id = ?", id).Update("last_used_at", now)
		}(apiKey.ID)

		if apiKey.User == nil {
			return ctx, false
		}
		ctx = context.WithValue(ctx, ctxKeyUserID, apiKey.UserID)
		ctx = context.WithValue(ctx, ctxKeyOrganizationID, apiKey.OrganizationID)
		ctx = context.WithValue(ctx, ctxKeyEmail, apiKey.User.Email)
		if apiKey.User.RoleID != nil {
			ctx = context.WithValue(ctx, ctxKeyRoleID, *apiKey.User.RoleID)
		}
		ctx = context.WithValue(ctx, ctxKeyIsSuperAdmin, apiKey.User.IsSuperAdmin)
		return ctx, true
	}
	return ctx, false
}

// RateLimitHTTP enforces a fixed-window limit per client IP.
func RateLimitHTTP(opts RateLimitOpts) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractClientIPHTTP(r, opts.TrustProxy)
			key := fmt.Sprintf("ratelimit:%s:%s", opts.KeyPrefix, ip)
			if !enforceHTTP(opts, w, key) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UserAwareRateLimitHTTP keys by authenticated user ID when present, else IP.
func UserAwareRateLimitHTTP(opts RateLimitOpts) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := ""
			if uid, ok := r.Context().Value(ctxKeyUserID).(uuid.UUID); ok && uid != uuid.Nil {
				identity = "u:" + uid.String()
			} else {
				identity = "ip:" + extractClientIPHTTP(r, opts.TrustProxy)
			}
			key := fmt.Sprintf("ratelimit:%s:%s", opts.KeyPrefix, identity)
			if !enforceHTTP(opts, w, key) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func enforceHTTP(opts RateLimitOpts, w http.ResponseWriter, key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	count, err := opts.Redis.Incr(ctx, key).Result()
	if err != nil {
		opts.Log.Error("Rate limit Redis INCR failed", "error", err, "key", key)
		return true
	}
	if count == 1 {
		if err := opts.Redis.Expire(ctx, key, opts.Window).Err(); err != nil {
			opts.Log.Error("Rate limit Redis EXPIRE failed", "error", err, "key", key)
		}
	}
	if count > int64(opts.Max) {
		ttl, err := opts.Redis.TTL(ctx, key).Result()
		if err != nil || ttl < 0 {
			ttl = opts.Window
		}
		retryAfter := max(int(ttl.Seconds()), 1)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		writeJSONError(w, http.StatusTooManyRequests, "Too many requests. Please try again later.")
		return false
	}
	return true
}

func extractClientIPHTTP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.SplitN(xff, ",", 2)
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// CopyHTTPContextToUserValues bridges stdlib auth context into fasthttp UserValues
// so legacy fastglue handlers keep reading the same keys.
func CopyHTTPContextToUserValues(r *http.Request, ctx *fasthttp.RequestCtx) {
	c := r.Context()
	if v := c.Value(ctxKeyUserID); v != nil {
		ctx.SetUserValue(ContextKeyUserID, v)
	}
	if v := c.Value(ctxKeyOrganizationID); v != nil {
		ctx.SetUserValue(ContextKeyOrganizationID, v)
	}
	if v := c.Value(ctxKeyEmail); v != nil {
		ctx.SetUserValue(ContextKeyEmail, v)
	}
	if v := c.Value(ctxKeyRoleID); v != nil {
		ctx.SetUserValue(ContextKeyRoleID, v)
	}
	if v := c.Value(ctxKeyIsSuperAdmin); v != nil {
		ctx.SetUserValue(ContextKeyIsSuperAdmin, v)
	}
	if v := c.Value(ctxKeyUser); v != nil {
		ctx.SetUserValue(ContextKeyUser, v)
	}
	if v := c.Value(ctxKeyOrganization); v != nil {
		ctx.SetUserValue(ContextKeyOrganization, v)
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "error",
		"message": message,
	})
}
