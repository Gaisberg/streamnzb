package stremio

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvmaze"
)

// Meta responses are long-lived on the client: content metadata drifts slowly,
// and the persistent response cache absorbs re-requests anyway.
const (
	metaCacheMaxAge     = 12 * 60 * 60     // 12h
	metaStaleRevalidate = 7 * 24 * 60 * 60 // 7d
	metaRequestTimeout  = 20 * time.Second
)

// tmdbImageBase is the TMDB image CDN; sizes follow the admin search endpoint
// precedent (w92 thumbnails there) scaled up for full meta views.
const (
	tmdbPosterURL   = "https://image.tmdb.org/t/p/w500"
	tmdbBackdropURL = "https://image.tmdb.org/t/p/w1280"
	tmdbStillURL    = "https://image.tmdb.org/t/p/w300"
)

// handleMeta serves /meta/{type}/{id}.json. With the metadata master switch
// off the resource does not exist — 404, matching a manifest that never
// declared it.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	if cfg == nil || !cfg.EffectiveMetadataEnabled() {
		http.NotFound(w, r)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/meta/")
	path = strings.TrimSuffix(path, ".json")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	contentType, id := parts[0], parts[1]

	ctx, cancel := context.WithTimeout(r.Context(), metaRequestTimeout)
	defer cancel()

	meta, err := s.buildMeta(ctx, contentType, id)
	if err != nil {
		logger.Debug("Meta build failed", "type", contentType, "id", id, "err", err)
		http.NotFound(w, r)
		return
	}
	writeJSONCached(w, MetaResponse{
		Meta:            meta,
		CacheMaxAge:     metaCacheMaxAge,
		StaleRevalidate: metaStaleRevalidate,
	}, metaCacheMaxAge, metaStaleRevalidate)
}

func (s *Server) buildMeta(ctx context.Context, contentType, id string) (*MetaObject, error) {
	rid, err := s.resolveMetaID(ctx, contentType, id)
	if err != nil {
		return nil, err
	}
	switch {
	case rid.kitsuID != "":
		return s.buildAnimeMeta(ctx, contentType, rid)
	case contentType == "movie":
		return s.buildMovieMeta(ctx, rid)
	default:
		return s.buildSeriesMeta(ctx, contentType, rid)
	}
}

// resolvedMetaID is the outcome of the lightweight id resolution the meta
// resource needs — deliberately not buildSearchParamsBase, whose translation
// and alternative-title fan-out only pays off for search queries.
type resolvedMetaID struct {
	canonicalID string // the id videos hang off: "tt..." | "tmdb:N" | "kitsu:N"
	tmdbID      int
	imdbID      string
	tvdbID      string
	kitsuID     string
}

func (s *Server) resolveMetaID(ctx context.Context, contentType, rawID string) (*resolvedMetaID, error) {
	id := strings.TrimSpace(rawID)
	rid := &resolvedMetaID{}
	switch {
	case strings.HasPrefix(id, "kitsu:"):
		parts := strings.Split(id, ":")
		if len(parts) < 2 || parts[1] == "" {
			return nil, fmt.Errorf("invalid kitsu id %q", id)
		}
		rid.kitsuID = parts[1]
		rid.canonicalID = "kitsu:" + parts[1]
		return rid, nil

	case strings.HasPrefix(id, "tmdb:"):
		parts := strings.Split(id, ":")
		n, err := strconv.Atoi(parts[1])
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid tmdb id %q", id)
		}
		rid.tmdbID = n
		rid.canonicalID = "tmdb:" + parts[1]
		return rid, nil

	case strings.HasPrefix(id, "tvdb:"):
		parts := strings.Split(id, ":")
		if len(parts) < 2 || parts[1] == "" {
			return nil, fmt.Errorf("invalid tvdb id %q", id)
		}
		// TVDB is the series meta source, so the id is usable as-is; TMDB
		// resolution is best-effort for the movie path and the tt fallback.
		rid.tvdbID = parts[1]
		rid.canonicalID = "tvdb:" + parts[1]
		if find, err := s.tmdbClient.Find(rid.tvdbID, "tvdb_id"); err == nil {
			if res, ok := pickFindResult(find, contentType); ok {
				rid.tmdbID = res.ID
				rid.canonicalID = fmt.Sprintf("tmdb:%d", res.ID)
			}
		}
		return rid, nil

	case strings.HasPrefix(id, "tt"):
		// TMDB resolution is best-effort: series can still be served from
		// TVDB via the imdb id alone.
		imdb := strings.Split(id, ":")[0]
		rid.imdbID = imdb
		rid.canonicalID = imdb
		if find, err := s.tmdbClient.Find(imdb, "imdb_id"); err == nil {
			if res, ok := pickFindResult(find, contentType); ok {
				rid.tmdbID = res.ID
			}
		}
		return rid, nil

	default:
		// Bare numeric ids are TMDB ids, matching the stream handler.
		numeric := strings.Split(id, ":")[0]
		n, err := strconv.Atoi(numeric)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("unrecognized meta id %q", id)
		}
		rid.tmdbID = n
		rid.canonicalID = "tmdb:" + numeric
		return rid, nil
	}
}

// pickFindResult chooses the TMDB find result matching the requested type,
// falling back to the other list — clients sometimes ask for a movie under
// "series" and vice versa.
func pickFindResult(find *tmdb.FindResponse, contentType string) (tmdb.Result, bool) {
	primary, secondary := find.TVResults, find.MovieResults
	if contentType == "movie" {
		primary, secondary = find.MovieResults, find.TVResults
	}
	if len(primary) > 0 {
		return primary[0], true
	}
	if len(secondary) > 0 {
		return secondary[0], true
	}
	return tmdb.Result{}, false
}

func (s *Server) buildMovieMeta(ctx context.Context, rid *resolvedMetaID) (*MetaObject, error) {
	if rid.tmdbID <= 0 {
		return nil, fmt.Errorf("no TMDB id resolved")
	}
	details, err := s.tmdbClient.GetMovieDetails(rid.tmdbID)
	if err != nil {
		return nil, err
	}
	if rid.imdbID == "" && details.IMDbID != "" {
		rid.imdbID = details.IMDbID
		rid.canonicalID = details.IMDbID
	}
	meta := &MetaObject{
		ID:          rid.canonicalID,
		Type:        "movie",
		Name:        details.Title,
		Description: details.Overview,
		Genres:      genreNames(details.Genres),
	}
	if details.PosterPath != "" {
		meta.Poster = tmdbPosterURL + details.PosterPath
	}
	if details.BackdropPath != "" {
		meta.Background = tmdbBackdropURL + details.BackdropPath
	}
	if len(details.ReleaseDate) >= 4 {
		meta.ReleaseInfo = details.ReleaseDate[:4]
	}
	if details.VoteAverage > 0 {
		meta.IMDBRating = fmt.Sprintf("%.1f", details.VoteAverage)
	}
	if details.Runtime > 0 {
		meta.Runtime = fmt.Sprintf("%d min", details.Runtime)
	}
	return meta, nil
}

// currentConfig snapshots the config pointer under the lock; Reload swaps it.
func (s *Server) currentConfig() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// buildSeriesMeta applies the configured source policy: series metadata comes
// from the primary source (TVDB by default), and the other source steps in
// only when the primary cannot serve (no resolvable id, provider down) —
// a fallback episode list beats none. Air dates are TVMaze's in both paths.
func (s *Server) buildSeriesMeta(ctx context.Context, contentType string, rid *resolvedMetaID) (*MetaObject, error) {
	primary, fallback := s.buildSeriesMetaFromTVDB, s.buildSeriesMetaFromTMDB
	if s.currentConfig().EffectiveSeriesMetaSource() == "tmdb" {
		primary, fallback = fallback, primary
	}
	meta, err := primary(ctx, contentType, rid)
	if err == nil {
		return meta, nil
	}
	logger.Debug("Primary series meta source unavailable; falling back",
		"source", s.currentConfig().EffectiveSeriesMetaSource(),
		"tvdb_id", rid.tvdbID, "imdb_id", rid.imdbID, "tmdb_id", rid.tmdbID, "err", err)
	return fallback(ctx, contentType, rid)
}

// resolveTVDBIDForMeta fills rid.tvdbID from whichever id the request carried.
func (s *Server) resolveTVDBIDForMeta(rid *resolvedMetaID) string {
	if rid.tvdbID != "" {
		return rid.tvdbID
	}
	if rid.imdbID != "" && s.tvdbClient != nil {
		if id, err := s.tvdbClient.ResolveTVDBID(rid.imdbID); err == nil && id != "" {
			rid.tvdbID = id
			return id
		}
	}
	if rid.tmdbID > 0 {
		if ext, err := s.tmdbClient.GetExternalIDs(rid.tmdbID, "tv"); err == nil && ext.TVDBID > 0 {
			rid.tvdbID = strconv.Itoa(ext.TVDBID)
			return rid.tvdbID
		}
	}
	return ""
}

func (s *Server) buildSeriesMetaFromTVDB(ctx context.Context, contentType string, rid *resolvedMetaID) (*MetaObject, error) {
	tvdbID := s.resolveTVDBIDForMeta(rid)
	if tvdbID == "" {
		return nil, fmt.Errorf("no TVDB id resolved")
	}
	ext, err := s.tvdbClient.GetSeriesExtended(tvdbID)
	if err != nil {
		return nil, err
	}
	meta := &MetaObject{
		ID:          rid.canonicalID,
		Type:        seriesMetaType(contentType),
		Name:        ext.Name,
		Description: ext.Overview,
		Poster:      ext.Image,
		Background:  ext.Background(),
	}
	for _, g := range ext.Genres {
		if g.Name != "" {
			meta.Genres = append(meta.Genres, g.Name)
		}
	}
	switch {
	case len(ext.Year) >= 4:
		meta.ReleaseInfo = ext.Year[:4]
	case len(ext.FirstAired) >= 4:
		meta.ReleaseInfo = ext.FirstAired[:4]
	}
	if ext.AverageRuntime > 0 {
		meta.Runtime = fmt.Sprintf("%d min", ext.AverageRuntime)
	}

	episodes, err := s.tvdbClient.GetSeriesEpisodes(tvdbID)
	if err != nil {
		logger.Debug("TVDB episodes fetch failed; serving series meta without videos", "tvdb_id", tvdbID, "err", err)
	}
	overlay := s.tvmazeEpisodeOverlay(ctx, rid.imdbID, tvdbID)
	for _, ep := range episodes {
		// Season 0 is specials; out of scope for the videos array.
		if ep.SeasonNumber < 1 || ep.Number < 1 {
			continue
		}
		video := MetaVideo{
			ID:        fmt.Sprintf("%s:%d:%d", rid.canonicalID, ep.SeasonNumber, ep.Number),
			Title:     ep.Name,
			Season:    ep.SeasonNumber,
			Episode:   ep.Number,
			Overview:  ep.Overview,
			Thumbnail: ep.Image,
		}
		if ep.Aired != "" {
			video.Released = ep.Aired + "T00:00:00.000Z"
		}
		applyTVMazeOverlay(&video, overlay)
		meta.Videos = append(meta.Videos, video)
	}
	return meta, nil
}

func (s *Server) buildSeriesMetaFromTMDB(ctx context.Context, contentType string, rid *resolvedMetaID) (*MetaObject, error) {
	if rid.tmdbID <= 0 {
		return nil, fmt.Errorf("no TMDB id resolved")
	}
	details, _, err := s.tmdbClient.GetTVDetailsWithSeasons(rid.tmdbID, nil)
	if err != nil {
		return nil, err
	}
	if details.ExternalIDs != nil {
		if rid.imdbID == "" && details.ExternalIDs.IMDbID != "" {
			rid.imdbID = details.ExternalIDs.IMDbID
			rid.canonicalID = details.ExternalIDs.IMDbID
		}
		if rid.tvdbID == "" && details.ExternalIDs.TVDBID > 0 {
			rid.tvdbID = strconv.Itoa(details.ExternalIDs.TVDBID)
		}
	}

	var seasonNumbers []int
	for _, si := range details.Seasons {
		// Season 0 is specials; out of scope for the videos array.
		if si.SeasonNumber >= 1 {
			seasonNumbers = append(seasonNumbers, si.SeasonNumber)
		}
	}
	_, seasons, err := s.tmdbClient.GetTVDetailsWithSeasons(rid.tmdbID, seasonNumbers)
	if err != nil {
		return nil, err
	}

	meta := &MetaObject{
		ID:          rid.canonicalID,
		Type:        seriesMetaType(contentType),
		Name:        details.Name,
		Description: details.Overview,
		Genres:      genreNames(details.Genres),
	}
	if details.PosterPath != "" {
		meta.Poster = tmdbPosterURL + details.PosterPath
	}
	if details.BackdropPath != "" {
		meta.Background = tmdbBackdropURL + details.BackdropPath
	}
	if len(details.FirstAirDate) >= 4 {
		meta.ReleaseInfo = details.FirstAirDate[:4]
	}
	if details.VoteAverage > 0 {
		meta.IMDBRating = fmt.Sprintf("%.1f", details.VoteAverage)
	}
	if len(details.EpisodeRunTime) > 0 && details.EpisodeRunTime[0] > 0 {
		meta.Runtime = fmt.Sprintf("%d min", details.EpisodeRunTime[0])
	}

	overlay := s.tvmazeEpisodeOverlay(ctx, rid.imdbID, rid.tvdbID)
	for _, n := range seasonNumbers {
		sd := seasons[n]
		if sd == nil {
			continue
		}
		for _, ep := range sd.Episodes {
			video := MetaVideo{
				ID:       fmt.Sprintf("%s:%d:%d", rid.canonicalID, n, ep.EpisodeNumber),
				Title:    ep.Name,
				Season:   n,
				Episode:  ep.EpisodeNumber,
				Overview: ep.Overview,
			}
			if ep.AirDate != "" {
				video.Released = ep.AirDate + "T00:00:00.000Z"
			}
			if ep.StillPath != "" {
				video.Thumbnail = tmdbStillURL + ep.StillPath
			}
			applyTVMazeOverlay(&video, overlay)
			meta.Videos = append(meta.Videos, video)
		}
	}
	return meta, nil
}

// seriesMetaType echoes the client's requested type for series-like content so
// the meta object matches the catalog row it was opened from.
func seriesMetaType(contentType string) string {
	switch contentType {
	case "series", "anime", "tv", "documentary", "other":
		return contentType
	}
	return "series"
}

func (s *Server) buildAnimeMeta(ctx context.Context, contentType string, rid *resolvedMetaID) (*MetaObject, error) {
	animeMeta, err := s.kitsuClient.GetAnimeMeta(ctx, rid.kitsuID)
	if err != nil {
		return nil, err
	}
	name := animeMeta.CanonicalTitle
	if name == "" {
		name = animeMeta.EnglishTitle
	}
	meta := &MetaObject{
		ID:          rid.canonicalID,
		Type:        seriesMetaType(contentType),
		Name:        name,
		Poster:      animeMeta.PosterImage,
		Background:  animeMeta.CoverImage,
		Description: animeMeta.Synopsis,
	}
	if len(animeMeta.StartDate) >= 4 {
		meta.ReleaseInfo = animeMeta.StartDate[:4]
	}
	// Kitsu rates on a 0-100 scale; Stremio expects 0-10.
	if rating, err := strconv.ParseFloat(animeMeta.AverageRating, 64); err == nil && rating > 0 {
		meta.IMDBRating = fmt.Sprintf("%.1f", rating/10)
	}

	// Movies get no episode list; everything else does. Kitsu numbering is
	// entry-relative, which is exactly what kitsu:<id>:<ep> stream ids carry.
	if contentType != "movie" && !strings.EqualFold(animeMeta.ShowType, "movie") {
		episodes, err := s.kitsuClient.GetAnimeEpisodes(ctx, rid.kitsuID)
		if err != nil {
			logger.Debug("Kitsu episodes fetch failed; serving meta without videos",
				"kitsu_id", rid.kitsuID, "err", err)
		}
		for _, ep := range episodes {
			if ep.Number <= 0 {
				continue
			}
			title := ep.CanonicalTitle
			if title == "" {
				title = fmt.Sprintf("Episode %d", ep.Number)
			}
			video := MetaVideo{
				ID:        fmt.Sprintf("kitsu:%s:%d", rid.kitsuID, ep.Number),
				Title:     title,
				Season:    1,
				Episode:   ep.Number,
				Overview:  ep.Synopsis,
				Thumbnail: ep.Thumbnail,
			}
			if ep.Airdate != "" {
				video.Released = ep.Airdate + "T00:00:00.000Z"
			}
			meta.Videos = append(meta.Videos, video)
		}
	}
	return meta, nil
}

// applyTVMazeOverlay makes TVMaze the air-date authority: its airstamp carries
// the actual air time and timezone and always wins over a source's date-only
// air date; thumbnail and summary only fill gaps the source left.
func applyTVMazeOverlay(video *MetaVideo, overlay map[[2]int]tvmaze.Episode) {
	tvm, ok := overlay[[2]int{video.Season, video.Episode}]
	if !ok {
		return
	}
	if tvm.Airstamp != "" {
		video.Released = tvm.Airstamp
	}
	if video.Thumbnail == "" && tvm.Image.Medium != "" {
		video.Thumbnail = tvm.Image.Medium
	}
	if video.Overview == "" && tvm.Summary != "" {
		video.Overview = stripHTMLTags(tvm.Summary)
	}
}

// tvmazeEpisodeOverlay resolves a show on TVMaze and indexes its episodes by
// (season, episode). Best-effort: any failure returns nil and the source's
// air dates stand. Returns nil when TVMaze air dates are disabled in config.
func (s *Server) tvmazeEpisodeOverlay(ctx context.Context, imdbID, tvdbID string) map[[2]int]tvmaze.Episode {
	if s.tvmazeClient == nil || !s.currentConfig().EffectiveTVMazeAirDates() {
		return nil
	}
	var show *tvmaze.Show
	var err error
	if imdbID != "" {
		show, err = s.tvmazeClient.LookupByIMDB(ctx, imdbID)
	}
	if show == nil && tvdbID != "" {
		show, err = s.tvmazeClient.LookupByTVDB(ctx, tvdbID)
	}
	if show == nil || show.ID <= 0 {
		if err != nil {
			logger.Debug("TVMaze lookup failed; using TMDB air dates", "imdb_id", imdbID, "tvdb_id", tvdbID, "err", err)
		}
		return nil
	}
	full, err := s.tvmazeClient.GetShowWithEpisodes(ctx, show.ID)
	if err != nil {
		logger.Debug("TVMaze episodes fetch failed; using TMDB air dates", "tvmaze_id", show.ID, "err", err)
		return nil
	}
	overlay := make(map[[2]int]tvmaze.Episode, len(full.Embedded.Episodes))
	for _, ep := range full.Embedded.Episodes {
		if ep.Season > 0 && ep.Number > 0 {
			overlay[[2]int{ep.Season, ep.Number}] = ep
		}
	}
	return overlay
}

func genreNames(genres []tmdb.Genre) []string {
	var names []string
	for _, g := range genres {
		if g.Name != "" {
			names = append(names, g.Name)
		}
	}
	return names
}

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// stripHTMLTags flattens TVMaze's HTML summaries to plain text.
func stripHTMLTags(s string) string {
	return strings.TrimSpace(htmlTagPattern.ReplaceAllString(s, ""))
}
