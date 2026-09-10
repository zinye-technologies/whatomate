package handlers

import (
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shridarpatil/whatomate/internal/models"
	"net/http"
)

// TagRequest represents the request body for creating/updating a tag
type TagRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// TagResponse represents the API response for a tag
type TagResponse struct {
	Name      string `json:"name"`
	Color     string `json:"color"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListTags returns all tags for the organization
func (a *App) ListTags(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceTags, models.ActionRead)
	if err != nil {
		return
	}

	pg := parsePaginationHTTP(r)
	search := strings.ToLower(r.URL.Query().Get("search"))

	tags, err := a.getTagsCached(orgID)
	if err != nil {
		a.Log.Error("Failed to list tags", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list tags", nil, "")
		return
	}

	// Apply search filter (case-insensitive) - search by name or color
	if search != "" {
		filtered := make([]models.Tag, 0)
		for _, tag := range tags {
			if strings.Contains(strings.ToLower(tag.Name), search) ||
				strings.Contains(strings.ToLower(tag.Color), search) {
				filtered = append(filtered, tag)
			}
		}
		tags = filtered
	}

	total := len(tags)

	// Apply pagination
	start := pg.Offset
	end := pg.Offset + pg.Limit
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	result := make([]TagResponse, 0, end-start)
	for i := start; i < end; i++ {
		result = append(result, tagToResponse(tags[i]))
	}

	SendEnvelope(w, listEnvelope("tags", result, total, pg))
	return
}

// CreateTag creates a new tag
func (a *App) CreateTag(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceTags, models.ActionWrite)
	if err != nil {
		return
	}

	var req TagRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.Name == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "name is required", nil, "")
		return
	}

	if len(req.Name) > 50 {
		SendErrorEnvelope(w, http.StatusBadRequest, "name must be at most 50 characters", nil, "")
		return
	}

	if !models.IsValidTagColor(req.Color) {
		SendErrorEnvelope(w, http.StatusBadRequest, "invalid color. Valid colors: blue, red, green, yellow, purple, gray", nil, "")
		return
	}

	// Check for duplicate name
	var existing models.Tag
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, req.Name).First(&existing).Error; err == nil {
		SendErrorEnvelope(w, http.StatusConflict, "Tag with this name already exists", nil, "")
		return
	}

	tag := models.Tag{
		OrganizationID: orgID,
		Name:           req.Name,
		Color:          req.Color,
	}

	if err := a.DB.Create(&tag).Error; err != nil {
		a.Log.Error("Failed to create tag", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create tag", nil, "")
		return
	}

	// Invalidate cache
	a.InvalidateTagsCache(orgID)

	SendEnvelope(w, tagToResponse(tag))
	return
}

// UpdateTag updates an existing tag
func (a *App) UpdateTag(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceTags, models.ActionWrite)
	if err != nil {
		return
	}

	// Get tag name from path (URL-encoded)
	tagNameEncoded := chi.URLParam(r, "name")
	tagName, err := url.PathUnescape(tagNameEncoded)
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid tag name", nil, "")
		return
	}

	var tag models.Tag
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, tagName).First(&tag).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Tag not found", nil, "")
		return
	}

	var req TagRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	// Validate color if provided
	if req.Color != "" && !models.IsValidTagColor(req.Color) {
		SendErrorEnvelope(w, http.StatusBadRequest, "invalid color. Valid colors: blue, red, green, yellow, purple, gray", nil, "")
		return
	}

	// Check if renaming and new name already exists
	if req.Name != "" && req.Name != tag.Name {
		if len(req.Name) > 50 {
			SendErrorEnvelope(w, http.StatusBadRequest, "name must be at most 50 characters", nil, "")
			return
		}
		var existing models.Tag
		if err := a.DB.Where("organization_id = ? AND name = ?", orgID, req.Name).First(&existing).Error; err == nil {
			SendErrorEnvelope(w, http.StatusConflict, "Tag with this name already exists", nil, "")
			return
		}
	}

	// If renaming, we need to delete old and create new (composite primary key)
	if req.Name != "" && req.Name != tag.Name {
		// Update contacts that use this tag
		// Note: Tags are stored as JSONB array of strings in contacts
		// This requires a raw SQL update
		if err := a.DB.Exec(`
			UPDATE contacts
			SET tags = (
				SELECT jsonb_agg(
					CASE WHEN elem::text = ? THEN ?::jsonb ELSE elem END
				)
				FROM jsonb_array_elements(COALESCE(tags, '[]'::jsonb)) elem
			)
			WHERE organization_id = ?
			AND tags @> ?::jsonb
		`, `"`+tagName+`"`, `"`+req.Name+`"`, orgID, `["`+tagName+`"]`).Error; err != nil {
			a.Log.Error("Failed to update contacts with renamed tag", "error", err)
			// Continue anyway - tag rename will still work
		}

		// Delete old tag
		if err := a.DB.Delete(&tag).Error; err != nil {
			a.Log.Error("Failed to delete old tag", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update tag", nil, "")
			return
		}

		// Create new tag
		newTag := models.Tag{
			OrganizationID: orgID,
			Name:           req.Name,
			Color:          req.Color,
		}
		if newTag.Color == "" {
			newTag.Color = tag.Color
		}

		if err := a.DB.Create(&newTag).Error; err != nil {
			a.Log.Error("Failed to create renamed tag", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update tag", nil, "")
			return
		}

		// Invalidate cache
		a.InvalidateTagsCache(orgID)

		SendEnvelope(w, tagToResponse(newTag))
		return
	}

	// Just updating color - use Updates for composite primary key
	if req.Color != "" && req.Color != tag.Color {
		if err := a.DB.Model(&models.Tag{}).
			Where("organization_id = ? AND name = ?", orgID, tagName).
			Update("color", req.Color).Error; err != nil {
			a.Log.Error("Failed to update tag", "error", err)
			SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update tag", nil, "")
			return
		}
		tag.Color = req.Color

		// Invalidate cache
		a.InvalidateTagsCache(orgID)
	}

	// Reload tag to get updated timestamp
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, tagName).First(&tag).Error; err != nil {
		a.Log.Error("Failed to reload tag", "error", err)
	}

	SendEnvelope(w, tagToResponse(tag))
	return
}

// DeleteTag deletes a tag
func (a *App) DeleteTag(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceTags, models.ActionDelete)
	if err != nil {
		return
	}

	// Get tag name from path (URL-encoded)
	tagNameEncoded := chi.URLParam(r, "name")
	tagName, err := url.PathUnescape(tagNameEncoded)
	if err != nil {
		SendErrorEnvelope(w, http.StatusBadRequest, "Invalid tag name", nil, "")
		return
	}

	var tag models.Tag
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, tagName).First(&tag).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Tag not found", nil, "")
		return
	}

	// Remove tag from all contacts that have it
	if err := a.DB.Exec(`
		UPDATE contacts
		SET tags = (
			SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb)
			FROM jsonb_array_elements(COALESCE(tags, '[]'::jsonb)) elem
			WHERE elem::text != ?
		)
		WHERE organization_id = ?
		AND tags @> ?::jsonb
	`, `"`+tagName+`"`, orgID, `["`+tagName+`"]`).Error; err != nil {
		a.Log.Error("Failed to remove tag from contacts", "error", err)
		// Continue anyway - tag deletion will still work
	}

	if err := a.DB.Delete(&tag).Error; err != nil {
		a.Log.Error("Failed to delete tag", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete tag", nil, "")
		return
	}

	// Invalidate cache
	a.InvalidateTagsCache(orgID)

	SendEnvelope(w, map[string]string{"message": "Tag deleted"})
	return
}

func tagToResponse(tag models.Tag) TagResponse {
	return TagResponse{
		Name:      tag.Name,
		Color:     tag.Color,
		CreatedAt: tag.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: tag.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}
