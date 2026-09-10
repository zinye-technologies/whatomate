package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// CannedResponseButton mirrors the chatbot flow ButtonConfig shape.
// type is one of "reply", "url", "phone", "voice_call", "flow". For voice_call,
// Title is the on-button label (Meta's display_text, 20-char cap applied at
// send time) and TTLMinutes is how long the button stays clickable (0 ⇒
// Meta default, 15 min). For flow, Title is the CTA label, FlowID is the Meta
// flow id to launch and Screen is the first screen to open.
type CannedResponseButton struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Type        string `json:"type,omitempty"`
	URL         string `json:"url,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	TTLMinutes  int    `json:"ttl_minutes,omitempty"`
	// flow only. Like voice_call, a flow button is exclusive — it can't share
	// a message with other button types.
	FlowID string `json:"flow_id,omitempty"`
	Screen string `json:"screen,omitempty"`
}

// CannedResponseRequest represents the request body for creating/updating a canned response
type CannedResponseRequest struct {
	Name     string                 `json:"name"`
	Shortcut string                 `json:"shortcut"`
	Content  string                 `json:"content"`
	Category string                 `json:"category"`
	IsActive bool                   `json:"is_active"`
	Buttons  []CannedResponseButton `json:"buttons"`
}

// CannedResponseResponse represents the API response for a canned response
type CannedResponseResponse struct {
	ID         uuid.UUID              `json:"id"`
	Name       string                 `json:"name"`
	Shortcut   string                 `json:"shortcut"`
	Content    string                 `json:"content"`
	Category   string                 `json:"category"`
	IsActive   bool                   `json:"is_active"`
	UsageCount int                    `json:"usage_count"`
	Buttons    []CannedResponseButton `json:"buttons"`
	CreatedAt  string                 `json:"created_at"`
	UpdatedAt  string                 `json:"updated_at"`
}

// ListCannedResponses returns all canned responses for the organization
func (a *App) ListCannedResponses(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	pg := parsePaginationHTTP(r)

	// Optional filters
	category := r.URL.Query().Get("category")
	search := r.URL.Query().Get("search")
	activeOnly := r.URL.Query().Get("active_only")

	query := a.DB.Where("organization_id = ?", orgID)

	// By default show all, but allow filtering to active only (for chat picker)
	if activeOnly == "true" {
		query = query.Where("is_active = ?", true)
	}

	if category != "" {
		query = query.Where("category = ?", category)
	}
	if search != "" {
		searchPattern := "%" + search + "%"
		query = query.Where("name ILIKE ? OR content ILIKE ? OR shortcut ILIKE ?",
			searchPattern, searchPattern, searchPattern)
	}

	var total int64
	query.Model(&models.CannedResponse{}).Count(&total)

	var responses []models.CannedResponse
	if err := pg.Apply(query.Order("usage_count DESC, name ASC")).
		Find(&responses).Error; err != nil {
		a.Log.Error("Failed to list canned responses", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list canned responses", nil, "")
		return
	}

	result := make([]CannedResponseResponse, len(responses))
	for i, cr := range responses {
		result[i] = cannedResponseToResponse(cr)
	}

	SendEnvelope(w, listEnvelope("canned_responses", result, total, pg))
	return
}

// CreateCannedResponse creates a new canned response
func (a *App) CreateCannedResponse(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	var req CannedResponseRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.Name == "" || req.Content == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "name and content are required", nil, "")
		return
	}

	if err := validateCannedResponseButtons(req.Buttons); err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, err.Error(), nil, "")
		return
	}

	// Check for duplicate name
	var existing models.CannedResponse
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, req.Name).
		First(&existing).Error; err == nil {
		SendErrorEnvelope(w, http.StatusConflict, "Canned response with this name already exists", nil, "")
		return
	}

	cannedResponse := models.CannedResponse{
		OrganizationID: orgID,
		Name:           req.Name,
		Shortcut:       req.Shortcut,
		Content:        req.Content,
		Category:       req.Category,
		IsActive:       true,
		Buttons:        buttonsToJSONBArray(req.Buttons),
		CreatedByID:    userID,
	}

	if err := a.DB.Create(&cannedResponse).Error; err != nil {
		a.Log.Error("Failed to create canned response", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create canned response", nil, "")
		return
	}

	a.logAudit(orgID, userID,
		"canned_response", cannedResponse.ID, models.AuditActionCreated, nil, cannedResponseAuditSnapshot(&cannedResponse))

	SendEnvelope(w, cannedResponseToResponse(cannedResponse))
	return
}

// GetCannedResponse returns a single canned response
func (a *App) GetCannedResponse(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "canned response")
	if err != nil {
		return
	}

	var cannedResponse models.CannedResponse
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).
		First(&cannedResponse).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Canned response not found", nil, "")
		return
	}

	SendEnvelope(w, cannedResponseToResponse(cannedResponse))
	return
}

// UpdateCannedResponse updates an existing canned response
func (a *App) UpdateCannedResponse(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "canned response")
	if err != nil {
		return
	}

	var cannedResponse models.CannedResponse
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).
		First(&cannedResponse).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Canned response not found", nil, "")
		return
	}

	var req CannedResponseRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if err := validateCannedResponseButtons(req.Buttons); err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, err.Error(), nil, "")
		return
	}

	oldSnap := cannedResponseAuditSnapshot(&cannedResponse)

	// Update fields
	if req.Name != "" {
		cannedResponse.Name = req.Name
	}
	cannedResponse.Shortcut = req.Shortcut
	if req.Content != "" {
		cannedResponse.Content = req.Content
	}
	cannedResponse.Category = req.Category
	cannedResponse.IsActive = req.IsActive
	cannedResponse.Buttons = buttonsToJSONBArray(req.Buttons)

	if err := a.DB.Save(&cannedResponse).Error; err != nil {
		a.Log.Error("Failed to update canned response", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update canned response", nil, "")
		return
	}

	a.logAudit(orgID, userID,
		"canned_response", cannedResponse.ID, models.AuditActionUpdated, oldSnap, cannedResponseAuditSnapshot(&cannedResponse))

	SendEnvelope(w, cannedResponseToResponse(cannedResponse))
	return
}

// DeleteCannedResponse deletes a canned response
func (a *App) DeleteCannedResponse(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "canned response")
	if err != nil {
		return
	}

	var cannedResponse models.CannedResponse
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).
		First(&cannedResponse).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Canned response not found", nil, "")
		return
	}

	if err := a.DB.Delete(&cannedResponse).Error; err != nil {
		a.Log.Error("Failed to delete canned response", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete canned response", nil, "")
		return
	}

	a.logAudit(orgID, userID,
		"canned_response", cannedResponse.ID, models.AuditActionDeleted, cannedResponseAuditSnapshot(&cannedResponse), nil)

	SendEnvelope(w, map[string]string{"message": "Canned response deleted"})
	return
}

// IncrementCannedResponseUsage increments the usage counter
func (a *App) IncrementCannedResponseUsage(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "canned response")
	if err != nil {
		return
	}

	if err := a.DB.Model(&models.CannedResponse{}).
		Where("id = ? AND organization_id = ?", id, orgID).
		UpdateColumn("usage_count", gorm.Expr("usage_count + 1")).Error; err != nil {
		a.Log.Error("Failed to update usage", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update usage", nil, "")
		return
	}

	SendEnvelope(w, map[string]string{"message": "Usage incremented"})
	return
}

// cannedResponseAuditSnapshot returns a diff-friendly representation of a
// canned response for audit logging. Noisy fields (usage_count, timestamps) are
// intentionally excluded so the activity log reflects user edits only.
//
// Note: the buttons array is serialised under "button_config" because the
// shared audit "buttons" field is on the global skipFields list (chatbot flow
// step buttons are noisy on every edit). Stringifying gives a readable
// before/after in the activity log.
func cannedResponseAuditSnapshot(cr *models.CannedResponse) map[string]any {
	if cr == nil {
		return nil
	}
	return map[string]any{
		"name":          cr.Name,
		"shortcut":      cr.Shortcut,
		"content":       cr.Content,
		"category":      cr.Category,
		"is_active":     cr.IsActive,
		"button_config": buttonsToAuditString(cr.Buttons),
	}
}

func cannedResponseToResponse(cr models.CannedResponse) CannedResponseResponse {
	return CannedResponseResponse{
		ID:         cr.ID,
		Name:       cr.Name,
		Shortcut:   cr.Shortcut,
		Content:    cr.Content,
		Category:   cr.Category,
		IsActive:   cr.IsActive,
		UsageCount: cr.UsageCount,
		Buttons:    jsonbArrayToButtons(cr.Buttons),
		CreatedAt:  cr.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:  cr.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// buttonsToJSONBArray converts the typed request shape into the JSONBArray
// column. We round-trip through JSON so the stored shape matches what the
// chatbot flow steps use (and what the frontend / whatsapp client expect).
func buttonsToJSONBArray(buttons []CannedResponseButton) models.JSONBArray {
	if len(buttons) == 0 {
		return models.JSONBArray{}
	}
	arr := make(models.JSONBArray, 0, len(buttons))
	for _, b := range buttons {
		raw, err := json.Marshal(b)
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		arr = append(arr, m)
	}
	return arr
}

func jsonbArrayToButtons(arr models.JSONBArray) []CannedResponseButton {
	out := make([]CannedResponseButton, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		raw, err := json.Marshal(m)
		if err != nil {
			continue
		}
		var b CannedResponseButton
		if err := json.Unmarshal(raw, &b); err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

// buttonsToAuditString renders the buttons array as a compact comparable
// string (e.g. "Yes [reply], Open (https://x.com) [url]") so the audit diff
// records a single readable change rather than a deep JSON blob.
func buttonsToAuditString(arr models.JSONBArray) string {
	buttons := jsonbArrayToButtons(arr)
	if len(buttons) == 0 {
		return ""
	}
	parts := make([]string, 0, len(buttons))
	for _, b := range buttons {
		t := b.Type
		if t == "" {
			t = "reply"
		}
		switch t {
		case "url":
			parts = append(parts, b.Title+" ("+b.URL+") [url]")
		case "phone":
			parts = append(parts, b.Title+" ("+b.PhoneNumber+") [phone]")
		case "voice_call":
			label := b.Title + " [voice_call"
			if b.TTLMinutes > 0 {
				label += ", " + strconv.Itoa(b.TTLMinutes) + "m"
			}
			parts = append(parts, label+"]")
		default:
			parts = append(parts, b.Title+" [reply]")
		}
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// validateCannedResponseButtons applies the shared free-form interactive
// button rules to a canned response. The rules themselves live in
// validateInteractiveButtons so the chatbot greeting/fallback path enforces
// exactly the same set.
func validateCannedResponseButtons(buttons []CannedResponseButton) error {
	converted := make([]InteractiveButton, 0, len(buttons))
	for _, b := range buttons {
		converted = append(converted, InteractiveButton{
			ID:          b.ID,
			Title:       b.Title,
			Type:        b.Type,
			URL:         b.URL,
			PhoneNumber: b.PhoneNumber,
			TTLMinutes:  b.TTLMinutes,
			FlowID:      b.FlowID,
		})
	}
	return validateInteractiveButtons(converted)
}
