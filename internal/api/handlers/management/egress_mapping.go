package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// GetEgressMapping returns a read-only observability snapshot for account egress mappings.
func (h *Handler) GetEgressMapping(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusOK, gin.H{
			"available":                false,
			"enabled":                  false,
			"state_file":               "",
			"drift_alert_threshold":    0,
			"total_accounts":           0,
			"drifted_accounts":         0,
			"alerted_accounts":         0,
			"total_drift_events":       0,
			"inconsistent_accounts":    0,
			"total_consistency_issues": 0,
			"accounts":                 []any{},
			"generated_at_utc":         time.Now().UTC(),
			"sensitive_redaction":      "applied",
		})
		return
	}

	snapshot := h.authManager.EgressMappingSnapshot()
	c.JSON(http.StatusOK, gin.H{
		"available":                true,
		"enabled":                  snapshot.Enabled,
		"state_file":               snapshot.StateFile,
		"drift_alert_threshold":    snapshot.DriftAlertThreshold,
		"total_accounts":           snapshot.TotalAccounts,
		"drifted_accounts":         snapshot.DriftedAccounts,
		"alerted_accounts":         snapshot.AlertedAccounts,
		"total_drift_events":       snapshot.TotalDriftEvents,
		"inconsistent_accounts":    snapshot.InconsistentAccounts,
		"total_consistency_issues": snapshot.TotalConsistencyIssues,
		"accounts":                 snapshot.Accounts,
		"generated_at_utc":         time.Now().UTC(),
		"sensitive_redaction":      "applied",
	})
}
