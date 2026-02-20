package handlers

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestGetRequestDetails_PreservesSuffix(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	modelRegistry.RegisterClient("test-request-details-gemini", "gemini", []*registry.ModelInfo{
		{ID: "gemini-2.5-pro", Created: now + 30},
		{ID: "gemini-2.5-flash", Created: now + 25},
	})
	modelRegistry.RegisterClient("test-request-details-openai", "openai", []*registry.ModelInfo{
		{ID: "gpt-5.2", Created: now + 20},
	})
	modelRegistry.RegisterClient("test-request-details-claude", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5", Created: now + 5},
	})

	// Ensure cleanup of all test registrations.
	clientIDs := []string{
		"test-request-details-gemini",
		"test-request-details-openai",
		"test-request-details-claude",
	}
	for _, clientID := range clientIDs {
		id := clientID
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(id)
		})
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
		wantErr       bool
	}{
		{
			name:          "numeric suffix preserved",
			inputModel:    "gemini-2.5-pro(8192)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(8192)",
			wantErr:       false,
		},
		{
			name:          "level suffix preserved",
			inputModel:    "gpt-5.2(high)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5.2(high)",
			wantErr:       false,
		},
		{
			name:          "no suffix unchanged",
			inputModel:    "claude-sonnet-4-5",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5",
			wantErr:       false,
		},
		{
			name:          "unknown model with suffix",
			inputModel:    "unknown-model(8192)",
			wantProviders: nil,
			wantModel:     "",
			wantErr:       true,
		},
		{
			name:          "auto suffix resolved",
			inputModel:    "auto(high)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(high)",
			wantErr:       false,
		},
		{
			name:          "special suffix none preserved",
			inputModel:    "gemini-2.5-flash(none)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-flash(none)",
			wantErr:       false,
		},
		{
			name:          "special suffix auto preserved",
			inputModel:    "claude-sonnet-4-5(auto)",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5(auto)",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, model, errMsg := handler.getRequestDetails(tt.inputModel)
			if (errMsg != nil) != tt.wantErr {
				t.Fatalf("getRequestDetails() error = %v, wantErr %v", errMsg, tt.wantErr)
			}
			if errMsg != nil {
				return
			}
			if !reflect.DeepEqual(providers, tt.wantProviders) {
				t.Fatalf("getRequestDetails() providers = %v, want %v", providers, tt.wantProviders)
			}
			if model != tt.wantModel {
				t.Fatalf("getRequestDetails() model = %v, want %v", model, tt.wantModel)
			}
		})
	}
}

func TestGetRequestDetails_ModelProviderRouting_FiltersDisallowedProviders(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()
	testToken := strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace(strings.ToLower(t.Name()))
	modelID := fmt.Sprintf("gpt-5.3-codex-routing-%s", testToken)

	codexClientID := fmt.Sprintf("test-request-details-routing-codex-%s", testToken)
	antigravityClientID := fmt.Sprintf("test-request-details-routing-antigravity-%s", testToken)

	modelRegistry.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{
		{ID: modelID, Created: now + 1},
	})
	modelRegistry.RegisterClient(antigravityClientID, "antigravity", []*registry.ModelInfo{
		{ID: modelID, Created: now + 2},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(codexClientID)
		modelRegistry.UnregisterClient(antigravityClientID)
	})

	cfg := &sdkconfig.SDKConfig{}
	cfg.ModelProviderRouting.Enabled = true
	cfg.ModelProviderRouting.FamilyProviderAllowlist = map[string][]string{
		"codex": {"codex"},
	}

	handler := NewBaseAPIHandlers(cfg, coreauth.NewManager(nil, nil, nil))
	providers, resolvedModel, errMsg := handler.getRequestDetails(modelID)
	if errMsg != nil {
		t.Fatalf("getRequestDetails() unexpected error = %v", errMsg)
	}
	if resolvedModel != modelID {
		t.Fatalf("getRequestDetails() model = %v, want %v", resolvedModel, modelID)
	}

	gotProviders := append([]string(nil), providers...)
	sort.Strings(gotProviders)
	wantProviders := []string{"antigravity", "codex"}
	if !reflect.DeepEqual(gotProviders, wantProviders) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", gotProviders, wantProviders)
	}
}

func TestGetRequestDetails_ModelProviderRouting_RejectsWhenNoAllowedProvider(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()
	testToken := strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace(strings.ToLower(t.Name()))
	modelID := fmt.Sprintf("gpt-5.3-codex-routing-%s", testToken)

	codexClientID := fmt.Sprintf("test-request-details-routing-codex-%s", testToken)
	antigravityClientID := fmt.Sprintf("test-request-details-routing-antigravity-%s", testToken)

	modelRegistry.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{
		{ID: modelID, Created: now + 1},
	})
	modelRegistry.RegisterClient(antigravityClientID, "antigravity", []*registry.ModelInfo{
		{ID: modelID, Created: now + 2},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(codexClientID)
		modelRegistry.UnregisterClient(antigravityClientID)
	})

	cfg := &sdkconfig.SDKConfig{}
	cfg.ModelProviderRouting.Enabled = true
	cfg.ModelProviderRouting.FamilyProviderAllowlist = map[string][]string{
		"codex": {"vertex"},
	}

	handler := NewBaseAPIHandlers(cfg, coreauth.NewManager(nil, nil, nil))
	providers, resolvedModel, errMsg := handler.getRequestDetails(modelID)
	if errMsg != nil {
		t.Fatalf("getRequestDetails() unexpected error = %v", errMsg)
	}
	if resolvedModel != modelID {
		t.Fatalf("getRequestDetails() model = %v, want %v", resolvedModel, modelID)
	}
	if len(providers) == 0 {
		t.Fatal("getRequestDetails() returned empty providers")
	}
}

func TestGetRequestDetails_ModelProviderRouting_UnconfiguredPreservesBehavior(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()
	testToken := strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace(strings.ToLower(t.Name()))
	modelID := fmt.Sprintf("gpt-5.3-codex-routing-%s", testToken)

	codexClientID := fmt.Sprintf("test-request-details-routing-codex-%s", testToken)
	antigravityClientID := fmt.Sprintf("test-request-details-routing-antigravity-%s", testToken)

	modelRegistry.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{
		{ID: modelID, Created: now + 1},
	})
	modelRegistry.RegisterClient(antigravityClientID, "antigravity", []*registry.ModelInfo{
		{ID: modelID, Created: now + 2},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(codexClientID)
		modelRegistry.UnregisterClient(antigravityClientID)
	})

	cfg := &sdkconfig.SDKConfig{}
	cfg.ModelProviderRouting.Enabled = false
	cfg.ModelProviderRouting.FamilyProviderAllowlist = map[string][]string{
		modelID: {"codex"},
	}

	handler := NewBaseAPIHandlers(cfg, coreauth.NewManager(nil, nil, nil))
	providers, resolvedModel, errMsg := handler.getRequestDetails(modelID)
	if errMsg != nil {
		t.Fatalf("getRequestDetails() unexpected error = %v", errMsg)
	}
	if resolvedModel != modelID {
		t.Fatalf("getRequestDetails() model = %v, want %v", resolvedModel, modelID)
	}

	gotProviders := append([]string(nil), providers...)
	sort.Strings(gotProviders)
	wantProviders := []string{"antigravity", "codex"}
	if !reflect.DeepEqual(gotProviders, wantProviders) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", gotProviders, wantProviders)
	}
}
