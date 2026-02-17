package promptqueue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFiveSessionsThirtyEachNoLoss(t *testing.T) {
	t.Parallel()

	manager := NewManager(Config{
		StoreDir:         t.TempDir(),
		SessionQueueSize: 8,
		MaxEvents:        20000,
		MaxSubmissions:   20000,
	})

	const (
		sessionCount = 5
		perSession   = 30
		total        = sessionCount * perSession
	)

	var processed atomic.Int64
	errCh := make(chan error, total)
	var wg sync.WaitGroup

	for s := 0; s < sessionCount; s++ {
		sessionKey := fmt.Sprintf("session-%d", s)
		for i := 0; i < perSession; i++ {
			promptIndex := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := manager.SubmitAndWait(SubmitRequest{
					SessionKey: sessionKey,
					Handler:    "openai",
					Model:      "gpt-5",
					RequestID:  fmt.Sprintf("%s-%d", sessionKey, promptIndex),
				}, func(_ string) error {
					time.Sleep(2 * time.Millisecond)
					processed.Add(1)
					return nil
				})
				if err != nil {
					errCh <- err
				}
			}()
		}
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("unexpected submit error: %v", err)
		}
	}

	if got := processed.Load(); got != int64(total) {
		t.Fatalf("processed=%d, want=%d", got, total)
	}

	allSubmissions := manager.ListSubmissions(ListOptions{Limit: total + 10})
	if len(allSubmissions) != total {
		t.Fatalf("submission count=%d, want=%d", len(allSubmissions), total)
	}

	metrics := manager.MetricsSnapshot()
	if metrics.Submitted != total {
		t.Fatalf("submitted=%d, want=%d", metrics.Submitted, total)
	}
	if metrics.Succeeded != total {
		t.Fatalf("succeeded=%d, want=%d", metrics.Succeeded, total)
	}
	if metrics.Failed != 0 {
		t.Fatalf("failed=%d, want=0", metrics.Failed)
	}

	events := manager.EventsSince("", 0, total*4)
	if len(events) < total*2 {
		t.Fatalf("events=%d, want_at_least=%d", len(events), total*2)
	}
}

func TestOverloadedMetricObserved(t *testing.T) {
	t.Parallel()

	manager := NewManager(Config{
		StoreDir:         t.TempDir(),
		SessionQueueSize: 1,
		MaxEvents:        256,
		MaxSubmissions:   1024,
	})

	start := make(chan struct{})
	release := make(chan struct{})

	// First request blocks worker to fill queue.
	go func() {
		_, _ = manager.SubmitAndWait(SubmitRequest{
			SessionKey: "s",
			Handler:    "openai",
			Model:      "gpt-5",
		}, func(_ string) error {
			close(start)
			<-release
			return nil
		})
	}()
	<-start

	// Two more requests on same session should make the second enqueue observe overload.
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, _ = manager.SubmitAndWait(SubmitRequest{
				SessionKey: "s",
				Handler:    "openai",
				Model:      "gpt-5",
			}, func(_ string) error { return nil })
		}()
	}

	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if metrics := manager.MetricsSnapshot(); metrics.Overloaded == 0 {
		t.Fatalf("overloaded metric not incremented")
	}
}
