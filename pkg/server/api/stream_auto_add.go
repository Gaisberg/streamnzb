package api

import (
	"strings"

	"streamnzb/pkg/core/config"
)

// lowerNameSet indexes items by their lowercased, trimmed name.
func lowerNameSet[T any](items []T, nameOf func(T) string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, it := range items {
		if n := strings.ToLower(strings.TrimSpace(nameOf(it))); n != "" {
			out[n] = true
		}
	}
	return out
}

func cloneStreamEntries(streams map[string]*config.StreamEntry) map[string]*config.StreamEntry {
	if streams == nil {
		return nil
	}
	cloned := make(map[string]*config.StreamEntry, len(streams))
	for username, stream := range streams {
		if stream == nil {
			continue
		}
		copied := *stream
		copied.ProviderSelections = append([]string(nil), stream.ProviderSelections...)
		copied.IndexerSelections = append([]string(nil), stream.IndexerSelections...)
		copied.MovieSearchQueries = append([]string(nil), stream.MovieSearchQueries...)
		copied.SeriesSearchQueries = append([]string(nil), stream.SeriesSearchQueries...)
		copied.IndexerOverrides = cloneIndexerOverrides(stream.IndexerOverrides)
		cloned[username] = &copied
	}
	return cloned
}

func cloneIndexerOverrides(overrides map[string]config.IndexerSearchConfig) map[string]config.IndexerSearchConfig {
	if overrides == nil {
		return nil
	}
	cloned := make(map[string]config.IndexerSearchConfig, len(overrides))
	for name, override := range overrides {
		cloned[name] = override
	}
	return cloned
}

func applyStreamAutoSelections(nextCfg *config.Config) {
	if nextCfg == nil {
		return
	}
	if nextCfg.Streams == nil {
		return
	}
	enabledProviders := enabledProviderNames(nextCfg.Providers)
	enabledIndexers := enabledIndexerNames(nextCfg.Indexers)
	for _, stream := range nextCfg.Streams {
		if stream == nil {
			continue
		}
		if stream.AutoAddProviders != nil && *stream.AutoAddProviders {
			stream.ProviderSelections = syncOrderedSelections(stream.ProviderSelections, enabledProviders)
		}
		if stream.AutoAddIndexers != nil && *stream.AutoAddIndexers {
			stream.IndexerSelections = syncOrderedSelections(stream.IndexerSelections, enabledIndexers)
			// A non-empty overrides map acts as the selected-indexer set at
			// search time, so every auto-added indexer needs an entry in it —
			// otherwise it is silently excluded from all searches. Empty/nil
			// maps mean "no restriction" and must stay empty.
			if len(stream.IndexerOverrides) > 0 {
				stream.IndexerOverrides = filterIndexerOverrides(stream.IndexerOverrides, stream.IndexerSelections)
				for _, name := range stream.IndexerSelections {
					if _, ok := stream.IndexerOverrides[name]; !ok {
						stream.IndexerOverrides[name] = config.IndexerSearchConfig{}
					}
				}
			}
		}
	}
}

func applyStreamNameRenames(streams map[string]*config.StreamEntry, providerRenames, indexerRenames map[string]string) {
	if len(providerRenames) == 0 && len(indexerRenames) == 0 {
		return
	}
	for _, stream := range streams {
		if stream == nil {
			continue
		}
		if len(providerRenames) > 0 {
			stream.ProviderSelections = renameSelectionList(stream.ProviderSelections, providerRenames)
		}
		if len(indexerRenames) > 0 {
			stream.IndexerSelections = renameSelectionList(stream.IndexerSelections, indexerRenames)
			stream.IndexerOverrides = renameIndexerOverrides(stream.IndexerOverrides, indexerRenames)
		}
	}
}

// applyFilterProfileRenames updates each stream's FilterProfileName when a filter
// profile is renamed, so streams keep pointing at the same profile under its new name.
func applyFilterProfileRenames(streams map[string]*config.StreamEntry, filterProfileRenames map[string]string) {
	if len(filterProfileRenames) == 0 {
		return
	}
	for _, stream := range streams {
		if stream == nil {
			continue
		}
		if current := strings.TrimSpace(stream.FilterProfileName); current != "" {
			if renamed, ok := filterProfileRenames[strings.ToLower(current)]; ok {
				stream.FilterProfileName = renamed
			}
		}
		// Per-content-kind bindings name profiles too, and would otherwise be
		// left pointing at a name that no longer exists.
		for kind, current := range stream.FilterProfileByType {
			current = strings.TrimSpace(current)
			if current == "" {
				continue
			}
			if renamed, ok := filterProfileRenames[strings.ToLower(current)]; ok {
				stream.FilterProfileByType[kind] = renamed
			}
		}
	}
}

func renamedNamesByIndex[TCfg any](current, next []TCfg, nameOf func(TCfg) string) map[string]string {
	limit := len(current)
	if len(next) < limit {
		limit = len(next)
	}
	renamed := make(map[string]string)
	for i := 0; i < limit; i++ {
		currentName := strings.TrimSpace(nameOf(current[i]))
		nextName := strings.TrimSpace(nameOf(next[i]))
		if currentName == "" || nextName == "" {
			continue
		}
		if strings.EqualFold(currentName, nextName) {
			continue
		}
		renamed[strings.ToLower(currentName)] = nextName
	}
	return renamed
}

func renameSelectionList(existing []string, renamedByLower map[string]string) []string {
	if len(existing) == 0 || len(renamedByLower) == 0 {
		return existing
	}
	next := make([]string, 0, len(existing))
	seen := make(map[string]bool, len(existing))
	for _, item := range existing {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		if renamed, ok := renamedByLower[strings.ToLower(name)]; ok {
			name = renamed
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		next = append(next, name)
	}
	return next
}

func renameIndexerOverrides(overrides map[string]config.IndexerSearchConfig, renamedByLower map[string]string) map[string]config.IndexerSearchConfig {
	if len(overrides) == 0 || len(renamedByLower) == 0 {
		return overrides
	}
	next := make(map[string]config.IndexerSearchConfig, len(overrides))
	for name, override := range overrides {
		target := strings.TrimSpace(name)
		if renamed, ok := renamedByLower[strings.ToLower(target)]; ok {
			target = renamed
		}
		if target == "" {
			continue
		}
		next[target] = override
	}
	return next
}

func syncOrderedSelections(existing []string, enabled []string) []string {
	if len(enabled) == 0 {
		return []string{}
	}
	enabledByLower := make(map[string]string, len(enabled))
	for _, name := range enabled {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		enabledByLower[strings.ToLower(trimmed)] = trimmed
	}
	seen := make(map[string]bool, len(enabledByLower))
	next := make([]string, 0, len(enabledByLower))
	for _, name := range existing {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		enabledName, ok := enabledByLower[key]
		if !ok {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		next = append(next, enabledName)
	}
	for _, name := range enabled {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		next = append(next, trimmed)
	}
	return next
}

func filterIndexerOverrides(
	overrides map[string]config.IndexerSearchConfig,
	selected []string,
) map[string]config.IndexerSearchConfig {
	filtered := make(map[string]config.IndexerSearchConfig, len(selected))
	for _, name := range selected {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		override, ok := overrides[trimmed]
		if !ok {
			continue
		}
		filtered[trimmed] = override
	}
	return filtered
}

// enabledNames lists the trimmed names of items that are not explicitly
// disabled (a nil Enabled pointer means enabled).
func enabledNames[T any](items []T, enabledOf func(T) *bool, nameOf func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if e := enabledOf(it); e != nil && !*e {
			continue
		}
		if name := strings.TrimSpace(nameOf(it)); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func enabledProviderNames(providers []config.Provider) []string {
	return enabledNames(providers,
		func(p config.Provider) *bool { return p.Enabled },
		func(p config.Provider) string { return p.Name })
}

func enabledIndexerNames(indexers []config.IndexerConfig) []string {
	return enabledNames(indexers,
		func(i config.IndexerConfig) *bool { return i.Enabled },
		func(i config.IndexerConfig) string { return i.Name })
}

// dropDeletedFilterProfiles clears references to profiles that no longer
// exist, so deleting one is not blocked by the streams still pointing at it.
// Renames are resolved first, so a renamed profile is never seen as deleted.
// A stream left with no profile falls back to returning everything, which is
// how a stream with none configured has always behaved.
func dropDeletedFilterProfiles(streams map[string]*config.StreamEntry, profiles []config.FilterProfileConfig) {
	known := lowerNameSet(profiles, func(x config.FilterProfileConfig) string { return x.Name })
	for _, stream := range streams {
		if stream == nil {
			continue
		}
		if name := strings.ToLower(strings.TrimSpace(stream.FilterProfileName)); name != "" && !known[name] {
			stream.FilterProfileName = ""
		}
		for kind, name := range stream.FilterProfileByType {
			if key := strings.ToLower(strings.TrimSpace(name)); key == "" || !known[key] {
				delete(stream.FilterProfileByType, kind)
			}
		}
		if len(stream.FilterProfileByType) == 0 {
			stream.FilterProfileByType = nil
		}
	}
}

// dropDeletedProviders clears references to providers that no longer exist
// from stream ProviderSelections.
func dropDeletedProviders(streams map[string]*config.StreamEntry, providers []config.Provider) {
	known := lowerNameSet(providers, func(x config.Provider) string { return x.Name })
	for _, stream := range streams {
		if stream == nil || len(stream.ProviderSelections) == 0 {
			continue
		}
		next := make([]string, 0, len(stream.ProviderSelections))
		for _, item := range stream.ProviderSelections {
			if key := strings.ToLower(strings.TrimSpace(item)); key != "" && known[key] {
				next = append(next, item)
			}
		}
		stream.ProviderSelections = next
	}
}

// dropDeletedIndexers clears references to indexers that no longer exist
// from stream IndexerSelections and IndexerOverrides.
func dropDeletedIndexers(streams map[string]*config.StreamEntry, indexers []config.IndexerConfig) {
	known := lowerNameSet(indexers, func(x config.IndexerConfig) string { return x.Name })
	for _, stream := range streams {
		if stream == nil {
			continue
		}
		if len(stream.IndexerSelections) > 0 {
			next := make([]string, 0, len(stream.IndexerSelections))
			for _, item := range stream.IndexerSelections {
				if key := strings.ToLower(strings.TrimSpace(item)); key != "" && known[key] {
					next = append(next, item)
				}
			}
			stream.IndexerSelections = next
		}
		if len(stream.IndexerOverrides) > 0 {
			for name := range stream.IndexerOverrides {
				if key := strings.ToLower(strings.TrimSpace(name)); key == "" || !known[key] {
					delete(stream.IndexerOverrides, name)
				}
			}
			if len(stream.IndexerOverrides) == 0 {
				stream.IndexerOverrides = nil
			}
		}
	}
}
