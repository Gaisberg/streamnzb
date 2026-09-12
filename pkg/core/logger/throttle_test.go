package logger

import (
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
	throttleSeen.Store(key, time.Now().Add(-2*time.Hour))
	if !Throttle(key, time.Hour) {
		t.Fatal("call after the interval must pass")
	}
	if Throttle("other-"+key, time.Hour) != true {
		t.Fatal("a different key is independent")
	}
}
