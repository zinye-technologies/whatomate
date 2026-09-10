package handlers

import (
	"net/http"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/utils"
)

// ListCallLogs returns call logs for the organization.
// Users with call_logs:read permission see all logs; others see only their own.
func (a *App) ListCallLogs(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	pg := parsePaginationHTTP(r)
	q := r.URL.Query()
	status := q.Get("status")
	account := q.Get("account")
	contactIDStr := q.Get("contact_id")
	direction := q.Get("direction")
	ivrFlowID := q.Get("ivr_flow_id")
	phone := q.Get("phone")

	query := a.DB.Where("call_logs.organization_id = ?", orgID).
		Preload("Contact").
		Preload("Agent").
		Preload("IVRFlow").
		Order("call_logs.created_at DESC")

	countQuery := a.DB.Model(&models.CallLog{}).Where("organization_id = ?", orgID)

	// Users without call_logs:read permission only see their own call logs
	if !a.HasPermission(userID, models.ResourceCallLogs, models.ActionRead, orgID) {
		query = query.Where("call_logs.agent_id = ?", userID)
		countQuery = countQuery.Where("agent_id = ?", userID)
	}

	if status != "" {
		query = query.Where("call_logs.status = ?", status)
		countQuery = countQuery.Where("status = ?", status)
	}
	if account != "" {
		query = query.Where("call_logs.whatsapp_account = ?", account)
		countQuery = countQuery.Where("whatsapp_account = ?", account)
	}
	if contactIDStr != "" {
		query = query.Where("call_logs.contact_id = ?", contactIDStr)
		countQuery = countQuery.Where("contact_id = ?", contactIDStr)
	}
	if direction != "" {
		query = query.Where("call_logs.direction = ?", direction)
		countQuery = countQuery.Where("direction = ?", direction)
	}
	if ivrFlowID != "" {
		query = query.Where("call_logs.ivr_flow_id = ?", ivrFlowID)
		countQuery = countQuery.Where("ivr_flow_id = ?", ivrFlowID)
	}
	if phone != "" {
		phoneLike := "%" + phone + "%"
		query = query.Where("call_logs.caller_phone LIKE ?", phoneLike)
		countQuery = countQuery.Where("caller_phone LIKE ?", phoneLike)
	}

	// Date range filter
	if start, ok := parseDateParamHTTP(r, "start_date"); ok {
		query = query.Where("call_logs.created_at >= ?", start)
		countQuery = countQuery.Where("created_at >= ?", start)
	}
	if end, ok := parseDateParamHTTP(r, "end_date"); ok {
		query = query.Where("call_logs.created_at <= ?", endOfDay(end))
		countQuery = countQuery.Where("created_at <= ?", endOfDay(end))
	}

	var total int64
	countQuery.Count(&total)

	var callLogs []models.CallLog
	if err := pg.Apply(query).Find(&callLogs).Error; err != nil {
		a.Log.Error("Failed to fetch call logs", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to fetch call logs", nil, "")
		return
	}

	// Mask phone numbers if enabled for this organization
	if a.ShouldMaskPhoneNumbers(orgID) {
		for i := range callLogs {
			callLogs[i].CallerPhone = utils.MaskPhoneNumber(callLogs[i].CallerPhone)
			if callLogs[i].Contact != nil {
				callLogs[i].Contact.PhoneNumber = utils.MaskPhoneNumber(callLogs[i].Contact.PhoneNumber)
				callLogs[i].Contact.ProfileName = utils.MaskIfPhoneNumber(callLogs[i].Contact.ProfileName)
			}
		}
	}

	SendEnvelope(w, listEnvelope("call_logs", callLogs, total, pg))
}

// GetCallLog returns a single call log by ID.
// Users without call_logs:read permission can only access their own call logs.
func (a *App) GetCallLog(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	logID, err := parsePathUUIDHTTP(w, r, "id", "call log")
	if err != nil {
		return
	}

	query := a.DB.Where("id = ? AND organization_id = ?", logID, orgID)
	if !a.HasPermission(userID, models.ResourceCallLogs, models.ActionRead, orgID) {
		query = query.Where("agent_id = ?", userID)
	}

	var callLog models.CallLog
	if err := query.Preload("Contact").Preload("Agent").Preload("IVRFlow").First(&callLog).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Call log not found", nil, "")
		return
	}

	if a.ShouldMaskPhoneNumbers(orgID) {
		callLog.CallerPhone = utils.MaskPhoneNumber(callLog.CallerPhone)
		if callLog.Contact != nil {
			callLog.Contact.PhoneNumber = utils.MaskPhoneNumber(callLog.Contact.PhoneNumber)
			callLog.Contact.ProfileName = utils.MaskIfPhoneNumber(callLog.Contact.ProfileName)
		}
	}

	// Fetch associated call transfers for this call log
	var transfers []models.CallTransfer
	a.DB.Where("call_log_id = ? AND organization_id = ?", callLog.ID, orgID).
		Preload("Agent").
		Preload("InitiatingAgent").
		Preload("Team").
		Order("created_at ASC").
		Find(&transfers)

	SendEnvelope(w, map[string]any{
		"call_log":  callLog,
		"transfers": transfers,
	})
}

// GetCallRecording returns a presigned S3 URL for a call recording.
// Users without call_logs:read permission can only access recordings for their own calls.
func (a *App) GetCallRecording(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	if a.S3Client == nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Recording not available", nil, "")
		return
	}

	logID, err := parsePathUUIDHTTP(w, r, "id", "call log")
	if err != nil {
		return
	}

	query := a.DB.Where("id = ? AND organization_id = ?", logID, orgID)
	if !a.HasPermission(userID, models.ResourceCallLogs, models.ActionRead, orgID) {
		query = query.Where("agent_id = ?", userID)
	}

	var callLog models.CallLog
	if err := query.First(&callLog).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Call log not found", nil, "")
		return
	}

	if callLog.RecordingS3Key == "" {
		SendErrorEnvelope(w, http.StatusNotFound, "No recording for this call", nil, "")
		return
	}

	url, err := a.S3Client.GetPresignedURL(r.Context(), callLog.RecordingS3Key, 15*time.Minute)
	if err != nil {
		a.Log.Error("Failed to generate presigned URL", "error", err, "call_log_id", logID)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to generate recording URL", nil, "")
		return
	}

	SendEnvelope(w, map[string]any{
		"url":      url,
		"duration": callLog.RecordingDuration,
	})
}
