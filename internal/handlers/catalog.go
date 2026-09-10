package handlers

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

// CatalogRequest represents the request body for creating a catalog
type CatalogRequest struct {
	WhatsAppAccount string `json:"whatsapp_account"`
	Name            string `json:"name"`
}

// CatalogResponse represents the API response for a catalog
type CatalogResponse struct {
	ID              uuid.UUID                `json:"id"`
	MetaCatalogID   string                   `json:"meta_catalog_id"`
	WhatsAppAccount string                   `json:"whatsapp_account"`
	Name            string                   `json:"name"`
	IsActive        bool                     `json:"is_active"`
	ProductCount    int                      `json:"product_count"`
	Products        []CatalogProductResponse `json:"products,omitempty"`
	CreatedAt       string                   `json:"created_at"`
	UpdatedAt       string                   `json:"updated_at"`
}

// CatalogProductRequest represents the request body for creating/updating a product
type CatalogProductRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       int64  `json:"price"` // Price in cents
	Currency    string `json:"currency"`
	URL         string `json:"url"`
	ImageURL    string `json:"image_url"`
	RetailerID  string `json:"retailer_id"` // SKU
}

// CatalogProductResponse represents the API response for a product
type CatalogProductResponse struct {
	ID            uuid.UUID `json:"id"`
	MetaProductID string    `json:"meta_product_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Price         int64     `json:"price"`
	Currency      string    `json:"currency"`
	URL           string    `json:"url"`
	ImageURL      string    `json:"image_url"`
	RetailerID    string    `json:"retailer_id"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     string    `json:"created_at"`
	UpdatedAt     string    `json:"updated_at"`
}

// SyncCatalogsRequest represents the request body for syncing catalogs
type SyncCatalogsRequest struct {
	WhatsAppAccount string `json:"whatsapp_account"`
}

// ListCatalogs returns all catalogs for the organization
func (a *App) ListCatalogs(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	whatsAppAccount := r.URL.Query().Get("whatsapp_account")

	query := a.DB.Where("organization_id = ?", orgID)
	if whatsAppAccount != "" {
		query = query.Where("whats_app_account = ?", whatsAppAccount)
	}

	var catalogs []models.Catalog
	if err := query.Order("name ASC").Find(&catalogs).Error; err != nil {
		a.Log.Error("Failed to list catalogs", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list catalogs", nil, "")
		return
	}

	result := make([]CatalogResponse, len(catalogs))
	for i, c := range catalogs {
		// Get product count
		var productCount int64
		a.DB.Model(&models.CatalogProduct{}).Where("catalog_id = ?", c.ID).Count(&productCount)
		result[i] = catalogToResponse(c, int(productCount))
	}

	SendEnvelope(w, map[string]any{
		"catalogs": result,
	})
	return
}

// CreateCatalog creates a new catalog in Meta and stores it locally
func (a *App) CreateCatalog(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	var req CatalogRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.Name == "" || req.WhatsAppAccount == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "name and whatsapp_account are required", nil, "")
		return
	}

	// Get WhatsApp account
	account, err := a.resolveWhatsAppAccount(orgID, req.WhatsAppAccount)
	if err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	// Create catalog in Meta
	ctx := context.Background()
	waAccount := a.toWhatsAppAccount(account)

	metaCatalogID, err := a.WhatsApp.CreateCatalog(ctx, waAccount, req.Name)
	if err != nil {
		a.Log.Error("Failed to create catalog in Meta", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create catalog", nil, "")
		return
	}

	// Store catalog locally
	catalog := models.Catalog{
		OrganizationID:  orgID,
		WhatsAppAccount: req.WhatsAppAccount,
		MetaCatalogID:   metaCatalogID,
		Name:            req.Name,
		IsActive:        true,
	}

	if err := a.DB.Create(&catalog).Error; err != nil {
		a.Log.Error("Failed to save catalog", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to save catalog", nil, "")
		return
	}

	SendEnvelope(w, catalogToResponse(catalog, 0))
	return
}

// GetCatalog returns a single catalog with its products
func (a *App) GetCatalog(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "catalog")
	if err != nil {
		return
	}

	var catalog models.Catalog
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).
		Preload("Products").First(&catalog).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Catalog not found", nil, "")
		return
	}

	resp := catalogToResponse(catalog, len(catalog.Products))
	resp.Products = make([]CatalogProductResponse, len(catalog.Products))
	for i, p := range catalog.Products {
		resp.Products[i] = productToResponse(p)
	}

	SendEnvelope(w, resp)
	return
}

// DeleteCatalog deletes a catalog from Meta and locally
func (a *App) DeleteCatalog(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "catalog")
	if err != nil {
		return
	}

	catalog, err := findByIDAndOrgHTTP[models.Catalog](a.DB, w, id, orgID, "Catalog")
	if err != nil {
		return
	}

	// Get WhatsApp account
	account, err := a.resolveWhatsAppAccount(orgID, catalog.WhatsAppAccount)
	if err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	// Delete from Meta
	ctx := context.Background()
	waAccount := a.toWhatsAppAccount(account)

	if err := a.WhatsApp.DeleteCatalog(ctx, waAccount, catalog.MetaCatalogID); err != nil {
		a.Log.Error("Failed to delete catalog from Meta", "error", err)
		// Continue with local deletion even if Meta fails
	}

	// Delete products first
	a.DB.Where("catalog_id = ?", id).Delete(&models.CatalogProduct{})

	// Delete catalog
	if err := a.DB.Delete(catalog).Error; err != nil {
		a.Log.Error("Failed to delete catalog", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete catalog", nil, "")
		return
	}

	SendEnvelope(w, map[string]string{"message": "Catalog deleted"})
	return
}

// SyncCatalogs syncs catalogs from Meta API
func (a *App) SyncCatalogs(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	var req SyncCatalogsRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.WhatsAppAccount == "" {
		SendErrorEnvelope(w, http.StatusBadRequest, "whatsapp_account is required", nil, "")
		return
	}

	// Get WhatsApp account
	account, err := a.resolveWhatsAppAccount(orgID, req.WhatsAppAccount)
	if err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	// Fetch catalogs from Meta
	ctx := context.Background()
	waAccount := a.toWhatsAppAccount(account)

	metaCatalogs, err := a.WhatsApp.ListCatalogs(ctx, waAccount)
	if err != nil {
		a.Log.Error("Failed to fetch catalogs from Meta", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to fetch catalogs", nil, "")
		return
	}

	// Sync each catalog
	synced := 0
	for _, mc := range metaCatalogs {
		var existing models.Catalog
		err := a.DB.Where("organization_id = ? AND meta_catalog_id = ?", orgID, mc.ID).First(&existing).Error
		if err != nil {
			// Create new catalog
			catalog := models.Catalog{
				OrganizationID:  orgID,
				WhatsAppAccount: req.WhatsAppAccount,
				MetaCatalogID:   mc.ID,
				Name:            mc.Name,
				IsActive:        true,
			}
			if err := a.DB.Create(&catalog).Error; err != nil {
				a.Log.Error("Failed to create synced catalog", "error", err, "meta_id", mc.ID)
				continue
			}
			synced++
		} else {
			// Update existing
			existing.Name = mc.Name
			a.DB.Save(&existing)
			synced++
		}
	}

	SendEnvelope(w, map[string]any{
		"message": "Catalogs synced",
		"synced":  synced,
		"total":   len(metaCatalogs),
	})
	return
}

// ListCatalogProducts returns all products in a catalog
func (a *App) ListCatalogProducts(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	catalogID, err := parsePathUUIDHTTP(w, r, "id", "catalog")
	if err != nil {
		return
	}

	// Verify catalog belongs to org
	catalog, err := findByIDAndOrgHTTP[models.Catalog](a.DB, w, catalogID, orgID, "Catalog")
	if err != nil {
		return
	}
	_ = catalog

	var products []models.CatalogProduct
	if err := a.DB.Where("catalog_id = ?", catalogID).Order("name ASC").Find(&products).Error; err != nil {
		a.Log.Error("Failed to list products", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to list products", nil, "")
		return
	}

	result := make([]CatalogProductResponse, len(products))
	for i, p := range products {
		result[i] = productToResponse(p)
	}

	SendEnvelope(w, map[string]any{
		"products": result,
	})
	return
}

// CreateCatalogProduct creates a new product in a catalog
func (a *App) CreateCatalogProduct(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	catalogID, err := parsePathUUIDHTTP(w, r, "id", "catalog")
	if err != nil {
		return
	}

	var req CatalogProductRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	if req.Name == "" || req.Price <= 0 {
		SendErrorEnvelope(w, http.StatusBadRequest, "name and price are required", nil, "")
		return
	}

	// Get catalog and verify ownership
	catalog, err := findByIDAndOrgHTTP[models.Catalog](a.DB, w, catalogID, orgID, "Catalog")
	if err != nil {
		return
	}

	// Get WhatsApp account
	account, err := a.resolveWhatsAppAccount(orgID, catalog.WhatsAppAccount)
	if err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	// Set defaults
	if req.Currency == "" {
		req.Currency = "USD"
	}

	// Create product in Meta
	ctx := context.Background()
	waAccount := a.toWhatsAppAccount(account)

	productInput := &whatsapp.ProductInput{
		Name:        req.Name,
		Price:       req.Price,
		Currency:    req.Currency,
		URL:         req.URL,
		ImageURL:    req.ImageURL,
		RetailerID:  req.RetailerID,
		Description: req.Description,
	}

	metaProductID, err := a.WhatsApp.CreateProduct(ctx, waAccount, catalog.MetaCatalogID, productInput)
	if err != nil {
		a.Log.Error("Failed to create product in Meta", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to create product", nil, "")
		return
	}

	// Store product locally
	product := models.CatalogProduct{
		OrganizationID: orgID,
		CatalogID:      catalogID,
		MetaProductID:  metaProductID,
		Name:           req.Name,
		Description:    req.Description,
		Price:          req.Price,
		Currency:       req.Currency,
		URL:            req.URL,
		ImageURL:       req.ImageURL,
		RetailerID:     req.RetailerID,
		IsActive:       true,
	}

	if err := a.DB.Create(&product).Error; err != nil {
		a.Log.Error("Failed to save product", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to save product", nil, "")
		return
	}

	SendEnvelope(w, productToResponse(product))
	return
}

// GetCatalogProduct returns a single product
func (a *App) GetCatalogProduct(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "product")
	if err != nil {
		return
	}

	product, err := findByIDAndOrgHTTP[models.CatalogProduct](a.DB, w, id, orgID, "Product")
	if err != nil {
		return
	}

	SendEnvelope(w, productToResponse(*product))
	return
}

// UpdateCatalogProduct updates a product
func (a *App) UpdateCatalogProduct(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "product")
	if err != nil {
		return
	}

	product, err := findByIDAndOrgHTTP[models.CatalogProduct](a.DB, w, id, orgID, "Product")
	if err != nil {
		return
	}

	var req CatalogProductRequest
	if err := a.decodeRequestHTTP(w, r, &req); err != nil {
		return
	}

	// Get catalog to get WhatsApp account
	var catalog models.Catalog
	if err := a.DB.Where("id = ?", product.CatalogID).First(&catalog).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Catalog not found", nil, "")
		return
	}

	// Get WhatsApp account
	account, err := a.resolveWhatsAppAccount(orgID, catalog.WhatsAppAccount)
	if err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	// Update product in Meta
	ctx := context.Background()
	waAccount := a.toWhatsAppAccount(account)

	productInput := &whatsapp.ProductInput{
		Name:        req.Name,
		Price:       req.Price,
		Currency:    req.Currency,
		URL:         req.URL,
		ImageURL:    req.ImageURL,
		Description: req.Description,
	}

	if err := a.WhatsApp.UpdateProduct(ctx, waAccount, product.MetaProductID, productInput); err != nil {
		a.Log.Error("Failed to update product in Meta", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to update product", nil, "")
		return
	}

	// Update locally
	if req.Name != "" {
		product.Name = req.Name
	}
	if req.Description != "" {
		product.Description = req.Description
	}
	if req.Price > 0 {
		product.Price = req.Price
	}
	if req.Currency != "" {
		product.Currency = req.Currency
	}
	if req.URL != "" {
		product.URL = req.URL
	}
	if req.ImageURL != "" {
		product.ImageURL = req.ImageURL
	}
	if req.RetailerID != "" {
		product.RetailerID = req.RetailerID
	}

	if err := a.DB.Save(product).Error; err != nil {
		a.Log.Error("Failed to save product", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to save product", nil, "")
		return
	}

	SendEnvelope(w, productToResponse(*product))
	return
}

// DeleteCatalogProduct deletes a product
func (a *App) DeleteCatalogProduct(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.getOrgIDHTTP(r)
	if err != nil {
		SendErrorEnvelope(w, http.StatusUnauthorized, "Unauthorized", nil, "")
		return
	}

	id, err := parsePathUUIDHTTP(w, r, "id", "product")
	if err != nil {
		return
	}

	product, err := findByIDAndOrgHTTP[models.CatalogProduct](a.DB, w, id, orgID, "Product")
	if err != nil {
		return
	}

	// Get catalog to get WhatsApp account
	var catalog models.Catalog
	if err := a.DB.Where("id = ?", product.CatalogID).First(&catalog).Error; err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "Catalog not found", nil, "")
		return
	}

	// Get WhatsApp account
	account, err := a.resolveWhatsAppAccount(orgID, catalog.WhatsAppAccount)
	if err != nil {
		SendErrorEnvelope(w, http.StatusNotFound, "WhatsApp account not found", nil, "")
		return
	}

	// Delete from Meta
	ctx := context.Background()
	waAccount := a.toWhatsAppAccount(account)

	if err := a.WhatsApp.DeleteProduct(ctx, waAccount, product.MetaProductID); err != nil {
		a.Log.Error("Failed to delete product from Meta", "error", err)
		// Continue with local deletion
	}

	if err := a.DB.Delete(product).Error; err != nil {
		a.Log.Error("Failed to delete product", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to delete product", nil, "")
		return
	}

	SendEnvelope(w, map[string]string{"message": "Product deleted"})
	return
}

// Helper functions

func catalogToResponse(c models.Catalog, productCount int) CatalogResponse {
	return CatalogResponse{
		ID:              c.ID,
		MetaCatalogID:   c.MetaCatalogID,
		WhatsAppAccount: c.WhatsAppAccount,
		Name:            c.Name,
		IsActive:        c.IsActive,
		ProductCount:    productCount,
		CreatedAt:       c.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:       c.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func productToResponse(p models.CatalogProduct) CatalogProductResponse {
	return CatalogProductResponse{
		ID:            p.ID,
		MetaProductID: p.MetaProductID,
		Name:          p.Name,
		Description:   p.Description,
		Price:         p.Price,
		Currency:      p.Currency,
		URL:           p.URL,
		ImageURL:      p.ImageURL,
		RetailerID:    p.RetailerID,
		IsActive:      p.IsActive,
		CreatedAt:     p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:     p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}
