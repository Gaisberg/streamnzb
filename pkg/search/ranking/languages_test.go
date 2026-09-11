package ranking_test

import (
	"reflect"
	"testing"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/ranking"
)

// languageProfile compiles a profile whose Languages block is the given one,
// on top of the default preset with its own language options cleared.
func languageProfile(t *testing.T, langs rank.Languages, opts func(*rank.Options)) *ranking.Profile {
	t.Helper()
	spec := config.PresetSpec("")
	spec.Languages = langs
	spec.Options.RemoveUnknownLanguages = false
	spec.Options.AllowEnglish = false
	if opts != nil {
		opts(&spec.Options)
	}
	return limitsProfile(t, config.FilterProfileConfig{Name: "Languages", Ranking: &spec})
}

// The untagged, unlabelled release: nothing in the name says a language.
const plainTitle = "Movie 2020 1080p BluRay x264-GRP"

// A required language is satisfied by the indexer's tag alone. This is the
// standalone half of issue #283: a release the indexer labels Arabic and whose
// name says nothing is not "unknown".
func TestLanguagesRequiredSeesIndexerTag(t *testing.T) {
	p := languageProfile(t, rank.Languages{Required: []string{"ar"}}, nil)

	if kept, reasons := applyOne(p, ranking.KindMovie, &release.Release{Title: plainTitle}); kept {
		t.Fatalf("untagged release kept, want language:missing_required")
	} else if !reflect.DeepEqual(reasons, []string{"language:missing_required"}) {
		t.Fatalf("untagged release rejected for %v, want language:missing_required", reasons)
	}

	tagged := &release.Release{Title: plainTitle, Languages: []string{"ar"}}
	if kept, reasons := applyOne(p, ranking.KindMovie, tagged); !kept {
		t.Fatalf("release tagged Arabic rejected for %v, want kept", reasons)
	}

	// The name still counts on its own, as it always did.
	if kept, reasons := applyOne(p, ranking.KindMovie, &release.Release{Title: "Movie 2020 1080p BluRay ARABIC x264-GRP"}); !kept {
		t.Fatalf("release named Arabic rejected for %v, want kept", reasons)
	}
}

// An excluded language is caught from the tag too, with jhin's reason.
func TestLanguagesExcludeSeesIndexerTag(t *testing.T) {
	p := languageProfile(t, rank.Languages{Exclude: []string{"fr"}}, nil)

	if kept, reasons := applyOne(p, ranking.KindMovie, &release.Release{Title: plainTitle}); !kept {
		t.Fatalf("untagged release rejected for %v, want kept", reasons)
	}
	tagged := &release.Release{Title: plainTitle, Languages: []string{"French"}}
	if kept, reasons := applyOne(p, ranking.KindMovie, tagged); kept {
		t.Fatalf("release tagged French kept, want language:fr")
	} else if !reflect.DeepEqual(reasons, []string{"language:fr"}) {
		t.Fatalf("release tagged French rejected for %v, want language:fr", reasons)
	}
}

// remove_unknown_languages counts a tagged release as known.
func TestLanguagesRemoveUnknownSeesIndexerTag(t *testing.T) {
	p := languageProfile(t, rank.Languages{}, func(o *rank.Options) { o.RemoveUnknownLanguages = true })

	if kept, reasons := applyOne(p, ranking.KindMovie, &release.Release{Title: plainTitle}); kept {
		t.Fatalf("untagged release kept, want language:unknown")
	} else if !reflect.DeepEqual(reasons, []string{"language:unknown"}) {
		t.Fatalf("untagged release rejected for %v, want language:unknown", reasons)
	}
	if kept, reasons := applyOne(p, ranking.KindMovie, &release.Release{Title: plainTitle, Languages: []string{"de"}}); !kept {
		t.Fatalf("release tagged German rejected for %v, want kept", reasons)
	}
}

// The escape hatches keep working over the merged list: AllowEnglish and an
// allowed language both exempt a release from the exclusions, and a group
// alias expands as it does in jhin.
func TestLanguagesAllowancesAndGroups(t *testing.T) {
	english := languageProfile(t, rank.Languages{Exclude: []string{"fr"}}, func(o *rank.Options) { o.AllowEnglish = true })
	if kept, reasons := applyOne(english, ranking.KindMovie, &release.Release{Title: plainTitle, Languages: []string{"fr", "en"}}); !kept {
		t.Fatalf("English release rejected for %v, want kept under allow_english", reasons)
	}

	allowed := languageProfile(t, rank.Languages{Exclude: []string{"all"}, Allowed: []string{"ja"}}, nil)
	if kept, reasons := applyOne(allowed, ranking.KindMovie, &release.Release{Title: plainTitle, Languages: []string{"ja", "fr"}}); !kept {
		t.Fatalf("release with an allowed language rejected for %v, want kept", reasons)
	}
	if kept, _ := applyOne(allowed, ranking.KindMovie, &release.Release{Title: plainTitle, Languages: []string{"fr"}}); kept {
		t.Fatalf("release in an excluded group kept, want rejected")
	}
}

// The preferred bonus pays on the tag and shows up in the breakdown under the
// name jhin gives it.
func TestLanguagesPreferredBonusSeesIndexerTag(t *testing.T) {
	p := languageProfile(t, rank.Languages{Preferred: []string{"fi"}}, func(o *rank.Options) { o.PreferredBonus = 777 })

	base := rankOf(t, p, ranking.KindMovie, &release.Release{Title: plainTitle})
	tagged := rankOf(t, p, ranking.KindMovie, &release.Release{Title: plainTitle, Languages: []string{"fi"}})
	if got := tagged - base; got != 777 {
		t.Fatalf("release tagged Finnish earned %d over an untagged one, want 777", got)
	}

	explanations, _ := p.Explain([]string{"Movie 2020 1080p BluRay FINNISH x264-GRP"}, ranking.Request{Kind: ranking.KindMovie}, rank.RankOptions{})
	if len(explanations) != 1 {
		t.Fatalf("got %d explanations, want 1", len(explanations))
	}
	found := 0
	for _, c := range explanations[0].Contributions {
		if c.Source == "preferred_language" {
			found++
			if c.Rank != 777 {
				t.Fatalf("preferred_language contribution is %d, want 777", c.Rank)
			}
		}
	}
	if found != 1 {
		t.Fatalf("breakdown lists preferred_language %d times, want once", found)
	}
}
