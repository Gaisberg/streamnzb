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

func TestCheckSearchAllowedSkipsWhenDownloadBudgetSpent(t *testing.T) {
	// One download left, no API limit: searching is still worthwhile.
	c := NewClientCore("Treasuremaps", 0, 10, 0, nil)
	now := time.Now()

	if err := c.CheckSearchAllowed("Treasuremaps", now); err != nil {
		t.Fatalf("expected search allowed while downloads remain, got %v", err)
	}

	h := http.Header{}
	h.Set("X-DNZBLimit-Daily-Limit", "10")
	h.Set("X-DNZBLimit-Daily-Remaining", "0")
	c.ApplyHeaderUsage(h)

	// The first call after exhaustion is the probe and passes through, so the
	// indexer keeps a chance to report a budget we cannot otherwise observe.
	if err := c.CheckSearchAllowed("Treasuremaps", now); err != nil {
		t.Fatalf("expected the first post-exhaustion search to probe, got %v", err)
	}
	if err := c.CheckSearchAllowed("Treasuremaps", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected search skipped inside the probe interval, got %v", err)
	}
	if err := c.CheckSearchAllowed("Treasuremaps", now.Add(DownloadExhaustedProbeInterval+time.Second)); err != nil {
		t.Fatalf("expected another probe once the interval elapsed, got %v", err)
	}
}

func TestCheckSearchAllowedIgnoresDownloadBudgetWithoutLimit(t *testing.T) {
	// No configured download limit means no budget to spend, so search must
	// never be gated on it.
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	now := time.Now()

	for i := 0; i < 3; i++ {
		if err := c.CheckSearchAllowed("Treasuremaps", now); err != nil {
			t.Fatalf("expected search allowed with no download limit, got %v", err)
		}
	}
}

func TestCheckGrabAllowedStillBlocksOnSpentDownloadBudget(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 1, 0, nil)
	now := time.Now()

	if err := c.CheckGrabAllowed("Treasuremaps", now); err != nil {
		t.Fatalf("expected grab allowed with budget left, got %v", err)
	}

	h := http.Header{}
	h.Set("X-DNZBLimit-Daily-Limit", "1")
	h.Set("X-DNZBLimit-Daily-Remaining", "0")
	c.ApplyHeaderUsage(h)

	// Unlike search there is no probe here: a grab that cannot succeed must not
	// be attempted just to refresh a counter.
	if err := c.CheckGrabAllowed("Treasuremaps", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected grab blocked once the budget is spent, got %v", err)
	}
}
