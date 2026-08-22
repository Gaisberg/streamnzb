package stremio

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/certification"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
)

// errCertificationBlocked marks content a profile's certification cap turned
// away; handleMeta 404s on it like any other build failure — the resource
// does not exist for that stream.
var errCertificationBlocked = errors.New("blocked by certification cap")

// capForProfile resolves the profile's parental cap. ok is false when the
// profile is nil or uncapped — the common case, which must cost nothing.
// Unknown certifications fail closed unless the profile allows unrated: the
// deliberate opposite of the fail-open doctrine release limits follow,
// because this is a parental control.
func capForProfile(p *config.MetadataProfileConfig) (certification.Cap, bool) {
	if p == nil || strings.TrimSpace(p.MaxCertification) == "" {
		return certification.Cap{}, false
	}
	return certification.CapForID(p.MaxCertification, p.EffectiveAllowUnrated())
}

// tmdbMovieCertEntries flattens an appended/standalone release-dates payload.
func tmdbMovieCertEntries(rd *tmdb.ReleaseDatesResponse) []certification.Entry {
	var entries []certification.Entry
	for _, pair := range rd.Certifications() {
		entries = append(entries, certification.Entry{Country: pair[0], Label: pair[1]})
	}
	return entries
}

// tmdbTVCertEntries flattens an appended/standalone content-ratings payload.
func tmdbTVCertEntries(cr *tmdb.ContentRatingsResponse) []certification.Entry {
	var entries []certification.Entry
	for _, pair := range cr.Certifications() {
		entries = append(entries, certification.Entry{Country: pair[0], Label: pair[1]})
	}
	return entries
}

// tvdbCertEntries converts TVDB contentRatings (3-letter country names).
func tvdbCertEntries(ratings []tvdb.ContentRating) []certification.Entry {
	var entries []certification.Entry
	for _, r := range ratings {
		entries = append(entries, certification.Entry{Country: r.Country, Label: r.Name})
	}
	return entries
}

// tmdbCertAge resolves one TMDB title's certification through the small
// cached lookup endpoints. mediaType is "movie" or "tv".
func (s *Server) tmdbCertAge(mediaType string, tmdbID int) (age int, known bool) {
	rt := s.runtime()
	if rt.tmdbClient == nil || tmdbID <= 0 {
		return 0, false
	}
	if mediaType == "movie" {
		rd, err := rt.tmdbClient.GetMovieReleaseDates(tmdbID)
		if err != nil {
			return 0, false
		}
		return certification.Resolve(tmdbMovieCertEntries(rd))
	}
	cr, err := rt.tmdbClient.GetTVContentRatings(tmdbID)
	if err != nil {
		return 0, false
	}
	return certification.Resolve(tmdbTVCertEntries(cr))
}

// previewCertAge resolves a catalog preview's certification from its id, via
// whichever cached client the id scheme addresses. Used only by the local
// catalogs — the provider catalogs filter from data they already fetched.
func (s *Server) previewCertAge(ctx context.Context, preview MetaPreview, contentType string) (age int, known bool) {
	rt := s.runtime()
	if kitsuID, ok := strings.CutPrefix(preview.ID, "kitsu:"); ok {
		if s.kitsuClient == nil {
			return 0, false
		}
		animeMeta, err := s.kitsuClient.GetAnimeMeta(ctx, kitsuID)
		if err != nil {
			return 0, false
		}
		return certification.NormalizeKitsu(animeMeta.AgeRating, animeMeta.Nsfw)
	}
	if tvdbID, ok := strings.CutPrefix(preview.ID, "tvdb:"); ok {
		if rt.tvdbClient == nil {
			return 0, false
		}
		ext, err := rt.tvdbClient.GetSeriesExtended(tvdbID)
		if err != nil {
			return 0, false
		}
		return certification.Resolve(tvdbCertEntries(ext.ContentRatings))
	}
	mediaType := "tv"
	if contentType == "movie" {
		mediaType = "movie"
	}
	tmdbID := 0
	if raw, ok := strings.CutPrefix(preview.ID, "tmdb:"); ok {
		tmdbID, _ = strconv.Atoi(raw)
	} else if strings.HasPrefix(preview.ID, "tt") && rt.tmdbClient != nil {
		if find, err := rt.tmdbClient.Find(preview.ID, "imdb_id"); err == nil {
			if res, ok := pickFindResult(find, contentType); ok {
				tmdbID = res.ID
			}
		}
	}
	return s.tmdbCertAge(mediaType, tmdbID)
}

// filterPreviewsByCertification drops previews the cap disallows, resolving
// each row's certification concurrently (bounded like the external-id
// fan-out; every lookup lands in the response cache). Used by the local
// catalogs, whose rows carry mixed id schemes.
func (s *Server) filterPreviewsByCertification(ctx context.Context, cap certification.Cap, previews []MetaPreview, contentType string) []MetaPreview {
	if len(previews) == 0 {
		return previews
	}
	allowed := make([]bool, len(previews))
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i := range previews {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			allowed[i] = cap.Allows(s.previewCertAge(ctx, previews[i], contentType))
		}(i)
	}
	wg.Wait()
	filtered := previews[:0]
	for i, preview := range previews {
		if allowed[i] {
			filtered = append(filtered, preview)
		}
	}
	return filtered
}

// catalogCertCeilingAge resolves the effective certification ceiling for one
// catalog under one profile: the tighter of the catalog's built-in ceiling
// (what keeps "Family Movies" family-safe on an uncapped profile) and the
// profile's cap. -1 means no ceiling.
func catalogCertCeilingAge(def CatalogDef, profile *config.MetadataProfileConfig) int {
	ceiling := -1
	if def.CertCeiling != "" {
		if c, ok := certification.CapForID(def.CertCeiling, false); ok {
			ceiling = c.MaxAge
		}
	}
	if cap, capped := capForProfile(profile); capped && (ceiling < 0 || cap.MaxAge < ceiling) {
		ceiling = cap.MaxAge
	}
	return ceiling
}

// filterTMDBResults drops listing rows the cap disallows, resolving each
// row's certification through the small cached lookups (bounded fan-out).
// Runs before the external-id resolution so blocked rows never pay for it.
func (s *Server) filterTMDBResults(mediaType string, results []tmdb.SearchMultiResult, cap certification.Cap) []tmdb.SearchMultiResult {
	if len(results) == 0 {
		return results
	}
	allowed := make([]bool, len(results))
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i, res := range results {
		wg.Add(1)
		sem <- struct{}{}
		go func(i, tmdbID int) {
			defer wg.Done()
			defer func() { <-sem }()
			allowed[i] = cap.Allows(s.tmdbCertAge(mediaType, tmdbID))
		}(i, res.ID)
	}
	wg.Wait()
	filtered := results[:0]
	for i, res := range results {
		if allowed[i] {
			filtered = append(filtered, res)
		}
	}
	return filtered
}

// certGateMeta applies the cap to a built meta page: blocked content errors
// out of buildMeta and 404s, matching a catalog that never listed it.
func certGateMeta(profile *config.MetadataProfileConfig, age int, known bool) error {
	cap, capped := capForProfile(profile)
	if !capped || cap.Allows(age, known) {
		return nil
	}
	return fmt.Errorf("%w: age %d (known=%v) over cap %d", errCertificationBlocked, age, known, cap.MaxAge)
}

// resolveSearchCertification resolves the requested content's certification
// for the playback gate, preferring metadata already resolved into params
// (Kitsu carries its rating inline) before the cached TMDB/TVDB lookups.
func (s *Server) resolveSearchCertification(contentType string, params *query.SearchParams) (age int, known bool) {
	rt := s.runtime()
	if params == nil {
		return 0, false
	}
	if md := params.Metadata; md != nil && md.KitsuDetails != nil {
		return certification.NormalizeKitsu(md.KitsuDetails.AgeRating, md.KitsuDetails.Nsfw)
	}
	mediaType := "tv"
	if query.MovieLike(params.Metadata, contentType) {
		mediaType = "movie"
	}
	if tmdbID, err := strconv.Atoi(strings.TrimSpace(params.Req.TMDBID)); err == nil && tmdbID > 0 {
		return s.tmdbCertAge(mediaType, tmdbID)
	}
	if mediaType == "tv" && strings.TrimSpace(params.Req.TVDBID) != "" && rt.tvdbClient != nil {
		if ext, err := rt.tvdbClient.GetSeriesExtended(strings.TrimSpace(params.Req.TVDBID)); err == nil {
			return certification.Resolve(tvdbCertEntries(ext.ContentRatings))
		}
	}
	return 0, false
}
