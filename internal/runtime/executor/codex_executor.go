package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	codexClientVersion = "0.101.0"
	codexUserAgent     = "codex_cli_rs/0.101.0 (Mac OS 26.0.1; arm64) Apple_Terminal/464"
)

var dataTag = []byte("data:")
var eventTag = []byte("event:")

var codexRetryAfterPattern = regexp.MustCompile(`(?i)(?:try again in|in)\s*([0-9]+(?:\.[0-9]+)?)\s*(?:s|sec|secs|second|seconds)\b`)

func codexEventPayload(line []byte) ([]byte, string, bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, dataTag) {
		return nil, "", false
	}
	data := bytes.TrimSpace(line[len(dataTag):])
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil, "", false
	}
	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	if eventType == "" {
		return nil, "", false
	}
	return data, eventType, true
}

func codexEventHeaderType(line []byte) (string, bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, eventTag) {
		return "", false
	}
	eventType := strings.TrimSpace(string(line[len(eventTag):]))
	if eventType == "" {
		return "", false
	}
	return eventType, true
}

func codexTerminalEventType(line []byte) (string, bool) {
	if _, eventType, ok := codexEventPayload(line); ok {
		switch eventType {
		case "response.completed", "response.done":
			return eventType, true
		default:
			return eventType, false
		}
	}
	if eventType, ok := codexEventHeaderType(line); ok {
		switch eventType {
		case "response.completed", "response.done":
			return eventType, true
		default:
			return eventType, false
		}
	}
	return "", false
}

func codexTerminalPayload(line []byte) ([]byte, bool) {
	data, eventType, ok := codexEventPayload(line)
	if !ok {
		return nil, false
	}
	switch eventType {
	case "response.completed", "response.done":
		return data, true
	default:
		return nil, false
	}
}

func codexStreamFailure(line []byte) (statusErr, bool) {
	data, eventType, ok := codexEventPayload(line)
	if !ok {
		return statusErr{}, false
	}
	switch eventType {
	case "response.failed":
		return codexFailedStreamErr(data), true
	case "response.incomplete":
		return codexIncompleteStreamErr(data), true
	default:
		return statusErr{}, false
	}
}

func codexFailedStreamErr(data []byte) statusErr {
	code := strings.TrimSpace(gjson.GetBytes(data, "response.error.code").String())
	message := strings.TrimSpace(gjson.GetBytes(data, "response.error.message").String())
	if message == "" {
		message = "response.failed event received"
	}

	errorNode := map[string]any{
		"message": message,
	}
	if code != "" {
		errorNode["code"] = code
	}

	for _, key := range []string{
		"resets_in_seconds",
		"retry_after_seconds",
		"retry_in_seconds",
		"reset_in_seconds",
		"resets_at",
		"reset_at",
		"retry_after_at",
		"retry_at",
		"retry_after",
	} {
		if v := gjson.GetBytes(data, "response.error."+key); v.Exists() {
			errorNode[key] = codexJSONValue(v)
		}
	}

	payload := map[string]any{
		"type":    "response.failed",
		"message": message,
		"error":   errorNode,
	}

	msg := message
	if b, err := json.Marshal(payload); err == nil {
		msg = string(b)
	}

	return statusErr{
		code:       codexStatusFromFailedCode(strings.ToLower(code)),
		msg:        msg,
		retryAfter: codexRetryAfterFromFailed(data, message),
	}
}

func codexIncompleteStreamErr(data []byte) statusErr {
	reason := strings.TrimSpace(gjson.GetBytes(data, "response.incomplete_details.reason").String())
	message := "Incomplete response returned, reason: unknown"
	if reason != "" {
		message = "Incomplete response returned, reason: " + reason
	}

	payload := map[string]any{
		"type":    "response.incomplete",
		"message": message,
		"error": map[string]any{
			"code":    "response_incomplete",
			"message": message,
			"reason":  reason,
		},
	}
	msg := message
	if b, err := json.Marshal(payload); err == nil {
		msg = string(b)
	}
	return statusErr{code: http.StatusRequestTimeout, msg: msg}
}

func codexStatusFromFailedCode(code string) int {
	switch code {
	case "rate_limit_exceeded", "insufficient_quota", "quota_exceeded", "usage_limit_exceeded":
		return http.StatusTooManyRequests
	case "invalid_prompt", "context_length_exceeded", "invalid_request", "invalid_request_error", "bad_request":
		return http.StatusBadRequest
	case "workspace_deactivated", "deactivated_workspace", "payment_required":
		return http.StatusPaymentRequired
	case "forbidden", "access_denied", "permission_denied":
		return http.StatusForbidden
	case "unauthorized", "invalid_api_key", "authentication_error", "token_invalidated", "workspace_unauthorized":
		return http.StatusUnauthorized
	case "server_overloaded", "overloaded", "service_unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func codexRetryAfterFromFailed(data []byte, message string) *time.Duration {
	for _, key := range []string{
		"response.error.resets_in_seconds",
		"response.error.retry_after_seconds",
		"response.error.retry_in_seconds",
		"response.error.reset_in_seconds",
		"response.error.retry_after",
	} {
		if v := gjson.GetBytes(data, key); v.Exists() {
			if seconds, ok := codexPositiveFloat(v); ok && seconds > 0 {
				delay := time.Duration(math.Ceil(seconds * float64(time.Second)))
				return &delay
			}
		}
	}

	for _, key := range []string{
		"response.error.resets_at",
		"response.error.reset_at",
		"response.error.retry_after_at",
		"response.error.retry_at",
	} {
		if v := gjson.GetBytes(data, key); v.Exists() {
			if ts, ok := codexPositiveFloat(v); ok && ts > 0 {
				delay := time.Until(time.Unix(int64(ts), 0))
				if delay > 0 {
					return &delay
				}
			}
		}
	}

	matches := codexRetryAfterPattern.FindStringSubmatch(message)
	if len(matches) > 1 {
		if seconds, err := strconv.ParseFloat(matches[1], 64); err == nil && seconds > 0 {
			delay := time.Duration(math.Ceil(seconds * float64(time.Second)))
			return &delay
		}
	}
	return nil
}

func codexPositiveFloat(v gjson.Result) (float64, bool) {
	switch v.Type {
	case gjson.Number:
		return v.Float(), true
	case gjson.String:
		s := strings.TrimSpace(v.String())
		if s == "" {
			return 0, false
		}
		value, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	default:
		return 0, false
	}
}

func codexJSONValue(v gjson.Result) any {
	var out any
	if err := json.Unmarshal([]byte(v.Raw), &out); err == nil {
		return out
	}
	return v.String()
}

type codexStreamDisconnectInfo struct {
	cause         string
	lastEventType string
	chunksSeen    int
	sawTerminal   bool
	sawOutputText bool
	scannerErr    error
}

func codexDisconnectedStreamErr(info codexStreamDisconnectInfo) statusErr {
	cause := strings.TrimSpace(info.cause)
	if cause == "" {
		cause = "no_terminal_event"
	}
	errNode := map[string]any{
		"code":              "stream_disconnected_before_completion",
		"message":           "stream closed before response.completed",
		"cause":             cause,
		"saw_terminal":      info.sawTerminal,
		"last_event_type":   info.lastEventType,
		"chunks_seen":       info.chunksSeen,
		"saw_output_text":   info.sawOutputText,
		"terminal_expected": []string{"response.completed", "response.done"},
	}
	if info.scannerErr != nil {
		errNode["scanner_error"] = info.scannerErr.Error()
	}
	payload := map[string]any{
		"type":    "stream.disconnected",
		"message": "stream disconnected before completion",
		"error":   errNode,
	}
	msg := "stream error: stream disconnected before completion: stream closed before response.completed"
	if b, err := json.Marshal(payload); err == nil {
		msg = string(b)
	}
	return statusErr{code: http.StatusRequestTimeout, msg: msg}
}

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg *config.Config
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

// PrepareRequest injects Codex credentials into the outgoing HTTP request.
func (e *CodexExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Codex credentials into the request and executes it.
func (e *CodexExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codex executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	if !gjson.GetBytes(body, "instructions").Exists() {
		body, _ = sjson.SetBytes(body, "instructions", "")
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return resp, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	lines := bytes.Split(data, []byte("\n"))
	var sawTerminal bool
	var lastEventType string
	var chunksSeen int
	var sawOutputText bool
	for _, line := range lines {
		chunksSeen++
		if eventType, terminal := codexTerminalEventType(line); eventType != "" {
			lastEventType = eventType
			if terminal {
				sawTerminal = true
			}
		}
		if _, eventType, ok := codexEventPayload(line); ok {
			if eventType == "response.output_text.delta" || eventType == "response.output_item.done" {
				sawOutputText = true
			}
		}
		if streamErr, failed := codexStreamFailure(line); failed {
			err = streamErr
			return resp, err
		}
		line, ok := codexTerminalPayload(line)
		if !ok {
			continue
		}
		if detail, ok := parseCodexUsage(line); ok {
			reporter.publish(ctx, detail)
		}

		var param any
		out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, line, &param)
		resp = cliproxyexecutor.Response{Payload: []byte(out)}
		return resp, nil
	}
	if sawTerminal {
		err = statusErr{code: http.StatusBadGateway, msg: "stream error: terminal event received without terminal payload"}
		return resp, err
	}
	err = codexDisconnectedStreamErr(codexStreamDisconnectInfo{
		cause:         "no_terminal_event",
		lastEventType: lastEventType,
		chunksSeen:    chunksSeen,
		sawTerminal:   sawTerminal,
		sawOutputText: sawOutputText,
	})
	return resp, err
}

func (e *CodexExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "stream")

	url := strings.TrimSuffix(baseURL, "/") + "/responses/compact"
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return resp, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, false)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)
	reporter.publish(ctx, parseOpenAIUsage(data))
	reporter.ensurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.SetBytes(body, "model", baseModel)
	if !gjson.GetBytes(body, "instructions").Exists() {
		body, _ = sjson.SetBytes(body, "instructions", "")
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return nil, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		logWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		sawTerminal := false
		lastEventType := ""
		chunksSeen := 0
		sawOutputText := false
		for scanner.Scan() {
			line := scanner.Bytes()
			chunksSeen++
			appendAPIResponseChunk(ctx, e.cfg, line)
			if eventType, terminal := codexTerminalEventType(line); eventType != "" {
				lastEventType = eventType
				if terminal {
					sawTerminal = true
				}
			}
			if _, eventType, ok := codexEventPayload(line); ok {
				if eventType == "response.output_text.delta" || eventType == "response.output_item.done" {
					sawOutputText = true
				}
			}

			if streamErr, failed := codexStreamFailure(line); failed {
				recordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: streamErr}
				return
			}

			if data, ok := codexTerminalPayload(line); ok {
				sawTerminal = true
				if detail, ok := parseCodexUsage(data); ok {
					reporter.publish(ctx, detail)
				}
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			streamErr := codexDisconnectedStreamErr(codexStreamDisconnectInfo{
				cause:         "scanner_error",
				lastEventType: lastEventType,
				chunksSeen:    chunksSeen,
				sawTerminal:   sawTerminal,
				sawOutputText: sawOutputText,
				scannerErr:    errScan,
			})
			recordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: streamErr}
			return
		}
		if ctx != nil && ctx.Err() != nil {
			return
		}
		if !sawTerminal {
			errDisconnected := codexDisconnectedStreamErr(codexStreamDisconnectInfo{
				cause:         "no_terminal_event",
				lastEventType: lastEventType,
				chunksSeen:    chunksSeen,
				sawTerminal:   sawTerminal,
				sawOutputText: sawOutputText,
			})
			recordAPIResponseError(ctx, e.cfg, errDisconnected)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errDisconnected}
		}
	}()
	return stream, nil
}

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err := thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.SetBytes(body, "stream", false)
	if !gjson.GetBytes(body, "instructions").Exists() {
		body, _ = sjson.SetBytes(body, "instructions", "")
	}

	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}

	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}

func countCodexInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(body) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(body)
	var segments []string

	if inst := strings.TrimSpace(root.Get("instructions").String()); inst != "" {
		segments = append(segments, inst)
	}

	inputItems := root.Get("input")
	if inputItems.IsArray() {
		arr := inputItems.Array()
		for i := range arr {
			item := arr[i]
			switch item.Get("type").String() {
			case "message":
				content := item.Get("content")
				if content.IsArray() {
					parts := content.Array()
					for j := range parts {
						part := parts[j]
						if text := strings.TrimSpace(part.Get("text").String()); text != "" {
							segments = append(segments, text)
						}
					}
				}
			case "function_call":
				if name := strings.TrimSpace(item.Get("name").String()); name != "" {
					segments = append(segments, name)
				}
				if args := strings.TrimSpace(item.Get("arguments").String()); args != "" {
					segments = append(segments, args)
				}
			case "function_call_output":
				if out := strings.TrimSpace(item.Get("output").String()); out != "" {
					segments = append(segments, out)
				}
			default:
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		}
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		tarr := tools.Array()
		for i := range tarr {
			tool := tarr[i]
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				segments = append(segments, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				segments = append(segments, desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				val := params.Raw
				if params.Type == gjson.String {
					val = params.String()
				}
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					segments = append(segments, trimmed)
				}
			}
		}
	}

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		if name := strings.TrimSpace(textFormat.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			val := schema.Raw
			if schema.Type == gjson.String {
				val = schema.String()
			}
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				segments = append(segments, trimmed)
			}
		}
	}

	text := strings.Join(segments, "\n")
	if text == "" {
		return 0, nil
	}

	count, err := enc.Count(text)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := codexauth.NewCodexAuth(e.cfg)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func (e *CodexExecutor) cacheHelper(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte) (*http.Request, error) {
	var cache codexCache
	if from == "claude" {
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
			var ok bool
			if cache, ok = getCodexCache(key); !ok {
				cache = codexCache{
					ID:     uuid.New().String(),
					Expire: time.Now().Add(1 * time.Hour),
				}
				setCodexCache(key, cache)
			}
		}
	} else if from == "openai-response" {
		promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key")
		if promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	}

	if cache.ID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", cache.ID)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawJSON))
	if err != nil {
		return nil, err
	}
	if cache.ID != "" {
		httpReq.Header.Set("Conversation_id", cache.ID)
		httpReq.Header.Set("Session_id", cache.ID)
	}
	return httpReq, nil
}

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	misc.EnsureHeader(r.Header, ginHeaders, "Version", codexClientVersion)
	misc.EnsureHeader(r.Header, ginHeaders, "Openai-Beta", "responses=experimental")
	misc.EnsureHeader(r.Header, ginHeaders, "Session_id", uuid.NewString())
	misc.EnsureHeader(r.Header, ginHeaders, "User-Agent", codexUserAgent)

	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")

	isAPIKey := false
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			isAPIKey = true
		}
	}
	if !isAPIKey {
		r.Header.Set("Originator", "codex_cli_rs")
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				r.Header.Set("Chatgpt-Account-Id", accountID)
			}
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.CodexKey {
		entry := &e.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.CodexKey {
			entry := &e.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}
