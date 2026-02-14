package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestManagerObserveEgress_DisabledSkipsPersistence(t *testing.T) {
	t.Parallel()

	stateFile := filepath.Join(t.TempDir(), "state", "egress.json")
	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:   false,
				StateFile: stateFile,
			},
		},
	})

	auth := &Auth{ID: "auth-1", Provider: "gemini", ProxyURL: "socks5://127.0.0.1:1080"}
	mgr.observeEgress(auth, "gemini", "gemini-2.5-pro")

	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("state file should not exist when disabled, stat err=%v", err)
	}
}

func TestManagerObserveEgress_PersistsAndRecoversAcrossManagerRestart(t *testing.T) {
	t.Parallel()

	stateFile := filepath.Join(t.TempDir(), "state", "egress.json")

	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:   true,
				StateFile: stateFile,
			},
		},
	})
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		ProxyURL: "socks5://user:pass@127.0.0.1:1080",
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register(auth): %v", err)
	}
	mgr.observeEgress(auth, "gemini", "gemini-2.5-pro")

	state, err := loadEgressMappingState(stateFile)
	if err != nil {
		t.Fatalf("loadEgressMappingState() error: %v", err)
	}
	entry, ok := state["auth-1"]
	if !ok {
		t.Fatal("missing auth-1 egress mapping")
	}
	if entry.ProxyIdentity != "socks5://127.0.0.1:1080" {
		t.Fatalf("ProxyIdentity = %q, want %q", entry.ProxyIdentity, "socks5://127.0.0.1:1080")
	}
	if entry.DriftCount != 0 {
		t.Fatalf("DriftCount = %d, want 0", entry.DriftCount)
	}

	mgr2 := NewManager(nil, deterministicSelector{}, nil)
	mgr2.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:   true,
				StateFile: stateFile,
			},
		},
	})

	mgr2.egressMu.Lock()
	recovered, recoveredOK := mgr2.egressMappings["auth-1"]
	mgr2.egressMu.Unlock()
	if !recoveredOK {
		t.Fatal("expected recovered mapping for auth-1")
	}
	if recovered.ProxyDigest != entry.ProxyDigest {
		t.Fatalf("recovered ProxyDigest = %q, want %q", recovered.ProxyDigest, entry.ProxyDigest)
	}

	mgr2.observeEgress(auth, "gemini", "gemini-2.5-pro")
	stateAfter, err := loadEgressMappingState(stateFile)
	if err != nil {
		t.Fatalf("loadEgressMappingState() after observe error: %v", err)
	}
	if got := stateAfter["auth-1"].DriftCount; got != 0 {
		t.Fatalf("DriftCount after same proxy observe = %d, want 0", got)
	}
}

func TestManagerObserveEgress_DetectsDriftAndPersistsNewMapping(t *testing.T) {
	t.Parallel()

	stateFile := filepath.Join(t.TempDir(), "state", "egress.json")
	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:   true,
				StateFile: stateFile,
			},
		},
	})

	auth := &Auth{
		ID:       "auth-1",
		Provider: "codex",
		ProxyURL: "socks5://127.0.0.1:1080",
	}
	mgr.observeEgress(auth, "codex", "gpt-5.3-codex")

	auth.ProxyURL = "http://127.0.0.1:8080"
	mgr.observeEgress(auth, "codex", "gpt-5.3-codex")

	state, err := loadEgressMappingState(stateFile)
	if err != nil {
		t.Fatalf("loadEgressMappingState() error: %v", err)
	}
	entry, ok := state["auth-1"]
	if !ok {
		t.Fatal("missing auth-1 egress mapping")
	}
	if entry.DriftCount != 1 {
		t.Fatalf("DriftCount = %d, want 1", entry.DriftCount)
	}
	if entry.LastDriftAt.IsZero() {
		t.Fatal("LastDriftAt is zero, want non-zero")
	}
	if entry.ProxyIdentity != "http://127.0.0.1:8080" {
		t.Fatalf("ProxyIdentity = %q, want %q", entry.ProxyIdentity, "http://127.0.0.1:8080")
	}
	if entry.LastProvider != "codex" {
		t.Fatalf("LastProvider = %q, want %q", entry.LastProvider, "codex")
	}
}

func TestManagerEgressMappingSnapshot_RedactsProxyIdentity(t *testing.T) {
	t.Parallel()

	stateFile := filepath.Join(t.TempDir(), "state", "egress.json")
	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:   true,
				StateFile: stateFile,
			},
		},
	})

	auth := &Auth{
		ID:       "auth-1",
		Provider: "codex",
		ProxyURL: "socks5://user:pass@127.0.0.1:1080",
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register(auth): %v", err)
	}
	mgr.observeEgress(auth, "codex", "gpt-5.3-codex")

	snapshot := mgr.EgressMappingSnapshot()
	if !snapshot.Enabled {
		t.Fatal("snapshot.Enabled = false, want true")
	}
	if snapshot.TotalAccounts != 1 {
		t.Fatalf("snapshot.TotalAccounts = %d, want 1", snapshot.TotalAccounts)
	}
	if snapshot.DriftAlertThreshold != 1 {
		t.Fatalf("snapshot.DriftAlertThreshold = %d, want 1", snapshot.DriftAlertThreshold)
	}
	if len(snapshot.Accounts) != 1 {
		t.Fatalf("len(snapshot.Accounts) = %d, want 1", len(snapshot.Accounts))
	}

	record := snapshot.Accounts[0]
	if record.ProxyIdentity == "" {
		t.Fatal("record.ProxyIdentity empty, want redacted token")
	}
	if !strings.HasPrefix(record.ProxyIdentity, "proxy#") {
		t.Fatalf("record.ProxyIdentity = %q, want prefix proxy#", record.ProxyIdentity)
	}
	for _, sensitive := range []string{"socks5://", "127.0.0.1", "user:pass", "@"} {
		if strings.Contains(record.ProxyIdentity, sensitive) {
			t.Fatalf("record.ProxyIdentity leaked sensitive value %q: %q", sensitive, record.ProxyIdentity)
		}
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("Marshal(snapshot): %v", err)
	}
	payload := string(raw)
	if strings.Contains(payload, "proxy_digest") {
		t.Fatalf("snapshot payload leaked proxy_digest: %s", payload)
	}
	for _, sensitive := range []string{"socks5://", "127.0.0.1", "user:pass", "proxy_url"} {
		if strings.Contains(payload, sensitive) {
			t.Fatalf("snapshot payload leaked sensitive value %q: %s", sensitive, payload)
		}
	}
}

func TestManagerEgressMappingSnapshot_ExposesAlertAndConsistencyMetrics(t *testing.T) {
	t.Parallel()

	stateFile := filepath.Join(t.TempDir(), "state", "egress.json")
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err != nil {
		t.Fatalf("MkdirAll(state dir): %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	statePayload := map[string]any{
		"version":    1,
		"updated_at": now,
		"accounts": map[string]any{
			"auth-1": map[string]any{
				"proxy_identity": "socks5://127.0.0.1:1080",
				"proxy_digest":   "digest-1",
				"first_seen_at":  now.Add(-5 * time.Minute),
				"last_seen_at":   now,
				"drift_count":    2,
				"last_provider":  "codex",
				"last_model":     "gpt-5.3-codex",
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

	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:             true,
				StateFile:           stateFile,
				DriftAlertThreshold: 2,
			},
		},
	})

	snapshot := mgr.EgressMappingSnapshot()
	if snapshot.TotalAccounts != 1 {
		t.Fatalf("snapshot.TotalAccounts = %d, want 1", snapshot.TotalAccounts)
	}
	if snapshot.DriftAlertThreshold != 2 {
		t.Fatalf("snapshot.DriftAlertThreshold = %d, want 2", snapshot.DriftAlertThreshold)
	}
	if snapshot.AlertedAccounts != 1 {
		t.Fatalf("snapshot.AlertedAccounts = %d, want 1", snapshot.AlertedAccounts)
	}
	if snapshot.InconsistentAccounts != 1 {
		t.Fatalf("snapshot.InconsistentAccounts = %d, want 1", snapshot.InconsistentAccounts)
	}
	if snapshot.TotalConsistencyIssues != 1 {
		t.Fatalf("snapshot.TotalConsistencyIssues = %d, want 1", snapshot.TotalConsistencyIssues)
	}
	if len(snapshot.Accounts) != 1 {
		t.Fatalf("len(snapshot.Accounts) = %d, want 1", len(snapshot.Accounts))
	}
	record := snapshot.Accounts[0]
	if !record.DriftAlerted {
		t.Fatalf("record.DriftAlerted = false, want true")
	}
	if record.ConsistencyStatus != "inconsistent" {
		t.Fatalf("record.ConsistencyStatus = %q, want %q", record.ConsistencyStatus, "inconsistent")
	}
	if len(record.ConsistencyIssues) != 1 || record.ConsistencyIssues[0] != "drift_without_timestamp" {
		t.Fatalf("record.ConsistencyIssues = %#v, want [drift_without_timestamp]", record.ConsistencyIssues)
	}
}
