package config

import (
	"fmt"
	"sort"
	"strings"
)

// FormatProfileConfig is one named result-format profile. Streams bind a
// profile by name (StreamEntry.FormatProfileName); a stream with no binding
// renders the built-in format. Profiles are global: the stream decides which
// one to use, the profile carries no stream scoping.
type FormatProfileConfig struct {
	Name string `json:"name"`
	// ResultNameTemplate and ResultDescriptionTemplate are Go text/templates
	// over a result's FormatContext. Empty falls back to the built-in format
	// for that half.
	ResultNameTemplate        string `json:"result_name_template,omitempty"`
	ResultDescriptionTemplate string `json:"result_description_template,omitempty"`
}

// FormatProfileByName finds a profile by name, case-insensitively. Returns
// nil when the name is empty or unknown.
func (c *Config) FormatProfileByName(name string) *FormatProfileConfig {
	if c == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	for i := range c.FormatProfiles {
		if strings.EqualFold(c.FormatProfiles[i].Name, name) {
			return &c.FormatProfiles[i]
		}
	}
	return nil
}

// migrateFormatProfiles is the one-shot conversion of per-stream inline
// result templates into the profile list. nil FormatProfiles means "never
// migrated" (the field has no omitempty, so once written it round-trips as []
// even when the user deletes every profile — that state is left alone).
// Streams sharing identical templates collapse onto one profile, which is the
// point of profiles; inline fields are cleared so the binding is the single
// source of truth. Runs after applyStreamModelUpgradeDefaults so every stream
// exists.
func (c *Config) migrateFormatProfiles() bool {
	if c.FormatProfiles != nil {
		return false
	}
	c.FormatProfiles = []FormatProfileConfig{}

	names := make([]string, 0, len(c.Streams))
	for name, entry := range c.Streams {
		if entry != nil {
			names = append(names, name)
		}
	}
	// Deterministic profile numbering whatever the map iteration order.
	sort.Strings(names)

	type templatePair struct{ name, desc string }
	profileByPair := make(map[templatePair]string)
	for _, streamName := range names {
		entry := c.Streams[streamName]
		pair := templatePair{
			name: strings.TrimSpace(entry.ResultNameTemplate),
			desc: strings.TrimSpace(entry.ResultDescriptionTemplate),
		}
		if pair.name == "" && pair.desc == "" {
			continue
		}
		profileName, ok := profileByPair[pair]
		if !ok {
			profileName = "Custom"
			if n := len(profileByPair); n > 0 {
				profileName = fmt.Sprintf("Custom %d", n+1)
			}
			c.FormatProfiles = append(c.FormatProfiles, FormatProfileConfig{
				Name:                      profileName,
				ResultNameTemplate:        pair.name,
				ResultDescriptionTemplate: pair.desc,
			})
			profileByPair[pair] = profileName
		}
		entry.FormatProfileName = profileName
		entry.ResultNameTemplate = ""
		entry.ResultDescriptionTemplate = ""
	}
	return true
}
