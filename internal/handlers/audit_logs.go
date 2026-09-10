package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// AuditLogResponse represents an audit log entry in API response
type AuditLogResponse struct {
	ID           uuid.UUID          `json:"id"`
	ResourceType string             `json:"resource_type"`
	ResourceID   uuid.UUID          `json:"resource_id"`
	UserID       uuid.UUID          `json:"user_id"`
	UserName     string             `json:"user_name"`
	Action       models.AuditAction `json:"action"`
	Changes      models.JSONBArray  `json:"changes"`
	CreatedAt    time.Time          `json:"created_at"`
}

// ListAuditLogs returns audit logs with optional filters.
// Supported query params: resource_type, resource_id, user_id, action, from, to, page, limit.
func (a *App) ListAuditLogs(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceAuditLogs, models.ActionRead)
	if err != nil {
		return
	}

	// Build query with optional filters
	baseQuery := a.DB.Where("organization_id = ?", orgID)

	if v := r.URL.Query().Get("resource_type"); v != "" {
		baseQuery = baseQuery.Where("resource_type = ?", v)
	}

	if v := r.URL.Query().Get("resource_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			baseQuery = baseQuery.Where("resource_id = ?", id)
		}
	}

	if v := r.URL.Query().Get("user_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			baseQuery = baseQuery.Where("user_id = ?", id)
		}
	}

	if v := r.URL.Query().Get("action"); v != "" {
		baseQuery = baseQuery.Where("action = ?", v)
	}

	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.DateOnly, v); err == nil {
			baseQuery = baseQuery.Where("created_at >= ?", t)
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			baseQuery = baseQuery.Where("created_at >= ?", t)
		}
	}

	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.DateOnly, v); err == nil {
			// End of day
			baseQuery = baseQuery.Where("created_at <= ?", t.Add(24*time.Hour-time.Second))
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			baseQuery = baseQuery.Where("created_at <= ?", t)
		}
	}

	pg := parsePaginationHTTP(r)

	var logs []models.AuditLog
	var total int64

	countQuery := baseQuery.Session(&gorm.Session{})
	countQuery.Model(&models.AuditLog{}).Count(&total)

	if err := pg.Apply(baseQuery.Order("created_at DESC")).Find(&logs).Error; err != nil {
		a.Log.Error("Failed to list audit logs", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list audit logs", nil, "")
		return
	}

	response := make([]AuditLogResponse, len(logs))
	for i, l := range logs {
		response[i] = AuditLogResponse{
			ID:           l.ID,
			ResourceType: l.ResourceType,
			ResourceID:   l.ResourceID,
			UserID:       l.UserID,
			UserName:     l.UserName,
			Action:       l.Action,
			Changes:      l.Changes,
			CreatedAt:    l.CreatedAt,
		}
	}

	SendEnvelope(w, listEnvelope("audit_logs", response, total, pg))
	return
}

// GetAuditLog returns a single audit log entry by ID
func (a *App) GetAuditLog(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceAuditLogs, models.ActionRead)
	if err != nil {
		return
	}

	logID, err := parsePathUUIDHTTP(w, r, "id", "audit log")
	if err != nil {
		return
	}

	var log models.AuditLog
	if err := a.DB.Where("id = ? AND organization_id = ?", logID, orgID).First(&log).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Audit log not found", nil, "")
		return
	}

	SendEnvelope(w, AuditLogResponse{
		ID:           log.ID,
		ResourceType: log.ResourceType,
		ResourceID:   log.ResourceID,
		UserID:       log.UserID,
		UserName:     log.UserName,
		Action:       log.Action,
		Changes:      log.Changes,
		CreatedAt:    log.CreatedAt,
	})
	return
}
