package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetUsage_CountsByCategoryAndAllowance(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Usage Reader", false, false, perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("usage")),
		testutil.WithRoleID(&role.ID),
	)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))

	utilityTpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	require.NoError(t, app.DB.Model(utilityTpl).Update("category", "UTILITY").Error)

	marketingTpl := &models.Template{
		OrganizationID:  org.ID,
		WhatsAppAccount: account.Name,
		Name:            "promo_" + uuid.New().String()[:8],
		DisplayName:     "Promo",
		Language:        "en",
		Category:        "MARKETING",
		Status:          "APPROVED",
		BodyContent:     "Hello {{1}}",
	}
	require.NoError(t, app.DB.Create(marketingTpl).Error)

	now := time.Now().UTC()
	mkMsg := func(msgType models.MessageType, templateName string) {
		m := &models.Message{
			OrganizationID:  org.ID,
			WhatsAppAccount: account.Name,
			ContactID:       contact.ID,
			Direction:       models.DirectionOutgoing,
			MessageType:     msgType,
			Status:          models.MessageStatusSent,
			Content:         "x",
			TemplateName:    templateName,
		}
		require.NoError(t, app.DB.Create(m).Error)
		require.NoError(t, app.DB.Model(m).UpdateColumn("created_at", now).Error)
	}

	mkMsg(models.MessageTypeText, "")
	mkMsg(models.MessageTypeText, "")
	mkMsg(models.MessageTypeInteractive, "")
	mkMsg(models.MessageTypeTemplate, utilityTpl.Name)
	mkMsg(models.MessageTypeTemplate, utilityTpl.Name)
	mkMsg(models.MessageTypeTemplate, marketingTpl.Name)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.InvokeHTTP(t, app.GetUsage, req)

	require.Equal(t, http.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Status string                 `json:"status"`
		Data   handlers.UsageResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, "success", resp.Status)
	assert.Equal(t, int64(1000), resp.Data.FreeServiceAllowance)
	require.NotEmpty(t, resp.Data.Phones)

	var phone *handlers.PhoneUsage
	for i := range resp.Data.Phones {
		if resp.Data.Phones[i].WhatsAppAccount == account.Name {
			phone = &resp.Data.Phones[i]
			break
		}
	}
	require.NotNil(t, phone)
	assert.Equal(t, account.PhoneID, phone.PhoneID)
	assert.Equal(t, int64(3), phone.Counts.Service)
	assert.Equal(t, int64(2), phone.Counts.Utility)
	assert.Equal(t, int64(1), phone.Counts.Marketing)
	assert.Equal(t, int64(0), phone.Counts.Authentication)
	assert.Equal(t, int64(6), phone.Counts.TotalOutbound)
	assert.Equal(t, int64(1000), phone.ServiceAllowance.Limit)
	assert.Equal(t, int64(3), phone.ServiceAllowance.Used)
	assert.Equal(t, int64(997), phone.ServiceAllowance.Remaining)
	assert.Equal(t, int64(3), resp.Data.Totals.Service)
	assert.Equal(t, int64(6), resp.Data.Totals.TotalOutbound)
}

func TestGetUsage_UnauthorizedWithoutAuth(t *testing.T) {
	app := newTestApp(t)
	req := testutil.NewGETRequest(t)
	testutil.InvokeHTTP(t, app.GetUsage, req)
	assert.Equal(t, http.StatusUnauthorized, testutil.GetResponseStatusCode(req))
}

func TestGetUsage_InvalidDateRange(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Usage Reader 2", false, false, perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("usage-bad")),
		testutil.WithRoleID(&role.ID),
	)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "from", "2026-09-01")
	testutil.InvokeHTTP(t, app.GetUsage, req)
	assert.Equal(t, http.StatusBadRequest, testutil.GetResponseStatusCode(req))
}
