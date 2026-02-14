package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type recordingProviderExecutor struct {
	identifier string
	mu         sync.Mutex
	calledAuth []string
}

func (e *recordingProviderExecutor) Identifier() string { return e.identifier }

func (e *recordingProviderExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calledAuth = append(e.calledAuth, auth.ID)
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (*recordingProviderExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return ch, nil
}

func (*recordingProviderExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (*recordingProviderExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*recordingProviderExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *recordingProviderExecutor) calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calledAuth))
	copy(out, e.calledAuth)
	return out
}

func TestManagerExecute_InternalDrillProxyFailure_FailsFastWithoutPenalty(t *testing.T) {
	manager := NewManager(nil, deterministicSelector{}, nil)
	executor := &recordingProviderExecutor{identifier: "gemini"}
	manager.RegisterExecutor(executor)

	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "a-auth", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(a-auth): %v", errRegister)
	}
	if _, errApply := manager.ApplyInternalDrillFault(InternalDrillFaultScenarioProxyFailure, 1); errApply != nil {
		t.Fatalf("ApplyInternalDrillFault(proxy-failure): %v", errApply)
	}

	req := cliproxyexecutor.Request{Model: ""}
	_, errExec := manager.Execute(context.Background(), []string{"gemini"}, req, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatalf("Execute() error = nil, want injected proxy failure")
	}
	authErr, ok := errExec.(*Error)
	if !ok {
		t.Fatalf("Execute() err type = %T, want *Error", errExec)
	}
	if authErr.Code != "proxy_failure_injected" {
		t.Fatalf("Execute() err.Code = %q, want %q", authErr.Code, "proxy_failure_injected")
	}
	if gotCalls := executor.calls(); len(gotCalls) != 0 {
		t.Fatalf("executor calls = %v, want none when proxy failure is injected before execution", gotCalls)
	}

	updated, okGet := manager.GetByID("a-auth")
	if !okGet || updated == nil {
		t.Fatalf("GetByID(a-auth) returned nil")
	}
	if updated.LastError != nil {
		t.Fatalf("LastError = %+v, want nil", updated.LastError)
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("ModelStates = %+v, want empty", updated.ModelStates)
	}

	resp, errSecond := manager.Execute(context.Background(), []string{"gemini"}, req, cliproxyexecutor.Options{})
	if errSecond != nil {
		t.Fatalf("second Execute() error = %v, want nil after one-shot injection", errSecond)
	}
	if string(resp.Payload) != "a-auth" {
		t.Fatalf("second Execute() payload = %q, want %q", string(resp.Payload), "a-auth")
	}
}

func TestManagerExecute_InternalDrillQuotaExhausted_TriggersAccountSwitch(t *testing.T) {
	manager := NewManager(nil, deterministicSelector{}, nil)
	executor := &recordingProviderExecutor{identifier: "gemini"}
	manager.RegisterExecutor(executor)

	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "a-auth", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(a-auth): %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "b-auth", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(b-auth): %v", errRegister)
	}
	if _, errApply := manager.ApplyInternalDrillFault(InternalDrillFaultScenarioAccountQuotaExhausted, 1); errApply != nil {
		t.Fatalf("ApplyInternalDrillFault(account-quota-exhausted): %v", errApply)
	}

	resp, errExec := manager.Execute(
		context.Background(),
		[]string{"gemini"},
		cliproxyexecutor.Request{Model: ""},
		cliproxyexecutor.Options{},
	)
	if errExec != nil {
		t.Fatalf("Execute() error = %v, want nil", errExec)
	}
	if string(resp.Payload) != "b-auth" {
		t.Fatalf("Execute() payload = %q, want %q", string(resp.Payload), "b-auth")
	}

	gotCalls := executor.calls()
	if len(gotCalls) != 1 || gotCalls[0] != "b-auth" {
		t.Fatalf("executor calls = %v, want [b-auth] (first auth should fail via injected quota before executor)", gotCalls)
	}

	firstAuth, okFirst := manager.GetByID("a-auth")
	if !okFirst || firstAuth == nil {
		t.Fatalf("GetByID(a-auth) returned nil")
	}
	if firstAuth.Status != StatusError {
		t.Fatalf("a-auth status = %q, want %q", firstAuth.Status, StatusError)
	}
	if !firstAuth.Quota.Exceeded {
		t.Fatalf("a-auth quota.exceeded = false, want true")
	}
	if firstAuth.Quota.Reason != "quota" {
		t.Fatalf("a-auth quota.reason = %q, want %q", firstAuth.Quota.Reason, "quota")
	}
	if firstAuth.NextRetryAfter.IsZero() {
		t.Fatalf("a-auth next_retry_after is zero, want non-zero")
	}

	secondAuth, okSecond := manager.GetByID("b-auth")
	if !okSecond || secondAuth == nil {
		t.Fatalf("GetByID(b-auth) returned nil")
	}
	if secondAuth.Quota.Exceeded {
		t.Fatalf("b-auth quota.exceeded = true, want false")
	}
}
