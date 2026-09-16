package api

import (
	"net/http"

	"github.com/dreulavelle/jhin"
	jhinrules "github.com/dreulavelle/jhin/rules"

	"streamnzb/pkg/search/rules"
	"streamnzb/pkg/server/stremio"
)

// capabilities is what this build understands, for a client that has to
// decide whether a profile or a template it is about to write will work here.
//
// The release version alone cannot answer that: the rule vocabulary and the
// template fields both grow in patch releases, and a client validating
// against a version table would either refuse features that work or emit
// ones that do not. So the surface is reported rather than implied.
type capabilities struct {
	// StreamNZB is the running build's version, "dev" for an untagged build.
	StreamNZB string           `json:"streamnzb"`
	Jhin      jhinInfo         `json:"jhin"`
	Rules     rules.Vocabulary `json:"rules"`
	Formatter formatterInfo    `json:"formatter"`
}

// jhinInfo is the engine underneath. SyntaxVersion is the one number a client
// should branch on: it is the rule language's own version, which jhin bumps
// when the grammar changes, independently of its release version.
type jhinInfo struct {
	Version       string `json:"version"`
	SyntaxVersion int    `json:"syntax_version"`
}

// formatterInfo is the result-format template surface.
type formatterInfo struct {
	Fields  []stremio.FormatField  `json:"fields"`
	Helpers []stremio.FormatHelper `json:"helpers"`
	Syntax  string                 `json:"syntax"`
}

// handleCapabilities reports the versions and the full vocabulary. It is a
// GET with no inputs, so it is the one place a client can start from.
//
// Admin-only, like the config and explain endpoints it exists to support:
// the field list names the fact sources this install has (SeaDex, the
// availability database, what its probes record), which is configuration
// rather than something every device should enumerate.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can read capabilities", http.MethodGet) {
		return
	}
	version := "dev"
	if s.strmServer != nil {
		version = s.strmServer.Version()
	}
	writeJSON(w, http.StatusOK, capabilities{
		StreamNZB: version,
		Jhin: jhinInfo{
			Version:       jhin.Version().String(),
			SyntaxVersion: jhinrules.SyntaxVersion,
		},
		Rules: rules.Describe(),
		Formatter: formatterInfo{
			Fields:  stremio.FormatFields(),
			Helpers: stremio.FormatHelpers(),
			Syntax:  stremio.FormatSyntaxNote,
		},
	})
}
