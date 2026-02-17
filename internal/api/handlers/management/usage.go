package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	log "github.com/sirupsen/logrus"
)

type usageExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// ExportUsageStatistics returns a complete usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
}

// ImportUsageStatistics merges a previously exported usage snapshot into memory.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage statistics unavailable"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	result := h.usageStats.MergeSnapshot(payload.Usage)
	snapshot := h.usageStats.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	})
}

// StreamUsageEvents provides a Server-Sent Events stream for real-time usage monitoring.
// Clients can subscribe to receive live request events as they occur.
//
// Event types:
//   - request: A normal API request was processed
//   - quota_exceeded: An account's quota was exceeded
//   - error: An error occurred during request processing
func (h *Handler) StreamUsageEvents(c *gin.Context) {
	eventStream := usage.GetEventStream()
	if eventStream == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "event stream not available"})
		return
	}

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // Disable buffering in nginx

	// Subscribe to events
	subID, events := eventStream.Subscribe()
	defer eventStream.Unsubscribe(subID)

	log.WithField("subscriber_id", subID).Info("SSE client connected for usage events")

	sinceSeq := parseSinceSeq(c)

	// Send initial connection event
	initialEvent := usage.RequestEvent{
		Type:      "connected",
		Timestamp: time.Now(),
		Seq:       eventStream.CurrentSeq(),
	}
	c.Writer.Write(usage.EventToSSE(initialEvent))
	c.Writer.Flush()

	lastSeq := sinceSeq
	if sinceSeq > 0 {
		replay := eventStream.ReplaySince(sinceSeq, 2000)
		for i := 0; i < len(replay); i++ {
			event := replay[i]
			if event.Seq > lastSeq {
				lastSeq = event.Seq
			}
			c.Writer.Write(usage.EventToSSE(event))
		}
		c.Writer.Flush()
	}

	// Stream events
	clientGone := c.Request.Context().Done()
	heartbeatTicker := time.NewTicker(20 * time.Second)
	defer heartbeatTicker.Stop()
	for {
		select {
		case <-clientGone:
			log.WithField("subscriber_id", subID).Info("SSE client disconnected")
			return
		case <-heartbeatTicker.C:
			heartbeat := usage.RequestEvent{
				Type:      "heartbeat",
				Timestamp: time.Now().UTC(),
			}
			c.Writer.Write(usage.EventToSSE(heartbeat))
			c.Writer.Flush()
		case event, ok := <-events:
			if !ok {
				return // Channel closed
			}
			if event.Seq > lastSeq+1 {
				missing := eventStream.ReplaySince(lastSeq, 2000)
				replayedAny := false
				for i := 0; i < len(missing); i++ {
					candidate := missing[i]
					if candidate.Seq <= lastSeq {
						continue
					}
					if candidate.Seq >= event.Seq {
						break
					}
					c.Writer.Write(usage.EventToSSE(candidate))
					lastSeq = candidate.Seq
					replayedAny = true
				}
				if !replayedAny {
					gapEvent := usage.RequestEvent{
						Type:      "replay_gap",
						Timestamp: time.Now().UTC(),
						Provider:  "",
						Model:     "",
						AuthFile:  "",
						Success:   false,
						Error:     fmt.Sprintf("missing seq range [%d,%d]", lastSeq+1, event.Seq-1),
					}
					c.Writer.Write(usage.EventToSSE(gapEvent))
				}
			}
			c.Writer.Write(usage.EventToSSE(event))
			if event.Seq > lastSeq {
				lastSeq = event.Seq
			}
			c.Writer.Flush()
		}
	}
}

func (h *Handler) GetUsageEvents(c *gin.Context) {
	eventStream := usage.GetEventStream()
	if eventStream == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "event stream not available"})
		return
	}
	sinceSeq := parseSinceSeq(c)
	limit := 500
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			if parsed > 5000 {
				parsed = 5000
			}
			limit = parsed
		}
	}
	events := eventStream.ReplaySince(sinceSeq, limit)
	c.JSON(http.StatusOK, gin.H{
		"events":  events,
		"metrics": eventStream.MetricsSnapshot(),
	})
}

func parseSinceSeq(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	if raw := c.Query("since_seq"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 0 {
			return parsed
		}
	}
	if raw := c.GetHeader("Last-Event-ID"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return 0
}

// GetRequestHistory returns recent request details with pagination.
// Query params:
//   - limit: Max number of requests to return (default 100, max 1000)
//   - offset: Number of requests to skip (default 0)
//   - model: Filter by model name (optional)
//   - provider: Filter by provider (optional)
//   - success: Filter by success status (optional, "true" or "false")
func (h *Handler) GetRequestHistory(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusOK, gin.H{"requests": []interface{}{}, "total": 0})
		return
	}

	// Parse query params
	limit := 100
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
		if limit > 1000 {
			limit = 1000
		}
		if limit < 1 {
			limit = 1
		}
	}

	offset := 0
	if o := c.Query("offset"); o != "" {
		fmt.Sscanf(o, "%d", &offset)
		if offset < 0 {
			offset = 0
		}
	}

	filterModel := c.Query("model")
	filterProvider := c.Query("provider")
	filterSuccess := c.Query("success")
	filterRequestID := c.Query("request_id")
	filterAuthIndex := c.Query("auth_index")
	filterSource := c.Query("source")

	snapshot := h.usageStats.Snapshot()

	// Collect all request details
	var allRequests []RequestHistoryItem
	for apiKey, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				// Apply filters
				if filterModel != "" && modelName != filterModel {
					continue
				}
				if filterProvider != "" && !matchesProvider(apiKey, filterProvider) {
					continue
				}
				if filterSuccess != "" {
					wantSuccess := filterSuccess == "true"
					if wantSuccess != !detail.Failed {
						continue
					}
				}
				if filterRequestID != "" && detail.RequestID != filterRequestID {
					continue
				}
				if filterAuthIndex != "" && detail.AuthIndex != filterAuthIndex {
					continue
				}
				if filterSource != "" && detail.Source != filterSource {
					continue
				}

				allRequests = append(allRequests, RequestHistoryItem{
					Timestamp: detail.Timestamp,
					RequestID: detail.RequestID,
					APIKey:    apiKey,
					Model:     modelName,
					AuthIndex: detail.AuthIndex,
					Source:    detail.Source,
					Success:   !detail.Failed,
					Tokens:    detail.Tokens.TotalTokens,
				})
			}
		}
	}

	// Sort by timestamp descending (most recent first)
	sortRequestsByTimestamp(allRequests)

	total := len(allRequests)

	// Apply pagination
	if offset >= len(allRequests) {
		allRequests = []RequestHistoryItem{}
	} else {
		end := offset + limit
		if end > len(allRequests) {
			end = len(allRequests)
		}
		allRequests = allRequests[offset:end]
	}

	c.JSON(http.StatusOK, gin.H{
		"requests": allRequests,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

func matchesProvider(apiKey, provider string) bool {
	// Simple heuristic: check if the API key starts with the provider name
	return len(apiKey) >= len(provider) && apiKey[:len(provider)] == provider
}

// RequestHistoryItem represents a single request in the history
type RequestHistoryItem struct {
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id,omitempty"`
	APIKey    string    `json:"api_key"`
	Model     string    `json:"model"`
	AuthIndex string    `json:"auth_index"`
	Source    string    `json:"source"`
	Success   bool      `json:"success"`
	Tokens    int64     `json:"tokens"`
}

func sortRequestsByTimestamp(requests []RequestHistoryItem) {
	// Sort by timestamp descending (bubble sort for simplicity)
	for i := 0; i < len(requests)-1; i++ {
		for j := i + 1; j < len(requests); j++ {
			if requests[j].Timestamp.After(requests[i].Timestamp) {
				requests[i], requests[j] = requests[j], requests[i]
			}
		}
	}
}
