package claude

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type claudePreFirstErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

type claudePostFirstErrorEnvelope struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeClaudePreFirstErrorEnvelope(t *testing.T, raw string) claudePreFirstErrorEnvelope {
	t.Helper()
	var payload claudePreFirstErrorEnvelope
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal pre-first error envelope: %v, raw=%s", err, raw)
	}
	if payload.Error.Type == "" {
		t.Fatalf("pre-first error.type is empty, raw=%s", raw)
	}
	return payload
}

func extractClaudePostFirstErrorEnvelope(t *testing.T, body string) claudePostFirstErrorEnvelope {
	t.Helper()
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
		if strings.HasPrefix(payload, "{") && strings.Contains(payload, `"error"`) {
			var parsed claudePostFirstErrorEnvelope
			if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
				t.Fatalf("unmarshal post-first error envelope: %v, raw=%s", err, payload)
			}
			return parsed
		}
	}
	t.Fatalf("post-first stream missing claude error envelope, body=%s", body)
	return claudePostFirstErrorEnvelope{}
}

func TestClaudeProtocolSnapshotMatrix_ClaudeCode_PreAndPostFirstPacketConsistency(t *testing.T) {
	const (
		client     = "Claude Code"
		entry      = "/v1/messages"
		errorMsg   = "claude protocol matrix blocked"
		wantStatus = http.StatusForbidden
		wantType   = "permission_error"
		wantCode   = "insufficient_quota"
	)

	preExecutor := &claudeErrorPathExecutor{
		streamErr: &coreauth.Error{
			Message:    errorMsg,
			HTTPStatus: wantStatus,
		},
	}
	preRouter := setupClaudeErrorPathHarness(t, preExecutor)

	preReq := httptest.NewRequest(http.MethodPost, entry, strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	preReq.Header.Set("Content-Type", "application/json")
	preResp := httptest.NewRecorder()
	preRouter.ServeHTTP(preResp, preReq)

	if preResp.Code != wantStatus {
		t.Fatalf("[%s] pre-first status = %d, want %d, body=%s", client, preResp.Code, wantStatus, preResp.Body.String())
	}
	prePayload := decodeClaudePreFirstErrorEnvelope(t, preResp.Body.String())
	if prePayload.Error.Type != wantType {
		t.Fatalf("[%s] pre-first error.type = %q, want %q", client, prePayload.Error.Type, wantType)
	}
	if prePayload.Error.Code != wantCode {
		t.Fatalf("[%s] pre-first error.code = %q, want %q", client, prePayload.Error.Code, wantCode)
	}
	if prePayload.Error.Message != errorMsg {
		t.Fatalf("[%s] pre-first error.message = %q, want %q", client, prePayload.Error.Message, errorMsg)
	}

	handler, ctx, rec, flusher := newClaudeStreamHarness(t)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	cancelErr := make(chan error, 1)
	go func() {
		data <- []byte("event: message\ndata: hello\n\n")
		errs <- &interfaces.ErrorMessage{
			StatusCode: wantStatus,
			Error:      errors.New(errorMsg),
		}
	}()

	handler.forwardClaudeStream(ctx, flusher, func(err error) { cancelErr <- err }, data, errs)

	postBody := rec.Body.String()
	if !strings.Contains(postBody, "event: error\ndata:") {
		t.Fatalf("[%s] post-first stream missing event:error payload, body=%s", client, postBody)
	}

	postPayload := extractClaudePostFirstErrorEnvelope(t, postBody)
	if postPayload.Type != "error" {
		t.Fatalf("[%s] post-first payload.type = %q, want %q", client, postPayload.Type, "error")
	}
	if postPayload.Error.Type != "api_error" {
		t.Fatalf("[%s] post-first payload.error.type = %q, want %q", client, postPayload.Error.Type, "api_error")
	}
	if postPayload.Error.Message != prePayload.Error.Message {
		t.Fatalf("[%s] pre/post error.message mismatch: pre=%q post=%q", client, prePayload.Error.Message, postPayload.Error.Message)
	}

	if gotCancelErr := <-cancelErr; gotCancelErr == nil || gotCancelErr.Error() != errorMsg {
		t.Fatalf("[%s] cancel error=%v, want %q", client, gotCancelErr, errorMsg)
	}
}
