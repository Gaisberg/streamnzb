package rules

import (
	"slices"
	"strings"
	"testing"

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

// TestVocabularyFunctions compiles a call to every reported function, which
// is what keeps a hand-written list honest: jhin holds its builtins in an
// unexported map, so a rename upstream would otherwise leave the report
// advertising a call no rule can make.
func TestVocabularyFunctions(t *testing.T) {
	// One condition per reported name and kind, exercising the signature the
	// report advertises.
	calls := map[string]string{
		"function/len":                  `len(audio) > 0`,
		"function/lower":                `lower(group) == "cake"`,
		"function/upper":                `upper(group) == "CAKE"`,
		"function/trim":                 `trim(group) == "cake"`,
		"function/abs":                  `abs(sizeGB - 4) < 1`,
		"function/floor":                `floor(sizeGB) == 4`,
		"function/ceil":                 `ceil(sizeGB) == 4`,
		"function/round":                `round(sizeGB) == 4`,
		"function/min":                  `min(sizeGB, 4, 9) == 4`,
		"function/max":                  `max(sizeGB, 4, 9) == 9`,
		"function/string":               `string(year) == "2019"`,
		"function/num":                  `num(bitrate) > 5`,
		"function/" + matchesExceptName: matchesExceptName + `(releaseName, "(?i)\\bMA\\b", "(?i)DTS-HD.MA")`,

		"collection/count": `count(languages, # == "ja") > 1`,
		"collection/any":   `any(languages, # == "ja")`,
		"collection/all":   `all(languages, # != "ja")`,
		"collection/none":  `none(languages, # == "ja")`,

		"aggregate/count":  `count(resolution == "2160p") > 2`,
		"aggregate/exists": `exists(resolution == "2160p")`,
		"aggregate/any":    `any(resolution == "2160p")`,
		"aggregate/none":   `none(resolution == "2160p")`,
	}

	seen := make(map[string]bool, len(functions))
	for _, fn := range functions {
		key := fn.Kind + "/" + fn.Name
		if seen[key] {
			t.Errorf("%s reported twice", key)
		}
		seen[key] = true

		if fn.Signature == "" || fn.Description == "" {
			t.Errorf("%s reported without a signature or description", key)
		}
		if !strings.HasPrefix(fn.Signature, fn.Name+"(") {
			t.Errorf("%s: signature %q does not start with the name", key, fn.Signature)
		}

		// matched() is resolved by inlining a rule that exists, so it cannot
		// be compiled from a condition on its own.
		if fn.Kind == "reference" {
			continue
		}
		when, ok := calls[key]
		if !ok {
			t.Errorf("%s is reported but this test does not compile it — add a call", key)
			continue
		}
		if err := compileProbe(when); err != nil {
			t.Errorf("%s: reported but %q does not compile: %v", key, when, err)
		}
	}

	for key := range calls {
		if !seen[key] {
			t.Errorf("this test compiles %s but the report does not list it", key)
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
