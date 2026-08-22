package indexer

import (
	"sort"
	"strconv"
	"strings"
)

// standardCategories is the Newznab standard category tree. Indexers are free
// to publish their own ids and names, but every well-behaved one maps its
// content onto these buckets, so they double as the naming authority for
// merged caps and as the fallback tree when nothing published caps at all.
var standardCategories = []CapsCategory{
	{ID: "1000", Name: "Console", Subcats: []CapsCategory{
		{ID: "1010", Name: "NDS"},
		{ID: "1020", Name: "PSP"},
		{ID: "1030", Name: "Wii"},
		{ID: "1040", Name: "XBox"},
		{ID: "1050", Name: "XBox 360"},
		{ID: "1060", Name: "Wiiware/VC"},
		{ID: "1070", Name: "XBox 360 DLC"},
		{ID: "1080", Name: "PS3"},
		{ID: "1090", Name: "Other"},
		{ID: "1110", Name: "3DS"},
		{ID: "1120", Name: "PS Vita"},
		{ID: "1130", Name: "WiiU"},
		{ID: "1140", Name: "XBox One"},
		{ID: "1180", Name: "PS4"},
	}},
	{ID: "2000", Name: "Movies", Subcats: []CapsCategory{
		{ID: "2010", Name: "Foreign"},
		{ID: "2020", Name: "Other"},
		{ID: "2030", Name: "SD"},
		{ID: "2040", Name: "HD"},
		{ID: "2045", Name: "UHD"},
		{ID: "2050", Name: "BluRay"},
		{ID: "2060", Name: "3D"},
		{ID: "2070", Name: "DVD"},
		{ID: "2080", Name: "WEB-DL"},
	}},
	{ID: "3000", Name: "Audio", Subcats: []CapsCategory{
		{ID: "3010", Name: "MP3"},
		{ID: "3020", Name: "Video"},
		{ID: "3030", Name: "Audiobook"},
		{ID: "3040", Name: "Lossless"},
		{ID: "3050", Name: "Other"},
		{ID: "3060", Name: "Foreign"},
	}},
	{ID: "4000", Name: "PC", Subcats: []CapsCategory{
		{ID: "4010", Name: "0day"},
		{ID: "4020", Name: "ISO"},
		{ID: "4030", Name: "Mac"},
		{ID: "4040", Name: "Mobile-Other"},
		{ID: "4050", Name: "Games"},
		{ID: "4060", Name: "Mobile-iOS"},
		{ID: "4070", Name: "Mobile-Android"},
	}},
	{ID: "5000", Name: "TV", Subcats: []CapsCategory{
		{ID: "5010", Name: "WEB-DL"},
		{ID: "5020", Name: "Foreign"},
		{ID: "5030", Name: "SD"},
		{ID: "5040", Name: "HD"},
		{ID: "5045", Name: "UHD"},
		{ID: "5050", Name: "Other"},
		{ID: "5060", Name: "Sport"},
		{ID: "5070", Name: "Anime"},
		{ID: "5080", Name: "Documentary"},
	}},
	{ID: "6000", Name: "XXX", Subcats: []CapsCategory{
		{ID: "6010", Name: "DVD"},
		{ID: "6020", Name: "WMV"},
		{ID: "6030", Name: "XviD"},
		{ID: "6040", Name: "x264"},
		{ID: "6045", Name: "Pack"},
		{ID: "6050", Name: "ImageSet"},
		{ID: "6060", Name: "Other"},
		{ID: "6070", Name: "SD"},
		{ID: "6080", Name: "WEB-DL"},
		{ID: "6090", Name: "UHD"},
	}},
	{ID: "7000", Name: "Books", Subcats: []CapsCategory{
		{ID: "7010", Name: "Mags"},
		{ID: "7020", Name: "EBook"},
		{ID: "7030", Name: "Comics"},
		{ID: "7040", Name: "Technical"},
		{ID: "7050", Name: "Other"},
		{ID: "7060", Name: "Foreign"},
	}},
	{ID: "8000", Name: "Other", Subcats: []CapsCategory{
		{ID: "8010", Name: "Misc"},
		{ID: "8020", Name: "Hashed"},
	}},
}

// standardCategoryNames maps every standard id to its canonical name, the
// short form a caps document carries ("TV", "HD") rather than the "TV/HD" path
// some indexers publish.
var standardCategoryNames = func() map[string]string {
	names := make(map[string]string)
	for _, cat := range standardCategories {
		names[cat.ID] = cat.Name
		for _, sub := range cat.Subcats {
			names[sub.ID] = sub.Name
		}
	}
	return names
}()

// StandardCategories returns a copy of the Newznab standard category tree.
func StandardCategories() []CapsCategory {
	return cloneCategories(standardCategories)
}

func cloneCategories(cats []CapsCategory) []CapsCategory {
	out := make([]CapsCategory, 0, len(cats))
	for _, cat := range cats {
		clone := CapsCategory{ID: cat.ID, Name: cat.Name}
		if len(cat.Subcats) > 0 {
			clone.Subcats = cloneCategories(cat.Subcats)
		}
		out = append(out, clone)
	}
	return out
}

// parentCategoryID returns the top-level id a category id rolls up into, e.g.
// 5040 -> 5000. Ids that are not numeric have no parent.
func parentCategoryID(id string) string {
	n, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil || n <= 0 {
		return ""
	}
	return strconv.Itoa((n / 1000) * 1000)
}

// categoryIDLess orders category ids numerically, falling back to a string
// compare for the non-numeric ids some private indexers publish.
func categoryIDLess(a, b string) bool {
	na, errA := strconv.Atoi(strings.TrimSpace(a))
	nb, errB := strconv.Atoi(strings.TrimSpace(b))
	switch {
	case errA == nil && errB == nil:
		return na < nb
	case errA == nil:
		return true
	case errB == nil:
		return false
	default:
		return a < b
	}
}

type mergedCategory struct {
	id      string
	name    string
	subcats map[string]string
}

// MergeCaps folds per-indexer capability documents into the one capability set
// an aggregating endpoint can honestly advertise: a category is offered when
// any indexer carries it, a search function when any indexer implements it, a
// parameter when any indexer accepts it. Limits and retention take the best
// value on offer, because a fan-out is only as constrained as its widest
// member. When nothing published caps — no indexers, or none that answer
// t=caps — the standard tree is advertised so clients still have categories to
// map against.
func MergeCaps(perIndexer map[string]*Caps) *Caps {
	merged := &Caps{}

	// Sorted so a name collision between two indexers resolves the same way on
	// every call rather than by map iteration order.
	names := make([]string, 0, len(perIndexer))
	for name, caps := range perIndexer {
		if caps != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	roots := make(map[string]*mergedCategory)
	root := func(id string) *mergedCategory {
		if existing, ok := roots[id]; ok {
			return existing
		}
		node := &mergedCategory{id: id, subcats: make(map[string]string)}
		roots[id] = node
		return node
	}
	// Standard names win over indexer-local ones so a merged tree reads the
	// same regardless of which indexers happen to be configured.
	nameFor := func(id, published string) string {
		if standard, ok := standardCategoryNames[id]; ok {
			return standard
		}
		return strings.TrimSpace(published)
	}
	addCategory := func(cat CapsCategory) {
		id := strings.TrimSpace(cat.ID)
		if id == "" {
			return
		}
		parent := parentCategoryID(id)
		if parent != "" && parent != id {
			// A subcategory published at the top level still belongs under its
			// parent; without this the tree grows duplicate roots.
			node := root(parent)
			if node.name == "" {
				node.name = nameFor(parent, "")
			}
			if _, ok := node.subcats[id]; !ok {
				node.subcats[id] = nameFor(id, cat.Name)
			}
			parent = id
		} else {
			node := root(id)
			if node.name == "" {
				node.name = nameFor(id, cat.Name)
			}
			parent = id
		}
		for _, sub := range cat.Subcats {
			subID := strings.TrimSpace(sub.ID)
			if subID == "" || subID == parent {
				continue
			}
			owner := parentCategoryID(subID)
			if owner == "" || owner == subID {
				owner = parent
			}
			node := root(owner)
			if node.name == "" {
				node.name = nameFor(owner, "")
			}
			if _, ok := node.subcats[subID]; !ok {
				node.subcats[subID] = nameFor(subID, sub.Name)
			}
		}
	}

	for _, name := range names {
		caps := perIndexer[name]
		for _, cat := range caps.Categories {
			addCategory(cat)
		}
		merged.Searching.Search = merged.Searching.Search || caps.Searching.Search
		merged.Searching.TVSearch = merged.Searching.TVSearch || caps.Searching.TVSearch
		merged.Searching.MovieSearch = merged.Searching.MovieSearch || caps.Searching.MovieSearch
		merged.Searching.AudioSearch = merged.Searching.AudioSearch || caps.Searching.AudioSearch
		merged.Searching.BookSearch = merged.Searching.BookSearch || caps.Searching.BookSearch
		merged.Searching.SearchSupportedParams = mergeParams(merged.Searching.SearchSupportedParams, caps.Searching.SearchSupportedParams)
		merged.Searching.TVSearchSupportedParams = mergeParams(merged.Searching.TVSearchSupportedParams, caps.Searching.TVSearchSupportedParams)
		merged.Searching.MovieSearchSupportedParams = mergeParams(merged.Searching.MovieSearchSupportedParams, caps.Searching.MovieSearchSupportedParams)
		merged.Searching.AudioSearchSupportedParams = mergeParams(merged.Searching.AudioSearchSupportedParams, caps.Searching.AudioSearchSupportedParams)
		merged.Searching.BookSearchSupportedParams = mergeParams(merged.Searching.BookSearchSupportedParams, caps.Searching.BookSearchSupportedParams)
		if caps.Limits.Max > merged.Limits.Max {
			merged.Limits.Max = caps.Limits.Max
		}
		if caps.Limits.Default > merged.Limits.Default {
			merged.Limits.Default = caps.Limits.Default
		}
		if caps.RetentionDays > merged.RetentionDays {
			merged.RetentionDays = caps.RetentionDays
		}
	}

	if len(names) == 0 {
		// Nothing to merge: an indexer that never answered t=caps still takes
		// searches, so claim the standard surface rather than "no functions".
		merged.Searching.Search = true
		merged.Searching.TVSearch = true
		merged.Searching.MovieSearch = true
	}
	if merged.Limits.Max <= 0 {
		merged.Limits.Max = 100
	}
	if merged.Limits.Default <= 0 {
		merged.Limits.Default = 100
	}
	if merged.Limits.Default > merged.Limits.Max {
		merged.Limits.Default = merged.Limits.Max
	}

	if len(roots) == 0 {
		merged.Categories = StandardCategories()
		return merged
	}
	merged.Categories = make([]CapsCategory, 0, len(roots))
	for _, node := range roots {
		cat := CapsCategory{ID: node.id, Name: node.name}
		if cat.Name == "" {
			cat.Name = "Other"
		}
		for subID, subName := range node.subcats {
			if subName == "" {
				subName = subID
			}
			cat.Subcats = append(cat.Subcats, CapsCategory{ID: subID, Name: subName})
		}
		sort.Slice(cat.Subcats, func(i, j int) bool {
			return categoryIDLess(cat.Subcats[i].ID, cat.Subcats[j].ID)
		})
		merged.Categories = append(merged.Categories, cat)
	}
	sort.Slice(merged.Categories, func(i, j int) bool {
		return categoryIDLess(merged.Categories[i].ID, merged.Categories[j].ID)
	})
	return merged
}

func mergeParams(into, from map[string]bool) map[string]bool {
	if len(from) == 0 {
		return into
	}
	if into == nil {
		into = make(map[string]bool, len(from))
	}
	for param, ok := range from {
		if ok {
			into[param] = true
		}
	}
	return into
}
