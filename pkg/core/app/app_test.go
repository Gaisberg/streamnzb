package app

import (
	"testing"
)

func TestNewReturnsNonNil(t *testing.T) {
	t.Parallel()
	a := New()
	if a == nil {
		t.Fatal("New() returned nil")
	}
}
