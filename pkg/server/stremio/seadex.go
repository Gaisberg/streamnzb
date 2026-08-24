package stremio

import (
	"context"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/rules"
	"streamnzb/pkg/search/triage"
)

// seadexLookupTimeout bounds the SeaDex call inside a playlist build. The
// answer is cached for a day, so only the first request for a title pays it.
const seadexLookupTimeout = 5 * time.Second

// seadexContext resolves the SeaDex recommendation for an anime request, or
// nil when no lookup applies: the request is not Kitsu-addressed, nothing in
// the stream's rules or format templates reads seadex, the title has no
// AniList mapping, or SeaDex could not be reached. Nil is what makes seadex.*
// rules skip instead of evaluating against zeros.
//
// Only Kitsu-addressed requests are looked up: SeaDex entries are keyed by
// AniList id, which the anime-lists import maps per Kitsu entry — season-exact
// by construction, since both address anime per season.
func (s *Server) seadexContext(ctx context.Context, source *playlistSource, stream *auth.Stream, profile *ranking.Profile) *rules.SeadexContext {
	if s == nil || s.seadexClient == nil || source == nil || source.Params == nil {
		return nil
	}
	kitsuID := strings.TrimSpace(source.Params.Req.KitsuID)
	if kitsuID == "" {
		return nil
	}
	if !profile.NeedsSeadex() && !s.formatWantsSeadex(stream) {
		return nil
	}
	mapping, ok := s.animeLists.LookupKitsu(kitsuID)
	if !ok || mapping.AniListID <= 0 {
		logger.Debug("SeaDex lookup skipped: no AniList mapping", "kitsu_id", kitsuID)
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, seadexLookupTimeout)
	defer cancel()
	entry, err := s.seadexClient.GetEntry(lookupCtx, mapping.AniListID)
	if err != nil {
		logger.Debug("SeaDex lookup failed", "kitsu_id", kitsuID, "anilist_id", mapping.AniListID, "err", err)
		return nil
	}
	if entry == nil {
		// SeaDex answered and has no entry: an uncataloged title, which rules
		// see as known=false rather than as missing data.
		return &rules.SeadexContext{}
	}
	best, alt := entry.GroupSets()
	logger.Debug("SeaDex entry resolved",
		"kitsu_id", kitsuID,
		"anilist_id", mapping.AniListID,
		"best_groups", len(best),
		"alt_groups", len(alt),
	)
	return &rules.SeadexContext{Known: true, Best: best, Alt: alt}
}

// annotateSeadexVerdicts judges each candidate's parsed group against the
// request's SeaDex answer, for the path where no filter profile runs and the
// ranking service's own verdict pass never happens. A nil context leaves every
// verdict unchecked.
func annotateSeadexVerdicts(candidates []triage.Candidate, seadexCtx *rules.SeadexContext) {
	if seadexCtx == nil {
		return
	}
	for i := range candidates {
		group := ""
		if m := candidates[i].Metadata; m != nil && m.Result != nil {
			group = m.Group
		}
		se := seadexCtx.For(group)
		candidates[i].Verdict.Seadex = triage.SeadexState{
			Checked:     se.Checked,
			Known:       se.Known,
			Best:        se.Best,
			Alternative: se.Alternative,
		}
	}
}

// formatWantsSeadex reports whether the stream's custom result templates read
// the SeaDex fields, which makes the lookup worth running even when no rule
// does.
func (s *Server) formatWantsSeadex(stream *auth.Stream) bool {
	nameText, descText := s.resultTemplateTexts(stream)
	return strings.Contains(nameText, ".Seadex") || strings.Contains(descText, ".Seadex")
}
