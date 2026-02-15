package auth

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type metadataRoundTripStore struct {
	mu   sync.Mutex
	data map[string]map[string]any
}

func (s *metadataRoundTripStore) List(context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*Auth, 0, len(s.data))
	for id, row := range s.data {
		metaRaw, _ := json.Marshal(row)
		meta := map[string]any{}
		_ = json.Unmarshal(metaRaw, &meta)
		disabled, _ := meta["disabled"].(bool)
		status := StatusActive
		if disabled {
			status = StatusDisabled
		}
		provider, _ := meta["type"].(string)
		out = append(out, &Auth{
			ID:         id,
			Provider:   provider,
			Status:     status,
			Disabled:   disabled,
			Attributes: map[string]string{"path": id},
			Metadata:   meta,
		})
	}
	return out, nil
}

func (s *metadataRoundTripStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		s.data = make(map[string]map[string]any)
	}
	metaRaw, _ := json.Marshal(auth.Metadata)
	meta := map[string]any{}
	_ = json.Unmarshal(metaRaw, &meta)
	meta["disabled"] = auth.Disabled
	meta["type"] = auth.Provider
	s.data[auth.ID] = meta
	return auth.ID, nil
}

func (s *metadataRoundTripStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
	return nil
}

func TestRuntimeStatePersistence_RoundTripCooldownAndModelState(t *testing.T) {
	store := &metadataRoundTripStore{}
	mgr := NewManager(store, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{DisableFatalAccounts: true},
	})

	auth := &Auth{
		ID:       "auth-cooldown",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "email": "cooldown@example.com"},
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
		Error:    &Error{HTTPStatus: 500, Message: "stream disconnected before completion"},
	})

	mgrReloaded := NewManager(store, nil, nil)
	if err := mgrReloaded.Load(context.Background()); err != nil {
		t.Fatalf("load manager: %v", err)
	}

	loaded, ok := mgrReloaded.GetByID(auth.ID)
	if !ok || loaded == nil {
		t.Fatalf("expected auth to be loaded")
	}
	state := loaded.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be restored from metadata")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected model cooldown to be restored")
	}
	if state.NextRetryAfter.Before(time.Now().Add(29 * time.Minute)) {
		t.Fatalf("expected cooldown >= 30m, got %v", state.NextRetryAfter)
	}
}

func TestRuntimeStatePersistence_RoundTripFatalDisable(t *testing.T) {
	store := &metadataRoundTripStore{}
	mgr := NewManager(store, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{DisableFatalAccounts: true},
	})

	auth := &Auth{
		ID:       "auth-fatal",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "email": "fatal@example.com"},
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	model := "gpt-5.3-codex"
	msg := `{"error":{"message":"Your authentication token has been invalidated. Please try signing in again.","code":"token_invalidated"}}`
	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 401, Message: msg},
	})

	mgrReloaded := NewManager(store, nil, nil)
	if err := mgrReloaded.Load(context.Background()); err != nil {
		t.Fatalf("load manager: %v", err)
	}

	loaded, ok := mgrReloaded.GetByID(auth.ID)
	if !ok || loaded == nil {
		t.Fatalf("expected auth to be loaded")
	}
	if !loaded.Disabled {
		t.Fatalf("expected fatal account to stay disabled after reload")
	}
	if loaded.Status != StatusDisabled {
		t.Fatalf("expected status disabled, got %s", loaded.Status)
	}
	if loaded.StatusMessage == "" || loaded.StatusMessage == "disabled via management API" {
		t.Fatalf("expected policy status message, got %q", loaded.StatusMessage)
	}
	if loaded.LastError == nil || loaded.LastError.Code == "" {
		t.Fatalf("expected last error kind to be restored")
	}
}
