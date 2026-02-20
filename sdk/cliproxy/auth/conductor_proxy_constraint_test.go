package auth

import (
	"context"
	"net/http"
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

func (noopProviderExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (noopProviderExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (noopProviderExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (noopProviderExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
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
