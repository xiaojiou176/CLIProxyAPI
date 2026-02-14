// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// ModelVisibility defines namespace-level model visibility allowlists used by request guards.
	// Each namespace can expose only an explicit set of model IDs to clients.
	ModelVisibility ModelVisibilityConfig `yaml:"model-visibility" json:"model-visibility"`

	// ModelProviderRouting defines model family -> provider pool hard-routing constraints.
	// When enabled, each model family can only use providers explicitly listed in the allowlist.
	ModelProviderRouting ModelProviderRoutingConfig `yaml:"model-provider-routing" json:"model-provider-routing"`

	// AccountProxyConstraint defines account-level proxy hard constraints.
	// When enabled, every account entry must set its own non-empty proxy-url.
	AccountProxyConstraint AccountProxyConstraintConfig `yaml:"account-proxy-constraint" json:"account-proxy-constraint"`

	// EgressDeterminism defines account-level egress mapping persistence and drift detection settings.
	EgressDeterminism EgressDeterminismConfig `yaml:"egress-determinism" json:"egress-determinism"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}

// ModelVisibilityConfig defines model visibility guard settings.
type ModelVisibilityConfig struct {
	// Enabled toggles model visibility guard enforcement.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Namespaces maps namespace IDs to visible model IDs.
	Namespaces map[string][]string `yaml:"namespaces,omitempty" json:"namespaces,omitempty"`

	// HostNamespaces maps request host/base-url host to namespace ID.
	// It enables Base URL driven namespace routing (e.g., codex.example -> codex namespace).
	HostNamespaces map[string]string `yaml:"host-namespaces,omitempty" json:"host-namespaces,omitempty"`
}

// ModelProviderRoutingConfig defines model family -> provider allowlist guard settings.
type ModelProviderRoutingConfig struct {
	// Enabled toggles model-family provider allowlist enforcement.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// FamilyProviderAllowlist maps model-family IDs to allowed provider-pool IDs.
	FamilyProviderAllowlist map[string][]string `yaml:"family-provider-allowlist,omitempty" json:"family-provider-allowlist,omitempty"`
}

// AccountProxyConstraintConfig defines account-level proxy hard constraints.
type AccountProxyConstraintConfig struct {
	// Enabled toggles strict per-account proxy requirement.
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// EgressDeterminismConfig defines account-level egress mapping persistence and drift detection settings.
type EgressDeterminismConfig struct {
	// Enabled toggles account-level egress mapping persistence and drift detection.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// StateFile is the state file path used to persist account -> egress mapping snapshots.
	// Relative paths are resolved from process working directory.
	StateFile string `yaml:"state-file,omitempty" json:"state-file,omitempty"`

	// DriftAlertThreshold marks an account as alerting when drift-count reaches this value.
	// <= 0 is normalized to 1.
	DriftAlertThreshold int `yaml:"drift-alert-threshold,omitempty" json:"drift-alert-threshold,omitempty"`
}
