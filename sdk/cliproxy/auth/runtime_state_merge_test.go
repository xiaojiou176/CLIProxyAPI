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

func (e *refreshOnlyExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return ch, nil
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

func TestRefreshAuth_PreservesActiveCooldownRuntimeState(t *testing.T) {
	mgr := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	mgr.RegisterExecutor(&refreshOnlyExecutor{provider: "codex"})

	auth := &Auth{
		ID:       "auth-codex-1",
		Provider: "codex",
		Label:    "chatgpt58@example.com",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "chatgpt58@example.com", "type": "codex"},
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	model := "gpt-5.3-codex"
	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: 429,
			Message:    "The usage limit has been reached",
		},
	})

	before, ok := mgr.GetByID(auth.ID)
	if !ok || before == nil {
		t.Fatalf("auth not found before refresh")
	}
	blockedBefore, _, nextBefore := isAuthBlockedForModel(before, model, time.Now())
	if !blockedBefore || nextBefore.IsZero() {
		t.Fatalf("expected auth to be blocked before refresh, blocked=%v next=%v", blockedBefore, nextBefore)
	}

	mgr.refreshAuth(context.Background(), auth.ID)

	after, ok := mgr.GetByID(auth.ID)
	if !ok || after == nil {
		t.Fatalf("auth not found after refresh")
	}
	blockedAfter, _, nextAfter := isAuthBlockedForModel(after, model, time.Now())
	if !blockedAfter || nextAfter.IsZero() {
		t.Fatalf("expected auth to stay blocked after refresh, blocked=%v next=%v", blockedAfter, nextAfter)
	}
	if nextAfter.Before(time.Now().Add(29 * time.Minute)) {
		t.Fatalf("expected cooldown >= 30m after refresh, got %v", nextAfter)
	}
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

func TestMarkResult_QuotaFreezeBlockUsesMinimumCooldown(t *testing.T) {
	mgr := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	auth := &Auth{
		ID:       "auth-codex-2",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "chatgpt60@example.com", "type": "codex"},
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	model := "gpt-5.3-codex"
	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: 429,
			Message:    "The usage limit has been reached",
		},
	})

	updated, ok := mgr.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("auth not found")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("model state missing")
	}
	minExpected := time.Now().Add(29 * time.Minute)
	if state.NextRetryAfter.Before(minExpected) {
		t.Fatalf("expected next_retry_after >= 30m, got %v", state.NextRetryAfter)
	}
	if state.Quota.NextRecoverAt.Before(minExpected) {
		t.Fatalf("expected quota.next_recover_at to honor minimum cooldown, got %v", state.Quota.NextRecoverAt)
	}

	blocked, _, next := isAuthBlockedForModel(updated, model, time.Now())
	if !blocked {
		t.Fatalf("expected auth blocked for model after 429")
	}
	if next.Before(minExpected) {
		t.Fatalf("expected selector block horizon >= 30m, got %v", next)
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
