// Package jellyfin serves a stream's catalogs, metadata and playback as a
// Jellyfin server, so Jellyfin clients — Swiftfin, Infuse, Findroid, the
// Android TV app — can use StreamNZB without a Stremio client in between.
//
// It is a translation layer, not a second server: every listing, detail page
// and play request maps onto what the Stremio addon already answers, through
// the facade in pkg/server/stremio. What Jellyfin calls a library is a
// catalog, a media source is a ranked release candidate, and the stream URL
// is the addon's /play/ slot with a different door. The layer keeps one thing
// of its own — playback progress — because Stremio clients keep theirs
// locally while Jellyfin clients expect the server to.
package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// Mount is the path prefix the server is served under. A Jellyfin client is
// pointed at the StreamNZB URL plus this prefix, e.g. https://host/jellyfin.
const Mount = "/jellyfin/"

const (
	// reportedVersion is the Jellyfin release the layer answers as. Clients
	// gate on it hard: the Kotlin SDK every Android client is built on refuses
	// a server below its minimumVersion, which is 12.0.0 since Jellyfin 12
	// shipped, and the check is a plain "version < minimum". Reporting less
	// means a client never gets past the handshake to find out what is
	// actually implemented here.
	//
	// Answering as 12.0 does not mean emulating it: what 12.0 removed — the
	// /emby route prefix, the legacy token headers — is still accepted here,
	// which only makes the layer more permissive than the version it claims.
	// A client that calls a 12.0 route this layer lacks gets a 404, logged as
	// an unhandled route.
	reportedVersion = "12.0.0"
	serverName      = "StreamNZB"
	jsonMediaType   = "application/json; charset=utf-8"
)

// Streams authenticates logins. *auth.StreamManager satisfies it.
type Streams interface {
	AuthenticateStream(username, password string) (*auth.Stream, error)
	AuthenticateToken(token, adminUsername, adminToken string) (*auth.Stream, error)
}

// Catalog is the slice of the Stremio addon the layer is built on.
// *stremio.Server satisfies it.
type Catalog interface {
	EnabledCatalogs(stream *auth.Stream) ([]stremio.CatalogDef, error)
	Catalog(ctx context.Context, stream *auth.Stream, catalogID, contentType, search string, skip int) ([]stremio.MetaPreview, error)
	Meta(ctx context.Context, stream *auth.Stream, contentType, id string) (*stremio.MetaObject, error)
	Playlist(ctx context.Context, stream *auth.Stream, contentType, id string) (*stremio.PlaylistView, error)
	PlaylistCached(stream *auth.Stream, contentType, id string) (*stremio.PlaylistView, bool)
	ServePlay(w http.ResponseWriter, r *http.Request, stream *auth.Stream, slotPath string, opts stremio.PlayServeOptions)
}

// Playstate persists playback progress. *persistence.JellyfinPlaystateStore
// satisfies it; a nil store is tolerated and simply remembers nothing.
type Playstate interface {
	Upsert(state persistence.JellyfinPlaystate) error
	Get(streamName, itemID string) (persistence.JellyfinPlaystate, bool)
	ListResume(streamName string, limit int) []persistence.JellyfinPlaystate
	ListForStream(streamName string) []persistence.JellyfinPlaystate
	SetPlayed(streamName, itemID, contentType, contentID string, played bool) error
}

// Options wires the server to the live process state. Config-derived fields
// are functions so a config reload reaches them without a rebind.
type Options struct {
	// Enabled reports whether the server answers at all; read per request. A
	// nil Enabled means always on.
	Enabled func() bool
	// ServerID is the stable id clients key their saved servers on.
	ServerID func() string
	// Admin returns the dashboard login: username, password hash and token.
	// The admin signs in with the dashboard credentials and is served as the
	// admin stream, the same as in the Stremio addon.
	Admin func() (username, passwordHash, token string)

	Streams   Streams
	Catalog   Catalog
	Playstate Playstate
	// Version is the StreamNZB version, reported alongside the Jellyfin one.
	Version string
}

// Server is the Jellyfin-compatible HTTP surface.
type Server struct {
	opts     Options
	images   *imageTags
	progress *progressDebounce
}

func New(opts Options) *Server {
	return &Server{opts: opts, images: newImageTags(), progress: newProgressDebounce()}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) enabled() bool {
	return s.opts.Enabled == nil || s.opts.Enabled()
}

func (s *Server) serverID() string {
	if s.opts.ServerID != nil {
		if id := strings.TrimSpace(s.opts.ServerID()); id != "" {
			return id
		}
	}
	return "streamnzb"
}

// request is one parsed request. Jellyfin paths and query names are
// case-insensitive, and clients use every casing there is, so segments and
// query keys are lower-cased once here.
type request struct {
	*http.Request
	segs   []string
	query  map[string]string
	stream *auth.Stream
}

func parseRequest(r *http.Request) *request {
	path := strings.TrimPrefix(r.URL.Path, Mount)
	var segs []string
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			segs = append(segs, strings.ToLower(seg))
		}
	}
	// Emby clients, and some Jellyfin ones on older code paths, prefix every
	// route with /emby.
	if len(segs) > 0 && segs[0] == "emby" {
		segs = segs[1:]
	}
	q := make(map[string]string)
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			q[strings.ToLower(key)] = values[0]
		}
	}
	return &request{Request: r, segs: segs, query: q}
}

func (rq *request) param(name string) string {
	return strings.TrimSpace(rq.query[strings.ToLower(name)])
}

func (rq *request) intParam(name string, def int) int {
	v := rq.param(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func (rq *request) boolParam(name string) bool {
	return strings.EqualFold(rq.param(name), "true")
}

// listParam reads a comma-separated parameter as lower-cased values.
func (rq *request) listParam(name string) []string {
	v := rq.param(name)
	if v == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// setParam writes a query parameter, dropping any parameter the client
// already sent under a different casing. Jellyfin query names are
// case-insensitive, so leaving both behind would make which one wins a
// matter of map order on the next request.
func setParam(q url.Values, name, value string) {
	for key := range q {
		if key != name && strings.EqualFold(key, name) {
			delete(q, key)
		}
	}
	q.Set(name, value)
}

func (rq *request) streamName() string {
	if rq.stream == nil {
		return ""
	}
	return rq.stream.Username
}

// match tests the path against a pattern where "*" captures one segment and
// returns the captures.
func (rq *request) match(pattern ...string) ([]string, bool) {
	if len(rq.segs) != len(pattern) {
		return nil, false
	}
	var captures []string
	for i, want := range pattern {
		switch {
		case want == "*":
			captures = append(captures, rq.segs[i])
		case want != rq.segs[i]:
			return nil, false
		}
	}
	return captures, true
}

// is tests the request method, answering HEAD wherever GET is served: a
// client checking a URL is reachable often asks for the headers alone, and
// net/http drops the body for us. Anything routed by GET is therefore
// routed by HEAD too.
func (rq *request) is(method string) bool {
	if method == http.MethodGet && rq.Method == http.MethodHead {
		return true
	}
	return rq.Method == method
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	// A disabled server is not here at all, rather than here and refusing:
	// a client testing the URL sees what it would see had the feature never
	// been built.
	if !s.enabled() {
		http.NotFound(w, r)
		return
	}
	rq := parseRequest(r)
	// Every request is logged while the layer is young: a client that cannot
	// connect is otherwise indistinguishable from one that never called, and
	// only the failures used to say anything at all.
	logger.Debug("Jellyfin request", "method", r.Method, "path", r.URL.Path, "agent", r.UserAgent(), "remote", r.RemoteAddr)
	if s.servePublic(w, rq) {
		return
	}
	// The login is resolved before routing but required only after it: images
	// are anonymous on a real Jellyfin server, and clients load them with a
	// plain image loader that carries no token at all. Answering those 401
	// breaks every poster, and risks a client reading it as a dead session.
	if stream, ok := s.authenticate(rq); ok {
		rq.stream = stream
		rq.Request = r.WithContext(auth.ContextWithStream(r.Context(), stream))
	}
	if s.serveImages(w, rq) {
		return
	}
	// The video player signs in the same way: not at all. It is handed a URL
	// and fetches it bare, so the stream route resolves its stream from the
	// media source id in the query instead.
	if rq.stream == nil {
		if stream, ok := s.streamFromMediaSource(rq); ok {
			rq.stream = stream
			rq.Request = r.WithContext(auth.ContextWithStream(r.Context(), stream))
		}
	}
	if rq.stream == nil {
		logger.Debug("Jellyfin request rejected", "reason", "no valid credentials",
			"method", r.Method, "path", r.URL.Path, "client", clientOf(rq).Client,
			"agent", r.UserAgent(), "remote", r.RemoteAddr)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if s.serveItems(w, rq) || s.serveShows(w, rq) || s.servePlayback(w, rq) || s.serveUser(w, rq) || s.serveStubs(w, rq) {
		return
	}
	logger.Debug("Jellyfin route not handled", "method", r.Method, "path", r.URL.Path, "agent", r.UserAgent())
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, status int, doc any) {
	w.Header().Set("Content-Type", jsonMediaType)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		logger.Debug("Jellyfin response write failed", "err", err)
	}
}

// writeEmpty answers the routes a client calls and never reads: capability
// reports, pings, logout.
func writeEmpty(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
