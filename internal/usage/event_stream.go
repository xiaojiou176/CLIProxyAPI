// Package usage provides SSE event streaming for real-time usage monitoring.
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// RequestEvent represents a single request event for SSE streaming.
type RequestEvent struct {
	Type      string    `json:"type"`      // "request" | "quota_exceeded" | "error"
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id,omitempty"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	AuthFile  string    `json:"auth_file"`
	Source    string    `json:"source,omitempty"`
	Success   bool      `json:"success"`
	Tokens    int64     `json:"tokens"`
	Latency   int64     `json:"latency_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// EventStreamManager manages SSE subscribers for real-time usage events.
type EventStreamManager struct {
	mu          sync.RWMutex
	subscribers map[string]chan RequestEvent
	nextID      int64
}

var defaultEventStream = NewEventStreamManager()

// GetEventStream returns the shared event stream manager.
func GetEventStream() *EventStreamManager { return defaultEventStream }

// NewEventStreamManager creates a new event stream manager.
func NewEventStreamManager() *EventStreamManager {
	return &EventStreamManager{
		subscribers: make(map[string]chan RequestEvent),
	}
}

// Subscribe adds a new subscriber and returns their event channel.
// The caller must call Unsubscribe when done.
func (m *EventStreamManager) Subscribe() (id string, events <-chan RequestEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	id = fmt.Sprintf("sub-%d", m.nextID)
	ch := make(chan RequestEvent, 100) // Buffer to avoid blocking
	m.subscribers[id] = ch
	return id, ch
}

// Unsubscribe removes a subscriber.
func (m *EventStreamManager) Unsubscribe(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ch, ok := m.subscribers[id]; ok {
		close(ch)
		delete(m.subscribers, id)
	}
}

// Publish broadcasts an event to all subscribers.
func (m *EventStreamManager) Publish(event RequestEvent) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, ch := range m.subscribers {
		select {
		case ch <- event:
		default:
			// Drop event if subscriber is slow
		}
	}
}

// SubscriberCount returns the number of active subscribers.
func (m *EventStreamManager) SubscriberCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.subscribers)
}

// EventStreamPlugin implements coreusage.Plugin to broadcast usage events.
type EventStreamPlugin struct {
	manager *EventStreamManager
}

// NewEventStreamPlugin creates a plugin wired to the shared event stream.
func NewEventStreamPlugin() *EventStreamPlugin {
	return &EventStreamPlugin{manager: defaultEventStream}
}

// HandleUsage implements coreusage.Plugin.
func (p *EventStreamPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.manager == nil {
		return
	}
	if p.manager.SubscriberCount() == 0 {
		return // No subscribers, skip processing
	}

	event := RequestEvent{
		Type:      "request",
		Timestamp: record.RequestedAt,
		Provider:  record.Provider,
		Model:     record.Model,
		AuthFile:  record.AuthIndex,
		Source:    record.Source,
		Success:   !record.Failed,
		Tokens:    record.Detail.TotalTokens,
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if record.Failed {
		event.Type = "error"
	}

	p.manager.Publish(event)
}

// PublishQuotaExceeded sends a quota exceeded event.
func PublishQuotaExceeded(provider, model, authFile string) {
	defaultEventStream.Publish(RequestEvent{
		Type:      "quota_exceeded",
		Timestamp: time.Now(),
		Provider:  provider,
		Model:     model,
		AuthFile:  authFile,
		Success:   false,
		Error:     "quota exceeded",
	})
}

// PublishError sends an error event.
func PublishError(provider, model, authFile, errorMsg string) {
	defaultEventStream.Publish(RequestEvent{
		Type:      "error",
		Timestamp: time.Now(),
		Provider:  provider,
		Model:     model,
		AuthFile:  authFile,
		Success:   false,
		Error:     errorMsg,
	})
}

// EventToSSE formats an event as SSE data.
func EventToSSE(event RequestEvent) []byte {
	data, _ := json.Marshal(event)
	return []byte(fmt.Sprintf("data: %s\n\n", data))
}

func init() {
	// Register the event stream plugin
	coreusage.RegisterPlugin(NewEventStreamPlugin())
}
