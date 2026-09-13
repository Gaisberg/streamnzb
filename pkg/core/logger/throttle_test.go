package logger

import (
	"fmt"
	"testing"
	"time"
)

func TestThrottleFirstPassesThenHolds(t *testing.T) {
	key := "throttle-test-" + t.Name()
	if !Throttle(key, time.Hour) {
		t.Fatal("first call must pass")
	}
	if Throttle(key, time.Hour) {
		t.Fatal("second call inside the interval must be held")
	}
	throttleMu.Lock()
	throttleSeen[key] = time.Now().Add(-2 * time.Hour)
	throttleMu.Unlock()
	if !Throttle(key, time.Hour) {
		t.Fatal("call after the interval must pass")
	}
	if Throttle("other-"+key, time.Hour) != true {
		t.Fatal("a different key is independent")
	}
}

// Past the bound, the whole table clears rather than growing forever: a
// long-running process serving many distinct rejected ids must not leak
// memory one key at a time with no eviction.
func TestThrottleClearsPastKeyBound(t *testing.T) {
	throttleMu.Lock()
	clear(throttleSeen)
	throttleMu.Unlock()
	for i := 0; i < throttleMaxKeys+10; i++ {
		Throttle(fmt.Sprintf("bound-test-%d", i), time.Hour)
	}
	throttleMu.Lock()
	n := len(throttleSeen)
	throttleMu.Unlock()
	if n >= throttleMaxKeys {
		t.Fatalf("throttleSeen has %d entries, want it cleared well before %d", n, throttleMaxKeys)
	}
}
