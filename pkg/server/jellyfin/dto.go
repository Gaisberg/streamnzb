package jellyfin

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// The Jellyfin BaseItemDto, cut down to what clients read. Every field a
// client might treat as required is present; the rest are omitted when
// empty. Pointers mark values Jellyfin sends as null when unknown — a zero
// there would be read as a fact (a 0-tick runtime, a 0 year).

// ticksPerSecond is Jellyfin's time unit: 100ns ticks.
const ticksPerSecond int64 = 10_000_000

// dateCreatedFallback stands in for DateCreated when an item has no
// PremiereDate (a view, or metadata missing one). Clients sort "recently
// added" by this field; a fixed old date keeps the whole catalog from
// looking freshly added.
const dateCreatedFallback = "2020-01-01T00:00:00.0000000Z"

// pathSlug turns a name into the one path segment views use: lowercase,
// non-alphanumerics collapsed to a single "-". Nothing fetches this path —
// Infuse just wants one present.
func pathSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

type baseItem struct {
	Name                     string            `json:"Name"`
	ServerID                 string            `json:"ServerId"`
	ID                       string            `json:"Id"`
	Etag                     string            `json:"Etag,omitempty"`
	DateCreated              string            `json:"DateCreated,omitempty"`
	CanDelete                bool              `json:"CanDelete"`
	CanDownload              bool              `json:"CanDownload"`
	SortName                 string            `json:"SortName,omitempty"`
	PremiereDate             string            `json:"PremiereDate,omitempty"`
	ExternalURLs             []externalURL     `json:"ExternalUrls,omitempty"`
	Path                     string            `json:"Path,omitempty"`
	Overview                 string            `json:"Overview,omitempty"`
	Genres                   []string          `json:"Genres,omitempty"`
	CommunityRating          *float64          `json:"CommunityRating,omitempty"`
	RunTimeTicks             *int64            `json:"RunTimeTicks,omitempty"`
	PlayAccess               string            `json:"PlayAccess,omitempty"`
	ProductionYear           *int              `json:"ProductionYear,omitempty"`
	IndexNumber              *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber        *int              `json:"ParentIndexNumber,omitempty"`
	RemoteTrailers           []remoteTrailer   `json:"RemoteTrailers,omitempty"`
	ProviderIDs              map[string]string `json:"ProviderIds,omitempty"`
	IsFolder                 bool              `json:"IsFolder"`
	ParentID                 string            `json:"ParentId,omitempty"`
	Type                     string            `json:"Type"`
	People                   []person          `json:"People,omitempty"`
	GenreItems               []nameGUID        `json:"GenreItems,omitempty"`
	UserData                 *userData         `json:"UserData,omitempty"`
	RecursiveItemCount       *int              `json:"RecursiveItemCount,omitempty"`
	ChildCount               *int              `json:"ChildCount,omitempty"`
	SeriesName               string            `json:"SeriesName,omitempty"`
	SeriesID                 string            `json:"SeriesId,omitempty"`
	SeasonID                 string            `json:"SeasonId,omitempty"`
	SeasonName               string            `json:"SeasonName,omitempty"`
	SeriesPrimaryImageTag    string            `json:"SeriesPrimaryImageTag,omitempty"`
	Status                   string            `json:"Status,omitempty"`
	CollectionType           string            `json:"CollectionType,omitempty"`
	ImageTags                map[string]string `json:"ImageTags,omitempty"`
	BackdropImageTags        []string          `json:"BackdropImageTags,omitempty"`
	ParentBackdropItemID     string            `json:"ParentBackdropItemId,omitempty"`
	ParentBackdropImageTags  []string          `json:"ParentBackdropImageTags,omitempty"`
	ParentPrimaryImageItemID string            `json:"ParentPrimaryImageItemId,omitempty"`
	ParentPrimaryImageTag    string            `json:"ParentPrimaryImageTag,omitempty"`
	MediaType                string            `json:"MediaType,omitempty"`
	LocationType             string            `json:"LocationType"`
	VideoType                string            `json:"VideoType,omitempty"`
	MediaSources             []mediaSource     `json:"MediaSources,omitempty"`
	AlternateMediaSources    []mediaSource     `json:"AlternateMediaSources,omitempty"`
	// EnableMediaSourceDisplay and MediaSourceCount are the Jellyfin item-level
	// signals clients use to expose a version selector. MediaSources alone is
	// not sufficient for Infuse's multi-version UI.
	EnableMediaSourceDisplay *bool         `json:"EnableMediaSourceDisplay,omitempty"`
	MediaSourceCount         *int          `json:"MediaSourceCount,omitempty"`
	MediaStreams             []mediaStream `json:"MediaStreams,omitempty"`
	Container                string        `json:"Container,omitempty"`
	Taglines                 []string      `json:"Taglines,omitempty"`
	Tags                     []string      `json:"Tags,omitempty"`
}

type externalURL struct {
	Name string `json:"Name"`
	URL  string `json:"Url"`
}

type remoteTrailer struct {
	URL  string `json:"Url"`
	Name string `json:"Name,omitempty"`
}

type person struct {
	Name            string `json:"Name"`
	ID              string `json:"Id"`
	Role            string `json:"Role,omitempty"`
	Type            string `json:"Type"`
	PrimaryImageTag string `json:"PrimaryImageTag,omitempty"`
}

type nameGUID struct {
	Name string `json:"Name"`
	ID   string `json:"Id"`
}

type userData struct {
	PlaybackPositionTicks int64    `json:"PlaybackPositionTicks"`
	PlayCount             int      `json:"PlayCount"`
	IsFavorite            bool     `json:"IsFavorite"`
	Played                bool     `json:"Played"`
	Key                   string   `json:"Key"`
	ItemID                string   `json:"ItemId"`
	LastPlayedDate        string   `json:"LastPlayedDate,omitempty"`
	PlayedPercentage      *float64 `json:"PlayedPercentage,omitempty"`
	UnplayedItemCount     *int     `json:"UnplayedItemCount,omitempty"`
}

// queryResult is the paged envelope of every listing route.
type queryResult struct {
	Items            []*baseItem `json:"Items"`
	TotalRecordCount int         `json:"TotalRecordCount"`
	StartIndex       int         `json:"StartIndex"`
}

func emptyResult() queryResult {
	return queryResult{Items: []*baseItem{}}
}

func intPtr(n int) *int           { return &n }
func int64Ptr(n int64) *int64     { return &n }
func floatPtr(f float64) *float64 { return &f }
func boolPtr(b bool) *bool        { return &b }

// Stremio meta conventions the addon writes and the DTOs read back.
var (
	runtimeMinutes = regexp.MustCompile(`(\d+)\s*min`)
	runtimeHours   = regexp.MustCompile(`(\d+)\s*h`)
	leadingYear    = regexp.MustCompile(`^\d{4}`)
)

// runtimeTicks reads the addon's "120 min" / "1h 45min" runtime.
func runtimeTicks(runtime string) *int64 {
	var minutes int64
	if m := runtimeHours.FindStringSubmatch(runtime); m != nil {
		h, _ := strconv.ParseInt(m[1], 10, 64)
		minutes += h * 60
	}
	if m := runtimeMinutes.FindStringSubmatch(runtime); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		minutes += n
	}
	if minutes <= 0 {
		return nil
	}
	return int64Ptr(minutes * 60 * ticksPerSecond)
}

// productionYear reads the leading year of "2019" or "2019-2023".
func productionYear(releaseInfo string) *int {
	if m := leadingYear.FindString(releaseInfo); m != "" {
		y, _ := strconv.Atoi(m)
		return intPtr(y)
	}
	return nil
}

// premiereDate normalises an ISO date to Jellyfin's timestamp form.
func premiereDate(released string) string {
	released = strings.TrimSpace(released)
	if released == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, released); err == nil {
			return jellyfinTime(t)
		}
	}
	return ""
}

func communityRating(rating string) *float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(rating), 64)
	if err != nil || f <= 0 {
		return nil
	}
	return floatPtr(f)
}

// seriesStatus reads "Continuing" or "Ended" off the addon's "2019-" and
// "2019-2023" release info; a lone year says nothing either way.
func seriesStatus(releaseInfo string) string {
	releaseInfo = strings.TrimSpace(releaseInfo)
	switch {
	case strings.HasSuffix(releaseInfo, "-"):
		return "Continuing"
	case strings.Contains(releaseInfo, "-"):
		return "Ended"
	}
	return ""
}

// jellyfinType is the item type a content type renders as. Anime is a
// series always: Kitsu numbers episodes per entry, and even a Kitsu movie
// arrives as a one-episode series so a title never changes shape between a
// catalog row and its detail page.
func jellyfinType(contentType string) string {
	if contentType == "movie" {
		return "Movie"
	}
	return "Series"
}

func collectionType(contentType string) string {
	if contentType == "movie" {
		return "movies"
	}
	return "tvshows"
}

// providerIDs renders the external ids clients show and match on.
func providerIDs(id itemID) map[string]string {
	n := strconv.FormatUint(id.Number, 10)
	switch id.Scheme {
	case schemeIMDb:
		return map[string]string{"Imdb": id.baseStremioID()}
	case schemeTMDB:
		return map[string]string{"Tmdb": n}
	case schemeTVDB:
		return map[string]string{"Tvdb": n}
	case schemeKitsu:
		return map[string]string{"Kitsu": n}
	}
	return nil
}

// setIdentity fills Path, Etag and DateCreated. Some clients (Infuse 8.5)
// request the three by name on every item and refuse a document that omits
// one, even though nothing here is a filesystem path. Called after images
// are set so the Etag folds in the Primary tag.
func (s *Server) setIdentity(item *baseItem, path string) {
	item.Path = path
	item.DateCreated = item.PremiereDate
	if item.DateCreated == "" {
		item.DateCreated = dateCreatedFallback
	}
	// Same hash style as an image tag (images.go tagFor): first 20 hex of a
	// sha1. Version the item DTO as well as its content: Infuse caches item
	// documents aggressively and otherwise can keep a document that predates
	// a compatibility-field change.
	item.Etag = s.images.tagFor("jellyfin-dto-v2:" + item.ID + item.ImageTags["Primary"] + item.Name)
}

// viewItem is the library folder for a catalog.
func (s *Server) viewItem(def stremio.CatalogDef) *baseItem {
	item := &baseItem{
		Name:           def.Name,
		ServerID:       s.serverID(),
		ID:             viewID(def.ID),
		SortName:       strings.ToLower(def.Name),
		IsFolder:       true,
		Type:           "CollectionFolder",
		CollectionType: collectionType(def.Type),
		LocationType:   "FileSystem",
		PlayAccess:     "Full",
		UserData:       &userData{Key: viewID(def.ID), ItemID: viewID(def.ID)},
	}
	s.setIdentity(item, "/"+pathSlug(def.Name))
	return item
}

// previewItem renders a catalog row. Rows carry no runtime or year, which is
// what a Jellyfin library grid shows anyway: poster and title.
func (s *Server) previewItem(preview stremio.MetaPreview, parentID string) (*baseItem, bool) {
	id, err := itemIDFor(preview.Type, preview.ID)
	if err != nil {
		return nil, false
	}
	item := s.newItem(id, preview.Name)
	item.ParentID = parentID
	item.Overview = preview.Description
	s.setImages(item, id, preview.Poster, preview.Background, "")
	return item, true
}

// newItem is the skeleton of a movie or series, before metadata.
func (s *Server) newItem(id itemID, name string) *baseItem {
	encoded := id.encode()
	item := &baseItem{
		Name:         name,
		ServerID:     s.serverID(),
		ID:           encoded,
		SortName:     strings.ToLower(name),
		Type:         jellyfinType(id.ContentType),
		LocationType: "FileSystem",
		PlayAccess:   "Full",
		ProviderIDs:  providerIDs(id),
		CanDownload:  false,
	}
	if id.Kind == kindMovie {
		item.MediaType = "Video"
		item.VideoType = "VideoFile"
	} else {
		item.IsFolder = true
	}
	return item
}

// metaItem renders a movie or series detail page from the addon's meta.
func (s *Server) metaItem(id itemID, meta *stremio.MetaObject) *baseItem {
	item := s.newItem(id, meta.Name)
	item.Overview = meta.Description
	item.Genres = meta.Genres
	for _, g := range meta.Genres {
		item.GenreItems = append(item.GenreItems, nameGUID{Name: g, ID: userID("genre:" + g)})
	}
	item.CommunityRating = communityRating(meta.IMDBRating)
	item.ProductionYear = productionYear(meta.ReleaseInfo)
	item.PremiereDate = premiereDate(meta.Released)
	item.RunTimeTicks = runtimeTicks(meta.Runtime)
	for _, t := range meta.Trailers {
		if t.Source != "" {
			item.RemoteTrailers = append(item.RemoteTrailers, remoteTrailer{URL: "https://www.youtube.com/watch?v=" + t.Source, Name: "Trailer"})
		}
	}
	item.People = s.people(meta)
	if id.Scheme == schemeIMDb {
		item.ExternalURLs = []externalURL{{Name: "IMDb", URL: "https://www.imdb.com/title/" + id.baseStremioID()}}
	}
	if item.Type == "Series" {
		item.Status = seriesStatus(meta.ReleaseInfo)
		seasons := seasonsOf(meta)
		item.ChildCount = intPtr(len(seasons))
		item.RecursiveItemCount = intPtr(len(videosOf(meta)))
	}
	s.setImages(item, id, meta.Poster, meta.Background, meta.Logo)
	prefix := "series"
	if id.Kind == kindMovie {
		prefix = "movie"
	}
	s.setIdentity(item, "/"+prefix+"/"+id.baseStremioID())
	return item
}

// people renders cast and crew. Plain metadata carries names only; app_extras
// cast members also carry a TMDB or TVDB headshot, which becomes the person's
// PrimaryImageTag so a client's cast row shows faces instead of initials. Ids
// are derived from the name so clients can key on them.
func (s *Server) people(meta *stremio.MetaObject) []person {
	var out []person
	add := func(name, role, kind, photo string) {
		if name = strings.TrimSpace(name); name == "" {
			return
		}
		p := person{Name: name, ID: userID("person:" + name), Role: role, Type: kind}
		if photo = strings.TrimSpace(photo); photo != "" {
			// Registered at a fixed size, not "original": a cast row renders
			// dozens of these at once, and nothing here ever asks for a
			// bigger one.
			p.PrimaryImageTag = s.images.register(rewriteImageSize(photo, "person"))
		}
		out = append(out, p)
	}
	if meta.AppExtras != nil && len(meta.AppExtras.Cast) > 0 {
		for _, c := range meta.AppExtras.Cast {
			add(c.Name, c.Character, "Actor", c.Photo)
		}
	} else {
		for _, name := range meta.Cast {
			add(name, "", "Actor", "")
		}
	}
	for _, name := range meta.Director {
		add(name, "", "Director", "")
	}
	for _, name := range meta.Writer {
		add(name, "", "Writer", "")
	}
	return out
}

// seasonItem renders one season of a series.
func (s *Server) seasonItem(seriesID itemID, meta *stremio.MetaObject, season int, episodes int) *baseItem {
	id := seriesID.season(season)
	name := "Season " + strconv.Itoa(season)
	if season == 0 {
		name = "Specials"
	}
	item := &baseItem{
		Name:           name,
		ServerID:       s.serverID(),
		ID:             id.encode(),
		SortName:       strconv.Itoa(season),
		IsFolder:       true,
		Type:           "Season",
		LocationType:   "FileSystem",
		PlayAccess:     "Full",
		IndexNumber:    intPtr(season),
		ParentID:       seriesID.encode(),
		SeriesID:       seriesID.encode(),
		SeriesName:     meta.Name,
		ChildCount:     intPtr(episodes),
		ProductionYear: productionYear(meta.ReleaseInfo),
	}
	// A season has no art of its own; the series poster stands in, under the
	// season's id so clients that only look at ImageTags still get one.
	s.setImages(item, id, meta.Poster, meta.Background, "")
	item.SeriesPrimaryImageTag = item.ImageTags["Primary"]
	item.ParentBackdropItemID = seriesID.encode()
	item.ParentBackdropImageTags = item.BackdropImageTags
	s.setIdentity(item, "/series/"+seriesID.baseStremioID()+"/"+strconv.Itoa(season))
	return item
}

// episodeItem renders one episode.
func (s *Server) episodeItem(seriesID itemID, meta *stremio.MetaObject, video stremio.MetaVideo) *baseItem {
	id := seriesID.episode(video.Season, video.Episode)
	name := video.Title
	if name == "" {
		name = "Episode " + strconv.Itoa(video.Episode)
	}
	item := &baseItem{
		Name:              name,
		ServerID:          s.serverID(),
		ID:                id.encode(),
		SortName:          strconv.Itoa(video.Episode),
		Type:              "Episode",
		MediaType:         "Video",
		VideoType:         "VideoFile",
		LocationType:      "FileSystem",
		PlayAccess:        "Full",
		Overview:          video.Overview,
		IndexNumber:       intPtr(video.Episode),
		ParentIndexNumber: intPtr(video.Season),
		ParentID:          seriesID.season(video.Season).encode(),
		SeriesID:          seriesID.encode(),
		SeriesName:        meta.Name,
		SeasonID:          seriesID.season(video.Season).encode(),
		SeasonName:        "Season " + strconv.Itoa(video.Season),
		PremiereDate:      premiereDate(video.Released),
		ProviderIDs:       providerIDs(id),
		RunTimeTicks:      runtimeTicks(meta.Runtime),
	}
	if video.Season == 0 {
		item.SeasonName = "Specials"
	}
	// The episode still is its Primary image; the series poster and backdrop
	// ride along as the parent's for the clients that show them.
	// The series' backdrop and logo, because an episode has no landscape art
	// of its own and a client rendering an episode — on a hero card, in a
	// Continue Watching row — asks for exactly that. The still stays Primary,
	// falling back to the series poster when the episode has none, since an
	// unadvertised Primary is an episode with no artwork a client can reach.
	poster := video.Thumbnail
	if poster == "" {
		poster = meta.Poster
	}
	s.setImages(item, id, poster, meta.Background, meta.Logo)
	if tag := s.images.tagFor(meta.Poster); tag != "" {
		item.SeriesPrimaryImageTag = tag
		item.ParentPrimaryImageItemID = seriesID.encode()
		item.ParentPrimaryImageTag = tag
	}
	if tag := s.images.tagFor(meta.Background); tag != "" {
		item.ParentBackdropItemID = seriesID.encode()
		item.ParentBackdropImageTags = []string{tag}
	}
	path := "/series/" + seriesID.baseStremioID() + "/" + strconv.Itoa(video.Season) + "/" + strconv.Itoa(video.Episode)
	s.setIdentity(item, path)
	return item
}

// setImages registers an item's artwork and advertises the tags for it.
//
// Tags are hashes of the upstream URLs, so a poster change is a tag change and
// clients refetch. They are also indexed per item and kind, because clients
// are not consistent about quoting a tag back and the app that fetches an
// image sends no credentials of its own: an image nothing can look up is a
// blank poster, however well the server could have answered it.
func (s *Server) setImages(item *baseItem, id itemID, poster, background, logo string) {
	encoded := id.encode()
	tags := map[string]string{}
	if tag := s.images.registerFor(encoded, "primary", poster); tag != "" {
		tags["Primary"] = tag
	}
	if tag := s.images.registerFor(encoded, "logo", logo); tag != "" {
		tags["Logo"] = tag
	}
	if tag := s.images.registerFor(encoded, "backdrop", background); tag != "" {
		// Thumb resolves to the backdrop too, under its own key: a client asks
		// for it by name and will not fall back to Backdrop on a 404.
		s.images.registerFor(encoded, "thumb", background)
		item.BackdropImageTags = []string{tag}
		// Thumb is the landscape image clients put on a Continue Watching row
		// and a hero card, and they ask for it by name rather than falling
		// back to Backdrop. There is no separate 16:9 asset here and the
		// backdrop is the right shape, so it answers for both.
		tags["Thumb"] = tag
	}
	if len(tags) > 0 {
		item.ImageTags = tags
	}
}

// userDataFor renders the stream's progress on an item. Every playable item
// carries UserData: clients read the Played flag and position off it, and a
// missing object reads as "never asked".
func userDataFor(itemID string, state persistence.JellyfinPlaystate, ok bool) *userData {
	ud := &userData{Key: itemID, ItemID: itemID}
	if !ok {
		return ud
	}
	ud.PlaybackPositionTicks = state.PositionTicks
	ud.PlayCount = state.PlayCount
	ud.Played = state.Played
	if !state.LastPlayedAt.IsZero() {
		ud.LastPlayedDate = jellyfinTime(state.LastPlayedAt)
	}
	if state.RuntimeTicks > 0 && state.PositionTicks > 0 && !state.Played {
		ud.PlayedPercentage = floatPtr(100 * float64(state.PositionTicks) / float64(state.RuntimeTicks))
	}
	return ud
}
