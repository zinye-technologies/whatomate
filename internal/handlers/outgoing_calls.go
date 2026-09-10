package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

// InitiateOutgoingCall handles POST /api/calls/outgoing
// Lets an agent start a voice call to a WhatsApp consumer.
func (a *App) InitiateOutgoingCall(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.requireAuthHTTP(w, r, models.ResourceOutgoingCalls, models.ActionWrite)
	if err != nil {
		return
	}

	var req struct {
		ContactID       string `json:"contact_id"`
		WhatsAppAccount string `json:"whatsapp_account"`
		SDPOffer        string `json:"sdp_offer"`
	}
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.ContactID == "" || req.WhatsAppAccount == "" || req.SDPOffer == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "contact_id, whatsapp_account, and sdp_offer are required", nil, "")
		return
	}

	if err := a.requireCallingEnabledHTTP(w, orgID); err != nil {
		return
	}

	// Look up account
	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, req.WhatsAppAccount).
		First(&account).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	// Look up contact by ID
	contactID, parseErr := uuid.Parse(req.ContactID)
	if parseErr != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid contact_id", nil, "")
		return
	}

	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID).
		First(&contact).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Contact not found", nil, "")
		return
	}

	waAccount := account.ToWAAccount()

	callLogID, sdpAnswer, err := a.CallManager.InitiateOutgoingCall(
		orgID, userID, contact.ID,
		contact.PhoneNumber, req.WhatsAppAccount,
		waAccount, req.SDPOffer,
	)
	if err != nil {
		a.Log.Error("Failed to initiate outgoing call", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to initiate call: "+err.Error(), nil, "")
		return
	}

	SendEnvelope(w, map[string]string{
		"call_log_id": callLogID.String(),
		"sdp_answer":  sdpAnswer,
	})
}

// HangupOutgoingCall handles POST /api/calls/outgoing/{id}/hangup
func (a *App) HangupOutgoingCall(w http.ResponseWriter, r *http.Request) {
	_, userID, err := a.requireAuthHTTP(w, r, models.ResourceOutgoingCalls, models.ActionWrite)
	if err != nil {
		return
	}

	callLogID, err := parsePathUUIDHTTP(w, r, "id", "call log")
	if err != nil {
		return
	}

	if a.CallManager == nil {
		SendErrorEnvelope(w, http.StatusServiceUnavailable, "Calling is not enabled", nil, "")
		return
	}

	if err := a.CallManager.HangupOutgoingCall(callLogID, userID); err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, err.Error(), nil, "")
		return
	}

	// Mark the call as disconnected by agent
	a.DB.Model(&models.CallLog{}).
		Where("id = ?", callLogID).
		Update("disconnected_by", models.DisconnectedByAgent)

	SendEnvelope(w, map[string]string{"status": "ok"})
}

// SendCallPermissionRequest handles POST /api/calls/permission-request
func (a *App) SendCallPermissionRequest(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.requireAuthHTTP(w, r, models.ResourceOutgoingCalls, models.ActionWrite)
	if err != nil {
		return
	}

	var req struct {
		ContactID       string `json:"contact_id"`
		WhatsAppAccount string `json:"whatsapp_account"`
	}
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.ContactID == "" || req.WhatsAppAccount == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "contact_id and whatsapp_account are required", nil, "")
		return
	}

	if err := a.requireCallingEnabledHTTP(w, orgID); err != nil {
		return
	}

	contactID, parseErr := uuid.Parse(req.ContactID)
	if parseErr != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid contact_id", nil, "")
		return
	}

	// Verify contact exists
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID).First(&contact).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Contact not found", nil, "")
		return
	}

	// Look up account
	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, req.WhatsAppAccount).
		First(&account).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	waAccount := account.ToWAAccount()

	// Send permission request via WhatsApp Messages API
	ctx := r.Context()
	rcpt := whatsapp.Recipient{Phone: contact.PhoneNumber, BSUID: contact.BSUID}
	messageID, err := a.WhatsApp.SendCallPermissionRequest(ctx, waAccount, rcpt, "")
	if err != nil {
		a.Log.Error("Failed to send call permission request", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to send permission request", nil, "")
		return
	}

	// Create CallPermission record
	permission := models.CallPermission{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  orgID,
		ContactID:       contactID,
		WhatsAppAccount: req.WhatsAppAccount,
		Status:          models.CallPermissionPending,
		MessageID:       messageID,
		RequestedByID:   &userID,
	}
	if err := a.DB.Create(&permission).Error; err != nil {
		a.Log.Error("Failed to create call permission record", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to save permission", nil, "")
		return
	}

	SendEnvelope(w, map[string]string{
		"permission_id": permission.ID.String(),
	})
}

// GetICEServers handles GET /api/calls/ice-servers
// Returns the configured ICE (STUN/TURN) servers for the frontend to use in WebRTC peer connections.
func (a *App) GetICEServers(w http.ResponseWriter, r *http.Request) {
	_, _, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	type iceServer struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username,omitempty"`
		Credential string   `json:"credential,omitempty"`
	}

	now := time.Now()
	servers := make([]iceServer, 0, len(a.Config.Calling.ICEServers))
	for _, s := range a.Config.Calling.ICEServers {
		username, credential := s.ResolveCredentials(now)
		servers = append(servers, iceServer{
			URLs:       s.URLs,
			Username:   username,
			Credential: credential,
		})
	}

	SendEnvelope(w, map[string]any{
		"ice_servers": servers,
	})
}

// GetCallPermission handles GET /api/calls/permission/{contactId}?whatsapp_account=X
// Checks call permission state directly via WhatsApp API.
func (a *App) GetCallPermission(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceOutgoingCalls, models.ActionRead)
	if err != nil {
		return
	}

	contactID, err := parsePathUUIDHTTP(w, r, "contactId", "contact")
	if err != nil {
		return
	}

	accountName := r.URL.Query().Get("whatsapp_account")
	if accountName == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "whatsapp_account query param is required", nil, "")
		return
	}

	// Look up contact
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID).First(&contact).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Contact not found", nil, "")
		return
	}

	// Look up WhatsApp account
	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, accountName).First(&account).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	waAccount := account.ToWAAccount()

	// Check permission via WhatsApp API
	ctx := r.Context()
	status, err := a.WhatsApp.GetCallPermission(ctx, waAccount, contact.PhoneNumber)
	if err != nil {
		a.Log.Error("Failed to check call permission via API", "error", err, "phone", contact.PhoneNumber)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to check permission", nil, "")
		return
	}

	a.Log.Info("Call permission check result", "contact_id", contactID, "phone", contact.PhoneNumber, "status", status)

	SendEnvelope(w, map[string]string{
		"status": status,
	})
}
