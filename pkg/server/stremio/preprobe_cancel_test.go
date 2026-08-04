package stremio

import (
	"context"
	"testing"
)

// TestPreProbeCancelRegistryNoPanic exercises the register/cleanup/cancel paths
// that previously panicked ("comparing uncomparable type context.CancelFunc")
// because a func value was stored bare in a sync.Map and CompareAndDelete
// compared it.
func TestPreProbeCancelRegistryNoPanic(t *testing.T) {
	s := &Server{}
	key := "default:series:tt1"

	_, cancel := context.WithCancel(context.Background())
	cleanup := s.registerPreProbeCancel(key, cancel)

	// A newer sweep for the same content must cancel and replace the older one.
	canceled := false
	_, c2 := context.WithCancel(context.Background())
	replace := &preProbeCancelEntry{cancel: func() { canceled = true }}
	if prev, loaded := s.preProbeCancels.Swap(key, replace); loaded {
		if pe, ok := prev.(*preProbeCancelEntry); ok {
			pe.cancel()
		}
	}
	_ = c2

	// cleanup for the ORIGINAL entry must NOT delete the replacement (identity check).
	cleanup() // must not panic
	if _, ok := s.preProbeCancels.Load(key); !ok {
		t.Fatal("cleanup of a superseded entry wrongly removed the current registration")
	}

	// Cancelling by slot path must invoke the current entry's cancel and remove it.
	// Register a fresh entry under a real slot-derived key.
	slot := "stream:default:series:tt1:0"
	realKey := preProbeCacheKeyFromSlot(slot)
	if realKey == "" {
		t.Fatal("expected a non-empty cache key from slot path")
	}
	invoked := false
	s.registerPreProbeCancel(realKey, func() { invoked = true })
	s.cancelPreProbeForContentSlot(slot) // must not panic
	if !invoked {
		t.Error("expected cancel to be invoked for the matching slot")
	}
	if _, ok := s.preProbeCancels.Load(realKey); ok {
		t.Error("expected the entry to be removed after cancel")
	}

	_ = canceled
}
