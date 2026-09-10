package handlers

import (
	"context"
	"net/http"
	"sync"

	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/assignment"
	"github.com/shridarpatil/whatomate/internal/calling"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/storage"
	"github.com/shridarpatil/whatomate/internal/tts"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// App holds all dependencies for handlers
type App struct {
	Config            *config.Config
	DB                *gorm.DB
	Redis             *redis.Client
	Log               logf.Logger
	WhatsApp          *whatsapp.Client
	WSHub             *websocket.Hub
	Queue             queue.Queue
	CampaignSubCancel context.CancelFunc
	// HTTPClient is a shared HTTP client with connection pooling for external API calls
	HTTPClient *http.Client
	// Assigner provides shared team-based agent assignment (used by both chat and call transfers)
	Assigner *assignment.Assigner
	// CallManager handles WebRTC call sessions (nil when calling is disabled)
	CallManager *calling.Manager
	// TTS generates audio from text for IVR greetings (nil when not configured)
	TTS *tts.PiperTTS
	// S3Client for serving call recording presigned URLs (nil when not configured)
	S3Client *storage.S3Client
	// wg tracks background goroutines for graceful shutdown
	wg sync.WaitGroup
}

// WaitForBackgroundTasks blocks until all background goroutines complete.
// Call this during graceful shutdown to ensure all async work finishes.
func (a *App) WaitForBackgroundTasks() {
	a.wg.Wait()
}

// HealthCheck returns server health status (native net/http).
func (a *App) HealthCheck(w http.ResponseWriter, r *http.Request) {
	SendEnvelope(w, map[string]string{
		"status":  "ok",
		"service": "whatomate",
	})
}

// ReadyCheck returns server readiness status (native net/http).
func (a *App) ReadyCheck(w http.ResponseWriter, r *http.Request) {
	sqlDB, err := a.DB.DB()
	if err != nil {
		a.Log.Error("Database connection error", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Database connection error", nil, "")
		return
	}
	if err := sqlDB.Ping(); err != nil {
		a.Log.Error("Database ping failed", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Database ping failed", nil, "")
		return
	}

	if err := a.Redis.Ping(r.Context()).Err(); err != nil {
		a.Log.Error("Redis connection error", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Redis connection error", nil, "")
		return
	}

	SendEnvelope(w, map[string]string{
		"status": "ready",
	})
}

// GetEmbeddedSignupConfig returns public configuration values for the embedded signup flow
func (a *App) GetEmbeddedSignupConfig(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	appID, _, configID, err := a.resolveMetaAppCreds(orgID)
	if err != nil {
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to resolve credentials", nil, "")
		return
	}

	type EmbeddedSignupConfig struct {
		WhatsAppAppID      string `json:"whatsapp_app_id,omitempty"`
		WhatsAppConfigID   string `json:"whatsapp_config_id,omitempty"`
		WhatsAppAPIVersion string `json:"whatsapp_api_version,omitempty"`
	}

	config := EmbeddedSignupConfig{
		WhatsAppAppID:      appID,
		WhatsAppConfigID:   configID,
		WhatsAppAPIVersion: a.Config.WhatsApp.APIVersion,
	}

	SendEnvelope(w, config)
}

// StartCampaignStatsSubscriber starts listening for campaign stats updates from Redis pub/sub
// and broadcasts them via WebSocket
func (a *App) StartCampaignStatsSubscriber() error {
	if a.WSHub == nil {
		a.Log.Warn("WebSocket hub not initialized, skipping campaign stats subscriber")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.CampaignSubCancel = cancel

	subscriber := queue.NewSubscriber(a.Redis, a.Log)

	err := subscriber.SubscribeCampaignStats(ctx, func(update *queue.CampaignStatsUpdate) {
		a.Log.Debug("Received campaign stats update from Redis",
			"campaign_id", update.CampaignID,
			"status", update.Status,
			"sent", update.SentCount,
		)

		// Broadcast to organization via WebSocket
		a.WSHub.BroadcastToOrg(update.OrganizationID, websocket.WSMessage{
			Type: websocket.TypeCampaignStatsUpdate,
			Payload: map[string]any{
				"campaign_id":     update.CampaignID,
				"status":          update.Status,
				"sent_count":      update.SentCount,
				"delivered_count": update.DeliveredCount,
				"read_count":      update.ReadCount,
				"failed_count":    update.FailedCount,
			},
		})
	})

	if err != nil {
		cancel()
		return err
	}

	a.Log.Info("Campaign stats subscriber started")
	return nil
}

// StopCampaignStatsSubscriber stops the campaign stats subscriber
func (a *App) StopCampaignStatsSubscriber() {
	if a.CampaignSubCancel != nil {
		a.CampaignSubCancel()
	}
}
