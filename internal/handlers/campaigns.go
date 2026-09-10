package handlers

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/utils"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CampaignRequest represents campaign create/update request
type CampaignRequest struct {
	Name            string     `json:"name" validate:"required"`
	WhatsAppAccount string     `json:"whatsapp_account" validate:"required"`
	TemplateID      string     `json:"template_id" validate:"required"`
	HeaderMediaID   string     `json:"header_media_id"`
	ScheduledAt     *time.Time `json:"scheduled_at"`
}

// CampaignResponse represents campaign in API responses
type CampaignResponse struct {
	ID                  uuid.UUID             `json:"id"`
	Name                string                `json:"name"`
	WhatsAppAccount     string                `json:"whatsapp_account"`
	TemplateID          uuid.UUID             `json:"template_id"`
	TemplateName        string                `json:"template_name,omitempty"`
	HeaderMediaID       string                `json:"header_media_id,omitempty"`
	HeaderMediaFilename string                `json:"header_media_filename,omitempty"`
	HeaderMediaMimeType string                `json:"header_media_mime_type,omitempty"`
	Status              models.CampaignStatus `json:"status"`
	TotalRecipients     int                   `json:"total_recipients"`
	SentCount           int                   `json:"sent_count"`
	DeliveredCount      int                   `json:"delivered_count"`
	ReadCount           int                   `json:"read_count"`
	FailedCount         int                   `json:"failed_count"`
	ScheduledAt         *time.Time            `json:"scheduled_at,omitempty"`
	StartedAt           *time.Time            `json:"started_at,omitempty"`
	CompletedAt         *time.Time            `json:"completed_at,omitempty"`
	CreatedByName       string                `json:"created_by_name,omitempty"`
	UpdatedByName       string                `json:"updated_by_name,omitempty"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

// RecipientRequest represents recipient import request
type RecipientRequest struct {
	PhoneNumber    string         `json:"phone_number" validate:"required"`
	RecipientName  string         `json:"recipient_name"`
	TemplateParams map[string]any `json:"template_params"`
	// HeaderParams carries the value for a TEXT-header variable (max 1 per
	// Meta), keyed by the variable's name. Kept separate from TemplateParams
	// so a positional header {{1}} doesn't collide with body {{1}}.
	HeaderParams map[string]any `json:"header_params"`
}

// ListCampaigns implements campaign listing
func (a *App) ListCampaigns(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	pg := parsePaginationHTTP(r)

	// Get query params
	status := r.URL.Query().Get("status")
	whatsappAccount := r.URL.Query().Get("whatsapp_account")
	search := r.URL.Query().Get("search")

	baseQuery := a.DB.Where("organization_id = ?", orgID)

	if search != "" {
		baseQuery = baseQuery.Where("name ILIKE ?", "%"+search+"%")
	}

	if status != "" {
		baseQuery = baseQuery.Where("status = ?", status)
	}
	if whatsappAccount != "" {
		baseQuery = baseQuery.Where("whats_app_account = ?", whatsappAccount)
	}
	if from, ok := parseDateParamHTTP(r, "from"); ok {
		baseQuery = baseQuery.Where("created_at >= ?", from)
	}
	if to, ok := parseDateParamHTTP(r, "to"); ok {
		baseQuery = baseQuery.Where("created_at <= ?", endOfDay(to))
	}

	// Get total count
	var total int64
	baseQuery.Model(&models.BulkMessageCampaign{}).Count(&total)

	var campaigns []models.BulkMessageCampaign
	if err := pg.Apply(baseQuery.
		Preload("Template").
		Order("created_at DESC")).
		Find(&campaigns).Error; err != nil {
		a.Log.Error("Failed to list campaigns", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list campaigns", nil, "")
		return
	}

	// Convert to response format
	response := make([]CampaignResponse, len(campaigns))
	for i, c := range campaigns {
		response[i] = CampaignResponse{
			ID:                  c.ID,
			Name:                c.Name,
			WhatsAppAccount:     c.WhatsAppAccount,
			TemplateID:          c.TemplateID,
			HeaderMediaID:       c.HeaderMediaID,
			HeaderMediaFilename: c.HeaderMediaFilename,
			HeaderMediaMimeType: c.HeaderMediaMimeType,
			Status:              c.Status,
			TotalRecipients:     c.TotalRecipients,
			SentCount:           c.SentCount,
			DeliveredCount:      c.DeliveredCount,
			ReadCount:           c.ReadCount,
			FailedCount:         c.FailedCount,
			ScheduledAt:         c.ScheduledAt,
			StartedAt:           c.StartedAt,
			CompletedAt:         c.CompletedAt,
			CreatedAt:           c.CreatedAt,
			UpdatedAt:           c.UpdatedAt,
		}
		if c.Template != nil {
			response[i].TemplateName = c.Template.Name
		}
	}

	SendEnvelope(w, listEnvelope("campaigns", response, total, pg))
	return
}

// CreateCampaign implements campaign creation
func (a *App) CreateCampaign(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	var req CampaignRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	// Validate template exists
	templateID, err := uuid.Parse(req.TemplateID)
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid template ID", nil, "")
		return
	}

	template, err := findByIDAndOrgHTTP[models.Template](a.DB, w, templateID, orgID, "Template")
	if err != nil {
		return
	}

	// Validate WhatsApp account exists
	if _, err := a.resolveWhatsAppAccount(orgID, req.WhatsAppAccount); err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "WhatsApp account not found", nil, "")
		return
	}

	campaign := models.BulkMessageCampaign{
		OrganizationID:  orgID,
		WhatsAppAccount: req.WhatsAppAccount,
		Name:            req.Name,
		TemplateID:      templateID,
		HeaderMediaID:   req.HeaderMediaID,
		Status:          models.CampaignStatusDraft,
		ScheduledAt:     req.ScheduledAt,
		CreatedBy:       userID,
		UpdatedByID:     &userID,
	}

	if err := a.DB.Create(&campaign).Error; err != nil {
		a.Log.Error("Failed to create campaign", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create campaign", nil, "")
		return
	}

	a.logAudit(orgID, userID,
		"campaign", campaign.ID, models.AuditActionCreated, nil, &campaign)

	a.Log.Info("Campaign created", "campaign_id", campaign.ID, "name", campaign.Name)

	SendEnvelope(w, CampaignResponse{
		ID:                  campaign.ID,
		Name:                campaign.Name,
		WhatsAppAccount:     campaign.WhatsAppAccount,
		TemplateID:          campaign.TemplateID,
		TemplateName:        template.Name,
		HeaderMediaID:       campaign.HeaderMediaID,
		HeaderMediaFilename: campaign.HeaderMediaFilename,
		HeaderMediaMimeType: campaign.HeaderMediaMimeType,
		Status:              campaign.Status,
		TotalRecipients:     campaign.TotalRecipients,
		SentCount:           campaign.SentCount,
		DeliveredCount:      campaign.DeliveredCount,
		ReadCount:           campaign.ReadCount,
		FailedCount:         campaign.FailedCount,
		ScheduledAt:         campaign.ScheduledAt,
		CreatedAt:           campaign.CreatedAt,
		UpdatedAt:           campaign.UpdatedAt,
	})
	return
}

// GetCampaign implements getting a single campaign
func (a *App) GetCampaign(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	var campaign models.BulkMessageCampaign
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).
		Preload("Template").
		Preload("Creator").
		Preload("UpdatedBy").
		First(&campaign).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Campaign not found", nil, "")
		return
	}

	response := CampaignResponse{
		ID:                  campaign.ID,
		Name:                campaign.Name,
		WhatsAppAccount:     campaign.WhatsAppAccount,
		TemplateID:          campaign.TemplateID,
		HeaderMediaID:       campaign.HeaderMediaID,
		HeaderMediaFilename: campaign.HeaderMediaFilename,
		HeaderMediaMimeType: campaign.HeaderMediaMimeType,
		Status:              campaign.Status,
		TotalRecipients:     campaign.TotalRecipients,
		SentCount:           campaign.SentCount,
		DeliveredCount:      campaign.DeliveredCount,
		ReadCount:           campaign.ReadCount,
		FailedCount:         campaign.FailedCount,
		ScheduledAt:         campaign.ScheduledAt,
		StartedAt:           campaign.StartedAt,
		CompletedAt:         campaign.CompletedAt,
		CreatedAt:           campaign.CreatedAt,
		UpdatedAt:           campaign.UpdatedAt,
	}
	if campaign.Template != nil {
		response.TemplateName = campaign.Template.Name
	}
	if campaign.Creator != nil {
		response.CreatedByName = campaign.Creator.FullName
	}
	if campaign.UpdatedBy != nil {
		response.UpdatedByName = campaign.UpdatedBy.FullName
	}

	SendEnvelope(w, response)
	return
}

// UpdateCampaign implements campaign update
func (a *App) UpdateCampaign(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, id, orgID, "Campaign")
	if err != nil {
		return
	}

	// Only allow updates to draft campaigns
	if campaign.Status != models.CampaignStatusDraft {
		SendErrorEnvelope(w, http.StatusBadRequest, "Can only update draft campaigns", nil, "")
		return
	}

	oldCampaign := *campaign

	var req CampaignRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	// Update fields
	updates := map[string]any{
		"name":          req.Name,
		"scheduled_at":  req.ScheduledAt,
		"updated_by_id": userID,
	}

	if req.TemplateID != "" {
		templateID, err := uuid.Parse(req.TemplateID)
		if err != nil {
			SendErrorEnvelope(w, http.StatusBadRequest, "Invalid template ID", nil, "")
			return
		}
		updates["template_id"] = templateID
	}

	if req.WhatsAppAccount != "" {
		updates["whats_app_account"] = req.WhatsAppAccount
	}

	if err := a.DB.Model(campaign).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to update campaign", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update campaign", nil, "")
		return
	}

	// Reload campaign
	a.DB.Where("id = ?", id).Preload("Template").Preload("Creator").Preload("UpdatedBy").First(campaign)

	a.logAudit(orgID, userID,
		"campaign", campaign.ID, models.AuditActionUpdated, &oldCampaign, campaign)

	response := CampaignResponse{
		ID:                  campaign.ID,
		Name:                campaign.Name,
		WhatsAppAccount:     campaign.WhatsAppAccount,
		TemplateID:          campaign.TemplateID,
		HeaderMediaID:       campaign.HeaderMediaID,
		HeaderMediaFilename: campaign.HeaderMediaFilename,
		HeaderMediaMimeType: campaign.HeaderMediaMimeType,
		Status:              campaign.Status,
		TotalRecipients:     campaign.TotalRecipients,
		SentCount:           campaign.SentCount,
		DeliveredCount:      campaign.DeliveredCount,
		ReadCount:           campaign.ReadCount,
		FailedCount:         campaign.FailedCount,
		ScheduledAt:         campaign.ScheduledAt,
		CreatedAt:           campaign.CreatedAt,
		UpdatedAt:           campaign.UpdatedAt,
	}
	if campaign.Template != nil {
		response.TemplateName = campaign.Template.Name
	}
	if campaign.Creator != nil {
		response.CreatedByName = campaign.Creator.FullName
	}
	if campaign.UpdatedBy != nil {
		response.UpdatedByName = campaign.UpdatedBy.FullName
	}

	SendEnvelope(w, response)
	return
}

// DeleteCampaign implements campaign deletion
func (a *App) DeleteCampaign(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, id, orgID, "Campaign")
	if err != nil {
		return
	}

	// Don't allow deletion of running campaigns
	if campaign.Status == models.CampaignStatusProcessing || campaign.Status == models.CampaignStatusQueued {
		SendErrorEnvelope(w, http.StatusBadRequest, "Cannot delete running campaign", nil, "")
		return
	}

	// Delete recipients first
	if err := a.DB.Where("campaign_id = ?", id).Delete(&models.BulkMessageRecipient{}).Error; err != nil {
		a.Log.Error("Failed to delete campaign recipients", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete campaign", nil, "")
		return
	}

	// Delete campaign
	if err := a.DB.Delete(campaign).Error; err != nil {
		a.Log.Error("Failed to delete campaign", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete campaign", nil, "")
		return
	}

	a.logAudit(orgID, userID,
		"campaign", id, models.AuditActionDeleted, campaign, nil)

	a.Log.Info("Campaign deleted", "campaign_id", id)

	SendEnvelope(w, map[string]any{
		"message": "Campaign deleted successfully",
	})
	return
}

// StartCampaign implements starting a campaign
func (a *App) StartCampaign(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, id, orgID, "Campaign")
	if err != nil {
		return
	}

	// Check if campaign can be started
	if campaign.Status != models.CampaignStatusDraft && campaign.Status != models.CampaignStatusScheduled && campaign.Status != models.CampaignStatusPaused {
		SendErrorEnvelope(w, http.StatusBadRequest, "Campaign cannot be started in current state", nil, "")
		return
	}

	// Get all pending recipients
	var recipients []models.BulkMessageRecipient
	if err := a.DB.Where("campaign_id = ? AND status = ?", id, models.MessageStatusPending).Find(&recipients).Error; err != nil {
		a.Log.Error("Failed to load recipients", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to load recipients", nil, "")
		return
	}

	if len(recipients) == 0 {
		SendErrorEnvelope(w, http.StatusBadRequest, "Campaign has no pending recipients", nil, "")
		return
	}

	// Validate template still exists
	if campaign.TemplateID != uuid.Nil {
		var template models.Template
		if err := a.DB.Where("id = ? AND organization_id = ?", campaign.TemplateID, orgID).First(&template).Error; err != nil {
			SendErrorEnvelope(w, http.StatusBadRequest, "Campaign template no longer exists", nil, "")
			return
		}
	}

	// Update status to processing
	now := time.Now()
	updates := map[string]any{
		"status":     models.CampaignStatusProcessing,
		"started_at": now,
	}

	if err := a.DB.Model(campaign).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to start campaign", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to start campaign", nil, "")
		return
	}

	a.Log.Info("Campaign started", "campaign_id", id, "recipients", len(recipients))

	// Enqueue all recipients as individual jobs for parallel processing
	jobs := make([]*queue.RecipientJob, len(recipients))
	for i, recipient := range recipients {
		jobs[i] = &queue.RecipientJob{
			CampaignID:     id,
			RecipientID:    recipient.ID,
			OrganizationID: orgID,
			PhoneNumber:    recipient.PhoneNumber,
			RecipientName:  recipient.RecipientName,
			TemplateParams: recipient.TemplateParams,
			HeaderParams:   recipient.HeaderParams,
		}
	}

	if err := a.Queue.EnqueueRecipients(r.Context(), jobs); err != nil {
		a.Log.Error("Failed to enqueue recipients", "error", err)
		// Revert status on failure
		a.DB.Model(campaign).Update("status", models.CampaignStatusDraft)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to queue recipients", nil, "")
		return
	}

	a.Log.Info("Recipients enqueued for processing", "campaign_id", id, "count", len(jobs))

	SendEnvelope(w, map[string]any{
		"message": "Campaign started",
		"status":  models.CampaignStatusProcessing,
	})
	return
}

// PauseCampaign implements pausing a campaign
func (a *App) PauseCampaign(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, id, orgID, "Campaign")
	if err != nil {
		return
	}

	if campaign.Status != models.CampaignStatusProcessing && campaign.Status != models.CampaignStatusQueued {
		SendErrorEnvelope(w, http.StatusBadRequest, "Campaign is not running", nil, "")
		return
	}

	if err := a.DB.Model(campaign).Update("status", models.CampaignStatusPaused).Error; err != nil {
		a.Log.Error("Failed to pause campaign", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to pause campaign", nil, "")
		return
	}

	a.Log.Info("Campaign paused", "campaign_id", id)

	SendEnvelope(w, map[string]any{
		"message": "Campaign paused",
		"status":  models.CampaignStatusPaused,
	})
	return
}

// CancelCampaign implements cancelling a campaign
func (a *App) CancelCampaign(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, id, orgID, "Campaign")
	if err != nil {
		return
	}

	if campaign.Status == models.CampaignStatusCompleted || campaign.Status == models.CampaignStatusCancelled {
		SendErrorEnvelope(w, http.StatusBadRequest, "Campaign already finished", nil, "")
		return
	}

	if err := a.DB.Model(campaign).Update("status", models.CampaignStatusCancelled).Error; err != nil {
		a.Log.Error("Failed to cancel campaign", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to cancel campaign", nil, "")
		return
	}

	a.Log.Info("Campaign cancelled", "campaign_id", id)

	SendEnvelope(w, map[string]any{
		"message": "Campaign cancelled",
		"status":  models.CampaignStatusCancelled,
	})
	return
}

// RetryFailed retries sending to all failed recipients
func (a *App) RetryFailed(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, id, orgID, "Campaign")
	if err != nil {
		return
	}

	// Only allow retry on completed or paused campaigns
	if campaign.Status != models.CampaignStatusCompleted && campaign.Status != models.CampaignStatusPaused && campaign.Status != models.CampaignStatusFailed {
		SendErrorEnvelope(w, http.StatusBadRequest, "Can only retry failed messages on completed, paused, or failed campaigns", nil, "")
		return
	}

	// Get failed recipients
	var failedRecipients []models.BulkMessageRecipient
	if err := a.DB.Where("campaign_id = ? AND status = ?", id, models.MessageStatusFailed).Find(&failedRecipients).Error; err != nil {
		a.Log.Error("Failed to load failed recipients", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to load failed recipients", nil, "")
		return
	}

	if len(failedRecipients) == 0 {
		SendErrorEnvelope(w, http.StatusBadRequest, "No failed messages to retry", nil, "")
		return
	}

	// Reset failed recipients to pending
	if err := a.DB.Model(&models.BulkMessageRecipient{}).
		Where("campaign_id = ? AND status = ?", id, models.MessageStatusFailed).
		Updates(map[string]any{
			"status":        models.MessageStatusPending,
			"error_message": "",
		}).Error; err != nil {
		a.Log.Error("Failed to reset failed recipients", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to reset failed recipients", nil, "")
		return
	}

	// Reset failed messages in messages table to pending
	if err := a.DB.Model(&models.Message{}).
		Where("metadata->>'campaign_id' = ? AND status = ?", id.String(), models.MessageStatusFailed).
		Updates(map[string]any{
			"status":        models.MessageStatusPending,
			"error_message": "",
		}).Error; err != nil {
		a.Log.Error("Failed to reset failed messages", "error", err)
	}

	// Recalculate campaign stats from messages table
	a.recalculateCampaignStats(id)

	// Update campaign status to processing
	if err := a.DB.Model(campaign).Update("status", models.CampaignStatusProcessing).Error; err != nil {
		a.Log.Error("Failed to update campaign status", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update campaign", nil, "")
		return
	}

	a.Log.Info("Retrying failed messages", "campaign_id", id, "failed_count", len(failedRecipients))

	// Enqueue failed recipients as individual jobs for parallel processing
	jobs := make([]*queue.RecipientJob, len(failedRecipients))
	for i, recipient := range failedRecipients {
		jobs[i] = &queue.RecipientJob{
			CampaignID:     id,
			RecipientID:    recipient.ID,
			OrganizationID: orgID,
			PhoneNumber:    recipient.PhoneNumber,
			RecipientName:  recipient.RecipientName,
			TemplateParams: recipient.TemplateParams,
			HeaderParams:   recipient.HeaderParams,
		}
	}

	if err := a.Queue.EnqueueRecipients(r.Context(), jobs); err != nil {
		a.Log.Error("Failed to enqueue recipients for retry", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to queue recipients", nil, "")
		return
	}

	a.Log.Info("Failed recipients enqueued for retry", "campaign_id", id, "count", len(jobs))

	SendEnvelope(w, map[string]any{
		"message":     "Retrying failed messages",
		"retry_count": len(failedRecipients),
		"status":      models.CampaignStatusProcessing,
	})
	return
}

// ImportRecipients implements adding recipients to a campaign
func (a *App) ImportRecipients(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, id, orgID, "Campaign")
	if err != nil {
		return
	}

	if campaign.Status != models.CampaignStatusDraft {
		SendErrorEnvelope(w, http.StatusBadRequest, "Can only add recipients to draft campaigns", nil, "")
		return
	}

	var req struct {
		Recipients []RecipientRequest `json:"recipients" validate:"required"`
	}
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	// Create recipients
	recipients := make([]models.BulkMessageRecipient, len(req.Recipients))
	for i, rec := range req.Recipients {
		recipients[i] = models.BulkMessageRecipient{
			CampaignID:     id,
			PhoneNumber:    rec.PhoneNumber,
			RecipientName:  rec.RecipientName,
			TemplateParams: models.JSONB(rec.TemplateParams),
			HeaderParams:   models.JSONB(rec.HeaderParams),
			Status:         models.MessageStatusPending,
		}
	}

	if err := a.DB.Create(&recipients).Error; err != nil {
		a.Log.Error("Failed to add recipients", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to add recipients", nil, "")
		return
	}

	// Update total recipients count
	var totalCount int64
	a.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ?", id).Count(&totalCount)
	a.DB.Model(campaign).Update("total_recipients", totalCount)

	a.Log.Info("Recipients added to campaign", "campaign_id", id, "count", len(req.Recipients))

	// Log recipient addition as audit
	phoneNumbers := make([]string, len(req.Recipients))
	for i, rec := range req.Recipients {
		phoneNumbers[i] = rec.PhoneNumber
	}
	a.logAudit(orgID, userID,
		"campaign", id, models.AuditActionUpdated, nil, nil,
		map[string]any{
			"field":     "recipients_added",
			"old_value": nil,
			"new_value": fmt.Sprintf("%d recipients added", len(req.Recipients)),
		})

	SendEnvelope(w, map[string]any{
		"message":          "Recipients added successfully",
		"added_count":      len(req.Recipients),
		"total_recipients": totalCount,
	})
	return
}

// GetCampaignRecipients implements listing campaign recipients
func (a *App) GetCampaignRecipients(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	// Verify campaign belongs to org
	_, err = findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, id, orgID, "Campaign")
	if err != nil {
		return
	}

	var recipients []models.BulkMessageRecipient
	if err := a.DB.Where("campaign_id = ?", id).Order("created_at ASC").Find(&recipients).Error; err != nil {
		a.Log.Error("Failed to list recipients", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list recipients", nil, "")
		return
	}

	if a.ShouldMaskPhoneNumbers(orgID) {
		for i := range recipients {
			recipients[i].PhoneNumber = utils.MaskPhoneNumber(recipients[i].PhoneNumber)
			recipients[i].RecipientName = utils.MaskIfPhoneNumber(recipients[i].RecipientName)
		}
	}

	SendEnvelope(w, map[string]any{
		"recipients": recipients,
		"total":      len(recipients),
	})
	return
}

// DeleteCampaignRecipient deletes a single recipient from a campaign
func (a *App) DeleteCampaignRecipient(w http.ResponseWriter, r *http.Request) {
	orgID, userID, err := a.getOrgAndUserIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	campaignUUID, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	recipientUUID, err := parsePathUUIDHTTP(w, r, "recipientId", "recipient")
	if err != nil {
		return
	}

	// Verify campaign belongs to org and is in draft status
	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, campaignUUID, orgID, "Campaign")
	if err != nil {
		return
	}

	if campaign.Status != models.CampaignStatusDraft {
		SendErrorEnvelope(w, http.StatusBadRequest, "Can only delete recipients from draft campaigns", nil, "")
		return
	}

	// Load recipient for audit before deleting
	var recipient models.BulkMessageRecipient
	a.DB.Where("id = ? AND campaign_id = ?", recipientUUID, campaignUUID).First(&recipient)

	// Delete recipient
	result := a.DB.Where("id = ? AND campaign_id = ?", recipientUUID, campaignUUID).Delete(&models.BulkMessageRecipient{})
	if result.Error != nil {
		a.Log.Error("Failed to delete recipient", "error", result.Error)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete recipient", nil, "")
		return
	}

	if result.RowsAffected == 0 {
		SendErrorEnvelope(w, http.StatusNotFound, "Recipient not found", nil, "")
		return
	}

	// Update campaign recipient count
	a.DB.Model(campaign).Update("total_recipients", gorm.Expr("total_recipients - 1"))

	a.logAudit(orgID, userID,
		"campaign", campaignUUID, models.AuditActionUpdated, nil, nil,
		map[string]any{
			"field":     "recipient_removed",
			"old_value": recipient.PhoneNumber,
			"new_value": nil,
		})

	SendEnvelope(w, map[string]any{
		"message": "Recipient deleted successfully",
	})
	return
}

// UploadCampaignMedia uploads media for a campaign's template header
func (a *App) UploadCampaignMedia(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	campaignUUID, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	// Get campaign with template
	var campaign models.BulkMessageCampaign
	if err := a.DB.Where("id = ? AND organization_id = ?", campaignUUID, orgID).
		Preload("Template").
		First(&campaign).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Campaign not found", nil, "")
		return
	}

	// Only allow media upload for draft campaigns
	if campaign.Status != models.CampaignStatusDraft {
		SendErrorEnvelope(w, http.StatusBadRequest, "Can only upload media for draft campaigns", nil, "")
		return
	}

	// Verify template has media header
	if campaign.Template == nil || campaign.Template.HeaderType == "" || campaign.Template.HeaderType == "TEXT" {
		SendErrorEnvelope(w, http.StatusBadRequest, "Template does not have a media header", nil, "")
		return
	}

	// Get WhatsApp account
	account, err := a.resolveWhatsAppAccount(orgID, campaign.WhatsAppAccount)
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "WhatsApp account not found", nil, "")
		return
	}

	// Parse multipart form
	err = r.ParseMultipartForm(16 << 20)
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid multipart form", nil, "")
		return
	}
	form := r.MultipartForm

	files := form.File["file"]
	if len(files) == 0 {
		SendErrorEnvelope(w, http.StatusBadRequest, "No file provided", nil, "")
		return
	}

	fileHeader := files[0]
	file, err := fileHeader.Open()
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Failed to open file", nil, "")
		return
	}
	defer func() { _ = file.Close() }()

	// Read file content (limit to 16MB)
	const maxMediaSize = 16 << 20 // 16MB
	data, err := io.ReadAll(io.LimitReader(file, maxMediaSize+1))
	if err != nil {
		a.Log.Error("Failed to read file", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to read file", nil, "")
		return
	}
	if len(data) > maxMediaSize {
		SendErrorEnvelope(w, http.StatusBadRequest, "File too large. Maximum size is 16MB", nil, "")
		return
	}

	// Determine and validate MIME type
	mimeType := fileHeader.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	allowedMIME := map[string]bool{
		"image/jpeg": true, "image/png": true, "image/webp": true,
		"video/mp4": true, "video/3gpp": true,
		"audio/aac": true, "audio/mp4": true, "audio/mpeg": true, "audio/ogg": true,
		"application/pdf": true, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   true,
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	}
	if !allowedMIME[mimeType] {
		SendErrorEnvelope(w, http.StatusBadRequest, "Unsupported file type: "+mimeType, nil, "")
		return
	}

	// Upload to WhatsApp
	waAccount := a.toWhatsAppAccount(account)

	ctx := r.Context()
	mediaID, err := a.WhatsApp.UploadMedia(ctx, waAccount, data, mimeType, fileHeader.Filename)
	if err != nil {
		a.Log.Error("Failed to upload media to WhatsApp", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to upload media to WhatsApp", nil, "")
		return
	}

	// Save file locally for preview
	localPath, err := a.saveCampaignMedia(campaignUUID.String(), data, mimeType)
	if err != nil {
		a.Log.Error("Failed to save media locally", "error", err)
		// Don't fail the request, just log the error - preview won't work
	}

	// Update campaign with media ID, filename, mime type, and local path
	updates := map[string]any{
		"header_media_id":         mediaID,
		"header_media_filename":   sanitizeFilename(fileHeader.Filename),
		"header_media_mime_type":  mimeType,
		"header_media_local_path": localPath,
	}
	if err := a.DB.Model(&campaign).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to update campaign with media info", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to save media info", nil, "")
		return
	}

	a.Log.Info("Campaign media uploaded", "campaign_id", campaignUUID, "media_id", mediaID, "filename", fileHeader.Filename, "local_path", localPath)

	SendEnvelope(w, map[string]any{
		"media_id":   mediaID,
		"filename":   fileHeader.Filename,
		"mime_type":  mimeType,
		"local_path": localPath,
		"message":    "Media uploaded successfully",
	})
	return
}

// saveCampaignMedia saves uploaded media locally for preview
func (a *App) saveCampaignMedia(campaignID string, data []byte, mimeType string) (string, error) {
	// Determine file extension
	ext := getExtensionFromMimeType(mimeType)
	if ext == "" {
		ext = ".bin"
	}

	// Create campaigns media directory
	subdir := "campaigns"
	if err := a.ensureMediaDir(subdir); err != nil {
		return "", fmt.Errorf("failed to create media directory: %w", err)
	}

	// Generate filename using campaign ID
	filename := campaignID + ext
	filePath := filepath.Join(a.getMediaStoragePath(), subdir, filename)

	// Save file
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to save media file: %w", err)
	}

	// Return relative path for storage
	relativePath := filepath.Join(subdir, filename)
	a.Log.Info("Campaign media saved locally", "path", relativePath, "size", len(data))

	return relativePath, nil
}

// ServeCampaignMedia serves campaign media files for preview
func (a *App) ServeCampaignMedia(w http.ResponseWriter, r *http.Request) {
	// Get auth context
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	// Get campaign ID from URL
	campaignUUID, err := parsePathUUIDHTTP(w, r, "id", "campaign")
	if err != nil {
		return
	}

	// Find campaign and verify access
	campaign, err := findByIDAndOrgHTTP[models.BulkMessageCampaign](a.DB, w, campaignUUID, orgID, "Campaign")
	if err != nil {
		return
	}

	// Check if campaign has media
	if campaign.HeaderMediaLocalPath == "" {
		SendErrorEnvelope(w, http.StatusNotFound, "No media found", nil, "")
		return
	}

	// Security: prevent directory traversal and symlink attacks
	filePath := filepath.Clean(campaign.HeaderMediaLocalPath)
	baseDir, err := filepath.Abs(a.getMediaStoragePath())
	if err != nil {
		a.Log.Error("Storage configuration error", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Storage configuration error", nil, "")
		return
	}
	fullPath, err := filepath.Abs(filepath.Join(baseDir, filePath))
	if err != nil || !strings.HasPrefix(fullPath, baseDir+string(os.PathSeparator)) {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid file path", nil, "")
		return
	}

	// Reject symlinks
	info, err := os.Lstat(fullPath)
	if err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "File not found", nil, "")
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid file path", nil, "")
		return
	}

	// Read file
	data, err := os.ReadFile(fullPath)
	if err != nil {
		a.Log.Error("Failed to read media file", "path", fullPath, "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to read file", nil, "")
		return
	}

	// Use stored mime type or determine from extension
	contentType := campaign.HeaderMediaMimeType
	if contentType == "" {
		ext := strings.ToLower(filepath.Ext(filePath))
		contentType = getMimeTypeFromExtension(ext)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return
}

// getMimeTypeFromExtension returns MIME type from file extension
func getMimeTypeFromExtension(ext string) string {
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".3gp":
		return "video/3gpp"
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return "application/octet-stream"
	}
}

// incrementCampaignStat increments the appropriate campaign counter based on status
func (a *App) incrementCampaignStat(campaignID string, status string) {
	campaignUUID, err := uuid.Parse(campaignID)
	if err != nil {
		a.Log.Error("Invalid campaign ID for stats update", "campaign_id", campaignID)
		return
	}

	var column string
	switch models.MessageStatus(status) {
	case models.MessageStatusDelivered:
		column = "delivered_count"
	case models.MessageStatusRead:
		column = "read_count"
	case models.MessageStatusFailed:
		column = "failed_count"
	default:
		// sent is already counted during processCampaign
		return
	}

	var campaign models.BulkMessageCampaign
	campaign.ID = campaignUUID

	// atomic update and return updated record
	result := a.DB.Model(&campaign).
		Clauses(clause.Returning{}).
		Update(column, gorm.Expr(column+" + 1"))

	if result.Error != nil {
		a.Log.Error("Failed to increment campaign stat", "error", result.Error, "campaign_id", campaignID, "column", column)
		return
	}

	// Broadcast stats update via WebSocket
	if a.WSHub != nil && result.RowsAffected > 0 {
		a.WSHub.BroadcastToOrg(campaign.OrganizationID, websocket.WSMessage{
			Type: websocket.TypeCampaignStatsUpdate,
			Payload: map[string]any{
				"campaign_id":     campaignID,
				"status":          campaign.Status,
				"sent_count":      campaign.SentCount,
				"delivered_count": campaign.DeliveredCount,
				"read_count":      campaign.ReadCount,
				"failed_count":    campaign.FailedCount,
			},
		})
	}
}

// recalculateCampaignStats recalculates all campaign stats from messages table
func (a *App) recalculateCampaignStats(campaignID uuid.UUID) {
	var stats struct {
		Sent      int64
		Delivered int64
		Read      int64
		Failed    int64
	}

	if err := a.DB.Model(&models.Message{}).
		Where("metadata->>'campaign_id' = ?", campaignID.String()).
		Select(`
			COUNT(CASE WHEN status IN ('sent','delivered','read') THEN 1 END) as sent,
			COUNT(CASE WHEN status IN ('delivered','read') THEN 1 END) as delivered,
			COUNT(CASE WHEN status = 'read' THEN 1 END) as read,
			COUNT(CASE WHEN status = 'failed' THEN 1 END) as failed
		`).Scan(&stats).Error; err != nil {
		a.Log.Error("Failed to scan campaign message stats", "error", err, "campaign_id", campaignID)
		return
	}

	if err := a.DB.Model(&models.BulkMessageCampaign{}).Where("id = ?", campaignID).
		Updates(map[string]any{
			"sent_count":      stats.Sent,
			"delivered_count": stats.Delivered,
			"read_count":      stats.Read,
			"failed_count":    stats.Failed,
		}).Error; err != nil {
		a.Log.Error("Failed to recalculate campaign stats", "error", err, "campaign_id", campaignID)
	}
}

// sanitizeFilename removes path separators, dangerous characters, and truncates length.
var safeFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func sanitizeFilename(name string) string {
	// Strip any path component
	name = filepath.Base(name)
	// Replace unsafe characters
	name = safeFilenameRe.ReplaceAllString(name, "_")
	// Truncate to 255 chars
	if len(name) > 255 {
		name = name[:255]
	}
	if name == "" || name == "." || name == ".." {
		name = "unnamed"
	}
	return name
}
