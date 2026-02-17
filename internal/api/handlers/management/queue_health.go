package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/queuehealth"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func (h *Handler) GetQueueHealth(c *gin.Context) {
	eventStreamMetrics := usage.GetEventStream().MetricsSnapshot()
	c.JSON(http.StatusOK, gin.H{
		"queue_health":         queuehealth.SnapshotAll(),
		"usage_stream_metrics": eventStreamMetrics,
	})
}
