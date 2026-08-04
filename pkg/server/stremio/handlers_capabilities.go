package stremio

import (
	"encoding/json"
	"strings"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
)

// libraryCapsForRelease returns the ffprobe capabilities persisted for a library
// release, or nil for non-library releases (whose real caps are not known until
// after pre-probe, so they can never be surfaced at list-build time).
func libraryCapsForRelease(rel *release.Release) *session.MediaCapabilities {
	if rel == nil || !rel.IsLibraryResult() {
		return nil
	}
	item, ok := rel.SourceIndexer.(*persistence.LibraryItem)
	if !ok || item == nil || strings.TrimSpace(item.MediaCapsJSON) == "" {
		return nil
	}
	var caps session.MediaCapabilities
	if err := json.Unmarshal([]byte(item.MediaCapsJSON), &caps); err != nil {
		return nil
	}
	return &caps
}

// capsSummaryLine renders the authoritative ffprobe capability summary for a
// library release (e.g. "hevc Main 10 2160p 10-bit HDR10"), or "" when caps are
// unknown (fresh indexer releases).
//
// This is deliberately client-agnostic: it informs the user/client of the file's
// codec/HDR so they can decide, and never gates or reorders by client — codec
// compatibility is the client/device decoder's responsibility.
func capsSummaryLine(rel *release.Release) string {
	if caps := libraryCapsForRelease(rel); caps != nil {
		return caps.Summary()
	}
	return ""
}
