package stremio

import (
	"context"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tmdb"
)

// rankingServerFor wires the minimum server applyRanking needs: a ranking
// service holding one profile, and a stream bound to it.
func rankingServerFor(t *testing.T, fp config.FilterProfileConfig) (*Server, *auth.Stream) {
	t.Helper()
	svc := ranking.NewService()
	if errs := svc.Reload(&config.Config{FilterProfiles: []config.FilterProfileConfig{fp}}); len(errs) > 0 {
		t.Fatalf("profile failed to compile: %v", errs)
	}
	s := &Server{rankingService: svc, triageService: triage.NewService()}
	stream := &auth.Stream{Username: "test", FilterProfileName: fp.Name}
	return s, stream
}

func movieSource() *playlistSource {
	return &playlistSource{Params: &query.SearchParams{ContentType: "movie", ContentTitle: "Movie"}}
}

func rankTitles(t *testing.T, s *Server, stream *auth.Stream, source *playlistSource, titles ...string) []triage.Candidate {
	t.Helper()
	rels := make([]*release.Release, 0, len(titles))
	for _, title := range titles {
		rels = append(rels, &release.Release{Title: title, DetailsURL: "https://example.invalid/" + title})
	}
	return s.applyRanking(context.Background(), releasesToCandidates(rels), source, true, "profile", stream)
}

// The playlist must return the order the profile decided, which is the order
// the finished score puts the releases in — rules included. It used to re-sort
// on a heuristic of its own after the profile had already ordered the list,
// which silently discarded whatever the profile had decided.
func TestApplyRankingKeepsProfileOrder(t *testing.T) {
	s, stream := rankingServerFor(t, config.FilterProfileConfig{
		Name:   "Rules",
		Preset: config.PresetUHD,
		Rules: []config.RuleConfig{{
			Name:   "Prefer Finnish",
			When:   `"fi" in languages`,
			Action: config.RuleActionScore,
			Points: 80000,
		}},
	})

	out := rankTitles(t, s, stream, movieSource(),
		"Movie 2020 2160p BluRay REMUX HEVC-GRP",
		"Movie 2020 1080p FINNISH WEB-DL H264-GRP",
	)
	if len(out) != 2 {
		t.Fatalf("got %d results, want 2", len(out))
	}
	if !strings.Contains(out[0].Release.Title, "1080p") {
		t.Fatalf("first result is %q, want the 1080p release the rule paid for", out[0].Release.Title)
	}
	if out[0].Score <= out[1].Score {
		t.Fatal("test is not exercising the order: the rule did not outscore the 2160p remux")
	}
}

// The score a candidate carries downstream is the finished one, with every
// source of points already in it.
func TestApplyRankingScoreIncludesLibraryBonus(t *testing.T) {
	bonus := 4242
	s, stream := rankingServerFor(t, config.FilterProfileConfig{
		Name:              "Bonus",
		LibraryScoreBonus: &bonus,
	})

	fresh := &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP"}
	cached := &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP", IsLibrary: true}
	out := s.applyRanking(context.Background(), releasesToCandidates([]*release.Release{fresh, cached}), movieSource(), true, "profile", stream)
	if len(out) != 2 {
		t.Fatalf("got %d results, want 2", len(out))
	}
	if !out[0].Release.IsLibraryResult() {
		t.Fatal("the library release did not sort first")
	}
	if got := out[0].Score - out[1].Score; got != bonus {
		t.Fatalf("library release scored %d above the fresh one, want the bonus %d", got, bonus)
	}
}

// The anime classification reaches the candidate, so custom formats no longer
// have to guess at it from the release name.
func TestApplyRankingRecordsAnimeVerdict(t *testing.T) {
	s, stream := rankingServerFor(t, config.FilterProfileConfig{Name: "Anime"})
	source := &playlistSource{Params: &query.SearchParams{
		ContentType: "series",
		Metadata:    &query.ResolvedSearchMetadata{KitsuDetails: &kitsu.AnimeDetails{ShowType: "TV"}},
	}}

	out := rankTitles(t, s, stream, source, "Show S01E01 1080p WEB-DL H264-GRP")
	if len(out) != 1 {
		t.Fatalf("got %d results, want 1", len(out))
	}
	if !out[0].Verdict.IsAnime {
		t.Error("Verdict.IsAnime = false for a Kitsu request")
	}
	if out[0].Verdict.Kind != ranking.KindAnimeShow {
		t.Errorf("Verdict.Kind = %q, want %q", out[0].Verdict.Kind, ranking.KindAnimeShow)
	}
}

// A live-action request classified through TMDB genres is not anime.
func TestApplyRankingLiveActionIsNotAnime(t *testing.T) {
	s, stream := rankingServerFor(t, config.FilterProfileConfig{Name: "LiveAction"})
	source := &playlistSource{Params: &query.SearchParams{
		ContentType: "series",
		Metadata: &query.ResolvedSearchMetadata{
			TVDetails: &tmdb.TVDetails{OriginalLanguage: "en", Genres: []tmdb.Genre{{Name: "Drama"}}},
		},
	}}

	out := rankTitles(t, s, stream, source, "Show S01E01 1080p WEB-DL H264-GRP")
	if len(out) != 1 {
		t.Fatalf("got %d results, want 1", len(out))
	}
	if out[0].Verdict.IsAnime {
		t.Error("Verdict.IsAnime = true for a live-action TMDB request")
	}
}

// Named rules that pay out are recorded by name, which is what custom formats
// render and what the score breakdown shows.
func TestApplyRankingRecordsMatchedRules(t *testing.T) {
	s, stream := rankingServerFor(t, config.FilterProfileConfig{
		Name: "Rules",
		Rules: []config.RuleConfig{
			{Name: "IMAX", When: `releaseName matches "(?i)\\bIMAX\\b"`, Points: 1000},
			{Name: "Never", When: `releaseName matches "(?i)\\bNOPE\\b"`, Points: 500},
		},
	})

	out := rankTitles(t, s, stream, movieSource(), "Movie 2020 IMAX 1080p WEB-DL H264-GRP")
	if len(out) != 1 {
		t.Fatalf("got %d results, want 1", len(out))
	}
	matched := out[0].Verdict.Matched
	if len(matched) != 1 {
		t.Fatalf("matched %d rules, want 1: %+v", len(matched), matched)
	}
	if matched[0].Name != "IMAX" || matched[0].Score != 1000 {
		t.Errorf("matched %+v, want IMAX at 1000", matched[0])
	}
}

// The formatter renders the verdict rather than a summary of it.
func TestFormatContextExposesVerdict(t *testing.T) {
	cand := triage.Candidate{
		Release: &release.Release{Title: "Movie 2020 2160p BluRay REMUX DV HEVC-GRP"},
		Verdict: triage.Verdict{
			Kind:    ranking.KindMovie,
			IsAnime: false,
			Matched: []triage.RuleMatch{{Name: "IMAX", Score: 1000}},
			Probed: &release.MediaCaps{
				VideoCodec: "hevc", Height: 2160, BitDepth: 10, DolbyVision: true,
			},
			Avail: triage.AvailState{
				Status:       triage.AvailAvailable,
				OnMyBackbone: true,
				CheckedAt:    time.Now().Add(-48 * time.Hour),
				Compression:  "rar",
			},
		},
	}

	ctx := newFormatContext(cand, 1, 1, "StreamNZB", "Standalone", "Movie", "", true)

	if ctx.Kind != ranking.KindMovie {
		t.Errorf("Kind = %q, want %q", ctx.Kind, ranking.KindMovie)
	}
	if len(ctx.MatchedRules) != 1 || ctx.MatchedRules[0].Name != "IMAX" {
		t.Errorf("MatchedRules = %+v, want one IMAX entry", ctx.MatchedRules)
	}
	if !ctx.Verified {
		t.Error("Verified = false for a probed release")
	}
	if ctx.Probed.DynamicRange != "DV only" {
		t.Errorf("Probed.DynamicRange = %q, want %q", ctx.Probed.DynamicRange, "DV only")
	}
	if ctx.Probed.HasHDRFallback {
		t.Error("HasHDRFallback = true for a DV release with no base layer")
	}
	if ctx.Availability.Status != "available" || !ctx.Availability.OnMyBackbone {
		t.Errorf("Availability = %+v, want available on our backbone", ctx.Availability)
	}
	if ctx.Availability.CheckedDaysAgo != 2 {
		t.Errorf("Availability.CheckedDaysAgo = %d, want 2", ctx.Availability.CheckedDaysAgo)
	}
}

// A fresh indexer hit reports no measurements and no availability opinion,
// rather than zero values that read as real ones.
func TestFormatContextUnprobedRelease(t *testing.T) {
	cand := triage.Candidate{Release: &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP"}}
	ctx := newFormatContext(cand, 1, 1, "StreamNZB", "Standalone", "Movie", "", false)

	if ctx.Verified {
		t.Error("Verified = true for a release that was never probed")
	}
	if ctx.Availability.Status != "unknown" || ctx.Availability.Known {
		t.Errorf("Availability = %+v, want unknown", ctx.Availability)
	}
	if ctx.Availability.CheckedDaysAgo != -1 {
		t.Errorf("Availability.CheckedDaysAgo = %d, want -1 for a record with no timestamp", ctx.Availability.CheckedDaysAgo)
	}
}
