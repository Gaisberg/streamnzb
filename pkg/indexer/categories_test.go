package indexer

import "testing"

func findCategory(cats []CapsCategory, id string) *CapsCategory {
	for i := range cats {
		if cats[i].ID == id {
			return &cats[i]
		}
	}
	return nil
}

func TestMergeCapsUnionsCategoriesAndSearching(t *testing.T) {
	merged := MergeCaps(map[string]*Caps{
		"alpha": {
			Categories: []CapsCategory{
				{ID: "5000", Name: "TV Shows", Subcats: []CapsCategory{{ID: "5040", Name: "TV HD"}}},
			},
			Searching: CapsSearching{
				TVSearch:                true,
				TVSearchSupportedParams: map[string]bool{"q": true, "tvdbid": true},
			},
			Limits:        CapsLimits{Max: 100, Default: 50},
			RetentionDays: 1500,
		},
		"beta": {
			Categories: []CapsCategory{
				{ID: "2000", Name: "Movies", Subcats: []CapsCategory{{ID: "2040", Name: "HD"}}},
				// Published as a top-level category even though it is a subcat.
				{ID: "5070", Name: "Anime"},
			},
			Searching: CapsSearching{
				MovieSearch:                true,
				MovieSearchSupportedParams: map[string]bool{"imdbid": true},
			},
			Limits:        CapsLimits{Max: 500, Default: 100},
			RetentionDays: 3000,
		},
	})

	if len(merged.Categories) != 2 {
		t.Fatalf("categories = %d, want 2 roots", len(merged.Categories))
	}
	if merged.Categories[0].ID != "2000" || merged.Categories[1].ID != "5000" {
		t.Fatalf("roots = %q/%q, want 2000/5000", merged.Categories[0].ID, merged.Categories[1].ID)
	}
	tv := findCategory(merged.Categories, "5000")
	if tv.Name != "TV" {
		t.Errorf("5000 name = %q, want the standard name TV", tv.Name)
	}
	if len(tv.Subcats) != 2 || tv.Subcats[0].ID != "5040" || tv.Subcats[1].ID != "5070" {
		t.Fatalf("TV subcats = %+v, want 5040 and 5070", tv.Subcats)
	}
	if tv.Subcats[0].Name != "HD" {
		t.Errorf("5040 name = %q, want the standard name HD", tv.Subcats[0].Name)
	}

	if !merged.Searching.TVSearch || !merged.Searching.MovieSearch {
		t.Error("tv-search and movie-search should both be available")
	}
	if merged.Searching.Search {
		t.Error("plain search should stay unavailable when no indexer offers it")
	}
	if !merged.Searching.TVSearchSupportedParams["tvdbid"] || !merged.Searching.MovieSearchSupportedParams["imdbid"] {
		t.Error("supported params should be the union across indexers")
	}
	if merged.Limits.Max != 500 || merged.Limits.Default != 100 {
		t.Errorf("limits = %+v, want max 500 default 100", merged.Limits)
	}
	if merged.RetentionDays != 3000 {
		t.Errorf("retention = %d, want the best on offer (3000)", merged.RetentionDays)
	}
}

func TestMergeCapsFallsBackToStandardTree(t *testing.T) {
	merged := MergeCaps(nil)

	if len(merged.Categories) != len(standardCategories) {
		t.Fatalf("categories = %d, want the full standard tree (%d)", len(merged.Categories), len(standardCategories))
	}
	if findCategory(merged.Categories, "5000") == nil || findCategory(merged.Categories, "2000") == nil {
		t.Fatal("standard tree should carry both TV and Movies")
	}
	if !merged.Searching.Search || !merged.Searching.TVSearch || !merged.Searching.MovieSearch {
		t.Error("with no published caps the standard search functions should be claimed")
	}
	if merged.Limits.Max != 100 || merged.Limits.Default != 100 {
		t.Errorf("limits = %+v, want the 100/100 default", merged.Limits)
	}
}

func TestMergeCapsClampsDefaultToMax(t *testing.T) {
	merged := MergeCaps(map[string]*Caps{
		"alpha": {Limits: CapsLimits{Max: 60, Default: 200}},
	})
	if merged.Limits.Default != 60 {
		t.Errorf("default = %d, want it clamped to max 60", merged.Limits.Default)
	}
}

func TestStandardCategoriesIsACopy(t *testing.T) {
	first := StandardCategories()
	first[0].Name = "mutated"
	first[0].Subcats[0].Name = "mutated"
	if StandardCategories()[0].Name == "mutated" || StandardCategories()[0].Subcats[0].Name == "mutated" {
		t.Fatal("StandardCategories handed out a reference to the package tree")
	}
}
