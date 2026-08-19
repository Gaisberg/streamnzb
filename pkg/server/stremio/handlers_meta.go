package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/certification"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
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
	tmdbPosterURL = "https://image.tmdb.org/t/p/w500"
	// Backdrops render full-bleed as hero/details backgrounds, upscaled past
	// 1280px on TVs and wide windows — original is the only TMDB size above
	// w1280, and anything less arrives visibly soft.
	tmdbBackdropURL = "https://image.tmdb.org/t/p/original"
	tmdbStillURL    = "https://image.tmdb.org/t/p/w300"
	tmdbProfileURL  = "https://image.tmdb.org/t/p/w276_and_h350_face"
	tmdbLogoURL     = "https://image.tmdb.org/t/p/w500"
)

// handleMeta serves /meta/{type}/{id}.json. For a stream with no metadata
// profile bound the resource does not exist — 404, matching a manifest that
// never declared it.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	stream, _ := auth.StreamFromContext(r)
	profile := s.metadataProfileFor(stream)
	if profile == nil {
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

	meta, err := s.buildMeta(ctx, profile, contentType, id)
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

func (s *Server) buildMeta(ctx context.Context, profile *config.MetadataProfileConfig, contentType, id string) (*MetaObject, error) {
	rid, err := s.resolveMetaID(ctx, contentType, id)
	if err != nil {
		return nil, err
	}
	switch {
	case rid.kitsuID != "":
		return s.buildAnimeMeta(ctx, profile, contentType, rid)
	case contentType == "movie":
		return s.buildMovieMeta(ctx, profile, rid)
	default:
		return s.buildSeriesMeta(ctx, profile, contentType, rid)
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

// metahubLogoURL serves title logos keyed by imdb id — the same CDN Cinemeta
// points clients at, so coverage matches what users are used to.
const metahubLogoURL = "https://images.metahub.space/logo/medium/%s/img"

// metaLogoLang is the ISO 639-1 code TMDB logo picks prefer, from the
// profile's display language ("" for the English default).
func metaLogoLang(profile *config.MetadataProfileConfig) string {
	base, _, _ := strings.Cut(profile.EffectiveLanguage(), "-")
	return base
}

func (s *Server) buildMovieMeta(ctx context.Context, profile *config.MetadataProfileConfig, rid *resolvedMetaID) (*MetaObject, error) {
	if rid.tmdbID <= 0 {
		return nil, fmt.Errorf("no TMDB id resolved")
	}
	details, err := s.tmdbClient.GetMovieDetailsFull(rid.tmdbID, profile.EffectiveLanguage())
	if err != nil {
		return nil, err
	}
	certAge, certKnown := certification.Resolve(tmdbMovieCertEntries(details.ReleaseDates))
	if err := certGateMeta(profile, certAge, certKnown); err != nil {
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
	if logo := details.Images.BestLogo(metaLogoLang(profile)); logo != "" {
		meta.Logo = tmdbLogoURL + logo
	} else if rid.imdbID != "" {
		meta.Logo = fmt.Sprintf(metahubLogoURL, rid.imdbID)
	}
	if len(details.ReleaseDate) >= 4 {
		meta.ReleaseInfo = details.ReleaseDate[:4]
		meta.Released = details.ReleaseDate + "T00:00:00.000Z"
	}
	if details.VoteAverage > 0 {
		meta.IMDBRating = fmt.Sprintf("%.1f", details.VoteAverage)
	}
	if details.Runtime > 0 {
		meta.Runtime = fmt.Sprintf("%d min", details.Runtime)
	}
	applyTMDBCredits(meta, details.Credits)
	meta.Trailers = tmdbTrailers(details.Videos)
	return meta, nil
}

const metaCastLimit = 10

// applyTMDBCredits fills cast/director/writer from an appended credits payload.
func applyTMDBCredits(meta *MetaObject, credits *tmdb.Credits) {
	if credits == nil {
		return
	}
	for _, member := range credits.Cast {
		if member.Name == "" {
			continue
		}
		meta.Cast = append(meta.Cast, member.Name)
		photo := ""
		if member.ProfilePath != "" {
			photo = tmdbProfileURL + member.ProfilePath
		}
		appendCastMember(meta, MetaCastMember{Name: member.Name, Character: member.Character, Photo: photo})
		if len(meta.Cast) >= metaCastLimit {
			break
		}
	}
	seen := make(map[string]bool)
	for _, member := range credits.Crew {
		if member.Name == "" || seen[member.Job+member.Name] {
			continue
		}
		seen[member.Job+member.Name] = true
		switch member.Job {
		case "Director":
			meta.Director = append(meta.Director, member.Name)
		case "Writer", "Screenplay", "Story":
			meta.Writer = append(meta.Writer, member.Name)
		}
	}
}

// appendCastMember adds one app_extras cast entry, allocating on first use.
func appendCastMember(meta *MetaObject, member MetaCastMember) {
	if meta.AppExtras == nil {
		meta.AppExtras = &MetaAppExtras{}
	}
	meta.AppExtras.Cast = append(meta.AppExtras.Cast, member)
}

// tmdbTrailers extracts YouTube trailers from an appended videos payload,
// official uploads first.
func tmdbTrailers(videos *tmdb.Videos) []MetaTrailer {
	if videos == nil {
		return nil
	}
	var official, rest []MetaTrailer
	for _, video := range videos.Results {
		if video.Site != "YouTube" || video.Type != "Trailer" || video.Key == "" {
			continue
		}
		trailer := MetaTrailer{Source: video.Key, Type: "Trailer"}
		if video.Official {
			official = append(official, trailer)
		} else {
			rest = append(rest, trailer)
		}
	}
	trailers := append(official, rest...)
	if len(trailers) > 3 {
		trailers = trailers[:3]
	}
	return trailers
}

// currentConfig snapshots the config pointer under the lock; Reload swaps it.
func (s *Server) currentConfig() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// buildSeriesMeta applies the profile's source policy: series metadata comes
// from the primary source (TVDB by default), and the other source steps in
// only when the primary cannot serve (no resolvable id, provider down) —
// a fallback episode list beats none. Air dates are TVMaze's in both paths.
func (s *Server) buildSeriesMeta(ctx context.Context, profile *config.MetadataProfileConfig, contentType string, rid *resolvedMetaID) (*MetaObject, error) {
	primary, fallback := s.buildSeriesMetaFromTVDB, s.buildSeriesMetaFromTMDB
	if profile.EffectiveSeriesMetaSource() == "tmdb" {
		primary, fallback = fallback, primary
	}
	meta, err := primary(ctx, profile, contentType, rid)
	if err == nil {
		return meta, nil
	}
	logger.Debug("Primary series meta source unavailable; falling back",
		"source", profile.EffectiveSeriesMetaSource(),
		"tvdb_id", rid.tvdbID, "imdb_id", rid.imdbID, "tmdb_id", rid.tmdbID, "err", err)
	return fallback(ctx, profile, contentType, rid)
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

func (s *Server) buildSeriesMetaFromTVDB(ctx context.Context, profile *config.MetadataProfileConfig, contentType string, rid *resolvedMetaID) (*MetaObject, error) {
	tvdbID := s.resolveTVDBIDForMeta(rid)
	if tvdbID == "" {
		return nil, fmt.Errorf("no TVDB id resolved")
	}
	lang3 := tvdb.LanguageToISO3(profile.EffectiveLanguage())
	ext, err := s.tvdbClient.GetSeriesExtendedTranslated(tvdbID, lang3)
	if err != nil {
		return nil, err
	}
	certAge, certKnown := certification.Resolve(tvdbCertEntries(ext.ContentRatings))
	if err := certGateMeta(profile, certAge, certKnown); err != nil {
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
	for _, member := range ext.CastMembers(metaCastLimit) {
		meta.Cast = append(meta.Cast, member.Name)
		appendCastMember(meta, MetaCastMember{Name: member.Name, Character: member.Character, Photo: member.Photo})
	}
	for _, g := range ext.Genres {
		if g.Name != "" {
			meta.Genres = append(meta.Genres, g.Name)
		}
	}
	firstYear := ext.Year
	if len(firstYear) < 4 && len(ext.FirstAired) >= 4 {
		firstYear = ext.FirstAired[:4]
	}
	lastYear := ""
	if len(ext.LastAired) >= 4 {
		lastYear = ext.LastAired[:4]
	}
	meta.ReleaseInfo = seriesReleaseInfo(firstYear, lastYear, ext.Status.Name)
	if len(ext.FirstAired) >= 10 {
		meta.Released = ext.FirstAired + "T00:00:00.000Z"
	}
	if ext.AverageRuntime > 0 {
		meta.Runtime = fmt.Sprintf("%d min", ext.AverageRuntime)
	}
	// The canonical id stays as requested; the fallback logo CDN needs imdb.
	if rid.imdbID == "" {
		rid.imdbID = ext.IMDbID()
	}
	if logo := ext.ClearLogo(); logo != "" {
		meta.Logo = logo
	} else if rid.imdbID != "" {
		meta.Logo = fmt.Sprintf(metahubLogoURL, rid.imdbID)
	}
	for _, trailer := range ext.Trailers {
		if ytID := youtubeIDFromURL(trailer.URL); ytID != "" {
			meta.Trailers = append(meta.Trailers, MetaTrailer{Source: ytID, Type: "Trailer"})
			if len(meta.Trailers) >= 3 {
				break
			}
		}
	}

	episodes, err := s.tvdbClient.GetSeriesEpisodesTranslated(tvdbID, lang3)
	if err != nil {
		logger.Debug("TVDB episodes fetch failed; serving series meta without videos", "tvdb_id", tvdbID, "err", err)
	}
	overlay := s.tvmazeEpisodeOverlay(ctx, profile, rid.imdbID, tvdbID)
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

func (s *Server) buildSeriesMetaFromTMDB(ctx context.Context, profile *config.MetadataProfileConfig, contentType string, rid *resolvedMetaID) (*MetaObject, error) {
	if rid.tmdbID <= 0 {
		return nil, fmt.Errorf("no TMDB id resolved")
	}
	details, _, err := s.tmdbClient.GetTVDetailsWithSeasons(rid.tmdbID, nil, profile.EffectiveLanguage())
	if err != nil {
		return nil, err
	}
	certAge, certKnown := certification.Resolve(tmdbTVCertEntries(details.ContentRatings))
	if err := certGateMeta(profile, certAge, certKnown); err != nil {
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
	_, seasons, err := s.tmdbClient.GetTVDetailsWithSeasons(rid.tmdbID, seasonNumbers, profile.EffectiveLanguage())
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
	if logo := details.Images.BestLogo(metaLogoLang(profile)); logo != "" {
		meta.Logo = tmdbLogoURL + logo
	} else if rid.imdbID != "" {
		meta.Logo = fmt.Sprintf(metahubLogoURL, rid.imdbID)
	}
	firstYear, lastYear := "", ""
	if len(details.FirstAirDate) >= 4 {
		firstYear = details.FirstAirDate[:4]
		meta.Released = details.FirstAirDate + "T00:00:00.000Z"
	}
	if len(details.LastAirDate) >= 4 {
		lastYear = details.LastAirDate[:4]
	}
	meta.ReleaseInfo = seriesReleaseInfo(firstYear, lastYear, details.Status)
	if details.VoteAverage > 0 {
		meta.IMDBRating = fmt.Sprintf("%.1f", details.VoteAverage)
	}
	if len(details.EpisodeRunTime) > 0 && details.EpisodeRunTime[0] > 0 {
		meta.Runtime = fmt.Sprintf("%d min", details.EpisodeRunTime[0])
	}
	applyTMDBCredits(meta, details.Credits)
	meta.Trailers = tmdbTrailers(details.Videos)

	overlay := s.tvmazeEpisodeOverlay(ctx, profile, rid.imdbID, rid.tvdbID)
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

// seriesReleaseInfo renders the year range clients show under a series title:
// "2011-2019" for a finished run, "2011-" for a continuing one, the bare year
// when the end is unknown or the same year.
func seriesReleaseInfo(firstYear, lastYear, status string) string {
	if firstYear == "" {
		return ""
	}
	switch strings.ToLower(status) {
	case "ended", "canceled", "cancelled":
		if lastYear != "" && lastYear != firstYear {
			return firstYear + "-" + lastYear
		}
		return firstYear
	case "continuing", "returning series", "in production":
		return firstYear + "-"
	}
	return firstYear
}

// youtubeIDFromURL extracts a YouTube video id from watch?v= and youtu.be
// forms; anything else yields "".
func youtubeIDFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.TrimPrefix(u.Hostname(), "www.") {
	case "youtube.com", "m.youtube.com":
		return u.Query().Get("v")
	case "youtu.be":
		return strings.Trim(u.Path, "/")
	}
	return ""
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

// applyAnimeArtwork upgrades a Kitsu-built meta with TVDB/TMDB artwork via the
// anime-lists mapping. Kitsu cover images are low-resolution banner crops (and
// often missing), while the mapped series carries proper 1920x1080 fanart and
// a title logo; Kitsu's poster is kept — it is the better of the two. With a
// display language configured, a real translated overview from the mapped
// record also replaces Kitsu's synopsis, which only exists in English; the
// Kitsu title stays — anime audiences expect the romaji/canonical name.
// Purely best-effort: no mapping or a failed fetch leaves the Kitsu meta as-is.
func (s *Server) applyAnimeArtwork(meta *MetaObject, profile *config.MetadataProfileConfig, kitsuID string) {
	if s.animeLists == nil {
		return
	}
	mapping, ok := s.animeLists.LookupKitsu(kitsuID)
	if !ok {
		return
	}
	switch {
	case mapping.TVDBID != "":
		ext, err := s.tvdbClient.GetSeriesExtended(mapping.TVDBID)
		if err != nil {
			logger.Debug("Anime TVDB artwork fetch failed", "kitsu_id", kitsuID, "tvdb_id", mapping.TVDBID, "err", err)
			return
		}
		if bg := ext.Background(); bg != "" {
			meta.Background = bg
		}
		if logo := ext.ClearLogo(); logo != "" {
			meta.Logo = logo
		}
		if meta.Poster == "" {
			meta.Poster = ext.Image
		}
		if _, overview := s.tvdbClient.SeriesTranslation(mapping.TVDBID, tvdb.LanguageToISO3(profile.EffectiveLanguage())); overview != "" {
			meta.Description = overview
		}
	case mapping.TMDBID != "" && strings.EqualFold(mapping.Type, "movie"):
		tmdbID, err := strconv.Atoi(mapping.TMDBID)
		if err != nil || tmdbID <= 0 {
			return
		}
		details, err := s.tmdbClient.GetMovieDetailsFull(tmdbID, profile.EffectiveLanguage())
		if err != nil {
			logger.Debug("Anime TMDB artwork fetch failed", "kitsu_id", kitsuID, "tmdb_id", mapping.TMDBID, "err", err)
			return
		}
		if details.BackdropPath != "" {
			meta.Background = tmdbBackdropURL + details.BackdropPath
		}
		if logo := details.Images.BestLogo(metaLogoLang(profile)); logo != "" {
			meta.Logo = tmdbLogoURL + logo
		}
		if meta.Poster == "" && details.PosterPath != "" {
			meta.Poster = tmdbPosterURL + details.PosterPath
		}
		// With a language set, TMDB's overview arrives translated (empty when
		// it has no translation, so this keeps Kitsu's text in that case).
		if profile.EffectiveLanguage() != "" && details.Overview != "" {
			meta.Description = details.Overview
		}
	}
}

func (s *Server) buildAnimeMeta(ctx context.Context, profile *config.MetadataProfileConfig, contentType string, rid *resolvedMetaID) (*MetaObject, error) {
	animeMeta, err := s.kitsuClient.GetAnimeMeta(ctx, rid.kitsuID)
	if err != nil {
		return nil, err
	}
	certAge, certKnown := certification.NormalizeKitsu(animeMeta.AgeRating, animeMeta.Nsfw)
	if err := certGateMeta(profile, certAge, certKnown); err != nil {
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
	s.applyAnimeArtwork(meta, profile, rid.kitsuID)

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
// air dates stand. Returns nil when the profile disables TVMaze air dates —
// a nil profile keeps the default-on behavior, which is what the search-path
// unaired gate wants for streams with no metadata profile bound.
// tvmazeEpisodeOverlay is the display-side overlay: it fills the air dates a
// meta response shows, so the profile's TVMaze toggle governs it.
func (s *Server) tvmazeEpisodeOverlay(ctx context.Context, profile *config.MetadataProfileConfig, imdbID, tvdbID string) map[[2]int]tvmaze.Episode {
	if !profile.EffectiveTVMazeAirDates() {
		return nil
	}
	return s.tvmazeEpisodes(ctx, imdbID, tvdbID)
}

// tvmazeEpisodes fetches a show's episodes with no profile in the way. The
// air-date gate uses this rather than the overlay above: the gate decides
// whether indexers are asked at all, which is not a metadata profile's call —
// a stream with no profile bound is gated the same as one with a profile that
// happens to display TVDB's air dates.
func (s *Server) tvmazeEpisodes(ctx context.Context, imdbID, tvdbID string) map[[2]int]tvmaze.Episode {
	if s.tvmazeClient == nil {
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
