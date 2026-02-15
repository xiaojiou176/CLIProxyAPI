package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestFillFirstSelectorPick_Deterministic(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "a" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "a")
	}
}

func TestRoundRobinSelectorPick_SticksToCurrentUntilBlocked(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	want := []string{"a", "a", "a", "a", "a"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestRoundRobinSelectorPick_SameEmailProjectMultipleCredentials(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{
			ID:       "a-legacy.json",
			Metadata: map[string]any{"email": "same@example.com", "project_id": "proj-a"},
		},
		{
			ID:       "b-scoped.json",
			Metadata: map[string]any{"email": "same@example.com", "project_id": "proj-a"},
		},
	}

	want := []string{"a-legacy.json", "a-legacy.json", "a-legacy.json", "a-legacy.json"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "gemini", "gemini-3.0-pro", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestRoundRobinSelectorPick_PriorityBuckets(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "c", Attributes: map[string]string{"priority": "0"}},
		{ID: "a", Attributes: map[string]string{"priority": "10"}},
		{ID: "b", Attributes: map[string]string{"priority": "10"}},
	}

	want := []string{"a", "a", "a", "a"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "mixed", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
		if got.ID == "c" {
			t.Fatalf("Pick() #%d unexpectedly selected lower priority auth", i)
		}
	}
}

func TestFillFirstSelectorPick_PriorityFallbackCooldown(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	now := time.Now()
	model := "test-model"

	high := &Auth{
		ID:         "high",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Quota: QuotaState{
					Exceeded: true,
				},
			},
		},
	}
	low := &Auth{ID: "low", Attributes: map[string]string{"priority": "0"}}

	got, err := selector.Pick(context.Background(), "mixed", model, cliproxyexecutor.Options{}, []*Auth{high, low})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "low" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "low")
	}
}

func TestRoundRobinSelectorPick_Concurrent(t *testing.T) {
	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
		{ID: "c"},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	goroutines := 32
	iterations := 100
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				got, err := selector.Pick(context.Background(), "gemini", "", cliproxyexecutor.Options{}, auths)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if got == nil {
					select {
					case errCh <- errors.New("Pick() returned nil auth"):
					default:
					}
					return
				}
				if got.ID == "" {
					select {
					case errCh <- errors.New("Pick() returned auth with empty ID"):
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent Pick() error = %v", err)
	default:
	}
}

func TestRoundRobinSelectorPick_AdvancesOnlyWhenCurrentUnavailable(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	authA := &Auth{ID: "a"}
	authB := &Auth{ID: "b"}
	auths := []*Auth{authB, authA}

	first, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	if first == nil || first.ID != "a" {
		t.Fatalf("Pick() first auth.ID = %q, want %q", first.ID, "a")
	}

	// Current account becomes unavailable -> selector should move to next one.
	authA.Unavailable = true
	authA.NextRetryAfter = time.Now().Add(30 * time.Minute)
	second, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if second == nil || second.ID != "b" {
		t.Fatalf("Pick() second auth.ID = %q, want %q", second.ID, "b")
	}

	// Even if previous account recovers, keep using current until it fails.
	authA.Unavailable = false
	authA.NextRetryAfter = time.Time{}
	third, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() third error = %v", err)
	}
	if third == nil || third.ID != "b" {
		t.Fatalf("Pick() third auth.ID = %q, want %q", third.ID, "b")
	}
}

func TestSelectorPick_AllCooldownReturnsModelCooldownError(t *testing.T) {
	t.Parallel()

	model := "test-model"
	now := time.Now()
	next := now.Add(60 * time.Second)
	auths := []*Auth{
		{
			ID: "a",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusActive,
					Unavailable:    true,
					NextRetryAfter: next,
					Quota: QuotaState{
						Exceeded:      true,
						NextRecoverAt: next,
					},
				},
			},
		},
		{
			ID: "b",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusActive,
					Unavailable:    true,
					NextRetryAfter: next,
					Quota: QuotaState{
						Exceeded:      true,
						NextRecoverAt: next,
					},
				},
			},
		},
	}

	t.Run("mixed provider redacts provider field", func(t *testing.T) {
		t.Parallel()

		selector := &FillFirstSelector{}
		_, err := selector.Pick(context.Background(), "mixed", model, cliproxyexecutor.Options{}, auths)
		if err == nil {
			t.Fatalf("Pick() error = nil")
		}

		var mce *modelCooldownError
		if !errors.As(err, &mce) {
			t.Fatalf("Pick() error = %T, want *modelCooldownError", err)
		}
		if mce.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("StatusCode() = %d, want %d", mce.StatusCode(), http.StatusTooManyRequests)
		}

		headers := mce.Headers()
		if got := headers.Get("Retry-After"); got == "" {
			t.Fatalf("Headers().Get(Retry-After) = empty")
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(mce.Error()), &payload); err != nil {
			t.Fatalf("json.Unmarshal(Error()) error = %v", err)
		}
		rawErr, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("Error() payload missing error object: %v", payload)
		}
		if got, _ := rawErr["code"].(string); got != "model_cooldown" {
			t.Fatalf("Error().error.code = %q, want %q", got, "model_cooldown")
		}
		if _, ok := rawErr["provider"]; ok {
			t.Fatalf("Error().error.provider exists for mixed provider: %v", rawErr["provider"])
		}
	})

	t.Run("non-mixed provider includes provider field", func(t *testing.T) {
		t.Parallel()

		selector := &FillFirstSelector{}
		_, err := selector.Pick(context.Background(), "gemini", model, cliproxyexecutor.Options{}, auths)
		if err == nil {
			t.Fatalf("Pick() error = nil")
		}

		var mce *modelCooldownError
		if !errors.As(err, &mce) {
			t.Fatalf("Pick() error = %T, want *modelCooldownError", err)
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(mce.Error()), &payload); err != nil {
			t.Fatalf("json.Unmarshal(Error()) error = %v", err)
		}
		rawErr, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("Error() payload missing error object: %v", payload)
		}
		if got, _ := rawErr["provider"].(string); got != "gemini" {
			t.Fatalf("Error().error.provider = %q, want %q", got, "gemini")
		}
	})
}

func TestIsAuthBlockedForModel_UnavailableWithoutNextRetryIsNotBlocked(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusActive,
				Unavailable: true,
				Quota: QuotaState{
					Exceeded: true,
				},
			},
		},
	}

	blocked, reason, next := isAuthBlockedForModel(auth, model, now)
	if blocked {
		t.Fatalf("blocked = true, want false")
	}
	if reason != blockReasonNone {
		t.Fatalf("reason = %v, want %v", reason, blockReasonNone)
	}
	if !next.IsZero() {
		t.Fatalf("next = %v, want zero", next)
	}
}

func TestFillFirstSelectorPick_ThinkingSuffixFallsBackToBaseModelState(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	now := time.Now()

	baseModel := "test-model"
	requestedModel := "test-model(high)"

	high := &Auth{
		ID:         "high",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			baseModel: {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Quota: QuotaState{
					Exceeded: true,
				},
			},
		},
	}
	low := &Auth{
		ID:         "low",
		Attributes: map[string]string{"priority": "0"},
	}

	got, err := selector.Pick(context.Background(), "mixed", requestedModel, cliproxyexecutor.Options{}, []*Auth{high, low})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil {
		t.Fatalf("Pick() auth = nil")
	}
	if got.ID != "low" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "low")
	}
}

func TestRoundRobinSelectorPick_ThinkingSuffixSharesCursor(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
	}

	first, err := selector.Pick(context.Background(), "gemini", "test-model(high)", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	second, err := selector.Pick(context.Background(), "gemini", "test-model(low)", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if first == nil || second == nil {
		t.Fatalf("Pick() returned nil auth")
	}
	if first.ID != "a" {
		t.Fatalf("Pick() first auth.ID = %q, want %q", first.ID, "a")
	}
	if second.ID != "a" {
		t.Fatalf("Pick() second auth.ID = %q, want %q", second.ID, "a")
	}
}

func TestRoundRobinSelectorPick_CursorKeyCap(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{maxKeys: 2}
	auths := []*Auth{{ID: "a"}}

	_, _ = selector.Pick(context.Background(), "gemini", "m1", cliproxyexecutor.Options{}, auths)
	_, _ = selector.Pick(context.Background(), "gemini", "m2", cliproxyexecutor.Options{}, auths)
	_, _ = selector.Pick(context.Background(), "gemini", "m3", cliproxyexecutor.Options{}, auths)

	selector.mu.Lock()
	defer selector.mu.Unlock()

	if selector.cursors == nil {
		t.Fatalf("selector.cursors = nil")
	}
	if len(selector.cursors) != 1 {
		t.Fatalf("len(selector.cursors) = %d, want %d", len(selector.cursors), 1)
	}
	if _, ok := selector.cursors["gemini:m3"]; !ok {
		t.Fatalf("selector.cursors missing key %q", "gemini:m3")
	}
}

func TestRoundRobinSelectorPick_SessionAffinitySticksToSameAuth(t *testing.T) {
	resetSessionAffinityBindings()
	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "b"},
		{ID: "a"},
	}
	optsS1 := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityKeyMetadataKey: "session-1",
		},
	}
	optsS2 := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityKeyMetadataKey: "session-2",
		},
	}

	first, err := selector.Pick(context.Background(), "codex", "gpt-5.3-codex", optsS1, auths)
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	if first == nil {
		t.Fatalf("Pick() first auth = nil")
	}
	second, err := selector.Pick(context.Background(), "codex", "gpt-5.3-codex", optsS1, auths)
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if second == nil {
		t.Fatalf("Pick() second auth = nil")
	}
	if first.ID != second.ID {
		t.Fatalf("same session switched auth: first=%q second=%q", first.ID, second.ID)
	}

	third, err := selector.Pick(context.Background(), "codex", "gpt-5.3-codex", optsS2, auths)
	if err != nil {
		t.Fatalf("Pick() third error = %v", err)
	}
	if third == nil {
		t.Fatalf("Pick() third auth = nil")
	}
	if third.ID != first.ID {
		t.Fatalf("sticky pointer expected same auth for new session, got first=%q third=%q", first.ID, third.ID)
	}
}

func TestRoundRobinSelectorPick_SessionAffinityFailsOverAndRebinds(t *testing.T) {
	resetSessionAffinityBindings()
	selector := &RoundRobinSelector{}
	authA := &Auth{ID: "a"}
	authB := &Auth{ID: "b"}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityKeyMetadataKey: "session-retry",
		},
	}

	first, err := selector.Pick(context.Background(), "codex", "gpt-5.3-codex", opts, []*Auth{authA, authB})
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	if first == nil {
		t.Fatalf("Pick() first auth = nil")
	}
	if first.ID != "a" {
		t.Fatalf("Pick() first auth.ID = %q, want %q", first.ID, "a")
	}

	// Simulate same request retry with tried map excluding previous auth "a".
	failedOver, err := selector.Pick(context.Background(), "codex", "gpt-5.3-codex", opts, []*Auth{authB})
	if err != nil {
		t.Fatalf("Pick() failedOver error = %v", err)
	}
	if failedOver == nil {
		t.Fatalf("Pick() failedOver auth = nil")
	}
	if failedOver.ID != "b" {
		t.Fatalf("Pick() failedOver auth.ID = %q, want %q", failedOver.ID, "b")
	}

	// After failover, the same session should stay on the new auth.
	rebound, err := selector.Pick(context.Background(), "codex", "gpt-5.3-codex", opts, []*Auth{authA, authB})
	if err != nil {
		t.Fatalf("Pick() rebound error = %v", err)
	}
	if rebound == nil {
		t.Fatalf("Pick() rebound auth = nil")
	}
	if rebound.ID != "b" {
		t.Fatalf("Pick() rebound auth.ID = %q, want %q", rebound.ID, "b")
	}
}
