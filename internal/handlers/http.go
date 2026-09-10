package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

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

// parsePathUUIDHTTP extracts a UUID path param via chi. On failure writes 400.
func parsePathUUIDHTTP(w http.ResponseWriter, r *http.Request, param, label string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid "+label+" ID", nil, "")
		return uuid.Nil, errEnvelopeSent
	}
	return id, nil
}

// parsePaginationHTTP extracts page/limit from query params (default 50, max 100).
func parsePaginationHTTP(r *http.Request) Pagination {
	return parsePaginationWithDefaultsHTTP(r, 50, 100)
}

// parsePaginationWithDefaultsHTTP extracts page-based pagination with custom defaults.
func parsePaginationWithDefaultsHTTP(r *http.Request, defaultLimit, maxLimit int) Pagination {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > maxLimit {
		limit = defaultLimit
	}
	return Pagination{
		Page:   page,
		Limit:  limit,
		Offset: (page - 1) * limit,
	}
}

// findByIDAndOrgHTTP fetches a record by id+org; writes 404 on miss.
func findByIDAndOrgHTTP[T any](db *gorm.DB, w http.ResponseWriter, id, orgID uuid.UUID, label string) (*T, error) {
	var model T
	if err := db.Where("id = ? AND organization_id = ?", id, orgID).First(&model).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, label+" not found", nil, "")
		return nil, errEnvelopeSent
	}
	return &model, nil
}

// parseSuperAdminFieldHTTP extracts is_super_admin from a raw JSON body.
func parseSuperAdminFieldHTTP(body []byte) *bool {
	var f superAdminField
	if err := json.Unmarshal(body, &f); err != nil {
		return nil
	}
	return f.IsSuperAdmin
}

// readBody reads the full request body (for handlers that previously used PostBody).
func readBody(r *http.Request) []byte {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	return b
}

// parseDateParamHTTP parses a YYYY-MM-DD date from a query parameter.
func parseDateParamHTTP(r *http.Request, param string) (time.Time, bool) {
	s := r.URL.Query().Get(param)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
