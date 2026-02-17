package queuehealth

import "sync"

type Snapshot struct {
	Counters map[string]int64 `json:"counters"`
}

var (
	mu       sync.RWMutex
	counters = map[string]int64{}
)

func Inc(reason string) {
	if reason == "" {
		return
	}
	mu.Lock()
	counters[reason]++
	mu.Unlock()
}

func Add(reason string, delta int64) {
	if reason == "" || delta == 0 {
		return
	}
	mu.Lock()
	counters[reason] += delta
	mu.Unlock()
}

func Get(reason string) int64 {
	mu.RLock()
	defer mu.RUnlock()
	return counters[reason]
}

func SnapshotAll() Snapshot {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]int64, len(counters))
	for key, value := range counters {
		out[key] = value
	}
	return Snapshot{Counters: out}
}
