package indexer

import (
	"encoding/xml"
	"strings"
)

type Caps struct {
	Categories    []CapsCategory `json:"categories"`
	Searching     CapsSearching  `json:"searching"`
	Limits        CapsLimits     `json:"limits"`
	RetentionDays int            `json:"retention_days"`
}

type CapsCategory struct {
	ID      string         `xml:"id,attr" json:"id"`
	Name    string         `xml:"name,attr" json:"name"`
	Subcats []CapsCategory `xml:"subcat" json:"subcats,omitempty"`
}

type CapsSearching struct {
	Search                     bool            `json:"search"`
	TVSearch                   bool            `json:"tv_search"`
	MovieSearch                bool            `json:"movie_search"`
	AudioSearch                bool            `json:"audio_search"`
	BookSearch                 bool            `json:"book_search"`
	SearchSupportedParams      map[string]bool `json:"search_supported_params,omitempty"`
	TVSearchSupportedParams    map[string]bool `json:"tv_search_supported_params,omitempty"`
	MovieSearchSupportedParams map[string]bool `json:"movie_search_supported_params,omitempty"`
	AudioSearchSupportedParams map[string]bool `json:"audio_search_supported_params,omitempty"`
	BookSearchSupportedParams  map[string]bool `json:"book_search_supported_params,omitempty"`
}

type CapsLimits struct {
	Max     int `json:"max"`
	Default int `json:"default"`
}

type capsXML struct {
	XMLName    xml.Name          `xml:"caps"`
	Limits     capsXMLLimits     `xml:"limits"`
	Retention  capsXMLRetention  `xml:"retention"`
	Searching  capsXMLSearching  `xml:"searching"`
	Categories capsXMLCategories `xml:"categories"`
}

type capsXMLLimits struct {
	Max     int `xml:"max,attr"`
	Default int `xml:"default,attr"`
}

type capsXMLRetention struct {
	Days int `xml:"days,attr"`
}

type capsXMLSearching struct {
	Search      capsXMLSearchType `xml:"search"`
	TVSearch    capsXMLSearchType `xml:"tv-search"`
	MovieSearch capsXMLSearchType `xml:"movie-search"`
	AudioSearch capsXMLSearchType `xml:"audio-search"`
	BookSearch  capsXMLSearchType `xml:"book-search"`
}

type capsXMLSearchType struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type capsXMLCategories struct {
	Categories []CapsCategory `xml:"category"`
}

func ParseCapsXML(data []byte) (*Caps, error) {
	var raw capsXML
	if err := xml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &Caps{
		Categories: raw.Categories.Categories,
		Searching: CapsSearching{
			Search:                     raw.Searching.Search.Available == "yes",
			TVSearch:                   raw.Searching.TVSearch.Available == "yes",
			MovieSearch:                raw.Searching.MovieSearch.Available == "yes",
			AudioSearch:                raw.Searching.AudioSearch.Available == "yes",
			BookSearch:                 raw.Searching.BookSearch.Available == "yes",
			SearchSupportedParams:      parseSupportedParams(raw.Searching.Search.SupportedParams),
			TVSearchSupportedParams:    parseSupportedParams(raw.Searching.TVSearch.SupportedParams),
			MovieSearchSupportedParams: parseSupportedParams(raw.Searching.MovieSearch.SupportedParams),
			AudioSearchSupportedParams: parseSupportedParams(raw.Searching.AudioSearch.SupportedParams),
			BookSearchSupportedParams:  parseSupportedParams(raw.Searching.BookSearch.SupportedParams),
		},
		Limits: CapsLimits{
			Max:     raw.Limits.Max,
			Default: raw.Limits.Default,
		},
		RetentionDays: raw.Retention.Days,
	}, nil
}

func parseSupportedParams(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	params := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		params[part] = true
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

type IndexerWithCaps interface {
	GetCaps() (*Caps, error)
}
