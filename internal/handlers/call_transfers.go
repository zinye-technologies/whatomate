package handlers

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
)

// ListCallTransfers returns call transfers for the organization
func (a *App) ListCallTransfers(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceCallTransfers, models.ActionRead)
	if err != nil {
		return
	}

	pg := parsePaginationHTTP(r)
	status := r.URL.Query().Get("status")

	query := a.DB.Where("call_transfers.organization_id = ?", orgID).
		Preload("Contact").
		Preload("Agent").
		Preload("InitiatingAgent").
		Preload("Team").
		Preload("CallLog").
		Order("call_transfers.created_at DESC")

	countQuery := a.DB.Model(&models.CallTransfer{}).Where("organization_id = ?", orgID)

	if status != "" {
		query = query.Where("call_transfers.status = ?", status)
		countQuery = countQuery.Where("status = ?", status)
	}

	var total int64
	countQuery.Count(&total)

	var transfers []models.CallTransfer
	if err := pg.Apply(query).Find(&transfers).Error; err != nil {
		a.Log.Error("Failed to fetch call transfers", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to fetch call transfers", nil, "")
		return
	}

	// Mask phone numbers if enabled for this organization
	if a.ShouldMaskPhoneNumbers(orgID) {
		for i := range transfers {
			transfers[i].CallerPhone = utils.MaskPhoneNumber(transfers[i].CallerPhone)
			if transfers[i].Contact != nil {
				transfers[i].Contact.PhoneNumber = utils.MaskPhoneNumber(transfers[i].Contact.PhoneNumber)
				transfers[i].Contact.ProfileName = utils.MaskIfPhoneNumber(transfers[i].Contact.ProfileName)
			}
		}
	}

	SendEnvelope(w, listEnvelope("call_transfers", transfers, total, pg))
}

// GetCallTransfer returns a single call transfer by ID
func (a *App) GetCallTransfer(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceCallTransfers, models.ActionRead)
	if err != nil {
		return
	}

	transferID, err := parsePathUUIDHTTP(w, r, "id", "call transfer")
	if err != nil {
		return
	}

	var transfer models.CallTransfer
	if err := a.DB.Where("id = ? AND organization_id = ?", transferID, orgID).
		Preload("Contact").
		Preload("Agent").
		Preload("InitiatingAgent").
		Preload("Team").
		Preload("CallLog").
		First(&transfer).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Call transfer not found", nil, "")
		return
	}

	if a.ShouldMaskPhoneNumbers(orgID) {
		transfer.CallerPhone = utils.MaskPhoneNumber(transfer.CallerPhone)
		if transfer.Contact != nil {
			transfer.Contact.PhoneNumber = utils.MaskPhoneNumber(transfer.Contact.PhoneNumber)
			transfer.Contact.ProfileName = utils.MaskIfPhoneNumber(transfer.Contact.ProfileName)
		}
	}

	SendEnvelope(w, transfer)
}

// ConnectCallTransfer handles an agent accepting a call transfer via WebRTC SDP exchange
func (a *App) ConnectCallTransfer(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.requireAuthHTTP(w, r, models.ResourceCallTransfers, models.ActionWrite)
	if err != nil {
		return
	}

	transferID, err := parsePathUUIDHTTP(w, r, "id", "call transfer")
	if err != nil {
		return
	}

	// Validate transfer exists and belongs to this org
	var transfer models.CallTransfer
	if err := a.DB.Where("id = ? AND organization_id = ?", transferID, orgID).
		First(&transfer).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Call transfer not found", nil, "")
		return
	}

	if transfer.Status != models.CallTransferStatusWaiting {
		SendErrorEnvelope(w, http.StatusConflict, "Transfer is no longer waiting", nil, "")
		return
	}

	// Check eligibility BEFORE atomically claiming the transfer.
	// This avoids claiming and then reverting, which creates a window where
	// the transfer is stuck as "connected" with no agent.

	// If transfer is directed to a specific agent (no team), reject other agents.
	// For team transfers with rotation, any team member can accept — the atomic
	// UPDATE below is the sole concurrency guard.
	if transfer.AgentID != nil && *transfer.AgentID != userID && transfer.TeamID == nil {
		SendErrorEnvelope(w, http.StatusForbidden,
			"This transfer is directed to a specific agent", nil, "")
		return
	}

	// If transfer has a team_id, check agent is a member (unless super admin)
	if transfer.TeamID != nil && !a.IsSuperAdmin(userID) {
		var memberCount int64
		a.DB.Table("team_members").
			Where("team_id = ? AND user_id = ? AND deleted_at IS NULL", transfer.TeamID, userID).
			Count(&memberCount)
		if memberCount == 0 {
			SendErrorEnvelope(w, http.StatusForbidden, "You are not a member of the target team", nil, "")
			return
		}
	}

	// Atomically claim the transfer — concurrent accepts are rejected
	res := a.DB.Model(&models.CallTransfer{}).
		Where("id = ? AND status = ?", transferID, models.CallTransferStatusWaiting).
		Update("status", models.CallTransferStatusConnected)
	if res.RowsAffected == 0 {
		SendErrorEnvelope(w, http.StatusConflict, "Transfer was already accepted by another agent", nil, "")
		return
	}

	// Parse SDP offer from body
	var req struct {
		SDPOffer string `json:"sdp_offer"`
	}
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}
	if req.SDPOffer == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "sdp_offer is required", nil, "")
		return
	}

	if err := a.requireCallingEnabledHTTP(w, orgID); err != nil {
		return
	}

	sdpAnswer, err := a.CallManager.ConnectAgentToTransfer(transferID, userID, req.SDPOffer)
	if err != nil {
		// Revert DB status so another agent can try
		a.DB.Model(&models.CallTransfer{}).
			Where("id = ? AND status = ?", transferID, models.CallTransferStatusConnected).
			Update("status", models.CallTransferStatusWaiting)
		a.Log.Error("Failed to connect agent to transfer", "error", err, "transfer_id", transferID)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to connect: "+err.Error(), nil, "")
		return
	}

	SendEnvelope(w, map[string]string{
		"sdp_answer": sdpAnswer,
	})
}

// HangupCallTransfer ends a connected call transfer
func (a *App) HangupCallTransfer(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceCallTransfers, models.ActionWrite)
	if err != nil {
		return
	}

	transferID, err := parsePathUUIDHTTP(w, r, "id", "call transfer")
	if err != nil {
		return
	}

	// Validate transfer belongs to this org
	var transfer models.CallTransfer
	if err := a.DB.Where("id = ? AND organization_id = ?", transferID, orgID).
		First(&transfer).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Call transfer not found", nil, "")
		return
	}

	if a.CallManager == nil {
		SendErrorEnvelope(w, http.StatusServiceUnavailable, "Calling is not enabled", nil, "")
		return
	}

	a.CallManager.EndTransfer(transferID)

	// Mark the call as disconnected by agent
	a.DB.Model(&models.CallLog{}).
		Where("id = ?", transfer.CallLogID).
		Update("disconnected_by", models.DisconnectedByAgent)

	SendEnvelope(w, map[string]string{
		"status": "completed",
	})
}

// HoldCall puts an active call on hold and plays hold music to the caller.
func (a *App) HoldCall(w http.ResponseWriter, r *http.Request) {
	_, _, err := a.requireAuthHTTP(w, r, models.ResourceCallTransfers, models.ActionWrite)
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

	if err := a.CallManager.HoldCall(callLogID); err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, err.Error(), nil, "")
		return
	}

	SendEnvelope(w, map[string]string{"status": "on_hold"})
}

// ResumeCall takes an active call off hold and restores the audio bridge.
func (a *App) ResumeCall(w http.ResponseWriter, r *http.Request) {
	_, _, err := a.requireAuthHTTP(w, r, models.ResourceCallTransfers, models.ActionWrite)
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

	if err := a.CallManager.ResumeCall(callLogID); err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, err.Error(), nil, "")
		return
	}

	SendEnvelope(w, map[string]string{"status": "connected"})
}

// InitiateAgentTransfer allows a connected agent to transfer their active call to another team/agent
func (a *App) InitiateAgentTransfer(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.requireAuthHTTP(w, r, models.ResourceCallTransfers, models.ActionWrite)
	if err != nil {
		return
	}
	if err := a.requireCallingEnabledHTTP(w, orgID); err != nil {
		return
	}

	var req struct {
		CallLogID string `json:"call_log_id"`
		TeamID    string `json:"team_id"`
		AgentID   string `json:"agent_id"`
	}
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.CallLogID == "" || req.TeamID == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "call_log_id and team_id are required", nil, "")
		return
	}

	callLogID, err := uuid.Parse(req.CallLogID)
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid call_log_id", nil, "")
		return
	}

	teamID, err := uuid.Parse(req.TeamID)
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid team_id", nil, "")
		return
	}

	// Verify team belongs to this org
	var teamCount int64
	a.DB.Model(&models.Team{}).Where("id = ? AND organization_id = ?", teamID, orgID).Count(&teamCount)
	if teamCount == 0 {
		SendErrorEnvelope(w, http.StatusNotFound, "Team not found", nil, "")
		return
	}

	var targetAgentID *uuid.UUID
	if req.AgentID != "" {
		agentID, err := uuid.Parse(req.AgentID)
		if err != nil {
			SendErrorEnvelope(w, http.StatusBadRequest, "Invalid agent_id", nil, "")
			return
		}
		// Verify agent is a member of the team
		var memberCount int64
		a.DB.Table("team_members").
			Where("team_id = ? AND user_id = ? AND deleted_at IS NULL", teamID, agentID).
			Count(&memberCount)
		if memberCount == 0 {
			SendErrorEnvelope(w, http.StatusBadRequest, "Agent is not a member of the specified team", nil, "")
			return
		}
		targetAgentID = &agentID
	}

	if a.CallManager == nil {
		SendErrorEnvelope(w, http.StatusServiceUnavailable, "Calling is not enabled", nil, "")
		return
	}

	if err := a.CallManager.InitiateAgentTransfer(callLogID, userID, &teamID, targetAgentID); err != nil {
		a.Log.Error("Failed to initiate agent transfer", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to initiate transfer: "+err.Error(), nil, "")
		return
	}

	SendEnvelope(w, map[string]string{
		"status": "transferring",
	})
}
