package search

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dreulavelle/jhin"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/diag"
	"streamnzb/pkg/search/parser"
)

type recordingIndexer struct {
	mu   sync.Mutex
	name string
	reqs []indexer.SearchRequest
}

func (r *recordingIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return &indexer.SearchResponse{}, nil
}

func (r *recordingIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (r *recordingIndexer) Ping(context.Context) error                          { return nil }
func (r *recordingIndexer) Name() string                                        { return r.name }
func (r *recordingIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

type staticIndexer struct {
	name string
	resp *indexer.SearchResponse
}

func (s *staticIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return s.resp, nil
}

func (s *staticIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (s *staticIndexer) Ping(context.Context) error                          { return nil }
func (s *staticIndexer) Name() string                                        { return s.name }
func (s *staticIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

type errIndexer struct {
	name string
	err  error
}

func (e *errIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return nil, e.err
}

func (e *errIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (e *errIndexer) Ping(context.Context) error                          { return nil }
func (e *errIndexer) Name() string                                        { return e.name }
func (e *errIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

func TestRunIndexerSearchesTextRequestCarriesSeasonEpisodeWhenEnabled(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		Cat:             "5000",
		Limit:           100,
		IMDbID:          "tt1234567",
		Season:          "1",
		Episode:         "5",
		Query:           "The Walking Dead",
		ValidationQuery: "The Walking Dead",
	}

	if _, err := RunIndexerSearches(context.Background(), idx, req, "series"); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}

	if len(idx.reqs) != 1 {
		t.Fatalf("expected 1 Search call, got %d", len(idx.reqs))
	}

	textReq := &idx.reqs[0]
	if textReq.Season != "1" || textReq.Episode != "5" {
		t.Fatalf("expected text request to keep season/episode when enabled, got season=%q episode=%q", textReq.Season, textReq.Episode)
	}
	if textReq.Query != "The Walking Dead" {
		t.Fatalf("expected text query to be preserved, got %q", textReq.Query)
	}
}

func TestRunIndexerSearchesAlwaysAppliesValidation(t *testing.T) {
	idx := &staticIndexer{
		name: "SceneNZBs",
		resp: &indexer.SearchResponse{
			Channel: indexer.Channel{
				Items: []indexer.Item{
					{Title: "Der.Patriot.2000.German.DL.1080p.BluRay.x264.iNTERNAL-VideoStar", ActualIndexer: "SceneNZBs"},
				},
			},
		},
	}
	req := indexer.SearchRequest{
		Cat:                  "2100",
		IMDbID:               "tt0187393",
		TMDBID:               "2024",
		Query:                "Der Patriot 2000",
		ValidationQuery:      "The Patriot 2000",
		EnableYearValidation: true,
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "movie")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected validation to remove mismatched title, got %d: %+v", len(got), got)
	}
}

func TestValidateSearchResultsWithStatsCountsSeriesEpisodeMatches(t *testing.T) {
	releases := []*release.Release{
		{Title: "Right.Show.S01E02.1080p.WEB-DL.x264-GROUP"},
		{Title: "Right.Show.S01E02E03.1080p.WEB-DL.x264-GROUP"},
		{Title: "Right.Show.S01.COMPLETE.1080p.WEB-DL.x264-GROUP"},
		{Title: "Right.Show.COMPLETE.1080p.WEB-DL.x264-GROUP"},
		{Title: "Right.Show.S01E04.1080p.WEB-DL.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Right Show S01E02", "1", "2", true, false)

	if len(filtered) != 4 {
		t.Fatalf("expected 4 accepted results, got %d", len(filtered))
	}
	if stats.RawResults != 5 || stats.FinalResults != 4 || stats.RejectedResults != 1 {
		t.Fatalf("unexpected raw/final/rejected counts: %+v", stats)
	}
	if !stats.TitleValidationApplied {
		t.Fatalf("expected title validation to be applied, got %+v", stats)
	}
	if stats.AcceptedExactEpisode != 1 {
		t.Fatalf("expected 1 exact episode match, got %d", stats.AcceptedExactEpisode)
	}
	if stats.AcceptedMultiEpisode != 1 {
		t.Fatalf("expected 1 multi-episode match, got %d", stats.AcceptedMultiEpisode)
	}
	if stats.AcceptedSeasonPack != 1 {
		t.Fatalf("expected 1 season pack match, got %d", stats.AcceptedSeasonPack)
	}
	if stats.AcceptedCompletePack != 1 {
		t.Fatalf("expected 1 complete pack match, got %d", stats.AcceptedCompletePack)
	}
	if stats.DroppedEpisodeRequest != 1 {
		t.Fatalf("expected 1 episode-request rejection, got %d", stats.DroppedEpisodeRequest)
	}
}

// Anime absolute numbering: with an absolute episode set, releases named with
// the absolute number (dash style, S01Exxx style, or bare) must be accepted
// even though their parsed season/episode do not match the TVDB-style request.
// A text query can match an episode *title* — searching the film "Spirited
// Away" returns "Avatar The Last Airbender S01E05 Spirited Away". Movie
// validation must drop those: the parsed release title is the series name.
func TestValidateSearchResultsDropsEpisodeTitleMatches(t *testing.T) {
	releases := []*release.Release{
		{Title: "Spirited.Away.2001.JP.Hybrid.BD.Remux.1080p.AVC.DTS-HD.MA.2.0-RedDeadProject"},
		{Title: "Avatar.The.Last.Airbender.2024.S01E05.Spirited.Away.2160p.NF.WEB-DL.DDP5.1.Atmos.H.265-Kitsune"},
		{Title: "Avatar The Last Airbender 2024 S01E05 Spirited Away 2160p NF WEB-DL DDP5 1 Atmos H 265-FLUX"},
	}

	queries := []string{"Spirited Away 2001", "Spirited Away", "Sen to Chihiro no Kamikakushi"}
	filtered, stats := ValidateSearchResultsWithStatsForQueries(releases, "movie", queries, "", "", "", true, true)

	if len(filtered) != 1 || filtered[0] != releases[0] {
		t.Fatalf("expected only the film to survive, got %d: %+v", len(filtered), stats)
	}
	if stats.DroppedTitle != 2 {
		t.Fatalf("expected 2 title rejections, got %+v", stats)
	}
}

// jhin's similarity fallback rescues titles the word-block matcher cannot
// line up, while staying strict enough to keep rejecting different titles.
func TestTitleValidationSimilarityFallback(t *testing.T) {
	releases := []*release.Release{
		{Title: "Spirited.Awey.2001.1080p.BluRay.x264-GROUP"},  // near-miss spelling
		{Title: "Spirit.Untamed.2021.1080p.BluRay.x264-GROUP"}, // different film
	}

	filtered, stats := ValidateSearchResultsWithStatsForQueries(releases, "movie", []string{"Spirited Away 2001"}, "", "", "", true, false)

	if len(filtered) != 1 || filtered[0] != releases[0] {
		t.Fatalf("expected the near-miss kept and the different film dropped, got %d: %+v", len(filtered), stats)
	}
}

// The similarity fallback must never bridge numbering: numbered sequels are
// one edit apart yet different films, so digit runs gate the ratio.
func TestSimilarTitleMatchesGuardsNumbering(t *testing.T) {
	if similarTitleMatches("The Hunger Games Mockingjay Part 1", "The Hunger Games Mockingjay Part 2") {
		t.Fatal("numbered sequel must not match via similarity")
	}
	if !similarTitleMatches("Evangelion: 3.0+1.0 Thrice Upon a Time", "Evangelion 3.0 + 1.0 Thrice Upon A Time") {
		t.Fatal("punctuation variant with agreeing numbers must match")
	}
}

func TestValidateSearchResultsAcceptsAbsoluteNumberedAnime(t *testing.T) {
	releases := []*release.Release{
		{Title: "One.Piece.S02E02.1080p.WEB-DL.x264-GROUP"},     // standard match
		{Title: "[Judas] One Piece - 63 (1080p) [ABCD1234]"},    // absolute, dash style
		{Title: "One.Piece.S01E63.1080p.WEB-DL.AAC2.0-HatSubs"}, // absolute, S01Exxx style
		{Title: "[Judas] One Piece - 62 (1080p) [ABCD1234]"},    // wrong absolute episode
		{Title: "One.Piece.S02E05.1080p.WEB-DL.x264-GROUP"},     // wrong episode
		{Title: "One.Piece.S03E63.1080p.WEB-DL.x264-GROUP"},     // absolute number under an explicit wrong season
	}

	filtered, stats := ValidateSearchResultsWithStatsForQueries(releases, "series", []string{"One Piece"}, "2", "2", "63", true, false)

	if len(filtered) != 3 {
		t.Fatalf("expected 3 accepted results, got %d: %+v", len(filtered), stats)
	}
	if stats.DroppedEpisodeRequest != 3 {
		t.Fatalf("expected 3 episode-request rejections, got %d", stats.DroppedEpisodeRequest)
	}
	if stats.AcceptedExactEpisode != 3 {
		t.Fatalf("expected 3 exact episode matches, got %d", stats.AcceptedExactEpisode)
	}

	// Without the absolute number the same absolute-styled releases must
	// still be dropped — the widening only applies when it is known.
	filtered, _ = ValidateSearchResultsWithStatsForQueries(releases, "series", []string{"One Piece"}, "2", "2", "", true, false)
	if len(filtered) != 1 {
		t.Fatalf("expected only the standard release without absolute, got %d", len(filtered))
	}
}

// Episodic anime is validated by episode, and only as far as playback's file
// match can back it: a release is dropped when it carries a number playback
// would read and refuse, and kept when playback would accept it or could not
// tell. The first case is the incident's own release formats, offered for an
// S01E04 request.
func TestValidateSearchResultsAnimeDropsOnlyWhatPlaybackRefuses(t *testing.T) {
	cases := []struct {
		name                 string
		season, episode, abs string
		kept, dropped        []string
	}{
		{
			name: "incident: S01E04, absolute 4", season: "1", episode: "4", abs: "4",
			kept: []string{
				"SHOW.S01E04.Pursuit.1080p.NF.WEB-DL.DDP2.0.H.264-GRP",
				"[Grp] Show - 04 - Pursuit [ABCDEF12]",
				"[Grp] Romaji Title - 04 [1080p]", // fansub title: not enforced
				"[Grp] Show (BD 1080p)",           // no episode marker
				"Show 1080p BluRay",               // no episode marker
				"Show S01 1080p BluRay x264",      // season pack
				"Show (2006) S01E01-E37 BluRay Remux 1080p",
				"[Grp] Show - 01-37 [Batch]",
				"[Grp] Show - Complete Series [BD]",
				// Season-only packs may be numbered absolutely inside, whatever
				// season their title names, and an extras tag changes nothing.
				"Show S02 1080p BluRay x264",
				"[Grp] Show S2 [BD][NCOP+NCED]",
				"[Grp] Show S03 + OVA [BD]",
				// OVA notation with no season: the number may be the OVA's.
				"[Grp] Show - 23 [1080p][OVA]",
				"[Grp] Show (TV + OVA) - 23 [1080p]",
			},
			dropped: []string{
				"Show (2006) S01E22 BluRay Remux 1080p DTS-HD MA 5.1 H264 AVC GRP",
				"Show (2006) S01E23 BluRay Remux 1080p DTS-HD MA 5.1 H264 AVC GRP",
				"Show (2006) S01E24 BluRay Remux 1080p DTS-HD MA 5.1 H264 AVC GRP",
				"[Grp] Show - 28 - Impatience [47CF5862]",
				// Ancillary extras excuse no wrong episode.
				"Show.S01E23.Sample.1080p.WEB-DL-GRP",
				"Show.S01E23.Featurette.1080p.WEB-DL-GRP",
				"Show.S01E23.Trailer.1080p.WEB-DL-GRP",
				"[Grp] Show - 23 [ONA]",
				// An explicit season leaves an OVA tag nothing to explain.
				"Show.S01E23.OVA.1080p.WEB-DL-GRP",
				"[Grp] Show S01E05 + OVA [BD]",
			},
		},
		{
			// A Kitsu cour mapped onto S01E14: playback reads the cour's own
			// "- 01" as episode 1 and refuses it, so search does too.
			name: "per-cour numbering: mapped S01E14", season: "1", episode: "14", abs: "14",
			kept:    []string{"[SubsPlease] Show - 14 (1080p) [ABCD1234]", "Show.S01E14.1080p.WEB-DL-GRP"},
			dropped: []string{"[SubsPlease] Show - Part 2 - 01 (1080p) [ABCD1234]"},
		},
		{
			// Absolute and in-season numbering of S02E03 are both accepted; a
			// bare in-season "- 03" names no season, which playback reads as
			// absolute 3 and refuses.
			name: "absolute vs season/episode: S02E03, absolute 28", season: "2", episode: "3", abs: "28",
			kept: []string{
				"Show.S02E03.1080p.WEB-DL-GRP",
				"[Grp] Show S2 - 03 (1080p) [ABCD1234]",
				"[Grp] Show 2nd Season - 03 [1080p]",
				"[Grp] Show - 28 [1080p]",      // absolute
				"Show.S01E28.1080p.WEB-DL-GRP", // absolute under season 1
				"[Grp] Show S2 [Batch]",        // season pack
				"[Grp] Show - 01-37 [Batch]",   // absolute range
				"Show S01 1080p BluRay x264",   // season-only: may hold "- 28"
			},
			dropped: []string{
				"[Grp] Show - 03 [1080p]", // bare per-season numbering
				"Show.S02E04.1080p.WEB-DL-GRP",
				"[Grp] Show S2 - 15 [1080p]",
				"Show.S01E03.1080p.WEB-DL-GRP",
				"[Grp] Show - 27 [1080p]",
				"Show.S02E28.1080p.WEB-DL-GRP", // a season's own E28, not absolute 28
			},
		},
		{
			// A first-season BD pack can be numbered absolutely inside and
			// hold "Show - 13.mkv" for S02E01.
			name: "season-only pack: S02E01, absolute 13", season: "2", episode: "1", abs: "13",
			kept:    []string{"Show S01 [BD 1080p]", "[Grp] Show Season 1 [BD]", "[Grp] Show - 13 [1080p]"},
			dropped: []string{"[Grp] Show - 12 [1080p]", "Show.S01E01.1080p.WEB-DL-GRP"},
		},
		{
			// The parser flags OVA notation without numbering it as a special,
			// and reads nothing in "SP02"/"Special 2": neither is evidence of
			// another episode. A plain bare number is a regular episode.
			name: "OVA specials: S00E02", season: "0", episode: "2",
			kept: []string{
				"Show.S00E02.1080p.WEB-DL-GRP",
				"[Grp] Show - OVA 2 [1080p]",
				"[Grp] Show OVA - 02 [1080p]",
				"[Grp] Show - SP02 [1080p]",
				"[Grp] Show - Special 2 [1080p]",
				"[Grp] Show OAD 2 [1080p]",
				// A season pack tagged OVA may bundle the special.
				"[Grp] Show S01 + OVA [BD 1080p]",
				"[Grp] Show Season 1 + OVA [BD]",
				// OVA notation with no season stays ambiguous, as before.
				"[Grp] Show - 05 [OVA]",
			},
			dropped: []string{
				"[Grp] Show - 02 [1080p]",
				"Show.S01E02.1080p.WEB-DL-GRP",
				// An explicit episode number is not excused by the OVA tag.
				"[Grp] Show S01E05 + OVA [BD]",
				"Show.S01E05.OVA.1080p.WEB-DL-GRP",
				// The parser tags only the word "OVA": a known limitation.
				"[Grp] Show Season 1 + OVAs [BD]",
				"[Grp] Show S1 + Specials [BD]",
				// Ancillary tags excuse nothing.
				"[Grp] Show S01 [BD][NCOP+NCED]",
			},
		},
		{
			// An entry spanning the whole series numbers absolutely; another
			// season's number cannot be placed in it, by search or playback.
			name: "seasonless absolute 154", episode: "154", abs: "154",
			kept:    []string{"[Grp] Show - 154 [1080p]", "Show.S01E154.1080p.WEB-DL-GRP", "Show 1080p BluRay"},
			dropped: []string{"[Grp] Show - 153 [1080p]", "Show.S01E23.1080p.WEB-DL-GRP", "Show.S05E15.1080p.WEB-DL-GRP"},
		},
		{
			// An unmapped Kitsu entry with no absolute number cannot pick a
			// file, so playback does not check it and nothing is turned away.
			name: "seasonless episode 4, absolute unknown", episode: "4",
			kept: []string{"Show.S03E04.1080p.WEB-DL-GRP", "[Grp] Show - 04 [1080p]", "Show S03 1080p BluRay x264",
				"Show.S03E05.1080p.WEB-DL-GRP", "[Grp] Show - 28 [1080p]"},
		},
		{
			// Missing absolute metadata: only the season/episode numbering
			// can match, at search as at playback.
			name: "absolute unknown: S02E01", season: "2", episode: "1",
			kept: []string{"Show.S02E01.1080p.WEB-DL-GRP", "[Grp] Show S2 - 01 [1080p]", "[Grp] Show (BD 1080p)", "[Grp] Show S2 [BD]"},
			dropped: []string{"Show.S02E02.1080p.WEB-DL-GRP", "Show.S01E01.1080p.WEB-DL-GRP", "[SubsPlease] Show - 25 (1080p)",
				"Show S01 [BD 1080p]"}, // with no absolute, another season's pack is just that
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var releases []*release.Release
			for _, title := range append(append([]string{}, tc.kept...), tc.dropped...) {
				releases = append(releases, &release.Release{Title: title})
			}
			opts := ValidationOptions{Season: tc.season, Episode: tc.episode, AbsoluteEpisode: tc.abs, EnforceTitle: true, EnforceYear: true, AcceptPacks: true}
			filtered, stats := ValidateSearchResultsWithOptions(releases, "anime", []string{"Show 2006"}, opts)
			got := make([]string, 0, len(filtered))
			for _, rel := range filtered {
				got = append(got, rel.Title)
			}
			if !reflect.DeepEqual(got, tc.kept) {
				t.Fatalf("kept %q\nwant %q", got, tc.kept)
			}
			if stats.DroppedEpisodeRequest != len(tc.dropped) {
				t.Fatalf("episode drops = %d, want %d", stats.DroppedEpisodeRequest, len(tc.dropped))
			}
			// A fansub's romaji name is not reported as a title mismatch.
			if stats.TitleMismatchKept != 0 || stats.TitleValidationApplied {
				t.Fatalf("anime title checked: mismatches=%d applied=%v", stats.TitleMismatchKept, stats.TitleValidationApplied)
			}

			// No dropped title would match as a file name either: playback
			// ranks NZB file names with the seasons/episodes-only partial
			// parse and archive members with the full one, against the same
			// target. A release's files may be named otherwise; those are
			// playback's to check.
			season, err := strconv.Atoi(tc.season)
			abs, _ := strconv.Atoi(tc.abs)
			episode, _ := strconv.Atoi(tc.episode)
			target := parser.EpisodeTarget{Season: season, Seasonless: err != nil, Episode: episode, Absolute: abs}
			partial := jhin.GetPartialParser([]string{"seasons", "episodes"})
			for _, title := range tc.dropped {
				if rank := parser.ParseReleaseTitle(title).TargetMatchRank(target); rank > 0 {
					t.Errorf("dropped %q, which the full parse ranks %d", title, rank)
				}
				if rank := parser.ParseReleaseTitleWithParser(title, partial).TargetMatchRank(target); rank > 0 {
					t.Errorf("dropped %q, which the partial parse ranks %d", title, rank)
				}
			}
		})
	}

	// A plan refusing packs refuses the packs it can recognise, and nothing
	// on a guess: a name with no number, or a special the parser cannot
	// number, may be a single episode. Numbered ranges and batches name the
	// episode and stay.
	for _, tc := range []struct {
		name                 string
		season, episode, abs string
	}{
		{"S02E03, absolute 28", "2", "3", "28"},
		{"unmapped Kitsu entry, episode 4", "", "4", ""},
	} {
		noPacks := []*release.Release{
			{Title: "[Grp] Show - 28 [1080p]"}, {Title: "[Grp] Show (BD 1080p)"}, {Title: "[Grp] Show - SP02 [1080p]"},
			{Title: "Show S01E01-E37 BluRay 1080p"},
			{Title: "[Grp] Show S2 [Batch]"}, {Title: "Show S01 [BD 1080p]"}, {Title: "[Grp] Show - Complete Series [BD]"},
		}
		kept, stats := ValidateSearchResultsWithOptions(noPacks, "anime", nil, ValidationOptions{Season: tc.season, Episode: tc.episode, AbsoluteEpisode: tc.abs, AcceptPacks: false, RecordDropped: 10})
		if len(kept) != 4 || kept[3].Title != "Show S01E01-E37 BluRay 1080p" || len(stats.Dropped) != 3 {
			t.Fatalf("%s, packs refused: kept %d, dropped %d; want the episode, the two unnumbered releases and the range kept, the three packs dropped", tc.name, len(kept), len(stats.Dropped))
		}
		for _, drop := range stats.Dropped {
			if !strings.Contains(drop.Detail, "pack") {
				t.Fatalf("%s: %q dropped as %q, want the pack reason", tc.name, drop.Title, drop.Detail)
			}
		}
	}

	// Specials with packs refused: OVA-tagged season packs go as packs that
	// may hold the special, an explicit wrong episode as a wrong episode.
	specials := []*release.Release{
		{Title: "Show.S00E02.1080p.WEB-DL-GRP"}, {Title: "[Grp] Show - OVA 2 [1080p]"},
		{Title: "[Grp] Show S01 + OVA [BD 1080p]"}, {Title: "[Grp] Show Season 1 + OVA [BD]"}, {Title: "[Grp] Show S01E05 + OVA [BD]"},
	}
	kept, stats := ValidateSearchResultsWithOptions(specials, "anime", nil, ValidationOptions{Season: "0", Episode: "2", AcceptPacks: false, RecordDropped: 10})
	wantDetail := map[string]string{
		"[Grp] Show S01 + OVA [BD 1080p]": "a pack that may contain the episode, which this request does not accept",
		"[Grp] Show Season 1 + OVA [BD]":  "a pack that may contain the episode, which this request does not accept",
		"[Grp] Show S01E05 + OVA [BD]":    "expected S00E02, got seasons [1] episodes [5]",
	}
	if len(kept) != 2 || len(stats.Dropped) != len(wantDetail) {
		t.Fatalf("specials, packs refused: kept %d, dropped %d; want 2 kept, 3 dropped", len(kept), len(stats.Dropped))
	}
	for _, drop := range stats.Dropped {
		if drop.Detail != wantDetail[drop.Title] {
			t.Fatalf("%q dropped as %q, want %q", drop.Title, drop.Detail, wantDetail[drop.Title])
		}
	}

	// Standard TV keeps its strict rule: no episode marker, or a bare number
	// without the absolute, is not the episode.
	strict := []*release.Release{{Title: "Show.S01E04.1080p.WEB-DL-GRP"}, {Title: "[Grp] Show - 04 [ABCDEF12]"}, {Title: "Show 1080p BluRay"}}
	filtered, _ := ValidateSearchResultsWithOptions(strict, "series", []string{"Show"}, ValidationOptions{Season: "1", Episode: "4", AcceptPacks: true})
	if len(filtered) != 1 || filtered[0].Title != "Show.S01E04.1080p.WEB-DL-GRP" {
		t.Fatalf("series validation changed: kept %d", len(filtered))
	}
}

// Search judges the release title with the full parse; playback ranks NZB
// file names with a seasons/episodes-only partial parse, and the two read OVA
// notation differently. Neither reading is trusted: search keeps an OVA title
// with no season whatever number it parsed.
func TestAnimeOVANotationParsesDifferentlyAndIsKept(t *testing.T) {
	partial := jhin.GetPartialParser([]string{"seasons", "episodes"})
	for _, tc := range []struct {
		title               string
		fullEps, partialEps []int
	}{
		{"[Grp] Show - OVA 2 [1080p]", []int{2}, nil},
		{"[Grp] Show OVA - 02 [1080p]", []int{2}, []int{2}},
	} {
		full := parser.ParseReleaseTitle(tc.title)
		if !slices.Equal(full.Episodes, tc.fullEps) || !slices.Contains(full.Extras, "OVA") {
			t.Fatalf("%q full parse: episodes %v extras %v, want episodes %v with OVA", tc.title, full.Episodes, full.Extras, tc.fullEps)
		}
		if got := parser.ParseReleaseTitleWithParser(tc.title, partial).Episodes; !slices.Equal(got, tc.partialEps) {
			t.Fatalf("%q partial parse: episodes %v, want %v", tc.title, got, tc.partialEps)
		}
		kept, _ := ValidateSearchResultsWithOptions([]*release.Release{{Title: tc.title}}, "anime", nil, ValidationOptions{Season: "0", Episode: "5", AcceptPacks: true})
		if len(kept) != 1 {
			t.Fatalf("%q dropped for S00E05", tc.title)
		}
	}
}

// Daily shows are released under their air date (#382). With the episode's
// air date known, the date-named release is the episode; every other check —
// title, numbering, packs, year — stays as it was.
func TestValidateSearchResultsAcceptsDailyShowAirDate(t *testing.T) {
	releases := []*release.Release{
		{Title: "Jimmy.Kimmel.Live.2026.10.26.Dave.Grohl.1080p.WEB.h264-GROUP"},
		{Title: "Jimmy.Kimmel.Live.2026.10.27.Owen.Wilson.1080p.WEB.h264-GROUP"}, // the next night
		{Title: "Jimmy.Kimmel.Live.S24E2524.1080p.WEB.h264-GROUP"},               // numbered
		{Title: "Jimmy.Kimmel.Live.S24E2525.1080p.WEB.h264-GROUP"},               // numbered, wrong
		{Title: "Late.Night.with.Seth.Meyers.2026.10.26.1080p.WEB.h264-GROUP"},   // same night, other show
	}
	opts := ValidationOptions{Season: "24", Episode: "2524", AirDate: "2026-10-26", EnforceTitle: true, EnforceYear: true, AcceptPacks: true, RecordDropped: 10}

	filtered, stats := ValidateSearchResultsWithOptions(releases, "series", []string{"Jimmy Kimmel Live 2003"}, opts)
	got := make([]string, 0, len(filtered))
	for _, rel := range filtered {
		got = append(got, rel.Title)
	}
	want := []string{
		"Jimmy.Kimmel.Live.2026.10.26.Dave.Grohl.1080p.WEB.h264-GROUP",
		"Jimmy.Kimmel.Live.S24E2524.1080p.WEB.h264-GROUP",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("accepted %q, want %q", got, want)
	}
	// The year in the validation query is enforced, and a date-named release
	// carries no year for it to disagree with.
	if !stats.YearValidationApplied || stats.DroppedYear != 0 {
		t.Fatalf("year validation applied=%v dropped=%d, want applied with no drops", stats.YearValidationApplied, stats.DroppedYear)
	}
	if stats.DroppedTitle != 1 || stats.DroppedEpisodeRequest != 2 || stats.AcceptedExactEpisode != 2 {
		t.Fatalf("dropped title=%d episode=%d accepted exact=%d, want 1, 2, 2", stats.DroppedTitle, stats.DroppedEpisodeRequest, stats.AcceptedExactEpisode)
	}
	// The drop names both dates: "dated 2026-10-27, episode 2026-10-26" is
	// the answer to "why was the daily release turned away".
	var wrongNight diag.DroppedRelease
	for _, d := range stats.Dropped {
		if strings.Contains(d.Title, "2026.10.27") {
			wrongNight = d
		}
	}
	if !strings.Contains(wrongNight.Detail, "2026-10-27") || !strings.Contains(wrongNight.Detail, "2026-10-26") {
		t.Errorf("drop detail should name both dates, got %q", wrongNight.Detail)
	}

	// Without the air date — no source knew it — the daily release is turned
	// away exactly as before the fix.
	opts.AirDate = ""
	filtered, _ = ValidateSearchResultsWithOptions(releases, "series", []string{"Jimmy Kimmel Live 2003"}, opts)
	if len(filtered) != 1 || filtered[0].Title != "Jimmy.Kimmel.Live.S24E2524.1080p.WEB.h264-GROUP" {
		t.Fatalf("without an air date only the numbered release may pass, got %+v", filtered)
	}
}

// The air date travels on the request: RunIndexerSearches must hand it to
// validation, or an id search answered with date-named releases keeps nothing.
func TestRunIndexerSearchesMatchesDailyShowByAirDate(t *testing.T) {
	idx := &staticIndexer{
		name: "Prowlarr",
		resp: &indexer.SearchResponse{
			Channel: indexer.Channel{
				Items: []indexer.Item{
					{Title: "The.Daily.Show.2026.10.26.1080p.WEB.h264-GROUP", ActualIndexer: "Prowlarr"},
					{Title: "The.Daily.Show.2026.10.22.1080p.WEB.h264-GROUP", ActualIndexer: "Prowlarr"},
				},
			},
		},
	}
	req := indexer.SearchRequest{
		Cat:             "5000",
		SearchMode:      "id",
		IMDbID:          "tt0115147",
		Season:          "31",
		Episode:         "120",
		AirDate:         "2026-10-26",
		ValidationQuery: "The Daily Show",
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "series")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 1 || got[0].Title != "The.Daily.Show.2026.10.26.1080p.WEB.h264-GROUP" {
		t.Fatalf("expected only the release for the episode's air date, got %+v", got)
	}

	req.AirDate = ""
	if got, _ = RunIndexerSearches(context.Background(), idx, req, "series"); len(got) != 0 {
		t.Fatalf("without an air date the date-named releases cannot be matched, got %+v", got)
	}
}

// Season 0 is Stremio's Specials season and a literal one: S00E01 serves a
// season 0 request, the same episode number in another season does not, and
// the seasonless anime path — asked for by an absent season, not season 0 —
// keeps its own rules.
func TestValidateSearchResultsSeasonZeroIsLiteral(t *testing.T) {
	releases := []*release.Release{
		{Title: "Example.Show.S00E01.1080p.WEB-DL"},
		{Title: "Example.Show.S01E01.1080p.WEB-DL"},
		{Title: "Example.Show.S02E01.1080p.WEB-DL"},
		{Title: "Example.Show.S00.1080p.WEB-DL"},
		{Title: "Example.Show.Complete.Series.1080p.WEB-DL"},
		{Title: "[Group] Example Show - 01 (1080p)"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Example Show", "0", "1", true, false)
	if len(filtered) != 3 {
		t.Fatalf("expected S00E01, the S00 pack and the complete pack, got %d: %+v", len(filtered), stats)
	}
	for _, rel := range filtered {
		if strings.Contains(rel.Title, "S01E01") || strings.Contains(rel.Title, "S02E01") || strings.Contains(rel.Title, " - 01") {
			t.Fatalf("season 0 request accepted %q", rel.Title)
		}
	}
	if stats.AcceptedExactEpisode != 1 || stats.AcceptedSeasonPack != 1 || stats.AcceptedCompletePack != 1 {
		t.Fatalf("unexpected acceptance counts: %+v", stats)
	}
	if stats.DroppedEpisodeRequest != 3 {
		t.Fatalf("expected 3 episode-request rejections, got %d", stats.DroppedEpisodeRequest)
	}

	// An absent season is the seasonless path an unmapped Kitsu entry takes:
	// a release naming no season or season 1 matches, and nothing reads the
	// request as season 0.
	filtered, _ = ValidateSearchResultsWithStats(releases, "series", "Example Show", "", "1", true, false)
	if len(filtered) != 3 {
		t.Fatalf("expected S01E01, the dashed release and the complete pack, got %d", len(filtered))
	}
	for _, rel := range filtered {
		if strings.Contains(rel.Title, "S00") || strings.Contains(rel.Title, "S02E01") {
			t.Fatalf("seasonless request accepted %q", rel.Title)
		}
	}

	// A season-only request for the Specials season keeps the S00 releases.
	filtered, stats = ValidateSearchResultsWithStats(releases, "series", "Example Show", "0", "", true, false)
	if len(filtered) != 2 || stats.AcceptedSeasonPack != 1 || stats.AcceptedSeasonMatch != 1 {
		t.Fatalf("expected the two S00 releases, got %d: %+v", len(filtered), stats)
	}
}

func TestValidateSearchResultsWithStatsCountsMovieTitleAndYearDrops(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Patriot.2000.1080p.BluRay.x264-GROUP"},
		{Title: "The.Patriot.2004.1080p.BluRay.x264-GROUP"},
		{Title: "Der.Patriot.2000.1080p.BluRay.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "movie", "The Patriot 2000", "", "", true, true)

	if len(filtered) != 1 {
		t.Fatalf("expected 1 accepted movie result, got %d", len(filtered))
	}
	if !stats.TitleValidationApplied || !stats.YearValidationApplied {
		t.Fatalf("expected title and year validation to be applied, got %+v", stats)
	}
	if stats.DroppedYear != 1 {
		t.Fatalf("expected 1 year rejection, got %d", stats.DroppedYear)
	}
	if stats.DroppedTitle != 1 {
		t.Fatalf("expected 1 title rejection, got %d", stats.DroppedTitle)
	}
}

func TestValidateSearchResultsWithStatsSkipsYearWhenDisabled(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Patriot.2000.1080p.BluRay.x264-GROUP"},
		{Title: "The.Patriot.2004.1080p.BluRay.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "movie", "The Patriot 2000", "", "", true, false)

	if len(filtered) != 2 {
		t.Fatalf("expected both results to remain when year validation is off, got %d", len(filtered))
	}
	if stats.YearValidationApplied {
		t.Fatalf("expected year validation to stay off, got %+v", stats)
	}
	if stats.DroppedYear != 0 {
		t.Fatalf("expected no year rejections, got %d", stats.DroppedYear)
	}
}

func TestValidateSearchResultsWithStatsAllowsOptionalAndWordMatches(t *testing.T) {
	releases := []*release.Release{
		{Title: "Your.Friends.Neighbors.S01E01.1080p.WEB-DL.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Your Friends & Neighbors", "1", "1", true, false)

	if len(filtered) != 1 {
		t.Fatalf("expected optional '&/and' title match to pass, got %d results", len(filtered))
	}
	if stats.DroppedTitle != 0 {
		t.Fatalf("expected no title rejection, got %+v", stats)
	}
	if stats.AcceptedExactEpisode != 1 {
		t.Fatalf("expected exact episode match, got %+v", stats)
	}
}

func TestValidateSearchResultsWithStatsForQueriesMatchesAnyExpectedTitle(t *testing.T) {
	releases := []*release.Release{
		{Title: "Koenig.der.Loewen.1994.1080p.BluRay.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStatsForQueries(releases, "movie", []string{"The Lion King 1994", "Koenig der Loewen 1994"}, "", "", "", true, true)

	if len(filtered) != 1 {
		t.Fatalf("expected multilingual validation to accept a matching alternate title, got %d results", len(filtered))
	}
	if stats.DroppedTitle != 0 || stats.DroppedYear != 0 {
		t.Fatalf("expected no validation drops, got %+v", stats)
	}
}

func TestValidateSearchResultsWithStatsAllowsDashedSeasonEpisodePattern(t *testing.T) {
	releases := []*release.Release{
		{Title: "[SubsPlease] Tensei Shitara Slime Datta Ken S4 - 03 (720p) [370B1C65]"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Tensei shitara Slime Datta Ken", "4", "3", true, false)

	if len(filtered) != 1 {
		t.Fatalf("expected dashed season/episode title to pass, got %d results", len(filtered))
	}
	if stats.DroppedEpisodeRequest != 0 || stats.DroppedTitle != 0 {
		t.Fatalf("expected no rejection for dashed season/episode pattern, got %+v", stats)
	}
	if stats.AcceptedExactEpisode != 1 {
		t.Fatalf("expected exact episode match, got %+v", stats)
	}
}

func TestValidateSearchResultsWithStatsRejectsExtraTrailingTitleWords(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Rookie.Feds.S01E01.1080p.WEB-DL.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "The Rookie", "1", "1", true, false)

	if len(filtered) != 0 {
		t.Fatalf("expected trailing title words to be rejected, got %d results", len(filtered))
	}
	if stats.DroppedTitle != 1 {
		t.Fatalf("expected title rejection for extra trailing words, got %+v", stats)
	}
}

func TestRunIndexerSearchesQueryWithIDsDoesNotAlsoRunIDSearch(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		Query:           "Meal Ticket 2026",
		Cat:             "2000",
		Limit:           100,
		IMDbID:          "tt40232255",
		TMDBID:          "1649758",
		ValidationQuery: "Meal Ticket 2026",
	}

	if _, err := RunIndexerSearches(context.Background(), idx, req, "movie"); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}

	if len(idx.reqs) != 1 {
		t.Fatalf("expected exactly 1 Search call, got %d", len(idx.reqs))
	}
	if idx.reqs[0].SearchMode != "text" {
		t.Fatalf("expected query request to remain text-only, got mode %q", idx.reqs[0].SearchMode)
	}
	if idx.reqs[0].Query != "Meal Ticket 2026" {
		t.Fatalf("expected text query to be preserved, got %q", idx.reqs[0].Query)
	}
}

func TestRunIndexerSearchesIDModePreservesPreparedQuery(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		Cat:             "2000",
		Limit:           100,
		SearchMode:      "id",
		IMDbID:          "tt1655441",
		TMDBID:          "1655441",
		Query:           "The Age of Adaline",
		ValidationQuery: "The Age of Adaline",
	}

	if _, err := RunIndexerSearches(context.Background(), idx, req, "movie"); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}

	if len(idx.reqs) != 1 {
		t.Fatalf("expected exactly 1 Search call, got %d", len(idx.reqs))
	}
	if idx.reqs[0].SearchMode != "id" {
		t.Fatalf("expected id mode to stay id-only, got mode %q", idx.reqs[0].SearchMode)
	}
	if idx.reqs[0].Query != "The Age of Adaline" {
		t.Fatalf("expected id request to preserve prepared query, got %q", idx.reqs[0].Query)
	}
}

func TestRunIndexerSearchesReturnsTextSearchErrors(t *testing.T) {
	idx := &errIndexer{name: "BrokenIndexer", err: fmt.Errorf("backend unavailable")}
	req := indexer.SearchRequest{
		SearchMode:      "text",
		Query:           "The King Who Never Was",
		ValidationQuery: "The King Who Never Was",
		StreamLabel:     "TestStream",
		RequestLabel:    "Text Request",
	}

	_, err := RunIndexerSearches(context.Background(), idx, req, "series")
	if err == nil {
		t.Fatalf("expected text search error, got nil")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "text search failed") {
		t.Fatalf("expected wrapped text search error, got %q", got)
	}
}

// A text request without a title has nothing to search for.
func TestRunIndexerSearchesSkipsTextRequestWithoutValidationBasis(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		SearchMode: "text",
		Cat:        "2000",
		IMDbID:     "tt1655441",
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "movie")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no releases without validation basis, got %d", len(got))
	}
	if len(idx.reqs) != 0 {
		t.Fatalf("expected no Search call without validation basis, got %d", len(idx.reqs))
	}
}

// An ID request needs an id, not a title. Metadata that resolved an id but no
// usable title used to sit the whole request out, which spent the one search
// that did not need the title at all.
func TestRunIndexerSearchesRunsIDRequestWithoutValidationBasis(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		SearchMode: "id",
		Cat:        "2000",
		IMDbID:     "tt1655441",
	}

	if _, err := RunIndexerSearches(context.Background(), idx, req, "movie"); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(idx.reqs) != 1 {
		t.Fatalf("expected the id search to run, got %d Search calls", len(idx.reqs))
	}
	if idx.reqs[0].SearchMode != "id" {
		t.Fatalf("expected an id request, got mode %q", idx.reqs[0].SearchMode)
	}
}

// An ID request trusts the indexer: it asked by imdbid/tvdbid, so a release
// whose scene name diverges from the metadata title is kept, and counted.
func TestRunIndexerSearchesIDRequestKeepsTitleMismatches(t *testing.T) {
	idx := &staticIndexer{
		name: "SceneNZBs",
		resp: &indexer.SearchResponse{
			Channel: indexer.Channel{
				Items: []indexer.Item{
					{Title: "Special.Ops.Lioness.S02E01.1080p.WEB.h264-GROUP", ActualIndexer: "SceneNZBs"},
				},
			},
		},
	}
	req := indexer.SearchRequest{
		SearchMode:      "id",
		Cat:             "5000",
		TVDBID:          "420299",
		Season:          "2",
		Episode:         "1",
		ValidationQuery: "Lioness",
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "series")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the id result to survive validation, got %d", len(got))
	}
}

// The same release under a text request: the title is enforced there, but the
// season/episode backs it up, so a prefix the metadata title dropped is fine.
func TestValidateSearchResultsAcceptsPrefixedSceneTitleForEpisodeRequest(t *testing.T) {
	releases := []*release.Release{
		{Title: "Special.Ops.Lioness.S02E01.1080p.WEB.h264-GROUP"},
		{Title: "Some.Other.Show.S02E01.1080p.WEB.h264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Lioness", "2", "1", true, false)

	if len(filtered) != 1 || filtered[0] != releases[0] {
		t.Fatalf("expected the prefixed scene title kept and the other show dropped, got %d: %+v", len(filtered), stats)
	}
	if stats.DroppedTitle != 1 {
		t.Fatalf("expected 1 title rejection, got %+v", stats)
	}
}

// Not enforcing the title keeps the mismatch, and counts it: an aggregator
// that turned an unsupported id search into a title search shows up as a
// request that is nearly all mismatch, not as a request that looked fine.
func TestValidateSearchResultsCountsTitleMismatchWhenNotEnforced(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Patriot.2000.1080p.BluRay.x264-GROUP"},
		{Title: "Something.Else.Entirely.2000.1080p.BluRay.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "movie", "The Patriot 2000", "", "", false, true)

	if len(filtered) != 2 {
		t.Fatalf("expected both results kept when the title is not enforced, got %d", len(filtered))
	}
	if stats.TitleValidationApplied {
		t.Fatalf("expected title validation not to be enforced, got %+v", stats)
	}
	if stats.DroppedTitle != 0 {
		t.Fatalf("expected no title drops, got %+v", stats)
	}
	if stats.TitleMismatchKept != 1 {
		t.Fatalf("expected 1 counted title mismatch, got %+v", stats)
	}
}

func TestRunIndexerSearchesUsesValidationQueriesWhenPresent(t *testing.T) {
	idx := &staticIndexer{
		name: "SceneNZBs",
		resp: &indexer.SearchResponse{
			Channel: indexer.Channel{
				Items: []indexer.Item{
					{Title: "Koenig.der.Loewen.1994.1080p.BluRay.x264-GROUP", ActualIndexer: "SceneNZBs"},
				},
			},
		},
	}
	req := indexer.SearchRequest{
		SearchMode:           "id",
		IMDbID:               "tt0110357",
		TMDBID:               "8587",
		ValidationQueries:    []string{"The Lion King 1994", "Koenig der Loewen 1994"},
		EnableYearValidation: true,
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "movie")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected multilingual validation queries to keep the result, got %d", len(got))
	}
}

func TestValidationQueriesForRequestUsesProfiles(t *testing.T) {
	req := indexer.SearchRequest{
		ValidationQueryProfiles: []indexer.ValidationQueryProfile{
			{Languages: []string{"en-US"}, Query: "Witch Hat Atelier"},
			{Languages: []string{"original"}, Query: "Tongari Boushi no Atelier"},
		},
	}

	got := validationQueriesForRequest(req)
	want := []string{"Witch Hat Atelier", "Tongari Boushi no Atelier"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validationQueriesForRequest() = %#v, want %#v", got, want)
	}
}

func TestValidationProfilesForRequestPreferExplicitProfiles(t *testing.T) {
	req := indexer.SearchRequest{
		ValidationQueryProfiles: []indexer.ValidationQueryProfile{
			{Languages: []string{"en-US"}, Query: "Witch Hat Atelier"},
			{Languages: []string{"original"}, Query: "Tongari Boushi no Atelier"},
		},
		ValidationQueries: []string{"ignored"},
	}

	got := validationProfilesForRequest(req)
	want := []indexer.ValidationQueryProfile{
		{Languages: []string{"en-US"}, Query: "Witch Hat Atelier"},
		{Languages: []string{"original"}, Query: "Tongari Boushi no Atelier"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validationProfilesForRequest() = %#v, want %#v", got, want)
	}
}

// The counters say a release was dropped; they never say which one. Issue #175
// is that question — "no results for tt0113957, why?" — and a count cannot
// answer it.
func TestValidateSearchResultsRecordsWhichReleasesWereDropped(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Patriot.2000.1080p.BluRay.x264-GROUP", Indexer: "NZBGeek"},
		{Title: "The.Patriot.2004.1080p.BluRay.x264-GROUP", Indexer: "NZBGeek"},
		{Title: "Der.Patriot.2000.1080p.BluRay.x264-GROUP", Indexer: "DrunkenSlug"},
	}

	filtered, stats := ValidateSearchResultsWithOptions(releases, "movie", []string{"The Patriot 2000"}, ValidationOptions{
		EnforceTitle:  true,
		EnforceYear:   true,
		RecordDropped: 10,
	})
	if len(filtered) != 1 {
		t.Fatalf("expected 1 accepted result, got %d", len(filtered))
	}
	if len(stats.Dropped) != 2 {
		t.Fatalf("expected both drops recorded, got %+v", stats.Dropped)
	}
	if stats.DroppedOmitted != 0 {
		t.Errorf("nothing should be omitted under a cap of 10, got %d", stats.DroppedOmitted)
	}

	byTitle := map[string]diag.DroppedRelease{}
	for _, d := range stats.Dropped {
		byTitle[d.Title] = d
	}
	year := byTitle["The.Patriot.2004.1080p.BluRay.x264-GROUP"]
	if year.Reason != diag.DropReasonYear || year.Stage != diag.DropStageValidation {
		t.Errorf("the 2004 release should be a year drop, got %+v", year)
	}
	// The detail is the whole point: "expected 2000, got 2004" is the answer,
	// where "dropped_year: 1" is only the symptom.
	if !strings.Contains(year.Detail, "2000") || !strings.Contains(year.Detail, "2004") {
		t.Errorf("year detail should name both years, got %q", year.Detail)
	}
	title := byTitle["Der.Patriot.2000.1080p.BluRay.x264-GROUP"]
	if title.Reason != diag.DropReasonTitle {
		t.Errorf("the German title should be a title drop, got %+v", title)
	}
	if title.Indexer != "DrunkenSlug" {
		t.Errorf("the drop should name its indexer, got %q", title.Indexer)
	}
}

// An indexer answering a query with hundreds of wrong results must not put
// hundreds of names into a record that is persisted per search. The cap keeps
// the list bounded, and the tally keeps the truncation visible — a list
// quietly cut short reads as a complete one.
func TestValidateSearchResultsCapsRecordedDrops(t *testing.T) {
	releases := make([]*release.Release, 0, 20)
	for i := 0; i < 20; i++ {
		releases = append(releases, &release.Release{Title: fmt.Sprintf("Wrong.Movie.%d.1080p.BluRay.x264-GROUP", 1980+i)})
	}

	_, stats := ValidateSearchResultsWithOptions(releases, "movie", []string{"The Patriot 2000"}, ValidationOptions{
		EnforceTitle:  true,
		RecordDropped: 3,
	})
	if len(stats.Dropped) != 3 {
		t.Fatalf("expected the cap to hold at 3, got %d", len(stats.Dropped))
	}
	if want := stats.DroppedTitle - 3; stats.DroppedOmitted != want {
		t.Errorf("DroppedOmitted = %d, want %d", stats.DroppedOmitted, want)
	}
	if stats.DroppedTitle != 20 {
		t.Errorf("the counter must stay complete regardless of the cap, got %d", stats.DroppedTitle)
	}
}

// Recording is opt-in so the per-profile debug sweep, which re-validates every
// raw release purely for counts, does not build a list nothing reads.
func TestValidateSearchResultsRecordsNothingByDefault(t *testing.T) {
	releases := []*release.Release{{Title: "Der.Patriot.2000.1080p.BluRay.x264-GROUP"}}

	_, stats := ValidateSearchResultsWithOptions(releases, "movie", []string{"The Patriot 2000"}, ValidationOptions{
		EnforceTitle: true,
	})
	if stats.DroppedTitle != 1 {
		t.Fatalf("expected the drop to be counted, got %+v", stats)
	}
	if len(stats.Dropped) != 0 || stats.DroppedOmitted != 0 {
		t.Errorf("nothing should be recorded without RecordDropped, got %+v (%d omitted)", stats.Dropped, stats.DroppedOmitted)
	}
}
