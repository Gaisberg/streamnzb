package indexer

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestNoteThrottledUsesDefaultWithoutRetryAfter(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	now := time.Now()

	c.NoteThrottled(http.Header{}, now)

	if err := c.CheckThrottled("Treasuremaps", now.Add(DefaultThrottleCooldown-time.Second)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected an active cooldown, got %v", err)
	}
	if err := c.CheckThrottled("Treasuremaps", now.Add(DefaultThrottleCooldown+time.Second)); err != nil {
		t.Fatalf("cooldown must expire on its own, got %v", err)
	}
}

func TestNoteThrottledHonoursRetryAfterSeconds(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	now := time.Now()

	h := http.Header{}
	h.Set("Retry-After", "5")
	c.NoteThrottled(h, now)

	// Well short of the default, proving the header won over the fallback.
	if err := c.CheckThrottled("Treasuremaps", now.Add(6*time.Second)); err != nil {
		t.Fatalf("expected the 5s Retry-After to have expired, got %v", err)
	}
	if err := c.CheckThrottled("Treasuremaps", now.Add(4*time.Second)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected cooldown still active at 4s, got %v", err)
	}
}

func TestNoteThrottledHonoursRetryAfterHTTPDate(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	now := time.Now().UTC().Truncate(time.Second)

	h := http.Header{}
	h.Set("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat))
	c.NoteThrottled(h, now)

	if err := c.CheckThrottled("Treasuremaps", now.Add(80*time.Second)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected date-form Retry-After to hold, got %v", err)
	}
	if err := c.CheckThrottled("Treasuremaps", now.Add(100*time.Second)); err != nil {
		t.Fatalf("expected date-form cooldown to expire, got %v", err)
	}
}

func TestNoteThrottledClampsAbsurdRetryAfter(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	now := time.Now()

	h := http.Header{}
	h.Set("Retry-After", "86400")
	c.NoteThrottled(h, now)

	if err := c.CheckThrottled("Treasuremaps", now.Add(MaxThrottleCooldown+time.Second)); err != nil {
		t.Fatalf("a day-long Retry-After must be capped, got %v", err)
	}
}

// A burst of concurrent grabs all come back 429; the later ones must not shorten
// the cooldown the first one opened.
func TestNoteThrottledNeverShortensActiveCooldown(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	now := time.Now()

	long := http.Header{}
	long.Set("Retry-After", "600")
	c.NoteThrottled(long, now)

	short := http.Header{}
	short.Set("Retry-After", "1")
	c.NoteThrottled(short, now)

	if err := c.CheckThrottled("Treasuremaps", now.Add(30*time.Second)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("a shorter later cooldown must not undercut the active one, got %v", err)
	}
}

func TestParseRetryAfterRejectsGarbageAndPastDates(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cases := []string{"", "   ", "soon", "-5", "0", now.Add(-time.Hour).Format(http.TimeFormat)}
	for _, value := range cases {
		if d, ok := parseRetryAfter(value, now); ok {
			t.Fatalf("parseRetryAfter(%q) = %v, true; want ok=false so the caller uses its default", value, d)
		}
	}
}

func TestCheckThrottledIsInertWithoutAThrottle(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	if err := c.CheckThrottled("Treasuremaps", time.Now()); err != nil {
		t.Fatalf("a fresh core must not be in cooldown, got %v", err)
	}
}
