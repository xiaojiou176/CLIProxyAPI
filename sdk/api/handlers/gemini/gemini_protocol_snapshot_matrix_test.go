package gemini

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

type geminiOpenAIErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func decodeGeminiOpenAIErrorEnvelope(t *testing.T, raw string) geminiOpenAIErrorEnvelope {
	t.Helper()
	var payload geminiOpenAIErrorEnvelope
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal error envelope: %v, raw=%s", err, raw)
	}
	if payload.Error.Type == "" {
		t.Fatalf("error.type is empty, raw=%s", raw)
	}
	return payload
}

func extractGeminiOpenAIErrorEnvelopeFromStream(t *testing.T, body string) geminiOpenAIErrorEnvelope {
	t.Helper()
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
		if strings.HasPrefix(payload, "{") && strings.Contains(payload, `"error"`) {
			return decodeGeminiOpenAIErrorEnvelope(t, payload)
		}
	}
	t.Fatalf("stream body missing mapped error payload, body=%s", body)
	return geminiOpenAIErrorEnvelope{}
}

func TestGeminiProtocolSnapshotMatrix_Cursor_PreAndPostFirstPacketConsistency(t *testing.T) {
	const (
		client     = "Cursor"
		entry      = "/v1beta/models/gemini-3.0-pro:streamGenerateContent"
		errorMsg   = "gemini protocol matrix blocked"
		wantStatus = http.StatusForbidden
		wantType   = "permission_error"
		wantCode   = "insufficient_quota"
	)

	preExecutor := &geminiErrorPathExecutor{
		streamErr: &coreauth.Error{
			Message:    errorMsg,
			HTTPStatus: wantStatus,
		},
	}
	preRouter := setupGeminiErrorPathHarness(t, preExecutor)

	preReq := httptest.NewRequest(http.MethodPost, entry, strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}]}`))
	preReq.Header.Set("Content-Type", "application/json")
	preResp := httptest.NewRecorder()
	preRouter.ServeHTTP(preResp, preReq)

	if preResp.Code != wantStatus {
		t.Fatalf("[%s] pre-first status = %d, want %d, body=%s", client, preResp.Code, wantStatus, preResp.Body.String())
	}
	prePayload := decodeGeminiOpenAIErrorEnvelope(t, preResp.Body.String())
	if prePayload.Error.Type != wantType {
		t.Fatalf("[%s] pre-first error.type = %q, want %q", client, prePayload.Error.Type, wantType)
	}
	if prePayload.Error.Code != wantCode {
		t.Fatalf("[%s] pre-first error.code = %q, want %q", client, prePayload.Error.Code, wantCode)
	}
	if prePayload.Error.Message != errorMsg {
		t.Fatalf("[%s] pre-first error.message = %q, want %q", client, prePayload.Error.Message, errorMsg)
	}

	handler, ctx, rec, flusher := newGeminiStreamHarness(t, entry)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	cancelErr := make(chan error, 1)
	go func() {
		data <- []byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`)
		errs <- &interfaces.ErrorMessage{
			StatusCode: wantStatus,
			Error:      errors.New(errorMsg),
		}
	}()

	handler.forwardGeminiStream(ctx, flusher, "", func(err error) { cancelErr <- err }, data, errs)

	postBody := rec.Body.String()
	if !strings.Contains(postBody, "event: error\ndata:") {
		t.Fatalf("[%s] post-first stream missing event:error payload, body=%s", client, postBody)
	}

	postPayload := extractGeminiOpenAIErrorEnvelopeFromStream(t, postBody)
	if postPayload.Error.Type != prePayload.Error.Type {
		t.Fatalf("[%s] pre/post error.type mismatch: pre=%q post=%q", client, prePayload.Error.Type, postPayload.Error.Type)
	}
	if postPayload.Error.Code != prePayload.Error.Code {
		t.Fatalf("[%s] pre/post error.code mismatch: pre=%q post=%q", client, prePayload.Error.Code, postPayload.Error.Code)
	}
	if postPayload.Error.Message != prePayload.Error.Message {
		t.Fatalf("[%s] pre/post error.message mismatch: pre=%q post=%q", client, prePayload.Error.Message, postPayload.Error.Message)
	}

	if gotCancelErr := <-cancelErr; gotCancelErr == nil || gotCancelErr.Error() != errorMsg {
		t.Fatalf("[%s] cancel error=%v, want %q", client, gotCancelErr, errorMsg)
	}
}
