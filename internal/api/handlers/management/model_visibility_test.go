package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func newModelVisibilityHarness(t *testing.T, cfg *internalconfig.Config) (*Handler, *gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 2456\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config): %v", err)
	}

	handler := NewHandler(cfg, configPath, nil)
	router := gin.New()
	mgmt := router.Group("/v0/management")
	{
		mgmt.GET("/model-visibility", handler.GetModelVisibility)
		mgmt.PUT("/model-visibility", handler.PutModelVisibility)
		mgmt.PATCH("/model-visibility", handler.PatchModelVisibility)
	}
	return handler, router, configPath
}

func TestModelVisibilityManagement_PutNormalizesAndPersists(t *testing.T) {
	handler, router, configPath := newModelVisibilityHarness(t, nil)

	body := `{
		"enabled": true,
		"namespaces": {
			" default ": [" gpt-5.3-codex ", "gpt-5.3-codex", ""],
			"team-b": ["gemini-3.0-pro", " GEMINI-3.0-PRO "]
		},
		"host-namespaces": {
			" https://Codex.Local:1456/v1 ": " default ",
			"antigravity.local:2456": " team-b ",
			"": "ignored"
		}
	}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/model-visibility", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	wantNamespaces := map[string][]string{
		"default": {"gpt-5.3-codex"},
		"team-b":  {"gemini-3.0-pro"},
	}
	if !reflect.DeepEqual(handler.cfg.ModelVisibility.Namespaces, wantNamespaces) {
		t.Fatalf("in-memory namespaces = %#v, want %#v", handler.cfg.ModelVisibility.Namespaces, wantNamespaces)
	}

	wantHostNamespaces := map[string]string{
		"codex.local":       "default",
		"antigravity.local": "team-b",
	}
	if !reflect.DeepEqual(handler.cfg.ModelVisibility.HostNamespaces, wantHostNamespaces) {
		t.Fatalf("in-memory host-namespaces = %#v, want %#v", handler.cfg.ModelVisibility.HostNamespaces, wantHostNamespaces)
	}

	loaded, err := internalconfig.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(configPath): %v", err)
	}
	if !loaded.ModelVisibility.Enabled {
		t.Fatalf("persisted model-visibility.enabled = false, want true")
	}
	if !reflect.DeepEqual(loaded.ModelVisibility.Namespaces, wantNamespaces) {
		t.Fatalf("persisted namespaces = %#v, want %#v", loaded.ModelVisibility.Namespaces, wantNamespaces)
	}
	if !reflect.DeepEqual(loaded.ModelVisibility.HostNamespaces, wantHostNamespaces) {
		t.Fatalf("persisted host-namespaces = %#v, want %#v", loaded.ModelVisibility.HostNamespaces, wantHostNamespaces)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v0/management/model-visibility", nil)
	getResp := httptest.NewRecorder()
	router.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", getResp.Code, http.StatusOK, getResp.Body.String())
	}

	var payload struct {
		ModelVisibility internalconfig.ModelVisibilityConfig `json:"model-visibility"`
	}
	if err := json.Unmarshal(getResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal(GET body): %v", err)
	}
	if !payload.ModelVisibility.Enabled {
		t.Fatalf("GET model-visibility.enabled = false, want true")
	}
	if !reflect.DeepEqual(payload.ModelVisibility.Namespaces, wantNamespaces) {
		t.Fatalf("GET namespaces = %#v, want %#v", payload.ModelVisibility.Namespaces, wantNamespaces)
	}
	if !reflect.DeepEqual(payload.ModelVisibility.HostNamespaces, wantHostNamespaces) {
		t.Fatalf("GET host-namespaces = %#v, want %#v", payload.ModelVisibility.HostNamespaces, wantHostNamespaces)
	}
}

func TestModelVisibilityManagement_PatchReplacesNamespacesAndHostMappings(t *testing.T) {
	cfg := &internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			ModelVisibility: internalconfig.ModelVisibilityConfig{
				Enabled: true,
				Namespaces: map[string][]string{
					"default": {"gpt-5.3-codex"},
				},
				HostNamespaces: map[string]string{
					"codex.local": "default",
				},
			},
		},
	}

	handler, router, configPath := newModelVisibilityHarness(t, cfg)

	body := `{
		"enabled": true,
		"namespaces": {
			"antigravity": ["gemini-3.0-pro", "gemini-3.0-pro"]
		},
		"host-namespaces": {
			"https://antigravity.local:2456/v1": " antigravity "
		}
	}`
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/model-visibility", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	wantNamespaces := map[string][]string{
		"antigravity": {"gemini-3.0-pro"},
	}
	wantHostNamespaces := map[string]string{
		"antigravity.local": "antigravity",
	}
	if !reflect.DeepEqual(handler.cfg.ModelVisibility.Namespaces, wantNamespaces) {
		t.Fatalf("patched namespaces = %#v, want %#v", handler.cfg.ModelVisibility.Namespaces, wantNamespaces)
	}
	if !reflect.DeepEqual(handler.cfg.ModelVisibility.HostNamespaces, wantHostNamespaces) {
		t.Fatalf("patched host-namespaces = %#v, want %#v", handler.cfg.ModelVisibility.HostNamespaces, wantHostNamespaces)
	}

	loaded, err := internalconfig.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(configPath): %v", err)
	}
	if !reflect.DeepEqual(loaded.ModelVisibility.Namespaces, wantNamespaces) {
		t.Fatalf("persisted namespaces = %#v, want %#v", loaded.ModelVisibility.Namespaces, wantNamespaces)
	}
	if !reflect.DeepEqual(loaded.ModelVisibility.HostNamespaces, wantHostNamespaces) {
		t.Fatalf("persisted host-namespaces = %#v, want %#v", loaded.ModelVisibility.HostNamespaces, wantHostNamespaces)
	}
}
