package stremio

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/query"
)

// This file is the addon's surface for other HTTP layers in the process — the
// Jellyfin layer today. Every method resolves the stream's metadata profile
// itself: a nil profile is also what the certification cap and the kill
// switch produce, so a caller-supplied profile would be a way around both.

// ErrMetadataDisabled is returned when the stream has no metadata profile
// bound, or metadata is switched off globally: the catalogs and metas that
// hang off it do not exist for that stream.
var ErrMetadataDisabled = errors.New("stremio: metadata disabled for stream")

// PlayServeOptions is how a play request wants to be answered when the slot it
// asked for cannot be served as-is. The zero value is the Stremio behaviour:
// absolute redirects under the addon base URL and the error video once the
// candidates run out. A caller on another origin sets both, since its client
// neither knows the addon base URL nor wants a placeholder video behind a 200.
type PlayServeOptions struct {
	// SlotLocation renders the Location header for a redirect to another
	// slot. nil means the token-scoped /play/ URL under the addon base URL.
	SlotLocation func(slotPath string) string
	// FailWithStatus answers exhausted candidates with a 502 and the cause
	// instead of a redirect to the error video.
	FailWithStatus bool
}

type playServeOptionsKeyType struct{}

var playServeOptionsKey = playServeOptionsKeyType{}

func playServeOptionsFromContext(ctx context.Context) PlayServeOptions {
	if opts, ok := ctx.Value(playServeOptionsKey).(PlayServeOptions); ok {
		return opts
	}
	return PlayServeOptions{}
}

// ServePlay serves one playback slot the way handlePlay does for Stremio,
// with the request answered per opts wherever it cannot be served as asked.
func (s *Server) ServePlay(w http.ResponseWriter, r *http.Request, stream *auth.Stream, slotPath string, opts PlayServeOptions) {
	r = r.WithContext(context.WithValue(r.Context(), playServeOptionsKey, opts))
	s.servePlaySlot(w, r, stream, slotPath)
}

// SlotPathFor is the play path of one candidate of a content id, for a
// stream. Slot paths are opaque to callers; this and SlotIndexOf are the only
// ways in and out of them.
func SlotPathFor(stream *auth.Stream, contentType, id string, index int) string {
	return StreamSlotKey{StreamID: streamID(stream), ContentType: contentType, ID: id}.SlotPath(index)
}

// SlotIndexOf reads the candidate index back out of a slot path.
func SlotIndexOf(slotPath string) (int, bool) {
	_, _, _, index, ok := parseStreamSlotID(slotPath)
	return index, ok
}

// EnabledCatalogs lists the browse catalogs the stream's profile enables, in
// the profile's order.
func (s *Server) EnabledCatalogs(stream *auth.Stream) ([]CatalogDef, error) {
	profile := s.metadataProfileFor(stream)
	if profile == nil {
		return nil, ErrMetadataDisabled
	}
	return enabledCatalogDefs(profile), nil
}

// SearchCatalogs lists the hidden search carriers, one per content type. They
// answer for every profile and only with a query.
func SearchCatalogs() []CatalogDef {
	out := make([]CatalogDef, len(searchCatalogs))
	copy(out, searchCatalogs)
	return out
}

// Catalog serves one page of a catalog under the same rules as the HTTP
// handler: an unknown, disabled or misused catalog is a not-found error, and
// an upstream failure is an empty page.
func (s *Server) Catalog(ctx context.Context, stream *auth.Stream, catalogID, contentType, search string, skip int) ([]MetaPreview, error) {
	profile := s.metadataProfileFor(stream)
	if profile == nil {
		return nil, ErrMetadataDisabled
	}
	req := catalogRequest{
		Type:       contentType,
		ID:         catalogID,
		Search:     strings.TrimSpace(search),
		Skip:       max(skip, 0),
		StreamName: streamID(stream),
		Profile:    profile,
	}
	def, ok := resolveCatalogDef(profile, req)
	if !ok {
		return nil, ErrCatalogNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, catalogRequestTimeout)
	defer cancel()
	return s.serveCatalog(ctx, def, req), nil
}

// ErrCatalogNotFound is returned by Catalog for an id the stream's profile
// cannot read: unknown, disabled, or a search carrier asked without a query.
var ErrCatalogNotFound = errors.New("stremio: catalog not found")

// CatalogPageSize is how many rows one Catalog page holds; the next page
// starts at skip + CatalogPageSize.
const CatalogPageSize = catalogPageSize

// Meta builds the meta object for a content id, as the HTTP handler would.
func (s *Server) Meta(ctx context.Context, stream *auth.Stream, contentType, id string) (*MetaObject, error) {
	profile := s.metadataProfileFor(stream)
	if profile == nil {
		return nil, ErrMetadataDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, metaRequestTimeout)
	defer cancel()
	return s.buildMeta(ctx, profile, contentType, id)
}

// RecentPlayed lists the distinct titles the stream successfully played,
// newest first, as id-and-title stubs. Episodes collapse to their series.
func (s *Server) RecentPlayed(stream *auth.Stream, contentType string, limit int) []MetaPreview {
	previews := s.recentPlayedPreviews(streamID(stream), contentType)
	if limit > 0 && len(previews) > limit {
		previews = previews[:limit]
	}
	return previews
}

// PlaylistEntry is one playable candidate of a content id.
type PlaylistEntry struct {
	Index    int
	SlotPath string
	Title    string
	Indexer  string
	Size     int64
	Score    int
	// Available reports the community availability database vouches for
	// the release.
	Available bool
	// Caps is what ffprobe measured, known only for releases that played
	// before (library rows); nil for fresh indexer results.
	Caps *release.MediaCaps
	// DisplayTitle is Title put through the stream's result-name template,
	// flattened to one line. Empty when the stream has no custom format, so a
	// caller falls back to Title rather than to a blank label.
	//
	// It is rendered here because the template needs the whole triage
	// candidate — score, verdict, rule matches, probe results — and a
	// PlaylistEntry deliberately keeps none of that.
	DisplayTitle string
}

// PlaylistView is the ranked candidate list for a content id — every
// candidate, regardless of the stream's results mode, which only shapes the
// Stremio response.
type PlaylistView struct {
	Entries []PlaylistEntry
	// ContentTitle is the resolved title of the movie or series.
	ContentTitle string
	// RuntimeSeconds is the metadata provider's runtime, 0 when unknown.
	RuntimeSeconds float64
}

// Playlist searches, ranks and returns the candidates for a content id the
// way a Stremio stream request does, including the top-candidate preload it
// kicks off. No candidates is an empty view, not an error.
func (s *Server) Playlist(ctx context.Context, stream *auth.Stream, contentType, id string) (*PlaylistView, error) {
	const playlistRequestTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(ctx, playlistRequestTimeout)
	defer cancel()
	key := StreamSlotKey{StreamID: streamID(stream), ContentType: contentType, ID: id}
	list, err := s.bootstrapPlaylistForPlay(ctx, key, stream)
	if err != nil {
		if strings.Contains(err.Error(), "no candidates found") {
			return &PlaylistView{}, nil
		}
		return nil, err
	}
	go s.preloadTopPlaylistCandidates(key, list, stream)
	return s.playlistViewOf(list, key, stream), nil
}

// PlaylistCached returns the candidate list already built for a content id,
// without searching. It is what a play request can consult without paying
// for a search the stream request already ran.
func (s *Server) PlaylistCached(stream *auth.Stream, contentType, id string) (*PlaylistView, bool) {
	key := StreamSlotKey{StreamID: streamID(stream), ContentType: contentType, ID: id}
	v, ok := s.playlistCache.Load(key.CacheKey())
	if !ok {
		return nil, false
	}
	ent, _ := v.(*playlistCacheEntry)
	if ent == nil || ent.result == nil || !time.Now().Before(ent.until) {
		return nil, false
	}
	list := ent.live
	if list == nil {
		list = ent.result
	}
	return s.playlistViewOf(list, key, stream), true
}

func (s *Server) playlistViewOf(list *playlistResult, key StreamSlotKey, stream *auth.Stream) *PlaylistView {
	view := &PlaylistView{}
	if list == nil {
		return view
	}
	if list.Params != nil {
		view.ContentTitle = list.Params.ContentTitle
		view.RuntimeSeconds = query.ContentRuntimeSeconds(list.Params.Metadata, list.Params.ContentType)
	}
	// Only the name half is rendered: a description template is a multi-line
	// detail card, and every consumer of a view labels a row with one string.
	var format *resultFormat
	if f := s.resultFormatForStream(stream); f != nil && f.name != nil {
		format = f
	}
	topScore := 0
	if format != nil {
		for _, cand := range list.Candidates {
			topScore = max(topScore, cand.Score)
		}
	}
	service := ServiceName(stream)
	useSlotPaths := len(list.SlotPaths) == len(list.Candidates)
	view.Entries = make([]PlaylistEntry, 0, len(list.Candidates))
	for i, cand := range list.Candidates {
		entry := PlaylistEntry{Index: i, SlotPath: key.SlotPath(i), Score: cand.Score}
		if useSlotPaths {
			entry.SlotPath = list.SlotPaths[i]
		}
		if rel := cand.Release; rel != nil {
			entry.Title = rel.Title
			entry.Indexer = indexerNameFromRelease(rel)
			entry.Size = rel.Size
			entry.Available = list.CachedAvailable != nil && rel.DetailsURL != "" && list.CachedAvailable[rel.DetailsURL]
			entry.Caps = libraryCapsForRelease(rel)
		}
		if format != nil {
			ctx := newFormatContext(cand, i+1, len(list.Candidates), topScore, service, key.StreamID,
				view.ContentTitle, capsSummaryLine(cand.Release), entry.Available, view.RuntimeSeconds)
			// An empty fallback keeps a failed or blank render out of the
			// label, so the caller falls back to the release title.
			entry.DisplayTitle = singleLineLabel(renderResultTemplate(format.name, ctx, ""))
		}
		view.Entries = append(view.Entries, entry)
	}
	return view
}
