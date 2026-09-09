package jellyfin

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

// Images are relayed, not redirected. Jellyfin clients build image URLs from
// an item's ImageTags and fetch /Items/{id}/Images/{type}?tag=..., and the
// answer here fetches the provider's CDN — the same URL the Stremio meta
// hands out — and streams the bytes back, because some clients (Infuse on
// tvOS/macOS) do not follow redirects for artwork and are left with a blank
// image on a 302. The tag is a hash of the upstream URL, so the layer can
// answer a tag it has seen without touching metadata, and one it has not by
// resolving the item again: the map is a fast path, not a source of truth.

const imageTagCap = 50_000

type imageTags struct {
	mu   sync.Mutex
	urls map[string]string
}

func newImageTags() *imageTags {
	return &imageTags{urls: make(map[string]string)}
}

// tagFor is the tag of a URL; "" for no URL.
func (t *imageTags) tagFor(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	h := sha1.Sum([]byte(url))
	return hex.EncodeToString(h[:10])
}

// register remembers a URL under its tag and returns the tag.
func (t *imageTags) register(url string) string {
	tag := t.tagFor(url)
	if tag == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.urls) >= imageTagCap {
		// A full map is dropped rather than evicted: misses re-resolve, and
		// a client only ever asks for tags it was just handed.
		t.urls = make(map[string]string)
	}
	t.urls[tag] = strings.TrimSpace(url)
	return tag
}

func (t *imageTags) urlFor(tag string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	url, ok := t.urls[tag]
	return url, ok
}

// serveImages answers /Items/{id}/Images/{type}[/{index}].
func (s *Server) serveImages(w http.ResponseWriter, rq *request) bool {
	if !rq.is(http.MethodGet) && !rq.is(http.MethodHead) {
		return false
	}
	var rawID, kind string
	if c, ok := rq.match("items", "*", "images", "*"); ok {
		rawID, kind = c[0], c[1]
	} else if c, ok := rq.match("items", "*", "images", "*", "*"); ok {
		rawID, kind = c[0], c[1]
	} else if _, ok := rq.match("users", "*", "images", "*"); ok {
		http.NotFound(w, rq.Request)
		return true
	} else {
		return false
	}
	if tag := rq.param("tag"); tag != "" {
		if url, ok := s.images.urlFor(tag); ok {
			s.relayImage(w, rq, url, kind)
			return true
		}
	}
	url := s.resolveImage(rq, rawID, kind)
	if url == "" {
		http.NotFound(w, rq.Request)
		return true
	}
	s.relayImage(w, rq, url, kind)
	return true
}

// resolveImage finds an item's image of a kind from its metadata, for a tag
// the map no longer holds (restart, eviction, another instance).
func (s *Server) resolveImage(rq *request, rawID, kind string) string {
	id, err := decodeItemID(rawID)
	if err != nil || id.Kind == kindView {
		return ""
	}
	// Resolving reads metadata, which is per-stream. An image request that
	// carried no token has the tag path and nothing else.
	if rq.stream == nil {
		return ""
	}
	meta, err := s.opts.Catalog.Meta(rq.Context(), rq.stream, id.ContentType, id.baseStremioID())
	if err != nil || meta == nil {
		logger.Debug("Jellyfin image resolve failed", "item", rawID, "err", err)
		return ""
	}
	switch kind {
	case "primary", "thumb":
		if id.Kind == kindEpisode {
			for _, v := range videosOf(meta) {
				if v.Season == id.Season && v.Episode == id.Episode && v.Thumbnail != "" {
					return v.Thumbnail
				}
			}
		}
		return meta.Poster
	case "backdrop":
		return meta.Background
	case "logo":
		return meta.Logo
	}
	return ""
}

// imageClient fetches provider images to relay. The dial, handshake and
// response-header timeouts are short so a dead CDN fails fast; the overall
// timeout is longer to cover a large backdrop over a slow upstream.
var imageClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   8,
	},
}

// maxImageBody caps a relayed image, upstream Content-Length and all: this
// layer never has to hold more than a poster in flight, and a CDN promising
// more is answered with a 502 rather than trusted to actually send that much.
const maxImageBody = 20 << 20 // 20 MiB

// relayImage fetches url and streams it back as the response, instead of
// redirecting the client to it: Infuse and some other clients ignore a 302
// on an artwork request and are left with a blank image.
func (s *Server) relayImage(w http.ResponseWriter, rq *request, rawURL, kind string) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		// Nothing here to fetch on the client's behalf; the redirect is the
		// only path left for a scheme this layer cannot relay.
		http.Redirect(w, rq.Request, rawURL, http.StatusFound)
		return
	}
	target := rewriteImageSize(rawURL, kind)
	// A GET is sent even for a HEAD request: some CDNs answer HEAD with the
	// wrong content type, or refuse it outright.
	req, err := http.NewRequestWithContext(rq.Context(), http.MethodGet, target, nil)
	if err != nil {
		logger.Debug("Jellyfin image relay failed", "url", target, "err", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	resp, err := imageClient.Do(req)
	if err != nil {
		// Including a request the client abandoned: its context is what was
		// passed above, so the fetch dies with it instead of running to
		// completion for no one.
		logger.Debug("Jellyfin image relay failed", "url", target, "err", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Debug("Jellyfin image relay failed", "url", target, "status", resp.StatusCode)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil && n > maxImageBody {
			// Refused before a header is written: a Content-Length promising
			// more than the cap would otherwise reach the client alongside a
			// body truncated well short of it.
			logger.Debug("Jellyfin image relay refused", "url", target, "length", n)
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		w.Header().Set("ETag", etag)
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		w.Header().Set("Last-Modified", lm)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return
	}
	written, _ := io.Copy(w, io.LimitReader(resp.Body, maxImageBody)) // write errors ignored: the client went away
	if written == maxImageBody {
		logger.Debug("Jellyfin image relay body truncated", "url", target, "limit", maxImageBody)
	}
}

// rewriteImageSize points a TMDB image at a size proportional to how large
// clients render it, rather than the "original" full-resolution file the
// catalog hands out: relaying a multi-megabyte poster for a thumbnail wastes
// bandwidth neither side needs. Any other host is passed through unchanged.
// There are no person items in this layer, so no people size.
func rewriteImageSize(rawURL, kind string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host != "image.tmdb.org" {
		return rawURL
	}
	segs := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(segs) != 4 || segs[0] != "t" || segs[1] != "p" {
		return rawURL
	}
	var size string
	switch kind {
	case "backdrop":
		size = "w1280"
	case "primary", "thumb":
		size = "w780"
	default:
		return rawURL
	}
	segs[2] = size
	u.Path = "/" + strings.Join(segs, "/")
	return u.String()
}
