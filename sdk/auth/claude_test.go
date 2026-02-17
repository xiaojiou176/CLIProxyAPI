package auth

import (
	"errors"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/claude"
)

func TestValidateClaudeOAuthStateMatch(t *testing.T) {
	err := validateClaudeOAuthState("expected-state", "expected-state")
	if err != nil {
		t.Fatalf("expected nil error for matching state, got %v", err)
	}
}

func TestValidateClaudeOAuthStateMismatch(t *testing.T) {
	err := validateClaudeOAuthState("expected-state", "received-state")
	if err == nil {
		t.Fatal("expected authentication error for state mismatch")
	}

	var authErr *claudeauth.AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthenticationError, got %T", err)
	}
	if authErr.Type != claudeauth.ErrInvalidState.Type {
		t.Fatalf("expected error type %q, got %q", claudeauth.ErrInvalidState.Type, authErr.Type)
	}
}
