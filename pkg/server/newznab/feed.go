package newznab

import (
	"encoding/xml"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
)

// Newznab namespaces. Go's encoder has no notion of an output prefix, so the
// prefixed names below are written literally and the declarations ride along
// as plain attributes on <rss>.
const (
	newznabNamespace = "http://www.newznab.com/DTD/2010/feeds/attributes/"
	atomNamespace    = "http://www.w3.org/2005/Atom"
	nzbMediaType     = "application/x-nzb"
)

type feed struct {
	XMLName   xml.Name    `xml:"rss"`
	Version   string      `xml:"version,attr"`
	AtomNS    string      `xml:"xmlns:atom,attr"`
	NewznabNS string      `xml:"xmlns:newznab,attr"`
	Channel   feedChannel `xml:"channel"`
}

type feedChannel struct {
	AtomLink    *feedAtomLink `xml:"atom:link,omitempty"`
	Title       string        `xml:"title"`
	Description string        `xml:"description"`
	Link        string        `xml:"link"`
	Language    string        `xml:"language,omitempty"`
	Response    feedResponse  `xml:"newznab:response"`
	Items       []feedItem    `xml:"item"`
}

type feedAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type feedResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type feedItem struct {
	Title       string        `xml:"title"`
	GUID        feedGUID      `xml:"guid"`
	Link        string        `xml:"link"`
	Comments    string        `xml:"comments,omitempty"`
	PubDate     string        `xml:"pubDate,omitempty"`
	Category    string        `xml:"category,omitempty"`
	Description string        `xml:"description,omitempty"`
	Enclosure   feedEnclosure `xml:"enclosure"`
	Attrs       []feedAttr    `xml:"newznab:attr"`
}

type feedGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type feedEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type feedAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// newFeed starts an empty feed with the channel metadata every response
// carries.
func newFeed(selfURL, siteURL string, offset int) *feed {
	f := &feed{
		Version:   "2.0",
		AtomNS:    atomNamespace,
		NewznabNS: newznabNamespace,
		Channel: feedChannel{
			Title:       serverTitle,
			Description: serverDescription,
			Link:        siteURL,
			Language:    "en-gb",
			Response:    feedResponse{Offset: offset},
			Items:       []feedItem{},
		},
	}
	if selfURL != "" {
		f.Channel.AtomLink = &feedAtomLink{Href: selfURL, Rel: "self", Type: "application/rss+xml"}
	}
	return f
}

// itemLinker turns one aggregated result into the download URL clients should
// call back on. It returns "" for a result whose origin cannot be sealed,
// which drops the item: a link we cannot rewrite is one that would hand the
// caller an indexer's API key.
type itemLinker func(item *indexer.Item) string

// buildFeedItems rewrites aggregated results into feed items. Every download
// URL is replaced by one of ours, and the source attributes are carried
// through untouched so clients keep the metadata their parsers rely on.
func buildFeedItems(items []indexer.Item, link itemLinker) []feedItem {
	out := make([]feedItem, 0, len(items))
	for i := range items {
		item := &items[i]
		downloadURL := link(item)
		if downloadURL == "" {
			continue
		}
		size := item.Size
		if size <= 0 {
			size = item.Enclosure.Length
		}
		converted := feedItem{
			Title:       item.Title,
			GUID:        feedGUID{IsPermaLink: "false", Value: guidFor(item, downloadURL)},
			Link:        downloadURL,
			Comments:    item.ReleaseDetailsURL(),
			PubDate:     item.PubDate,
			Category:    item.Category,
			Description: item.Description,
			Enclosure:   feedEnclosure{URL: downloadURL, Length: size, Type: nzbMediaType},
			Attrs:       make([]feedAttr, 0, len(item.Attributes)+1),
		}
		hasSize := false
		for _, attr := range item.Attributes {
			if strings.EqualFold(attr.Name, "size") {
				hasSize = true
			}
			converted.Attrs = append(converted.Attrs, feedAttr{Name: attr.Name, Value: attr.Value})
		}
		if !hasSize && size > 0 {
			converted.Attrs = append(converted.Attrs, feedAttr{Name: "size", Value: strconv.FormatInt(size, 10)})
		}
		out = append(out, converted)
	}
	return out
}

// guidFor keeps the indexer's own guid when it is an opaque id, and falls back
// to the download URL. A guid that is a link to the source would leak that
// indexer's API key, so those are replaced.
func guidFor(item *indexer.Item, downloadURL string) string {
	guid := strings.TrimSpace(item.GUID)
	if guid != "" && !strings.Contains(guid, "://") {
		return guid
	}
	return downloadURL
}

// sortItemsByDate orders a merged result set newest first. The aggregator
// sorts by size because that is what ranking wants; a feed is read as a
// chronological listing.
func sortItemsByDate(items []indexer.Item) {
	dates := make([]int64, len(items))
	for i := range items {
		if t, ok := release.ParseDate(items[i].PubDate); ok {
			dates[i] = t.Unix()
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return dates[i] > dates[j] })
}

// jsonFeed mirrors the RSS document in the "@attributes" shape Newznab's
// o=json output uses, so a client that asks for JSON gets the same data under
// the field names it expects.
type jsonFeed struct {
	Channel jsonChannel `json:"channel"`
}

type jsonChannel struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Link        string         `json:"link"`
	Language    string         `json:"language,omitempty"`
	Response    jsonAttributes `json:"response"`
	Items       []jsonItem     `json:"item"`
}

type jsonItem struct {
	Title       string           `json:"title"`
	GUID        string           `json:"guid"`
	Link        string           `json:"link"`
	Comments    string           `json:"comments,omitempty"`
	PubDate     string           `json:"pubDate,omitempty"`
	Category    string           `json:"category,omitempty"`
	Description string           `json:"description,omitempty"`
	Enclosure   jsonAttributes   `json:"enclosure"`
	Attrs       []jsonAttributes `json:"attr"`
}

type jsonAttributes struct {
	Attributes map[string]string `json:"@attributes"`
}

func attributes(pairs ...string) jsonAttributes {
	out := jsonAttributes{Attributes: make(map[string]string, len(pairs)/2)}
	for i := 0; i+1 < len(pairs); i += 2 {
		out.Attributes[pairs[i]] = pairs[i+1]
	}
	return out
}

func (f *feed) toJSON() jsonFeed {
	out := jsonFeed{Channel: jsonChannel{
		Title:       f.Channel.Title,
		Description: f.Channel.Description,
		Link:        f.Channel.Link,
		Language:    f.Channel.Language,
		Response: attributes(
			"offset", strconv.Itoa(f.Channel.Response.Offset),
			"total", strconv.Itoa(f.Channel.Response.Total),
		),
		Items: make([]jsonItem, 0, len(f.Channel.Items)),
	}}
	for _, item := range f.Channel.Items {
		converted := jsonItem{
			Title:       item.Title,
			GUID:        item.GUID.Value,
			Link:        item.Link,
			Comments:    item.Comments,
			PubDate:     item.PubDate,
			Category:    item.Category,
			Description: item.Description,
			Enclosure: attributes(
				"url", item.Enclosure.URL,
				"length", strconv.FormatInt(item.Enclosure.Length, 10),
				"type", item.Enclosure.Type,
			),
			Attrs: make([]jsonAttributes, 0, len(item.Attrs)),
		}
		for _, attr := range item.Attrs {
			converted.Attrs = append(converted.Attrs, attributes("name", attr.Name, "value", attr.Value))
		}
		out.Channel.Items = append(out.Channel.Items, converted)
	}
	return out
}

// downloadURL builds the endpoint's own t=get link for a sealed reference.
func downloadURL(baseURL, apiKey, id string) string {
	params := url.Values{}
	params.Set("t", "get")
	params.Set("id", id)
	if apiKey != "" {
		params.Set("apikey", apiKey)
	}
	return baseURL + APIPath + "?" + params.Encode()
}

// linkerFor returns the itemLinker that seals each result's origin under
// secret and points it back at baseURL.
func linkerFor(secret, baseURL, apiKey, class string) itemLinker {
	return func(item *indexer.Item) string {
		source := strings.TrimSpace(item.Link)
		if source == "" {
			source = strings.TrimSpace(item.Enclosure.URL)
		}
		name := ""
		if item.SourceIndexer != nil {
			name = item.SourceIndexer.Name()
		}
		if source == "" || name == "" {
			logger.Debug("Newznab result dropped", "reason", "no resolvable download source", "title", item.Title, "indexer", name)
			return ""
		}
		id, err := sealGrabRef(secret, grabRef{Indexer: name, URL: source, Title: item.Title, Class: class})
		if err != nil {
			logger.Warn("Newznab result dropped", "reason", "failed to seal download reference", "title", item.Title, "err", err)
			return ""
		}
		return downloadURL(baseURL, apiKey, id)
	}
}
