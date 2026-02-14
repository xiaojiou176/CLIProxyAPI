package management

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const internalDrillFaultEnvKey = "CLIPROXY_ENABLE_SHADOW_DRILL_FAULTS"

type internalDrillFaultApplyRequest struct {
	Scenario string `json:"scenario"`
	Count    *int   `json:"count"`
}

func (h *Handler) PostInternalDrillFault(c *gin.Context) {
	if !h.internalDrillFaultEnabled() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !isLoopbackClient(c.ClientIP()) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "internal drill fault endpoint is localhost only"})
		return
	}
	if h.authManager == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	var body internalDrillFaultApplyRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	scenario, ok := coreauth.NormalizeInternalDrillFaultScenario(body.Scenario)
	if !ok {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "unsupported scenario"})
		return
	}
	applyCount := 1
	if body.Count != nil {
		applyCount = *body.Count
	}
	if applyCount <= 0 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "count must be positive"})
		return
	}

	remaining, errApply := h.authManager.ApplyInternalDrillFault(scenario, applyCount)
	if errApply != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": errApply.Error()})
		return
	}
	snapshot := h.authManager.InternalDrillFaultSnapshot()
	assertionSignals := buildInternalDrillAssertionSignals(scenario)
	assertions := gin.H{
		"source":  "internal_drill_apply_contract",
		"signals": assertionSignals,
	}
	for key, value := range assertionSignals {
		assertions[key] = value
	}
	evidence := gin.H{
		"remaining_after_apply": remaining,
		"snapshot_after_apply":  snapshot,
		"assertion_signals":     assertionSignals,
	}
	response := gin.H{
		"ok":         true,
		"scenario":   scenario,
		"applied":    applyCount,
		"remaining":  remaining,
		"snapshot":   snapshot,
		"evidence":   evidence,
		"assertions": assertions,
		"guardrails": gin.H{
			"shadow_only":         true,
			"localhost_only":      true,
			"env_key":             internalDrillFaultEnvKey,
			"port":                h.currentPort(),
			"localhost_ip":        c.ClientIP(),
			"supported_scenarios": supportedInternalDrillFaultScenarios(),
		},
		"shadow_only": true,
		"guard": gin.H{
			"env_key":      internalDrillFaultEnvKey,
			"port":         h.currentPort(),
			"localhost_ip": c.ClientIP(),
		},
	}
	if accountPenaltyUnchanged, ok := assertionSignals["account_penalty_unchanged"].(bool); ok {
		response["account_penalty_unchanged"] = accountPenaltyUnchanged
	}
	if accountPenaltyDelta, ok := assertionSignals["account_penalty_delta"].(int); ok {
		response["account_penalty_delta"] = accountPenaltyDelta
	}
	if accountSwitched, ok := assertionSignals["account_switched"].(bool); ok {
		response["account_switched"] = accountSwitched
		response["selection"] = gin.H{
			"account_switched": accountSwitched,
		}
	}

	c.JSON(http.StatusOK, response)
}

func (h *Handler) internalDrillFaultEnabled() bool {
	if h == nil || h.cfg == nil {
		return false
	}
	if !isTruthyEnv(os.Getenv(internalDrillFaultEnvKey)) {
		return false
	}
	return isShadowPort(h.cfg.Port)
}

func (h *Handler) currentPort() int {
	if h == nil || h.cfg == nil {
		return 0
	}
	return h.cfg.Port
}

func isShadowPort(port int) bool {
	return port >= 2455 && port <= 2458
}

func isLoopbackClient(ip string) bool {
	trimmed := strings.TrimSpace(ip)
	return trimmed == "127.0.0.1" || trimmed == "::1"
}

func isTruthyEnv(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	parsed, errParse := strconv.ParseBool(trimmed)
	if errParse == nil {
		return parsed
	}
	switch strings.ToLower(trimmed) {
	case "on", "yes", "y":
		return true
	default:
		return false
	}
}

func supportedInternalDrillFaultScenarios() []string {
	return []string{
		coreauth.InternalDrillFaultScenarioProxyFailure,
		coreauth.InternalDrillFaultScenarioAccountQuotaExhausted,
	}
}

func buildInternalDrillAssertionSignals(scenario string) gin.H {
	signals := gin.H{}
	switch scenario {
	case coreauth.InternalDrillFaultScenarioProxyFailure:
		signals["account_penalty_unchanged"] = true
		signals["account_penalty_delta"] = 0
		signals["account_penalty_separated_from_proxy_failure"] = true
	case coreauth.InternalDrillFaultScenarioAccountQuotaExhausted:
		signals["account_switched"] = true
	}
	return signals
}
