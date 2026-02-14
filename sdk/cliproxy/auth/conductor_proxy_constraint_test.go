package auth

import (
	"context"
	"net/http"
	"path/filepath"
	"sort"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type deterministicSelector struct{}

func (deterministicSelector) Pick(_ context.Context, _ string, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, nil
	}
	candidates := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if auth != nil {
			candidates = append(candidates, auth)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0], nil
}

type noopProviderExecutor struct {
	identifier string
}

func (e noopProviderExecutor) Identifier() string { return e.identifier }

func (noopProviderExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (noopProviderExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return ch, nil
}

func (noopProviderExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (noopProviderExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (noopProviderExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerPickNext_AccountProxyConstraintEnabledSkipsMissingProxy(t *testing.T) {
	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			AccountProxyConstraint: internalconfig.AccountProxyConstraintConfig{Enabled: true},
		},
	})
	mgr.RegisterExecutor(noopProviderExecutor{identifier: "gemini"})

	if _, err := mgr.Register(context.Background(), &Auth{ID: "a-no-proxy", Provider: "gemini"}); err != nil {
		t.Fatalf("Register(a-no-proxy): %v", err)
	}
	if _, err := mgr.Register(context.Background(), &Auth{ID: "b-with-proxy", Provider: "gemini", ProxyURL: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatalf("Register(b-with-proxy): %v", err)
	}

	auth, _, err := mgr.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if err != nil {
		t.Fatalf("pickNext() returned error: %v", err)
	}
	if auth == nil {
		t.Fatal("pickNext() returned nil auth")
	}
	if auth.ID != "b-with-proxy" {
		t.Fatalf("pickNext() auth.ID = %q, want %q", auth.ID, "b-with-proxy")
	}
}

func TestManagerPickNext_AccountProxyConstraintDisabledAllowsMissingProxy(t *testing.T) {
	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			AccountProxyConstraint: internalconfig.AccountProxyConstraintConfig{Enabled: false},
		},
	})
	mgr.RegisterExecutor(noopProviderExecutor{identifier: "gemini"})

	if _, err := mgr.Register(context.Background(), &Auth{ID: "a-no-proxy", Provider: "gemini"}); err != nil {
		t.Fatalf("Register(a-no-proxy): %v", err)
	}
	if _, err := mgr.Register(context.Background(), &Auth{ID: "b-with-proxy", Provider: "gemini", ProxyURL: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatalf("Register(b-with-proxy): %v", err)
	}

	auth, _, err := mgr.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if err != nil {
		t.Fatalf("pickNext() returned error: %v", err)
	}
	if auth == nil {
		t.Fatal("pickNext() returned nil auth")
	}
	if auth.ID != "a-no-proxy" {
		t.Fatalf("pickNext() auth.ID = %q, want %q", auth.ID, "a-no-proxy")
	}
}

func TestManagerPickNextMixed_AccountProxyConstraintEnabledRejectsMissingProxyCandidates(t *testing.T) {
	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			AccountProxyConstraint: internalconfig.AccountProxyConstraintConfig{Enabled: true},
		},
	})
	mgr.RegisterExecutor(noopProviderExecutor{identifier: "gemini"})
	mgr.RegisterExecutor(noopProviderExecutor{identifier: "codex"})

	if _, err := mgr.Register(context.Background(), &Auth{ID: "a-no-proxy", Provider: "gemini"}); err != nil {
		t.Fatalf("Register(a-no-proxy): %v", err)
	}
	if _, err := mgr.Register(context.Background(), &Auth{ID: "b-no-proxy", Provider: "codex"}); err != nil {
		t.Fatalf("Register(b-no-proxy): %v", err)
	}

	auth, _, _, err := mgr.pickNextMixed(context.Background(), []string{"gemini", "codex"}, "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if err == nil {
		t.Fatal("pickNextMixed() expected error, got nil")
	}
	if auth != nil {
		t.Fatalf("pickNextMixed() auth = %v, want nil", auth)
	}
	authErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("pickNextMixed() err type = %T, want *Error", err)
	}
	if authErr.Code != "auth_not_found" {
		t.Fatalf("pickNextMixed() err.Code = %q, want %q", authErr.Code, "auth_not_found")
	}
}

func TestManagerExecuteCount_AccountProxyConstraintWithEgressDeterminism_BindsEligibleAccount(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state", "egress.json")

	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			AccountProxyConstraint: internalconfig.AccountProxyConstraintConfig{Enabled: true},
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:   true,
				StateFile: stateFile,
			},
		},
	})
	mgr.RegisterExecutor(noopProviderExecutor{identifier: "gemini"})

	if _, err := mgr.Register(context.Background(), &Auth{ID: "a-no-proxy", Provider: "gemini"}); err != nil {
		t.Fatalf("Register(a-no-proxy): %v", err)
	}
	if _, err := mgr.Register(context.Background(), &Auth{ID: "b-with-proxy", Provider: "gemini", ProxyURL: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatalf("Register(b-with-proxy): %v", err)
	}

	if _, err := mgr.ExecuteCount(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("ExecuteCount(): %v", err)
	}

	state, err := loadEgressMappingState(stateFile)
	if err != nil {
		t.Fatalf("loadEgressMappingState(): %v", err)
	}
	if len(state) != 1 {
		t.Fatalf("len(state) = %d, want 1", len(state))
	}
	if _, exists := state["a-no-proxy"]; exists {
		t.Fatalf("state contains a-no-proxy mapping, want skipped by account proxy constraint")
	}
	entry, exists := state["b-with-proxy"]
	if !exists {
		t.Fatal("state missing b-with-proxy mapping")
	}
	if entry.ProxyIdentity != "socks5://127.0.0.1:1080" {
		t.Fatalf("entry.ProxyIdentity = %q, want %q", entry.ProxyIdentity, "socks5://127.0.0.1:1080")
	}
	if entry.DriftCount != 0 {
		t.Fatalf("entry.DriftCount = %d, want 0", entry.DriftCount)
	}

	snapshot := mgr.EgressMappingSnapshot()
	if snapshot.TotalAccounts != 1 {
		t.Fatalf("snapshot.TotalAccounts = %d, want 1", snapshot.TotalAccounts)
	}
	if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].AuthID != "b-with-proxy" {
		t.Fatalf("snapshot.Accounts = %+v, want only b-with-proxy", snapshot.Accounts)
	}
}

func TestManagerExecuteCount_AccountProxyConstraintWithEgressDeterminism_RestartRecoversMapping(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state", "egress.json")

	mgr := NewManager(nil, deterministicSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			AccountProxyConstraint: internalconfig.AccountProxyConstraintConfig{Enabled: true},
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:   true,
				StateFile: stateFile,
			},
		},
	})
	mgr.RegisterExecutor(noopProviderExecutor{identifier: "gemini"})
	if _, err := mgr.Register(context.Background(), &Auth{ID: "a-no-proxy", Provider: "gemini"}); err != nil {
		t.Fatalf("Register(a-no-proxy): %v", err)
	}
	if _, err := mgr.Register(context.Background(), &Auth{ID: "b-with-proxy", Provider: "gemini", ProxyURL: "http://127.0.0.1:8080"}); err != nil {
		t.Fatalf("Register(b-with-proxy): %v", err)
	}
	if _, err := mgr.ExecuteCount(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("ExecuteCount() warmup: %v", err)
	}

	mgr2 := NewManager(nil, deterministicSelector{}, nil)
	mgr2.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			AccountProxyConstraint: internalconfig.AccountProxyConstraintConfig{Enabled: true},
			EgressDeterminism: internalconfig.EgressDeterminismConfig{
				Enabled:   true,
				StateFile: stateFile,
			},
		},
	})
	mgr2.RegisterExecutor(noopProviderExecutor{identifier: "gemini"})
	if _, err := mgr2.Register(context.Background(), &Auth{ID: "a-no-proxy", Provider: "gemini"}); err != nil {
		t.Fatalf("Register(a-no-proxy) on mgr2: %v", err)
	}
	if _, err := mgr2.Register(context.Background(), &Auth{ID: "b-with-proxy", Provider: "gemini", ProxyURL: "http://127.0.0.1:8080"}); err != nil {
		t.Fatalf("Register(b-with-proxy) on mgr2: %v", err)
	}

	mgr2.egressMu.Lock()
	recovered, exists := mgr2.egressMappings["b-with-proxy"]
	_, blockedRecovered := mgr2.egressMappings["a-no-proxy"]
	mgr2.egressMu.Unlock()
	if !exists {
		t.Fatal("expected recovered mapping for b-with-proxy after restart")
	}
	if blockedRecovered {
		t.Fatal("unexpected recovered mapping for a-no-proxy")
	}
	if recovered.ProxyIdentity != "http://127.0.0.1:8080" {
		t.Fatalf("recovered.ProxyIdentity = %q, want %q", recovered.ProxyIdentity, "http://127.0.0.1:8080")
	}

	if _, err := mgr2.ExecuteCount(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("ExecuteCount() after restart: %v", err)
	}

	state, err := loadEgressMappingState(stateFile)
	if err != nil {
		t.Fatalf("loadEgressMappingState(): %v", err)
	}
	if len(state) != 1 {
		t.Fatalf("len(state) = %d, want 1", len(state))
	}
	if got := state["b-with-proxy"].DriftCount; got != 0 {
		t.Fatalf("state[b-with-proxy].DriftCount = %d, want 0", got)
	}
	if _, exists := state["a-no-proxy"]; exists {
		t.Fatalf("state contains a-no-proxy mapping, want skipped by account proxy constraint")
	}
}
