package stremio

import (
	"strings"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
)

// metadataProfileFor resolves the metadata profile serving one stream's
// request, or nil when metadata is off for that stream: the global
// METADATA_ENABLED kill-switch is down, the stream has no binding, or the
// bound name no longer exists (config validation prevents that, but a stale
// name degrades to metadata-off rather than erroring).
//
// The admin token authenticates as a synthesized stream that exists in no
// config and so can never carry a binding; it falls back to the Default
// profile, else the first profile, so the admin's own install keeps its
// catalogs after the profile migration.
func (s *Server) metadataProfileFor(stream *auth.Stream) *config.MetadataProfileConfig {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	if cfg == nil || !cfg.EffectiveMetadataEnabled() || stream == nil {
		return nil
	}
	name := strings.TrimSpace(stream.MetadataProfileName)
	if name == "" {
		if stream.Username == cfg.GetAdminUsername() {
			if p := cfg.MetadataProfileByName(config.DefaultMetadataProfileName); p != nil {
				return p
			}
			if len(cfg.MetadataProfiles) > 0 {
				return &cfg.MetadataProfiles[0]
			}
		}
		return nil
	}
	p := cfg.MetadataProfileByName(name)
	if p == nil {
		logger.Warn("Stream references unknown metadata profile; metadata off for this stream",
			"stream", streamLogName(stream), "profile", name)
	}
	return p
}
