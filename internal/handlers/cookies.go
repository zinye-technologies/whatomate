package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

const (
	cookieAccessName  = "whm_access"
	cookieRefreshName = "whm_refresh"
	cookieCSRFName    = "whm_csrf"
)

// setAuthCookiesHTTP sets httpOnly auth cookies and a JS-readable CSRF cookie (net/http).
func (a *App) setAuthCookiesHTTP(w http.ResponseWriter, accessToken, refreshToken string) {
	secure := a.Config.Cookie.Secure
	domain := a.Config.Cookie.Domain
	bp := a.Config.Server.BasePath

	http.SetCookie(w, &http.Cookie{
		Name:     cookieAccessName,
		Value:    accessToken,
		Path:     bp + "/api",
		Domain:   domain,
		MaxAge:   a.Config.JWT.AccessExpiryMins * 60,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieRefreshName,
		Value:    refreshToken,
		Path:     bp + "/api/auth/refresh",
		Domain:   domain,
		MaxAge:   a.Config.JWT.RefreshExpiryDays * 86400,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieCSRFName,
		Value:    generateCSRFToken(),
		Path:     bp + "/",
		Domain:   domain,
		MaxAge:   a.Config.JWT.RefreshExpiryDays * 86400,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearAuthCookiesHTTP expires all auth cookies (net/http).
func (a *App) clearAuthCookiesHTTP(w http.ResponseWriter) {
	domain := a.Config.Cookie.Domain
	bp := a.Config.Server.BasePath
	secure := a.Config.Cookie.Secure
	for _, name := range []string{cookieAccessName, cookieRefreshName, cookieCSRFName} {
		path := bp + "/"
		httpOnly := true
		switch name {
		case cookieAccessName:
			path = bp + "/api"
		case cookieRefreshName:
			path = bp + "/api/auth/refresh"
		default:
			httpOnly = false
		}
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     path,
			Domain:   domain,
			MaxAge:   -1,
			HttpOnly: httpOnly,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// generateCSRFToken returns 32 random bytes, base64url encoded.
func generateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand.Read failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
