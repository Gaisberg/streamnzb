package ranking

import (
	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config/pttoptions"
)

// languageJudge is the profile's Languages block, compiled the way jhin
// compiles its own, so the profile can judge it here over every language the
// release is known to carry rather than only the ones its name spells out.
//
// jhin parses the title itself and judges the block on that alone. An
// indexer's language tag is regularly the only place a dub or a foreign
// release is labelled — "Movie.2020.1080p.BluRay-GRP" tagged Arabic by the
// indexer is, to jhin, a release with no language — so a required or excluded
// language judged by jhin never sees it. The block is stripped from the copy of
// the profile handed to jhin (see Compile) and applied by applyLanguages
// instead, with the same reasons and the same bonus, over the merged list the
// rules already read as languages.
type languageJudge struct {
	required, allowed, exclude, preferred map[string]bool
	removeUnknown, allowEnglish           bool
	preferredBonus                        int
}

// jhinLanguageGroups mirrors the alias groups jhin accepts in a Languages list
// (rank/profile.go). They are unexported there, so they are repeated here; the
// contents have been stable across every jhin release this has been built
// against.
var jhinLanguageGroups = map[string][]string{
	"anime": {"ja", "zh", "ko"},
	"non_anime": {
		"de", "es", "hi", "ta", "ru", "ua", "th", "it", "ar", "pt", "fr",
		"pa", "mr", "gu", "te", "kn", "ml", "vi", "id", "tr", "he", "fa",
		"el", "lt", "lv", "et", "pl", "cs", "sk", "hu", "ro", "bg", "sr",
		"hr", "sl", "nl", "da", "fi", "sv", "no", "ms",
	},
	"common": {"de", "es", "hi", "ta", "ru", "ua", "th", "it", "zh", "ar", "fr"},
}

// expandLanguages resolves group aliases into a lookup set, as jhin does.
func expandLanguages(codes []string) map[string]bool {
	out := make(map[string]bool, len(codes))
	for _, c := range codes {
		if c == "all" {
			for _, g := range [2]string{"anime", "non_anime"} {
				for _, l := range jhinLanguageGroups[g] {
					out[l] = true
				}
			}
			continue
		}
		if group, ok := jhinLanguageGroups[c]; ok {
			for _, l := range group {
				out[l] = true
			}
			continue
		}
		out[c] = true
	}
	return out
}

func compileLanguageJudge(spec rank.Profile) languageJudge {
	return languageJudge{
		required:       expandLanguages(spec.Languages.Required),
		allowed:        expandLanguages(spec.Languages.Allowed),
		exclude:        expandLanguages(spec.Languages.Exclude),
		preferred:      expandLanguages(spec.Languages.Preferred),
		removeUnknown:  spec.Options.RemoveUnknownLanguages,
		allowEnglish:   spec.Options.AllowEnglish,
		preferredBonus: spec.Options.PreferredBonus,
	}
}

// rejections is jhin's checkLanguages over the given list: the same reasons in
// the same order, so the preview and the history read the same whichever side
// judged the release.
func (j languageJudge) rejections(langs []string) []string {
	if len(langs) == 0 {
		if j.removeUnknown {
			return []string{"language:unknown"}
		}
		if len(j.required) > 0 {
			return []string{"language:missing_required"}
		}
		return nil
	}
	if len(j.required) > 0 && !languageOverlap(langs, j.required) {
		return []string{"language:missing_required"}
	}
	if j.allowEnglish {
		for _, l := range langs {
			if l == "en" {
				return nil
			}
		}
	}
	if len(j.allowed) > 0 && languageOverlap(langs, j.allowed) {
		return nil
	}
	var reasons []string
	for _, l := range langs {
		if j.exclude[l] {
			reasons = append(reasons, "language:"+l)
		}
	}
	return reasons
}

// bonus is what the preferred languages pay: the profile's one preferred bonus,
// once, when any of them is present.
func (j languageJudge) bonus(langs []string) int {
	if len(j.preferred) > 0 && languageOverlap(langs, j.preferred) {
		return j.preferredBonus
	}
	return 0
}

func languageOverlap(langs []string, set map[string]bool) bool {
	for _, l := range langs {
		if set[l] {
			return true
		}
	}
	return false
}

// resultLanguages is every language a result is known to carry: what jhin
// parsed out of the name, merged with the indexer's own tag. The same union
// the rules read as languages and the Stremio stream advertises.
func resultLanguages(r *Result) []string {
	var parsed, reported []string
	if r.Torrent.Data != nil {
		parsed = r.Torrent.Data.Languages
	}
	if r.Candidate.Release != nil {
		reported = r.Candidate.Release.Languages
	}
	return pttoptions.MergeLanguageCodes(parsed, reported)
}

// applyLanguages judges the profile's Languages block over each result's
// merged languages, rejecting and paying the preferred bonus exactly as jhin
// would have on the title alone. It runs first after jhin so the reasons sit
// beside jhin's own and the bonus is in the score before anything reads it.
func (p *Profile) applyLanguages(results []Result) {
	if p == nil {
		return
	}
	for i := range results {
		r := &results[i]
		langs := resultLanguages(r)
		r.Torrent.Rank += p.languages.bonus(langs)
		reasons := p.languages.rejections(langs)
		if len(reasons) == 0 {
			continue
		}
		r.Torrent.Fetch = false
		r.Torrent.Rejections = append(r.Torrent.Rejections, reasons...)
	}
}

// languageContribution is the result's preferred-language line in its score
// breakdown, under the source name jhin uses so the preview labels it the
// same. ok is false when the release earned nothing.
func (p *Profile) languageContribution(r *Result) (c rank.Contribution, ok bool) {
	bonus := p.languages.bonus(resultLanguages(r))
	if bonus == 0 {
		return rank.Contribution{}, false
	}
	return rank.Contribution{Source: "preferred_language", Rank: bonus}, true
}
