package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type refreshOnlyExecutor struct {
	provider string
}

func (e *refreshOnlyExecutor) Identifier() string {
	return e.provider
}

func (e *refreshOnlyExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshOnlyExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *refreshOnlyExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	// Simulate provider refresh returning a "clean" auth shape that would
	// otherwise wipe runtime cooldown/disabled state.
	return &Auth{
		ID:       auth.ID,
		Provider: auth.Provider,
		Label:    auth.Label,
		Status:   StatusActive,
		Disabled: false,
		Metadata: auth.Metadata,
	}, nil
}

func (e *refreshOnlyExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshOnlyExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestMergeRuntimeFailureState_PreservesDisabledPolicyState(t *testing.T) {
	now := time.Now()
	src := &Auth{
		ID:            "auth-dead",
		Provider:      "codex",
		Status:        StatusDisabled,
		Disabled:      true,
		Unavailable:   true,
		StatusMessage: "disabled_by_policy:account_deactivated",
		LastError:     &Error{Code: string(ErrorKindAccountDeactivated), Message: "account deactivated", HTTPStatus: 401},
		ModelStates: map[string]*ModelState{
			"gpt-5.3-codex": {
				Status:        StatusDisabled,
				Unavailable:   true,
				StatusMessage: "disabled_by_policy:account_deactivated",
				LastError:     &Error{Code: string(ErrorKindAccountDeactivated), Message: "account deactivated", HTTPStatus: 401},
			},
		},
	}
	dst := &Auth{
		ID:       src.ID,
		Provider: src.Provider,
		Status:   StatusActive,
		Disabled: false,
	}

	MergeRuntimeFailureState(dst, src, now)

	if !dst.Disabled || dst.Status != StatusDisabled {
		t.Fatalf("expected dst disabled, got disabled=%v status=%s", dst.Disabled, dst.Status)
	}
	if !strings.HasPrefix(dst.StatusMessage, "disabled_by_policy:") {
		t.Fatalf("expected disabled_by_policy status message, got %q", dst.StatusMessage)
	}
	state := dst.ModelStates["gpt-5.3-codex"]
	if state == nil || state.Status != StatusDisabled {
		t.Fatalf("expected model state to remain disabled, got %+v", state)
	}
}

func TestPickNextMixed_SkipsRecentlyFrozenAuth(t *testing.T) {
	mgr := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	mgr.RegisterExecutor(&refreshOnlyExecutor{provider: "codex"})

	auth1 := &Auth{
		ID:       "auth-codex-frozen",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "chatgpt58@example.com", "type": "codex"},
	}
	auth2 := &Auth{
		ID:       "auth-codex-healthy",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "chatgpt60@example.com", "type": "codex"},
	}
	if _, err := mgr.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := mgr.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth1.ID,
		Provider: "codex",
		Model:    "",
		Success:  false,
		Error: &Error{
			HTTPStatus: 429,
			Message:    "The usage limit has been reached",
		},
	})

	picked, _, _, err := mgr.pickNextMixed(context.Background(), []string{"codex"}, "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if err != nil {
		t.Fatalf("pickNextMixed error: %v", err)
	}
	if picked == nil {
		t.Fatalf("expected picked auth, got nil")
	}
	if picked.ID != auth2.ID {
		t.Fatalf("expected healthy auth to be selected, got %s", picked.ID)
	}
}
