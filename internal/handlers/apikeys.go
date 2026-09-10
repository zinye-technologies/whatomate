package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// APIKeyRequest represents the request body for creating an API key
type APIKeyRequest struct {
	Name      string  `json:"name"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// APIKeyResponse represents an API key in list responses
type APIKeyResponse struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	IsActive   bool       `json:"is_active"`
	CreatedAt  string     `json:"created_at"`
}

// APIKeyCreateResponse includes the full key (only shown once)
type APIKeyCreateResponse struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Key       string     `json:"key"` // Full key, only returned on create
	KeyPrefix string     `json:"key_prefix"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt string     `json:"created_at"`
}

// generateAPIKey generates a random API key with whm_ prefix
func generateAPIKey() (string, error) {
	bytes := make([]byte, 16) // 32 hex chars
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "whm_" + hex.EncodeToString(bytes), nil
}

// ListAPIKeys returns all API keys for the organization (native net/http).
func (a *App) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceAPIKeys, models.ActionRead)
	if err != nil {
		return
	}

	pg := parsePaginationHTTP(r)
	search := r.URL.Query().Get("search")

	query := a.DB.Model(&models.APIKey{}).Where("organization_id = ?", orgID)

	// Apply search filter - search by name or key prefix (case-insensitive)
	if search != "" {
		searchPattern := "%" + search + "%"
		query = query.Where("name ILIKE ? OR key_prefix ILIKE ?", searchPattern, searchPattern)
	}

	var total int64
	query.Count(&total)

	var apiKeys []models.APIKey
	if err := pg.Apply(query.Order("created_at DESC")).
		Find(&apiKeys).Error; err != nil {
		a.Log.Error("Failed to list API keys", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list API keys", nil, "")
		return
	}

	response := make([]APIKeyResponse, len(apiKeys))
	for i, key := range apiKeys {
		response[i] = APIKeyResponse{
			ID:         key.ID,
			Name:       key.Name,
			KeyPrefix:  key.KeyPrefix,
			LastUsedAt: key.LastUsedAt,
			ExpiresAt:  key.ExpiresAt,
			IsActive:   key.IsActive,
			CreatedAt:  key.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	SendEnvelope(w, listEnvelope("api_keys", response, total, pg))
}

// GetAPIKey returns a single API key by ID (native net/http).
func (a *App) GetAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceAPIKeys, models.ActionRead)
	if err != nil {
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "API key")
	if err != nil {
		return
	}

	var apiKey models.APIKey
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&apiKey).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "API key not found", nil, "")
		return
	}

	SendEnvelope(w, APIKeyResponse{
		ID:         apiKey.ID,
		Name:       apiKey.Name,
		KeyPrefix:  apiKey.KeyPrefix,
		LastUsedAt: apiKey.LastUsedAt,
		ExpiresAt:  apiKey.ExpiresAt,
		IsActive:   apiKey.IsActive,
		CreatedAt:  apiKey.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// UpdateAPIKey updates an API key (currently only is_active toggle) (native net/http).
func (a *App) UpdateAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceAPIKeys, models.ActionWrite)
	if err != nil {
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "API key")
	if err != nil {
		return
	}

	var apiKey models.APIKey
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&apiKey).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "API key not found", nil, "")
		return
	}

	var req struct {
		IsActive *bool `json:"is_active"`
	}
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.IsActive != nil {
		apiKey.IsActive = *req.IsActive
	}

	if err := a.DB.Save(&apiKey).Error; err != nil {
		a.Log.Error("Failed to update API key", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update API key", nil, "")
		return
	}

	SendEnvelope(w, APIKeyResponse{
		ID:         apiKey.ID,
		Name:       apiKey.Name,
		KeyPrefix:  apiKey.KeyPrefix,
		LastUsedAt: apiKey.LastUsedAt,
		ExpiresAt:  apiKey.ExpiresAt,
		IsActive:   apiKey.IsActive,
		CreatedAt:  apiKey.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// CreateAPIKey creates a new API key (native net/http).
func (a *App) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.requireAuthHTTP(w, r, models.ResourceAPIKeys, models.ActionWrite)
	if err != nil {
		return
	}

	var req APIKeyRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	// Validate required fields
	if req.Name == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "Name is required", nil, "")
		return
	}

	// Parse expiration date if provided
	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			SendErrorEnvelope(w, http.StatusBadRequest, "Invalid expires_at format. Use RFC3339 format", nil, "")
			return
		}
		expiresAt = &t
	}

	// Generate the API key
	fullKey, err := generateAPIKey()
	if err != nil {
		a.Log.Error("Failed to generate API key", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate API key", nil, "")
		return
	}

	// Hash the key for storage
	hashedKey, err := bcrypt.GenerateFromPassword([]byte(fullKey), bcrypt.DefaultCost)
	if err != nil {
		a.Log.Error("Failed to hash API key", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create API key", nil, "")
		return
	}

	// Extract prefix (first 16 chars after "whm_")
	keyPrefix := fullKey[4:20]

	apiKey := models.APIKey{
		OrganizationID: orgID,
		UserID:         userID,
		Name:           req.Name,
		KeyPrefix:      keyPrefix,
		KeyHash:        string(hashedKey),
		ExpiresAt:      expiresAt,
		IsActive:       true,
	}

	if err := a.DB.Create(&apiKey).Error; err != nil {
		a.Log.Error("Failed to create API key", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create API key", nil, "")
		return
	}

	// Return full key only on creation
	SendEnvelope(w, APIKeyCreateResponse{
		ID:        apiKey.ID,
		Name:      apiKey.Name,
		Key:       fullKey, // This is the only time the full key is returned
		KeyPrefix: apiKey.KeyPrefix,
		ExpiresAt: apiKey.ExpiresAt,
		CreatedAt: apiKey.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// DeleteAPIKey revokes an API key (native net/http).
func (a *App) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceAPIKeys, models.ActionDelete)
	if err != nil {
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "API key")
	if err != nil {
		return
	}

	result := a.DB.Where("id = ? AND organization_id = ?", id, orgID).Delete(&models.APIKey{})
	if result.Error != nil {
		a.Log.Error("Failed to delete API key", "error", result.Error)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete API key", nil, "")
		return
	}
	if result.RowsAffected == 0 {
		SendErrorEnvelope(w, http.StatusNotFound, "API key not found", nil, "")
		return
	}

	SendEnvelope(w, map[string]string{"message": "API key deleted successfully"})
}
