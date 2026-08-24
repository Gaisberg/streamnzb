package rules_test

import (
	"strings"
	"testing"
	"time"

	jhin "github.com/dreulavelle/jhin/parser"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/rules"
	"streamnzb/pkg/search/triage"
)

func compile(t *testing.T, cfgs ...config.RuleConfig) *rules.Set {
	t.Helper()
	set, err := rules.Compile(cfgs)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return set
}

// envFor builds the environment the way ranking does: a parsed title plus
// whatever the candidate carries.
func envFor(title string, mutate func(*triage.Candidate)) rules.Env {
	cand := triage.Candidate{Release: &release.Release{Title: title}}
	if mutate != nil {
		mutate(&cand)
	}
	return rules.BuildEnv(cand, jhin.Parse(title), rules.Context{Kind: "movie"})
}

func TestScoreRuleMatchesByName(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "IMAX",
		When:   `releaseName matches "(?i)\\bIMAX\\b"`,
		Points: 1000,
	})

	got := set.Evaluate(envFor("Movie 2020 IMAX 2160p BluRay-GRP", nil), "movie")
	if got.Points != 1000 {
		t.Errorf("Points = %d, want 1000", got.Points)
	}
	if len(got.Matched) != 1 || got.Matched[0].Name != "IMAX" {
		t.Errorf("Matched = %+v, want one IMAX entry", got.Matched)
	}

	if got := set.Evaluate(envFor("Movie 2020 2160p BluRay-GRP", nil), "movie"); got.Points != 0 {
		t.Errorf("non-matching release scored %d, want 0", got.Points)
	}
}

// The DV-only case from the issue that started this: a Dolby Vision release
// carrying an HDR10 fallback is fine, one without it is not. No lookahead, no
// new parsed trait — the release name already lists both.
func TestDolbyVisionWithoutFallbackFromTitle(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "DV without HDR fallback",
		When:   `dolbyVision and not hdrFallback`,
		Action: config.RuleActionReject,
	})

	tests := []struct {
		title      string
		wantReject bool
	}{
		{"Movie 2020 2160p BluRay REMUX DV HEVC-GRP", true},
		{"Movie 2020 2160p BluRay REMUX DV HDR10 HEVC-GRP", false},
		{"Movie 2020 2160p BluRay REMUX HDR10 HEVC-GRP", false},
		{"Movie 2020 2160p BluRay REMUX HEVC-GRP", false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := set.Evaluate(envFor(tt.title, nil), "movie")
			if rejected := len(got.Rejections) > 0; rejected != tt.wantReject {
				t.Errorf("rejected = %v, want %v (hdr from title)", rejected, tt.wantReject)
			}
		})
	}
}

// The same question answered from the file rather than from its name. The
// probe reports Dolby Vision and the base layer separately, so "DV only" is a
// measurement, not an inference.
func TestDolbyVisionWithoutFallbackFromProbe(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "DV only, measured",
		When:   `probed.dolbyVision and not probed.hasHDRFallback`,
		Action: config.RuleActionReject,
	})

	dvOnly := envFor("Movie 2020 2160p BluRay-GRP", func(c *triage.Candidate) {
		c.Verdict.Probed = &release.MediaCaps{VideoCodec: "hevc", Height: 2160, DolbyVision: true}
	})
	if got := set.Evaluate(dvOnly, "movie"); len(got.Rejections) == 0 {
		t.Error("a measured DV-only release was not rejected")
	}

	dvWithFallback := envFor("Movie 2020 2160p BluRay-GRP", func(c *triage.Candidate) {
		c.Verdict.Probed = &release.MediaCaps{VideoCodec: "hevc", Height: 2160, DolbyVision: true, HDR: "HDR10"}
	})
	if got := set.Evaluate(dvWithFallback, "movie"); len(got.Rejections) > 0 {
		t.Errorf("a measured DV+HDR10 release was rejected: %v", got.Rejections)
	}
}

// What lookarounds were being asked for: a condition over two attributes at
// once. RE2 cannot express it in one pattern; a rule does not need to.
func TestConditionCombinesAttributes(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "Oversized unless 4K",
		When:   `sizeGB > 30 and resolution != "2160p"`,
		Action: config.RuleActionReject,
	})

	big1080 := envFor("Movie 2020 1080p BluRay REMUX-GRP", func(c *triage.Candidate) {
		c.Release.Size = 40e9
	})
	if got := set.Evaluate(big1080, "movie"); len(got.Rejections) == 0 {
		t.Error("a 40 GB 1080p release was not rejected")
	}

	big2160 := envFor("Movie 2020 2160p BluRay REMUX-GRP", func(c *triage.Candidate) {
		c.Release.Size = 40e9
	})
	if got := set.Evaluate(big2160, "movie"); len(got.Rejections) > 0 {
		t.Errorf("a 40 GB 2160p release was rejected: %v", got.Rejections)
	}
}

// Rules that read a tier the release has nothing in do not run. Without this,
// turning on one probe rule would empty every result list of everything except
// library hits.
func TestFailsOpenOnMissingTiers(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "SD reject", When: `probed.height < 1080`, Action: config.RuleActionReject},
		config.RuleConfig{Name: "Dead reject", When: `avail.status == "unavailable"`, Action: config.RuleActionReject},
		config.RuleConfig{Name: "Unprobed penalty", When: `probed.bitDepth < 10`, Points: -5000},
	)

	got := set.Evaluate(envFor("Movie 2020 1080p WEB-DL-GRP", nil), "movie")
	if len(got.Rejections) > 0 {
		t.Errorf("an unprobed, unchecked release was rejected: %v", got.Rejections)
	}
	if got.Points != 0 {
		t.Errorf("an unprobed release was docked %d points", got.Points)
	}
	if len(got.Skipped) != 3 {
		t.Errorf("Skipped = %v, want all three rules reported", got.Skipped)
	}
	for _, s := range got.Skipped {
		if !strings.Contains(s, "probed") && !strings.Contains(s, "availability") {
			t.Errorf("skip reason %q names neither the probe nor availability", s)
		}
	}
}

// Once the data is there, the same rules judge normally.
func TestRunsOnceTheDataIsThere(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name: "SD reject", When: `probed.height < 1080`, Action: config.RuleActionReject,
	})

	sd := envFor("Movie 2020 WEB-DL-GRP", func(c *triage.Candidate) {
		c.Verdict.Probed = &release.MediaCaps{Height: 480}
	})
	if got := set.Evaluate(sd, "movie"); len(got.Rejections) == 0 {
		t.Error("a probed 480p release was not rejected")
	}
}

// Availability is three-valued. Reported bad and never reported are different
// claims and only one of them is evidence.
func TestAvailabilityTriState(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name: "Dead", When: `avail.status == "unavailable"`, Action: config.RuleActionReject,
	})

	dead := envFor("Movie 2020 1080p WEB-DL-GRP", func(c *triage.Candidate) {
		c.Verdict.Avail = triage.AvailState{Status: triage.AvailUnavailable}
	})
	if got := set.Evaluate(dead, "movie"); len(got.Rejections) == 0 {
		t.Error("a release reported unavailable was kept")
	}

	alive := envFor("Movie 2020 1080p WEB-DL-GRP", func(c *triage.Candidate) {
		c.Verdict.Avail = triage.AvailState{Status: triage.AvailAvailable}
	})
	if got := set.Evaluate(alive, "movie"); len(got.Rejections) > 0 {
		t.Error("a release reported available was rejected")
	}

	unknown := envFor("Movie 2020 1080p WEB-DL-GRP", nil)
	if got := set.Evaluate(unknown, "movie"); len(got.Rejections) > 0 {
		t.Error("a release nobody has reported was treated as reported bad")
	}
}

func TestAvailabilityBackboneAndAge(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "On our backbone", When: `avail.onMyBackbone`, Points: 500},
		config.RuleConfig{Name: "Fresh check", When: `avail.checkedDaysAgo >= 0 and avail.checkedDaysAgo < 30`, Points: 300},
	)

	env := envFor("Movie 2020 1080p WEB-DL-GRP", func(c *triage.Candidate) {
		c.Verdict.Avail = triage.AvailState{
			Status:       triage.AvailAvailable,
			OnMyBackbone: true,
			CheckedAt:    time.Now().Add(-72 * time.Hour),
		}
	})
	if got := set.Evaluate(env, "movie"); got.Points != 800 {
		t.Errorf("Points = %d, want 800", got.Points)
	}
}

// Bare attribute names prefer what was measured; parsed.* always reports what
// the name said, so a rule can insist on either.
func TestMergedAttributesPreferTheProbe(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Really 4K", When: `resolution == "2160p"`, Points: 100},
		config.RuleConfig{Name: "Claims 4K", When: `parsed.resolution == "2160p"`, Points: 10},
	)

	// The name says 2160p; the file is 1080p.
	mislabelled := envFor("Movie 2020 2160p WEB-DL-GRP", func(c *triage.Candidate) {
		c.Verdict.Probed = &release.MediaCaps{Height: 1080}
	})
	got := set.Evaluate(mislabelled, "movie")
	if got.Points != 10 {
		t.Errorf("Points = %d, want only the parsed rule's 10 — the probe should win the bare name", got.Points)
	}

	unprobed := envFor("Movie 2020 2160p WEB-DL-GRP", nil)
	if got := set.Evaluate(unprobed, "movie"); got.Points != 110 {
		t.Errorf("Points = %d, want 110 — with no probe the bare name falls back to the title", got.Points)
	}
}

func TestScopeLimitsRuleToOneKind(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name: "Anime only", Scope: "anime_show", When: `true`, Points: 100,
	})

	if got := set.Evaluate(envFor("Show S01E01 1080p WEB-DL-GRP", nil), "anime_show"); got.Points != 100 {
		t.Errorf("Points = %d for the scoped kind, want 100", got.Points)
	}
	if got := set.Evaluate(envFor("Show S01E01 1080p WEB-DL-GRP", nil), "series"); got.Points != 0 {
		t.Errorf("Points = %d for another kind, want 0", got.Points)
	}
}

func TestDisabledRuleDoesNothing(t *testing.T) {
	off := false
	set := compile(t, config.RuleConfig{
		Name: "Off", When: `true`, Points: 100, Enabled: &off,
	})
	if set.Len() != 0 {
		t.Fatalf("Len = %d, want 0 — a disabled rule should not compile in", set.Len())
	}
	if got := set.Evaluate(envFor("Movie 2020 1080p WEB-DL-GRP", nil), "movie"); got.Points != 0 {
		t.Errorf("a disabled rule scored %d", got.Points)
	}
}

// A rule that cannot compile fails the profile, naming the rule. A profile
// that silently dropped one would filter differently than configured and give
// no sign of it.
func TestCompileErrorsNameTheRule(t *testing.T) {
	tests := []struct {
		name string
		rule config.RuleConfig
	}{
		{"unknown attribute", config.RuleConfig{Name: "Bad field", When: `seeders > 10`}},
		{"not a boolean", config.RuleConfig{Name: "Not bool", When: `sizeGB`}},
		{"syntax error", config.RuleConfig{Name: "Syntax", When: `sizeGB >`}},
		{"empty condition", config.RuleConfig{Name: "Empty", When: "  "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rules.Compile([]config.RuleConfig{tt.rule})
			if err == nil {
				t.Fatal("Compile succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.rule.Name) {
				t.Errorf("error %q does not name the rule %q", err, tt.rule.Name)
			}
		})
	}
}

// A string that happens to mention a namespace is not a reference to it, so a
// rule matching the literal text "probed." still runs on unprobed releases.
func TestTierDetectionIgnoresStringLiterals(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "Mentions probed",
		When:   `releaseName contains "probed."`,
		Points: 1,
	})
	got := set.Evaluate(envFor("Movie 2020 probed. 1080p WEB-DL-GRP", nil), "movie")
	if len(got.Skipped) > 0 {
		t.Errorf("rule was skipped as probe-dependent: %v", got.Skipped)
	}
	if got.Points != 1 {
		t.Errorf("Points = %d, want 1", got.Points)
	}
}

// A release whose indexer reports no date is not brand new. Treating a missing
// date as age zero would let it win every freshness rule.
func TestMissingDateIsNotAgeZero(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name: "Fresh", When: `ageDays >= 0 and ageDays < 7`, Points: 100,
	})
	// The release has a size, so it is a real indexer result — just one whose
	// date the indexer never reported. Without the size the rule would be
	// skipped for lack of any NZB at all, which is a different thing.
	dateless := envFor("Movie 2020 1080p WEB-DL-GRP", func(c *triage.Candidate) {
		c.Release.Size = 8e9
	})
	if got := set.Evaluate(dateless, "movie"); got.Points != 0 {
		t.Errorf("a dateless release earned %d freshness points", got.Points)
	}

	fresh := envFor("Movie 2020 1080p WEB-DL-GRP", func(c *triage.Candidate) {
		c.Release.Size = 8e9
		c.Release.PubDate = time.Now().Add(-24 * time.Hour).Format(time.RFC1123Z)
	})
	if got := set.Evaluate(fresh, "movie"); got.Points != 100 {
		t.Errorf("a day-old release earned %d, want 100", got.Points)
	}
}

// Size, age and grab count come from the NZB. A bare release name has none, so
// a rule reading them is skipped and reported rather than judged against zeros
// — otherwise the preview would show a grabs rule rejecting everything when it
// will do nothing of the sort against real results.
func TestIndexerRulesSkipWithoutAnNZB(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Unpopular", When: "grabs < 5", Action: config.RuleActionReject},
		config.RuleConfig{Name: "Oversized", When: "sizeGB > 30", Action: config.RuleActionReject},
		config.RuleConfig{Name: "From my indexer", When: `indexer == "nzbgeek"`, Points: 100},
	)

	bare := set.Evaluate(envFor("Movie 2020 1080p WEB-DL-GRP", nil), "movie")
	if len(bare.Rejections) > 0 {
		t.Errorf("a bare release name was rejected on indexer data it does not have: %v", bare.Rejections)
	}
	if len(bare.Skipped) != 3 {
		t.Errorf("Skipped = %v, want all three reported", bare.Skipped)
	}
	for _, note := range bare.Skipped {
		if !strings.Contains(note, "size, age or grabs") {
			t.Errorf("skip reason %q does not say what is missing", note)
		}
	}
}

// A real indexer result carries an NZB, so the same rules judge it normally.
// This is the half that matters: the skip must never reach a live search.
func TestIndexerRulesRunOnRealResults(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name: "Unpopular", When: "grabs < 5", Action: config.RuleActionReject,
	})

	real := envFor("Movie 2020 1080p WEB-DL-GRP", func(c *triage.Candidate) {
		c.Release.Size = 8e9
		c.Release.Grabs = 2
	})
	got := set.Evaluate(real, "movie")
	if len(got.Skipped) > 0 {
		t.Errorf("a real indexer result skipped an indexer rule: %v", got.Skipped)
	}
	if len(got.Rejections) == 0 {
		t.Error("a release with 2 grabs was not rejected by a grabs rule")
	}
}

// A library release counts as having indexer data even with nothing else set:
// it came from somewhere and its provenance is exactly what the rule asks about.
func TestLibraryReleaseCountsAsIndexerData(t *testing.T) {
	set := compile(t, config.RuleConfig{Name: "Cached", When: "library", Points: 500})

	env := envFor("Movie 2020 1080p WEB-DL-GRP", func(c *triage.Candidate) {
		c.Release.IsLibrary = true
	})
	if got := set.Evaluate(env, "movie"); got.Points != 500 {
		t.Errorf("Points = %d, want 500 — a library release should be judged", got.Points)
	}
}

// seadexEnvFor builds the environment for one release under a request that
// resolved a SeaDex answer, the way ranking does for an anime search.
func seadexEnvFor(title string, seadex *rules.SeadexContext) rules.Env {
	cand := triage.Candidate{Release: &release.Release{Title: title}}
	return rules.BuildEnv(cand, jhin.Parse(title), rules.Context{Kind: "anime_show", Seadex: seadex})
}

// SeaDex recommendations are per title: the request context names the groups,
// and each release is judged by its own parsed group, case-insensitively —
// SeaDex spells groups as they name themselves, release names as they were
// typed.
func TestSeadexJudgesByGroup(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "SeaDex best", When: `seadex.best`, Points: 1000},
		config.RuleConfig{Name: "SeaDex alt", When: `seadex.alternative`, Points: 500},
	)
	seadex := &rules.SeadexContext{
		Known: true,
		Best:  map[string]bool{"koala": true},
		Alt:   map[string]bool{"commie": true},
	}

	if got := set.Evaluate(seadexEnvFor("Anime S01E01 1080p BluRay x265-KoaLa", seadex), "anime_show"); got.Points != 1000 {
		t.Errorf("best group scored %d, want 1000", got.Points)
	}
	if got := set.Evaluate(seadexEnvFor("Anime S01E01 1080p BluRay x265-Commie", seadex), "anime_show"); got.Points != 500 {
		t.Errorf("alternative group scored %d, want 500", got.Points)
	}
	if got := set.Evaluate(seadexEnvFor("Anime S01E01 1080p BluRay x265-OTHER", seadex), "anime_show"); got.Points != 0 {
		t.Errorf("an unlisted group scored %d, want 0", got.Points)
	}
}

// No lookup and no entry are different claims. Without a lookup, seadex rules
// are skipped; with a lookup that found nothing, they run and seadex.known is
// simply false.
func TestSeadexFailsOpenWithoutLookup(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "SeaDex best", When: `seadex.best`, Points: 1000},
		config.RuleConfig{Name: "Not on SeaDex", When: `not seadex.known`, Points: -100},
	)

	unchecked := set.Evaluate(seadexEnvFor("Anime S01E01 1080p BluRay x265-KoaLa", nil), "anime_show")
	if unchecked.Points != 0 {
		t.Errorf("Points = %d without a lookup, want 0", unchecked.Points)
	}
	if len(unchecked.Skipped) != 2 {
		t.Errorf("Skipped = %v, want both seadex rules reported", unchecked.Skipped)
	}
	for _, note := range unchecked.Skipped {
		if !strings.Contains(note, "SeaDex") {
			t.Errorf("skip reason %q does not name SeaDex", note)
		}
	}

	uncataloged := set.Evaluate(seadexEnvFor("Anime S01E01 1080p BluRay x265-KoaLa", &rules.SeadexContext{}), "anime_show")
	if len(uncataloged.Skipped) != 0 {
		t.Errorf("Skipped = %v after a successful lookup, want none", uncataloged.Skipped)
	}
	if uncataloged.Points != -100 {
		t.Errorf("Points = %d for an uncataloged title, want -100", uncataloged.Points)
	}
}
