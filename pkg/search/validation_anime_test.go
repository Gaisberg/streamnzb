package search

import (
	"slices"
	"testing"

	"streamnzb/pkg/release"
)

// validate runs the validator over titles and returns the kept ones in order.
func validate(t *testing.T, contentType string, queries []string, opts ValidationOptions, titles ...string) []string {
	t.Helper()
	rels := make([]*release.Release, 0, len(titles))
	for _, title := range titles {
		rels = append(rels, &release.Release{Title: title})
	}
	out, _ := ValidateSearchResultsWithOptions(rels, contentType, queries, opts)
	kept := make([]string, 0, len(out))
	for _, r := range out {
		kept = append(kept, r.Title)
	}
	return kept
}

func assertKept(t *testing.T, name string, got []string, kept, dropped []string) {
	t.Helper()
	for _, title := range kept {
		if !slices.Contains(got, title) {
			t.Errorf("%s: %q dropped, want kept", name, title)
		}
	}
	for _, title := range dropped {
		if slices.Contains(got, title) {
			t.Errorf("%s: %q kept, want dropped", name, title)
		}
	}
}

// When TVDB and TMDB split seasons differently the derived absolute number
// names another episode (TVDB S1 of 50 episodes, TMDB 25+25: S02E03 derives
// 28, TVDB says 53). TVDB's veto leaves the number unknown, and with no
// absolute number neither validator accepts the wrong "- 28"; nor can they
// recognise "- 53", which playback could not match either. A regular series
// whose plan asks by no absolute number carries none and keeps its strict
// season/episode rules; one whose plan does recognises "- 28" as before.
func TestValidateUnknownAbsoluteAcceptsNoWrongEpisode(t *testing.T) {
	titles := []string{"Show.S02E03.1080p.WEB-DL-GRP", "[Grp] Show - 28 [1080p]", "[Grp] Show - 53 [1080p]", "Show.S01E28.1080p.WEB-DL-GRP"}
	unknown := ValidationOptions{Season: "2", Episode: "3", AcceptPacks: true}
	for _, ct := range []string{"anime", "series"} {
		assertKept(t, ct+", absolute unknown", validate(t, ct, []string{"Show"}, unknown, titles...),
			[]string{"Show.S02E03.1080p.WEB-DL-GRP"},
			[]string{"[Grp] Show - 28 [1080p]", "[Grp] Show - 53 [1080p]", "Show.S01E28.1080p.WEB-DL-GRP"})
	}
	known := unknown
	known.AbsoluteEpisode = "28"
	assertKept(t, "series, plan asks by absolute", validate(t, "series", []string{"Show"}, known, titles...),
		[]string{"Show.S02E03.1080p.WEB-DL-GRP", "[Grp] Show - 28 [1080p]", "Show.S01E28.1080p.WEB-DL-GRP"},
		[]string{"[Grp] Show - 53 [1080p]"})
}

// A title-addressed anime attempt sent a keyword, so what came back is the
// show's only if its title says so: an unrelated show with the right episode
// number is dropped, while every title the metadata knows the show by —
// canonical, English, romaji, synonyms — still matches. An id-addressed
// attempt asked for the show itself and stays lenient about fansub names.
func TestValidateSearchResultsAnimeTitleAttempts(t *testing.T) {
	// Kitsu's titles for the entry, as ValidationQueryProfilesFromMetadata
	// lists them: canonical, English, romaji, then synonyms.
	titles := []string{"Sousou no Frieren", "Frieren Beyond Journeys End", "Frieren", "Frieren at the Funeral"}
	releases := []string{
		"[SubsPlease] Sousou no Frieren - 04 (1080p) [ABCD1234]",
		"Frieren.Beyond.Journeys.End.S01E04.1080p.WEB-DL-GRP",
		"[Grp] Frieren - 04 [1080p]",
		"[Grp] Frieren at the Funeral - 04 [1080p]",
		"[Grp] Kusuriya no Hitorigoto - 04 [1080p]",
		"Totally.Different.Show.S01E04.1080p.WEB-DL-GRP",
	}
	right, wrong := releases[:4], releases[4:]
	opts := ValidationOptions{Season: "1", Episode: "4", AbsoluteEpisode: "4", EnforceTitle: true, AcceptPacks: true}
	assertKept(t, "title attempt", validate(t, "anime", titles, opts, releases...), right, wrong)

	opts.EnforceTitle = false
	assertKept(t, "id attempt", validate(t, "anime", titles, opts, releases...), releases, nil)
	rels := make([]*release.Release, 0, len(releases))
	for _, title := range releases {
		rels = append(rels, &release.Release{Title: title})
	}
	// The mismatches are counted, as for a series, so diagnostics still show
	// an indexer that answered an id search with a keyword search.
	if _, stats := ValidateSearchResultsWithOptions(rels, "anime", titles, opts); stats.TitleMismatchKept != len(wrong) || stats.TitleValidationApplied {
		t.Fatalf("id attempt: mismatches=%d applied=%v, want %d counted and none enforced", stats.TitleMismatchKept, stats.TitleValidationApplied, len(wrong))
	}
}

// An anime request for a whole season is held to the season and pack rules a
// series one is: another season is dropped, and with packs off so is every
// pack, season or complete. A title that names no season at all is dropped
// by the season check, as a series request has always done.
func TestValidateSearchResultsAnimeSeasonRequest(t *testing.T) {
	releases := []string{
		"Show.S02E05.1080p.WEB-DL-GRP",
		"[Grp] Show S2 - 05 [1080p]",
		"Show S02 1080p BluRay x264",
		"Show S01-S03 Complete 1080p BluRay x264",
		"Show.S03E01.1080p.WEB-DL-GRP",
		"Show S01 1080p BluRay x264",
		"[Grp] Show - 05 [1080p]",
		"[Grp] Show - Complete Series [BD]",
	}
	episodes, packs, wrong := releases[:2], releases[2:4], releases[4:]
	for _, ct := range []string{"anime", "series"} {
		on := ValidationOptions{Season: "2", AcceptPacks: true}
		assertKept(t, ct+", packs on", validate(t, ct, nil, on, releases...), append(slices.Clone(episodes), packs...), wrong)
		off := ValidationOptions{Season: "2"}
		assertKept(t, ct+", packs off", validate(t, ct, nil, off, releases...), episodes, append(slices.Clone(packs), wrong...))
	}
}

// An OVA tag explains a release only for a special. A regular episode is never
// an OVA, so an OVA release that does not name the episode is another one,
// numbered or not. For a special, OVA numbering cannot be lined up with TVDB's
// season 0 and stays ambiguous, and a season pack tagged OVA may bundle it —
// subject, like every pack, to the plan's pack setting.
func TestValidateSearchResultsAnimeOVA(t *testing.T) {
	regular := ValidationOptions{Season: "1", Episode: "5", AbsoluteEpisode: "5", AcceptPacks: true}
	assertKept(t, "S01E05", validate(t, "anime", nil, regular,
		"Show.S01E05.1080p.WEB-DL-GRP", "[Grp] Show - 05 [1080p]", "[Grp] Show (TV + OVA) - 05 [1080p]",
		"[Grp] Show (TV + OVA) [BD 1080p]", "[Grp] Show TV + OVA Batch [BD]",
		"Show.TV.+.OVA.720p.BluRay.x264-GRP", "[Group] Show + OVA 10bit [BD 1080p]", "[Group] Show OVA 5.1 [BD]",
		"[Grp] Show - 23 [OVA]", "[Grp] Show OVA 2 [1080p]", "[Grp] Show OVA-02 [1080p]", "[Grp] Show - OVA 2 [1080p]",
		"[Grp]_Show_OVA_02_[1080p]", "[Grp] Show - OVA2 [1080p]", "Show.S01E23.OVA.1080p.WEB-DL-GRP",
		"Show.OVA.02.1080p.BluRay.x264-GRP", "Show.OVA.2.1080p.BluRay.x264-GRP", "[Grp] Show OVA 02[1080p]",
		"[Grp] Show OVA 02v2 [1080p]", "[Grp] Show OVA 02(1080p)", "[Grp] Show OVA 02: Title [1080p]",
		"[Grp] Show OVA 02! [1080p]", "【Grp】Show OVA 02【1080p】", "[Grp] Show OVA 2/3 [1080p]",
		"[Grp] Show - OVA 5 [1080p]"),
		// An unnumbered TV + OVA bundle names no other episode, and a
		// resolution, bit depth or channel count is no OVA number.
		[]string{"Show.S01E05.1080p.WEB-DL-GRP", "[Grp] Show - 05 [1080p]",
			"[Grp] Show (TV + OVA) [BD 1080p]", "[Grp] Show TV + OVA Batch [BD]",
			"Show.TV.+.OVA.720p.BluRay.x264-GRP", "[Group] Show + OVA 10bit [BD 1080p]", "[Group] Show OVA 5.1 [BD]"},
		[]string{"[Grp] Show - 23 [OVA]", "[Grp] Show OVA 2 [1080p]", "[Grp] Show OVA-02 [1080p]", "[Grp] Show - OVA 2 [1080p]",
			"[Grp]_Show_OVA_02_[1080p]", "[Grp] Show - OVA2 [1080p]", "Show.S01E23.OVA.1080p.WEB-DL-GRP",
			// Dotted, glued, versioned and otherwise punctuated numbers.
			"Show.OVA.02.1080p.BluRay.x264-GRP", "Show.OVA.2.1080p.BluRay.x264-GRP", "[Grp] Show OVA 02[1080p]",
			"[Grp] Show OVA 02v2 [1080p]", "[Grp] Show OVA 02(1080p)", "[Grp] Show OVA 02: Title [1080p]",
			"[Grp] Show OVA 02! [1080p]", "【Grp】Show OVA 02【1080p】", "[Grp] Show OVA 2/3 [1080p]",
			// A regular TV episode is not an OVA that shares its number:
			// without metadata saying the entry is an OVA series, the tag
			// rules the release out however it is numbered.
			"[Grp] Show - OVA 5 [1080p]", "[Grp] Show (TV + OVA) - 05 [1080p]"})

	// An OVA series — Kitsu types the entry OVA — is listed as season 1: its
	// OVAs are its episodes, a range among them. The same titles without that
	// metadata are judged as for a TV series and dropped.
	hellsing := []string{"[Grp] Hellsing Ultimate OVA 02 (BD 1080p)", "[Grp] Hellsing Ultimate - OVA - 02 [1080p]", "[Grp] Hellsing Ultimate OVA 03 (BD 1080p)"}
	ovaSeries := ValidationOptions{Season: "1", Episode: "2", AbsoluteEpisode: "2", AcceptPacks: true, OVASeries: true}
	assertKept(t, "OVA series S01E02", validate(t, "anime", nil, ovaSeries, hellsing...), hellsing[:2], hellsing[2:])
	noAbsolute := ValidationOptions{Season: "1", Episode: "5", AcceptPacks: true, OVASeries: true}
	assertKept(t, "OVA series S01E05, absolute unknown", validate(t, "anime", nil, noAbsolute,
		"[Grp] Hellsing Ultimate OVA 01-10 [BD 1080p]"), []string{"[Grp] Hellsing Ultimate OVA 01-10 [BD 1080p]"}, nil)
	ovaSeries.OVASeries = false
	assertKept(t, "S01E02, show type unknown", validate(t, "anime", nil, ovaSeries, hellsing...), nil, hellsing)

	// A later season's episode is never an OVA numbered like it, and an
	// episode number the parser read outranks the OVA's own.
	assertKept(t, "S02E01", validate(t, "anime", nil, ValidationOptions{Season: "2", Episode: "1", AbsoluteEpisode: "13", AcceptPacks: true},
		"[Grp] Show OVA 1 [1080p]"), nil, []string{"[Grp] Show OVA 1 [1080p]"})
	assertKept(t, "S01E01", validate(t, "anime", nil, ValidationOptions{Season: "1", Episode: "1", AbsoluteEpisode: "1", AcceptPacks: true},
		"[Grp] Show - 25 (OVA 1) [1080p]", "[Grp] Show - 13 [OVA 1]"), nil, []string{"[Grp] Show - 25 (OVA 1) [1080p]", "[Grp] Show - 13 [OVA 1]"})
	// Nor is an explicit other season excused by the tag, and a season tag
	// beside an OVA number makes it no season pack.
	assertKept(t, "S01E05, other season", validate(t, "anime", nil, ValidationOptions{Season: "1", Episode: "5", AbsoluteEpisode: "5", AcceptPacks: true},
		"Show.S02E05.OVA.1080p.WEB-DL-GRP", "[Grp] Show S2 - 05 [OVA] [1080p]"), nil,
		[]string{"Show.S02E05.OVA.1080p.WEB-DL-GRP", "[Grp] Show S2 - 05 [OVA] [1080p]"})
	assertKept(t, "S02E01, season-tagged OVA", validate(t, "anime", nil, ValidationOptions{Season: "2", Episode: "1", AbsoluteEpisode: "13", AcceptPacks: true},
		"[Grp] Show S01 OVA 2 [1080p]"), nil, []string{"[Grp] Show S01 OVA 2 [1080p]"})
	// A specials pack holds no regular episode.
	assertKept(t, "S02E01, specials pack", validate(t, "anime", nil, ValidationOptions{Season: "2", Episode: "1", AbsoluteEpisode: "13", AcceptPacks: true},
		"[Grp] Show S00 [BD]", "[Grp] Show S01 [BD]"), []string{"[Grp] Show S01 [BD]"}, []string{"[Grp] Show S00 [BD]"})
	assertKept(t, "S01E02, season-1 OVA series", validate(t, "anime", nil, ValidationOptions{Season: "1", Episode: "2", AbsoluteEpisode: "2", AcceptPacks: true},
		"[Grp] Show S01 OVA 2 [1080p]"), []string{"[Grp] Show S01 OVA 2 [1080p]"}, nil)

	special := ValidationOptions{Season: "0", Episode: "2", AcceptPacks: true}
	ancillary := []string{"Show.S00E05.Sample.1080p.WEB-DL-GRP", "Show.S00E05.Trailer.1080p.WEB-DL-GRP",
		"Show.S00E05.Featurette.1080p.WEB-DL-GRP", "[Grp] Show S01 [BD][NCOP+NCED]"}
	ambiguous := []string{"Show.S00E02.1080p.WEB-DL-GRP", "[Grp] Show - OVA 2 [1080p]", "[Grp] Show OVA - 02 [1080p]", "[Grp] Show - 05 [OVA]",
		"[Grp] Show - OVA2 [1080p]"}
	ovaPack := []string{"[Grp] Show S01 + OVA [BD 1080p]"}
	all := append(append(append([]string{}, ambiguous...), ovaPack...), ancillary...)
	assertKept(t, "S00E02, packs on", validate(t, "anime", nil, special, all...), append(slices.Clone(ambiguous), ovaPack...), ancillary)
	special.AcceptPacks = false
	assertKept(t, "S00E02, packs off", validate(t, "anime", nil, special, all...), ambiguous, append(slices.Clone(ovaPack), ancillary...))

	// An earlier season's pack tagged OVA may hold a regular episode numbered
	// absolutely; a later season's cannot.
	later := ValidationOptions{Season: "2", Episode: "1", AbsoluteEpisode: "13", AcceptPacks: true}
	packs := []string{"[Grp] Show S01 + OVA [BD]", "[Grp] Show S03 + OVA [BD]"}
	assertKept(t, "S02E01, packs on", validate(t, "anime", nil, later, packs...), packs[:1], packs[1:])
	later.AcceptPacks = false
	assertKept(t, "S02E01, packs off", validate(t, "anime", nil, later, packs...), nil, packs)
}

// A release named only by an air date is judged by that date: another night's
// is dropped. A day either side stays, since a late-night broadcast (25:30
// JST) is often dated the next calendar day. With the episode's date unknown
// nothing confirms the release is this episode, and it is dropped.
func TestValidateSearchResultsAnimeAirDate(t *testing.T) {
	titles := []string{"Show.2024.01.14.1080p.WEB-DL-GRP", "Show.2024.01.15.1080p.WEB-DL-GRP", "Show.2024.01.07.1080p.WEB-DL-GRP"}
	dated := ValidationOptions{Season: "2", Episode: "3", AbsoluteEpisode: "28", AirDate: "2024-01-14", AcceptPacks: true}
	assertKept(t, "air date known", validate(t, "anime", nil, dated, titles...), titles[:2], titles[2:])
	dated.AirDate = ""
	assertKept(t, "air date unknown", validate(t, "anime", nil, dated, titles...), nil, titles)
}

// A Kitsu entry spanning the series asks by absolute number alone, and nothing
// in the mapping says which season holds it: a pack of any regular season may,
// so it is kept as ambiguous — subject to the plan's pack setting — while a
// specials pack never holds a regular episode.
func TestValidateSearchResultsAnimeSeasonlessPacks(t *testing.T) {
	packs := []string{"[Group] Show S03 [BD 1080p]", "[Group] Show S01 [BD 1080p]"}
	on := ValidationOptions{Episode: "5", AbsoluteEpisode: "5", AcceptPacks: true}
	assertKept(t, "packs on", validate(t, "anime", nil, on, append([]string{"[Group] Show S00 [BD]"}, packs...)...), packs, []string{"[Group] Show S00 [BD]"})
	off := on
	off.AcceptPacks = false
	assertKept(t, "packs off", validate(t, "anime", nil, off, packs...), nil, packs)
}

// A plan that asks for the year gets it for anime too: a same-title show from
// another year is dropped, while a fansub name carrying no year passes.
func TestValidateSearchResultsAnimeYear(t *testing.T) {
	opts := ValidationOptions{Season: "1", Episode: "5", AbsoluteEpisode: "5", EnforceYear: true, AcceptPacks: true}
	assertKept(t, "year enforced", validate(t, "anime", []string{"Hunter x Hunter 2011"}, opts,
		"Hunter x Hunter (2011) - 05 [1080p]", "[SubsPlease] Hunter x Hunter - 05 (1080p)", "Hunter x Hunter (1999) - 05 [1080p]"),
		[]string{"Hunter x Hunter (2011) - 05 [1080p]", "[SubsPlease] Hunter x Hunter - 05 (1080p)"},
		[]string{"Hunter x Hunter (1999) - 05 [1080p]"})
	opts.EnforceYear = false
	assertKept(t, "year not enforced", validate(t, "anime", []string{"Hunter x Hunter 2011"}, opts,
		"Hunter x Hunter (1999) - 05 [1080p]"), []string{"Hunter x Hunter (1999) - 05 [1080p]"}, nil)
}

// A numbered batch that bundles an OVA names the episodes it holds, so the OVA
// tag does not turn it away.
func TestValidateSearchResultsAnimeOVABatch(t *testing.T) {
	opts := ValidationOptions{Season: "1", Episode: "5", AbsoluteEpisode: "5", AcceptPacks: true}
	batches := []string{"[Group] Show (01-12 + OVA) [BD 1080p]", "[Group] Show - 01-12 + OVA [BD 1080p]"}
	assertKept(t, "S01E05", validate(t, "anime", nil, opts, batches...), batches, nil)
}
