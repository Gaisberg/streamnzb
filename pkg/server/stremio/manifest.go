package stremio

import (
	"encoding/json"
	"strings"
)

type ManifestBehaviorHints struct {
	Configurable          bool `json:"configurable,omitempty"`
	ConfigurationRequired bool `json:"configurationRequired,omitempty"`
}

type Manifest struct {
	ID            string                 `json:"id"`
	Version       string                 `json:"version"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	Resources     []string               `json:"resources"`
	Types         []string               `json:"types"`
	Catalogs      []Catalog              `json:"catalogs"`
	IDPrefixes    []string               `json:"idPrefixes,omitempty"`
	Background    string                 `json:"background,omitempty"`
	Logo          string                 `json:"logo,omitempty"`
	BehaviorHints *ManifestBehaviorHints `json:"behaviorHints,omitempty"`
}

type Catalog struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func manifestVersion(version string) string {
	if version == "" {
		version = "dev"
	}

	if len(version) > 0 && version[0] >= '0' && version[0] <= '9' {
		return version
	}

	return "0.0.0-" + strings.ReplaceAll(version, "-", ".")
}

func NewManifest(version string) *Manifest {
	if version == "" {
		version = "dev"
	}
	return &Manifest{
		ID:          "community.streamnzb",
		Version:     manifestVersion(version),
		Name:        "StreamNZB",
		Description: "Stream content directly from Usenet",
		Resources:   []string{"stream"},
		Types:       []string{"movie", "series", "anime"},
		Catalogs:    []Catalog{},
		IDPrefixes:  []string{"tt", "tmdb", "kitsu"},
		Logo:        "https://cdn.discordapp.com/icons/1470288400157380710/6f397b4a2e9561dc7ad43526588cfd67.png",
	}
}

func (m *Manifest) ToJSONForDevice(isAdmin bool) ([]byte, error) {

	out := *m
	out.BehaviorHints = &ManifestBehaviorHints{
		Configurable:          isAdmin,
		ConfigurationRequired: false,
	}
	return json.MarshalIndent(out, "", "  ")
}
