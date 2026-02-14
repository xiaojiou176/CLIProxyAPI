package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestGetEgressMapping_ReturnsAggregatedSnapshot(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state", "egress-mapping.json")
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err != nil {
		t.Fatalf("MkdirAll(state dir): %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	statePayload := map[string]any{
		"version":    1,
		"updated_at": now,
		"accounts": map[string]any{
			"auth-1": map[string]any{
				"proxy_identity": "socks5://user:pass@127.0.0.1:1080",
				"proxy_digest":   "digest-1",
				"first_seen_at":  now.Add(-5 * time.Minute),
				"last_seen_at":   now.Add(-1 * time.Minute),
				"drift_count":    1,
				"last_drift_at":  now.Add(-2 * time.Minute),
				"last_provider":  "codex",
				"last_model":     "gpt-5.3-codex",
			},
			"auth-2": map[string]any{
				"proxy_identity": "http://127.0.0.1:8080",
				"proxy_digest":   "digest-2",
				"first_seen_at":  now.Add(-10 * time.Minute),
				"last_seen_at":   now,
				"drift_count":    2,
				"last_provider":  "gemini",
				"last_model":     "gemini-2.5-pro",
			},
		},
	}
	raw, err := json.Marshal(statePayload)
	if err != nil {
		t.Fatalf("Marshal(state payload): %v", err)
	}
	if errWrite := os.WriteFile(stateFile, raw, 0o600); errWrite != nil {
		t.Fatalf("WriteFile(state): %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:             true,
				StateFile:           stateFile,
				DriftAlertThreshold: 2,
			},
		},
	})
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "auth-1", FileName: "auth-1.json", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(auth-1): %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "auth-2", FileName: "auth-2.json", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-2): %v", errRegister)
	}

	handler := &Handler{authManager: manager}
	router := gin.New()
	router.GET("/v0/management/egress-mapping", handler.GetEgressMapping)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/egress-mapping", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "proxy_digest") {
		t.Fatalf("response must not expose proxy_digest, body=%s", resp.Body.String())
	}
	for _, sensitive := range []string{"socks5://", "http://", "127.0.0.1", "user:pass", "@"} {
		if strings.Contains(resp.Body.String(), sensitive) {
			t.Fatalf("response must not expose sensitive proxy details %q, body=%s", sensitive, resp.Body.String())
		}
	}

	var body struct {
		Available              bool `json:"available"`
		Enabled                bool `json:"enabled"`
		DriftAlertThreshold    int  `json:"drift_alert_threshold"`
		TotalAccounts          int  `json:"total_accounts"`
		DriftedAccounts        int  `json:"drifted_accounts"`
		AlertedAccounts        int  `json:"alerted_accounts"`
		TotalDriftEvents       int  `json:"total_drift_events"`
		InconsistentAccounts   int  `json:"inconsistent_accounts"`
		TotalConsistencyIssues int  `json:"total_consistency_issues"`
		Accounts               []struct {
			AuthID            string   `json:"auth_id"`
			AuthIndex         string   `json:"auth_index"`
			Provider          string   `json:"provider"`
			ProxyIdentity     string   `json:"proxy_identity"`
			DriftCount        int      `json:"drift_count"`
			DriftAlerted      bool     `json:"drift_alerted"`
			ConsistencyStatus string   `json:"consistency_status"`
			ConsistencyIssues []string `json:"consistency_issues"`
		} `json:"accounts"`
	}
	if errUnmarshal := json.Unmarshal(resp.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("Unmarshal(response): %v", errUnmarshal)
	}

	if !body.Available {
		t.Fatalf("available = false, want true")
	}
	if !body.Enabled {
		t.Fatalf("enabled = false, want true")
	}
	if body.TotalAccounts != 2 {
		t.Fatalf("total_accounts = %d, want 2", body.TotalAccounts)
	}
	if body.DriftAlertThreshold != 2 {
		t.Fatalf("drift_alert_threshold = %d, want 2", body.DriftAlertThreshold)
	}
	if body.DriftedAccounts != 2 {
		t.Fatalf("drifted_accounts = %d, want 2", body.DriftedAccounts)
	}
	if body.AlertedAccounts != 1 {
		t.Fatalf("alerted_accounts = %d, want 1", body.AlertedAccounts)
	}
	if body.TotalDriftEvents != 3 {
		t.Fatalf("total_drift_events = %d, want 3", body.TotalDriftEvents)
	}
	if body.InconsistentAccounts != 1 {
		t.Fatalf("inconsistent_accounts = %d, want 1", body.InconsistentAccounts)
	}
	if body.TotalConsistencyIssues != 1 {
		t.Fatalf("total_consistency_issues = %d, want 1", body.TotalConsistencyIssues)
	}
	if len(body.Accounts) != 2 {
		t.Fatalf("len(accounts) = %d, want 2", len(body.Accounts))
	}
	if body.Accounts[0].AuthID != "auth-1" || body.Accounts[0].DriftCount != 1 {
		t.Fatalf("accounts[0] mismatch: %+v", body.Accounts[0])
	}
	if body.Accounts[0].AuthIndex == "" {
		t.Fatalf("accounts[0].auth_index empty, want non-empty")
	}
	if body.Accounts[0].DriftAlerted {
		t.Fatalf("accounts[0].drift_alerted = true, want false")
	}
	if body.Accounts[0].ConsistencyStatus != "ok" {
		t.Fatalf("accounts[0].consistency_status = %q, want %q", body.Accounts[0].ConsistencyStatus, "ok")
	}
	if body.Accounts[1].AuthID != "auth-2" {
		t.Fatalf("accounts[1].auth_id = %q, want %q", body.Accounts[1].AuthID, "auth-2")
	}
	if !body.Accounts[1].DriftAlerted {
		t.Fatalf("accounts[1].drift_alerted = false, want true")
	}
	if body.Accounts[1].ConsistencyStatus != "inconsistent" {
		t.Fatalf("accounts[1].consistency_status = %q, want %q", body.Accounts[1].ConsistencyStatus, "inconsistent")
	}
	if len(body.Accounts[1].ConsistencyIssues) != 1 || body.Accounts[1].ConsistencyIssues[0] != "drift_without_timestamp" {
		t.Fatalf("accounts[1].consistency_issues = %#v, want [drift_without_timestamp]", body.Accounts[1].ConsistencyIssues)
	}
	for index, account := range body.Accounts {
		if !strings.HasPrefix(account.ProxyIdentity, "proxy#") {
			t.Fatalf("accounts[%d].proxy_identity = %q, want redacted proxy token", index, account.ProxyIdentity)
		}
	}
}

func TestGetEgressMapping_WithoutAuthManagerReturnsUnavailable(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	handler := &Handler{}
	router := gin.New()
	router.GET("/v0/management/egress-mapping", handler.GetEgressMapping)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/egress-mapping", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	var body struct {
		Available            bool              `json:"available"`
		Enabled              bool              `json:"enabled"`
		DriftAlertThreshold  int               `json:"drift_alert_threshold"`
		TotalAccounts        int               `json:"total_accounts"`
		AlertedAccounts      int               `json:"alerted_accounts"`
		InconsistentAccounts int               `json:"inconsistent_accounts"`
		SensitiveRedaction   string            `json:"sensitive_redaction"`
		Accounts             []json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal(response): %v", err)
	}
	if body.Available {
		t.Fatalf("available = true, want false")
	}
	if body.Enabled {
		t.Fatalf("enabled = true, want false")
	}
	if body.TotalAccounts != 0 {
		t.Fatalf("total_accounts = %d, want 0", body.TotalAccounts)
	}
	if body.DriftAlertThreshold != 0 {
		t.Fatalf("drift_alert_threshold = %d, want 0", body.DriftAlertThreshold)
	}
	if body.AlertedAccounts != 0 {
		t.Fatalf("alerted_accounts = %d, want 0", body.AlertedAccounts)
	}
	if body.InconsistentAccounts != 0 {
		t.Fatalf("inconsistent_accounts = %d, want 0", body.InconsistentAccounts)
	}
	if body.SensitiveRedaction != "applied" {
		t.Fatalf("sensitive_redaction = %q, want %q", body.SensitiveRedaction, "applied")
	}
	if len(body.Accounts) != 0 {
		t.Fatalf("len(accounts) = %d, want 0", len(body.Accounts))
	}
}
