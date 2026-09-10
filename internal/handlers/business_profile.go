package handlers

import (
	"context"
	"time"

	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"net/http"
)

// businessProfileHTTPTimeout bounds calls to Meta's profile endpoints. We use a
// detached context (not the request context) so a client disconnect mid-call
// doesn't cancel an in-flight Meta update.
const businessProfileHTTPTimeout = 30 * time.Second

// GetBusinessProfile returns the business profile for a WhatsApp account
func (a *App) GetBusinessProfile(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "account")
	if err != nil {
		return
	}

	account, err := a.resolveWhatsAppAccountByIDHTTP(w, id, orgID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessProfileHTTPTimeout)
	defer cancel()

	profile, err := a.WhatsApp.GetBusinessProfile(ctx, a.toWhatsAppAccount(account))
	if err != nil {
		a.Log.Error("Failed to get business profile", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to get business profile", nil, "")
		return
	}

	SendEnvelope(w, profile)
	return
}

// UpdateBusinessProfile updates the business profile for a WhatsApp account
func (a *App) UpdateBusinessProfile(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "account")
	if err != nil {
		return
	}

	account, err := a.resolveWhatsAppAccountByIDHTTP(w, id, orgID)
	if err != nil {
		return
	}

	var input whatsapp.BusinessProfileInput
	if err := a.decodeRequestHTTP(w, r, &input); err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessProfileHTTPTimeout)
	defer cancel()
	waAccount := a.toWhatsAppAccount(account)

	if err := a.WhatsApp.UpdateBusinessProfile(ctx, waAccount, input); err != nil {
		a.Log.Error("Failed to update business profile", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update business profile", nil, "")
		return
	}

	// Re-fetch to ensure we have the latest state
	profile, err := a.WhatsApp.GetBusinessProfile(ctx, waAccount)
	if err != nil {
		// If re-fetch fails, just return success message
		SendEnvelope(w, map[string]string{"message": "Profile updated successfully"})
		return
	}

	SendEnvelope(w, profile)
	return
}

// UpdateProfilePicture handles the profile picture upload
func (a *App) UpdateProfilePicture(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "account")
	if err != nil {
		return
	}

	account, err := a.resolveWhatsAppAccountByIDHTTP(w, id, orgID)
	if err != nil {
		return
	}

	// 1. Get the file from request
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid multipart form", nil, "")
		return
	}
	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Missing file", nil, "")
		return
	}
	defer file.Close() //nolint:errcheck

	// 2. Read file
	fileSize := fileHeader.Size
	fileContent := make([]byte, fileSize)
	_, err = file.Read(fileContent)
	if err != nil {
		a.Log.Error("Failed to read file", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to read file", nil, "")
		return
	}

	// Use a longer timeout for upload — profile pictures can be a few MB.
	ctx, cancel := context.WithTimeout(context.Background(), 2*businessProfileHTTPTimeout)
	defer cancel()
	waAccount := a.toWhatsAppAccount(account)

	// Upload to Meta to get handle
	handle, err := a.WhatsApp.UploadProfilePicture(ctx, waAccount, fileContent, fileHeader.Header.Get("Content-Type"))
	if err != nil {
		a.Log.Error("Failed to upload profile picture", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to upload profile picture", nil, "")
		return
	}

	// Update Business Profile with the handle
	input := whatsapp.BusinessProfileInput{
		MessagingProduct:     "whatsapp",
		ProfilePictureHandle: handle,
	}

	err = a.WhatsApp.UpdateBusinessProfile(ctx, waAccount, input)

	if err != nil {
		a.Log.Error("Failed to update profile request", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Uploaded but failed to set profile picture", nil, "")
		return
	}

	SendEnvelope(w, map[string]string{
		"message": "Profile picture updated successfully",
		"handle":  handle,
	})
	return
}
