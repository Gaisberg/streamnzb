package api

import (
	"strings"

	"streamnzb/pkg/core/config"
)

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
			if stream.IndexerOverrides != nil {
				stream.IndexerOverrides = filterIndexerOverrides(stream.IndexerOverrides, stream.IndexerSelections)
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
		current := strings.TrimSpace(stream.FilterProfileName)
		if current == "" {
			continue
		}
		if renamed, ok := filterProfileRenames[strings.ToLower(current)]; ok {
			stream.FilterProfileName = renamed
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

func enabledProviderNames(providers []config.Provider) []string {
	enabled := make([]string, 0, len(providers))
	for _, provider := range providers {
		if provider.Enabled != nil && !*provider.Enabled {
			continue
		}
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			continue
		}
		enabled = append(enabled, name)
	}
	return enabled
}

func enabledIndexerNames(indexers []config.IndexerConfig) []string {
	enabled := make([]string, 0, len(indexers))
	for _, indexer := range indexers {
		if indexer.Enabled != nil && !*indexer.Enabled {
			continue
		}
		name := strings.TrimSpace(indexer.Name)
		if name == "" {
			continue
		}
		enabled = append(enabled, name)
	}
	return enabled
}
