package api

import (
	"encoding/json"
	"path/filepath"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/paths"
)

// applyConfigPatch runs the full config-save pipeline shared by the HTTP PUT
// and WebSocket transports: merge the JSON patch over the current config,
// carry over admin/env-managed fields, apply provider/indexer/filter-profile
// renames and drops, normalize, validate, persist, clear affected caches and
// schedule the async reload.
//
// On success it returns the user-facing cache-clear suffix. Validation
// problems come back in fieldErrors; any other failure returns errMessage, a
// user-safe message for the transport to render.
func (s *Server) applyConfigPatch(patch []byte) (cacheSuffix string, fieldErrors map[string]string, errMessage string) {
	s.mu.RLock()
	currentCfg := s.config
	currentLoadedPath := s.config.LoadedPath
	s.mu.RUnlock()

	currentJSON, err := json.Marshal(currentCfg)
	if err != nil {
		return "", nil, "Failed to prepare config update"
	}

	var newCfg config.Config
	if err := json.Unmarshal(currentJSON, &newCfg); err != nil {
		return "", nil, "Failed to prepare config update"
	}
	clearPatchedFilterProfiles(patch, &newCfg)
	if err := json.Unmarshal(patch, &newCfg); err != nil {
		return "", nil, "Invalid config data"
	}
	newCfg.AvailNZBMode = config.NormalizeAvailNZBMode(newCfg.AvailNZBMode)

	config.CopyEnvOverridesFrom(currentCfg, &newCfg)
	newCfg.AdminPasswordHash = currentCfg.AdminPasswordHash
	newCfg.AdminToken = currentCfg.AdminToken
	newCfg.AdminMustChangePassword = currentCfg.AdminMustChangePassword
	newCfg.Streams = cloneStreamEntries(currentCfg.Streams)
	providerRenames := renamedNamesByIndex(currentCfg.Providers, newCfg.Providers, func(provider config.Provider) string {
		return provider.Name
	})
	indexerRenames := renamedNamesByIndex(currentCfg.Indexers, newCfg.Indexers, func(indexer config.IndexerConfig) string {
		return indexer.Name
	})
	filterProfileRenames := renamedNamesByIndex(currentCfg.FilterProfiles, newCfg.FilterProfiles, func(fp config.FilterProfileConfig) string {
		return fp.Name
	})
	metadataProfileRenames := renamedNamesByIndex(currentCfg.MetadataProfiles, newCfg.MetadataProfiles, func(mp config.MetadataProfileConfig) string {
		return mp.Name
	})
	formatProfileRenames := renamedNamesByIndex(currentCfg.FormatProfiles, newCfg.FormatProfiles, func(fp config.FormatProfileConfig) string {
		return fp.Name
	})
	applyStreamNameRenames(newCfg.Streams, providerRenames, indexerRenames)
	applyFilterProfileRenames(newCfg.Streams, filterProfileRenames)
	applyMetadataProfileRenames(newCfg.Streams, metadataProfileRenames)
	applyFormatProfileRenames(newCfg.Streams, formatProfileRenames)
	dropDeletedProviders(newCfg.Streams, newCfg.Providers)
	dropDeletedIndexers(newCfg.Streams, newCfg.Indexers)
	dropDeletedFilterProfiles(newCfg.Streams, newCfg.FilterProfiles)
	dropDeletedMetadataProfiles(newCfg.Streams, newCfg.MetadataProfiles)
	dropDeletedFormatProfiles(newCfg.Streams, newCfg.FormatProfiles)
	newCfg.ApplyProviderDefaults()
	applyStreamAutoSelections(&newCfg)
	if newCfg.AdminUsername == "" {
		newCfg.AdminUsername = currentCfg.GetAdminUsername()
	}
	// A blank Newznab key locks the endpoint rather than opening it, so a save
	// that clears the field gets a fresh key instead of an unusable one.
	if newCfg.NewznabAPIKey == "" {
		if key, err := config.NewAPIKey(); err == nil {
			newCfg.NewznabAPIKey = key
		}
	}

	plan := validationPlanFromPatch(patch, currentCfg, &newCfg)
	if fieldErrors := s.validateConfigWithPlan(&newCfg, plan); len(fieldErrors) > 0 {
		return "", fieldErrors, ""
	}

	if currentLoadedPath == "" {
		currentLoadedPath = filepath.Join(paths.GetDataDir(), "config.json")
	}
	newCfg.LoadedPath = currentLoadedPath

	s.mu.Lock()
	s.config = &newCfg
	s.mu.Unlock()

	if err := newCfg.Save(); err != nil {
		return "", nil, "Failed to save config: " + err.Error()
	}

	syncComponentHealth(currentCfg, &newCfg)
	cacheSuffix = s.clearCachesForScope(cacheClearScopeForPatch(patch))
	s.reloadConfigAsync(&newCfg)
	return cacheSuffix, nil, ""
}
