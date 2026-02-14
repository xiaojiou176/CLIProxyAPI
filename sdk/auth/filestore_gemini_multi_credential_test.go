package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestFileTokenStore_SaveGeminiSameEmailProject_MultipleFilesDoNotOverwrite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tempDir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(tempDir)

	email := "same@example.com"
	projectID := "proj-a"
	legacyName := "same@example.com-proj-a.json"
	newName := "gemini-same@example.com-proj-a-cred-b.json"

	recordA := &cliproxyauth.Auth{
		ID:       legacyName,
		Provider: "gemini",
		FileName: legacyName,
		Storage: &gemini.GeminiTokenStorage{
			Token:     map[string]any{"access_token": "token-a"},
			ProjectID: projectID,
			Email:     email,
		},
		Metadata: map[string]any{
			"type":       "gemini",
			"email":      email,
			"project_id": projectID,
		},
	}
	recordB := &cliproxyauth.Auth{
		ID:       newName,
		Provider: "gemini",
		FileName: newName,
		Storage: &gemini.GeminiTokenStorage{
			Token:     map[string]any{"access_token": "token-b"},
			ProjectID: projectID,
			Email:     email,
		},
		Metadata: map[string]any{
			"type":       "gemini",
			"email":      email,
			"project_id": projectID,
		},
	}

	pathA, err := store.Save(ctx, recordA)
	if err != nil {
		t.Fatalf("Save(recordA) error = %v", err)
	}
	pathB, err := store.Save(ctx, recordB)
	if err != nil {
		t.Fatalf("Save(recordB) error = %v", err)
	}
	if pathA == pathB {
		t.Fatalf("pathA == pathB == %q, want distinct files", pathA)
	}

	dataA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("ReadFile(pathA) error = %v", err)
	}
	if err := assertGeminiAccessToken(dataA, "token-a"); err != nil {
		t.Fatalf("pathA token assertion failed: %v", err)
	}

	dataB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("ReadFile(pathB) error = %v", err)
	}
	if err := assertGeminiAccessToken(dataB, "token-b"); err != nil {
		t.Fatalf("pathB token assertion failed: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	ids := make([]string, 0, 2)
	for _, item := range list {
		if item == nil || item.Provider != "gemini" {
			continue
		}
		metaEmail, _ := item.Metadata["email"].(string)
		metaProject, _ := item.Metadata["project_id"].(string)
		if metaEmail == email && metaProject == projectID {
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	if len(ids) != 2 {
		t.Fatalf("gemini entries for %s/%s = %d, want 2 (ids=%v)", email, projectID, len(ids), ids)
	}
	gotSet := map[string]struct{}{ids[0]: {}, ids[1]: {}}
	if _, ok := gotSet[legacyName]; !ok {
		t.Fatalf("legacy id missing, ids=%v", ids)
	}
	if _, ok := gotSet[newName]; !ok {
		t.Fatalf("new-style id missing, ids=%v", ids)
	}
}

func TestFileTokenStore_List_ReadsLegacyGeminiFileName(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(tempDir)

	legacyName := "legacy@example.com-legacy-project.json"
	legacyPath := filepath.Join(tempDir, legacyName)

	payload := map[string]any{
		"type":       "gemini",
		"email":      "legacy@example.com",
		"project_id": "legacy-project",
		"token": map[string]any{
			"access_token": "legacy-access-token",
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(payload) error = %v", err)
	}
	if err := os.WriteFile(legacyPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", legacyPath, err)
	}

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var found *cliproxyauth.Auth
	for _, item := range list {
		if item != nil && item.ID == legacyName {
			found = item
			break
		}
	}
	if found == nil {
		t.Fatalf("legacy entry %q not found in list", legacyName)
	}
	if found.Provider != "gemini" {
		t.Fatalf("found.Provider = %q, want %q", found.Provider, "gemini")
	}
	if found.Label != "legacy@example.com" {
		t.Fatalf("found.Label = %q, want %q", found.Label, "legacy@example.com")
	}
	if found.Attributes["email"] != "legacy@example.com" {
		t.Fatalf("found.Attributes[email] = %q, want %q", found.Attributes["email"], "legacy@example.com")
	}
}

func assertGeminiAccessToken(raw []byte, want string) error {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	tokenObj, ok := payload["token"].(map[string]any)
	if !ok {
		return os.ErrInvalid
	}
	got, _ := tokenObj["access_token"].(string)
	if got != want {
		return &tokenMismatchError{got: got, want: want}
	}
	return nil
}

type tokenMismatchError struct {
	got  string
	want string
}

func (e *tokenMismatchError) Error() string {
	return "access_token mismatch: got=" + e.got + " want=" + e.want
}
