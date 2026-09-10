package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// FreeServiceAllowancePerPhone is Meta's monthly free service-message tier
// per business phone number (effective Oct 2026 pricing). Tracked approx from
// outbound non-template sends; not a billable ledger.
const FreeServiceAllowancePerPhone int64 = 1000

// UsageCategoryCounts holds approximate outbound send counts by Meta category.
type UsageCategoryCounts struct {
	Service        int64 `json:"service"`
	Utility        int64 `json:"utility"`
	Marketing      int64 `json:"marketing"`
	Authentication int64 `json:"authentication"`
	Unknown        int64 `json:"unknown"`
	TotalOutbound  int64 `json:"total_outbound"`
}

// ServiceAllowanceRemaining reports the 1k free service allowance for a phone.
type ServiceAllowanceRemaining struct {
	Limit     int64 `json:"limit"`
	Used      int64 `json:"used"`
	Remaining int64 `json:"remaining"`
}

// PhoneUsage is per-WhatsApp-account (phone number) usage for a period.
type PhoneUsage struct {
	WhatsAppAccount  string                    `json:"whatsapp_account"`
	PhoneID          string                    `json:"phone_id"`
	Counts           UsageCategoryCounts       `json:"counts"`
	ServiceAllowance ServiceAllowanceRemaining `json:"service_allowance"`
}

// UsagePeriod describes the aggregation window.
type UsagePeriod struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Label string `json:"label"`
}

// UsageResponse is returned by GET /api/usage and GET /api/billing/usage.
type UsageResponse struct {
	Period               UsagePeriod         `json:"period"`
	FreeServiceAllowance int64               `json:"free_service_allowance"`
	Phones               []PhoneUsage        `json:"phones"`
	Totals               UsageCategoryCounts `json:"totals"`
}

type usageRow struct {
	WhatsAppAccount string
	Category        string
	Count           int64
}

// GetUsage returns approximate outbound message send counts by category for
// Kapso-beater / cost-meter DX. Categories:
//   - service: non-template outbound (session / free-form)
//   - utility / marketing / authentication: template outbound via template.category
//   - unknown: template with missing/unmatched category
//
// Query params: from, to (YYYY-MM-DD, inclusive). Default: current UTC calendar month.
// Optional: account (WhatsApp account name filter).
func (a *App) GetUsage(w http.ResponseWriter, r *http.Request) {
	orgID, _, err := a.requireAuthHTTP(w, r, models.ResourceAnalytics, models.ActionRead)
	if err != nil {
		return
	}

	periodStart, periodEnd, errMsg := parseUsagePeriod(r)
	if errMsg != "" {
		SendErrorEnvelope(w, http.StatusBadRequest, errMsg, nil, "")
		return
	}

	accountFilter := strings.TrimSpace(r.URL.Query().Get("account"))

	rows, err := a.queryOutboundUsage(orgID, periodStart, periodEnd, accountFilter)
	if err != nil {
		a.Log.Error("Failed to query usage", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to query usage", nil, "")
		return
	}

	// Load accounts for phone_id mapping (include zero-usage phones in org).
	var accounts []models.WhatsAppAccount
	aq := a.DB.Where("organization_id = ?", orgID)
	if accountFilter != "" {
		aq = aq.Where("name = ?", accountFilter)
	}
	if err := aq.Find(&accounts).Error; err != nil {
		a.Log.Error("Failed to list accounts for usage", "error", err)
		SendErrorEnvelope(w, http.StatusInternalServerError, "Failed to query usage", nil, "")
		return
	}

	byAccount := map[string]*UsageCategoryCounts{}
	for _, row := range rows {
		c := byAccount[row.WhatsAppAccount]
		if c == nil {
			c = &UsageCategoryCounts{}
			byAccount[row.WhatsAppAccount] = c
		}
		addUsageCategory(c, row.Category, row.Count)
	}

	phones := make([]PhoneUsage, 0, len(accounts))
	totals := UsageCategoryCounts{}
	seen := map[string]bool{}

	for _, acc := range accounts {
		counts := UsageCategoryCounts{}
		if c := byAccount[acc.Name]; c != nil {
			counts = *c
		}
		remaining := FreeServiceAllowancePerPhone - counts.Service
		if remaining < 0 {
			remaining = 0
		}
		phones = append(phones, PhoneUsage{
			WhatsAppAccount: acc.Name,
			PhoneID:         acc.PhoneID,
			Counts:          counts,
			ServiceAllowance: ServiceAllowanceRemaining{
				Limit:     FreeServiceAllowancePerPhone,
				Used:      counts.Service,
				Remaining: remaining,
			},
		})
		addUsageTotals(&totals, counts)
		seen[acc.Name] = true
	}

	// Include orphaned message account names not in current accounts table.
	for name, c := range byAccount {
		if seen[name] {
			continue
		}
		remaining := FreeServiceAllowancePerPhone - c.Service
		if remaining < 0 {
			remaining = 0
		}
		phones = append(phones, PhoneUsage{
			WhatsAppAccount: name,
			PhoneID:         "",
			Counts:          *c,
			ServiceAllowance: ServiceAllowanceRemaining{
				Limit:     FreeServiceAllowancePerPhone,
				Used:      c.Service,
				Remaining: remaining,
			},
		})
		addUsageTotals(&totals, *c)
	}

	label := periodStart.UTC().Format("2006-01")
	if periodStart.UTC().Month() != periodEnd.UTC().Month() || periodStart.UTC().Year() != periodEnd.UTC().Year() {
		label = periodStart.UTC().Format("2006-01-02") + ".." + periodEnd.UTC().Format("2006-01-02")
	}

	SendEnvelope(w, UsageResponse{
		Period: UsagePeriod{
			Start: periodStart.UTC().Format(time.RFC3339Nano),
			End:   periodEnd.UTC().Format(time.RFC3339Nano),
			Label: label,
		},
		FreeServiceAllowance: FreeServiceAllowancePerPhone,
		Phones:               phones,
		Totals:               totals,
	})
}

func parseUsagePeriod(r *http.Request) (start, end time.Time, errMsg string) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	now := time.Now().UTC()
	if fromStr == "" && toStr == "" {
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		// End of current month (exclusive next month start - 1ns) or now — use end of month for stable windows
		end = start.AddDate(0, 1, 0).Add(-time.Nanosecond)
		return start, end, ""
	}
	if fromStr == "" || toStr == "" {
		return time.Time{}, time.Time{}, "Both from and to are required (YYYY-MM-DD), or omit both for the current UTC month"
	}
	start, end, errMsg = parseDateRange(fromStr, toStr)
	return start, end, errMsg
}

func (a *App) queryOutboundUsage(orgID uuid.UUID, start, end time.Time, account string) ([]usageRow, error) {
	// Approximate category:
	// - non-template outbound → service
	// - template: join templates by org+account+name for category (uppercased)
	// Failed sends are still counted as attempts (outbound sends).
	sql := `
SELECT whats_app_account, category, COUNT(*) AS count
FROM (
  SELECT m.whats_app_account AS whats_app_account,
         CASE
           WHEN m.message_type = ? THEN COALESCE(NULLIF(UPPER(t.category), ''), 'UNKNOWN')
           ELSE 'SERVICE'
         END AS category
  FROM messages m
  LEFT JOIN templates t
    ON t.organization_id = m.organization_id
   AND t.whats_app_account = m.whats_app_account
   AND t.name = m.template_name
   AND t.deleted_at IS NULL
  WHERE m.organization_id = ?
    AND m.direction = ?
    AND m.deleted_at IS NULL
    AND m.created_at >= ?
    AND m.created_at <= ?`

	args := []any{
		models.MessageTypeTemplate,
		orgID,
		models.DirectionOutgoing,
		start,
		end,
	}
	if account != "" {
		sql += `
    AND m.whats_app_account = ?`
		args = append(args, account)
	}
	sql += `
) categorized
GROUP BY whats_app_account, category`

	var rows []usageRow
	if err := a.DB.Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func addUsageCategory(c *UsageCategoryCounts, category string, n int64) {
	switch strings.ToUpper(category) {
	case "SERVICE":
		c.Service += n
	case "UTILITY":
		c.Utility += n
	case "MARKETING":
		c.Marketing += n
	case "AUTHENTICATION", "AUTH":
		c.Authentication += n
	default:
		c.Unknown += n
	}
	c.TotalOutbound += n
}

func addUsageTotals(dst *UsageCategoryCounts, src UsageCategoryCounts) {
	dst.Service += src.Service
	dst.Utility += src.Utility
	dst.Marketing += src.Marketing
	dst.Authentication += src.Authentication
	dst.Unknown += src.Unknown
	dst.TotalOutbound += src.TotalOutbound
}
