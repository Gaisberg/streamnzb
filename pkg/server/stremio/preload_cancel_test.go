package stremio

import (
	"context"
	"testing"
)

// TestPreloadCancelRegistryNoPanic exercises the register/cleanup/cancel paths
// that previously panicked ("comparing uncomparable type context.CancelFunc")
// because a func value was stored bare in a sync.Map and CompareAndDelete
// compared it.
func TestPreloadCancelRegistryNoPanic(t *testing.T) {
	s := &Server{}
	key := "default:series:tt1"

	_, cancel := context.WithCancel(context.Background())
	cleanup := s.registerPreloadCancel(key, cancel)

	// A newer sweep for the same content must cancel and replace the older one.
	canceled := false
	_, c2 := context.WithCancel(context.Background())
	replace := &preloadCancelEntry{cancel: func() { canceled = true }}
	if prev, loaded := s.preloadCancels.Swap(key, replace); loaded {
		if pe, ok := prev.(*preloadCancelEntry); ok {
			pe.cancel()
		}
	}
	_ = c2

	// cleanup for the ORIGINAL entry must NOT delete the replacement (identity check).
	cleanup() // must not panic
	if _, ok := s.preloadCancels.Load(key); !ok {
		t.Fatal("cleanup of a superseded entry wrongly removed the current registration")
	}

	// Cancelling by slot path must invoke the current entry's cancel and remove it.
	// Register a fresh entry under a real slot-derived key.
	slot := "stream:default:series:tt1:0"
	realKey := preloadCacheKeyFromSlot(slot)
	if realKey == "" {
		t.Fatal("expected a non-empty cache key from slot path")
	}
	invoked := false
	s.registerPreloadCancel(realKey, func() { invoked = true })
	s.cancelPreloadForContentSlot(slot) // must not panic
	if !invoked {
		t.Error("expected cancel to be invoked for the matching slot")
	}
	if _, ok := s.preloadCancels.Load(realKey); ok {
		t.Error("expected the entry to be removed after cancel")
	}

	_ = canceled
}
