package stremio

import (
	"encoding/json"
	"strings"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
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
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Extra []CatalogExtra `json:"extra,omitempty"`
}

// CatalogExtra declares one extra a catalog supports (search, skip). Without
// the declaration clients never send the extra, so search and pagination hang
// off this.
type CatalogExtra struct {
	Name       string `json:"name"`
	IsRequired bool   `json:"isRequired,omitempty"`
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
		Name:        DefaultServiceName,
		Description: "Stream content directly from Usenet",
		Resources:   []string{"stream"},
		Types:       []string{"movie", "series", "anime", "other", "documentary", "tv"},
		Catalogs:    []Catalog{},
		IDPrefixes:  []string{"tt", "tmdb", "kitsu", "tvdb"},
		Logo:        "https://cdn.discordapp.com/icons/1470288400157380710/6f397b4a2e9561dc7ad43526588cfd67.png",
	}
}

// ForProfile renders the manifest for one stream's metadata profile: with a
// profile bound, the addon declares the meta and catalog resources plus the
// profile's enabled catalogs; with none (nil), the result is identical to the
// stream-only manifest this addon always served. Derived per request so
// config reloads and binding changes take effect without rebuilding the base
// manifest. dropProviders suppresses catalogs of providers that currently
// cannot serve — see enabledCatalogs.
//
// The value copy is shallow — fresh slices are assigned, never appended, so
// the shared base manifest's backing arrays are never written.
func (m *Manifest) ForProfile(profile *config.MetadataProfileConfig, dropProviders ...string) *Manifest {
	out := *m
	if profile != nil {
		out.Resources = []string{"stream", "catalog", "meta"}
		out.Catalogs = enabledCatalogs(profile, dropProviders...)
	}
	return &out
}

// DefaultServiceName is what a stream calls itself when it has no addon name
// override — in the manifest, and as the branding line on every result.
const DefaultServiceName = "StreamNZB"

// MaxAddonNameLength bounds a stream's addon name override. Clients render it
// in a list, so an unbounded string would push everything else off the row.
const MaxAddonNameLength = 60

// ServiceName is the label a stream presents itself under: its addon name
// override, or the default. Anything that brands a result should go through
// here — an override the client sees in its addon list but not on the results
// underneath it looks like two different addons.
func ServiceName(stream *auth.Stream) string {
	if stream != nil {
		if name := NormalizeAddonName(stream.AddonName); name != "" {
			return name
		}
	}
	return DefaultServiceName
}

// NormalizeAddonName trims an override and caps its length. Empty means "use
// the default name".
func NormalizeAddonName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > MaxAddonNameLength {
		name = strings.TrimSpace(name[:MaxAddonNameLength])
	}
	return name
}

// ToJSONForDevice renders the manifest for one requesting device.
//
// streamName is the stream config the token resolved to; it is appended to the
// service name so multiple installed configs are tellable apart in client addon
// lists. addonName overrides that whole label — a stream that sets one is asking
// for a specific name in the client, not a suffix on ours.
func (m *Manifest) ToJSONForDevice(isAdmin bool, streamName, addonName string) ([]byte, error) {

	out := *m
	if override := NormalizeAddonName(addonName); override != "" {
		out.Name = override
	} else if name := strings.TrimSpace(streamName); name != "" {
		out.Name = out.Name + " · " + name
	}
	out.BehaviorHints = &ManifestBehaviorHints{
		Configurable:          isAdmin,
		ConfigurationRequired: false,
	}
	return json.MarshalIndent(out, "", "  ")
}
