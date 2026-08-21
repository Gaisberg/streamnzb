package stremio

import (
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
)

func TestRawSearchCacheUntil(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	full := srv.playlistCacheTTL()

	t.Run("results keep the full sliding TTL", func(t *testing.T) {
		raw := &rawSearchResult{IndexerReleases: []*release.Release{{Title: "Some.Release"}}}
		until := srv.rawSearchCacheUntil(raw)
		if remaining := time.Until(until); remaining < full-time.Minute {
			t.Fatalf("results cached for %v, want the full TTL %v", remaining, full)
		}
		if !rawSearchCacheSlides(raw) {
			t.Fatalf("a result set should keep sliding on access")
		}
	})

	t.Run("empty results expire early and never slide", func(t *testing.T) {
		raw := &rawSearchResult{}
		remaining := time.Until(srv.rawSearchCacheUntil(raw))
		if remaining > emptyRawSearchTTL {
			t.Fatalf("empty result cached for %v, want at most %v", remaining, emptyRawSearchTTL)
		}
		if remaining < emptyRawSearchTTL-time.Minute {
			t.Fatalf("empty result cached for %v, want about %v", remaining, emptyRawSearchTTL)
		}
		if rawSearchCacheSlides(raw) {
			t.Fatalf("an empty result must not have its deadline pushed out on access")
		}
	})

	t.Run("unaired expires when the gate opens", func(t *testing.T) {
		// Inside the TTL, so the gate instant is the binding deadline.
		opensAt := time.Now().Add(time.Hour)
		raw := &rawSearchResult{Unaired: true, Air: airWindow{opensAt: opensAt}}
		until := srv.rawSearchCacheUntil(raw)
		want := opensAt.Add(-unairedMargin)
		if !until.Equal(want) {
			t.Fatalf("unaired cached until %v, want the gate-open moment %v", until, want)
		}
		if !until.Before(opensAt) {
			t.Fatalf("unaired cached until %v, not before its own gate instant %v", until, opensAt)
		}
		if rawSearchCacheSlides(raw) {
			t.Fatalf("an unaired entry must not have its deadline pushed out on access")
		}
	})

	t.Run("the deadline tracks the gate, not the schedule", func(t *testing.T) {
		// A scheduled broadcast well after the gate opens must not hold the
		// cached "unaired" answer past the moment searching becomes useful.
		opensAt := time.Now().Add(time.Hour)
		raw := &rawSearchResult{Unaired: true, Air: airWindow{
			opensAt:   opensAt,
			scheduled: opensAt.Add(20 * time.Hour),
		}}
		if until := srv.rawSearchCacheUntil(raw); !until.Equal(opensAt.Add(-unairedMargin)) {
			t.Fatalf("unaired cached until %v, want the gate-open moment %v", until, opensAt.Add(-unairedMargin))
		}
	})

	t.Run("a gate instant beyond the TTL keeps the TTL", func(t *testing.T) {
		raw := &rawSearchResult{Unaired: true, Air: airWindow{opensAt: time.Now().Add(30 * 24 * time.Hour)}}
		if remaining := time.Until(srv.rawSearchCacheUntil(raw)); remaining > full {
			t.Fatalf("unaired cached for %v, want no more than the TTL %v", remaining, full)
		}
	})
}
