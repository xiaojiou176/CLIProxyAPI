package management

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/promptqueue"
)

func (h *Handler) GetPromptQueueMetrics(c *gin.Context) {
	manager := promptqueue.GetDefaultManager()
	c.JSON(http.StatusOK, manager.MetricsSnapshot())
}

func (h *Handler) GetPromptQueueSubmissions(c *gin.Context) {
	manager := promptqueue.GetDefaultManager()
	status := strings.TrimSpace(c.Query("status"))
	sessionKey := strings.TrimSpace(c.Query("session_key"))
	offset := parseNonNegativeInt(c.Query("offset"), 0)
	limit := parseBoundedInt(c.Query("limit"), 200, 1, 1000)

	items := manager.ListSubmissions(promptqueue.ListOptions{
		SessionKey: sessionKey,
		Status:     promptqueue.SubmissionStatus(status),
		Offset:     offset,
		Limit:      limit,
	})
	c.JSON(http.StatusOK, gin.H{
		"submissions": items,
		"limit":       limit,
		"offset":      offset,
	})
}

func (h *Handler) GetPromptQueueEvents(c *gin.Context) {
	manager := promptqueue.GetDefaultManager()
	sessionKey := strings.TrimSpace(c.Query("session_key"))
	sinceSeq := parseInt64(c.Query("since_seq"), 0)
	limit := parseBoundedInt(c.Query("limit"), 500, 1, 5000)
	events := manager.EventsSince(sessionKey, sinceSeq, limit)
	c.JSON(http.StatusOK, gin.H{
		"events":     events,
		"since_seq":  sinceSeq,
		"next_limit": limit,
	})
}

func parseNonNegativeInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func parseBoundedInt(raw string, fallback, minValue, maxValue int) int {
	value := parseNonNegativeInt(raw, fallback)
	if value < minValue {
		value = minValue
	}
	if value > maxValue {
		value = maxValue
	}
	return value
}

func parseInt64(raw string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}
