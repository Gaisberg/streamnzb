package search

import (
	"sort"
	"strings"

	"streamnzb/pkg/release"
)

func dedupeReleaseKeys(rel *release.Release) []string {
	if rel == nil {
		return nil
	}
	var keys []string
	if rel.DetailsURL != "" {
		keys = append(keys, "details:"+rel.DetailsURL)
	}
	if rel.GUID != "" {
		keys = append(keys, "guid:"+rel.GUID)
	}
	normTitle := release.NormalizeTitleForDedup(rel.Title)
	if normTitle != "" {
		keys = append(keys, "title:"+strings.ToLower(normTitle))
	}
	return keys
}

func MergeAndDedupeSearchResults(releases []*release.Release) []*release.Release {
	seenIndex := make(map[string]int)
	var result []*release.Release

	for _, rel := range releases {
		if rel == nil {
			continue
		}
		keys := dedupeReleaseKeys(rel)
		existingIdx := -1
		for _, k := range keys {
			if idx, found := seenIndex[k]; found {
				existingIdx = idx
				break
			}
		}

		if existingIdx >= 0 {
			// If existing candidate in result is NOT from library, but new candidate IS from library,
			// replace the existing non-library candidate with the library release!
			if !result[existingIdx].IsLibraryResult() && rel.IsLibraryResult() {
				result[existingIdx] = rel
				for _, k := range keys {
					seenIndex[k] = existingIdx
				}
			}
			continue
		}

		newIdx := len(result)
		for _, k := range keys {
			seenIndex[k] = newIdx
		}
		result = append(result, rel)
	}
	return result
}

// VariantMergeOptions configures MergeSameReleaseVariants.
type VariantMergeOptions struct {
	// Rank scores one copy as a playback target; the highest-ranked copy of a
	// group becomes the primary and the rest become its variants, best first.
	// Ties break on grabs, then on the newer post, then on input order. A nil
	// Rank leaves the whole decision to those tiebreakers, which is first-seen
	// ordering — what plain deduplication did.
	Rank func(*release.Release) int
}

// MergeSameReleaseVariants groups copies of the same release into one result
// that keeps the others as variants.
//
// The grouping keys are the ones deduplication has always used — details URL,
// GUID, normalized title — so nothing merges here that would not have been
// discarded before. The difference is what happens to the losers: they ride
// along on the winner as playback fallbacks instead of being dropped, because
// two indexers' NZBs for one release are not always the same NZB, and the copy
// that is missing articles is not always the copy that was listed first.
//
// Releases already carrying variants are flattened into the group, so merging
// a merged list is idempotent.
func MergeSameReleaseVariants(releases []*release.Release, opts VariantMergeOptions) []*release.Release {
	groupIndex := make(map[string]int)
	var groups [][]*release.Release

	for _, rel := range releases {
		if rel == nil {
			continue
		}
		for _, copyRel := range rel.Copies() {
			if copyRel == nil {
				continue
			}
			flat := copyRel.Clone()
			flat.Variants = nil
			keys := dedupeReleaseKeys(flat)
			existing := -1
			for _, k := range keys {
				if idx, found := groupIndex[k]; found {
					existing = idx
					break
				}
			}
			if existing < 0 {
				existing = len(groups)
				groups = append(groups, nil)
			}
			groups[existing] = append(groups[existing], flat)
			for _, k := range keys {
				groupIndex[k] = existing
			}
		}
	}

	out := make([]*release.Release, 0, len(groups))
	for _, group := range groups {
		if merged := mergeGroup(group, opts.Rank); merged != nil {
			out = append(out, merged)
		}
	}
	return out
}

// mergeGroup picks the primary of one group of copies and folds the rest into
// it as variants.
func mergeGroup(group []*release.Release, rank func(*release.Release) int) *release.Release {
	if len(group) == 0 {
		return nil
	}
	ordered := make([]*release.Release, len(group))
	copy(ordered, group)
	order := make(map[*release.Release]int, len(ordered))
	for i, rel := range ordered {
		order[rel] = i
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if rank != nil {
			ra, rb := rank(a), rank(b)
			if ra != rb {
				return ra > rb
			}
		}
		if a.Grabs != b.Grabs {
			return a.Grabs > b.Grabs
		}
		aDate, aOK := a.PublishedAt()
		bDate, bOK := b.PublishedAt()
		if aOK && bOK && !aDate.Equal(bDate) {
			return aDate.After(bDate)
		}
		if aOK != bOK {
			return aOK
		}
		return order[a] < order[b]
	})

	primary := ordered[0]
	primary.Variants = append([]*release.Release(nil), ordered[1:]...)
	mergeCopyMetadata(primary, ordered[1:])
	return primary
}

// mergeCopyMetadata fills in what the primary's indexer did not report from
// the copies that did. Only the attributes that describe the release itself
// travel — everything identifying one NZB (link, details URL, GUID, indexer)
// stays on the copy it belongs to, because that is what failover switches
// between.
func mergeCopyMetadata(primary *release.Release, variants []*release.Release) {
	if primary == nil {
		return
	}
	for _, variant := range variants {
		if variant == nil {
			continue
		}
		if variant.Grabs > primary.Grabs {
			primary.Grabs = variant.Grabs
		}
		if primary.Size == 0 {
			primary.Size = variant.Size
		}
		if primary.PubDate == "" {
			primary.PubDate = variant.PubDate
		}
		if primary.Duration == 0 {
			primary.Duration = variant.Duration
		}
		for _, lang := range variant.Languages {
			if !containsFold(primary.Languages, lang) {
				primary.Languages = append(primary.Languages, lang)
			}
		}
	}
}

func containsFold(list []string, want string) bool {
	for _, have := range list {
		if strings.EqualFold(have, want) {
			return true
		}
	}
	return false
}

// DropCopies removes every copy whose details URL is in drop, promoting the
// best surviving variant when the primary itself goes. It returns nil when
// nothing is left, and reports whether anything was removed.
//
// It is how a per-NZB verdict — a persistent bad-release record, an
// availability report — applies to a merged result: the broken copy stops
// being a playback target without taking the copies that still work with it.
func DropCopies(rel *release.Release, drop map[string]bool) (*release.Release, bool) {
	if rel == nil {
		return nil, false
	}
	kept := make([]*release.Release, 0, rel.CopyCount())
	removed := false
	for _, c := range rel.Copies() {
		if c == nil {
			continue
		}
		if c.DetailsURL != "" && drop[c.DetailsURL] {
			removed = true
			continue
		}
		kept = append(kept, c)
	}
	if !removed {
		return rel, false
	}
	if len(kept) == 0 {
		return nil, true
	}
	primary := kept[0]
	primary.Variants = append([]*release.Release(nil), kept[1:]...)
	return primary, true
}
