package stremio

import (
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
)

type StreamResponse struct {
	Streams []Stream `json:"streams"`
}

type Stream struct {
	FailoverID string `json:"failoverId,omitempty"`

	URL string `json:"url,omitempty"`

	ExternalUrl string `json:"externalUrl,omitempty"`

	Name string `json:"name,omitempty"`

	Score int `json:"-"`

	ParsedMetadata *parser.ParsedRelease `json:"-"`

	Release *release.Release `json:"-"`

	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`

	// Languages are the release's languages as ISO 639-1 codes, from the
	// release name and the indexer's own tag. Stremio ignores the field;
	// aggregators that filter by language read it instead of guessing from
	// the description text.
	Languages []string `json:"languages,omitempty"`

	BehaviorHints *BehaviorHints `json:"behaviorHints,omitempty"`
	StreamType    string         `json:"streamType,omitempty"`
}

type BehaviorHints struct {
	NotWebReady      bool     `json:"notWebReady,omitempty"`
	BingeGroup       string   `json:"bingeGroup,omitempty"`
	CountryWhitelist []string `json:"countryWhitelist,omitempty"`
	VideoSize        int64    `json:"videoSize,omitempty"`
	Filename         string   `json:"filename,omitempty"`

	Cached *bool `json:"cached,omitempty"`
}

type SearchReleasesResponse struct {
	Releases []SearchReleaseTag `json:"releases"`
}

type SearchReleaseTag struct {
	Title        string `json:"title"`
	Link         string `json:"link"`
	DetailsURL   string `json:"details_url"`
	Size         int64  `json:"size"`
	Indexer      string `json:"indexer"`
	Availability string `json:"availability"`
}
