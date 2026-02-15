// Package handlers provides core API handler functionality for the CLI Proxy API server.
// It includes common types, client management, load balancing, and error handling
// shared across all API endpoint handlers (OpenAI, Claude, Gemini).
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"golang.org/x/net/context"
)

// ErrorResponse represents a standard error response format for the API.
// It contains a single ErrorDetail field.
type ErrorResponse struct {
	// Error contains detailed information about the error that occurred.
	Error ErrorDetail `json:"error"`
}

// ErrorDetail provides specific information about an error that occurred.
// It includes a human-readable message, an error type, and an optional error code.
type ErrorDetail struct {
	// Message is a human-readable message providing more details about the error.
	Message string `json:"message"`

	// Type is the category of error that occurred (e.g., "invalid_request_error").
	Type string `json:"type"`

	// Code is a short code identifying the error, if applicable.
	Code string `json:"code,omitempty"`
}

const idempotencyKeyMetadataKey = "idempotency_key"

const (
	defaultStreamingKeepAliveSeconds = 0
	defaultStreamingBootstrapRetries = 0
	defaultModelVisibilityNamespace  = "default"
)

var modelVisibilityNamespaceHeaders = []string{
	"X-Model-Namespace",
	"X-Base-URL-Namespace",
	"X-Namespace",
}

var modelVisibilityNamespaceQueryParams = []string{
	"namespace",
	"model_namespace",
}

// BuildErrorResponseBody builds an OpenAI-compatible JSON error response body.
// If errText is already valid JSON, it is returned as-is to preserve upstream error payloads.
func BuildErrorResponseBody(status int, errText string) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(errText) == "" {
		errText = http.StatusText(status)
	}

	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		return []byte(trimmed)
	}

	errType := "invalid_request_error"
	var code string
	switch status {
	case http.StatusUnauthorized:
		errType = "authentication_error"
		code = "invalid_api_key"
	case http.StatusForbidden:
		errType = "permission_error"
		code = "insufficient_quota"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
		code = "rate_limit_exceeded"
	case http.StatusNotFound:
		errType = "invalid_request_error"
		code = "model_not_found"
	default:
		if status >= http.StatusInternalServerError {
			errType = "server_error"
			code = "internal_server_error"
		}
	}

	payload, err := json.Marshal(ErrorResponse{
		Error: ErrorDetail{
			Message: errText,
			Type:    errType,
			Code:    code,
		},
	})
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"server_error","code":"internal_server_error"}}`, errText))
	}
	return payload
}

// StreamingKeepAliveInterval returns the SSE keep-alive interval for this server.
// Returning 0 disables keep-alives (default when unset).
func StreamingKeepAliveInterval(cfg *config.SDKConfig) time.Duration {
	seconds := defaultStreamingKeepAliveSeconds
	if cfg != nil {
		seconds = cfg.Streaming.KeepAliveSeconds
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// NonStreamingKeepAliveInterval returns the keep-alive interval for non-streaming responses.
// Returning 0 disables keep-alives (default when unset).
func NonStreamingKeepAliveInterval(cfg *config.SDKConfig) time.Duration {
	seconds := 0
	if cfg != nil {
		seconds = cfg.NonStreamKeepAliveInterval
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// StreamingBootstrapRetries returns how many times a streaming request may be retried before any bytes are sent.
func StreamingBootstrapRetries(cfg *config.SDKConfig) int {
	retries := defaultStreamingBootstrapRetries
	if cfg != nil {
		retries = cfg.Streaming.BootstrapRetries
	}
	if retries < 0 {
		retries = 0
	}
	return retries
}

func requestExecutionMetadata(ctx context.Context, rawJSON []byte) map[string]any {
	// Idempotency-Key is an optional client-supplied header used to correlate retries.
	// It is forwarded as execution metadata; when absent we generate a UUID.
	key := ""
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			key = strings.TrimSpace(ginCtx.GetHeader("Idempotency-Key"))
		}
	}
	if key == "" {
		key = uuid.NewString()
	}
	meta := map[string]any{idempotencyKeyMetadataKey: key}
	if sessionKey := deriveSessionAffinityKey(ctx, rawJSON); sessionKey != "" {
		meta[coreexecutor.SessionAffinityKeyMetadataKey] = sessionKey
	}
	return meta
}

func deriveSessionAffinityKey(ctx context.Context, rawJSON []byte) string {
	headerCandidates := []string{
		"session_id",
		"Session_id",
		"x-session-id",
		"x-codex-session-id",
		"x-jarvis-run-id",
		"x-jarvis-task-id",
		"conversation_id",
		"Conversation_id",
	}
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			for _, header := range headerCandidates {
				if val := strings.TrimSpace(ginCtx.GetHeader(header)); val != "" {
					return val
				}
			}
		}
	}

	if len(rawJSON) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return ""
	}
	payloadCandidates := []string{
		"session_id",
		"sessionId",
		"conversation_id",
		"conversationId",
		"prompt_cache_key",
		"previous_response_id",
		"response_id",
		"thread_id",
		"run_id",
	}
	for _, key := range payloadCandidates {
		if raw, ok := payload[key]; ok {
			if str, ok := raw.(string); ok {
				if trimmed := strings.TrimSpace(str); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

// BaseAPIHandler contains the handlers for API endpoints.
// It holds a pool of clients to interact with the backend service and manages
// load balancing, client selection, and configuration.
type BaseAPIHandler struct {
	// AuthManager manages auth lifecycle and execution in the new architecture.
	AuthManager *coreauth.Manager

	// Cfg holds the current application configuration.
	Cfg *config.SDKConfig
}

// NewBaseAPIHandlers creates a new API handlers instance.
// It takes a slice of clients and configuration as input.
//
// Parameters:
//   - cliClients: A slice of AI service clients
//   - cfg: The application configuration
//
// Returns:
//   - *BaseAPIHandler: A new API handlers instance
func NewBaseAPIHandlers(cfg *config.SDKConfig, authManager *coreauth.Manager) *BaseAPIHandler {
	return &BaseAPIHandler{
		Cfg:         cfg,
		AuthManager: authManager,
	}
}

// UpdateClients updates the handlers' client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (h *BaseAPIHandler) UpdateClients(cfg *config.SDKConfig) { h.Cfg = cfg }

// GetAlt extracts the 'alt' parameter from the request query string.
// It checks both 'alt' and '$alt' parameters and returns the appropriate value.
//
// Parameters:
//   - c: The Gin context containing the HTTP request
//
// Returns:
//   - string: The alt parameter value, or empty string if it's "sse"
func (h *BaseAPIHandler) GetAlt(c *gin.Context) string {
	var alt string
	var hasAlt bool
	alt, hasAlt = c.GetQuery("alt")
	if !hasAlt {
		alt, _ = c.GetQuery("$alt")
	}
	if alt == "sse" {
		return ""
	}
	return alt
}

// GetContextWithCancel creates a new context with cancellation capabilities.
// It embeds the Gin context and the API handler into the new context for later use.
// The returned cancel function also handles logging the API response if request logging is enabled.
//
// Parameters:
//   - handler: The API handler associated with the request.
//   - c: The Gin context of the current request.
//   - ctx: The parent context (caller values/deadlines are preserved; request context adds cancellation and request ID).
//
// Returns:
//   - context.Context: The new context with cancellation and embedded values.
//   - APIHandlerCancelFunc: A function to cancel the context and log the response.
func (h *BaseAPIHandler) GetContextWithCancel(handler interfaces.APIHandler, c *gin.Context, ctx context.Context) (context.Context, APIHandlerCancelFunc) {
	parentCtx := ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	var requestCtx context.Context
	if c != nil && c.Request != nil {
		requestCtx = c.Request.Context()
	}

	if requestCtx != nil && logging.GetRequestID(parentCtx) == "" {
		if requestID := logging.GetRequestID(requestCtx); requestID != "" {
			parentCtx = logging.WithRequestID(parentCtx, requestID)
		} else if requestID := logging.GetGinRequestID(c); requestID != "" {
			parentCtx = logging.WithRequestID(parentCtx, requestID)
		}
	}
	newCtx, cancel := context.WithCancel(parentCtx)
	if requestCtx != nil && requestCtx != parentCtx {
		go func() {
			select {
			case <-requestCtx.Done():
				cancel()
			case <-newCtx.Done():
			}
		}()
	}
	newCtx = context.WithValue(newCtx, "gin", c)
	newCtx = context.WithValue(newCtx, "handler", handler)
	return newCtx, func(params ...interface{}) {
		if h.Cfg.RequestLog && len(params) == 1 {
			if existing, exists := c.Get("API_RESPONSE"); exists {
				if existingBytes, ok := existing.([]byte); ok && len(bytes.TrimSpace(existingBytes)) > 0 {
					switch params[0].(type) {
					case error, string:
						cancel()
						return
					}
				}
			}

			var payload []byte
			switch data := params[0].(type) {
			case []byte:
				payload = data
			case error:
				if data != nil {
					payload = []byte(data.Error())
				}
			case string:
				payload = []byte(data)
			}
			if len(payload) > 0 {
				if existing, exists := c.Get("API_RESPONSE"); exists {
					if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
						trimmedPayload := bytes.TrimSpace(payload)
						if len(trimmedPayload) > 0 && bytes.Contains(existingBytes, trimmedPayload) {
							cancel()
							return
						}
					}
				}
				appendAPIResponse(c, payload)
			}
		}

		cancel()
	}
}

// StartNonStreamingKeepAlive emits blank lines every 5 seconds while waiting for a non-streaming response.
// It returns a stop function that must be called before writing the final response.
func (h *BaseAPIHandler) StartNonStreamingKeepAlive(c *gin.Context, ctx context.Context) func() {
	if h == nil || c == nil {
		return func() {}
	}
	interval := NonStreamingKeepAliveInterval(h.Cfg)
	if interval <= 0 {
		return func() {}
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	stopChan := make(chan struct{})
	var stopOnce sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
		wg.Wait()
	}
}

// appendAPIResponse preserves any previously captured API response and appends new data.
func appendAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}

	// Capture timestamp on first API response
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); !exists {
		c.Set("API_RESPONSE_TIMESTAMP", time.Now())
	}

	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+1)
			combined = append(combined, existingBytes...)
			if existingBytes[len(existingBytes)-1] != '\n' {
				combined = append(combined, '\n')
			}
			combined = append(combined, data...)
			c.Set("API_RESPONSE", combined)
			return
		}
	}

	c.Set("API_RESPONSE", bytes.Clone(data))
}

// ExecuteWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	if errMsg := h.enforceModelVisibility(ctx, modelName); errMsg != nil {
		return nil, errMsg
	}
	providers, normalizedModel, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	reqMeta := requestExecutionMetadata(ctx, rawJSON)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = normalizedModel
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	opts.Metadata = reqMeta
	resp, err := h.AuthManager.Execute(ctx, providers, req, opts)
	if err != nil {
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	return resp.Payload, nil
}

// ExecuteCountWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	if errMsg := h.enforceModelVisibility(ctx, modelName); errMsg != nil {
		return nil, errMsg
	}
	providers, normalizedModel, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	reqMeta := requestExecutionMetadata(ctx, rawJSON)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = normalizedModel
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	opts.Metadata = reqMeta
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	return resp.Payload, nil
}

// ExecuteStreamWithAuthManager executes a streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	if errMsg := h.enforceModelVisibility(ctx, modelName); errMsg != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, errChan
	}
	providers, normalizedModel, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, errChan
	}
	reqMeta := requestExecutionMetadata(ctx, rawJSON)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = normalizedModel
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          true,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	opts.Metadata = reqMeta
	chunks, err := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
	if err != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
		close(errChan)
		return nil, errChan
	}
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		defer close(dataChan)
		defer close(errChan)
		sentPayload := false
		bootstrapRetries := 0
		maxBootstrapRetries := StreamingBootstrapRetries(h.Cfg)

		sendErr := func(msg *interfaces.ErrorMessage) bool {
			if ctx == nil {
				errChan <- msg
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case errChan <- msg:
				return true
			}
		}

		sendData := func(chunk []byte) bool {
			if ctx == nil {
				dataChan <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case dataChan <- chunk:
				return true
			}
		}

		bootstrapEligible := func(err error) bool {
			status := statusFromError(err)
			if status == 0 {
				return true
			}
			switch status {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired,
				http.StatusRequestTimeout, http.StatusTooManyRequests:
				return true
			default:
				return status >= http.StatusInternalServerError
			}
		}

	outer:
		for {
			for {
				var chunk coreexecutor.StreamChunk
				var ok bool
				if ctx != nil {
					select {
					case <-ctx.Done():
						return
					case chunk, ok = <-chunks:
					}
				} else {
					chunk, ok = <-chunks
				}
				if !ok {
					return
				}
				if chunk.Err != nil {
					streamErr := chunk.Err
					// Safe bootstrap recovery: if the upstream fails before any payload bytes are sent,
					// retry a few times (to allow auth rotation / transient recovery) and then attempt model fallback.
					if !sentPayload {
						if bootstrapRetries < maxBootstrapRetries && bootstrapEligible(streamErr) {
							bootstrapRetries++
							retryChunks, retryErr := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
							if retryErr == nil {
								chunks = retryChunks
								continue outer
							}
							streamErr = retryErr
						}
					}

					status := http.StatusInternalServerError
					if se, ok := streamErr.(interface{ StatusCode() int }); ok && se != nil {
						if code := se.StatusCode(); code > 0 {
							status = code
						}
					}
					var addon http.Header
					if he, ok := streamErr.(interface{ Headers() http.Header }); ok && he != nil {
						if hdr := he.Headers(); hdr != nil {
							addon = hdr.Clone()
						}
					}
					_ = sendErr(&interfaces.ErrorMessage{StatusCode: status, Error: streamErr, Addon: addon})
					return
				}
				if len(chunk.Payload) > 0 {
					sentPayload = true
					if okSendData := sendData(cloneBytes(chunk.Payload)); !okSendData {
						return
					}
				}
			}
		}
	}()
	return dataChan, errChan
}

func statusFromError(err error) int {
	if err == nil {
		return 0
	}
	if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
		if code := se.StatusCode(); code > 0 {
			return code
		}
	}
	return 0
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	resolvedModelName := modelName
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
		if initialSuffix.HasSuffix {
			resolvedModelName = fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
		} else {
			resolvedModelName = resolvedBase
		}
	} else {
		resolvedModelName = util.ResolveAutoModel(modelName)
	}

	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	providers = util.GetProviderName(baseModel)
	// Fallback: if baseModel has no provider but differs from resolvedModelName,
	// try using the full model name. This handles edge cases where custom models
	// may be registered with their full suffixed name (e.g., "my-model(8192)").
	// Evaluated in Story 11.8: This fallback is intentionally preserved to support
	// custom model registrations that include thinking suffixes.
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}

	if len(providers) == 0 {
		return nil, "", &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("unknown provider for model %s", modelName)}
	}
	providers, routeErr := h.filterProvidersByModelProviderRouting(resolvedModelName, baseModel, providers)
	if routeErr != nil {
		return nil, "", routeErr
	}

	// The thinking suffix is preserved in the model name itself, so no
	// metadata-based configuration passing is needed.
	return providers, resolvedModelName, nil
}

func (h *BaseAPIHandler) filterProvidersByModelProviderRouting(modelName, baseModel string, providers []string) ([]string, *interfaces.ErrorMessage) {
	if h == nil || h.Cfg == nil || !h.Cfg.ModelProviderRouting.Enabled {
		return providers, nil
	}
	if len(providers) == 0 {
		return providers, nil
	}

	allowlist := h.Cfg.ModelProviderRouting.FamilyProviderAllowlist
	if len(allowlist) == 0 {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusForbidden,
			Error:      fmt.Errorf("model-provider-routing is enabled but family-provider-allowlist is empty"),
		}
	}

	familyCandidates := modelProviderRoutingFamilyCandidates(baseModel, providers)
	familyKey, allowedProviders := lookupModelProviderAllowlist(allowlist, familyCandidates)
	if len(allowedProviders) == 0 {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusForbidden,
			Error: fmt.Errorf(
				"model %q is blocked by model-provider-routing: no allowed providers configured for family candidates %v",
				strings.TrimSpace(modelName),
				familyCandidates,
			),
		}
	}

	filtered := filterProvidersByAllowlist(providers, allowedProviders)
	if len(filtered) == 0 {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusForbidden,
			Error: fmt.Errorf(
				"model %q is blocked by model-provider-routing: family %q allows %v but resolved providers are %v",
				strings.TrimSpace(modelName),
				familyKey,
				allowedProviders,
				providers,
			),
		}
	}

	return filtered, nil
}

func lookupModelProviderAllowlist(allowlist map[string][]string, familyCandidates []string) (familyKey string, providers []string) {
	if len(allowlist) == 0 || len(familyCandidates) == 0 {
		return "", nil
	}
	for _, candidate := range familyCandidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		if allowed, ok := allowlist[key]; ok && len(allowed) > 0 {
			return key, allowed
		}
		for rawFamily, allowed := range allowlist {
			if strings.EqualFold(strings.TrimSpace(rawFamily), key) && len(allowed) > 0 {
				return strings.ToLower(strings.TrimSpace(rawFamily)), allowed
			}
		}
	}
	return "", nil
}

func filterProvidersByAllowlist(providers []string, allowedProviders []string) []string {
	if len(providers) == 0 || len(allowedProviders) == 0 {
		return nil
	}
	allowedSet := make(map[string]struct{}, len(allowedProviders))
	for _, raw := range allowedProviders {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		allowedSet[key] = struct{}{}
	}
	if len(allowedSet) == 0 {
		return nil
	}

	filtered := make([]string, 0, len(providers))
	for _, provider := range providers {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" {
			continue
		}
		if _, ok := allowedSet[key]; ok {
			filtered = append(filtered, provider)
		}
	}
	return filtered
}

func modelProviderRoutingFamilyCandidates(baseModel string, providers []string) []string {
	seen := make(map[string]struct{}, len(providers)+2)
	out := make([]string, 0, len(providers)+2)
	add := func(raw string) {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}

	add(modelFamilyFromModelName(baseModel))
	for _, provider := range providers {
		add(provider)
		add(modelFamilyFromProvider(provider))
	}
	return out
}

func modelFamilyFromProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "openai":
		return "codex"
	case "claude":
		return "claude"
	case "gemini", "gemini-cli", "vertex", "aistudio":
		return "gemini"
	default:
		return ""
	}
}

func modelFamilyFromModelName(modelName string) string {
	normalized := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName))
	if normalized == "" {
		return ""
	}
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 && idx < len(normalized)-1 {
		normalized = normalized[idx+1:]
	}

	switch {
	case strings.HasPrefix(normalized, "gpt-"),
		strings.HasPrefix(normalized, "o1"),
		strings.HasPrefix(normalized, "o3"),
		strings.HasPrefix(normalized, "chatgpt"),
		strings.Contains(normalized, "codex"):
		return "codex"
	case strings.HasPrefix(normalized, "claude"):
		return "claude"
	case strings.HasPrefix(normalized, "gemini"):
		return "gemini"
	case strings.HasPrefix(normalized, "qwen"):
		return "qwen"
	case strings.HasPrefix(normalized, "kimi"):
		return "kimi"
	case strings.HasPrefix(normalized, "glm"), strings.HasPrefix(normalized, "iflow"):
		return "iflow"
	default:
		return ""
	}
}

func (h *BaseAPIHandler) enforceModelVisibility(ctx context.Context, modelName string) *interfaces.ErrorMessage {
	allowlist, guardEnabled, namespace := h.modelVisibilityAllowlistFromContext(ctx)
	if !guardEnabled {
		return nil
	}

	if len(allowlist) == 0 {
		targetNamespace := strings.TrimSpace(namespace)
		if targetNamespace == "" {
			targetNamespace = defaultModelVisibilityNamespace
		}
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusForbidden,
			Error:      fmt.Errorf("model visibility namespace %q has no allowed models configured", targetNamespace),
		}
	}

	if modelVisibilityModelAllowed(modelName, allowlist) {
		return nil
	}

	targetNamespace := strings.TrimSpace(namespace)
	if targetNamespace == "" {
		targetNamespace = defaultModelVisibilityNamespace
	}
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusForbidden,
		Error:      fmt.Errorf("model %q is not allowed for namespace %q", strings.TrimSpace(modelName), targetNamespace),
	}
}

// ValidateModelVisibility validates whether the requested model is visible in the request namespace.
// It returns nil when model visibility guard is disabled or when the model is allowed.
func (h *BaseAPIHandler) ValidateModelVisibility(c *gin.Context, modelName string) *interfaces.ErrorMessage {
	ctx := context.Background()
	if c != nil {
		ctx = context.WithValue(ctx, "gin", c)
	}
	return h.enforceModelVisibility(ctx, modelName)
}

// FilterVisibleModels applies model-visibility allowlist filtering to model metadata.
// When model visibility is disabled or not configured, models are returned unchanged.
func (h *BaseAPIHandler) FilterVisibleModels(c *gin.Context, models []map[string]any) []map[string]any {
	if len(models) == 0 {
		return models
	}
	allowlist, guardEnabled, _ := h.modelVisibilityAllowlist(c)
	if !guardEnabled {
		return models
	}
	if len(allowlist) == 0 {
		return []map[string]any{}
	}

	filtered := make([]map[string]any, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID, _ := model["id"].(string)
		if modelID == "" {
			if fallbackName, ok := model["name"].(string); ok {
				modelID = fallbackName
			}
		}
		if modelVisibilityModelAllowed(modelID, allowlist) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func (h *BaseAPIHandler) modelVisibilityAllowlistFromContext(ctx context.Context) (map[string]struct{}, bool, string) {
	if ctx == nil {
		return h.modelVisibilityAllowlist(nil)
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	return h.modelVisibilityAllowlist(ginCtx)
}

func (h *BaseAPIHandler) modelVisibilityAllowlist(c *gin.Context) (map[string]struct{}, bool, string) {
	if h == nil || h.Cfg == nil || !h.Cfg.ModelVisibility.Enabled || len(h.Cfg.ModelVisibility.Namespaces) == 0 {
		return nil, false, ""
	}

	namespace := strings.TrimSpace(h.resolveModelVisibilityNamespace(c))
	if namespace == "" {
		if _, ok := h.Cfg.ModelVisibility.Namespaces[defaultModelVisibilityNamespace]; ok {
			namespace = defaultModelVisibilityNamespace
		} else if len(h.Cfg.ModelVisibility.Namespaces) == 1 {
			for key := range h.Cfg.ModelVisibility.Namespaces {
				namespace = strings.TrimSpace(key)
				break
			}
		}
	}

	if namespace == "" {
		return nil, true, ""
	}

	models, ok := lookupModelVisibilityNamespaceModels(h.Cfg.ModelVisibility.Namespaces, namespace)
	if !ok {
		return nil, true, namespace
	}

	allowlist := make(map[string]struct{}, len(models))
	for _, model := range models {
		key := strings.ToLower(strings.TrimSpace(model))
		if key == "" {
			continue
		}
		allowlist[key] = struct{}{}
	}
	if len(allowlist) == 0 {
		return nil, true, namespace
	}
	return allowlist, true, namespace
}

func (h *BaseAPIHandler) resolveModelVisibilityNamespace(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if namespace := h.resolveModelVisibilityNamespaceByHost(c); namespace != "" {
		return namespace
	}

	for _, headerName := range modelVisibilityNamespaceHeaders {
		if namespace := strings.TrimSpace(c.GetHeader(headerName)); namespace != "" {
			return namespace
		}
	}
	for _, queryKey := range modelVisibilityNamespaceQueryParams {
		if namespace := strings.TrimSpace(c.Query(queryKey)); namespace != "" {
			return namespace
		}
	}
	return ""
}

func (h *BaseAPIHandler) resolveModelVisibilityNamespaceByHost(c *gin.Context) string {
	if h == nil || h.Cfg == nil || len(h.Cfg.ModelVisibility.HostNamespaces) == 0 || c == nil {
		return ""
	}
	return lookupModelVisibilityHostNamespace(h.Cfg.ModelVisibility.HostNamespaces, modelVisibilityHostCandidates(c))
}

func lookupModelVisibilityHostNamespace(hostNamespaces map[string]string, hostCandidates []string) string {
	if len(hostNamespaces) == 0 || len(hostCandidates) == 0 {
		return ""
	}

	normalizedMap := make(map[string]string, len(hostNamespaces))
	for rawHost, namespace := range hostNamespaces {
		hostKey := normalizeModelVisibilityHost(rawHost)
		ns := strings.TrimSpace(namespace)
		if hostKey == "" || ns == "" {
			continue
		}
		normalizedMap[hostKey] = ns
	}
	if len(normalizedMap) == 0 {
		return ""
	}

	for _, candidate := range hostCandidates {
		hostKey := normalizeModelVisibilityHost(candidate)
		if hostKey == "" {
			continue
		}
		if namespace, ok := normalizedMap[hostKey]; ok {
			return namespace
		}
	}
	return ""
}

func modelVisibilityHostCandidates(c *gin.Context) []string {
	if c == nil {
		return nil
	}
	candidates := make([]string, 0, 4)
	add := func(raw string) {
		host := strings.TrimSpace(raw)
		if host == "" {
			return
		}
		// X-Forwarded-Host may contain a comma-separated chain.
		if idx := strings.Index(host, ","); idx >= 0 {
			host = strings.TrimSpace(host[:idx])
		}
		if host != "" {
			candidates = append(candidates, host)
		}
	}
	if c.Request != nil {
		add(c.Request.Host)
	}
	add(c.GetHeader("X-Forwarded-Host"))
	add(c.GetHeader("X-Original-Host"))
	add(c.GetHeader("Host"))
	return candidates
}

func normalizeModelVisibilityHost(rawHost string) string {
	host := strings.TrimSpace(strings.ToLower(rawHost))
	if host == "" {
		return ""
	}
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	if parsedHost, port, err := net.SplitHostPort(host); err == nil {
		if port != "" {
			host = parsedHost
		}
	}
	host = strings.Trim(host, "[]")
	return strings.TrimSpace(strings.ToLower(host))
}

func lookupModelVisibilityNamespaceModels(namespaces map[string][]string, namespace string) ([]string, bool) {
	if len(namespaces) == 0 {
		return nil, false
	}
	if models, ok := namespaces[namespace]; ok {
		return models, true
	}
	for key, models := range namespaces {
		if strings.EqualFold(strings.TrimSpace(key), namespace) {
			return models, true
		}
	}
	return nil, false
}

func modelVisibilityModelAllowed(modelName string, allowlist map[string]struct{}) bool {
	if len(allowlist) == 0 {
		return false
	}
	candidates := modelVisibilityCandidates(modelName)
	for _, candidate := range candidates {
		if _, ok := allowlist[candidate]; ok {
			return true
		}
	}
	return false
}

func modelVisibilityCandidates(modelName string) []string {
	seen := make(map[string]struct{}, 3)
	out := make([]string, 0, 3)
	add := func(v string) {
		key := strings.ToLower(strings.TrimSpace(v))
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}

	trimmedModel := strings.TrimSpace(modelName)
	add(trimmedModel)

	resolvedModel := util.ResolveAutoModel(trimmedModel)
	add(resolvedModel)

	parsed := thinking.ParseSuffix(resolvedModel)
	add(parsed.ModelName)

	return out
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

// WriteErrorResponse writes an error message to the response writer using the HTTP status embedded in the message.
func (h *BaseAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && msg.Addon != nil {
		for key, values := range msg.Addon {
			if len(values) == 0 {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
	}

	errText := http.StatusText(status)
	if msg != nil && msg.Error != nil {
		if v := strings.TrimSpace(msg.Error.Error()); v != "" {
			errText = v
		}
	}

	body := BuildErrorResponseBody(status, errText)
	// Append first to preserve upstream response logs, then drop duplicate payloads if already recorded.
	var previous []byte
	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			previous = existingBytes
		}
	}
	appendAPIResponse(c, body)
	trimmedErrText := strings.TrimSpace(errText)
	trimmedBody := bytes.TrimSpace(body)
	if len(previous) > 0 {
		if (trimmedErrText != "" && bytes.Contains(previous, []byte(trimmedErrText))) ||
			(len(trimmedBody) > 0 && bytes.Contains(previous, trimmedBody)) {
			c.Set("API_RESPONSE", previous)
		}
	}

	if !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Status(status)
	_, _ = c.Writer.Write(body)
}

func (h *BaseAPIHandler) LoggingAPIResponseError(ctx context.Context, err *interfaces.ErrorMessage) {
	if h.Cfg.RequestLog {
		if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
			if apiResponseErrors, isExist := ginContext.Get("API_RESPONSE_ERROR"); isExist {
				if slicesAPIResponseError, isOk := apiResponseErrors.([]*interfaces.ErrorMessage); isOk {
					slicesAPIResponseError = append(slicesAPIResponseError, err)
					ginContext.Set("API_RESPONSE_ERROR", slicesAPIResponseError)
				}
			} else {
				// Create new response data entry
				ginContext.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{err})
			}
		}
	}
}

// APIHandlerCancelFunc is a function type for canceling an API handler's context.
// It can optionally accept parameters, which are used for logging the response.
type APIHandlerCancelFunc func(params ...interface{})
