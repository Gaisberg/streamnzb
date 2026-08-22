// Package newznab serves StreamNZB's configured indexers as a single Newznab
// API, so any Newznab-compatible application can search the whole fan-out
// through one endpoint with one API key.
//
// Queries are proxied, not reinterpreted: the function and parameters a client
// sends reach each indexer as they arrived, and the merged results are handed
// back raw. Nothing here ranks, validates or filters — that is what the stream
// pipeline does for playback, and a client that parses release titles itself
// wants the unfiltered set.
package newznab

import (
	"crypto/subtle"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
)

// Mount is the path prefix the endpoint is served under, and APIPath the
// endpoint itself. A Newznab client is configured with the URL up to Mount and
// an "API path" of /api, which is the default in every client.
const (
	Mount   = "/newznab/"
	APIPath = "/newznab/api"

	serverTitle       = "StreamNZB"
	serverDescription = "StreamNZB aggregated Newznab API"

	xmlMediaType  = "application/xml; charset=utf-8"
	rssMediaType  = "application/rss+xml; charset=utf-8"
	jsonMediaType = "application/json; charset=utf-8"
)

// Newznab API error codes. Only the ones this endpoint can actually produce
// are listed.
const (
	errIncorrectCredentials = 100
	errMissingParameter     = 200
	errIncorrectParameter   = 201
	errNoSuchFunction       = 202
	errNoSuchItem           = 300
	errUnknownError         = 900
)

// Options wires the endpoint to the live server state. Every field is a
// function rather than a value because indexers, caps and config are all
// swapped out by config reloads — a captured pointer would go stale.
type Options struct {
	// Enabled reports whether the endpoint should answer at all. It is read
	// per request rather than at mount time, so the setting applies live. A
	// nil Enabled means always on.
	Enabled func() bool
	// Indexer returns the current aggregator to search.
	Indexer func() indexer.Indexer
	// Caps returns the capabilities fetched from each indexer, keyed by name.
	Caps func() map[string]*indexer.Caps
	// Config returns the live configuration.
	Config func() *config.Config
	// APIKey returns the endpoint's credential. Clients present it as the
	// apikey parameter, the one form every Newznab client speaks.
	APIKey func() string
	// GrabSecret keys the sealed download references handed to clients.
	GrabSecret func() string
	// Version is the running StreamNZB version, reported in caps.
	Version string
}

type Server struct {
	opts Options
}

func New(opts Options) *Server {
	return &Server{opts: opts}
}

// Handler serves the endpoint. Mount it at Mount.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.writeError(w, r, http.StatusMethodNotAllowed, errIncorrectParameter, "Method not allowed")
		return
	}
	// A disabled endpoint is not here at all, rather than here and refusing:
	// a client testing the URL should see the same thing it would see if the
	// feature had never been built.
	if !s.enabled() || !isAPIPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}

	query := r.URL.Query()
	if !s.authorized(r, query) {
		logger.Warn("Newznab request rejected", "reason", "invalid API key", "path", r.URL.Path, "remote", r.RemoteAddr)
		s.writeError(w, r, http.StatusUnauthorized, errIncorrectCredentials, "Incorrect user credentials")
		return
	}

	function := strings.ToLower(strings.TrimSpace(query.Get("t")))
	switch function {
	case "":
		s.writeError(w, r, http.StatusBadRequest, errMissingParameter, "Missing parameter (t)")
	case "caps":
		s.handleCaps(w, r)
	case "search", "tvsearch", "tv-search", "movie", "movie-search", "music", "book":
		s.handleSearch(w, r, normalizeFunction(function))
	case "get", "getnzb", "nzb":
		s.handleGet(w, r, s.currentIndexer())
	default:
		s.writeError(w, r, http.StatusBadRequest, errNoSuchFunction, "No such function ("+function+")")
	}
}

// normalizeFunction folds the aliases clients use onto the canonical function
// names the rest of the package switches on.
func normalizeFunction(function string) string {
	switch function {
	case "tv-search":
		return "tvsearch"
	case "movie-search":
		return "movie"
	default:
		return function
	}
}

// isAPIPath accepts the endpoint itself and the bare mount point, so a client
// configured with an empty API path still reaches it.
func isAPIPath(path string) bool {
	rest := strings.Trim(strings.TrimPrefix(path, strings.TrimSuffix(Mount, "/")), "/")
	return rest == "" || rest == "api"
}

// authorized reports whether the request presented the endpoint's API key.
// Newznab clients send it as the apikey query parameter; the header forms are
// accepted for the sake of scripted callers. An unset key authorizes nothing —
// an endpoint with no credential is one anybody who reaches the port can
// search, which is never what "no key configured" is meant to express.
func (s *Server) authorized(r *http.Request, query url.Values) bool {
	want := s.apiKey()
	if want == "" {
		return false
	}
	got := strings.TrimSpace(firstNonEmpty(
		query.Get("apikey"),
		query.Get("apiKey"),
		query.Get("api_key"),
		r.Header.Get("X-Api-Key"),
		bearerToken(r.Header.Get("Authorization")),
	))
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) apiKey() string {
	if s.opts.APIKey == nil {
		return ""
	}
	return strings.TrimSpace(s.opts.APIKey())
}

func bearerToken(header string) string {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// effectiveByIndexer resolves each configured indexer's search settings, so a
// proxied query still honours the per-indexer switches that describe what an
// indexer can do: id/text search support and content scope.
func (s *Server) effectiveByIndexer() map[string]*config.IndexerSearchConfig {
	cfg := s.currentConfig()
	if cfg == nil || len(cfg.Indexers) == 0 {
		return nil
	}
	out := make(map[string]*config.IndexerSearchConfig, len(cfg.Indexers))
	for i := range cfg.Indexers {
		ic := &cfg.Indexers[i]
		if ic.Enabled != nil && !*ic.Enabled {
			continue
		}
		effective := config.MergeIndexerSearch(ic, nil, cfg)
		if strings.EqualFold(ic.Type, "easynews") {
			// Easynews has no id search to offer; without this an id-only
			// query would reach it as a blank text search.
			disabled := true
			effective.DisableIdSearch = &disabled
		}
		out[ic.Name] = effective
	}
	return out
}

func (s *Server) enabled() bool {
	return s.opts.Enabled == nil || s.opts.Enabled()
}

func (s *Server) currentIndexer() indexer.Indexer {
	if s.opts.Indexer == nil {
		return nil
	}
	return s.opts.Indexer()
}

func (s *Server) currentConfig() *config.Config {
	if s.opts.Config == nil {
		return nil
	}
	return s.opts.Config()
}

// mergedCaps folds every indexer's capabilities into the single set this
// endpoint advertises.
func (s *Server) mergedCaps() *indexer.Caps {
	var perIndexer map[string]*indexer.Caps
	if s.opts.Caps != nil {
		perIndexer = s.opts.Caps()
	}
	return indexer.MergeCaps(perIndexer)
}

func (s *Server) grabSecret() string {
	if s.opts.GrabSecret == nil {
		return ""
	}
	return s.opts.GrabSecret()
}

func (s *Server) version() string {
	if v := strings.TrimSpace(s.opts.Version); v != "" {
		return v
	}
	return "1.0"
}

// baseURL is the origin clients should call back on for downloads. The
// configured addon base URL wins, because that is the one address known to be
// reachable from outside; the request's own host stands in when it is unset.
func (s *Server) baseURL(r *http.Request) string {
	if cfg := s.currentConfig(); cfg != nil {
		if base := strings.TrimRight(strings.TrimSpace(cfg.AddonBaseURL), "/"); base != "" {
			return base
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// selfURL renders the request back as an absolute URL, for the feed's
// atom:link.
func (s *Server) selfURL(r *http.Request) string {
	self := s.baseURL(r) + APIPath
	if raw := scrubbedQuery(r); raw != "" {
		self += "?" + raw
	}
	return self
}

// scrubbedQuery renders the request's query without the API key, so a feed
// never quotes the caller's credential back at it.
func scrubbedQuery(r *http.Request) string {
	query := r.URL.Query()
	for _, key := range []string{"apikey", "apiKey", "api_key"} {
		query.Del(key)
	}
	return query.Encode()
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status, code int, description string) {
	if wantsJSON(r) {
		writeJSON(w, status, map[string]any{
			"error": map[string]any{"code": code, "description": description},
		})
		return
	}
	writeXML(w, status, xmlMediaType, errorDocument{Code: code, Description: description})
}

type errorDocument struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

func wantsJSON(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("o")), "json")
}

func writeXML(w http.ResponseWriter, status int, mediaType string, doc any) {
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		logger.Error("Newznab response encode failed", "err", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return
	}
	if _, err := w.Write(body); err != nil {
		logger.Debug("Newznab response write failed", "err", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, doc any) {
	w.Header().Set("Content-Type", jsonMediaType)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		logger.Debug("Newznab response write failed", "err", err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
