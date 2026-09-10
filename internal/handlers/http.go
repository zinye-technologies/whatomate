package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/shridarpatil/whatomate/internal/middleware"
)

// Envelope mirrors fastglue's JSON response shape for native net/http handlers.
type Envelope struct {
	Status    string  `json:"status"`
	Message   *string `json:"message,omitempty"`
	Data      any     `json:"data"`
	ErrorType *string `json:"error_type,omitempty"`
}

// SendEnvelope writes a success envelope (status=success) with HTTP 200.
func SendEnvelope(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, Envelope{Status: "success", Data: data})
}

// SendErrorEnvelope writes an error envelope with the given status code.
func SendErrorEnvelope(w http.ResponseWriter, code int, message string, data any, errorType string) {
	e := Envelope{Status: "error", Message: &message, Data: data}
	if errorType != "" {
		e.ErrorType = &errorType
	}
	writeJSON(w, code, e)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON decodes a JSON request body. On failure it writes a 400 envelope
// and returns errEnvelopeSent.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid request body", nil, "")
		return errEnvelopeSent
	}
	return nil
}

// getOrgIDHTTP extracts organization ID from net/http context (set by AuthHTTP).
// Super admins / members can override via X-Organization-ID (same rules as getOrgID).
func (a *App) getOrgIDHTTP(r *http.Request) (uuid.UUID, error) {
	defaultOrgID, ok := middleware.OrganizationIDFromContext(r.Context())
	if !ok {
		return uuid.Nil, errors.New("organization_id not found in context")
	}

	userID, _ := middleware.UserIDFromContext(r.Context())
	overrideOrgID := r.Header.Get("X-Organization-ID")
	if overrideOrgID != "" {
		parsedOrgID, err := uuid.Parse(overrideOrgID)
		if err == nil && parsedOrgID != defaultOrgID {
			if a.IsSuperAdmin(userID) {
				var count int64
				if err := a.DB.Table("organizations").Where("id = ?", parsedOrgID).Count(&count).Error; err == nil && count > 0 {
					return parsedOrgID, nil
				}
			} else {
				var count int64
				if err := a.DB.Table("user_organizations").
					Where("user_id = ? AND organization_id = ? AND deleted_at IS NULL", userID, parsedOrgID).
					Count(&count).Error; err == nil && count > 0 {
					return parsedOrgID, nil
				}
			}
		}
	}
	return defaultOrgID, nil
}

// getOrgAndUserIDHTTP extracts org + user IDs from net/http auth context.
func (a *App) getOrgAndUserIDHTTP(r *http.Request) (orgID, userID uuid.UUID, err error) {
	orgID, err = a.getOrgIDHTTP(r)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		return uuid.Nil, uuid.Nil, errors.New("user_id not found in context")
	}
	return orgID, userID, nil
}

// requireAuthHTTP extracts org/user and checks permission; writes 401/403 on failure.
func (a *App) requireAuthHTTP(w http.ResponseWriter, r *http.Request, resource, action string) (orgID, userID uuid.UUID, err error) {
	orgID, userID, err = a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return uuid.Nil, uuid.Nil, errEnvelopeSent
	}
	if !a.HasPermission(userID, resource, action, orgID) {
		SendErrorEnvelope(w, http.StatusForbidden, "Insufficient permissions", nil, "")
		return uuid.Nil, uuid.Nil, errEnvelopeSent
	}
	return orgID, userID, nil
}

// decodeRequestHTTP decodes JSON into v or writes a 400 envelope.
func (a *App) decodeRequestHTTP(w http.ResponseWriter, r *http.Request, v any) error {
	return DecodeJSON(w, r, v)
}
