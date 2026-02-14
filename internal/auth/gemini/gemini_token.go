// Package gemini provides authentication and token management functionality
// for Google's Gemini AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Gemini API.
package gemini

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	log "github.com/sirupsen/logrus"
)

// GeminiTokenStorage stores OAuth2 token information for Google Gemini API authentication.
// It maintains compatibility with the existing auth system while adding Gemini-specific fields
// for managing access tokens, refresh tokens, and user account information.
type GeminiTokenStorage struct {
	// Token holds the raw OAuth2 token data, including access and refresh tokens.
	Token any `json:"token"`

	// ProjectID is the Google Cloud Project ID associated with this token.
	ProjectID string `json:"project_id"`

	// Email is the email address of the authenticated user.
	Email string `json:"email"`

	// Auto indicates if the project ID was automatically selected.
	Auto bool `json:"auto"`

	// Checked indicates if the associated Cloud AI API has been verified as enabled.
	Checked bool `json:"checked"`

	// Type indicates the authentication provider type, always "gemini" for this storage.
	Type string `json:"type"`

	// CredentialID is a stable per-credential identifier used to avoid filename collisions
	// when multiple records share the same email and project_id.
	CredentialID string `json:"credential_id,omitempty"`
}

// SaveTokenToFile serializes the Gemini token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *GeminiTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "gemini"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("failed to close file: %v", errClose)
		}
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// CredentialFileName returns the filename used to persist Gemini CLI credentials.
// When projectID represents multiple projects (comma-separated or literal ALL),
// the suffix is normalized to "all" and a "gemini-" prefix is enforced to keep
// web and CLI generated files consistent.
func CredentialFileName(email, projectID string, includeProviderPrefix bool) string {
	email = strings.TrimSpace(email)
	project := strings.TrimSpace(projectID)
	if strings.EqualFold(project, "all") || strings.Contains(project, ",") {
		return fmt.Sprintf("gemini-%s-all.json", email)
	}
	prefix := ""
	if includeProviderPrefix {
		prefix = "gemini-"
	}
	return fmt.Sprintf("%s%s-%s.json", prefix, email, project)
}

// CredentialFileNameForStorage returns a collision-safe credential filename while preserving
// human readability (email + project). A stable credential suffix is persisted in storage so
// repeated saves keep the same path.
func CredentialFileNameForStorage(ts *GeminiTokenStorage, includeProviderPrefix bool) string {
	if ts == nil {
		return CredentialFileName("", "", includeProviderPrefix)
	}
	base := CredentialFileName(ts.Email, ts.ProjectID, includeProviderPrefix)
	credentialID := normalizeCredentialID(ts.CredentialID)
	if credentialID == "" {
		credentialID = newCredentialID()
	}
	ts.CredentialID = credentialID
	return appendCredentialIDSuffix(base, credentialID)
}

func appendCredentialIDSuffix(fileName, credentialID string) string {
	fileName = strings.TrimSpace(fileName)
	credentialID = normalizeCredentialID(credentialID)
	if fileName == "" || credentialID == "" {
		return fileName
	}
	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)
	suffix := "--" + credentialID
	if strings.HasSuffix(base, suffix) {
		return fileName
	}
	return base + suffix + ext
}

func normalizeCredentialID(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	builder := strings.Builder{}
	builder.Grow(len(raw))
	for _, ch := range raw {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			builder.WriteRune(ch)
		}
	}
	return builder.String()
}

func newCredentialID() string {
	const bytesLen = 6 // 12 hex chars
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}
