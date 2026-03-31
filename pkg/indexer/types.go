package indexer

import (
	"context"
	"encoding/xml"
	"strconv"
	"strings"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
)

type Indexer interface {
	Search(req SearchRequest) (*SearchResponse, error)
	DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error)
	Ping() error
	Name() string
	GetUsage() Usage
}

type IndexerWithResolve interface {
	ResolveDownloadURL(ctx context.Context, directURL, title string, size int64, cat string) (resolvedURL string, err error)
}

type Usage struct {
	APIHitsLimit         int
	APIHitsUsed          int
	APIHitsRemaining     int
	DownloadsLimit       int
	DownloadsUsed        int
	DownloadsRemaining   int
	AllTimeAPIHitsUsed   int
	AllTimeDownloadsUsed int
}

type SearchRequest struct {
	Query                  string
	IMDbID                 string
	TMDBID                 string
	TVDBID                 string
	Cat                    string
	Limit                  int
	Season                 string
	Episode                string
	UseSeasonEpisodeParams bool
	ForceIDSearch          bool
	IndexerMode            string
	FilterQuery            string
	StreamLabel            string `json:"-"`
	RequestLabel           string `json:"-"`

	EffectiveByIndexer map[string]*config.IndexerSearchConfig `json:"-"`
	PerIndexerQuery    map[string][]string                    `json:"-"`

	OptionalOverrides *config.IndexerSearchConfig `json:"-"`
}

type SearchResponse struct {
	XMLName  xml.Name           `xml:"rss"`
	Channel  Channel            `xml:"channel"`
	Releases []*release.Release `xml:"-"`
}

type NewznabResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type Channel struct {
	Response NewznabResponse `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
	Items    []Item          `xml:"item"`
}

type Item struct {
	Title       string      `xml:"title"`
	Link        string      `xml:"link"`
	GUID        string      `xml:"guid"`
	PubDate     string      `xml:"pubDate"`
	Category    string      `xml:"category"`
	Description string      `xml:"description"`
	Comments    string      `xml:"comments"`
	Size        int64       `xml:"size"`
	Enclosure   Enclosure   `xml:"enclosure"`
	Attributes  []Attribute `xml:"attr"`

	SourceIndexer Indexer `xml:"-"`

	ActualIndexer string `xml:"-"`

	ActualGUID string `xml:"-"`

	QuerySource string `xml:"-"`

	Duration float64 `xml:"-"`
}

type Attribute struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type Enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

func (i *Item) GetAttribute(name string) string {
	for _, attr := range i.Attributes {
		if attr.Name == name {
			return attr.Value
		}
	}
	return ""
}

func (i *Item) ToRelease() *release.Release {
	if i == nil {
		return nil
	}
	grabs := 0
	if s := i.GetAttribute("grabs"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			grabs = n
		}
	}

	var languages []string
	if lang := i.GetAttribute("language"); lang != "" {
		for _, part := range strings.Split(lang, ",") {
			if t := strings.TrimSpace(part); t != "" {
				languages = append(languages, t)
			}
		}
	}
	indexerName := i.ActualIndexer
	if indexerName == "" && i.SourceIndexer != nil {
		indexerName = i.SourceIndexer.Name()
	}
	return &release.Release{
		Title:         i.Title,
		Link:          i.Link,
		DetailsURL:    i.ReleaseDetailsURL(),
		Size:          i.Size,
		Indexer:       indexerName,
		SourceIndexer: i.SourceIndexer,
		PubDate:       i.PubDate,
		GUID:          i.GUID,
		QuerySource:   i.QuerySource,
		Grabs:         grabs,
		Languages:     languages,
		Duration:      i.Duration,
	}
}

func (i *Item) ReleaseDetailsURL() string {
	comments := strings.TrimSpace(i.Comments)
	if comments != "" && (strings.HasPrefix(comments, "http://") || strings.HasPrefix(comments, "https://")) {
		if idx := strings.Index(comments, "#"); idx >= 0 {
			comments = comments[:idx]
		}
		if comments != "" {
			return comments
		}
	}
	if i.ActualGUID != "" && strings.Contains(i.ActualGUID, "://") {
		return i.ActualGUID
	}
	if i.GUID != "" && strings.Contains(i.GUID, "://") {
		return i.GUID
	}
	return i.Link
}

func NormalizeItem(item *Item) {
	if item == nil {
		return
	}
	if item.Link == "" && item.Enclosure.URL != "" {
		item.Link = item.Enclosure.URL
	}
	if item.Size <= 0 {
		if item.Enclosure.Length > 0 {
			item.Size = item.Enclosure.Length
		} else if s := item.GetAttribute("size"); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				item.Size = n
			}
		}
	}
}

func NormalizeSearchResponse(resp *SearchResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Channel.Items {
		NormalizeItem(&resp.Channel.Items[i])
	}
	resp.Releases = make([]*release.Release, 0, len(resp.Channel.Items))
	for i := range resp.Channel.Items {
		if rel := resp.Channel.Items[i].ToRelease(); rel != nil {
			resp.Releases = append(resp.Releases, rel)
		}
	}
}
