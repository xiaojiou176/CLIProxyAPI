package usage

import (
	"testing"
	"time"
)

func TestReplaySinceReturnsMonotonicSequence(t *testing.T) {
	t.Parallel()

	manager := NewEventStreamManager()
	for i := 0; i < 10; i++ {
		manager.Publish(RequestEvent{
			Type:      "request",
			Timestamp: time.Now().UTC(),
			Provider:  "openai",
			Model:     "gpt-5",
			AuthFile:  "acct-1",
			Success:   true,
			Tokens:    int64(i + 1),
		})
	}

	events := manager.ReplaySince(4, 100)
	if len(events) != 6 {
		t.Fatalf("len(events)=%d, want=6", len(events))
	}
	last := int64(4)
	for i := 0; i < len(events); i++ {
		if events[i].Seq <= last {
			t.Fatalf("sequence not monotonic at index=%d seq=%d last=%d", i, events[i].Seq, last)
		}
		last = events[i].Seq
	}
}

func TestSlowSubscriberDropIsObservable(t *testing.T) {
	t.Parallel()

	manager := NewEventStreamManager()
	id, _ := manager.Subscribe()
	defer manager.Unsubscribe(id)

	for i := 0; i < 5000; i++ {
		manager.Publish(RequestEvent{
			Type:      "request",
			Timestamp: time.Now().UTC(),
			Provider:  "openai",
			Model:     "gpt-5",
			AuthFile:  "acct-1",
			Success:   true,
			Tokens:    1,
		})
	}

	metrics := manager.MetricsSnapshot()
	if metrics.DroppedTotal == 0 {
		t.Fatalf("expected dropped_total > 0 for slow subscriber")
	}
	if metrics.PublishedTotal < 5000 {
		t.Fatalf("published_total=%d, want>=5000", metrics.PublishedTotal)
	}
}
