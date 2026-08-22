package newznab

import (
	"encoding/xml"
	"net/http"
	"sort"
	"strings"

	"streamnzb/pkg/indexer"
)

type capsDocument struct {
	XMLName    xml.Name       `xml:"caps"`
	Server     capsServer     `xml:"server"`
	Limits     capsLimits     `xml:"limits"`
	Retention  *capsRetention `xml:"retention,omitempty"`
	Registr    capsRegistr    `xml:"registration"`
	Searching  capsSearching  `xml:"searching"`
	Categories capsCategories `xml:"categories"`
}

type capsServer struct {
	Version   string `xml:"version,attr"`
	Title     string `xml:"title,attr"`
	Strapline string `xml:"strapline,attr"`
	Email     string `xml:"email,attr"`
	URL       string `xml:"url,attr"`
	Image     string `xml:"image,attr"`
}

type capsLimits struct {
	Max     int `xml:"max,attr"`
	Default int `xml:"default,attr"`
}

type capsRetention struct {
	Days int `xml:"days,attr"`
}

type capsRegistr struct {
	Available string `xml:"available,attr"`
	Open      string `xml:"open,attr"`
}

type capsSearching struct {
	Search      capsSearchType `xml:"search"`
	TVSearch    capsSearchType `xml:"tv-search"`
	MovieSearch capsSearchType `xml:"movie-search"`
	AudioSearch capsSearchType `xml:"audio-search"`
	BookSearch  capsSearchType `xml:"book-search"`
}

type capsSearchType struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type capsCategories struct {
	Categories []capsCategory `xml:"category"`
}

type capsCategory struct {
	ID      string       `xml:"id,attr"`
	Name    string       `xml:"name,attr"`
	Subcats []capsSubcat `xml:"subcat"`
}

type capsSubcat struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

// forwardableParams are the Newznab search parameters this endpoint passes on
// to indexers. A parameter an indexer accepts but we would drop has no place
// in our caps: advertising it would promise filtering that never happens.
var forwardableParams = map[string]bool{
	"q":        true,
	"cat":      true,
	"season":   true,
	"ep":       true,
	"imdbid":   true,
	"tmdbid":   true,
	"tvdbid":   true,
	"tvmazeid": true,
	"traktid":  true,
	"rid":      true,
	"genre":    true,
	"year":     true,
	"maxage":   true,
	"minage":   true,
	"minsize":  true,
	"maxsize":  true,
	"group":    true,
	"author":   true,
	"title":    true,
	"artist":   true,
	"album":    true,
	"label":    true,
	"track":    true,
	"attrs":    true,
	"extended": true,
}

// Base parameters per function: what the endpoint honours regardless of what
// any indexer publishes, plus the id parameters to claim when no indexer
// published caps at all (the Newznab client assumes the same set in that case).
var (
	baseSearchParams      = []string{"q", "cat", "limit", "offset"}
	baseTVSearchParams    = []string{"q", "cat", "limit", "offset", "season", "ep"}
	fallbackTVIDParams    = []string{"tvdbid", "tmdbid", "imdbid"}
	baseMovieParams       = []string{"q", "cat", "limit", "offset"}
	fallbackMovieIDParams = []string{"imdbid", "tmdbid"}
	baseAudioParams       = []string{"q", "cat", "limit", "offset"}
	baseBookParams        = []string{"q", "cat", "limit", "offset"}
)

// supportedParams renders one function's supportedParams attribute: the base
// set the endpoint always honours, widened by whatever the indexers accept and
// this endpoint forwards. With no published caps the fallback set stands in.
func supportedParams(base []string, published map[string]bool, fallback []string) string {
	seen := make(map[string]bool, len(base)+len(published))
	params := make([]string, 0, len(base)+len(published))
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		params = append(params, name)
	}
	for _, name := range base {
		add(name)
	}
	if len(published) == 0 {
		for _, name := range fallback {
			add(name)
		}
		return strings.Join(params, ",")
	}
	extra := make([]string, 0, len(published))
	for name := range published {
		if forwardableParams[name] && !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		add(name)
	}
	return strings.Join(params, ",")
}

// searchFunction renders one <search>-style element. An unavailable function
// advertises no parameters: a client should read it as closed, not as one it
// could call with the right arguments.
func searchFunction(available bool, base []string, published map[string]bool, fallback []string) capsSearchType {
	if !available {
		return capsSearchType{Available: "no"}
	}
	return capsSearchType{
		Available:       "yes",
		SupportedParams: supportedParams(base, published, fallback),
	}
}

// buildCaps renders the merged capabilities as the caps document a Newznab
// client reads on setup.
func buildCaps(merged *indexer.Caps, version, siteURL string) *capsDocument {
	doc := &capsDocument{
		Server: capsServer{
			Version:   version,
			Title:     serverTitle,
			Strapline: serverDescription,
			URL:       siteURL,
		},
		Limits:  capsLimits{Max: merged.Limits.Max, Default: merged.Limits.Default},
		Registr: capsRegistr{Available: "no", Open: "no"},
		Searching: capsSearching{
			Search:      searchFunction(merged.Searching.Search, baseSearchParams, merged.Searching.SearchSupportedParams, nil),
			TVSearch:    searchFunction(merged.Searching.TVSearch, baseTVSearchParams, merged.Searching.TVSearchSupportedParams, fallbackTVIDParams),
			MovieSearch: searchFunction(merged.Searching.MovieSearch, baseMovieParams, merged.Searching.MovieSearchSupportedParams, fallbackMovieIDParams),
			AudioSearch: searchFunction(merged.Searching.AudioSearch, baseAudioParams, merged.Searching.AudioSearchSupportedParams, nil),
			BookSearch:  searchFunction(merged.Searching.BookSearch, baseBookParams, merged.Searching.BookSearchSupportedParams, nil),
		},
	}
	if merged.RetentionDays > 0 {
		doc.Retention = &capsRetention{Days: merged.RetentionDays}
	}
	doc.Categories.Categories = make([]capsCategory, 0, len(merged.Categories))
	for _, cat := range merged.Categories {
		converted := capsCategory{ID: cat.ID, Name: cat.Name}
		for _, sub := range cat.Subcats {
			converted.Subcats = append(converted.Subcats, capsSubcat{ID: sub.ID, Name: sub.Name})
		}
		doc.Categories.Categories = append(doc.Categories.Categories, converted)
	}
	return doc
}

// handleCaps serves t=caps: what the configured indexers can do between them,
// merged into one capability document. Always XML — o=json shapes the result
// feed, and no Newznab client reads capabilities any other way.
func (s *Server) handleCaps(w http.ResponseWriter, r *http.Request) {
	doc := buildCaps(s.mergedCaps(), s.version(), s.baseURL(r))
	writeXML(w, http.StatusOK, xmlMediaType, doc)
}
