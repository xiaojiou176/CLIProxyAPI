// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
	*handlers.BaseAPIHandler
}

const upstreamResponseHeadersContextKey = "UPSTREAM_RESPONSE_HEADERS"

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
	return &OpenAIResponsesAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OpenaiResponse
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.FilterVisibleModels(c, h.Models()),
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

func (h *OpenAIResponsesAPIHandler) Compact(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported for compact responses",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if streamResult.Exists() {
		if updated, err := sjson.DeleteBytes(rawJSON, "stream"); err == nil {
			rawJSON = updated
		}
	}

	c.Header("Content-Type", "application/json")
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "responses/compact")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	applyUpstreamModelHeaders(c)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	resp = rewriteSpawnAgentExplorerInNonStreamingResponse(resp)
	applyUpstreamModelHeaders(c)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Get the http.Flusher interface to manually flush the response.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	// New core execution path
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
		applyUpstreamModelHeaders(c)
	}

	// Peek at the first chunk
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for data channel.
				errChan = nil
				continue
			}
			// Upstream failed immediately. Return proper error status and JSON.
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream closed without data? Send headers and done.
				setSSEHeaders()
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// Success! Set headers.
			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

			// Write first chunk logic (matching forwardResponsesStream)
			chunk = rewriteSpawnAgentExplorerChunk(chunk)
			if bytes.HasPrefix(chunk, []byte("event:")) {
				_, _ = c.Writer.Write([]byte("\n"))
			}
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n"))
			flusher.Flush()

			// Continue
			h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
			return
		}
	}
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			chunk = rewriteSpawnAgentExplorerChunk(chunk)
			if bytes.HasPrefix(chunk, []byte("event:")) {
				_, _ = c.Writer.Write([]byte("\n"))
			}
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n"))
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil {
				return
			}
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			body := handlers.BuildErrorResponseBody(status, errText)
			_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", string(body))
		},
		WriteDone: func() {
			_, _ = c.Writer.Write([]byte("\n"))
		},
	})
}

func rewriteSpawnAgentExplorerChunk(chunk []byte) []byte {
	if len(chunk) == 0 {
		return chunk
	}
	lines := strings.Split(string(chunk), "\n")
	changed := false
	for i, line := range lines {
		idx := strings.Index(line, "data:")
		if idx < 0 {
			continue
		}
		prefix := line[:idx]
		rawData := strings.TrimSpace(line[idx+len("data:"):])
		if rawData == "" || rawData == "[DONE]" || !gjson.Valid(rawData) {
			continue
		}
		rewritten, ok := rewriteSpawnAgentExplorerPayload(rawData)
		if !ok {
			continue
		}
		lines[i] = prefix + "data: " + rewritten
		changed = true
	}
	if !changed {
		return chunk
	}
	return []byte(strings.Join(lines, "\n"))
}

func applyUpstreamModelHeaders(c *gin.Context) {
	if c == nil {
		return
	}
	raw, exists := c.Get(upstreamResponseHeadersContextKey)
	if !exists {
		return
	}
	headers, ok := raw.(http.Header)
	if !ok || headers == nil {
		return
	}
	model := firstHeaderValueCaseInsensitive(headers, "openai-model", "x-openai-model")
	if model == "" {
		return
	}
	c.Header("openai-model", model)
	c.Header("x-openai-model", model)
}

func firstHeaderValueCaseInsensitive(headers http.Header, keys ...string) string {
	if headers == nil {
		return ""
	}
	for _, key := range keys {
		if v := strings.TrimSpace(headers.Get(key)); v != "" {
			return v
		}
		lowerKey := strings.ToLower(key)
		for existingKey, values := range headers {
			if strings.ToLower(existingKey) != lowerKey || len(values) == 0 {
				continue
			}
			if v := strings.TrimSpace(values[0]); v != "" {
				return v
			}
		}
	}
	return ""
}

func rewriteSpawnAgentExplorerPayload(payload string) (string, bool) {
	eventType := gjson.Get(payload, "type").String()
	switch eventType {
	case "response.function_call_arguments.done":
		args := gjson.Get(payload, "arguments").String()
		rewrittenArgs, changed := stripExplorerAgentType(args)
		if !changed {
			return payload, false
		}
		updated, err := sjson.Set(payload, "arguments", rewrittenArgs)
		if err != nil {
			return payload, false
		}
		return updated, true
	case "response.output_item.done":
		toolName := gjson.Get(payload, "item.name").String()
		if !strings.EqualFold(toolName, "spawn_agent") {
			return payload, false
		}
		args := gjson.Get(payload, "item.arguments").String()
		rewrittenArgs, changed := stripExplorerAgentType(args)
		if !changed {
			return payload, false
		}
		updated, err := sjson.Set(payload, "item.arguments", rewrittenArgs)
		if err != nil {
			return payload, false
		}
		return updated, true
	default:
		return payload, false
	}
}

func stripExplorerAgentType(arguments string) (string, bool) {
	raw := strings.TrimSpace(arguments)
	if raw == "" || !gjson.Valid(raw) {
		return arguments, false
	}
	agentType := strings.TrimSpace(gjson.Get(raw, "agent_type").String())
	if !strings.EqualFold(agentType, "explorer") {
		agentType = strings.TrimSpace(gjson.Get(raw, "agentType").String())
	}
	if !strings.EqualFold(agentType, "explorer") {
		return arguments, false
	}
	updated, err := sjson.Delete(raw, "agent_type")
	if err != nil {
		return arguments, false
	}
	updated, err = sjson.Delete(updated, "agentType")
	if err != nil {
		return arguments, false
	}
	return updated, true
}

func rewriteSpawnAgentExplorerInNonStreamingResponse(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	raw := string(body)
	if !gjson.Valid(raw) {
		return body
	}
	output := gjson.Get(raw, "output")
	if !output.Exists() || !output.IsArray() {
		return body
	}
	updated := raw
	changed := false
	output.ForEach(func(key, item gjson.Result) bool {
		if item.Get("type").String() != "function_call" {
			return true
		}
		if !strings.EqualFold(item.Get("name").String(), "spawn_agent") {
			return true
		}
		args := item.Get("arguments").String()
		rewrittenArgs, argsChanged := stripExplorerAgentType(args)
		if !argsChanged {
			return true
		}
		index := int(key.Int())
		path := fmt.Sprintf("output.%d.arguments", index)
		next, err := sjson.Set(updated, path, rewrittenArgs)
		if err != nil {
			return true
		}
		updated = next
		changed = true
		return true
	})
	if !changed {
		return body
	}
	return []byte(updated)
}
