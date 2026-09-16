package rules

import (
	"slices"
	"strings"
	"testing"

	jhinrules "github.com/dreulavelle/jhin/rules"

	"streamnzb/pkg/core/config"
)

// TestDescribeFields checks the report against the registries rather than
// against a golden list: a golden list is a second copy of the vocabulary,
// which is the duplication the report exists to remove.
func TestDescribeFields(t *testing.T) {
	vocab := Describe()
	if len(vocab.Fields) == 0 {
		t.Fatal("no fields reported")
	}

	byName := make(map[string]FieldInfo, len(vocab.Fields))
	for i, f := range vocab.Fields {
		if f.Name == "" || f.Type == "" {
			t.Errorf("field %d reported without a name or type: %+v", i, f)
		}
		if _, dup := byName[f.Name]; dup {
			t.Errorf("field %q reported twice", f.Name)
		}
		byName[f.Name] = f
		if i > 0 && vocab.Fields[i-1].Name > f.Name {
			t.Errorf("fields out of order at %d: %q after %q", i, f.Name, vocab.Fields[i-1].Name)
		}
	}

	// Every field the registries declare has to appear, or a client
	// validating compatibility would conclude a rule it can write is
	// unsupported.
	for _, name := range postRegistry.Fields() {
		if _, ok := byName[name]; !ok {
			t.Errorf("registry declares %q but the report omits it", name)
		}
	}

	// A spot check of each shape the report has to get right: a core
	// attribute with no tier, a tiered one, and one that only a prune rule
	// can read.
	for name, want := range map[string]FieldInfo{
		"resolution":             {Type: "string"},
		"library":                {Type: "bool"},
		"seadex.best":            {Type: "bool", Tier: tierSeadex},
		"probed.audioLanguages":  {Type: "list<string>", Tier: tierTracks},
		"sizeGB":                 {Type: "num", Tier: tierIndexer},
		"finalScore":             {Type: "num", PruneOnly: true},
		"current.finalRank":      {Type: "num", PruneOnly: true},
		"probed.dolbyVision":     {Type: "bool", Tier: tierMeasured},
		"avail.status":           {Type: "string", Tier: tierAvail},
		"parsed.languages":       {Type: "list<string>"},
		"originalLanguage":       {Type: "string"},
		"probed.subtitleStreams": {Type: "num", Tier: tierTracks},
	} {
		got, ok := byName[name]
		if !ok {
			t.Errorf("%q missing from the report", name)
			continue
		}
		if got.Type != want.Type || got.Tier != want.Tier || got.PruneOnly != want.PruneOnly {
			t.Errorf("%q reported as type=%q tier=%q prune_only=%v, want type=%q tier=%q prune_only=%v",
				name, got.Type, got.Tier, got.PruneOnly, want.Type, want.Tier, want.PruneOnly)
		}
	}

	// A scoring rule may not read the post-scoring values, which is the whole
	// reason for two registries; the report has to say so.
	if _, ok := registry.Lookup("finalScore"); ok {
		t.Error("finalScore is readable by a scoring rule, so prune_only is a lie")
	}
}

// TestDescribeTiers checks every tier the registries use is described, so a
// skipped rule's reason and the report can never name different sets.
func TestDescribeTiers(t *testing.T) {
	vocab := Describe()
	described := make(map[string]string, len(vocab.Tiers))
	for _, tier := range vocab.Tiers {
		if tier.Name == "" || tier.Description == "" {
			t.Errorf("tier reported without a name or description: %+v", tier)
		}
		described[tier.Name] = tier.Description
	}
	for _, name := range postRegistry.Tiers() {
		if name == "" {
			continue // the always-present tier, which needs no description
		}
		if described[name] == "" {
			t.Errorf("registry declares tier %q but the report does not describe it", name)
		}
	}
	// Every field's tier must be one the report describes, or a client cannot
	// tell when the field is answerable.
	for _, f := range vocab.Fields {
		if f.Tier != "" && described[f.Tier] == "" {
			t.Errorf("field %q names undescribed tier %q", f.Name, f.Tier)
		}
	}
}

// The call vocabulary is read from the registry now, so the set cannot drift
// from what compiles and there is nothing left to verify about it. What can
// still drift is the prose: a call jhin adds arrives in the report with no
// description at all unless someone writes one.
func TestDescribeFunctions(t *testing.T) {
	vocab := Describe()
	if len(vocab.Functions) == 0 {
		t.Fatal("no functions reported")
	}

	seen := map[string]bool{}
	for _, fn := range vocab.Functions {
		key := fn.Kind + "/" + fn.Name
		if seen[key] {
			t.Errorf("%s reported twice", key)
		}
		seen[key] = true

		if fn.Description == "" {
			t.Errorf("%s has no description — add one to funcDescriptions", key)
		}
		if fn.Signature == "" || !strings.HasPrefix(fn.Signature, fn.Name+"(") {
			t.Errorf("%s: signature %q does not start with the name", key, fn.Signature)
		}
		// "invalid" is what jhin's zero Type prints as; a parameter that takes
		// anything has to read as "any", not as a defect.
		if strings.Contains(fn.Signature, "invalid") {
			t.Errorf("%s: signature %q leaks an unrendered type", key, fn.Signature)
		}
	}

	// The function StreamNZB registers has to arrive through the same report
	// as jhin's own, or a client cannot discover it.
	if !seen["function/"+matchesExceptName] {
		t.Errorf("%s is registered but not reported", matchesExceptName)
	}
	// matched() is not a call and jhin does not report it, so it is ours to
	// add; a client that never hears about it cannot use define libraries.
	if !seen["reference/matched"] {
		t.Error("matched() missing from the report")
	}
	// The overloaded names must appear once per form.
	for _, key := range []string{"collection/count", "aggregate/count", "collection/any", "aggregate/any"} {
		if !seen[key] {
			t.Errorf("%s missing — count/any are overloaded across both forms", key)
		}
	}
}

// A reported signature has to describe a call that really compiles. The set
// comes from the registry, but the rendering is ours and could misstate it.
func TestReportedSignaturesCompile(t *testing.T) {
	for _, when := range []string{
		`len(audio) > 0`,
		`min(sizeGB, 4, 9) == 4`,
		`num(bitrate) > 5`,
		matchesExceptName + `(releaseName, "(?i)\\bMA\\b", "(?i)DTS-HD.MA")`,
		`any(languages, # == "ja")`,
		`count(resolution == "2160p") > 2`,
		`exists(resolution == "2160p")`,
	} {
		if _, err := jhinrules.Compile(postRegistry, []jhinrules.Rule{
			{Name: "probe", When: when, Action: jhinrules.ActionReject},
		}); err != nil {
			t.Errorf("%q does not compile: %v", when, err)
		}
	}
}

// TestVocabularyMatchedReference compiles the inlining form, since the
// function loop above cannot.
func TestVocabularyMatchedReference(t *testing.T) {
	set, err := Compile([]config.RuleConfig{
		{Name: "Good groups", When: `group == "cake"`, Action: config.RuleActionDefine},
		{Name: "Reward", When: `matched("Good groups")`, Action: config.RuleActionScore, Points: 100},
	})
	if err != nil {
		t.Fatalf("matched() is reported but does not compile: %v", err)
	}
	if set.Len() == 0 {
		t.Fatal("compiled to no acting rules")
	}
}

// TestDescribeActionsAndScopes checks the report's actions and scopes are the
// ones a stored rule is actually validated against.
func TestDescribeActionsAndScopes(t *testing.T) {
	vocab := Describe()

	for _, action := range vocab.Actions {
		if got := (config.RuleConfig{Action: action}).EffectiveAction(); got != action {
			t.Errorf("reported action %q resolves to %q, so config does not accept it", action, got)
		}
	}
	// Score is the default, so an empty action must resolve to a reported one.
	if got := (config.RuleConfig{}).EffectiveAction(); !slices.Contains(vocab.Actions, got) {
		t.Errorf("the default action %q is not reported", got)
	}

	if len(vocab.Scopes) == 0 || vocab.Scopes[0] != config.RuleScopeAll {
		t.Errorf("scopes should lead with %q, got %v", config.RuleScopeAll, vocab.Scopes)
	}
	for _, scope := range vocab.Scopes {
		if got := (config.RuleConfig{Scope: scope}).EffectiveScope(); got != scope {
			t.Errorf("reported scope %q resolves to %q", scope, got)
		}
	}
	if slices.Contains(vocab.Scopes, config.LimitKindDefault) {
		t.Errorf("%q is a limit bucket, not a rule scope", config.LimitKindDefault)
	}
}
