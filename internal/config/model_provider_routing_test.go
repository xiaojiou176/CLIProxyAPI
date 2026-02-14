package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeModelFamilyProviderAllowlist_NormalizesAndDeduplicates(t *testing.T) {
	got := NormalizeModelFamilyProviderAllowlist(map[string][]string{
		" Codex ":   {" codex ", "CODEX", " antigravity ", ""},
		"Gemini ":   {" gemini-cli ", "VERTEX", "vertex", " "},
		"    ":      {"codex"},
		"claude  ":  nil,
		"ignored":   {},
		"another  ": {"   "},
	})

	want := map[string][]string{
		"codex":  {"codex", "antigravity"},
		"gemini": {"gemini-cli", "vertex"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeModelFamilyProviderAllowlist() = %#v, want %#v", got, want)
	}
}

func TestNormalizeModelVisibilityHostNamespaces(t *testing.T) {
	got := NormalizeModelVisibilityHostNamespaces(map[string]string{
		" https://Codex.Local:1456/v1 ": " codex ",
		"antigravity.local:2456":        "antigravity",
		"  ":                            "ignored",
		"example.local":                 " ",
	})

	want := map[string]string{
		"codex.local":       "codex",
		"antigravity.local": "antigravity",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeModelVisibilityHostNamespaces() = %#v, want %#v", got, want)
	}
}

func TestValidateAccountProxyConstraint_EnabledRejectsMissingProxyURL(t *testing.T) {
	cfg := &Config{
		SDKConfig: SDKConfig{
			AccountProxyConstraint: AccountProxyConstraintConfig{Enabled: true},
		},
		GeminiKey: []GeminiKey{
			{APIKey: "g-1", ProxyURL: " "},
		},
		CodexKey: []CodexKey{
			{APIKey: "c-1", BaseURL: "https://api.openai.com/v1", ProxyURL: "socks5://127.0.0.1:1080"},
		},
		OpenAICompatibility: []OpenAICompatibility{
			{
				Name:    "compat-a",
				BaseURL: "https://example.com/v1",
				APIKeyEntries: []OpenAICompatibilityAPIKey{
					{APIKey: "k-1", ProxyURL: ""},
				},
			},
		},
	}

	err := cfg.ValidateAccountProxyConstraint()
	if err == nil {
		t.Fatal("ValidateAccountProxyConstraint() expected error, got nil")
	}

	errText := err.Error()
	if !strings.Contains(errText, "gemini-api-key[0]") {
		t.Fatalf("ValidateAccountProxyConstraint() error = %q, want missing gemini-api-key[0]", errText)
	}
	if !strings.Contains(errText, "openai-compatibility[0].api-key-entries[0]") {
		t.Fatalf("ValidateAccountProxyConstraint() error = %q, want missing openai-compatibility api-key entry", errText)
	}
}

func TestLoadConfigOptional_ModelProviderRoutingAndProxyConstraint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	content := `model-provider-routing:
  enabled: true
  family-provider-allowlist:
    " Codex ":
      - " codex "
      - "CODEX"
      - " antigravity "
account-proxy-constraint:
  enabled: true
gemini-api-key:
  - api-key: "g-1"
    proxy-url: " socks5://127.0.0.1:1080 "
codex-api-key:
  - api-key: "c-1"
    base-url: "https://api.openai.com/v1"
    proxy-url: " http://127.0.0.1:8080 "
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigOptional(configFile, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() unexpected error: %v", err)
	}

	wantAllowlist := map[string][]string{
		"codex": {"codex", "antigravity"},
	}
	if !reflect.DeepEqual(cfg.ModelProviderRouting.FamilyProviderAllowlist, wantAllowlist) {
		t.Fatalf("model-provider-routing.family-provider-allowlist = %#v, want %#v", cfg.ModelProviderRouting.FamilyProviderAllowlist, wantAllowlist)
	}

	if gotProxy := cfg.GeminiKey[0].ProxyURL; gotProxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("gemini-api-key[0].proxy-url = %q, want %q", gotProxy, "socks5://127.0.0.1:1080")
	}
	if gotProxy := cfg.CodexKey[0].ProxyURL; gotProxy != "http://127.0.0.1:8080" {
		t.Fatalf("codex-api-key[0].proxy-url = %q, want %q", gotProxy, "http://127.0.0.1:8080")
	}
}

func TestLoadConfigOptional_AccountProxyConstraintRejectsMissingProxy(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	content := `account-proxy-constraint:
  enabled: true
codex-api-key:
  - api-key: "c-1"
    base-url: "https://api.openai.com/v1"
    proxy-url: " "
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfigOptional(configFile, false)
	if err == nil {
		t.Fatal("LoadConfigOptional() expected account proxy constraint error, got nil")
	}
	if !strings.Contains(err.Error(), "codex-api-key[0]") {
		t.Fatalf("LoadConfigOptional() error = %q, want missing codex-api-key[0]", err.Error())
	}
}

func TestLoadConfigOptional_EgressDeterminismTrimsStateFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	content := `egress-determinism:
  enabled: true
  state-file: "  .runtime-cache/state/egress-map.json  "
  drift-alert-threshold: 2
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigOptional(configFile, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() unexpected error: %v", err)
	}
	if !cfg.EgressDeterminism.Enabled {
		t.Fatalf("egress-determinism.enabled = false, want true")
	}
	if got, want := cfg.EgressDeterminism.StateFile, ".runtime-cache/state/egress-map.json"; got != want {
		t.Fatalf("egress-determinism.state-file = %q, want %q", got, want)
	}
	if got, want := cfg.EgressDeterminism.DriftAlertThreshold, 2; got != want {
		t.Fatalf("egress-determinism.drift-alert-threshold = %d, want %d", got, want)
	}
}

func TestLoadConfigOptional_EgressDeterminismNormalizesInvalidThreshold(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	content := `egress-determinism:
  enabled: true
  state-file: ".runtime-cache/state/egress-map.json"
  drift-alert-threshold: 0
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigOptional(configFile, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() unexpected error: %v", err)
	}
	if got, want := cfg.EgressDeterminism.DriftAlertThreshold, 1; got != want {
		t.Fatalf("egress-determinism.drift-alert-threshold = %d, want %d", got, want)
	}
}
