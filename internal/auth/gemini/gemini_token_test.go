package gemini

import (
	"regexp"
	"strings"
	"testing"
)

func TestCredentialFileNameForStorage_GeneratesStableSuffix(t *testing.T) {
	ts := &GeminiTokenStorage{
		Email:     "user@example.com",
		ProjectID: "project-123",
	}

	first := CredentialFileNameForStorage(ts, true)
	second := CredentialFileNameForStorage(ts, true)

	if first != second {
		t.Fatalf("expected stable filename, got %q then %q", first, second)
	}
	if strings.TrimSpace(ts.CredentialID) == "" {
		t.Fatal("expected credential_id to be generated")
	}

	pattern := regexp.MustCompile(`^gemini-user@example\.com-project-123--[a-z0-9]+\.json$`)
	if !pattern.MatchString(first) {
		t.Fatalf("unexpected filename format: %q", first)
	}
}

func TestCredentialFileNameForStorage_UsesExistingCredentialID(t *testing.T) {
	ts := &GeminiTokenStorage{
		Email:        "user@example.com",
		ProjectID:    "project-abc",
		CredentialID: "ID-XYZ_001",
	}

	name := CredentialFileNameForStorage(ts, true)

	if name != "gemini-user@example.com-project-abc--idxyz001.json" {
		t.Fatalf("unexpected filename: %q", name)
	}
	if ts.CredentialID != "idxyz001" {
		t.Fatalf("expected normalized credential_id, got %q", ts.CredentialID)
	}
}

func TestCredentialFileName_LegacyCompatibility(t *testing.T) {
	name := CredentialFileName("user@example.com", "project-a", true)
	if name != "gemini-user@example.com-project-a.json" {
		t.Fatalf("unexpected legacy filename: %q", name)
	}

	allName := CredentialFileName("user@example.com", "project-a,project-b", false)
	if allName != "gemini-user@example.com-all.json" {
		t.Fatalf("unexpected all-project filename: %q", allName)
	}
}

func TestCredentialFileNameForStorage_SameEmailProjectDifferentCredentials(t *testing.T) {
	first := &GeminiTokenStorage{
		Email:     "same@example.com",
		ProjectID: "project-a",
	}
	second := &GeminiTokenStorage{
		Email:     "same@example.com",
		ProjectID: "project-a",
	}

	firstName := CredentialFileNameForStorage(first, true)
	secondName := CredentialFileNameForStorage(second, true)

	if firstName == secondName {
		t.Fatalf("expected distinct filenames for different credentials, got %q", firstName)
	}
	if first.CredentialID == "" || second.CredentialID == "" {
		t.Fatal("expected credential IDs to be populated")
	}
	if first.CredentialID == second.CredentialID {
		t.Fatalf("expected distinct credential IDs, got same value %q", first.CredentialID)
	}
}
