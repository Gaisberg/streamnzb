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
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
	DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error)
	Ping(ctx context.Context) error
	Name() string
	GetUsage() Usage
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
	SearchesCount        int
	AvgResponseMS        float64
}

type SearchRequest struct {
	Query   string
	IMDbID  string
	TMDBID  string
	TVDBID  string
	KitsuID string
	Cat     string
	Limit   int
	Season  string
	Episode string
	// AbsoluteEpisode is the anime absolute episode number for the requested
	// season/episode (e.g. S02E02 of One Piece = 63). When set, validation
	// also accepts absolute-numbered releases ("Show - 63", "Show S01E63").
	AbsoluteEpisode string
	// ContentIsAnime marks the request as anime content (Kitsu-addressed, or
	// TMDB metadata: animation not originally in English). Per-indexer
	// content_scope decides participation from it.
	ContentIsAnime          bool
	SeriesSearchScope       string
	SearchMode              string
	DisableResultFiltering  bool
	EnableYearValidation    bool
	IndexerMode             string
	ValidationQuery         string
	ValidationQueries       []string
	ValidationQueryProfiles []ValidationQueryProfile
	StreamLabel             string `json:"-"`
	RequestLabel            string `json:"-"`

	EffectiveByIndexer map[string]*config.IndexerSearchConfig `json:"-"`

	OptionalOverrides *config.IndexerSearchConfig `json:"-"`
}

type ValidationQueryProfile struct {
	Languages []string
	Query     string
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
	if i.ActualGUID != "" && strings.Contains(i.ActualGUID, "://") {
		return i.ActualGUID
	}
	if i.Comments != "" && strings.Contains(i.Comments, "://") {
		return i.Comments
	}
	if i.GUID != "" && strings.Contains(i.GUID, "://") {
		return i.GUID
	}
	return ""
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
