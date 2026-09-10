package handlers

import (
	"net/http"

	"github.com/fasthttp/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	ws "github.com/shridarpatil/whatomate/internal/websocket"
)

// newHTTPUpgrader creates a net/http WebSocket upgrader for the chi stack.
func newHTTPUpgrader(allowedOrigins map[string]bool) websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return middleware.IsOriginAllowed(r.Header.Get("Origin"), allowedOrigins)
		},
	}
}

func (a *App) wsHTTPUpgrader() websocket.Upgrader {
	allowedOrigins := middleware.ParseAllowedOrigins(a.Config.Server.AllowedOrigins)
	return newHTTPUpgrader(allowedOrigins)
}

// WebSocketHTTP handles WebSocket connections on the net/http + chi stack.
func (a *App) WebSocketHTTP(w http.ResponseWriter, r *http.Request) {
	up := a.wsHTTPUpgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		a.Log.Error("WebSocket upgrade failed", "error", err)
		return
	}
	client := ws.NewUnauthenticatedClient(a.WSHub, conn, a.validateWSTokenFn())
	go client.WritePump()
	client.ReadPump()
}

func (a *App) validateWSTokenFn() ws.AuthenticateFn {
	return func(tokenString string) (uuid.UUID, uuid.UUID, error) {
		token, err := jwt.ParseWithClaims(tokenString, &middleware.JWTClaims{}, func(token *jwt.Token) (any, error) {
			return []byte(a.Config.JWT.Secret), nil
		})
		if err != nil || !token.Valid {
			return uuid.Nil, uuid.Nil, err
		}
		claims, ok := token.Claims.(*middleware.JWTClaims)
		if !ok {
			return uuid.Nil, uuid.Nil, jwt.ErrTokenInvalidClaims
		}
		return claims.UserID, claims.OrganizationID, nil
	}
}
