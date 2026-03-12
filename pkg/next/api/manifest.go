package api

import "strings"

type addonManifestBehaviorHints struct {
	Configurable          bool `json:"configurable,omitempty"`
	ConfigurationRequired bool `json:"configurationRequired,omitempty"`
}

type addonManifestCatalog struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type addonManifest struct {
	ID            string                      `json:"id"`
	Version       string                      `json:"version"`
	Name          string                      `json:"name"`
	Description   string                      `json:"description"`
	Resources     []string                    `json:"resources"`
	Types         []string                    `json:"types"`
	Catalogs      []addonManifestCatalog      `json:"catalogs"`
	IDPrefixes    []string                    `json:"idPrefixes,omitempty"`
	BehaviorHints *addonManifestBehaviorHints `json:"behaviorHints,omitempty"`
}

func newAddonManifest(version string) addonManifest {
	return addonManifest{
		ID:          "community.streamnzb.next",
		Version:     addonManifestVersion(version),
		Name:        "StreamNZB Next",
		Description: "Simplified Usenet streaming for AIOStreams and compatible addon clients",
		Resources:   []string{"stream"},
		Types:       []string{"movie", "series"},
		Catalogs:    []addonManifestCatalog{},
		IDPrefixes:  []string{"tt", "tmdb", "tvdb"},
		BehaviorHints: &addonManifestBehaviorHints{
			Configurable:          false,
			ConfigurationRequired: false,
		},
	}
}

func addonManifestVersion(version string) string {
	if version == "" {
		version = "dev"
	}

	if version[0] >= '0' && version[0] <= '9' {
		return version
	}

	return "0.0.0-" + strings.ReplaceAll(version, "-", ".")
}
