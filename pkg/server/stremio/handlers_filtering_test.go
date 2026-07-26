package stremio

import (
	"testing"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/metadata/tmdb"
)

// candidatesFor builds candidates the way the playlist pipeline does, so these
// tests exercise the same parse jhin sees at runtime.
func candidatesFor(titles ...string) []triage.Candidate {
	rels := make([]*release.Release, 0, len(titles))
	for _, t := range titles {
		rels = append(rels, &release.Release{Title: t})
	}
	return releasesToCandidates(rels)
}

func profileFor(t *testing.T, fp config.FilterProfileConfig) *ranking.Profile {
	t.Helper()
	svc := ranking.NewService()
	if errs := svc.Reload(&config.Config{FilterProfiles: []config.FilterProfileConfig{fp}}); len(errs) > 0 {
		t.Fatalf("profile failed to compile: %v", errs)
	}
	p, ok := svc.Get(fp.Name)
	if !ok {
		t.Fatalf("profile %q not found after reload", fp.Name)
	}
	return p
}

func keptTitles(results []ranking.Result) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Candidate.Release.Title)
	}
	return out
}

// A profile migrated from the pre-jhin schema must still gate the same way.
func TestSynthesizedProfileFiltering(t *testing.T) {
	yes := true
	profile := profileFor(t, config.FilterProfileConfig{
		Name:               "Legacy",
		AllowedResolutions: []string{"1080p", "720p"},
		BlockedQualities:   []string{"CAM", "TeleSync"},
		RequireHDR:         &yes,
		ExcludedKeywords:   []string{"subbed"},
		BlockedLanguages:   []string{"ru"},
	})

	tests := []struct {
		title string
		want  bool
	}{
		{"Obsession 2026 1080p BluRay HDR", true},
		{"Obsession 2026 1080p CAM HDR", false},    // blocked quality
		{"Obsession 2026 2160p BluRay HDR", false}, // resolution not allowed
		{"Obsession 2026 1080p BluRay", false},     // HDR required
		{"Obsession 2026 1080p BluRay HDR SUBBED", false},
		{"Obsession 2026 1080p BluRay HDR RUSSIAN", false},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			results := profile.Evaluate(candidatesFor(tt.title), rank.RankOptions{})
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if got := results[0].Torrent.Fetch; got != tt.want {
				t.Errorf("Fetch = %v, want %v (rejections: %v)",
					got, tt.want, results[0].Torrent.Rejections)
			}
		})
	}
}

// Sorting runs off jhin's score.
func TestProfileSortOrder(t *testing.T) {
	profile := profileFor(t, config.FilterProfileConfig{
		Name:      "Sorting",
		SortOrder: []string{"resolution", "rank"},
	})

	cands := candidatesFor(
		"Movie 2020 1080p WEB-DL H.264-GRP",
		"Movie 2020 2160p BluRay REMUX DV TrueHD 7.1-GRP",
		"Movie 2020 1080p BluRay REMUX-GRP",
	)

	got := keptTitles(profile.Apply(cands, rank.RankOptions{}))
	if len(got) == 0 {
		t.Fatal("expected results")
	}
	if got[0] != "Movie 2020 2160p BluRay REMUX DV TrueHD 7.1-GRP" {
		t.Errorf("expected the 2160p remux first, got %q (order: %v)", got[0], got)
	}
}

// Size is a Usenet signal jhin cannot see, so it is compared here.
func TestSortBySize(t *testing.T) {
	profile := profileFor(t, config.FilterProfileConfig{
		Name:      "BySize",
		SortOrder: []string{"size"},
	})

	cands := candidatesFor("Movie 2020 1080p WEB-DL-A", "Movie 2020 1080p WEB-DL-B")
	cands[0].Release.Size = 1 << 30
	cands[1].Release.Size = 8 << 30

	got := keptTitles(profile.Apply(cands, rank.RankOptions{}))
	if len(got) != 2 || got[0] != "Movie 2020 1080p WEB-DL-B" {
		t.Errorf("expected the larger release first, got %v", got)
	}
}

// Every request lands in exactly one kind, so selection is never ambiguous.
func TestKindClassifiesRequests(t *testing.T) {
	tests := []struct {
		name          string
		contentType   string
		kitsuShowType string
		isAnime       bool
		want          string
	}{
		{"movie", "movie", "", false, ranking.KindMovie},
		{"series", "series", "", false, ranking.KindSeries},
		{"anime film", "series", "movie", true, ranking.KindAnimeMovie},
		{"anime TV", "series", "TV", true, ranking.KindAnimeShow},
		{"anime OVA", "series", "OVA", true, ranking.KindAnimeShow},
		{"anime special", "series", "special", true, ranking.KindAnimeShow},
		// Kitsu occasionally omits the type; fall back to the request's own.
		{"anime, type unknown, movie request", "movie", "", true, ranking.KindAnimeMovie},
		{"anime, type unknown, series request", "series", "", true, ranking.KindAnimeShow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ranking.Kind(tt.contentType, tt.kitsuShowType, tt.isAnime); got != tt.want {
				t.Errorf("Kind() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelectNameUsesKindBinding(t *testing.T) {
	byKind := map[string]string{
		ranking.KindMovie:      "Movies",
		ranking.KindAnimeMovie: "Anime Films",
	}

	tests := []struct {
		name     string
		byKind   map[string]string
		fallback string
		kind     string
		want     string
	}{
		{"bound kind wins", byKind, "Default", ranking.KindMovie, "Movies"},
		{"anime film has its own", byKind, "Default", ranking.KindAnimeMovie, "Anime Films"},
		{"unbound kind falls back", byKind, "Default", ranking.KindAnimeShow, "Default"},
		{"no bindings at all", nil, "Default", ranking.KindSeries, "Default"},
		{"no fallback either", nil, "", ranking.KindSeries, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ranking.SelectName(tt.byKind, tt.fallback, tt.kind); got != tt.want {
				t.Errorf("SelectName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Explain has to account for a rejection, not just a score.
func TestExplainReportsRejection(t *testing.T) {
	profile := profileFor(t, config.FilterProfileConfig{
		Name:             "Explained",
		BlockedQualities: []string{"CAM"},
	})

	ex := profile.Explain("Movie 2020 1080p CAM x264-TRASH", rank.RankOptions{})
	if ex == nil {
		t.Fatal("expected an explanation")
	}
	if ex.Fetch {
		t.Errorf("Fetch = true, want false")
	}
	if len(ex.Rejections) == 0 {
		t.Error("expected at least one rejection reason")
	}
	if len(ex.Contributions) == 0 {
		t.Error("expected the score breakdown to be populated")
	}
}

// A server has to come up with its profiles already compiled. Loading them
// only on reload leaves every lookup failing until the next config save, which
// silently means no filtering at all.
func TestNewRankingServiceLoadsProfilesUpFront(t *testing.T) {
	cfg := &config.Config{
		FilterProfiles: []config.FilterProfileConfig{
			{Name: "Movies Only", AllowedResolutions: []string{"1080p"}},
		},
	}

	svc := newRankingService(cfg)
	profile, ok := svc.Get("Movies Only")
	if !ok {
		t.Fatal("expected the profile to be compiled at construction")
	}

	results := profile.Evaluate(candidatesFor("Movie 2020 2160p BluRay REMUX-GRP"), rank.RankOptions{})
	if results[0].Torrent.Fetch {
		t.Errorf("2160p should be rejected by a 1080p-only profile: %v", results[0].Torrent.Rejections)
	}
}

// Most anime is browsed through ordinary catalogues, so classification cannot
// rely on a Kitsu request. Anime is animation that is not originally English.
func TestMetadataLooksLikeAnime(t *testing.T) {
	tvWith := func(lang string, genres ...string) *resolvedSearchMetadata {
		g := make([]tmdb.Genre, 0, len(genres))
		for _, name := range genres {
			g = append(g, tmdb.Genre{Name: name})
		}
		return &resolvedSearchMetadata{TVDetails: &tmdb.TVDetails{OriginalLanguage: lang, Genres: g}}
	}
	movieWith := func(lang string, genres ...string) *resolvedSearchMetadata {
		g := make([]tmdb.Genre, 0, len(genres))
		for _, name := range genres {
			g = append(g, tmdb.Genre{Name: name})
		}
		return &resolvedSearchMetadata{MovieDetails: &tmdb.MovieDetails{OriginalLanguage: lang, Genres: g}}
	}

	tests := []struct {
		name        string
		meta        *resolvedSearchMetadata
		contentType string
		want        bool
	}{
		{"japanese animation", tvWith("ja", "Animation", "Action"), "series", true},
		{"chinese animation", tvWith("zh", "Animation"), "series", true},
		{"korean animation", tvWith("ko", "Animation"), "series", true},
		{"an explicit anime genre", tvWith("en", "Anime"), "series", true},
		{"american cartoon", tvWith("en", "Animation", "Comedy"), "series", false},
		{"japanese live action", tvWith("ja", "Drama"), "series", false},
		{"anime film", movieWith("ja", "Animation"), "movie", true},
		{"english film", movieWith("en", "Animation"), "movie", false},
		{"no metadata at all", nil, "series", false},
		{"metadata for the other kind", tvWith("ja", "Animation"), "movie", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metadataLooksLikeAnime(tt.meta, tt.contentType); got != tt.want {
				t.Errorf("metadataLooksLikeAnime() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A Kitsu request stays anime regardless of what the other metadata says.
func TestKindForRequestPrefersKitsu(t *testing.T) {
	source := &playlistSource{Params: &SearchParams{
		ContentType: "series",
		Metadata: &resolvedSearchMetadata{
			KitsuDetails: &KitsuAnimeDetails{ShowType: "movie"},
			TVDetails:    &tmdb.TVDetails{OriginalLanguage: "en", Genres: []tmdb.Genre{{Name: "Drama"}}},
		},
	}}
	if got := kindForRequest(source); got != ranking.KindAnimeMovie {
		t.Errorf("kindForRequest() = %q, want %q", got, ranking.KindAnimeMovie)
	}
}
