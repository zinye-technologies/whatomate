package handlers

import (
	"net/http"
)

// Stub handlers - not yet implemented

// Message handlers
func (a *App) MarkMessageRead(w http.ResponseWriter, r *http.Request) {
	SendErrorEnvelope(w, http.StatusNotImplemented, "Not implemented yet", nil, "")
	return
}

// Analytics handlers
func (a *App) GetMessageAnalytics(w http.ResponseWriter, r *http.Request) {
	SendErrorEnvelope(w, http.StatusNotImplemented, "Not implemented yet", nil, "")
	return
}

func (a *App) GetChatbotAnalytics(w http.ResponseWriter, r *http.Request) {
	SendErrorEnvelope(w, http.StatusNotImplemented, "Not implemented yet", nil, "")
	return
}
