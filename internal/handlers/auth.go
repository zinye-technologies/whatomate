package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// LoginRequest represents login credentials
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=12"`
}

// RegisterRequest represents registration data
type RegisterRequest struct {
	Email          string    `json:"email" validate:"required,email"`
	Password       string    `json:"password" validate:"required,min=12"`
	FullName       string    `json:"full_name" validate:"required"`
	OrganizationID uuid.UUID `json:"organization_id" validate:"required"`
}

// CookieAuthResponse represents authentication response when tokens are in cookies.
// No tokens in the body — only the expiry hint and user object.
type CookieAuthResponse struct {
	ExpiresIn int         `json:"expires_in"`
	User      models.User `json:"user"`
}

// RefreshRequest represents token refresh request
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Login authenticates a user and returns tokens (native net/http).
func (a *App) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	var user models.User
	if err := a.DB.Preload("Role").Where("email = ?", req.Email).First(&user).Error; err != nil {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), []byte(req.Password))
		SendErrorEnvelope(w, http.StatusUnauthorized, "Invalid credentials", nil, "")
		return
	}

	if user.Role != nil && user.RoleID != nil {
		cachedPerms, err := a.GetRolePermissionsCached(*user.RoleID)
		if err == nil {
			permissions := make([]models.Permission, 0, len(cachedPerms))
			for _, p := range cachedPerms {
				for i := len(p) - 1; i >= 0; i-- {
					if p[i] == ':' {
						permissions = append(permissions, models.Permission{
							Resource: p[:i],
							Action:   p[i+1:],
						})
						break
					}
				}
			}
			user.Role.Permissions = permissions
		}
	}

	if !user.IsActive {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Account is disabled", nil, "")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Invalid credentials", nil, "")
		return
	}

	accessToken, err := a.generateAccessToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate access token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}

	refreshToken, err := a.generateRefreshToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate refresh token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}

	a.setAuthCookiesHTTP(w, accessToken, refreshToken)

	SendEnvelope(w, CookieAuthResponse{
		ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
		User:      user,
	})
}

// Register creates a new user in an existing organization (native net/http).
func (a *App) Register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.OrganizationID == uuid.Nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "organization_id is required", nil, "")
		return
	}

	var org models.Organization
	if err := a.DB.Where("id = ?", req.OrganizationID).First(&org).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Organization not found", nil, "")
		return
	}

	var defaultRole models.CustomRole
	if err := a.DB.Where("organization_id = ? AND is_default = ?", req.OrganizationID, true).First(&defaultRole).Error; err != nil {
		if err := a.DB.Where("organization_id = ? AND name = ? AND is_system = ?", req.OrganizationID, "agent", true).First(&defaultRole).Error; err != nil {
			a.Log.Error("Failed to find default role", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to find default role", nil, "")
			return
		}
	}

	var existingUser models.User
	if err := a.DB.Where("email = ?", req.Email).First(&existingUser).Error; err == nil {
		if err := bcrypt.CompareHashAndPassword([]byte(existingUser.PasswordHash), []byte(req.Password)); err != nil {
			SendErrorEnvelope(w, http.StatusConflict, "An account with this email already exists. Please sign in and ask your organization admin to add you.", nil, "")
			return
		}

		if !existingUser.IsActive {
			SendErrorEnvelope(w, http.StatusUnauthorized, "Account is disabled", nil, "")
			return
		}

		var count int64
		a.DB.Model(&models.UserOrganization{}).
			Where("user_id = ? AND organization_id = ?", existingUser.ID, req.OrganizationID).
			Count(&count)
		if count > 0 {
			SendErrorEnvelope(w, http.StatusConflict, "You are already a member of this organization", nil, "")
			return
		}

		userOrg := models.UserOrganization{
			UserID:         existingUser.ID,
			OrganizationID: req.OrganizationID,
			RoleID:         &defaultRole.ID,
			IsDefault:      false,
		}
		if err := a.DB.Create(&userOrg).Error; err != nil {
			a.Log.Error("Failed to add existing user to organization", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to join organization", nil, "")
			return
		}

		a.Log.Info("Existing user joined organization", "user_id", existingUser.ID, "org_id", req.OrganizationID)

		existingUser.OrganizationID = req.OrganizationID
		existingUser.Role = &defaultRole
		existingUser.RoleID = &defaultRole.ID

		accessToken, err := a.generateAccessToken(&existingUser)
		if err != nil {
			a.Log.Error("Failed to generate access token", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
			return
		}
		refreshToken, err := a.generateRefreshToken(&existingUser)
		if err != nil {
			a.Log.Error("Failed to generate refresh token", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
			return
		}

		a.setAuthCookiesHTTP(w, accessToken, refreshToken)

		SendEnvelope(w, CookieAuthResponse{
			ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
			User:      existingUser,
		})
		return
	}

	_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), []byte(req.Password))

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		a.Log.Error("Failed to hash password", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create account", nil, "")
		return
	}

	tx := a.DB.Begin()
	if tx.Error != nil {
		a.Log.Error("Failed to begin transaction", "error", tx.Error)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create account", nil, "")
		return
	}

	user := models.User{
		OrganizationID: req.OrganizationID,
		Email:          req.Email,
		PasswordHash:   string(hashedPassword),
		FullName:       req.FullName,
		RoleID:         &defaultRole.ID,
		IsActive:       true,
	}

	if err := tx.Create(&user).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to create user", "error", err, "email", req.Email, "org_id", req.OrganizationID)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create account", nil, "")
		return
	}

	userOrg := models.UserOrganization{
		UserID:         user.ID,
		OrganizationID: req.OrganizationID,
		RoleID:         &defaultRole.ID,
		IsDefault:      true,
	}
	if err := tx.Create(&userOrg).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to create user organization entry", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create account", nil, "")
		return
	}

	if err := tx.Commit().Error; err != nil {
		a.Log.Error("Failed to commit transaction", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create account", nil, "")
		return
	}

	a.Log.Info("Registration completed", "user_id", user.ID, "org_id", req.OrganizationID)

	user.Role = &defaultRole

	accessToken, err := a.generateAccessToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate access token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}
	refreshToken, err := a.generateRefreshToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate refresh token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}

	a.setAuthCookiesHTTP(w, accessToken, refreshToken)

	SendEnvelope(w, CookieAuthResponse{
		ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
		User:      user,
	})
}

// RefreshToken refreshes access token using refresh token with rotation (native net/http).
func (a *App) RefreshToken(w http.ResponseWriter, r *http.Request) {
	refreshTokenStr := ""
	if c, err := r.Cookie(cookieRefreshName); err == nil {
		refreshTokenStr = c.Value
	}
	if refreshTokenStr == "" {
		var req RefreshRequest
		_ = jsonDecodeSoft(r, &req)
		refreshTokenStr = req.RefreshToken
	}
	if refreshTokenStr == "" {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Missing refresh token", nil, "")
		return
	}

	token, err := jwt.ParseWithClaims(refreshTokenStr, &middleware.JWTClaims{}, func(token *jwt.Token) (any, error) {
		return []byte(a.Config.JWT.Secret), nil
	})

	if err != nil || !token.Valid {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Invalid refresh token", nil, "")
		return
	}

	claims, ok := token.Claims.(*middleware.JWTClaims)
	if !ok {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Invalid token claims", nil, "")
		return
	}

	if claims.ID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		deleted, err := a.Redis.Del(ctx, refreshTokenKey(claims.ID)).Result()
		if err != nil || deleted == 0 {
			SendErrorEnvelope(w, http.StatusUnauthorized, "Refresh token has been revoked", nil, "")
			return
		}
	}

	var user models.User
	if err := a.DB.Where("id = ?", claims.UserID).First(&user).Error; err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "User not found", nil, "")
		return
	}

	if !user.IsActive {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Account is disabled", nil, "")
		return
	}

	accessToken, err := a.generateAccessToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate access token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}
	newRefreshToken, err := a.generateRefreshToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate refresh token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}

	a.setAuthCookiesHTTP(w, accessToken, newRefreshToken)

	SendEnvelope(w, CookieAuthResponse{
		ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
		User:      user,
	})
}

func jsonDecodeSoft(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

func (a *App) generateAccessToken(user *models.User) (string, error) {
	claims := middleware.JWTClaims{
		UserID:         user.ID,
		OrganizationID: user.OrganizationID,
		Email:          user.Email,
		RoleID:         user.RoleID,
		IsSuperAdmin:   user.IsSuperAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(a.Config.JWT.AccessExpiryMins) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "whatomate",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(a.Config.JWT.Secret))
}

func (a *App) generateRefreshToken(user *models.User) (string, error) {
	jti := uuid.New().String()
	expiry := time.Duration(a.Config.JWT.RefreshExpiryDays) * 24 * time.Hour

	claims := middleware.JWTClaims{
		UserID:         user.ID,
		OrganizationID: user.OrganizationID,
		Email:          user.Email,
		RoleID:         user.RoleID,
		IsSuperAdmin:   user.IsSuperAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "whatomate",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(a.Config.JWT.Secret))
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Redis.Set(ctx, refreshTokenKey(jti), user.ID.String(), expiry).Err(); err != nil {
		a.Log.Error("Failed to store refresh token in Redis", "error", err)
	}

	return signed, nil
}

func refreshTokenKey(jti string) string {
	return fmt.Sprintf("refresh:%s", jti)
}

// SwitchOrgRequest represents the request body for switching organization
type SwitchOrgRequest struct {
	OrganizationID uuid.UUID `json:"organization_id"`
}

// SwitchOrg generates new tokens for a different organization (native net/http).
func (a *App) SwitchOrg(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	var req SwitchOrgRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.OrganizationID == uuid.Nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "organization_id is required", nil, "")
		return
	}

	var org models.Organization
	if err := a.DB.Where("id = ?", req.OrganizationID).First(&org).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Organization not found", nil, "")
		return
	}

	var user models.User
	if err := a.DB.Where("id = ?", userID).First(&user).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "User not found", nil, "")
		return
	}

	if !user.IsSuperAdmin {
		var userOrg models.UserOrganization
		if err := a.DB.Where("user_id = ? AND organization_id = ?", userID, req.OrganizationID).First(&userOrg).Error; err != nil {
			SendErrorEnvelope(w, http.StatusForbidden, "You are not a member of this organization", nil, "")
			return
		}
		if userOrg.RoleID != nil {
			user.RoleID = userOrg.RoleID
		}
	}

	user.OrganizationID = req.OrganizationID

	if user.RoleID != nil {
		var role models.CustomRole
		if err := a.DB.Where("id = ?", *user.RoleID).First(&role).Error; err == nil {
			user.Role = &role
			cachedPerms, err := a.GetRolePermissionsCached(*user.RoleID)
			if err == nil {
				permissions := make([]models.Permission, 0, len(cachedPerms))
				for _, p := range cachedPerms {
					parts := splitPermission(p)
					if len(parts) == 2 {
						permissions = append(permissions, models.Permission{
							Resource: parts[0],
							Action:   parts[1],
						})
					}
				}
				user.Role.Permissions = permissions
			}
		}
	}

	accessToken, err := a.generateAccessToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate access token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}

	refreshToken, err := a.generateRefreshToken(&user)
	if err != nil {
		a.Log.Error("Failed to generate refresh token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}

	a.setAuthCookiesHTTP(w, accessToken, refreshToken)

	SendEnvelope(w, CookieAuthResponse{
		ExpiresIn: a.Config.JWT.AccessExpiryMins * 60,
		User:      user,
	})
}

// LogoutRequest represents logout request body
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout invalidates the user's refresh token (native net/http).
func (a *App) Logout(w http.ResponseWriter, r *http.Request) {
	refreshTokenStr := ""
	if c, err := r.Cookie(cookieRefreshName); err == nil {
		refreshTokenStr = c.Value
	}
	if refreshTokenStr == "" {
		var req LogoutRequest
		_ = jsonDecodeSoft(r, &req)
		refreshTokenStr = req.RefreshToken
	}

	if refreshTokenStr != "" {
		token, _ := jwt.ParseWithClaims(refreshTokenStr, &middleware.JWTClaims{}, func(token *jwt.Token) (any, error) {
			return []byte(a.Config.JWT.Secret), nil
		})
		if token != nil {
			if claims, ok := token.Claims.(*middleware.JWTClaims); ok && claims.ID != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				a.Redis.Del(ctx, refreshTokenKey(claims.ID))
			}
		}
	}

	a.clearAuthCookiesHTTP(w)

	SendEnvelope(w, map[string]string{"status": "logged_out"})
}

func generateSlug(name string) string {
	slug := ""
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			slug += string(c)
		} else if c >= 'A' && c <= 'Z' {
			slug += string(c + 32)
		} else if c == ' ' || c == '-' {
			slug += "-"
		}
	}
	return slug + "-" + uuid.New().String()[:8]
}

// GetWSToken returns a short-lived single-use JWT for WebSocket authentication (native net/http).
func (a *App) GetWSToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}
	orgID, ok := middleware.OrganizationIDFromContext(r.Context())
	if !ok {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	claims := middleware.JWTClaims{
		UserID:         userID,
		OrganizationID: orgID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * time.Second)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "whatomate",
			Subject:   "ws",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(a.Config.JWT.Secret))
	if err != nil {
		a.Log.Error("Failed to generate WS token", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate token", nil, "")
		return
	}

	SendEnvelope(w, map[string]string{"token": signed})
}
