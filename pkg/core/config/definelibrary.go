package config

// DefineLibraryConfig is a shared bundle of define rules — release-group
// tiers, known-bad-group lists, community classifications — kept once and
// available to every filter profile through matched("Name"). A library
// separates the data from the policy: it may only carry define rules, so an
// upstream library can never smuggle in a score or reject rule, and what a
// tier is worth stays each profile's own decision.
//
// Libraries are consumed at compile time: a profile's rules resolve
// matched() against its own rules first, then against every library's, so a
// profile rule under the same name shadows the library's. A library must be
// self-contained — its defines may reference each other but never a
// profile's rules — which is what lets one library serve many profiles.
type DefineLibraryConfig struct {
	Name string `json:"name"`
	// Rules are the library's defines. Every rule must have the define
	// action; validation refuses anything else.
	Rules []RuleConfig `json:"rules,omitempty"`
	// Source links the library to the remote file it was imported from, for
	// the manual Refresh flow. Nil for a library made or pasted locally.
	Source *ProfileSourceConfig `json:"source,omitempty"`
}

// DefineLibraryRules flattens every library's rules into the one slice
// profile compilation resolves references against.
func DefineLibraryRules(libs []DefineLibraryConfig) []RuleConfig {
	var out []RuleConfig
	for i := range libs {
		out = append(out, libs[i].Rules...)
	}
	return out
}
