package search

import (
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
